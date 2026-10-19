// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package query

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/just-very-quickly/query/internal/jv"
)

// The rest of jq 1.7's builtins: more math, streams, joins, dates.

func math2(f func(a, b float64) float64) builtinFn {
	return withValues(func(_ any, a []any) (any, error) {
		x, ok1 := jv.AsNumber(a[0])
		y, ok2 := jv.AsNumber(a[1])
		if !ok1 || !ok2 {
			return nil, errorf("%s and %s are not both numbers", describe(a[0]), describe(a[1]))
		}
		return jv.Num(f(x.F, y.F)), nil
	})
}

func pairFn(f func(x float64) (float64, float64)) builtinFn {
	return simple(func(in any) (any, error) {
		n, ok := jv.AsNumber(in)
		if !ok {
			return nil, errorf("%s number required", describe(in))
		}
		a, b := f(n.F)
		return []any{jv.Num(a), jv.Num(b)}, nil
	})
}

func addMoreBuiltins(m map[string]builtinFn) {
	for name, f := range map[string]func(float64) float64{
		"acos": math.Acos, "acosh": math.Acosh, "asin": math.Asin, "asinh": math.Asinh,
		"atan": math.Atan, "atanh": math.Atanh, "cbrt": math.Cbrt, "cos": math.Cos,
		"cosh": math.Cosh, "sin": math.Sin, "sinh": math.Sinh, "tan": math.Tan, "tanh": math.Tanh,
		"expm1": math.Expm1, "log1p": math.Log1p, "logb": math.Logb, "tgamma": math.Gamma,
		"gamma":  func(x float64) float64 { v, _ := math.Lgamma(x); return v },
		"lgamma": func(x float64) float64 { v, _ := math.Lgamma(x); return v },
		"j0":     math.J0, "j1": math.J1, "y0": math.Y0, "y1": math.Y1,
		"rint": math.RoundToEven, "nearbyint": math.RoundToEven, "pow10": func(x float64) float64 { return math.Pow(10, x) },
		"significand": func(x float64) float64 {
			if x == 0 || math.IsInf(x, 0) || math.IsNaN(x) {
				return x
			}
			f, _ := math.Frexp(x)
			return f * 2
		},
	} {
		m[name+"/0"] = mathFn(f)
	}
	for name, f := range map[string]func(a, b float64) float64{
		"atan2": math.Atan2, "fmin": math.Min, "fmax": math.Max, "fmod": math.Mod, "hypot": math.Hypot,
		"copysign": math.Copysign, "drem": math.Remainder, "remainder": math.Remainder, "nextafter": math.Nextafter,
		"nexttoward": math.Nextafter, "fdim": func(a, b float64) float64 { return math.Max(a-b, 0) },
		"ldexp":   func(a, b float64) float64 { return math.Ldexp(a, int(b)) },
		"scalb":   func(a, b float64) float64 { return a * math.Pow(2, b) },
		"scalbln": func(a, b float64) float64 { return math.Ldexp(a, int(b)) },
		"jn":      func(n, x float64) float64 { return math.Jn(int(n), x) },
		"yn":      func(n, x float64) float64 { return math.Yn(int(n), x) },
	} {
		m[name+"/2"] = math2(f)
	}
	m["fma/3"] = withValues(func(_ any, a []any) (any, error) {
		var f [3]float64
		for i := range f {
			n, ok := jv.AsNumber(a[i])
			if !ok {
				return nil, errorf("fma requires numbers")
			}
			f[i] = n.F
		}
		return jv.Num(math.FMA(f[0], f[1], f[2])), nil
	})
	m["frexp/0"] = simple(func(in any) (any, error) {
		n, ok := jv.AsNumber(in)
		if !ok {
			return nil, errorf("%s number required", describe(in))
		}
		f, e := math.Frexp(n.F)
		return []any{jv.Num(f), jv.Num(float64(e))}, nil
	})
	m["modf/0"] = pairFn(func(x float64) (float64, float64) {
		i, f := math.Modf(x)
		return f, i
	})
	m["lgamma_r/0"] = pairFn(func(x float64) (float64, float64) {
		v, s := math.Lgamma(x)
		return v, float64(s)
	})
	m["abs/0"] = simple(func(in any) (any, error) {
		if n, ok := jv.AsNumber(in); ok {
			if n.F < 0 {
				return jv.Num(-n.F), nil
			}
			return in, nil
		}
		return nil, errorf("%s has no absolute value", describe(in))
	})
	m["finites/0"] = func(*compiler, []node) (filter, error) {
		return func(_ *env, in any, out emitFn) error {
			if n, ok := jv.AsNumber(in); ok && !math.IsInf(n.F, 0) && !math.IsNaN(n.F) {
				return out(in)
			}
			return nil
		}, nil
	}
	m["normals/0"] = func(*compiler, []node) (filter, error) {
		return func(_ *env, in any, out emitFn) error {
			if n, ok := jv.AsNumber(in); ok {
				f := math.Abs(n.F)
				if f >= 2.2250738585072014e-308 && !math.IsInf(f, 0) {
					return out(in)
				}
			}
			return nil
		}, nil
	}
	m["transpose/0"] = simple(func(in any) (any, error) {
		rows, ok := jv.Norm(in).([]any)
		if !ok {
			return nil, errorf("Cannot iterate over %s", describe(in))
		}
		maxLen := 0
		for _, r := range rows {
			l, err := lengthOf(r)
			if err != nil {
				return nil, err
			}
			if n := int(l.(jv.Number).F); n > maxLen {
				maxLen = n
			}
		}
		out := make([]any, maxLen)
		for j := 0; j < maxLen; j++ {
			col := make([]any, len(rows))
			for i, r := range rows {
				v, err := indexValue(r, jv.Num(float64(j)))
				if err != nil {
					return nil, err
				}
				col[i] = v
			}
			out[j] = col
		}
		return out, nil
	})
	m["combinations/0"] = func(*compiler, []node) (filter, error) { return combinations, nil }
	m["combinations/1"] = withFilters(func(f []filter) filter {
		return func(e *env, in any, out emitFn) error {
			return f[0](e, in, func(nv any) error {
				n, ok := jv.AsNumber(nv)
				if !ok {
					return errorf("Range bounds must be numeric")
				}
				arrs := []any{}
				for i := 0.0; i < n.F; i++ {
					arrs = append(arrs, in)
				}
				return combinations(e, arrs, out)
			})
		}
	})
	m["tostream/0"] = func(*compiler, []node) (filter, error) {
		return func(_ *env, in any, out emitFn) error { return toStream([]any{}, in, true, out) }, nil
	}
	m["fromstream/1"] = withFilters(func(f []filter) filter { return fromStream(f[0]) })
	m["truncate_stream/1"] = withFilters(func(f []filter) filter {
		return func(e *env, in any, out emitFn) error {
			depth, ok := jv.AsNumber(in)
			if !ok {
				return errorf("truncate_stream depth must be a number")
			}
			return f[0](e, in, func(ev any) error {
				a, ok := jv.Norm(ev).([]any)
				if !ok || len(a) == 0 {
					return errorf("Invalid stream event %s", describe(ev))
				}
				p, ok := jv.Norm(a[0]).([]any)
				if !ok {
					return errorf("Invalid stream event %s", describe(ev))
				}
				d := int(depth.F)
				if len(p) <= d {
					return nil
				}
				na := append([]any{p[d:]}, a[1:]...)
				return out(na)
			})
		}
	})
	m["INDEX/2"] = withFilters(func(f []filter) filter { return indexFilter(f[0], f[1]) })
	m["INDEX/1"] = withFilters(func(f []filter) filter { return indexFilter(iterateFilter, f[0]) })
	m["JOIN/2"] = withFilters(func(f []filter) filter {
		return func(e *env, in any, out emitFn) error {
			return f[0](e, in, func(idx any) error {
				rows := []any{}
				err := iterate(in, func(row any) error {
					return f[1](e, row, func(k any) error {
						v, err := indexValue(idx, k)
						if err != nil {
							return err
						}
						rows = append(rows, []any{row, v})
						return nil
					})
				})
				if err != nil {
					return err
				}
				return out(rows)
			})
		}
	})
	m["JOIN/3"] = withFilters(func(f []filter) filter { return joinFilter(f[0], f[1], f[2], nil) })
	m["JOIN/4"] = withFilters(func(f []filter) filter { return joinFilter(f[0], f[1], f[2], f[3]) })
	m["bsearch/1"] = withValues(func(in any, a []any) (any, error) {
		arr, ok := jv.Norm(in).([]any)
		if !ok {
			return nil, errorf("%s cannot be searched from", describe(in))
		}
		i := sort.Search(len(arr), func(i int) bool { return jv.Compare(arr[i], a[0]) >= 0 })
		if i < len(arr) && jv.Compare(arr[i], a[0]) == 0 {
			return jv.Num(float64(i)), nil
		}
		return jv.Num(float64(-1 - i)), nil
	})
	m["format/1"] = withValues(func(in any, a []any) (any, error) {
		if jv.KindOf(a[0]) != jv.KString {
			return nil, errorf("%s is not a valid format", describe(a[0]))
		}
		fm, err := formatter(jv.Str(a[0]))
		if err != nil {
			return nil, errorf("%s is not a valid format", jv.Str(a[0]))
		}
		return fm(in)
	})
	m["builtins/0"] = simple(func(any) (any, error) {
		names := make([]string, 0, len(builtins))
		for k := range builtins {
			names = append(names, k)
		}
		sort.Strings(names)
		out := make([]any, len(names))
		for i, k := range names {
			out[i] = k
		}
		return out, nil
	})
	m["input_filename/0"] = func(*compiler, []node) (filter, error) {
		return func(e *env, _ any, out emitFn) error {
			v, _ := e.lookup(fileVar)
			return out(v)
		}, nil
	}
	m["input/0"] = func(*compiler, []node) (filter, error) {
		return func(e *env, _ any, out emitFn) error {
			src, _ := e.lookup(inputsVar)
			if s, ok := src.(InputSource); ok {
				v, ok, err := s.Next()
				if err != nil {
					return err
				}
				if ok {
					return out(v)
				}
			}
			return errorf("No more inputs")
		}, nil
	}
	m["inputs/0"] = func(*compiler, []node) (filter, error) {
		return func(e *env, _ any, out emitFn) error {
			src, _ := e.lookup(inputsVar)
			s, ok := src.(InputSource)
			if !ok {
				return nil
			}
			for {
				v, ok, err := s.Next()
				if err != nil {
					return err
				}
				if !ok {
					return nil
				}
				if err := out(v); err != nil {
					return err
				}
			}
		}, nil
	}
	m["halt/0"] = simple(func(any) (any, error) { return nil, &HaltError{Code: 0} })
	m["halt_error/0"] = simple(func(in any) (any, error) { return nil, &HaltError{Code: 5, Value: in, HasValue: true} })
	m["halt_error/1"] = withValues(func(in any, a []any) (any, error) {
		n, ok := jv.AsNumber(a[0])
		if !ok {
			return nil, errorf("halt_error/1: number required")
		}
		return nil, &HaltError{Code: int(n.F), Value: in, HasValue: true}
	})
	m["gmtime/0"] = simple(func(in any) (any, error) {
		n, ok := jv.AsNumber(in)
		if !ok {
			return nil, errorf("gmtime() requires a number")
		}
		return brokenDown(n.F, time.UTC), nil
	})
	m["localtime/0"] = simple(func(in any) (any, error) {
		n, ok := jv.AsNumber(in)
		if !ok {
			return nil, errorf("localtime() requires a number")
		}
		return brokenDown(n.F, time.Local), nil
	})
	m["mktime/0"] = simple(func(in any) (any, error) {
		t, err := fromBrokenDown(in, "mktime", time.UTC)
		if err != nil {
			return nil, err
		}
		return jv.Num(float64(t.Unix())), nil
	})
	m["strptime/1"] = withValues(func(in any, a []any) (any, error) {
		if jv.KindOf(in) != jv.KString || jv.KindOf(a[0]) != jv.KString {
			return nil, errorf("strptime/1 requires string inputs and arguments")
		}
		t, err := strptime(jv.Str(in), jv.Str(a[0]))
		if err != nil {
			return nil, err
		}
		return brokenDown(float64(t.Unix())+float64(t.Nanosecond())/1e9, time.UTC), nil
	})
	strf := func(loc *time.Location, name string) builtinFn {
		return withValues(func(in any, a []any) (any, error) {
			if jv.KindOf(a[0]) != jv.KString {
				return nil, errorf("%s requires a string format", name)
			}
			var t time.Time
			var err error
			if jv.KindOf(in) == jv.KArray {
				t, err = fromBrokenDown(in, name, loc)
			} else {
				t, err = timeOf(in, name)
				t = t.In(loc)
			}
			if err != nil {
				return nil, err
			}
			return strftime(t, jv.Str(a[0])), nil
		})
	}
	m["strftime/1"] = strf(time.UTC, "strftime/1")
	m["strflocaltime/1"] = strf(time.Local, "strflocaltime/1")
	m["todateiso8601/0"] = simple(func(in any) (any, error) {
		if jv.KindOf(in) == jv.KArray {
			t, err := fromBrokenDown(in, "strftime/1", time.UTC)
			if err != nil {
				return nil, err
			}
			return t.Format("2006-01-02T15:04:05Z"), nil
		}
		return toDate(in)
	})
	m["todate/0"] = m["todateiso8601/0"]
}

// HaltError ends the whole run: halt (code 0, nothing printed) or
// halt_error (the value printed to standard error).
type HaltError struct {
	Code     int
	Value    any
	HasValue bool
}

func (h *HaltError) Error() string { return "halt" }

// Message is what halt_error prints: a string as is, anything else as JSON
// on its own line.
func (h *HaltError) Message() string {
	if !h.HasValue {
		return ""
	}
	if jv.KindOf(h.Value) == jv.KString {
		return jv.Str(h.Value)
	}
	return string(jv.Encode(nil, h.Value, jv.Format{Compact: true})) + "\n"
}

const fileVar = "\x00file"

func combinations(e *env, in any, out emitFn) error {
	arrs, ok := jv.Norm(in).([]any)
	if !ok {
		return errorf("Cannot iterate over %s", describe(in))
	}
	cur := make([]any, len(arrs))
	var rec func(i int) error
	rec = func(i int) error {
		if i == len(arrs) {
			return out(append([]any(nil), cur...))
		}
		return iterate(arrs[i], func(v any) error {
			cur[i] = v
			return rec(i + 1)
		})
	}
	return rec(0)
}

// toStream emits jq's streaming form of v at path p: [path, leaf] for each
// leaf, and [path] closing each array or object after its last member.
func toStream(p []any, v any, top bool, out emitFn) error {
	n := jv.Norm(v)
	var keys []any
	var vals []any
	switch x := n.(type) {
	case []any:
		for i, c := range x {
			keys = append(keys, jv.Num(float64(i)))
			vals = append(vals, c)
		}
	case *jv.Object:
		for i, k := range x.Keys {
			keys = append(keys, k)
			vals = append(vals, x.Vals[i])
		}
	}
	if len(keys) == 0 {
		return out([]any{append([]any{}, p...), n})
	}
	for i, k := range keys {
		if err := toStream(appendPath(p, k), vals[i], false, out); err != nil {
			return err
		}
	}
	return out([]any{appendPath(p, keys[len(keys)-1])})
}

func fromStream(f filter) filter {
	return func(e *env, in any, out emitFn) error {
		var x any
		emitted := false
		return f(e, in, func(ev any) error {
			if emitted {
				x, emitted = nil, false
			}
			a, ok := jv.Norm(ev).([]any)
			if !ok || len(a) == 0 {
				return errorf("Invalid stream event %s", describe(ev))
			}
			p, ok := jv.Norm(a[0]).([]any)
			if !ok {
				return errorf("Invalid stream event %s", describe(ev))
			}
			if len(a) == 2 {
				nx, err := setPath(x, p, a[1])
				if err != nil {
					return err
				}
				x = nx
				if len(p) == 0 {
					emitted = true
					return out(x)
				}
				return nil
			}
			if len(p) == 1 {
				emitted = true
				return out(x)
			}
			return nil
		})
	}
}

func indexFilter(stream, key filter) filter {
	return func(e *env, in any, out emitFn) error {
		o := jv.NewObject(8)
		err := stream(e, in, func(row any) error {
			return key(e, row, func(k any) error {
				o.Set(toString(k), row)
				return nil
			})
		})
		if err != nil {
			return err
		}
		return out(o)
	}
}

func joinFilter(idxF, stream, key, join filter) filter {
	return func(e *env, in any, out emitFn) error {
		return idxF(e, in, func(idx any) error {
			return stream(e, in, func(row any) error {
				return key(e, row, func(k any) error {
					v, err := indexValue(idx, k)
					if err != nil {
						return err
					}
					pair := []any{row, v}
					if join == nil {
						return out(pair)
					}
					return join(e, pair, out)
				})
			})
		})
	}
}

// brokenDown is jq's "broken down time": [year, month (0-11), day,
// hours, minutes, seconds (with fraction), weekday, day of the year (0-)].
func brokenDown(secs float64, loc *time.Location) []any {
	whole, frac := math.Modf(secs)
	t := time.Unix(int64(whole), 0).In(loc)
	return []any{
		jv.Num(float64(t.Year())), jv.Num(float64(t.Month() - 1)), jv.Num(float64(t.Day())),
		jv.Num(float64(t.Hour())), jv.Num(float64(t.Minute())), jv.Num(float64(t.Second()) + frac),
		jv.Num(float64(t.Weekday())), jv.Num(float64(t.YearDay() - 1)),
	}
}

func fromBrokenDown(in any, name string, loc *time.Location) (time.Time, error) {
	a, ok := jv.Norm(in).([]any)
	if !ok || len(a) < 6 {
		return time.Time{}, errorf("%s requires array of 6 numbers", name)
	}
	var f [6]float64
	for i := 0; i < 6; i++ {
		n, ok := jv.AsNumber(a[i])
		if !ok {
			return time.Time{}, errorf("%s requires parsed datetime inputs", name)
		}
		f[i] = n.F
	}
	return time.Date(int(f[0]), time.Month(int(f[1])+1), int(f[2]), int(f[3]), int(f[4]), int(f[5]), 0, loc), nil
}

// strptime parses s with a strftime-style format. Like jq with glibc, a
// %z offset is read but not applied.
func strptime(s, format string) (time.Time, error) {
	fail := func() (time.Time, error) {
		return time.Time{}, errorf("date \"%s\" does not match format \"%s\"", s, format)
	}
	year, month, day, hour, min, sec := 1900, 1, 1, 0, 0, 0
	pm, has12 := false, false
	i := 0
	num := func(maxDigits int) (int, bool) {
		j := i
		neg := false
		if j < len(s) && (s[j] == '+' || s[j] == '-') {
			neg = s[j] == '-'
			j++
		}
		start := j
		for j < len(s) && j-start < maxDigits && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j == start {
			return 0, false
		}
		n, _ := strconv.Atoi(s[start:j])
		i = j
		if neg {
			n = -n
		}
		return n, true
	}
	name := func(names []string) (int, bool) {
		for k, n := range names {
			if len(s)-i >= len(n) && strings.EqualFold(s[i:i+len(n)], n) {
				i += len(n)
				return k, true
			}
		}
		for k, n := range names {
			if len(s)-i >= 3 && strings.EqualFold(s[i:i+3], n[:3]) {
				i += 3
				return k, true
			}
		}
		return 0, false
	}
	months := []string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"}
	days := []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
	for k := 0; k < len(format); k++ {
		c := format[k]
		if c != '%' {
			if c == ' ' {
				for i < len(s) && s[i] == ' ' {
					i++
				}
				continue
			}
			if i >= len(s) || s[i] != c {
				return fail()
			}
			i++
			continue
		}
		k++
		if k >= len(format) {
			return fail()
		}
		var ok bool
		switch format[k] {
		case 'Y':
			year, ok = num(4)
		case 'm':
			month, ok = num(2)
		case 'd', 'e':
			for i < len(s) && s[i] == ' ' {
				i++
			}
			day, ok = num(2)
		case 'H':
			hour, ok = num(2)
		case 'I':
			hour, ok = num(2)
			has12 = true
		case 'M':
			min, ok = num(2)
		case 'S':
			sec, ok = num(2)
		case 'y':
			var y int
			y, ok = num(2)
			if y < 69 {
				year = 2000 + y
			} else {
				year = 1900 + y
			}
		case 'j':
			var yd int
			yd, ok = num(3)
			month, day = 1, yd
		case 'b', 'B', 'h':
			var mo int
			mo, ok = name(months)
			month = mo + 1
		case 'a', 'A':
			_, ok = name(days)
		case 'p':
			if len(s)-i >= 2 {
				switch strings.ToUpper(s[i : i+2]) {
				case "AM":
					ok = true
				case "PM":
					ok, pm = true, true
				}
				i += 2
			}
		case 'z':
			if i < len(s) && (s[i] == 'Z' || s[i] == 'z') {
				i++
				ok = true
			} else {
				_, ok = num(4)
				if ok && i < len(s) && s[i] == ':' {
					i++
					_, ok = num(2)
				}
			}
		case 'Z':
			j := i
			for i < len(s) && (s[i] >= 'A' && s[i] <= 'Z' || s[i] >= 'a' && s[i] <= 'z') {
				i++
			}
			ok = i > j
		case 's':
			var n int
			n, ok = num(20)
			if ok {
				t := time.Unix(int64(n), 0).UTC()
				year, month, day = t.Year(), int(t.Month()), t.Day()
				hour, min, sec = t.Hour(), t.Minute(), t.Second()
			}
		case 'T':
			hour, ok = num(2)
			if ok && i < len(s) && s[i] == ':' {
				i++
				min, ok = num(2)
				if ok && i < len(s) && s[i] == ':' {
					i++
					sec, ok = num(2)
				} else {
					ok = false
				}
			} else {
				ok = false
			}
		case 'F':
			year, ok = num(4)
			if ok && i < len(s) && s[i] == '-' {
				i++
				month, ok = num(2)
				if ok && i < len(s) && s[i] == '-' {
					i++
					day, ok = num(2)
				} else {
					ok = false
				}
			} else {
				ok = false
			}
		case '%':
			ok = i < len(s) && s[i] == '%'
			i++
		case 'n', 't':
			for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n') {
				i++
			}
			ok = true
		default:
			return time.Time{}, errorf("strptime: %%%c is not supported", format[k])
		}
		if !ok {
			return fail()
		}
	}
	if i != len(s) {
		return fail()
	}
	if has12 {
		hour %= 12
		if pm {
			hour += 12
		}
	}
	return time.Date(year, time.Month(month), day, hour, min, sec, 0, time.UTC), nil
}
