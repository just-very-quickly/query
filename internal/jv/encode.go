// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package jv

import (
	"math"
	"sort"
	"strconv"
)

// Format says how values are written: indented by Indent spaces (2 by
// default, as jq) or a tab, or compact, optionally with object keys sorted.
type Format struct {
	Compact  bool
	Indent   int // spaces per level when not compact; 0 means 2
	Tab      bool
	SortKeys bool
}

// Encode appends v to buf as JSON text, exactly as jq 1.7 writes it.
func Encode(buf []byte, v any, f Format) []byte {
	if !f.Compact && f.Indent == 0 && !f.Tab {
		f.Indent = 2
	}
	return encode(buf, v, f, 0)
}

func newline(buf []byte, f Format, level int) []byte {
	buf = append(buf, '\n')
	if f.Tab {
		for i := 0; i < level; i++ {
			buf = append(buf, '\t')
		}
		return buf
	}
	for i := 0; i < level*f.Indent; i++ {
		buf = append(buf, ' ')
	}
	return buf
}

// maxPrintDepth: as jq, values nested deeper are written as
// <skipped: too deep>.
const maxPrintDepth = 256

func encode(buf []byte, v any, f Format, level int) []byte {
	if level > maxPrintDepth {
		return append(buf, "<skipped: too deep>"...)
	}
	switch x := v.(type) {
	case nil:
		return append(buf, "null"...)
	case bool:
		if x {
			return append(buf, "true"...)
		}
		return append(buf, "false"...)
	case Number:
		return AppendNumber(buf, x)
	case string:
		return AppendString(buf, x)
	case []any:
		if len(x) == 0 {
			return append(buf, "[]"...)
		}
		buf = append(buf, '[')
		for i, e := range x {
			if i > 0 {
				buf = append(buf, ',')
			}
			if !f.Compact {
				buf = newline(buf, f, level+1)
			}
			buf = encode(buf, e, f, level+1)
		}
		if !f.Compact {
			buf = newline(buf, f, level)
		}
		return append(buf, ']')
	case *Object:
		if x.Len() == 0 {
			return append(buf, "{}"...)
		}
		keys := x.Keys
		if f.SortKeys {
			keys = x.SortedKeys()
		}
		buf = append(buf, '{')
		for i, k := range keys {
			if i > 0 {
				buf = append(buf, ',')
			}
			if !f.Compact {
				buf = newline(buf, f, level+1)
			}
			buf = AppendString(buf, k)
			buf = append(buf, ':')
			if !f.Compact {
				buf = append(buf, ' ')
			}
			val, _ := x.Get(k)
			buf = encode(buf, val, f, level+1)
		}
		if !f.Compact {
			buf = newline(buf, f, level)
		}
		return append(buf, '}')
	case Raw:
		return encodeRaw(buf, x, f, level)
	case *Doc:
		return encodeRaw(buf, x.Raw, f, level)
	}
	panic("jv: not a value")
}

// encodeRaw writes raw JSON text in jq's format without decoding it, except
// for objects with duplicate keys or when keys are sorted.
func encodeRaw(buf []byte, r Raw, f Format, level int) []byte {
	if level > maxPrintDepth {
		return append(buf, "<skipped: too deep>"...)
	}
	// A raw value can't hold more than len/2 levels; only a deep one placed
	// deep in a built value can pass the limit, and is written the long way.
	if level+len(r)/2 > maxPrintDepth && level+rawDepth(r) > maxPrintDepth {
		v, _ := DecodeValid(r, 0)
		return encode(buf, v, f, level)
	}
	switch r[0] {
	case '"':
		if body, ok := plainASCII(r); ok {
			buf = append(buf, '"')
			buf = append(buf, body...)
			return append(buf, '"')
		}
		return AppendString(buf, RawString(r))
	case 't', 'f', 'n':
		return append(buf, r...)
	case '{', '[':
		if f.Compact && !f.SortKeys && canonical(r) {
			return append(buf, r...)
		}
		if r[0] == '{' && (f.SortKeys || hasDuplicateKeys(r)) {
			return encode(buf, Norm(r), f, level)
		}
		open, close := r[0], r[len(r)-1]
		i := SkipSpace(r, 1)
		if r[i] == close {
			return append(buf, open, close)
		}
		buf = append(buf, open)
		obj := open == '{'
		first := true
		for {
			if !first {
				buf = append(buf, ',')
			}
			first = false
			if !f.Compact {
				buf = newline(buf, f, level+1)
			}
			if obj {
				ke := skipString(r, i)
				buf = encodeRaw(buf, r[i:ke], f, level+1)
				buf = append(buf, ':')
				if !f.Compact {
					buf = append(buf, ' ')
				}
				i = SkipSpace(r, ke)
				i = SkipSpace(r, i+1)
			}
			ve := Skip(r, i)
			buf = encodeRaw(buf, r[i:ve], f, level+1)
			i = SkipSpace(r, ve)
			if r[i] == close {
				break
			}
			i = SkipSpace(r, i+1)
		}
		if !f.Compact {
			buf = newline(buf, f, level)
		}
		return append(buf, close)
	}
	return AppendLiteral(buf, string(r))
}

// plainASCII returns the body of a raw string that jq would write byte for
// byte: printable ASCII with no escapes.
// rawDepth is how deeply the valid JSON text r nests.
func rawDepth(r Raw) int {
	d, best := 0, 0
	for i := 0; i < len(r); i++ {
		switch r[i] {
		case '"':
			for i++; r[i] != '"'; i++ {
				if r[i] == '\\' {
					i++
				}
			}
		case '[', '{':
			d++
			best = max(best, d)
		case ']', '}':
			d--
		}
	}
	return best
}

func plainASCII(r Raw) ([]byte, bool) {
	body := r[1 : len(r)-1]
	for _, c := range body {
		if c < 0x20 || c >= 0x7f || c == '\\' {
			return nil, false
		}
	}
	return body, true
}

const hexDigits = "0123456789abcdef"

// AppendString appends s as a JSON string the way jq writes it: ", \ and
// control characters escaped (with \b \f \n \r \t where they exist, and
// lowercase \u00xx otherwise, DEL included), everything else as UTF-8.
func AppendString(buf []byte, s string) []byte {
	buf = append(buf, '"')
	start := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c >= 0x20 && c != '"' && c != '\\' && c != 0x7f {
			if c < 0x80 {
				i++
				continue
			}
			cp, n := utf8Next(s, i)
			if cp < 0 {
				buf = append(buf, s[start:i]...)
				buf = append(buf, replacement...)
				i += n
				start = i
				continue
			}
			i += n
			continue
		}
		buf = append(buf, s[start:i]...)
		switch c {
		case '"':
			buf = append(buf, '\\', '"')
		case '\\':
			buf = append(buf, '\\', '\\')
		case '\b':
			buf = append(buf, '\\', 'b')
		case '\f':
			buf = append(buf, '\\', 'f')
		case '\n':
			buf = append(buf, '\\', 'n')
		case '\r':
			buf = append(buf, '\\', 'r')
		case '\t':
			buf = append(buf, '\\', 't')
		default:
			buf = append(buf, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xf])
		}
		i++
		start = i
	}
	buf = append(buf, s[start:]...)
	return append(buf, '"')
}

// AppendNumber writes n as jq does: an unchanged literal in canonical
// decimal form, a computed number in shortest round-trip form.
func AppendNumber(buf []byte, n Number) []byte {
	if n.Lit != "" {
		return AppendLiteral(buf, n.Lit)
	}
	return AppendFloat(buf, n.F)
}

// AppendFloat writes a computed number the way jq 1.7 prints doubles.
func AppendFloat(buf []byte, f float64) []byte {
	switch {
	case math.IsNaN(f):
		return append(buf, "null"...)
	case math.IsInf(f, 1):
		f = math.MaxFloat64
	case math.IsInf(f, -1):
		f = -math.MaxFloat64
	}
	var tmp [32]byte
	e := strconv.AppendFloat(tmp[:0], f, 'e', -1, 64)
	neg := e[0] == '-'
	if neg {
		e = e[1:]
		buf = append(buf, '-')
	}
	// e is d[.ddd]e±XX
	var digits [24]byte
	nd := 0
	k := 0
	for ; e[k] != 'e'; k++ {
		if e[k] != '.' {
			digits[nd] = e[k]
			nd++
		}
	}
	exp, _ := strconv.Atoi(string(e[k+1:]))
	decpt := exp + 1
	d := digits[:nd]
	if decpt <= -4 || decpt > nd+15 {
		buf = append(buf, d[0])
		if nd > 1 {
			buf = append(buf, '.')
			buf = append(buf, d[1:]...)
		}
		buf = append(buf, 'e')
		x := decpt - 1
		if x < 0 {
			buf = append(buf, '-')
			x = -x
		} else {
			buf = append(buf, '+')
		}
		if x < 10 {
			buf = append(buf, '0')
		}
		return strconv.AppendInt(buf, int64(x), 10)
	}
	switch {
	case decpt <= 0:
		buf = append(buf, '0', '.')
		for i := 0; i < -decpt; i++ {
			buf = append(buf, '0')
		}
		buf = append(buf, d...)
	case decpt >= nd:
		buf = append(buf, d...)
		for i := 0; i < decpt-nd; i++ {
			buf = append(buf, '0')
		}
	default:
		buf = append(buf, d[:decpt]...)
		buf = append(buf, '.')
		buf = append(buf, d[decpt:]...)
	}
	return buf
}

// AppendLiteral writes a JSON number literal in the canonical decimal form
// jq 1.7 prints it in (the "to-scientific-string" of the General Decimal
// Arithmetic spec): 1e2 is 1E+2, 100e-2 is 1.00, 0.0000001 is 1E-7.
func AppendLiteral(buf []byte, lit string) []byte {
	s := lit
	neg := false
	if s[0] == '-' {
		neg = true
		s = s[1:]
	}
	// split into integer digits, fraction digits, exponent
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	intPart := s[:i]
	frac := ""
	if i < len(s) && s[i] == '.' {
		j := i + 1
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		frac = s[i+1 : j]
		i = j
	}
	exp := 0
	hasExp := i < len(s)
	if hasExp {
		x, err := strconv.Atoi(s[i+1:])
		if err != nil || x > 999999999 || x < -999999999 {
			// beyond any decimal format; print as a double, as jq does
			return AppendFloat(buf, NumberFromLit(lit).F)
		}
		exp = x
	}
	// Fast path: a plain integer or decimal that is already canonical.
	if !hasExp && (intPart != "0" || leadingZeros(frac) < 6) {
		return append(buf, lit...)
	}
	coef := intPart + frac
	k := 0
	for k < len(coef)-1 && coef[k] == '0' {
		k++
	}
	coef = coef[k:]
	e := exp - len(frac)
	if neg {
		buf = append(buf, '-')
	}
	n := len(coef)
	adj := e + n - 1
	if e <= 0 && adj >= -6 {
		if e == 0 {
			return append(buf, coef...)
		}
		pt := n + e // digits before the point
		if pt > 0 {
			buf = append(buf, coef[:pt]...)
			buf = append(buf, '.')
			return append(buf, coef[pt:]...)
		}
		buf = append(buf, '0', '.')
		for ; pt < 0; pt++ {
			buf = append(buf, '0')
		}
		return append(buf, coef...)
	}
	buf = append(buf, coef[0])
	if n > 1 {
		buf = append(buf, '.')
		buf = append(buf, coef[1:]...)
	}
	buf = append(buf, 'E')
	if adj >= 0 {
		buf = append(buf, '+')
	}
	return strconv.AppendInt(buf, int64(adj), 10)
}

func leadingZeros(s string) int {
	n := 0
	for n < len(s) && s[n] == '0' {
		n++
	}
	return n
}

// SortStrings sorts by codepoint, as jq does (byte order of UTF-8).
func SortStrings(s []string) { sort.Strings(s) }
