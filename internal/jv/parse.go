// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package jv

import (
	"bytes"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// MaxDepth is the deepest nesting accepted, as in jq.
const MaxDepth = 10000

// SyntaxError is invalid JSON at byte offset Off. EOF is set when the text
// ended inside a value, so more input could complete it.
type SyntaxError struct {
	Off int
	Msg string
	EOF bool
}

func (e *SyntaxError) Error() string { return e.Msg }

func synErr(off int, msg string) error { return &SyntaxError{Off: off, Msg: msg} }
func eofErr(off int, msg string) error { return &SyntaxError{Off: off, Msg: msg, EOF: true} }

// IsSpace reports JSON whitespace.
func IsSpace(c byte) bool { return c == ' ' || c == '\n' || c == '\r' || c == '\t' }

// SkipSpace returns the first index at or after i that isn't whitespace.
func SkipSpace(b []byte, i int) int {
	for i < len(b) && IsSpace(b[i]) {
		i++
	}
	return i
}

// strSpecial marks bytes that end a run of plain string bytes.
var strSpecial = func() (t [256]bool) {
	for c := 0; c < 0x20; c++ {
		t[c] = true
	}
	t['"'] = true
	t['\\'] = true
	return
}()

// Valid checks that b[i:] starts with one valid JSON value and returns the
// index just after it.
func Valid(b []byte, i int) (int, error) {
	return valid(b, i, 0)
}

func isWS(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// valid checks one value starting at b[i] (whitespace first allowed), at
// nesting depth depth. It is one loop with an explicit stack: faster than
// recursion on the short, flat objects of log lines.
func valid(b []byte, i int, depth int) (int, error) {
	var stackBuf [64]byte
	stack := stackBuf[:0]
	n := len(b)
value:
	for i < n && isWS(b[i]) {
		i++
	}
	if i >= n {
		return i, eofErr(i, "Unfinished JSON term")
	}
	switch c := b[i]; c {
	case '{', '[':
		if depth+len(stack) >= MaxDepth {
			return i, synErr(i, "Exceeds depth limit for parsing")
		}
		i++
		for i < n && isWS(b[i]) {
			i++
		}
		if c == '{' {
			if i < n && b[i] == '}' {
				i++
				goto after
			}
			stack = append(stack, '{')
			goto key
		}
		if i < n && b[i] == ']' {
			i++
			goto after
		}
		stack = append(stack, '[')
		goto value
	case '"':
		i++
		for i < n && !strSpecial[b[i]] {
			i++
		}
		if i < n && b[i] == '"' {
			i++
			goto after
		}
		{
			e, err := validStringFrom(b, i)
			if err != nil {
				return e, err
			}
			i = e
		}
		goto after
	case 't', 'f', 'n':
		lit := "null"
		if c == 't' {
			lit = "true"
		} else if c == 'f' {
			lit = "false"
		}
		e, err := validLit(b, i, lit)
		if err != nil {
			return e, err
		}
		i = e
		goto after
	default:
		// Plain integers inline; anything else through validNumber.
		j := i
		if b[j] == '-' {
			j++
		}
		if j < n && b[j] >= '1' && b[j] <= '9' {
			j++
			for j < n && b[j] >= '0' && b[j] <= '9' {
				j++
			}
			if j < n && (b[j] == '.' || b[j] == 'e' || b[j] == 'E' || isLitByte(b[j])) || j == n {
				e, err := validNumber(b, i)
				if err != nil {
					return e, err
				}
				j = e
			}
			i = j
			goto after
		}
		e, err := validNumber(b, i)
		if err != nil {
			if c != '-' && (c < '0' || c > '9') {
				return i, synErr(i, "Invalid literal")
			}
			return e, err
		}
		i = e
		goto after
	}
key:
	if i >= n {
		return i, eofErr(i, "Unfinished JSON term")
	}
	if b[i] != '"' {
		return i, synErr(i, "Object keys must be strings")
	}
	{
		e, err := validString(b, i)
		if err != nil {
			return e, err
		}
		i = e
	}
	for i < n && isWS(b[i]) {
		i++
	}
	if i >= n {
		return i, eofErr(i, "Unfinished JSON term")
	}
	if b[i] != ':' {
		return i, synErr(i, "Objects must consist of key:value pairs")
	}
	i++
	goto value
after:
	if len(stack) == 0 {
		return i, nil
	}
	for i < n && isWS(b[i]) {
		i++
	}
	if i >= n {
		return i, eofErr(i, "Unfinished JSON term")
	}
	if b[i] == ',' {
		i++
		for i < n && isWS(b[i]) {
			i++
		}
		if stack[len(stack)-1] == '{' {
			goto key
		}
		goto value
	}
	if top := stack[len(stack)-1]; top == '{' && b[i] == '}' || top == '[' && b[i] == ']' {
		i++
		stack = stack[:len(stack)-1]
		goto after
	}
	return i, synErr(i, "Expected separator between values")
}

func validLit(b []byte, i int, lit string) (int, error) {
	n := len(lit)
	if len(b)-i < n {
		if string(b[i:]) == lit[:len(b)-i] {
			return len(b), eofErr(len(b), "Unfinished JSON term")
		}
		return i, synErr(i, "Invalid literal")
	}
	if string(b[i:i+n]) != lit {
		return i, synErr(i, "Invalid literal")
	}
	i += n
	if i < len(b) && isLitByte(b[i]) {
		return i, synErr(i, "Invalid literal")
	}
	return i, nil
}

func isLitByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '+' || c == '-'
}

func validNumber(b []byte, i int) (int, error) {
	start := i
	if b[i] == '-' {
		i++
	}
	if i >= len(b) {
		return i, eofErr(i, "Unfinished JSON term")
	}
	switch {
	case b[i] == '0':
		i++
	case b[i] >= '1' && b[i] <= '9':
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			i++
		}
	default:
		return start, synErr(start, "Invalid numeric literal")
	}
	if i < len(b) && b[i] == '.' {
		i++
		d := i
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			i++
		}
		if i == d {
			if i == len(b) {
				return i, eofErr(i, "Unfinished JSON term")
			}
			return start, synErr(start, "Invalid numeric literal")
		}
	}
	if i < len(b) && (b[i] == 'e' || b[i] == 'E') {
		i++
		if i < len(b) && (b[i] == '+' || b[i] == '-') {
			i++
		}
		d := i
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			i++
		}
		if i == d {
			if i == len(b) {
				return i, eofErr(i, "Unfinished JSON term")
			}
			return start, synErr(start, "Invalid numeric literal")
		}
	}
	if i < len(b) && isLitByte(b[i]) {
		return start, synErr(start, "Invalid numeric literal")
	}
	return i, nil
}

func validString(b []byte, i int) (int, error) {
	return validStringFrom(b, i+1)
}

// validStringFrom checks a string from b[i], just after its opening quote.
func validStringFrom(b []byte, i int) (int, error) {
	for {
		for i < len(b) && !strSpecial[b[i]] {
			i++
		}
		if i >= len(b) {
			return i, eofErr(i, "Unfinished string")
		}
		switch c := b[i]; {
		case c == '"':
			return i + 1, nil
		case c == '\\':
			if i+1 >= len(b) {
				return i + 1, eofErr(i+1, "Unfinished string")
			}
			switch b[i+1] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				i += 2
			case 'u':
				r, ok := hex4(b, i+2)
				if !ok {
					if len(b)-(i+2) < 4 {
						return len(b), eofErr(len(b), "Unfinished string")
					}
					return i, synErr(i, "Invalid escape")
				}
				i += 6
				if utf16.IsSurrogate(r) {
					if r >= 0xdc00 {
						return i, synErr(i, "Invalid \\uXXXX\\uXXXX surrogate pair escape")
					}
					if len(b)-i < 6 && couldBeEscape(b[i:]) {
						return len(b), eofErr(len(b), "Unfinished string")
					}
					if i+1 >= len(b) || b[i] != '\\' || b[i+1] != 'u' {
						return i, synErr(i, "Invalid \\uXXXX\\uXXXX surrogate pair escape")
					}
					r2, ok := hex4(b, i+2)
					if !ok || r2 < 0xdc00 || r2 > 0xdfff {
						return i, synErr(i, "Invalid \\uXXXX\\uXXXX surrogate pair escape")
					}
					i += 6
				}
			default:
				return i, synErr(i, "Invalid escape")
			}
		default:
			return i, synErr(i, "Invalid string: control characters from U+0000 through U+001F must be escaped")
		}
	}
}

// couldBeEscape reports whether the short tail t could be the start of a
// \uXXXX escape.
func couldBeEscape(t []byte) bool {
	for k, c := range t {
		switch k {
		case 0:
			if c != '\\' {
				return false
			}
		case 1:
			if c != 'u' {
				return false
			}
		default:
			if _, ok := hex4([]byte{c, '0', '0', '0'}, 0); !ok {
				return false
			}
		}
	}
	return true
}

func hex4(b []byte, i int) (rune, bool) {
	if len(b)-i < 4 {
		return 0, false
	}
	var r rune
	for _, c := range b[i : i+4] {
		var d byte
		switch {
		case c >= '0' && c <= '9':
			d = c - '0'
		case c >= 'a' && c <= 'f':
			d = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			d = c - 'A' + 10
		default:
			return 0, false
		}
		r = r<<4 | rune(d)
	}
	return r, true
}

// Decode parses the single JSON value in b (surrounding whitespace allowed).
func Decode(b []byte) (any, error) {
	i := SkipSpace(b, 0)
	end, err := Valid(b, i)
	if err != nil {
		return nil, err
	}
	if j := SkipSpace(b, end); j != len(b) {
		return nil, synErr(j, "Unexpected extra JSON values")
	}
	v, _ := decode(b, i)
	return v, nil
}

// DecodeValid decodes the value starting at b[i], which must be valid
// (checked by Valid), and returns it with the index after it.
func DecodeValid(b []byte, i int) (any, int) { return decode(b, i) }

func decode(b []byte, i int) (any, int) {
	switch b[i] {
	case '{':
		o := NewObject(8)
		i = SkipSpace(b, i+1)
		if b[i] == '}' {
			return o, i + 1
		}
		for {
			var k string
			k, i = decodeString(b, i)
			i = SkipSpace(b, i)
			i = SkipSpace(b, i+1) // ':'
			var v any
			v, i = decode(b, i)
			o.Set(k, v)
			i = SkipSpace(b, i)
			if b[i] == '}' {
				return o, i + 1
			}
			i = SkipSpace(b, i+1)
		}
	case '[':
		a := make([]any, 0, 4)
		i = SkipSpace(b, i+1)
		if b[i] == ']' {
			return a, i + 1
		}
		for {
			var v any
			v, i = decode(b, i)
			a = append(a, v)
			i = SkipSpace(b, i)
			if b[i] == ']' {
				return a, i + 1
			}
			i = SkipSpace(b, i+1)
		}
	case '"':
		return decodeString(b, i)
	case 't':
		return true, i + 4
	case 'f':
		return false, i + 5
	case 'n':
		return nil, i + 4
	}
	end := numberEnd(b, i)
	return NumberFromLit(string(b[i:end])), end
}

func numberEnd(b []byte, i int) int {
	for i < len(b) {
		c := b[i]
		if c >= '0' && c <= '9' || c == '-' || c == '+' || c == '.' || c == 'e' || c == 'E' {
			i++
			continue
		}
		break
	}
	return i
}

// NumberFromLit makes a number from a valid JSON number literal.
func NumberFromLit(lit string) Number {
	f, _ := strconv.ParseFloat(lit, 64) // a range error gives ±Inf or 0, as jq
	return Number{F: f, Lit: lit}
}

// decodeString decodes the valid string starting at b[i] (the quote).
func decodeString(b []byte, i int) (string, int) {
	j := i + 1
	plain := true
	for {
		c := b[j]
		if c == '"' {
			break
		}
		if c == '\\' {
			plain = false
			break
		}
		if c >= 0x80 {
			plain = false
		}
		j++
	}
	if plain {
		return string(b[i+1 : j]), j + 1
	}
	return unescape(b, i)
}

func unescape(b []byte, i int) (string, int) {
	out := make([]byte, 0, 32)
	i++
	for {
		c := b[i]
		switch {
		case c == '"':
			return string(out), i + 1
		case c == '\\':
			switch b[i+1] {
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'u':
				r, _ := hex4(b, i+2)
				i += 6
				if utf16.IsSurrogate(r) {
					r2, _ := hex4(b, i+2)
					r = utf16.DecodeRune(r, r2)
					i += 6
				}
				out = utf8.AppendRune(out, r)
				continue
			default: // " \ /
				out = append(out, b[i+1])
			}
			i += 2
		case c < 0x80:
			out = append(out, c)
			i++
		default:
			r, n := utf8.DecodeRune(b[i:])
			if r == utf8.RuneError && n <= 1 {
				out = append(out, "�"...)
				i++
			} else {
				out = append(out, b[i:i+n]...)
				i += n
			}
		}
	}
}

// Skip returns the index after the valid value starting at b[i]. It trusts
// the text to be valid.
func Skip(b []byte, i int) int {
	switch b[i] {
	case '"':
		return skipString(b, i)
	case '{', '[':
		depth := 0
		for {
			c := b[i]
			switch c {
			case '"':
				i = skipString(b, i)
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1
				}
			}
			i++
		}
	case 't', 'n':
		return i + 4
	case 'f':
		return i + 5
	}
	return numberEnd(b, i)
}

func skipString(b []byte, i int) int {
	i++
	for {
		j := bytes.IndexByte(b[i:], '"')
		i += j
		// count the backslashes before the quote
		k := i - 1
		for b[k] == '\\' {
			k--
		}
		if (i-1-k)%2 == 0 {
			return i + 1
		}
		i++
	}
}

// Field looks key up in the raw object r. With duplicate keys the last one
// wins, as in jq.
func Field(r Raw, key string) (Raw, bool) {
	var found Raw
	ok := false
	i := SkipSpace(r, 1)
	if r[i] == '}' {
		return nil, false
	}
	for {
		ks := i
		ke := skipString(r, i)
		match := keyEquals(r[ks:ke], key)
		i = SkipSpace(r, ke)
		i = SkipSpace(r, i+1)
		ve := Skip(r, i)
		if match {
			found, ok = r[i:ve], true
		}
		i = SkipSpace(r, ve)
		if r[i] == '}' {
			return found, ok
		}
		i = SkipSpace(r, i+1)
	}
}

// keyEquals compares a raw string (quotes included) with key.
func keyEquals(q []byte, key string) bool {
	body := q[1 : len(q)-1]
	if bytes.IndexByte(body, '\\') < 0 && !hasHigh(body) {
		return string(body) == key
	}
	s, _ := decodeString(q, 0)
	return s == key
}

func hasHigh(b []byte) bool {
	for _, c := range b {
		if c >= 0x80 {
			return true
		}
	}
	return false
}

// RawString decodes the raw string r.
func RawString(r Raw) string {
	s, _ := decodeString(r, 0)
	return s
}

// RawPlainString returns the bytes of the raw string r between its quotes
// when they need no decoding (no escapes, ASCII only).
func RawPlainString(r Raw) ([]byte, bool) {
	body := r[1 : len(r)-1]
	for _, c := range body {
		if c == '\\' || c >= 0x80 {
			return nil, false
		}
	}
	return body, true
}

// Each calls fn for each member of the raw array or object r, with the
// raw key (quoted) for objects. With duplicate object keys, only the last
// one is seen, at the place of the first, as jq would.
func Each(r Raw, fn func(key Raw, v Raw) error) error {
	if r[0] == '{' && hasDuplicateKeys(r) {
		o := Norm(r).(*Object)
		for i, k := range o.Keys {
			if err := fn(Raw(AppendString(nil, k)), toRaw(o.Vals[i])); err != nil {
				return err
			}
		}
		return nil
	}
	i := SkipSpace(r, 1)
	if r[i] == ']' || r[i] == '}' {
		return nil
	}
	obj := r[0] == '{'
	for {
		var key Raw
		if obj {
			ke := skipString(r, i)
			key = r[i:ke]
			i = SkipSpace(r, ke)
			i = SkipSpace(r, i+1)
		}
		ve := Skip(r, i)
		if err := fn(key, r[i:ve]); err != nil {
			return err
		}
		i = SkipSpace(r, ve)
		if r[i] == ']' || r[i] == '}' {
			return nil
		}
		i = SkipSpace(r, i+1)
	}
}

// toRaw re-encodes a decoded value as Raw (used only on the rare
// duplicate-key path).
func toRaw(v any) Raw {
	return Raw(Encode(nil, v, Format{Compact: true}))
}

// hasDuplicateKeys reports whether the raw object r has a key twice at its
// top level.
func hasDuplicateKeys(r Raw) bool {
	var small [16][]byte
	seen := small[:0]
	var m map[string]struct{}
	i := SkipSpace(r, 1)
	if r[i] == '}' {
		return false
	}
	for {
		ke := skipString(r, i)
		k := r[i:ke]
		if bytes.IndexByte(k, '\\') >= 0 {
			// escapes can spell the same key two ways; decode to compare
			return hasDuplicateKeysSlow(r)
		}
		if m != nil {
			if _, dup := m[string(k)]; dup {
				return true
			}
			m[string(k)] = struct{}{}
		} else {
			for _, s := range seen {
				if bytes.Equal(s, k) {
					return true
				}
			}
			seen = append(seen, k)
			if len(seen) == cap(small) {
				m = make(map[string]struct{}, 64)
				for _, s := range seen {
					m[string(s)] = struct{}{}
				}
			}
		}
		i = SkipSpace(r, ke)
		i = SkipSpace(r, i+1)
		i = SkipSpace(r, Skip(r, i))
		if r[i] == '}' {
			return false
		}
		i = SkipSpace(r, i+1)
	}
}

func hasDuplicateKeysSlow(r Raw) bool {
	seen := map[string]struct{}{}
	dup := false
	i := SkipSpace(r, 1)
	for r[i] != '}' {
		k, ke := decodeString(r, i)
		if _, ok := seen[k]; ok {
			dup = true
		}
		seen[k] = struct{}{}
		i = SkipSpace(r, ke)
		i = SkipSpace(r, i+1)
		i = SkipSpace(r, Skip(r, i))
		if r[i] == ',' {
			i = SkipSpace(r, i+1)
		}
	}
	return dup
}

// Len counts the members of the raw array or object r.
func Len(r Raw) int {
	if r[0] == '{' && hasDuplicateKeys(r) {
		return Norm(r).(*Object).Len()
	}
	n := 0
	Each(r, func(Raw, Raw) error { n++; return nil })
	return n
}

// Index returns element n of the raw array r (n counted from 0).
func Index(r Raw, n int) (Raw, bool) {
	var out Raw
	k := 0
	errStop := &SyntaxError{}
	Each(r, func(_ Raw, v Raw) error {
		if k == n {
			out = v
			return errStop
		}
		k++
		return nil
	})
	return out, out != nil
}
