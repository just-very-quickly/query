// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package jv

import (
	"math"
	"strings"
)

// jq 1.7.1 reads more than JSON: number tokens like 01, .5, 1., +1 and
// 1.e3, and nan, NaN, inf and Infinity in any case, with either sign. Valid
// rejects them, which keeps the fast path strict; DecodeLenient is the slow
// path for the rare line that has them. It decodes every value in b and
// fails, like jq, on anything else that isn't JSON.

// DecodeLenient decodes all the values in b, reading numbers as jq does.
// ok is false if b holds something jq would reject too.
func DecodeLenient(b []byte) (vals []any, ok bool) {
	p := &lenient{b: b}
	for p.i = SkipSpace(b, 0); p.i < len(b); p.i = SkipSpace(b, p.i) {
		v, ok := p.value(0)
		if !ok {
			return nil, false
		}
		vals = append(vals, v)
	}
	return vals, true
}

type lenient struct {
	b []byte
	i int
}

// literalByte reports the bytes that make up a bare token: anything that
// isn't structure, a quote or whitespace, as in jq's scanner.
func literalByte(c byte) bool {
	switch c {
	case '[', ']', '{', '}', ',', ':', '"', ' ', '\t', '\r', '\n':
		return false
	}
	return true
}

func (p *lenient) value(depth int) (any, bool) {
	b := p.b
	p.i = SkipSpace(b, p.i)
	if p.i >= len(b) {
		return nil, false
	}
	switch c := b[p.i]; c {
	case '{':
		if depth+2 > MaxDepth {
			return nil, false
		}
		o := NewObject(4)
		p.i = SkipSpace(b, p.i+1)
		if p.i < len(b) && b[p.i] == '}' {
			p.i++
			return o, true
		}
		for {
			p.i = SkipSpace(b, p.i)
			if p.i >= len(b) || b[p.i] != '"' {
				return nil, false
			}
			end, err := validString(b, p.i)
			if err != nil {
				return nil, false
			}
			k, _ := decodeString(b, p.i)
			p.i = SkipSpace(b, end)
			if p.i >= len(b) || b[p.i] != ':' {
				return nil, false
			}
			p.i++
			v, ok := p.value(depth + 2)
			if !ok {
				return nil, false
			}
			o.Set(k, v)
			p.i = SkipSpace(b, p.i)
			if p.i >= len(b) {
				return nil, false
			}
			if b[p.i] == '}' {
				p.i++
				return o, true
			}
			if b[p.i] != ',' {
				return nil, false
			}
			p.i++
		}
	case '[':
		if depth+1 > MaxDepth {
			return nil, false
		}
		a := []any{}
		p.i = SkipSpace(b, p.i+1)
		if p.i < len(b) && b[p.i] == ']' {
			p.i++
			return a, true
		}
		for {
			v, ok := p.value(depth + 1)
			if !ok {
				return nil, false
			}
			a = append(a, v)
			p.i = SkipSpace(b, p.i)
			if p.i >= len(b) {
				return nil, false
			}
			if b[p.i] == ']' {
				p.i++
				return a, true
			}
			if b[p.i] != ',' {
				return nil, false
			}
			p.i++
		}
	case '"':
		end, err := validString(b, p.i)
		if err != nil {
			return nil, false
		}
		s, _ := decodeString(b, p.i)
		p.i = end
		return s, true
	}
	j := p.i
	for j < len(b) && literalByte(b[j]) {
		j++
	}
	if j == p.i {
		return nil, false
	}
	tok := string(b[p.i:j])
	p.i = j
	return lenientLiteral(tok)
}

// lenientLiteral reads one bare token the way jq 1.7.1 does.
func lenientLiteral(tok string) (any, bool) {
	switch tok {
	case "true":
		return true, true
	case "false":
		return false, true
	case "null":
		return nil, true
	case "nan":
		return Num(math.NaN()), true
	}
	if tok[0] == 't' || tok[0] == 'f' {
		return nil, false
	}
	t := tok
	neg := false
	if t[0] == '+' || t[0] == '-' {
		neg = t[0] == '-'
		t = t[1:]
	}
	switch strings.ToLower(t) {
	case "nan", "snan":
		return Num(math.NaN()), true
	case "inf", "infinity":
		if neg {
			return Num(math.Inf(-1)), true
		}
		return Num(math.Inf(1)), true
	}
	// digits [. digits] [e [sign] digits], with digits on at least one
	// side of the point
	k := 0
	for k < len(t) && t[k] >= '0' && t[k] <= '9' {
		k++
	}
	intPart := t[:k]
	frac := ""
	if k < len(t) && t[k] == '.' {
		k++
		s := k
		for k < len(t) && t[k] >= '0' && t[k] <= '9' {
			k++
		}
		frac = t[s:k]
	}
	if intPart == "" && frac == "" {
		return nil, false
	}
	exp := ""
	if k < len(t) && (t[k] == 'e' || t[k] == 'E') {
		k++
		s := k
		if k < len(t) && (t[k] == '+' || t[k] == '-') {
			k++
		}
		d := k
		for k < len(t) && t[k] >= '0' && t[k] <= '9' {
			k++
		}
		if k == d {
			return nil, false
		}
		exp = t[s:k]
	}
	if k != len(t) {
		return nil, false
	}
	// The same value and digits written as JSON: no leading zeros or plus
	// sign, a digit on both sides of the point.
	intPart = strings.TrimLeft(intPart, "0")
	if intPart == "" {
		intPart = "0"
	}
	lit := intPart
	if frac != "" {
		lit += "." + frac
	}
	if exp != "" {
		lit += "e" + exp
	}
	if neg {
		lit = "-" + lit
	}
	return NumberFromLit(lit), true
}
