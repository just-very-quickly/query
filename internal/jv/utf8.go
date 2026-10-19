// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package jv

// Invalid UTF-8 becomes U+FFFD the way jq does it (jvp_utf8_next): a bad
// lead byte or a stray continuation byte is one replacement; a sequence cut
// short by a byte that isn't a continuation is one replacement for the part
// before that byte; an overlong form, a surrogate or a code point past
// U+10FFFF is one replacement for the whole sequence; and a sequence cut
// short by the end of the string is one replacement for the rest.

const replacement = "�"

// utf8Next decodes the sequence at s[i]: its code point (-1 if invalid)
// and its length.
func utf8Next(s string, i int) (rune, int) {
	c := s[i]
	var n int
	var cp rune
	switch {
	case c < 0x80:
		return rune(c), 1
	case c < 0xC2: // continuation byte, or a lead byte that is always overlong
		return -1, 1
	case c < 0xE0:
		n, cp = 2, rune(c&0x1F)
	case c < 0xF0:
		n, cp = 3, rune(c&0x0F)
	case c < 0xF5:
		n, cp = 4, rune(c&0x07)
	default:
		return -1, 1
	}
	if i+n > len(s) {
		return -1, len(s) - i
	}
	for k := 1; k < n; k++ {
		b := s[i+k]
		if b&0xC0 != 0x80 {
			return -1, k
		}
		cp = cp<<6 | rune(b&0x3F)
	}
	if cp < [5]rune{0, 0, 0x80, 0x800, 0x10000}[n] || cp >= 0xD800 && cp <= 0xDFFF || cp > 0x10FFFF {
		return -1, n
	}
	return cp, n
}

// appendJqUTF8 appends s with each invalid sequence replaced as jq does.
func appendJqUTF8(out []byte, s string) []byte {
	start := 0
	for i := 0; i < len(s); {
		if s[i] < 0x80 {
			i++
			continue
		}
		cp, n := utf8Next(s, i)
		if cp < 0 {
			out = append(out, s[start:i]...)
			out = append(out, replacement...)
			i += n
			start = i
			continue
		}
		i += n
	}
	return append(out, s[start:]...)
}

// ToValidUTF8 returns s with invalid UTF-8 replaced as jq does.
func ToValidUTF8(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			if cp, _ := utf8Next(s, i); cp < 0 {
				return string(appendJqUTF8(nil, s))
			}
		}
	}
	return s
}
