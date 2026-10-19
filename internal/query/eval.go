// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package query

import (
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/just-very-quickly/query/internal/jv"
)

// emitFn receives one output. Returning an error stops the filter.
type emitFn func(any) error

// filter runs on one input and emits its outputs, in order.
type filter func(e *env, in any, out emitFn) error

// env is the chain of variable bindings.
type env struct {
	name string
	val  any
	next *env
}

func (e *env) bind(name string, v any) *env { return &env{name, v, e} }

func (e *env) lookup(name string) (any, bool) {
	for ; e != nil; e = e.next {
		if e.name == name {
			return e.val, true
		}
	}
	return nil, false
}

// RuntimeError is an error raised while running a filter, such as indexing
// a string with a field name or error("...").
type RuntimeError struct {
	Value any // a string message, or the value given to error()
}

func (e *RuntimeError) Error() string {
	if s, ok := e.Value.(string); ok {
		return s
	}
	if e.Value == nil {
		return "null (null) not a string"
	}
	return string(jv.Encode(nil, e.Value, jv.Format{Compact: true})) + " (not a string)"
}

func errorf(format string, args ...any) error {
	return &RuntimeError{fmt.Sprintf(format, args...)}
}

// breakErr stops a generator early (limit, first, isempty...). It is never
// caught by try.
type breakErr struct{ id *int }

func (b *breakErr) Error() string { return "break" }

func newBreak() *breakErr { return &breakErr{new(int)} }

// describe writes a value the way jq does in error messages: its type and
// a shortened dump, such as: string ("abc").
func describe(v any) string {
	s := string(jv.Encode(nil, v, jv.Format{Compact: true}))
	if len(s) > 11 {
		cut := 10
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "..."
	}
	return fmt.Sprintf("%s (%s)", jv.TypeName(v), s)
}

// compiler turns the syntax tree into filters.
type compiler struct {
	vars        map[string]int  // variables in scope (count of bindings)
	globalNames map[string]bool // variables given on the command line
	agg         *aggCollector   // set while compiling the aggregation step
}

func (c *compiler) withVar(name string, f func() (filter, error)) (filter, error) {
	c.vars[name]++
	defer func() { c.vars[name]-- }()
	return f()
}

func (c *compiler) compile(n node) (filter, error) {
	switch x := n.(type) {
	case nIdentity:
		return func(_ *env, in any, out emitFn) error { return out(in) }, nil
	case nRecurse:
		return recurseFilter(nil), nil
	case nLiteral:
		v := x.v
		return func(_ *env, _ any, out emitFn) error { return out(v) }, nil
	case nField:
		return c.compileField(x)
	case nIndex:
		target, err := c.compile(x.target)
		if err != nil {
			return nil, err
		}
		index, err := c.compile(x.index)
		if err != nil {
			return nil, err
		}
		opt := x.opt
		return func(e *env, in any, out emitFn) error {
			return target(e, in, func(t any) error {
				var downstream error
				err := index(e, in, func(k any) error {
					v, err := indexValue(t, k)
					if err != nil {
						return err
					}
					if err := out(v); err != nil {
						downstream = err
						return err
					}
					return nil
				})
				if err != nil && err != downstream {
					return skipOpt(opt, err)
				}
				return err
			})
		}, nil
	case nSlice:
		return c.compileSlice(x)
	case nIterate:
		target, err := c.compile(x.target)
		if err != nil {
			return nil, err
		}
		opt := x.opt
		return func(e *env, in any, out emitFn) error {
			return target(e, in, func(t any) error {
				if opt {
					k := jv.KindOf(t)
					if k != jv.KArray && k != jv.KObject {
						return nil
					}
				}
				return iterate(t, out)
			})
		}, nil
	case nTry:
		return c.compileTry(x)
	case nPipe:
		l, err := c.compile(x.left)
		if err != nil {
			return nil, err
		}
		r, err := c.compile(x.right)
		if err != nil {
			return nil, err
		}
		return func(e *env, in any, out emitFn) error {
			return l(e, in, func(v any) error { return r(e, v, out) })
		}, nil
	case nComma:
		l, err := c.compile(x.left)
		if err != nil {
			return nil, err
		}
		r, err := c.compile(x.right)
		if err != nil {
			return nil, err
		}
		return func(e *env, in any, out emitFn) error {
			if err := l(e, in, out); err != nil {
				return err
			}
			return r(e, in, out)
		}, nil
	case nAlt:
		return c.compileAlt(x)
	case nAnd, nOr:
		var l, r node
		isAnd := false
		if a, ok := x.(nAnd); ok {
			l, r, isAnd = a.left, a.right, true
		} else {
			o := x.(nOr)
			l, r = o.left, o.right
		}
		lf, err := c.compile(l)
		if err != nil {
			return nil, err
		}
		rf, err := c.compile(r)
		if err != nil {
			return nil, err
		}
		return func(e *env, in any, out emitFn) error {
			return lf(e, in, func(a any) error {
				ta := jv.Truthy(a)
				if isAnd && !ta {
					return out(false)
				}
				if !isAnd && ta {
					return out(true)
				}
				return rf(e, in, func(b any) error { return out(jv.Truthy(b)) })
			})
		}, nil
	case nBinop:
		return c.compileBinop(x)
	case nNeg:
		f, err := c.compile(x.x)
		if err != nil {
			return nil, err
		}
		return func(e *env, in any, out emitFn) error {
			return f(e, in, func(v any) error {
				n, ok := jv.AsNumber(v)
				if !ok {
					return errorf("%s cannot be negated", describe(v))
				}
				return out(jv.Num(-n.F))
			})
		}, nil
	case nString:
		return c.compileString(x)
	case nFormat:
		fm, err := formatter(x.name)
		if err != nil {
			return nil, err
		}
		return func(_ *env, in any, out emitFn) error {
			s, err := fm(in)
			if err != nil {
				return err
			}
			return out(s)
		}, nil
	case nArray:
		if x.body == nil {
			return func(_ *env, _ any, out emitFn) error { return out([]any{}) }, nil
		}
		body, err := c.compile(x.body)
		if err != nil {
			return nil, err
		}
		return func(e *env, in any, out emitFn) error {
			arr := []any{}
			if err := body(e, in, func(v any) error { arr = append(arr, v); return nil }); err != nil {
				return err
			}
			return out(arr)
		}, nil
	case nObject:
		return c.compileObject(x)
	case nIf:
		cond, err := c.compile(x.cond)
		if err != nil {
			return nil, err
		}
		then, err := c.compile(x.then)
		if err != nil {
			return nil, err
		}
		var els filter
		if x.els != nil {
			if els, err = c.compile(x.els); err != nil {
				return nil, err
			}
		}
		return func(e *env, in any, out emitFn) error {
			return cond(e, in, func(v any) error {
				if jv.Truthy(v) {
					return then(e, in, out)
				}
				if els == nil {
					return out(in)
				}
				return els(e, in, out)
			})
		}, nil
	case nReduce:
		return c.compileReduce(x)
	case nForeach:
		return c.compileForeach(x)
	case nBind:
		src, err := c.compile(x.source)
		if err != nil {
			return nil, err
		}
		body, err := c.withVar(x.name, func() (filter, error) { return c.compile(x.body) })
		if err != nil {
			return nil, err
		}
		name := x.name
		return func(e *env, in any, out emitFn) error {
			return src(e, in, func(v any) error { return body(e.bind(name, v), in, out) })
		}, nil
	case nVar:
		name := x.name
		if name == "ENV" {
			return func(_ *env, _ any, out emitFn) error { return out(envObject()) }, nil
		}
		if c.vars[name] == 0 {
			return nil, &SyntaxError{0, fmt.Sprintf("$%s is not defined", name)}
		}
		return func(e *env, _ any, out emitFn) error {
			v, _ := e.lookup(name)
			return out(v)
		}, nil
	case nCall:
		return c.compileCall(x)
	case nAssign:
		return c.compileAssign(x)
	}
	return nil, fmt.Errorf("internal error: unknown node %T", n)
}

// skipOpt drops a runtime error when the step is optional (a trailing ?).
func skipOpt(opt bool, err error) error {
	if opt && isRuntime(err) {
		return nil
	}
	return err
}

func (c *compiler) compileField(x nField) (filter, error) {
	name := x.name
	opt := x.opt
	if _, ok := x.target.(nIdentity); ok {
		return func(_ *env, in any, out emitFn) error {
			v, err := field(in, name)
			if err != nil {
				return skipOpt(opt, err)
			}
			return out(v)
		}, nil
	}
	if singleValued(x.target) {
		g, err := c.compileGetter(x.target)
		if err != nil {
			return nil, err
		}
		return func(e *env, in any, out emitFn) error {
			t, err := g(e, in)
			if err != nil {
				return err
			}
			v, err := field(t, name)
			if err != nil {
				return skipOpt(opt, err)
			}
			return out(v)
		}, nil
	}
	target, err := c.compile(x.target)
	if err != nil {
		return nil, err
	}
	return func(e *env, in any, out emitFn) error {
		return target(e, in, func(t any) error {
			v, err := field(t, name)
			if err != nil {
				return skipOpt(opt, err)
			}
			return out(v)
		})
	}, nil
}

// field is .name on v.
func field(v any, name string) (any, error) {
	switch x := v.(type) {
	case jv.Raw:
		if x[0] == '{' {
			if f, ok := jv.Field(x, name); ok {
				return f, nil
			}
			return nil, nil
		}
		if x[0] == 'n' {
			return nil, nil
		}
	case *jv.Doc:
		if x.Raw[0] == '{' {
			if f, ok := x.Field(name); ok {
				return f, nil
			}
			return nil, nil
		}
		if x.Raw[0] == 'n' {
			return nil, nil
		}
	case *jv.Object:
		f, _ := x.Get(name)
		return f, nil
	case nil:
		return nil, nil
	}
	return nil, errorf("Cannot index %s with string \"%s\"", jv.TypeName(v), name)
}

// indexValue is t[k].
func indexValue(t, k any) (any, error) {
	switch jv.KindOf(k) {
	case jv.KString:
		return field(t, jv.Str(k))
	case jv.KNumber:
		n, _ := jv.AsNumber(k)
		switch tk := jv.KindOf(t); tk {
		case jv.KNull:
			return nil, nil
		case jv.KArray:
			i := math.Floor(n.F)
			if r, ok := jv.RawOf(t); ok {
				if i < 0 {
					i += float64(jv.Len(r))
				}
				if i < 0 || i > math.MaxInt32 {
					return nil, nil
				}
				e, ok := jv.Index(r, int(i))
				if !ok {
					return nil, nil
				}
				return e, nil
			}
			a := t.([]any)
			if i < 0 {
				i += float64(len(a))
			}
			if i < 0 || i >= float64(len(a)) {
				return nil, nil
			}
			return a[int(i)], nil
		}
		return nil, errorf("Cannot index %s with number", jv.TypeName(t))
	case jv.KObject:
		if jv.KindOf(t) == jv.KNull {
			return nil, nil
		}
		if jv.KindOf(t) == jv.KArray {
			return slice(t, k)
		}
	case jv.KArray:
		if jv.KindOf(t) == jv.KArray {
			return indicesOf(t, k)
		}
		if jv.KindOf(t) == jv.KNull {
			return nil, nil
		}
	case jv.KNull:
		if jv.KindOf(t) == jv.KObject || jv.KindOf(t) == jv.KNull {
			return nil, nil
		}
	}
	return nil, errorf("Cannot index %s with %s", jv.TypeName(t), jv.TypeName(k))
}

// slice handles .[{"start":..,"end":..}], which is how jq paths spell slices.
func slice(t, k any) (any, error) {
	o := jv.Norm(k).(*jv.Object)
	from, _ := o.Get("start")
	to, _ := o.Get("end")
	return sliceValue(t, from, to)
}

func iterate(t any, out emitFn) error {
	switch x := t.(type) {
	case jv.Raw:
		if x[0] == '[' || x[0] == '{' {
			return jv.Each(x, func(_ jv.Raw, v jv.Raw) error { return out(v) })
		}
	case *jv.Doc:
		if x.Raw[0] == '[' || x.Raw[0] == '{' {
			return jv.Each(x.Raw, func(_ jv.Raw, v jv.Raw) error { return out(v) })
		}
	case []any:
		for _, v := range x {
			if err := out(v); err != nil {
				return err
			}
		}
		return nil
	case *jv.Object:
		for _, v := range x.Vals {
			if err := out(v); err != nil {
				return err
			}
		}
		return nil
	}
	return errorf("Cannot iterate over %s", describe(t))
}

func (c *compiler) compileSlice(x nSlice) (filter, error) {
	target, err := c.compile(x.target)
	if err != nil {
		return nil, err
	}
	var from, to filter
	if x.from != nil {
		if from, err = c.compile(x.from); err != nil {
			return nil, err
		}
	}
	if x.to != nil {
		if to, err = c.compile(x.to); err != nil {
			return nil, err
		}
	}
	nullf := func(_ *env, _ any, out emitFn) error { return out(nil) }
	if from == nil {
		from = nullf
	}
	if to == nil {
		to = nullf
	}
	opt := x.opt
	return func(e *env, in any, out emitFn) error {
		return target(e, in, func(t any) error {
			return to(e, in, func(b any) error {
				return from(e, in, func(a any) error {
					v, err := sliceValue(t, a, b)
					if err != nil {
						return skipOpt(opt, err)
					}
					return out(v)
				})
			})
		})
	}, nil
}

// sliceBounds works out jq's slice indices for length n.
func sliceBounds(n int, from, to any) (int, int, error) {
	start, end := 0.0, float64(n)
	if from != nil {
		f, ok := jv.AsNumber(from)
		if !ok {
			return 0, 0, errorf("Start and end indices of an array slice must be numbers")
		}
		start = f.F
	}
	if to != nil {
		t, ok := jv.AsNumber(to)
		if !ok {
			return 0, 0, errorf("Start and end indices of an array slice must be numbers")
		}
		end = t.F
	}
	ln := float64(n)
	if start < 0 {
		start += ln
	}
	if end < 0 {
		end += ln
	}
	if start < 0 {
		start = 0
	}
	if start > ln {
		start = ln
	}
	if end > ln {
		end = ln
	}
	if end < 0 {
		end = 0
	}
	s := int(start)
	e := int(end)
	if float64(e) < end {
		e++
	}
	if e > n {
		e = n
	}
	if e < s {
		e = s
	}
	return s, e, nil
}

func sliceValue(t, from, to any) (any, error) {
	switch jv.KindOf(t) {
	case jv.KNull:
		return nil, nil
	case jv.KString:
		rs := []rune(jv.Str(t))
		s, e, err := sliceBounds(len(rs), from, to)
		if err != nil {
			return nil, err
		}
		return string(rs[s:e]), nil
	case jv.KArray:
		a := jv.Norm(t).([]any)
		s, e, err := sliceBounds(len(a), from, to)
		if err != nil {
			return nil, err
		}
		return a[s:e:e], nil
	}
	return nil, errorf("Cannot index %s with object", jv.TypeName(t))
}

func (c *compiler) compileTry(x nTry) (filter, error) {
	body, err := c.compile(x.body)
	if err != nil {
		return nil, err
	}
	var catch filter
	if x.catch != nil {
		if catch, err = c.compile(x.catch); err != nil {
			return nil, err
		}
	}
	return func(e *env, in any, out emitFn) error {
		// Errors raised by out (later in the pipeline) must pass through.
		var downstream error
		err := body(e, in, func(v any) error {
			if err := out(v); err != nil {
				downstream = err
				return err
			}
			return nil
		})
		if err == nil || err == downstream {
			return err
		}
		re, ok := err.(*RuntimeError)
		if !ok {
			return err // breaks and internal errors
		}
		if catch == nil {
			return nil
		}
		return catch(e, re.Value, out)
	}, nil
}

func (c *compiler) compileAlt(x nAlt) (filter, error) {
	l, err := c.compile(x.left)
	if err != nil {
		return nil, err
	}
	r, err := c.compile(x.right)
	if err != nil {
		return nil, err
	}
	// As in jq 1.7, errors on the left side are not suppressed.
	return func(e *env, in any, out emitFn) error {
		found := false
		err := l(e, in, func(v any) error {
			if !jv.Truthy(v) {
				return nil
			}
			found = true
			return out(v)
		})
		if err != nil || found {
			return err
		}
		return r(e, in, out)
	}, nil
}

func (c *compiler) compileBinop(x nBinop) (filter, error) {
	l, err := c.compile(x.left)
	if err != nil {
		return nil, err
	}
	r, err := c.compile(x.right)
	if err != nil {
		return nil, err
	}
	op := x.op
	// Single values on both sides (select(.level == "error")) skip the
	// callbacks.
	if singleValued(x.left) && singleValued(x.right) {
		gl, err := c.compileGetter(x.left)
		if err != nil {
			return nil, err
		}
		gr, err := c.compileGetter(x.right)
		if err != nil {
			return nil, err
		}
		return func(e *env, in any, out emitFn) error {
			b, err := gr(e, in)
			if err != nil {
				return err
			}
			a, err := gl(e, in)
			if err != nil {
				return err
			}
			v, err := binop(op, a, b)
			if err != nil {
				return err
			}
			return out(v)
		}, nil
	}
	// jq evaluates the right side first and the left side inside it.
	return func(e *env, in any, out emitFn) error {
		return r(e, in, func(b any) error {
			return l(e, in, func(a any) error {
				v, err := binop(op, a, b)
				if err != nil {
					return err
				}
				return out(v)
			})
		})
	}, nil
}

func binop(op string, a, b any) (any, error) {
	switch op {
	case "==":
		return jv.Equal(a, b), nil
	case "!=":
		return !jv.Equal(a, b), nil
	case "<":
		return jv.Compare(a, b) < 0, nil
	case "<=":
		return jv.Compare(a, b) <= 0, nil
	case ">":
		return jv.Compare(a, b) > 0, nil
	case ">=":
		return jv.Compare(a, b) >= 0, nil
	case "+":
		return add(a, b)
	case "-":
		return subtract(a, b)
	case "*":
		return multiply(a, b)
	case "/":
		return divide(a, b)
	case "%":
		return modulo(a, b)
	}
	return nil, errorf("unknown operator %s", op)
}

func add(a, b any) (any, error) {
	ka, kb := jv.KindOf(a), jv.KindOf(b)
	switch {
	case ka == jv.KNull:
		return b, nil
	case kb == jv.KNull:
		return a, nil
	case ka == jv.KNumber && kb == jv.KNumber:
		x, _ := jv.AsNumber(a)
		y, _ := jv.AsNumber(b)
		return jv.Num(x.F + y.F), nil
	case ka == jv.KString && kb == jv.KString:
		return jv.Str(a) + jv.Str(b), nil
	case ka == jv.KArray && kb == jv.KArray:
		x, y := jv.Norm(a).([]any), jv.Norm(b).([]any)
		out := make([]any, 0, len(x)+len(y))
		return append(append(out, x...), y...), nil
	case ka == jv.KObject && kb == jv.KObject:
		x := jv.Norm(a).(*jv.Object).Copy()
		y := jv.Norm(b).(*jv.Object)
		for i, k := range y.Keys {
			x.Set(k, y.Vals[i])
		}
		return x, nil
	}
	return nil, errorf("%s and %s cannot be added", describe(a), describe(b))
}

func subtract(a, b any) (any, error) {
	ka, kb := jv.KindOf(a), jv.KindOf(b)
	switch {
	case ka == jv.KNumber && kb == jv.KNumber:
		x, _ := jv.AsNumber(a)
		y, _ := jv.AsNumber(b)
		return jv.Num(x.F - y.F), nil
	case ka == jv.KArray && kb == jv.KArray:
		x, y := jv.Norm(a).([]any), jv.Norm(b).([]any)
		out := []any{}
	outer:
		for _, v := range x {
			for _, w := range y {
				if jv.Equal(v, w) {
					continue outer
				}
			}
			out = append(out, v)
		}
		return out, nil
	}
	return nil, errorf("%s and %s cannot be subtracted", describe(a), describe(b))
}

func multiply(a, b any) (any, error) {
	ka, kb := jv.KindOf(a), jv.KindOf(b)
	switch {
	case ka == jv.KNumber && kb == jv.KNumber:
		x, _ := jv.AsNumber(a)
		y, _ := jv.AsNumber(b)
		return jv.Num(x.F * y.F), nil
	case ka == jv.KString && kb == jv.KNumber, ka == jv.KNumber && kb == jv.KString:
		s, n := a, b
		if ka == jv.KNumber {
			s, n = b, a
		}
		num, _ := jv.AsNumber(n)
		if num.F < 0 || math.IsNaN(num.F) {
			return nil, nil
		}
		reps := int(math.Min(num.F, math.MaxInt32))
		str := jv.Str(s)
		if reps > 1<<24 || len(str)*reps > 1<<30 {
			return nil, errorf("Repeat string result too long")
		}
		return strings.Repeat(str, reps), nil
	case ka == jv.KObject && kb == jv.KObject:
		return deepMerge(jv.Norm(a).(*jv.Object), jv.Norm(b).(*jv.Object)), nil
	}
	return nil, errorf("%s and %s cannot be multiplied", describe(a), describe(b))
}

func deepMerge(a, b *jv.Object) *jv.Object {
	out := a.Copy()
	for i, k := range b.Keys {
		bv := jv.Norm(b.Vals[i])
		if av, ok := out.Get(k); ok {
			ao, aok := jv.Norm(av).(*jv.Object)
			bo, bok := bv.(*jv.Object)
			if aok && bok {
				out.Set(k, deepMerge(ao, bo))
				continue
			}
		}
		out.Set(k, b.Vals[i])
	}
	return out
}

func divide(a, b any) (any, error) {
	ka, kb := jv.KindOf(a), jv.KindOf(b)
	switch {
	case ka == jv.KNumber && kb == jv.KNumber:
		x, _ := jv.AsNumber(a)
		y, _ := jv.AsNumber(b)
		if y.F == 0 {
			return nil, errorf("%s and %s cannot be divided because the divisor is zero", describe(a), describe(b))
		}
		return jv.Num(x.F / y.F), nil
	case ka == jv.KString && kb == jv.KString:
		return splitString(jv.Str(a), jv.Str(b)), nil
	}
	return nil, errorf("%s and %s cannot be divided", describe(a), describe(b))
}

func splitString(s, sep string) any {
	if s == "" {
		return []any{}
	}
	var parts []string
	if sep == "" {
		parts = nil
		for _, r := range s {
			parts = append(parts, string(r))
		}
	} else {
		parts = strings.Split(s, sep)
	}
	out := make([]any, len(parts))
	for i, p := range parts {
		out[i] = p
	}
	return out
}

func modulo(a, b any) (any, error) {
	ka, kb := jv.KindOf(a), jv.KindOf(b)
	if ka == jv.KNumber && kb == jv.KNumber {
		x, _ := jv.AsNumber(a)
		y, _ := jv.AsNumber(b)
		xi, yi := toInt(x.F), toInt(y.F)
		if yi == 0 {
			return nil, errorf("%s and %s cannot be divided because the divisor is zero", describe(a), describe(b))
		}
		if yi < 0 {
			yi = -yi
		}
		return jv.Num(float64(xi % yi)), nil
	}
	return nil, errorf("%s and %s cannot be divided", describe(a), describe(b))
}

func toInt(f float64) int64 {
	switch {
	case math.IsNaN(f):
		return 0
	case f >= math.MaxInt64:
		return math.MaxInt64
	case f <= math.MinInt64:
		return math.MinInt64
	}
	return int64(f)
}

func (c *compiler) compileString(x nString) (filter, error) {
	fm, err := formatter(x.format)
	if err != nil {
		return nil, err
	}
	parts := make([]filter, len(x.parts))
	lits := make([]*string, len(x.parts))
	for i, p := range x.parts {
		if l, ok := p.(nLiteral); ok {
			if s, ok := l.v.(string); ok {
				lits[i] = &s
				continue
			}
		}
		if parts[i], err = c.compile(p); err != nil {
			return nil, err
		}
	}
	// Later parts form the outer loop, as in jq.
	return func(e *env, in any, out emitFn) error {
		pieces := make([]string, len(parts))
		var run func(i int) error
		run = func(i int) error {
			if i < 0 {
				return out(strings.Join(pieces, ""))
			}
			if lits[i] != nil {
				pieces[i] = *lits[i]
				return run(i - 1)
			}
			return parts[i](e, in, func(v any) error {
				s, err := fm(v)
				if err != nil {
					return err
				}
				pieces[i] = s.(string)
				return run(i - 1)
			})
		}
		return run(len(parts) - 1)
	}, nil
}

func (c *compiler) compileObject(x nObject) (filter, error) {
	type entry struct{ key, value filter }
	entries := make([]entry, len(x.entries))
	for _, en := range x.entries {
		if lit, ok := en.key.(nLiteral); ok && jv.KindOf(lit.v) != jv.KString {
			return nil, &SyntaxError{0, fmt.Sprintf("Cannot use %s as object key", describe(lit.v))}
		}
	}
	for i, en := range x.entries {
		k, err := c.compile(en.key)
		if err != nil {
			return nil, err
		}
		v, err := c.compile(en.value)
		if err != nil {
			return nil, err
		}
		entries[i] = entry{k, v}
	}
	// Fast path: constant, distinct keys and values that each give exactly
	// one value, the common {ts, level, path: .request.path}.
	if keys, ok := constKeys(x); ok {
		single := true
		for _, en := range x.entries {
			if !singleValued(en.value) {
				single = false
			}
		}
		if single {
			gets := make([]getter, len(x.entries))
			for i, en := range x.entries {
				g, err := c.compileGetter(en.value)
				if err != nil {
					return nil, err
				}
				gets[i] = g
			}
			return func(e *env, in any, out emitFn) error {
				vals := make([]any, len(gets))
				for i, g := range gets {
					v, err := g(e, in)
					if err != nil {
						return err
					}
					vals[i] = v
				}
				return out(jv.ObjectOf(keys, vals))
			}, nil
		}
	}
	return func(e *env, in any, out emitFn) error {
		keys := make([]string, len(entries))
		vals := make([]any, len(entries))
		var run func(i int) error
		run = func(i int) error {
			if i == len(entries) {
				o := jv.NewObject(len(entries))
				for j := range keys {
					o.Set(keys[j], vals[j])
				}
				return out(o)
			}
			return entries[i].key(e, in, func(k any) error {
				if jv.KindOf(k) != jv.KString {
					return errorf("Cannot use %s as object key", describe(k))
				}
				keys[i] = jv.Str(k)
				return entries[i].value(e, in, func(v any) error {
					vals[i] = v
					return run(i + 1)
				})
			})
		}
		return run(0)
	}, nil
}

// getter evaluates a filter that always gives exactly one value (see
// singleValued) without the callback, which saves an allocation per call.
type getter func(e *env, in any) (any, error)

func (c *compiler) compileGetter(n node) (getter, error) {
	switch x := n.(type) {
	case nIdentity:
		return func(_ *env, in any) (any, error) { return in, nil }, nil
	case nLiteral:
		v := x.v
		return func(*env, any) (any, error) { return v, nil }, nil
	case nVar:
		f, err := c.compile(x)
		if err != nil {
			return nil, err
		}
		return func(e *env, in any) (any, error) {
			var v any
			err := f(e, in, func(w any) error { v = w; return nil })
			return v, err
		}, nil
	case nField:
		name := x.name
		if _, ok := x.target.(nIdentity); ok {
			return func(_ *env, in any) (any, error) { return field(in, name) }, nil
		}
		t, err := c.compileGetter(x.target)
		if err != nil {
			return nil, err
		}
		return func(e *env, in any) (any, error) {
			v, err := t(e, in)
			if err != nil {
				return nil, err
			}
			return field(v, name)
		}, nil
	case nIndex:
		t, err := c.compileGetter(x.target)
		if err != nil {
			return nil, err
		}
		k := x.index.(nLiteral).v
		return func(e *env, in any) (any, error) {
			v, err := t(e, in)
			if err != nil {
				return nil, err
			}
			return indexValue(v, k)
		}, nil
	}
	return nil, fmt.Errorf("internal error: no getter for %T", n)
}

// constKeys returns the keys of an object construction when they are all
// constant strings and distinct.
func constKeys(x nObject) ([]string, bool) {
	keys := make([]string, len(x.entries))
	seen := map[string]bool{}
	for i, en := range x.entries {
		lit, ok := en.key.(nLiteral)
		if !ok {
			return nil, false
		}
		k, ok := lit.v.(string)
		if !ok || seen[k] {
			return nil, false
		}
		seen[k] = true
		keys[i] = k
	}
	return keys, true
}

// singleValued reports whether n always gives exactly one value or an
// error: paths of plain fields, literals and variables.
func singleValued(n node) bool {
	switch x := n.(type) {
	case nIdentity, nLiteral, nVar:
		return true
	case nField:
		return !x.opt && singleValued(x.target)
	case nIndex:
		_, lit := x.index.(nLiteral)
		return !x.opt && lit && singleValued(x.target)
	}
	return false
}

func (c *compiler) compileReduce(x nReduce) (filter, error) {
	src, err := c.compile(x.source)
	if err != nil {
		return nil, err
	}
	init, err := c.compile(x.init)
	if err != nil {
		return nil, err
	}
	update, err := c.withVar(x.name, func() (filter, error) { return c.compile(x.update) })
	if err != nil {
		return nil, err
	}
	name := x.name
	return func(e *env, in any, out emitFn) error {
		return init(e, in, func(acc any) error {
			err := src(e, in, func(v any) error {
				var last any
				got := false
				if err := update(e.bind(name, v), acc, func(u any) error { last, got = u, true; return nil }); err != nil {
					return err
				}
				if got {
					acc = last
				} else {
					acc = nil
				}
				return nil
			})
			if err != nil {
				return err
			}
			return out(acc)
		})
	}, nil
}

func (c *compiler) compileForeach(x nForeach) (filter, error) {
	src, err := c.compile(x.source)
	if err != nil {
		return nil, err
	}
	init, err := c.compile(x.init)
	if err != nil {
		return nil, err
	}
	var update, extract filter
	_, err = c.withVar(x.name, func() (filter, error) {
		if update, err = c.compile(x.update); err != nil {
			return nil, err
		}
		if x.extract != nil {
			if extract, err = c.compile(x.extract); err != nil {
				return nil, err
			}
		}
		return nil, nil
	})
	if err != nil {
		return nil, err
	}
	name := x.name
	return func(e *env, in any, out emitFn) error {
		return init(e, in, func(state any) error {
			return src(e, in, func(v any) error {
				ev := e.bind(name, v)
				return update(ev, state, func(u any) error {
					state = u
					if extract == nil {
						return out(u)
					}
					return extract(ev, u, out)
				})
			})
		})
	}, nil
}

// recurseFilter is recurse(f), or recurse(.[]?) when f is nil.
func recurseFilter(f filter) filter {
	var rec func(e *env, v any, out emitFn) error
	rec = func(e *env, v any, out emitFn) error {
		if err := out(v); err != nil {
			return err
		}
		if f == nil {
			k := jv.KindOf(v)
			if k != jv.KArray && k != jv.KObject {
				return nil
			}
			return iterate(v, func(c any) error { return rec(e, c, out) })
		}
		return f(e, v, func(c any) error { return rec(e, c, out) })
	}
	return rec
}
