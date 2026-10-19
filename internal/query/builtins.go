// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package query

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/just-very-quickly/query/internal/jv"
)

type builtinFn func(c *compiler, args []node) (filter, error)

var builtins map[string]builtinFn

// simple makes a builtin of no arguments from a function of the input.
func simple(f func(in any) (any, error)) builtinFn {
	return func(*compiler, []node) (filter, error) {
		return func(_ *env, in any, out emitFn) error {
			v, err := f(in)
			if err != nil {
				return err
			}
			return out(v)
		}, nil
	}
}

// withValues makes a builtin whose arguments are values: each argument is
// run on the input and the function is called for every combination, the
// first argument in the outer loop.
func withValues(f func(in any, args []any) (any, error)) builtinFn {
	return func(c *compiler, args []node) (filter, error) {
		fs, err := c.compileAll(args)
		if err != nil {
			return nil, err
		}
		return func(e *env, in any, out emitFn) error {
			vals := make([]any, len(fs))
			var run func(i int) error
			run = func(i int) error {
				if i == len(fs) {
					v, err := f(in, vals)
					if err != nil {
						return err
					}
					return out(v)
				}
				return fs[i](e, in, func(v any) error { vals[i] = v; return run(i + 1) })
			}
			return run(0)
		}, nil
	}
}

// withFilters makes a builtin from a function of the compiled arguments.
func withFilters(f func(args []filter) filter) builtinFn {
	return func(c *compiler, args []node) (filter, error) {
		fs, err := c.compileAll(args)
		if err != nil {
			return nil, err
		}
		return f(fs), nil
	}
}

func (c *compiler) compileAll(args []node) ([]filter, error) {
	fs := make([]filter, len(args))
	for i, a := range args {
		var err error
		if fs[i], err = c.compile(a); err != nil {
			return nil, err
		}
	}
	return fs, nil
}

func typeSelect(kinds ...jv.Kind) builtinFn {
	return func(*compiler, []node) (filter, error) {
		return func(_ *env, in any, out emitFn) error {
			k := jv.KindOf(in)
			for _, want := range kinds {
				if k == want {
					return out(in)
				}
			}
			return nil
		}, nil
	}
}

func mathFn(f func(float64) float64) builtinFn {
	return simple(func(in any) (any, error) {
		n, ok := jv.AsNumber(in)
		if !ok {
			return nil, errorf("%s number required", describe(in))
		}
		return jv.Num(f(n.F)), nil
	})
}

func (c *compiler) compileCall(x nCall) (filter, error) {
	if isAggregation(x.name, len(x.args)) {
		if c.agg == nil {
			return nil, &SyntaxError{0, fmt.Sprintf("%s/%d is an aggregation; it can only be used in one step of the filter's top-level pipe (see jvqq --help)", x.name, len(x.args))}
		}
		return c.agg.add(c, x)
	}
	key := fmt.Sprintf("%s/%d", x.name, len(x.args))
	b, ok := builtins[key]
	if !ok {
		for k := range builtins {
			if strings.HasPrefix(k, x.name+"/") {
				return nil, &SyntaxError{0, fmt.Sprintf("%s is not defined with %d arguments", x.name, len(x.args))}
			}
		}
		switch x.name {
		case "input_line_number", "get_search_list", "get_prog_origin", "get_jq_origin", "modulemeta", "label":
			return nil, &SyntaxError{0, fmt.Sprintf("%s isn't supported by jvqq", x.name)}
		}
		return nil, &SyntaxError{0, fmt.Sprintf("%s/%d is not defined", x.name, len(x.args))}
	}
	return b(c, x.args)
}

func lengthOf(in any) (any, error) {
	switch jv.KindOf(in) {
	case jv.KNull:
		return jv.Num(0), nil
	case jv.KTrue, jv.KFalse:
		return nil, errorf("%s has no length", describe(in))
	case jv.KNumber:
		n, _ := jv.AsNumber(in)
		return jv.Num(math.Abs(n.F)), nil
	case jv.KString:
		if r, ok := jv.RawOf(in); ok {
			if body, ok := jv.RawPlainString(r); ok {
				return jv.Num(float64(len(body))), nil
			}
		}
		return jv.Num(float64(utf8.RuneCountInString(jv.Str(in)))), nil
	case jv.KArray:
		if r, ok := jv.RawOf(in); ok {
			return jv.Num(float64(jv.Len(r))), nil
		}
		return jv.Num(float64(len(in.([]any)))), nil
	}
	if r, ok := jv.RawOf(in); ok {
		return jv.Num(float64(jv.Len(r))), nil
	}
	return jv.Num(float64(in.(*jv.Object).Len())), nil
}

func keysOf(in any, sorted bool) (any, error) {
	switch v := jv.Norm(in).(type) {
	case *jv.Object:
		ks := v.Keys
		if sorted {
			ks = v.SortedKeys()
		}
		out := make([]any, len(ks))
		for i, k := range ks {
			out[i] = k
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i := range v {
			out[i] = jv.Num(float64(i))
		}
		return out, nil
	}
	return nil, errorf("%s has no keys", describe(in))
}

func hasKey(in, k any) (any, error) {
	switch {
	case jv.KindOf(in) == jv.KNull:
		return false, nil
	case jv.KindOf(in) == jv.KObject && jv.KindOf(k) == jv.KString:
		if r, ok := jv.RawOf(in); ok {
			_, found := jv.Field(r, jv.Str(k))
			return found, nil
		}
		_, found := in.(*jv.Object).Get(jv.Str(k))
		return found, nil
	case jv.KindOf(in) == jv.KArray && jv.KindOf(k) == jv.KNumber:
		n, _ := jv.AsNumber(k)
		l, _ := lengthOf(in)
		return n.F >= 0 && n.F < l.(jv.Number).F, nil
	}
	return nil, errorf("Cannot check whether %s has a %s key", jv.TypeName(in), jv.TypeName(k))
}

func toString(v any) string {
	if jv.KindOf(v) == jv.KString {
		return jv.Str(v)
	}
	return string(jv.Encode(nil, v, jv.Format{Compact: true}))
}

func toNumber(in any) (any, error) {
	switch jv.KindOf(in) {
	case jv.KNumber:
		return in, nil
	case jv.KString:
		s := jv.Str(in)
		if end, err := jv.Valid([]byte(s), 0); err == nil && end == len(s) && jv.KindOf(jv.Raw(s)) == jv.KNumber {
			return jv.NumberFromLit(s), nil
		}
		t := strings.TrimSpace(s)
		if f, err := strconv.ParseFloat(t, 64); err == nil && t != "" && !strings.ContainsAny(t, "xXpP_") {
			return jv.Num(f), nil
		}
		return nil, errorf("Cannot parse '%s' as JSON", s)
	}
	return nil, errorf("%s cannot be parsed as a number", describe(in))
}

func arrayOf(in any, what string) ([]any, error) {
	a, ok := jv.Norm(in).([]any)
	if !ok {
		return nil, errorf("%s cannot be %s, as it is not an array", describe(in), what)
	}
	return a, nil
}

// sortByKeys sorts a stably by the key arrays ks.
func sortByKeys(a []any, ks [][]any) []any {
	idx := make([]int, len(a))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(i, j int) bool { return jv.Compare(ks[idx[i]], ks[idx[j]]) < 0 })
	out := make([]any, len(a))
	keysOut := make([][]any, len(a))
	for i, k := range idx {
		out[i] = a[k]
		keysOut[i] = ks[k]
	}
	copy(ks, keysOut)
	return out
}

// keysBy runs f on every element and collects its outputs as the sort key.
func keysBy(e *env, f filter, a []any) ([][]any, error) {
	ks := make([][]any, len(a))
	for i, v := range a {
		var k []any
		if err := f(e, v, func(x any) error { k = append(k, x); return nil }); err != nil {
			return nil, err
		}
		if k == nil {
			k = []any{}
		}
		ks[i] = k
	}
	return ks, nil
}

func byFilter(what string, f func(a []any, ks [][]any) (any, error)) builtinFn {
	return withFilters(func(args []filter) filter {
		return func(e *env, in any, out emitFn) error {
			a, err := arrayOf(in, what)
			if err != nil {
				return err
			}
			ks, err := keysBy(e, args[0], a)
			if err != nil {
				return err
			}
			v, err := f(a, ks)
			if err != nil {
				return err
			}
			return out(v)
		}
	})
}

func identityKeys(a []any) [][]any {
	ks := make([][]any, len(a))
	for i, v := range a {
		ks[i] = []any{v}
	}
	return ks
}

func groupSorted(a []any, ks [][]any) []any {
	sorted := sortByKeys(a, ks)
	out := []any{}
	for i := 0; i < len(sorted); {
		j := i + 1
		for j < len(sorted) && jv.Compare(ks[i], ks[j]) == 0 {
			j++
		}
		out = append(out, append([]any(nil), sorted[i:j]...))
		i = j
	}
	return out
}

func uniqueSorted(a []any, ks [][]any) []any {
	sorted := sortByKeys(a, ks)
	out := []any{}
	for i := 0; i < len(sorted); i++ {
		if i > 0 && jv.Compare(ks[i-1], ks[i]) == 0 {
			continue
		}
		out = append(out, sorted[i])
	}
	return out
}

func extremeBy(a []any, ks [][]any, wantMax bool) any {
	if len(a) == 0 {
		return nil
	}
	best := 0
	for i := 1; i < len(a); i++ {
		c := jv.Compare(ks[i], ks[best])
		if wantMax && c >= 0 || !wantMax && c < 0 {
			best = i
		}
	}
	return a[best]
}

func contains(a, b any) (bool, error) {
	ka, kb := jv.KindOf(a), jv.KindOf(b)
	if ka != kb && !(ka == jv.KTrue || ka == jv.KFalse) && !(kb == jv.KTrue || kb == jv.KFalse) {
		return false, errorf("%s and %s cannot have their containment checked", describe(a), describe(b))
	}
	switch ka {
	case jv.KObject:
		if kb != jv.KObject {
			return false, errorf("%s and %s cannot have their containment checked", describe(a), describe(b))
		}
		x, y := jv.Norm(a).(*jv.Object), jv.Norm(b).(*jv.Object)
		for i, k := range y.Keys {
			av, ok := x.Get(k)
			if !ok {
				return false, nil
			}
			okc, err := contains(av, y.Vals[i])
			if err != nil || !okc {
				return false, err
			}
		}
		return true, nil
	case jv.KArray:
		x, y := jv.Norm(a).([]any), jv.Norm(b).([]any)
	outer:
		for _, bv := range y {
			for _, av := range x {
				if ok, _ := contains(av, bv); ok {
					continue outer
				}
			}
			return false, nil
		}
		return true, nil
	case jv.KString:
		return strings.Contains(jv.Str(a), jv.Str(b)), nil
	}
	return jv.Equal(a, b), nil
}

func flatten(a []any, depth float64, out []any) []any {
	for _, v := range a {
		if arr, ok := jv.Norm(v).([]any); ok && depth > 0 {
			out = flatten(arr, depth-1, out)
		} else {
			out = append(out, v)
		}
	}
	return out
}

func implode(in any) (any, error) {
	a, ok := jv.Norm(in).([]any)
	if !ok {
		return nil, errorf("Cannot implode %s", describe(in))
	}
	var sb strings.Builder
	for _, v := range a {
		n, ok := jv.AsNumber(v)
		if !ok {
			return nil, errorf("Unicode codepoint must be numeric")
		}
		r := rune(n.F)
		if r > unicode.MaxRune || r < 0 || (r >= 0xd800 && r < 0xe000) {
			r = unicode.ReplacementChar
		}
		sb.WriteRune(r)
	}
	return sb.String(), nil
}

func strFn(name string, f func(s string) any) builtinFn {
	return simple(func(in any) (any, error) {
		if jv.KindOf(in) != jv.KString {
			return nil, errorf("%s input must be a string", name)
		}
		return f(jv.Str(in)), nil
	})
}

func joinArray(in, sep any) (any, error) {
	a, err := arrayOf(in, "joined")
	if err != nil {
		if jv.KindOf(in) == jv.KObject {
			a = jv.Norm(in).(*jv.Object).Vals
		} else {
			return nil, errorf("Cannot iterate over %s", describe(in))
		}
	}
	if len(a) == 0 {
		return "", nil
	}
	if jv.KindOf(sep) != jv.KString {
		return nil, errorf("%s and %s cannot be added", describe(""), describe(sep))
	}
	s := jv.Str(sep)
	var sb strings.Builder
	for i, v := range a {
		if i > 0 {
			sb.WriteString(s)
		}
		switch jv.KindOf(v) {
		case jv.KNull:
		case jv.KString:
			sb.WriteString(jv.Str(v))
		case jv.KNumber, jv.KTrue, jv.KFalse:
			sb.WriteString(toString(v))
		default:
			return nil, errorf("Cannot join with %s", jv.TypeName(v))
		}
	}
	return sb.String(), nil
}

func toEntries(in any) (any, error) {
	switch v := jv.Norm(in).(type) {
	case *jv.Object:
		out := make([]any, len(v.Keys))
		for i, k := range v.Keys {
			o := jv.NewObject(2)
			o.Set("key", k)
			o.Set("value", v.Vals[i])
			out[i] = o
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			o := jv.NewObject(2)
			o.Set("key", jv.Num(float64(i)))
			o.Set("value", x)
			out[i] = o
		}
		return out, nil
	}
	return nil, errorf("%s has no keys", describe(in))
}

// fromEntries is jq 1.7's: the key from key, Key, name or Name (the first
// that is neither null nor false), the value from value or else Value.
func fromEntries(in any) (any, error) {
	a, ok := jv.Norm(in).([]any)
	if !ok {
		if o, isObj := jv.Norm(in).(*jv.Object); isObj {
			a = o.Vals
		} else {
			return nil, errorf("Cannot iterate over %s", describe(in))
		}
	}
	o := jv.NewObject(len(a))
	for _, item := range a {
		var k any
		for _, name := range []string{"key", "Key", "name", "Name"} {
			v, err := indexValue(item, name)
			if err != nil {
				return nil, err
			}
			if jv.Truthy(v) {
				k = v
				break
			}
		}
		if jv.KindOf(k) != jv.KString {
			return nil, errorf("Cannot use %s as object key", describe(k))
		}
		has, err := hasKey(item, "value")
		if err != nil {
			return nil, err
		}
		field := "Value"
		if has.(bool) {
			field = "value"
		}
		val, err := indexValue(item, field)
		if err != nil {
			return nil, err
		}
		o.Set(jv.Str(k), val)
	}
	return o, nil
}

// stringIndices finds the byte offsets of sub in s, as jq 1.7 does.
func stringIndices(s, sub string) []any {
	out := []any{}
	if sub == "" {
		return out
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if strings.HasPrefix(s[i:], sub) {
			out = append(out, jv.Num(float64(i)))
		}
	}
	return out
}

func indicesOf(in, x any) (any, error) {
	switch jv.KindOf(in) {
	case jv.KNull:
		return nil, nil
	case jv.KString:
		if jv.KindOf(x) != jv.KString {
			return nil, errorf("Cannot determine indices of %s in string", jv.TypeName(x))
		}
		return stringIndices(jv.Str(in), jv.Str(x)), nil
	case jv.KArray:
		a := jv.Norm(in).([]any)
		out := []any{}
		if sub, ok := jv.Norm(x).([]any); ok {
			if len(sub) == 0 {
				return nil, nil
			}
			for i := 0; i+len(sub) <= len(a); i++ {
				match := true
				for j := range sub {
					if !jv.Equal(a[i+j], sub[j]) {
						match = false
						break
					}
				}
				if match {
					out = append(out, jv.Num(float64(i)))
				}
			}
			return out, nil
		}
		for i, v := range a {
			if jv.Equal(v, x) {
				out = append(out, jv.Num(float64(i)))
			}
		}
		return out, nil
	}
	return nil, errorf("Cannot index %s with %s", jv.TypeName(in), jv.TypeName(x))
}

func envObject() *jv.Object {
	o := jv.NewObject(32)
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 {
			o.Set(kv[:i], kv[i+1:])
		}
	}
	return o
}

// walk applies f bottom-up.
func walk(e *env, f filter, v any, out emitFn) error {
	switch x := jv.Norm(v).(type) {
	case []any:
		arr := make([]any, 0, len(x))
		for _, c := range x {
			if err := walk(e, f, c, func(w any) error { arr = append(arr, w); return nil }); err != nil {
				return err
			}
		}
		return f(e, arr, out)
	case *jv.Object:
		o := jv.NewObject(x.Len())
		for i, k := range x.Keys {
			var last any
			got := false
			if err := walk(e, f, x.Vals[i], func(w any) error { last, got = w, true; return nil }); err != nil {
				return err
			}
			if got {
				o.Set(k, last)
			}
		}
		return f(e, o, out)
	}
	return f(e, v, out)
}

func init() {
	builtins = map[string]builtinFn{
		"empty/0": func(*compiler, []node) (filter, error) {
			return func(*env, any, emitFn) error { return nil }, nil
		},
		"not/0":            simple(func(in any) (any, error) { return !jv.Truthy(in), nil }),
		"length/0":         simple(lengthOf),
		"utf8bytelength/0": strFn("utf8bytelength", func(s string) any { return jv.Num(float64(len(s))) }),
		"keys/0":           simple(func(in any) (any, error) { return keysOf(in, true) }),
		"keys_unsorted/0":  simple(func(in any) (any, error) { return keysOf(in, false) }),
		"has/1":            withValues(func(in any, a []any) (any, error) { return hasKey(in, a[0]) }),
		"in/1":             withValues(func(in any, a []any) (any, error) { return hasKey(a[0], in) }),
		"values/0":         func(*compiler, []node) (filter, error) { return notNull, nil },
		"nulls/0":          typeSelect(jv.KNull),
		"booleans/0":       typeSelect(jv.KTrue, jv.KFalse),
		"numbers/0":        typeSelect(jv.KNumber),
		"strings/0":        typeSelect(jv.KString),
		"arrays/0":         typeSelect(jv.KArray),
		"objects/0":        typeSelect(jv.KObject),
		"iterables/0":      typeSelect(jv.KArray, jv.KObject),
		"scalars/0":        typeSelect(jv.KNull, jv.KTrue, jv.KFalse, jv.KNumber, jv.KString),
		"type/0":           simple(func(in any) (any, error) { return jv.TypeName(in), nil }),
		"tostring/0":       simple(func(in any) (any, error) { return toString(in), nil }),
		"tonumber/0":       simple(toNumber),
		"tojson/0": simple(func(in any) (any, error) {
			return string(jv.Encode(nil, in, jv.Format{Compact: true})), nil
		}),
		"fromjson/0": simple(func(in any) (any, error) {
			if jv.KindOf(in) != jv.KString {
				return nil, errorf("%s cannot be parsed as JSON", describe(in))
			}
			s := jv.Str(in)
			v, err := jv.Decode([]byte(s))
			if err != nil {
				return nil, errorf("%s (while parsing '%s')", err.Error(), s)
			}
			return v, nil
		}),
		"ascii_downcase/0": strFn("ascii_downcase", func(s string) any {
			return strings.Map(func(r rune) rune {
				if r >= 'A' && r <= 'Z' {
					return r + 32
				}
				return r
			}, s)
		}),
		"ascii_upcase/0": strFn("ascii_upcase", func(s string) any {
			return strings.Map(func(r rune) rune {
				if r >= 'a' && r <= 'z' {
					return r - 32
				}
				return r
			}, s)
		}),
		"explode/0": strFn("explode", func(s string) any {
			out := []any{}
			for _, r := range s {
				out = append(out, jv.Num(float64(r)))
			}
			return out
		}),
		"implode/0": simple(implode),
		"ltrimstr/1": withValues(func(in any, a []any) (any, error) {
			if jv.KindOf(in) == jv.KString && jv.KindOf(a[0]) == jv.KString {
				if s, p := jv.Str(in), jv.Str(a[0]); strings.HasPrefix(s, p) {
					return s[len(p):], nil
				}
			}
			return in, nil
		}),
		"rtrimstr/1": withValues(func(in any, a []any) (any, error) {
			if jv.KindOf(in) == jv.KString && jv.KindOf(a[0]) == jv.KString {
				if s, p := jv.Str(in), jv.Str(a[0]); strings.HasSuffix(s, p) && len(p) > 0 {
					return s[:len(s)-len(p)], nil
				}
			}
			return in, nil
		}),
		"startswith/1": withValues(func(in any, a []any) (any, error) {
			if jv.KindOf(in) != jv.KString || jv.KindOf(a[0]) != jv.KString {
				return nil, errorf("startswith() requires string inputs")
			}
			return strings.HasPrefix(jv.Str(in), jv.Str(a[0])), nil
		}),
		"endswith/1": withValues(func(in any, a []any) (any, error) {
			if jv.KindOf(in) != jv.KString || jv.KindOf(a[0]) != jv.KString {
				return nil, errorf("endswith() requires string inputs")
			}
			return strings.HasSuffix(jv.Str(in), jv.Str(a[0])), nil
		}),
		"split/1": withValues(func(in any, a []any) (any, error) {
			if jv.KindOf(in) != jv.KString || jv.KindOf(a[0]) != jv.KString {
				return nil, errorf("split input and separator must be strings")
			}
			return splitString(jv.Str(in), jv.Str(a[0])), nil
		}),
		"join/1": withValues(func(in any, a []any) (any, error) { return joinArray(in, a[0]) }),
		"contains/1": withValues(func(in any, a []any) (any, error) {
			return contains(in, a[0])
		}),
		"inside/1": withValues(func(in any, a []any) (any, error) {
			return contains(a[0], in)
		}),
		"indices/1": withValues(func(in any, a []any) (any, error) { return indicesOf(in, a[0]) }),
		"index/1": withValues(func(in any, a []any) (any, error) {
			v, err := indicesOf(in, a[0])
			if arr, ok := v.([]any); ok && err == nil {
				if len(arr) == 0 {
					return nil, nil
				}
				return arr[0], nil
			}
			return v, err
		}),
		"rindex/1": withValues(func(in any, a []any) (any, error) {
			v, err := indicesOf(in, a[0])
			if arr, ok := v.([]any); ok && err == nil {
				if len(arr) == 0 {
					return nil, nil
				}
				return arr[len(arr)-1], nil
			}
			return v, err
		}),
		"add/0": simple(func(in any) (any, error) {
			var items []any
			switch v := jv.Norm(in).(type) {
			case []any:
				items = v
			case *jv.Object:
				items = v.Vals
			default:
				return nil, errorf("Cannot iterate over %s", describe(in))
			}
			var acc any
			for _, x := range items {
				var err error
				if acc, err = add(acc, x); err != nil {
					return nil, err
				}
			}
			return acc, nil
		}),
		"any/0": simple(func(in any) (any, error) {
			a, err := iterItems(in)
			if err != nil {
				return nil, err
			}
			for _, v := range a {
				if jv.Truthy(v) {
					return true, nil
				}
			}
			return false, nil
		}),
		"all/0": simple(func(in any) (any, error) {
			a, err := iterItems(in)
			if err != nil {
				return nil, err
			}
			for _, v := range a {
				if !jv.Truthy(v) {
					return false, nil
				}
			}
			return true, nil
		}),
		"any/1": withFilters(func(f []filter) filter { return anyAll(iterateFilter, f[0], true) }),
		"all/1": withFilters(func(f []filter) filter { return anyAll(iterateFilter, f[0], false) }),
		"any/2": withFilters(func(f []filter) filter { return anyAll(f[0], f[1], true) }),
		"all/2": withFilters(func(f []filter) filter { return anyAll(f[0], f[1], false) }),
		"flatten/0": simple(func(in any) (any, error) {
			a, err := arrayOf(in, "flattened")
			if err != nil {
				return nil, errorf("Cannot iterate over %s", describe(in))
			}
			return flatten(a, 1e9, []any{}), nil
		}),
		"flatten/1": withValues(func(in any, args []any) (any, error) {
			d, ok := jv.AsNumber(args[0])
			if !ok {
				return nil, errorf("flatten depth must not be negative")
			}
			if d.F < 0 {
				return nil, errorf("flatten depth must not be negative")
			}
			a, err := arrayOf(in, "flattened")
			if err != nil {
				return nil, errorf("Cannot iterate over %s", describe(in))
			}
			return flatten(a, d.F, []any{}), nil
		}),
		"range/1": withFilters(func(f []filter) filter {
			return func(e *env, in any, out emitFn) error {
				return f[0](e, in, func(n any) error { return rangeOut(jv.Num(0), n, jv.Num(1), out) })
			}
		}),
		"range/2": withFilters(func(f []filter) filter {
			return func(e *env, in any, out emitFn) error {
				return f[0](e, in, func(a any) error {
					return f[1](e, in, func(b any) error { return rangeOut(a, b, jv.Num(1), out) })
				})
			}
		}),
		"range/3": withFilters(func(f []filter) filter {
			return func(e *env, in any, out emitFn) error {
				return f[0](e, in, func(a any) error {
					return f[1](e, in, func(b any) error {
						return f[2](e, in, func(s any) error { return rangeOut(a, b, s, out) })
					})
				})
			}
		}),
		"floor/0":    mathFn(math.Floor),
		"ceil/0":     mathFn(math.Ceil),
		"round/0":    mathFn(math.Round),
		"trunc/0":    mathFn(math.Trunc),
		"sqrt/0":     mathFn(math.Sqrt),
		"fabs/0":     mathFn(math.Abs),
		"log/0":      mathFn(math.Log),
		"log2/0":     mathFn(math.Log2),
		"log10/0":    mathFn(math.Log10),
		"exp/0":      mathFn(math.Exp),
		"exp2/0":     mathFn(math.Exp2),
		"exp10/0":    mathFn(func(x float64) float64 { return math.Pow(10, x) }),
		"infinite/0": simple(func(any) (any, error) { return jv.Num(math.Inf(1)), nil }),
		"nan/0":      simple(func(any) (any, error) { return jv.Num(math.NaN()), nil }),
		"isinfinite/0": simple(func(in any) (any, error) {
			n, ok := jv.AsNumber(in)
			if !ok {
				return false, nil
			}
			return math.IsInf(n.F, 0), nil
		}),
		"isnan/0": simple(func(in any) (any, error) {
			n, ok := jv.AsNumber(in)
			if !ok {
				return false, nil
			}
			return math.IsNaN(n.F), nil
		}),
		"isnormal/0": simple(func(in any) (any, error) {
			n, ok := jv.AsNumber(in)
			if !ok {
				return false, nil
			}
			f := math.Abs(n.F)
			return f >= 2.2250738585072014e-308 && !math.IsInf(f, 0) && !math.IsNaN(f), nil
		}),
		"pow/2": withValues(func(_ any, a []any) (any, error) {
			x, ok1 := jv.AsNumber(a[0])
			y, ok2 := jv.AsNumber(a[1])
			if !ok1 || !ok2 {
				return nil, errorf("pow requires numbers")
			}
			return jv.Num(math.Pow(x.F, y.F)), nil
		}),
		"sort/0": simple(func(in any) (any, error) {
			a, err := arrayOf(in, "sorted")
			if err != nil {
				return nil, err
			}
			return sortByKeys(a, identityKeys(a)), nil
		}),
		"sort_by/1":  byFilter("sorted", func(a []any, ks [][]any) (any, error) { return sortByKeys(a, ks), nil }),
		"group_by/1": byFilter("grouped", func(a []any, ks [][]any) (any, error) { return groupSorted(a, ks), nil }),
		"unique/0": simple(func(in any) (any, error) {
			a, err := arrayOf(in, "sorted")
			if err != nil {
				return nil, err
			}
			return uniqueSorted(a, identityKeys(a)), nil
		}),
		"unique_by/1": byFilter("sorted", func(a []any, ks [][]any) (any, error) { return uniqueSorted(a, ks), nil }),
		"min/0": simple(func(in any) (any, error) {
			a, err := arrayOf(in, "sorted")
			if err != nil {
				return nil, err
			}
			return extremeBy(a, identityKeys(a), false), nil
		}),
		"max/0": simple(func(in any) (any, error) {
			a, err := arrayOf(in, "sorted")
			if err != nil {
				return nil, err
			}
			return extremeBy(a, identityKeys(a), true), nil
		}),
		"min_by/1": byFilter("iterated over", func(a []any, ks [][]any) (any, error) { return extremeBy(a, ks, false), nil }),
		"max_by/1": byFilter("iterated over", func(a []any, ks [][]any) (any, error) { return extremeBy(a, ks, true), nil }),
		"reverse/0": simple(func(in any) (any, error) {
			switch v := jv.Norm(in).(type) {
			case nil:
				return []any{}, nil
			case string:
				rs := []rune(v)
				for i, j := 0, len(rs)-1; i < j; i, j = i+1, j-1 {
					rs[i], rs[j] = rs[j], rs[i]
				}
				return string(rs), nil
			case []any:
				out := make([]any, len(v))
				for i, x := range v {
					out[len(v)-1-i] = x
				}
				return out, nil
			}
			return nil, errorf("Cannot reverse %s", describe(in))
		}),
		"to_entries/0":   simple(toEntries),
		"from_entries/0": simple(fromEntries),
		"with_entries/1": withFilters(func(f []filter) filter {
			return func(e *env, in any, out emitFn) error {
				ents, err := toEntries(in)
				if err != nil {
					return err
				}
				mapped := []any{}
				for _, en := range ents.([]any) {
					if err := f[0](e, en, func(v any) error { mapped = append(mapped, v); return nil }); err != nil {
						return err
					}
				}
				o, err := fromEntries(mapped)
				if err != nil {
					return err
				}
				return out(o)
			}
		}),
		"map/1": withFilters(func(f []filter) filter {
			return func(e *env, in any, out emitFn) error {
				arr := []any{}
				if err := iterate(in, func(v any) error {
					return f[0](e, v, func(w any) error { arr = append(arr, w); return nil })
				}); err != nil {
					return err
				}
				return out(arr)
			}
		}),
		"map_values/1": func(c *compiler, args []node) (filter, error) {
			return c.compileAssign(nAssign{"|=", nIterate{target: nIdentity{}}, args[0]})
		},
		"select/1": withFilters(func(f []filter) filter {
			return func(e *env, in any, out emitFn) error {
				return f[0](e, in, func(v any) error {
					if jv.Truthy(v) {
						return out(in)
					}
					return nil
				})
			}
		}),
		"recurse/0": func(*compiler, []node) (filter, error) { return recurseFilter(nil), nil },
		"recurse/1": withFilters(func(f []filter) filter { return recurseFilter(f[0]) }),
		"recurse/2": withFilters(func(f []filter) filter {
			step, cond := f[0], f[1]
			var rec func(e *env, v any, out emitFn) error
			rec = func(e *env, v any, out emitFn) error {
				if err := out(v); err != nil {
					return err
				}
				return step(e, v, func(c any) error {
					return cond(e, c, func(ok any) error {
						if jv.Truthy(ok) {
							return rec(e, c, out)
						}
						return nil
					})
				})
			}
			return rec
		}),
		"env/0":   simple(func(any) (any, error) { return envObject(), nil }),
		"error/0": simple(func(in any) (any, error) { return nil, &RuntimeError{jv.Norm(in)} }),
		"error/1": withValues(func(_ any, a []any) (any, error) { return nil, &RuntimeError{jv.Norm(a[0])} }),
		"first/0": simple(func(in any) (any, error) { return indexValue(in, jv.Num(0)) }),
		"last/0":  simple(func(in any) (any, error) { return indexValue(in, jv.Num(-1)) }),
		"nth/1":   withValues(func(in any, a []any) (any, error) { return indexValue(in, a[0]) }),
		"first/1": withFilters(func(f []filter) filter { return limitFilter(func(*env, any, emitFn) error { return nil }, f[0], 1) }),
		"last/1": withFilters(func(f []filter) filter {
			return func(e *env, in any, out emitFn) error {
				var last any
				got := false
				if err := f[0](e, in, func(v any) error { last, got = v, true; return nil }); err != nil {
					return err
				}
				if got {
					return out(last)
				}
				return nil
			}
		}),
		"limit/2": withFilters(func(f []filter) filter { return limitFilter(f[0], f[1], -1) }),
		"nth/2": withFilters(func(f []filter) filter {
			return func(e *env, in any, out emitFn) error {
				return f[0](e, in, func(nv any) error {
					n, ok := jv.AsNumber(nv)
					if !ok || n.F < 0 {
						return errorf("Out of bounds negative array index")
					}
					k := 0.0
					brk := newBreak()
					err := f[1](e, in, func(v any) error {
						if k >= n.F {
							if err := out(v); err != nil {
								return err
							}
							return brk
						}
						k++
						return nil
					})
					if err == brk {
						return nil
					}
					return err
				})
			}
		}),
		"isempty/1": withFilters(func(f []filter) filter {
			return func(e *env, in any, out emitFn) error {
				brk := newBreak()
				err := f[0](e, in, func(any) error { return brk })
				if err == brk {
					return out(false)
				}
				if err != nil {
					return err
				}
				return out(true)
			}
		}),
		"until/2": withFilters(func(f []filter) filter {
			cond, update := f[0], f[1]
			var loop func(e *env, v any, out emitFn) error
			loop = func(e *env, v any, out emitFn) error {
				return cond(e, v, func(c any) error {
					if jv.Truthy(c) {
						return out(v)
					}
					return update(e, v, func(u any) error { return loop(e, u, out) })
				})
			}
			return loop
		}),
		"while/2": withFilters(func(f []filter) filter {
			cond, update := f[0], f[1]
			var loop func(e *env, v any, out emitFn) error
			loop = func(e *env, v any, out emitFn) error {
				return cond(e, v, func(c any) error {
					if !jv.Truthy(c) {
						return nil
					}
					if err := out(v); err != nil {
						return err
					}
					return update(e, v, func(u any) error { return loop(e, u, out) })
				})
			}
			return loop
		}),
		"repeat/1": withFilters(func(f []filter) filter {
			// def repeat(f): def _repeat: f | (., _repeat); _repeat;
			var loop func(e *env, v any, out emitFn) error
			loop = func(e *env, v any, out emitFn) error {
				return f[0](e, v, func(u any) error {
					if err := out(u); err != nil {
						return err
					}
					return loop(e, u, out)
				})
			}
			return loop
		}),
		"walk/1": withFilters(func(f []filter) filter {
			return func(e *env, in any, out emitFn) error { return walk(e, f[0], in, out) }
		}),
		"IN/1": withFilters(func(f []filter) filter {
			return func(e *env, in any, out emitFn) error {
				found := false
				brk := newBreak()
				err := f[0](e, in, func(v any) error {
					if jv.Equal(v, in) {
						found = true
						return brk
					}
					return nil
				})
				if err != nil && err != brk {
					return err
				}
				return out(found)
			}
		}),
		"IN/2": withFilters(func(f []filter) filter {
			return func(e *env, in any, out emitFn) error {
				found := false
				brk := newBreak()
				err := f[0](e, in, func(x any) error {
					return f[1](e, in, func(v any) error {
						if jv.Equal(v, x) {
							found = true
							return brk
						}
						return nil
					})
				})
				if err != nil && err != brk {
					return err
				}
				return out(found)
			}
		}),
		"debug/0": func(*compiler, []node) (filter, error) {
			return func(e *env, in any, out emitFn) error {
				msg := []any{"DEBUG:", in}
				debugWrite(e, string(jv.Encode(nil, msg, jv.Format{Compact: true}))+"\n")
				return out(in)
			}, nil
		},
		"debug/1": withFilters(func(f []filter) filter {
			return func(e *env, in any, out emitFn) error {
				if err := f[0](e, in, func(m any) error {
					debugWrite(e, string(jv.Encode(nil, []any{"DEBUG:", m}, jv.Format{Compact: true}))+"\n")
					return nil
				}); err != nil {
					return err
				}
				return out(in)
			}
		}),
		"stderr/0": func(*compiler, []node) (filter, error) {
			return func(e *env, in any, out emitFn) error {
				debugWrite(e, string(jv.Encode(nil, in, jv.Format{Compact: true})))
				return out(in)
			}, nil
		},
		"getpath/1": withValues(func(in any, a []any) (any, error) {
			p, ok := jv.Norm(a[0]).([]any)
			if !ok {
				return nil, errorf("Path must be specified as an array")
			}
			return getPath(in, p)
		}),
		"setpath/2": withValues(func(in any, a []any) (any, error) {
			p, ok := jv.Norm(a[0]).([]any)
			if !ok {
				return nil, errorf("Path must be specified as an array")
			}
			return setPath(in, p, a[1])
		}),
		"delpaths/1": withValues(func(in any, a []any) (any, error) {
			ps, ok := jv.Norm(a[0]).([]any)
			if !ok {
				return nil, errorf("Paths must be specified as an array")
			}
			paths := make([][]any, len(ps))
			for i, p := range ps {
				if paths[i], ok = jv.Norm(p).([]any); !ok {
					return nil, errorf("Path must be specified as an array")
				}
			}
			return delPaths(in, paths)
		}),
		"path/1": func(c *compiler, args []node) (filter, error) {
			pf, err := c.compilePath(args[0])
			if err != nil {
				return nil, err
			}
			return func(e *env, in any, out emitFn) error {
				return runPaths(pf, e, in, func(p []any) error { return out(p) })
			}, nil
		},
		"paths/0": func(c *compiler, _ []node) (filter, error) {
			return pathsFilter(nil), nil
		},
		"paths/1": withFilters(func(f []filter) filter { return pathsFilter(f[0]) }),
		"del/1": func(c *compiler, args []node) (filter, error) {
			pf, err := c.compilePath(args[0])
			if err != nil {
				return nil, err
			}
			return func(e *env, in any, out emitFn) error {
				var ps [][]any
				if err := runPaths(pf, e, in, func(p []any) error { ps = append(ps, p); return nil }); err != nil {
					return err
				}
				v, err := delPaths(in, ps)
				if err != nil {
					return err
				}
				return out(v)
			}, nil
		},
		"pick/1": func(c *compiler, args []node) (filter, error) {
			pf, err := c.compilePath(args[0])
			if err != nil {
				return nil, err
			}
			return func(e *env, in any, out emitFn) error {
				var acc any
				err := runPaths(pf, e, in, func(p []any) error {
					v, err := getPath(in, p)
					if err != nil {
						return err
					}
					acc, err = setPath(acc, p, v)
					return err
				})
				if err != nil {
					return err
				}
				return out(acc)
			}, nil
		},
		"splits/1": regexBuiltin(func(re *jre, in string, out emitFn) error {
			for _, s := range re.split(in) {
				if err := out(s); err != nil {
					return err
				}
			}
			return nil
		}, 1),
		"splits/2": regexBuiltin(func(re *jre, in string, out emitFn) error {
			for _, s := range re.split(in) {
				if err := out(s); err != nil {
					return err
				}
			}
			return nil
		}, 2),
		"split/2": regexBuiltin(func(re *jre, in string, out emitFn) error { return out(re.split(in)) }, 2),
		"test/1":  regexBuiltin(func(re *jre, in string, out emitFn) error { return out(re.re.MatchString(in)) }, 1),
		"test/2":  regexBuiltin(func(re *jre, in string, out emitFn) error { return out(re.re.MatchString(in)) }, 2),
		"match/1": regexBuiltin(func(re *jre, in string, out emitFn) error { return re.matches(in, out) }, 1),
		"match/2": regexBuiltin(func(re *jre, in string, out emitFn) error { return re.matches(in, out) }, 2),
		"capture/1": regexBuiltin(func(re *jre, in string, out emitFn) error {
			return re.matches(in, func(m any) error { return out(captureObject(m)) })
		}, 1),
		"capture/2": regexBuiltin(func(re *jre, in string, out emitFn) error {
			return re.matches(in, func(m any) error { return out(captureObject(m)) })
		}, 2),
		"scan/1": regexBuiltin(func(re *jre, in string, out emitFn) error { return re.scan(in, out) }, 1),
		"scan/2": regexBuiltin(func(re *jre, in string, out emitFn) error { return re.scan(in, out) }, 2),
		"sub/2":  subBuiltin(false, 2),
		"sub/3":  subBuiltin(false, 3),
		"gsub/2": subBuiltin(true, 2),
		"gsub/3": subBuiltin(true, 3),
		"now/0": simple(func(any) (any, error) {
			return jv.Num(float64(time.Now().UnixNano()) / 1e9), nil
		}),
		"fromdateiso8601/0": simple(fromDate),
		"fromdate/0":        simple(fromDate),
	}
	addMoreBuiltins(builtins)
}

var notNull filter = func(_ *env, in any, out emitFn) error {
	if jv.KindOf(in) == jv.KNull {
		return nil
	}
	return out(in)
}

var iterateFilter filter = func(_ *env, in any, out emitFn) error { return iterate(in, out) }

func iterItems(in any) ([]any, error) {
	switch v := jv.Norm(in).(type) {
	case []any:
		return v, nil
	case *jv.Object:
		return v.Vals, nil
	}
	return nil, errorf("Cannot iterate over %s", describe(in))
}

func anyAll(gen, cond filter, isAny bool) filter {
	return func(e *env, in any, out emitFn) error {
		brk := newBreak()
		err := gen(e, in, func(v any) error {
			return cond(e, v, func(c any) error {
				if jv.Truthy(c) == isAny {
					return brk
				}
				return nil
			})
		})
		if err == brk {
			return out(isAny)
		}
		if err != nil {
			return err
		}
		return out(!isAny)
	}
}

func rangeOut(a, b, s any, out emitFn) error {
	from, ok1 := jv.AsNumber(a)
	upto, ok2 := jv.AsNumber(b)
	by, ok3 := jv.AsNumber(s)
	if !ok1 || !ok2 || !ok3 {
		return errorf("Range bounds must be numeric")
	}
	switch {
	case by.F > 0:
		for x := from.F; x < upto.F; x += by.F {
			if err := out(jv.Num(x)); err != nil {
				return err
			}
		}
	case by.F < 0:
		for x := from.F; x > upto.F; x += by.F {
			if err := out(jv.Num(x)); err != nil {
				return err
			}
		}
	default:
		if from.F < upto.F {
			for {
				if err := out(jv.Num(from.F)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// limitFilter emits at most n outputs of f; n comes from nf unless fixed >= 0.
func limitFilter(nf, f filter, fixed int) filter {
	run := func(e *env, in any, out emitFn, n float64) error {
		if n <= 0 {
			if n < 0 {
				return f(e, in, out)
			}
			return nil
		}
		k := 0.0
		brk := newBreak()
		err := f(e, in, func(v any) error {
			if err := out(v); err != nil {
				return err
			}
			k++
			if k >= n {
				return brk
			}
			return nil
		})
		if err == brk {
			return nil
		}
		return err
	}
	if fixed >= 0 {
		return func(e *env, in any, out emitFn) error { return run(e, in, out, float64(fixed)) }
	}
	return func(e *env, in any, out emitFn) error {
		return nf(e, in, func(nv any) error {
			n, ok := jv.AsNumber(nv)
			if !ok {
				return errorf("Invalid limit: must be a number")
			}
			return run(e, in, out, n.F)
		})
	}
}

func pathsFilter(f filter) filter {
	var rec func(e *env, root any, p []any, v any, out emitFn) error
	rec = func(e *env, root any, p []any, v any, out emitFn) error {
		return eachChild(pathItem{p, v}, func(ch pathItem) error {
			if f == nil {
				if err := out(ch.path); err != nil {
					return err
				}
			} else {
				err := f(e, ch.val, func(c any) error {
					if jv.Truthy(c) {
						return out(ch.path)
					}
					return nil
				})
				if err != nil {
					return err
				}
			}
			return rec(e, root, ch.path, ch.val, out)
		})
	}
	return func(e *env, in any, out emitFn) error { return rec(e, in, []any{}, in, out) }
}

// DebugSink receives debug and stderr messages; the engine binds one per
// worker so the messages keep their order.
type DebugSink interface{ WriteString(string) (int, error) }

const debugVar = "\x00debug"

func debugWrite(e *env, s string) {
	if v, ok := e.lookup(debugVar); ok {
		if sink, ok := v.(DebugSink); ok {
			sink.WriteString(s)
			return
		}
	}
	os.Stderr.WriteString(s)
}

func timeOf(in any, name string) (time.Time, error) {
	n, ok := jv.AsNumber(in)
	if !ok {
		return time.Time{}, errorf("%s requires parsed datetime inputs", name)
	}
	sec, frac := math.Modf(n.F)
	return time.Unix(int64(sec), int64(frac*1e9)).UTC(), nil
}

func fromDate(in any) (any, error) {
	if jv.KindOf(in) != jv.KString {
		return nil, errorf("strptime/1 requires string inputs and arguments")
	}
	s := jv.Str(in)
	t, err := time.Parse("2006-01-02T15:04:05Z", s)
	if err != nil {
		return nil, errorf("date \"%s\" does not match format \"%%Y-%%m-%%dT%%H:%%M:%%SZ\"", s)
	}
	return jv.Num(float64(t.Unix())), nil
}

func toDate(in any) (any, error) {
	t, err := timeOf(in, "strftime/1")
	if err != nil {
		return nil, err
	}
	return t.Format("2006-01-02T15:04:05Z"), nil
}

func strftime(t time.Time, f string) string {
	var sb strings.Builder
	for i := 0; i < len(f); i++ {
		if f[i] != '%' || i+1 == len(f) {
			sb.WriteByte(f[i])
			continue
		}
		i++
		switch f[i] {
		case 'Y':
			sb.WriteString(strconv.Itoa(t.Year()))
		case 'm':
			fmt.Fprintf(&sb, "%02d", int(t.Month()))
		case 'd':
			fmt.Fprintf(&sb, "%02d", t.Day())
		case 'e':
			fmt.Fprintf(&sb, "%2d", t.Day())
		case 'H':
			fmt.Fprintf(&sb, "%02d", t.Hour())
		case 'I':
			h := t.Hour() % 12
			if h == 0 {
				h = 12
			}
			fmt.Fprintf(&sb, "%02d", h)
		case 'M':
			fmt.Fprintf(&sb, "%02d", t.Minute())
		case 'S':
			fmt.Fprintf(&sb, "%02d", t.Second())
		case 'p':
			if t.Hour() < 12 {
				sb.WriteString("AM")
			} else {
				sb.WriteString("PM")
			}
		case 'j':
			fmt.Fprintf(&sb, "%03d", t.YearDay())
		case 'a':
			sb.WriteString(t.Weekday().String()[:3])
		case 'A':
			sb.WriteString(t.Weekday().String())
		case 'b', 'h':
			sb.WriteString(t.Month().String()[:3])
		case 'B':
			sb.WriteString(t.Month().String())
		case 'y':
			fmt.Fprintf(&sb, "%02d", t.Year()%100)
		case 'Z':
			sb.WriteString("UTC")
		case 'z':
			sb.WriteString("+0000")
		case 's':
			sb.WriteString(strconv.FormatInt(t.Unix(), 10))
		case 'u':
			wd := int(t.Weekday())
			if wd == 0 {
				wd = 7
			}
			sb.WriteString(strconv.Itoa(wd))
		case 'w':
			sb.WriteString(strconv.Itoa(int(t.Weekday())))
		case 'T':
			fmt.Fprintf(&sb, "%02d:%02d:%02d", t.Hour(), t.Minute(), t.Second())
		case 'F':
			fmt.Fprintf(&sb, "%d-%02d-%02d", t.Year(), int(t.Month()), t.Day())
		case 'D':
			fmt.Fprintf(&sb, "%02d/%02d/%02d", int(t.Month()), t.Day(), t.Year()%100)
		case 'R':
			fmt.Fprintf(&sb, "%02d:%02d", t.Hour(), t.Minute())
		case 'n':
			sb.WriteByte('\n')
		case 't':
			sb.WriteByte('\t')
		case '%':
			sb.WriteByte('%')
		default:
			sb.WriteByte('%')
			sb.WriteByte(f[i])
		}
	}
	return sb.String()
}
