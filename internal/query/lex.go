// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package query

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

type tokKind int

const (
	tEOF tokKind = iota
	tIdent
	tField   // .name or ."name"; text holds the name, str set for a string
	tDot     // .
	tDotDot  // ..
	tVar     // $name
	tFormat  // @name
	tNumber  // text holds the literal
	tString  // parts hold the pieces
	tKeyword // text holds the keyword
	tOp      // text holds the operator or punctuation
)

type strPart struct {
	lit  string  // literal text (when toks is nil)
	toks []token // an interpolation \( ... )
}

type token struct {
	kind  tokKind
	text  string
	parts []strPart
	pos   int
}

var keywords = map[string]bool{
	"if": true, "then": true, "elif": true, "else": true, "end": true,
	"and": true, "or": true, "as": true, "reduce": true, "foreach": true,
	"try": true, "catch": true, "def": true, "label": true, "import": true, "include": true,
	"__loc__": true,
}

// Longest operators first.
var operators = []string{
	"?//=", "?//", "//=", "|=", "+=", "-=", "*=", "/=", "%=", "==", "!=", "<=", ">=", "//", "..",
	"|", ",", "(", ")", "[", "]", "{", "}", ":", ";", "?", "=", "<", ">", "+", "-", "*", "/", "%", ".",
}

// SyntaxError is a problem in the filter text.
type SyntaxError struct {
	Pos int
	Msg string
}

func (e *SyntaxError) Error() string { return e.Msg }

func isIdentStart(c byte) bool { return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isIdentByte(c byte) bool  { return isIdentStart(c) || c >= '0' && c <= '9' }

type lexer struct {
	src string
	i   int
}

// lex turns src into tokens. It stops at the end, or at an unmatched ')'
// when inside an interpolation (closing true).
func (l *lexer) lex(closing bool) ([]token, error) {
	var out []token
	depth := 0
	for {
		// whitespace and comments
		for l.i < len(l.src) {
			c := l.src[l.i]
			if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
				l.i++
			} else if c == '#' {
				for l.i < len(l.src) && l.src[l.i] != '\n' {
					l.i++
				}
			} else {
				break
			}
		}
		if l.i >= len(l.src) {
			if closing {
				return nil, &SyntaxError{l.i, "unterminated string interpolation"}
			}
			return append(out, token{kind: tEOF, pos: l.i}), nil
		}
		start := l.i
		c := l.src[l.i]
		switch {
		case c == '"':
			parts, err := l.lexString()
			if err != nil {
				return nil, err
			}
			out = append(out, token{kind: tString, parts: parts, pos: start})
			continue
		case c == '.' && l.i+1 < len(l.src) && isIdentStart(l.src[l.i+1]):
			l.i++
			s := l.i
			for l.i < len(l.src) && isIdentByte(l.src[l.i]) {
				l.i++
			}
			out = append(out, token{kind: tField, text: l.src[s:l.i], pos: start})
			continue
		case c == '.' && l.i+1 < len(l.src) && l.src[l.i+1] == '"':
			l.i++
			parts, err := l.lexString()
			if err != nil {
				return nil, err
			}
			out = append(out, token{kind: tField, parts: parts, pos: start})
			continue
		case c == '.' && l.i+1 < len(l.src) && l.src[l.i+1] >= '0' && l.src[l.i+1] <= '9':
			// .5 is a number in jq
			out = append(out, l.lexNumber())
			continue
		case c >= '0' && c <= '9':
			out = append(out, l.lexNumber())
			continue
		case c == '$' || c == '@':
			l.i++
			s := l.i
			if l.i < len(l.src) && l.src[l.i] == '_' && strings.HasPrefix(l.src[l.i:], "__loc__") {
				l.i += len("__loc__")
			}
			for l.i < len(l.src) && isIdentByte(l.src[l.i]) {
				l.i++
			}
			if s == l.i {
				return nil, &SyntaxError{start, fmt.Sprintf("expected a name after %c", c)}
			}
			k := tVar
			if c == '@' {
				k = tFormat
			}
			out = append(out, token{kind: k, text: l.src[s:l.i], pos: start})
			continue
		case isIdentStart(c):
			for l.i < len(l.src) && (isIdentByte(l.src[l.i]) || l.src[l.i] == ':' && l.i+1 < len(l.src) && l.src[l.i+1] == ':') {
				if l.src[l.i] == ':' {
					l.i++
				}
				l.i++
			}
			w := l.src[start:l.i]
			if keywords[w] {
				out = append(out, token{kind: tKeyword, text: w, pos: start})
			} else {
				out = append(out, token{kind: tIdent, text: w, pos: start})
			}
			continue
		}
		matched := ""
		for _, op := range operators {
			if strings.HasPrefix(l.src[l.i:], op) {
				matched = op
				break
			}
		}
		if matched == "" {
			r, _ := utf8.DecodeRuneInString(l.src[l.i:])
			return nil, &SyntaxError{start, fmt.Sprintf("unexpected character %q", r)}
		}
		if closing {
			switch matched {
			case "(":
				depth++
			case ")":
				if depth == 0 {
					l.i++
					return append(out, token{kind: tEOF, pos: start}), nil
				}
				depth--
			}
		}
		l.i += len(matched)
		if matched == "." {
			out = append(out, token{kind: tDot, pos: start})
		} else if matched == ".." {
			out = append(out, token{kind: tDotDot, pos: start})
		} else {
			out = append(out, token{kind: tOp, text: matched, pos: start})
		}
	}
}

func (l *lexer) lexNumber() token {
	start := l.i
	for l.i < len(l.src) && (l.src[l.i] >= '0' && l.src[l.i] <= '9' || l.src[l.i] == '.') {
		l.i++
	}
	if l.i < len(l.src) && (l.src[l.i] == 'e' || l.src[l.i] == 'E') {
		j := l.i + 1
		if j < len(l.src) && (l.src[j] == '+' || l.src[j] == '-') {
			j++
		}
		if j < len(l.src) && l.src[j] >= '0' && l.src[j] <= '9' {
			l.i = j
			for l.i < len(l.src) && l.src[l.i] >= '0' && l.src[l.i] <= '9' {
				l.i++
			}
		}
	}
	return token{kind: tNumber, text: l.src[start:l.i], pos: start}
}

// lexString reads a string literal starting at the quote, splitting out
// \( ... ) interpolations.
func (l *lexer) lexString() ([]strPart, error) {
	start := l.i
	l.i++ // quote
	var parts []strPart
	var sb strings.Builder
	for {
		if l.i >= len(l.src) {
			return nil, &SyntaxError{start, "unterminated string"}
		}
		c := l.src[l.i]
		switch {
		case c == '"':
			l.i++
			if sb.Len() > 0 || len(parts) == 0 {
				parts = append(parts, strPart{lit: sb.String()})
			}
			return parts, nil
		case c == '\\':
			if l.i+1 >= len(l.src) {
				return nil, &SyntaxError{start, "unterminated string"}
			}
			e := l.src[l.i+1]
			l.i += 2
			switch e {
			case '"', '\\', '/':
				sb.WriteByte(e)
			case 'b':
				sb.WriteByte('\b')
			case 'f':
				sb.WriteByte('\f')
			case 'n':
				sb.WriteByte('\n')
			case 'r':
				sb.WriteByte('\r')
			case 't':
				sb.WriteByte('\t')
			case 'u':
				r, ok := hex4(l.src, l.i)
				if !ok {
					return nil, &SyntaxError{l.i, "invalid \\u escape in string"}
				}
				l.i += 4
				if r >= 0xd800 && r < 0xdc00 && strings.HasPrefix(l.src[l.i:], `\u`) {
					if r2, ok := hex4(l.src, l.i+2); ok && r2 >= 0xdc00 && r2 < 0xe000 {
						r = (r-0xd800)<<10 + (r2 - 0xdc00) + 0x10000
						l.i += 6
					}
				}
				sb.WriteRune(r)
			case '(':
				if sb.Len() > 0 {
					parts = append(parts, strPart{lit: sb.String()})
					sb.Reset()
				}
				toks, err := l.lex(true)
				if err != nil {
					return nil, err
				}
				parts = append(parts, strPart{toks: toks})
			default:
				return nil, &SyntaxError{l.i - 2, fmt.Sprintf("invalid escape \\%c in string", e)}
			}
		default:
			sb.WriteByte(c)
			l.i++
		}
	}
}

func hex4(s string, i int) (rune, bool) {
	if len(s)-i < 4 {
		return 0, false
	}
	var r rune
	for k := 0; k < 4; k++ {
		c := s[i+k]
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
