// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package jv

import (
	"bytes"
	"unicode/utf8"
)

// Doc is one input value with the members of its top-level object found
// while it was checked, so looking up a field doesn't scan the line again.
// The engine reuses one Doc per worker: a Doc must not be kept after the
// filter has run on it (Detach copies it out).
type Doc struct {
	Raw     Raw
	members []member
	slow    bool // a key with escapes or non-ASCII bytes: use Field
}

type member struct{ ks, ke, vs, ve int32 } // key with quotes, value; offsets in Raw

// RawOf returns the raw text of a Raw or a Doc.
func RawOf(v any) (Raw, bool) {
	switch x := v.(type) {
	case Raw:
		return x, true
	case *Doc:
		return x.Raw, true
	}
	return nil, false
}

// unwrap turns a Doc into its Raw.
func unwrap(v any) any {
	if d, ok := v.(*Doc); ok {
		return d.Raw
	}
	return v
}

// ValidDoc checks the value at b[i] like Valid and fills d with it.
func ValidDoc(b []byte, i int, d *Doc) (int, error) {
	d.members = d.members[:0]
	d.slow = false
	d.Raw = nil
	if i >= len(b) || b[i] != '{' {
		end, err := Valid(b, i)
		if err == nil {
			d.Raw = b[i:end]
		}
		return end, err
	}
	start := i
	i = SkipSpace(b, i+1)
	if i < len(b) && b[i] == '}' {
		d.Raw = b[start : i+1]
		return i + 1, nil
	}
	for {
		if i >= len(b) {
			return i, eofErr(i, "Unfinished JSON term")
		}
		if b[i] != '"' {
			return i, synErr(i, "Object keys must be strings")
		}
		ks := i
		var err error
		if i, err = validString(b, i); err != nil {
			return i, err
		}
		ke := i
		if !d.slow {
			for _, c := range b[ks+1 : ke-1] {
				if c == '\\' || c >= 0x80 {
					d.slow = true
					break
				}
			}
		}
		i = SkipSpace(b, i)
		if i >= len(b) {
			return i, eofErr(i, "Unfinished JSON term")
		}
		if b[i] != ':' {
			return i, synErr(i, "Objects must consist of key:value pairs")
		}
		i = SkipSpace(b, i+1)
		vs := i
		if i, err = valid(b, i, 1); err != nil {
			return i, err
		}
		d.members = append(d.members, member{int32(ks - start), int32(ke - start), int32(vs - start), int32(i - start)})
		i = SkipSpace(b, i)
		if i >= len(b) {
			return i, eofErr(i, "Unfinished JSON term")
		}
		if b[i] == '}' {
			i++
			break
		}
		if b[i] != ',' {
			return i, synErr(i, "Expected separator between values")
		}
		i = SkipSpace(b, i+1)
	}
	d.Raw = b[start:i]
	return i, nil
}

// Field looks up name in the Doc's object. With a duplicate key the last
// one wins, as in jq, so the search runs from the end.
func (d *Doc) Field(name string) (Raw, bool) {
	if d.slow || d.Raw[0] != '{' {
		return Field(d.Raw, name)
	}
	for j := len(d.members) - 1; j >= 0; j-- {
		m := d.members[j]
		k := d.Raw[m.ks+1 : m.ke-1]
		if len(k) == len(name) && string(k) == name {
			return d.Raw[m.vs:m.ve], true
		}
	}
	return nil, false
}

// canonical reports whether the valid JSON text b is already exactly what
// jq prints in compact form, so it can be copied as it is: no space between
// tokens, strings with only the escapes jq writes, numbers in jq's form,
// and no duplicate keys.
func canonical(b []byte) bool {
	var keyBuf [16][]byte
	keys := keyBuf[:0]
	var startBuf [32]int
	starts := startBuf[:0] // per open object: where its keys begin
	var kindBuf [32]byte
	kinds := kindBuf[:0] // the open brackets
	expectKey := false
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c == '"':
			j := i + 1
			for {
				for canonStop[b[j]] == 0 {
					j++
				}
				c := b[j]
				if c == '"' {
					break
				}
				if c == '\\' {
					switch b[j+1] {
					case '"', '\\', 'n', 'r', 't', 'b', 'f':
						j += 2
						continue
					}
					return false
				}
				if c == 0x7f {
					return false
				}
				r, n := utf8.DecodeRune(b[j:])
				if r == utf8.RuneError && n <= 1 {
					return false
				}
				j += n
			}
			if expectKey {
				k := b[i+1 : j]
				for _, prev := range keys[starts[len(starts)-1]:] {
					if len(prev) == len(k) && (len(k) == 0 || prev[0] == k[0] && bytes.Equal(prev, k)) {
						return false
					}
				}
				keys = append(keys, k)
				expectKey = false
			}
			i = j + 1
		case c == '{':
			starts = append(starts, len(keys))
			kinds = append(kinds, '{')
			expectKey = true
			i++
		case c == '[':
			kinds = append(kinds, '[')
			i++
		case c == '}':
			keys = keys[:starts[len(starts)-1]]
			starts = starts[:len(starts)-1]
			kinds = kinds[:len(kinds)-1]
			i++
		case c == ']':
			kinds = kinds[:len(kinds)-1]
			i++
		case c == ',':
			expectKey = kinds[len(kinds)-1] == '{'
			i++
		case c == ':':
			i++
		case c == 't' || c == 'n':
			i += 4
		case c == 'f':
			i += 5
		case c == '-' || c >= '0' && c <= '9':
			j := numberEnd(b, i)
			if !canonicalNumber(b[i:j]) {
				return false
			}
			i = j
		default:
			return false // whitespace between tokens
		}
	}
	return true
}

// canonStop marks the bytes that end a run of plain string bytes in
// canonical: the quote, a backslash, DEL and non-ASCII bytes (valid JSON
// has no raw control characters).
var canonStop = func() (t [256]byte) {
	t['"'], t['\\'], t[0x7f] = 1, 1, 1
	for c := 0x80; c < 256; c++ {
		t[c] = 1
	}
	return
}()

// canonicalNumber reports whether jq prints the literal n unchanged.
func canonicalNumber(n []byte) bool {
	s := n
	if s[0] == '-' {
		s = s[1:]
	}
	for _, c := range s {
		if c == 'e' || c == 'E' {
			return false
		}
	}
	if len(s) > 1 && s[0] == '0' && s[1] == '.' {
		z := 0
		for _, c := range s[2:] {
			if c != '0' {
				break
			}
			z++
		}
		return z < 6
	}
	return true
}
