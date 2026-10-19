// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package jv

import (
	"bytes"
	"strconv"
	"strings"
)

// Compare orders a and b the way jq does: null < false < true < numbers <
// strings < arrays < objects; numbers by value, strings by codepoint,
// arrays element by element, objects by their sorted keys and then their
// values. It returns -1, 0 or 1.
func Compare(a, b any) int {
	a, b = unwrap(a), unwrap(b)
	ka, kb := KindOf(a), KindOf(b)
	if ka != kb {
		if ka < kb {
			return -1
		}
		return 1
	}
	switch ka {
	case KNull, KFalse, KTrue:
		return 0
	case KNumber:
		return compareNumbers(number(a), number(b))
	case KString:
		// Two plain raw strings compare without decoding.
		if ra, ok := a.(Raw); ok {
			if rb, ok := b.(Raw); ok {
				pa, okA := RawPlainString(ra)
				pb, okB := RawPlainString(rb)
				if okA && okB {
					return bytes.Compare(pa, pb)
				}
			}
		}
		return strings.Compare(Str(a), Str(b))
	case KArray:
		x, y := Norm(a).([]any), Norm(b).([]any)
		for i := 0; i < len(x) && i < len(y); i++ {
			if c := Compare(x[i], y[i]); c != 0 {
				return c
			}
		}
		switch {
		case len(x) < len(y):
			return -1
		case len(x) > len(y):
			return 1
		}
		return 0
	}
	x, y := Norm(a).(*Object), Norm(b).(*Object)
	kx, ky := x.SortedKeys(), y.SortedKeys()
	for i := 0; i < len(kx) && i < len(ky); i++ {
		if c := strings.Compare(kx[i], ky[i]); c != 0 {
			return c
		}
	}
	if len(kx) != len(ky) {
		if len(kx) < len(ky) {
			return -1
		}
		return 1
	}
	for _, k := range kx {
		va, _ := x.Get(k)
		vb, _ := y.Get(k)
		if c := Compare(va, vb); c != 0 {
			return c
		}
	}
	return 0
}

// Equal reports whether jq considers a and b equal.
func Equal(a, b any) bool {
	a, b = unwrap(a), unwrap(b)
	// Fast path: a raw string against a string.
	if ra, ok := a.(Raw); ok {
		if s, ok := b.(string); ok {
			return rawStringEquals(ra, s)
		}
	}
	if rb, ok := b.(Raw); ok {
		if s, ok := a.(string); ok {
			return rawStringEquals(rb, s)
		}
	}
	return Compare(a, b) == 0
}

func rawStringEquals(r Raw, s string) bool {
	if r[0] != '"' {
		return false
	}
	if body, ok := RawPlainString(r); ok {
		return string(body) == s
	}
	return RawString(r) == s
}

// Str returns the string value of a string or raw string.
func Str(v any) string {
	if r, ok := RawOf(v); ok {
		return RawString(r)
	}
	return v.(string)
}

// number returns the number value of a number or raw number.
func number(v any) Number {
	if r, ok := RawOf(v); ok {
		return NumberFromLit(string(r))
	}
	return v.(Number)
}

// AsNumber returns v as a Number, decoding a raw number.
func AsNumber(v any) (Number, bool) {
	switch x := v.(type) {
	case Number:
		return x, true
	case Raw:
		if rawKind(x) == KNumber {
			return NumberFromLit(string(x)), true
		}
	case *Doc:
		return AsNumber(x.Raw)
	}
	return Number{}, false
}

func compareNumbers(a, b Number) int {
	if a.Lit != "" && b.Lit != "" {
		if c, ok := compareLiterals(a.Lit, b.Lit); ok {
			return c
		}
	}
	switch {
	case a.F < b.F:
		return -1
	case a.F > b.F:
		return 1
	case a.F == b.F:
		return 0
	}
	// NaN: jq sorts it below every number
	switch {
	case a.F != a.F && b.F != b.F:
		return 0
	case a.F != a.F:
		return -1
	}
	return 1
}

type decimal struct {
	neg    bool
	digits string // no leading or trailing zeros; "" for zero
	adj    int64  // exponent of the first digit
}

func parseDecimal(lit string) (decimal, bool) {
	var d decimal
	s := lit
	if s[0] == '-' {
		d.neg = true
		s = s[1:]
	}
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
	var exp int64
	if i < len(s) {
		x, err := strconv.ParseInt(s[i+1:], 10, 64)
		if err != nil || x > 1<<40 || x < -(1<<40) {
			return d, false
		}
		exp = x
	}
	coef := intPart + frac
	lead := 0
	for lead < len(coef) && coef[lead] == '0' {
		lead++
	}
	trail := len(coef)
	for trail > lead && coef[trail-1] == '0' {
		trail--
	}
	if lead == trail {
		return decimal{}, true // zero, either sign
	}
	d.digits = coef[lead:trail]
	// the first significant digit sits at position lead of coef; coef's last
	// digit has exponent exp-len(frac)
	d.adj = exp - int64(len(frac)) + int64(len(coef)-1-lead)
	return d, true
}

// compareLiterals compares two number literals exactly.
func compareLiterals(a, b string) (int, bool) {
	if a == b {
		return 0, true
	}
	x, ok := parseDecimal(a)
	if !ok {
		return 0, false
	}
	y, ok := parseDecimal(b)
	if !ok {
		return 0, false
	}
	sign := func(d decimal) int {
		switch {
		case d.digits == "":
			return 0
		case d.neg:
			return -1
		}
		return 1
	}
	sx, sy := sign(x), sign(y)
	if sx != sy {
		if sx < sy {
			return -1, true
		}
		return 1, true
	}
	if sx == 0 {
		return 0, true
	}
	c := 0
	switch {
	case x.adj < y.adj:
		c = -1
	case x.adj > y.adj:
		c = 1
	default:
		c = strings.Compare(x.digits, y.digits) // equal length prefix compares as numbers
	}
	if sx < 0 {
		c = -c
	}
	return c, true
}
