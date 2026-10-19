// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package query

import (
	"math"
	"sort"

	"github.com/just-very-quickly/query/internal/jv"
)

// A path expression (the left side of |=, the argument of path() and
// del()) is run in path mode: it yields where its values are, not just the
// values.

type pathItem struct {
	path []any
	val  any
}

type pathFilter func(e *env, cur pathItem, out func(pathItem) error) error

func appendPath(p []any, k any) []any {
	out := make([]any, len(p)+1)
	copy(out, p)
	out[len(p)] = k
	return out
}

func (c *compiler) compilePath(n node) (pathFilter, error) {
	switch x := n.(type) {
	case nIdentity:
		return func(_ *env, cur pathItem, out func(pathItem) error) error { return out(cur) }, nil
	case nRecurse:
		var rec func(cur pathItem, out func(pathItem) error) error
		rec = func(cur pathItem, out func(pathItem) error) error {
			if err := out(cur); err != nil {
				return err
			}
			return eachChild(cur, func(ch pathItem) error { return rec(ch, out) })
		}
		return func(_ *env, cur pathItem, out func(pathItem) error) error { return rec(cur, out) }, nil
	case nField:
		target, err := c.compilePath(x.target)
		if err != nil {
			return nil, err
		}
		name, opt := x.name, x.opt
		return func(e *env, cur pathItem, out func(pathItem) error) error {
			return target(e, cur, func(t pathItem) error {
				v, err := field(t.val, name)
				if err != nil {
					return skipOpt(opt, err)
				}
				return out(pathItem{appendPath(t.path, name), v})
			})
		}, nil
	case nIndex:
		target, err := c.compilePath(x.target)
		if err != nil {
			return nil, err
		}
		index, err := c.compile(x.index)
		if err != nil {
			return nil, err
		}
		opt := x.opt
		return func(e *env, cur pathItem, out func(pathItem) error) error {
			return target(e, cur, func(t pathItem) error {
				var downstream error
				err := index(e, cur.val, func(k any) error {
					v, err := indexValue(t.val, k)
					if err != nil {
						return err
					}
					if err := out(pathItem{appendPath(t.path, jv.Norm(k)), v}); err != nil {
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
		target, err := c.compilePath(x.target)
		if err != nil {
			return nil, err
		}
		nullf := func(_ *env, _ any, out emitFn) error { return out(nil) }
		from, to := filter(nullf), filter(nullf)
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
		return func(e *env, cur pathItem, out func(pathItem) error) error {
			return target(e, cur, func(t pathItem) error {
				return to(e, cur.val, func(b any) error {
					return from(e, cur.val, func(a any) error {
						v, err := sliceValue(t.val, a, b)
						if err != nil {
							return err
						}
						k := jv.NewObject(2)
						k.Set("start", jv.Norm(a))
						k.Set("end", jv.Norm(b))
						return out(pathItem{appendPath(t.path, k), v})
					})
				})
			})
		}, nil
	case nIterate:
		target, err := c.compilePath(x.target)
		if err != nil {
			return nil, err
		}
		return func(e *env, cur pathItem, out func(pathItem) error) error {
			return target(e, cur, func(t pathItem) error {
				k := jv.KindOf(t.val)
				if k != jv.KArray && k != jv.KObject {
					if x.opt {
						return nil
					}
					return errorf("Cannot iterate over %s", describe(t.val))
				}
				return eachChild(t, out)
			})
		}, nil
	case nPipe:
		l, err := c.compilePath(x.left)
		if err != nil {
			return nil, err
		}
		r, err := c.compilePath(x.right)
		if err != nil {
			return nil, err
		}
		return func(e *env, cur pathItem, out func(pathItem) error) error {
			return l(e, cur, func(t pathItem) error { return r(e, t, out) })
		}, nil
	case nComma:
		l, err := c.compilePath(x.left)
		if err != nil {
			return nil, err
		}
		r, err := c.compilePath(x.right)
		if err != nil {
			return nil, err
		}
		return func(e *env, cur pathItem, out func(pathItem) error) error {
			if err := l(e, cur, out); err != nil {
				return err
			}
			return r(e, cur, out)
		}, nil
	case nTry:
		body, err := c.compilePath(x.body)
		if err != nil {
			return nil, err
		}
		return func(e *env, cur pathItem, out func(pathItem) error) error {
			var downstream error
			err := body(e, cur, func(t pathItem) error {
				if err := out(t); err != nil {
					downstream = err
					return err
				}
				return nil
			})
			if err != nil && err != downstream {
				if _, ok := err.(*RuntimeError); ok {
					return nil
				}
			}
			return err
		}, nil
	case nIf:
		cond, err := c.compile(x.cond)
		if err != nil {
			return nil, err
		}
		then, err := c.compilePath(x.then)
		if err != nil {
			return nil, err
		}
		els := pathFilter(func(_ *env, cur pathItem, out func(pathItem) error) error { return out(cur) })
		if x.els != nil {
			if els, err = c.compilePath(x.els); err != nil {
				return nil, err
			}
		}
		return func(e *env, cur pathItem, out func(pathItem) error) error {
			return cond(e, cur.val, func(v any) error {
				if jv.Truthy(v) {
					return then(e, cur, out)
				}
				return els(e, cur, out)
			})
		}, nil
	case nAlt:
		l, err := c.compilePath(x.left)
		if err != nil {
			return nil, err
		}
		r, err := c.compilePath(x.right)
		if err != nil {
			return nil, err
		}
		return func(e *env, cur pathItem, out func(pathItem) error) error {
			found := false
			err := l(e, cur, func(t pathItem) error {
				if !jv.Truthy(t.val) {
					return nil
				}
				found = true
				return out(t)
			})
			if err != nil || found {
				return err
			}
			return r(e, cur, out)
		}, nil
	case nBind:
		src, err := c.compile(x.source)
		if err != nil {
			return nil, err
		}
		var body pathFilter
		if _, err := c.withVar(x.name, func() (filter, error) {
			body, err = c.compilePath(x.body)
			return nil, err
		}); err != nil {
			return nil, err
		}
		name := x.name
		return func(e *env, cur pathItem, out func(pathItem) error) error {
			return src(e, cur.val, func(v any) error { return body(e.bind(name, v), cur, out) })
		}, nil
	case nCall:
		if pf, ok, err := c.pathCall(x); ok || err != nil {
			return pf, err
		}
	}
	// Anything else isn't a path; like jq, it fails when it yields a value.
	f, err := c.compile(n)
	if err != nil {
		return nil, err
	}
	return func(e *env, cur pathItem, out func(pathItem) error) error {
		return f(e, cur.val, func(v any) error {
			return errorf("Invalid path expression with result %s", shortJSON(v))
		})
	}, nil
}

func isRuntime(err error) bool {
	_, ok := err.(*RuntimeError)
	return ok
}

func shortJSON(v any) string {
	s := string(jv.Encode(nil, v, jv.Format{Compact: true}))
	if len(s) > 11 {
		s = s[:10] + "..."
	}
	return s
}

// pathCall handles the builtins that are path expressions.
func (c *compiler) pathCall(x nCall) (pathFilter, bool, error) {
	switch {
	case x.name == "empty" && len(x.args) == 0:
		return func(*env, pathItem, func(pathItem) error) error { return nil }, true, nil
	case x.name == "select" && len(x.args) == 1:
		cond, err := c.compile(x.args[0])
		if err != nil {
			return nil, true, err
		}
		return func(e *env, cur pathItem, out func(pathItem) error) error {
			return cond(e, cur.val, func(v any) error {
				if jv.Truthy(v) {
					return out(cur)
				}
				return nil
			})
		}, true, nil
	case x.name == "recurse" && len(x.args) == 0:
		pf, err := c.compilePath(nRecurse{})
		return pf, true, err
	case x.name == "recurse" && len(x.args) == 1:
		step, err := c.compilePath(x.args[0])
		if err != nil {
			return nil, true, err
		}
		var rec func(e *env, cur pathItem, out func(pathItem) error) error
		rec = func(e *env, cur pathItem, out func(pathItem) error) error {
			if err := out(cur); err != nil {
				return err
			}
			return step(e, cur, func(ch pathItem) error { return rec(e, ch, out) })
		}
		return rec, true, nil
	case x.name == "getpath" && len(x.args) == 1:
		pf, err := c.compile(x.args[0])
		if err != nil {
			return nil, true, err
		}
		return func(e *env, cur pathItem, out func(pathItem) error) error {
			return pf(e, cur.val, func(p any) error {
				ps, ok := jv.Norm(p).([]any)
				if !ok {
					return errorf("Path must be specified as an array")
				}
				v, err := getPath(cur.val, ps)
				if err != nil {
					return err
				}
				full := append(append([]any{}, cur.path...), ps...)
				return out(pathItem{full, v})
			})
		}, true, nil
	case (x.name == "first" || x.name == "last") && len(x.args) == 0:
		idx := 0
		if x.name == "last" {
			idx = -1
		}
		pf, err := c.compilePath(nIndex{target: nIdentity{}, index: nLiteral{jv.Num(float64(idx))}})
		return pf, true, err
	case x.name == "first" && len(x.args) == 1:
		inner, err := c.compilePath(x.args[0])
		if err != nil {
			return nil, true, err
		}
		return func(e *env, cur pathItem, out func(pathItem) error) error {
			brk := newBreak()
			err := inner(e, cur, func(t pathItem) error {
				if err := out(t); err != nil {
					return err
				}
				return brk
			})
			if err == brk {
				return nil
			}
			return err
		}, true, nil
	case len(x.args) == 0 && typeSelectors[x.name] != nil:
		kinds := typeSelectors[x.name]
		return func(_ *env, cur pathItem, out func(pathItem) error) error {
			k := jv.KindOf(cur.val)
			for _, want := range kinds {
				if k == want {
					return out(cur)
				}
			}
			return nil
		}, true, nil
	case x.name == "values" && len(x.args) == 0:
		return func(_ *env, cur pathItem, out func(pathItem) error) error {
			if jv.KindOf(cur.val) == jv.KNull {
				return nil
			}
			return out(cur)
		}, true, nil
	case x.name == "error" && len(x.args) <= 1:
		return nil, false, nil
	case x.name == "limit" && len(x.args) == 2:
		nf, err := c.compile(x.args[0])
		if err != nil {
			return nil, true, err
		}
		inner, err := c.compilePath(x.args[1])
		if err != nil {
			return nil, true, err
		}
		return func(e *env, cur pathItem, out func(pathItem) error) error {
			return nf(e, cur.val, func(nv any) error {
				n, ok := jv.AsNumber(nv)
				if !ok {
					return errorf("Invalid limit: must be a number")
				}
				if n.F <= 0 {
					if n.F < 0 {
						return inner(e, cur, out)
					}
					return nil
				}
				k := 0.0
				brk := newBreak()
				err := inner(e, cur, func(t pathItem) error {
					if err := out(t); err != nil {
						return err
					}
					k++
					if k >= n.F {
						return brk
					}
					return nil
				})
				if err == brk {
					return nil
				}
				return err
			})
		}, true, nil
	}
	return nil, false, nil
}

var typeSelectors = map[string][]jv.Kind{
	"nulls": {jv.KNull}, "booleans": {jv.KTrue, jv.KFalse}, "numbers": {jv.KNumber},
	"strings": {jv.KString}, "arrays": {jv.KArray}, "objects": {jv.KObject},
	"iterables": {jv.KArray, jv.KObject}, "scalars": {jv.KNull, jv.KTrue, jv.KFalse, jv.KNumber, jv.KString},
}

// eachChild yields the members of an array or object as path items.
func eachChild(cur pathItem, out func(pathItem) error) error {
	v := jv.Norm(cur.val)
	switch x := v.(type) {
	case []any:
		for i, ch := range x {
			if err := out(pathItem{appendPath(cur.path, jv.Num(float64(i))), ch}); err != nil {
				return err
			}
		}
	case *jv.Object:
		for i, k := range x.Keys {
			if err := out(pathItem{appendPath(cur.path, k), x.Vals[i]}); err != nil {
				return err
			}
		}
	}
	return nil
}

// runPaths yields the paths of pf on in.
func runPaths(pf pathFilter, e *env, in any, out func([]any) error) error {
	return pf(e, pathItem{nil, in}, func(t pathItem) error {
		p := t.path
		if p == nil {
			p = []any{}
		}
		return out(p)
	})
}

func getPath(v any, p []any) (any, error) {
	for i, k := range p {
		if v == nil {
			return nil, nil
		}
		if jv.KindOf(v) == jv.KNull {
			return nil, nil
		}
		var err error
		v, err = indexValue(v, k)
		if err != nil {
			_ = i
			return nil, err
		}
	}
	return v, nil
}

func setPath(root any, p []any, val any) (any, error) {
	if len(p) == 0 {
		return val, nil
	}
	k := jv.Norm(p[0])
	cur := jv.Norm(root)
	switch kk := k.(type) {
	case string:
		var o *jv.Object
		switch c := cur.(type) {
		case nil:
			o = jv.NewObject(1)
		case *jv.Object:
			o = c.Copy()
		default:
			return nil, errorf("Cannot index %s with \"%s\"", jv.TypeName(cur), kk)
		}
		child, _ := o.Get(kk)
		nv, err := setPath(child, p[1:], val)
		if err != nil {
			return nil, err
		}
		o.Set(kk, nv)
		return o, nil
	case jv.Number:
		var a []any
		switch c := cur.(type) {
		case nil:
		case []any:
			a = c
		default:
			return nil, errorf("Cannot index %s with number", jv.TypeName(cur))
		}
		i := math.Floor(kk.F)
		if i < 0 {
			i += float64(len(a))
			if i < 0 {
				return nil, errorf("Out of bounds negative array index")
			}
		}
		if i > 1<<26 {
			return nil, errorf("Array index too large")
		}
		n := int(i)
		out := make([]any, max(len(a), n+1))
		copy(out, a)
		var child any
		if n < len(a) {
			child = a[n]
		}
		nv, err := setPath(child, p[1:], val)
		if err != nil {
			return nil, err
		}
		out[n] = nv
		return out, nil
	case *jv.Object:
		// a slice
		var a []any
		switch c := cur.(type) {
		case nil:
		case []any:
			a = c
		default:
			return nil, errorf("Cannot update field at object index of %s", jv.TypeName(cur))
		}
		from, _ := kk.Get("start")
		to, _ := kk.Get("end")
		s, e, err := sliceBounds(len(a), from, to)
		if err != nil {
			return nil, err
		}
		old := a[s:e:e]
		nv, err := setPath(old, p[1:], val)
		if err != nil {
			return nil, err
		}
		repl, ok := jv.Norm(nv).([]any)
		if !ok {
			return nil, errorf("A slice of an array can only be assigned another array")
		}
		out := make([]any, 0, len(a)-len(old)+len(repl))
		out = append(out, a[:s]...)
		out = append(out, repl...)
		return append(out, a[e:]...), nil
	}
	return nil, errorf("Invalid path component")
}

func delPaths(root any, paths [][]any) (any, error) {
	// Delete the longest and latest paths first, as jq does.
	sort.SliceStable(paths, func(i, j int) bool { return jv.Compare(toArr(paths[i]), toArr(paths[j])) > 0 })
	var err error
	for _, p := range paths {
		if root, err = delPath(root, p); err != nil {
			return nil, err
		}
	}
	return root, nil
}

func toArr(p []any) []any { return p }

func delPath(root any, p []any) (any, error) {
	if len(p) == 0 {
		return nil, nil
	}
	cur := jv.Norm(root)
	if cur == nil {
		return nil, nil
	}
	k := jv.Norm(p[0])
	if len(p) == 1 {
		switch kk := k.(type) {
		case string:
			o, ok := cur.(*jv.Object)
			if !ok {
				return nil, errorf("Cannot delete field at object index of %s", jv.TypeName(cur))
			}
			o = o.Copy()
			o.Delete(kk)
			return o, nil
		case jv.Number:
			a, ok := cur.([]any)
			if !ok {
				return nil, errorf("Cannot delete field at index of %s", jv.TypeName(cur))
			}
			i := int(math.Floor(kk.F))
			if i < 0 {
				i += len(a)
				if i < 0 {
					return nil, errorf("Out of bounds negative array index")
				}
			}
			if i >= len(a) {
				return a, nil
			}
			out := make([]any, 0, len(a)-1)
			out = append(out, a[:i]...)
			return append(out, a[i+1:]...), nil
		case *jv.Object:
			a, ok := cur.([]any)
			if !ok {
				return nil, errorf("Cannot delete slice of %s", jv.TypeName(cur))
			}
			from, _ := kk.Get("start")
			to, _ := kk.Get("end")
			s, e, err := sliceBounds(len(a), from, to)
			if err != nil {
				return nil, err
			}
			out := make([]any, 0, len(a)-(e-s))
			out = append(out, a[:s]...)
			return append(out, a[e:]...), nil
		}
		return nil, errorf("Invalid path component")
	}
	child, err := indexValue(cur, k)
	if err != nil {
		return nil, err
	}
	if child == nil {
		return cur, nil
	}
	nc, err := delPath(child, p[1:])
	if err != nil {
		return nil, err
	}
	return setPath(cur, []any{k}, nc)
}

// compileAssign handles = |= += -= *= /= %= //=.
func (c *compiler) compileAssign(x nAssign) (filter, error) {
	lhs, err := c.compilePath(x.lhs)
	if err != nil {
		return nil, err
	}
	rhs, err := c.compile(x.rhs)
	if err != nil {
		return nil, err
	}
	collect := func(e *env, in any) ([][]any, error) {
		var ps [][]any
		err := runPaths(lhs, e, in, func(p []any) error { ps = append(ps, p); return nil })
		return ps, err
	}
	switch x.op {
	case "|=":
		return func(e *env, in any, out emitFn) error {
			ps, err := collect(e, in)
			if err != nil {
				return err
			}
			v := in
			var dels [][]any
			for _, p := range ps {
				old, err := getPath(v, p)
				if err != nil {
					return err
				}
				var nv any
				got := false
				brk := newBreak()
				err = rhs(e, old, func(u any) error { nv, got = u, true; return brk })
				if err != nil && err != brk {
					return err
				}
				if !got {
					dels = append(dels, p)
					continue
				}
				if v, err = setPath(v, p, nv); err != nil {
					return err
				}
			}
			if len(dels) > 0 {
				if v, err = delPaths(v, dels); err != nil {
					return err
				}
			}
			return out(v)
		}, nil
	case "=":
		return func(e *env, in any, out emitFn) error {
			return rhs(e, in, func(val any) error {
				ps, err := collect(e, in)
				if err != nil {
					return err
				}
				v := in
				for _, p := range ps {
					if v, err = setPath(v, p, val); err != nil {
						return err
					}
				}
				return out(v)
			})
		}, nil
	}
	op := x.op[:len(x.op)-1] // += becomes +
	return func(e *env, in any, out emitFn) error {
		return rhs(e, in, func(val any) error {
			ps, err := collect(e, in)
			if err != nil {
				return err
			}
			v := in
			for _, p := range ps {
				old, err := getPath(v, p)
				if err != nil {
					return err
				}
				var nv any
				if op == "//" {
					nv = old
					if !jv.Truthy(old) {
						nv = val
					}
				} else if nv, err = binop(op, old, val); err != nil {
					return err
				}
				if v, err = setPath(v, p, nv); err != nil {
					return err
				}
			}
			return out(v)
		})
	}, nil
}
