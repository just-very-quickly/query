// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package query

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/just-very-quickly/query/internal/jv"
)

// formatter returns the @format function name stands for.
func formatter(name string) (func(any) (any, error), error) {
	switch name {
	case "", "text":
		return func(v any) (any, error) { return toString(v), nil }, nil
	case "json":
		return func(v any) (any, error) { return string(jv.Encode(nil, v, jv.Format{Compact: true})), nil }, nil
	case "csv", "tsv":
		csv := name == "csv"
		return func(v any) (any, error) {
			a, ok := jv.Norm(v).([]any)
			if !ok {
				return nil, errorf("%s cannot be %s-formatted, only an array can be", describe(v), name)
			}
			var sb strings.Builder
			for i, x := range a {
				if i > 0 {
					if csv {
						sb.WriteByte(',')
					} else {
						sb.WriteByte('\t')
					}
				}
				switch jv.KindOf(x) {
				case jv.KNull:
				case jv.KTrue:
					sb.WriteString("true")
				case jv.KFalse:
					sb.WriteString("false")
				case jv.KNumber:
					sb.WriteString(toString(x))
				case jv.KString:
					s := jv.Str(x)
					if csv {
						sb.WriteByte('"')
						sb.WriteString(strings.ReplaceAll(s, `"`, `""`))
						sb.WriteByte('"')
					} else {
						sb.WriteString(tsvEscaper.Replace(s))
					}
				default:
					return nil, errorf("%s is not valid in a %s row", describe(x), name)
				}
			}
			return sb.String(), nil
		}, nil
	case "html":
		return func(v any) (any, error) { return htmlEscaper.Replace(toString(v)), nil }, nil
	case "uri":
		return func(v any) (any, error) {
			s := toString(v)
			var sb strings.Builder
			for i := 0; i < len(s); i++ {
				c := s[i]
				if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' {
					sb.WriteByte(c)
				} else {
					fmt.Fprintf(&sb, "%%%02X", c)
				}
			}
			return sb.String(), nil
		}, nil
	case "sh":
		quote := func(x any) (string, error) {
			switch jv.KindOf(x) {
			case jv.KString:
				return "'" + strings.ReplaceAll(jv.Str(x), "'", `'\''`) + "'", nil
			case jv.KArray, jv.KObject:
				return "", errorf("%s can not be escaped for shell", describe(x))
			}
			return toString(x), nil
		}
		return func(v any) (any, error) {
			if a, ok := jv.Norm(v).([]any); ok {
				parts := make([]string, len(a))
				for i, x := range a {
					q, err := quote(x)
					if err != nil {
						return nil, err
					}
					parts[i] = q
				}
				return strings.Join(parts, " "), nil
			}
			return quote(v)
		}, nil
	case "base64":
		return func(v any) (any, error) { return base64.StdEncoding.EncodeToString([]byte(toString(v))), nil }, nil
	case "base64d":
		return func(v any) (any, error) {
			s := strings.TrimRight(toString(v), "=")
			b, err := base64.RawStdEncoding.DecodeString(s)
			if err != nil {
				return nil, errorf("%s is not valid base64 data", describe(v))
			}
			return validUTF8(b), nil
		}, nil
	}
	// Like jq, an unknown format fails when it runs.
	return func(any) (any, error) { return nil, errorf("%s is not a valid format", name) }, nil
}

func validUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	return strings.ToValidUTF8(string(b), "�")
}

var tsvEscaper = strings.NewReplacer(`\`, `\\`, "\t", `\t`, "\n", `\n`, "\r", `\r`)
var htmlEscaper = strings.NewReplacer("<", "&lt;", ">", "&gt;", "&", "&amp;", "'", "&apos;", `"`, "&quot;")

// jre is a regular expression with jq's flags.
type jre struct {
	re      *regexp.Regexp
	global  bool
	noEmpty bool
}

func compileRegex(pattern, flags string) (*jre, error) {
	j := &jre{}
	prefix := ""
	extended := false
	for _, f := range flags {
		switch f {
		case 'g':
			j.global = true
		case 'i':
			prefix += "i"
		case 'x':
			extended = true
		case 'n':
			j.noEmpty = true
		case 's':
			// single-line mode: ^ and $ match only at the ends, as in Go
		case 'p':
			prefix += "s"
		case 'l':
		default:
			return nil, errorf("%s is not a valid modifier string", flags)
		}
	}
	if extended {
		pattern = stripExtended(pattern)
	}
	if prefix != "" {
		pattern = "(?" + prefix + ")" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, errorf("%s (at offset 0) is not a valid regex: %s", pattern, err.Error())
	}
	if strings.ContainsRune(flags, 'l') {
		re.Longest()
	}
	j.re = re
	return j, nil
}

// stripExtended removes whitespace and # comments outside character classes.
func stripExtended(p string) string {
	var sb strings.Builder
	inClass := false
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '\\' && i+1 < len(p):
			sb.WriteByte(c)
			sb.WriteByte(p[i+1])
			i++
		case inClass:
			if c == ']' {
				inClass = false
			}
			sb.WriteByte(c)
		case c == '[':
			inClass = true
			sb.WriteByte(c)
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
		case c == '#':
			for i < len(p) && p[i] != '\n' {
				i++
			}
		default:
			sb.WriteByte(c)
		}
	}
	return sb.String()
}

// runeOffsets converts byte offsets in s to codepoint offsets.
type runeOffsets struct {
	s     string
	b, cp int
}

func (r *runeOffsets) at(b int) int {
	if b < r.b {
		r.b, r.cp = 0, 0
	}
	r.cp += utf8.RuneCountInString(r.s[r.b:b])
	r.b = b
	return r.cp
}

func (j *jre) find(s string) [][]int {
	n := 1
	if j.global {
		n = -1
	}
	ms := j.re.FindAllStringSubmatchIndex(s, n)
	if j.noEmpty {
		out := ms[:0]
		for _, m := range ms {
			if m[1] > m[0] {
				out = append(out, m)
			}
		}
		ms = out
	}
	return ms
}

func (j *jre) matchObject(s string, m []int, ro *runeOffsets) *jv.Object {
	o := jv.NewObject(4)
	start := ro.at(m[0])
	end := ro.at(m[1])
	o.Set("offset", jv.Num(float64(start)))
	o.Set("length", jv.Num(float64(end-start)))
	o.Set("string", s[m[0]:m[1]])
	caps := []any{}
	names := j.re.SubexpNames()
	for g := 1; g < len(names); g++ {
		c := jv.NewObject(4)
		a, b := m[2*g], m[2*g+1]
		if a < 0 {
			c.Set("offset", jv.Num(-1))
			c.Set("length", jv.Num(0))
			c.Set("string", nil)
		} else {
			cs := ro.at(a)
			ce := ro.at(b)
			c.Set("offset", jv.Num(float64(cs)))
			c.Set("length", jv.Num(float64(ce-cs)))
			c.Set("string", s[a:b])
		}
		if names[g] != "" {
			c.Set("name", names[g])
		} else {
			c.Set("name", nil)
		}
		caps = append(caps, c)
	}
	o.Set("captures", caps)
	return o
}

func (j *jre) matches(s string, out emitFn) error {
	ro := &runeOffsets{s: s}
	for _, m := range j.find(s) {
		if err := out(j.matchObject(s, m, ro)); err != nil {
			return err
		}
	}
	return nil
}

func (j *jre) split(s string) []any {
	g := *j
	g.global = true
	out := []any{}
	prev := 0
	for _, m := range g.find(s) {
		out = append(out, s[prev:m[0]])
		prev = m[1]
	}
	return append(out, s[prev:])
}

func (j *jre) scan(s string, out emitFn) error {
	g := *j
	g.global = true
	for _, m := range g.find(s) {
		if len(m) > 2 {
			caps := []any{}
			for k := 2; k < len(m); k += 2 {
				if m[k] < 0 {
					caps = append(caps, nil)
				} else {
					caps = append(caps, s[m[k]:m[k+1]])
				}
			}
			if err := out(caps); err != nil {
				return err
			}
			continue
		}
		if err := out(s[m[0]:m[1]]); err != nil {
			return err
		}
	}
	return nil
}

func captureObject(m any) any {
	mo := m.(*jv.Object)
	caps, _ := mo.Get("captures")
	o := jv.NewObject(4)
	for _, c := range caps.([]any) {
		co := c.(*jv.Object)
		name, _ := co.Get("name")
		if name == nil {
			continue
		}
		str, _ := co.Get("string")
		o.Set(name.(string), str)
	}
	return o
}

// regexBuiltin builds test, match, capture, scan and split with a regex
// argument and optional flags (arity 1 or 2).
func regexBuiltin(f func(re *jre, in string, out emitFn) error, arity int) builtinFn {
	return withFilters(func(args []filter) filter {
		return func(e *env, in any, out emitFn) error {
			if jv.KindOf(in) != jv.KString {
				return errorf("%s cannot be matched, as it is not a string", describe(in))
			}
			s := jv.Str(in)
			runFlags := func(re any) error {
				if jv.KindOf(re) != jv.KString {
					// test(["re", "flags"]) form
					if a, ok := jv.Norm(re).([]any); ok && len(a) > 0 && arity == 1 {
						flags := ""
						if len(a) > 1 && jv.KindOf(a[1]) == jv.KString {
							flags = jv.Str(a[1])
						}
						r, err := compileCached(jv.Str(a[0]), flags)
						if err != nil {
							return err
						}
						return f(r, s, out)
					}
					return errorf("%s cannot be matched, as it is not a string", describe(re))
				}
				if arity == 1 {
					r, err := compileCached(jv.Str(re), "")
					if err != nil {
						return err
					}
					return f(r, s, out)
				}
				return args[1](e, in, func(fl any) error {
					flags := ""
					if fl != nil {
						if jv.KindOf(fl) != jv.KString {
							return errorf("%s is not a string", describe(fl))
						}
						flags = jv.Str(fl)
					}
					r, err := compileCached(jv.Str(re), flags)
					if err != nil {
						return err
					}
					return f(r, s, out)
				})
			}
			return args[0](e, in, runFlags)
		}
	})
}

func subBuiltin(global bool, arity int) builtinFn {
	return withFilters(func(args []filter) filter {
		return func(e *env, in any, out emitFn) error {
			if jv.KindOf(in) != jv.KString {
				return errorf("%s cannot be matched, as it is not a string", describe(in))
			}
			s := jv.Str(in)
			flagsF := filter(func(_ *env, _ any, out emitFn) error { return out("") })
			if arity == 3 {
				flagsF = args[2]
			}
			return args[0](e, in, func(re any) error {
				if jv.KindOf(re) != jv.KString {
					return errorf("%s cannot be matched, as it is not a string", describe(re))
				}
				return flagsF(e, in, func(fl any) error {
					flags := ""
					if jv.KindOf(fl) == jv.KString {
						flags = jv.Str(fl)
					}
					if global {
						flags += "g"
					}
					r, err := compileCached(jv.Str(re), flags)
					if err != nil {
						return err
					}
					ms := r.find(s)
					ro := &runeOffsets{s: s}
					caps := make([]any, len(ms))
					for i, m := range ms {
						caps[i] = captureObject(r.matchObject(s, m, ro))
					}
					var build func(i, prev int, acc string) error
					build = func(i, prev int, acc string) error {
						if i == len(ms) {
							return out(acc + s[prev:])
						}
						m := ms[i]
						return args[1](e, caps[i], func(rep any) error {
							if jv.KindOf(rep) != jv.KString {
								return errorf("%s cannot be added to a string", describe(rep))
							}
							return build(i+1, m[1], acc+s[prev:m[0]]+jv.Str(rep))
						})
					}
					return build(0, 0, "")
				})
			})
		}
	})
}

// reCache holds compiled patterns: most filters use one or two constant
// patterns, and regexp.Regexp is safe for concurrent use.
var reCache sync.Map

func compileCached(pattern, flags string) (*jre, error) {
	key := flags + "\x00" + pattern
	if v, ok := reCache.Load(key); ok {
		return v.(*jre), nil
	}
	r, err := compileRegex(pattern, flags)
	if err != nil {
		return nil, err
	}
	reCache.Store(key, r)
	return r, nil
}
