// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package query

import (
	"fmt"
	"strings"

	"github.com/just-very-quickly/query/internal/jv"
)

// The syntax tree.
type node interface{}

type (
	nIdentity struct{}
	nRecurse  struct{} // ..
	// opt is set by a trailing "?", which in jq covers only the last step
	// of a chain: in .a.b? an error from .a is still an error.
	nField struct {
		target node
		name   string
		opt    bool
	}
	nIndex struct {
		target, index node
		opt           bool
	}
	nSlice struct {
		target, from, to node // from or to may be nil
		opt              bool
	}
	nIterate struct {
		target node
		opt    bool
	}
	nTry     struct {
		body, catch node // catch may be nil; nil catch with silent=true is "?"
	}
	nPipe   struct{ left, right node }
	nComma  struct{ left, right node }
	nAlt    struct{ left, right node }
	nAnd    struct{ left, right node }
	nOr     struct{ left, right node }
	nBinop  struct {
		op          string
		left, right node
	}
	nNeg     struct{ x node }
	nLiteral struct{ v any }
	nString  struct {
		parts  []node // string literals and expressions
		format string // "" for @text
	}
	nFormat struct{ name string }
	nArray  struct{ body node } // body nil for []
	nObject struct{ entries []objEntry }
	nIf     struct{ cond, then, els node } // els nil means "else ."
	nReduce struct {
		source             node
		name               string
		init, update       node
	}
	nForeach struct {
		source                node
		name                  string
		init, update, extract node // extract may be nil
	}
	nBind struct {
		source node
		name   string
		body   node
	}
	nVar  struct{ name string }
	nCall struct {
		name string
		args []node
	}
	nAssign struct {
		op       string // = |= += -= *= /= %= //=
		lhs, rhs node
	}
)

type objEntry struct {
	key   node // evaluates to the key string
	value node
}

type parser struct {
	toks []token
	i    int
	src  string
}

// parse parses a whole filter.
func parse(src string) (node, error) {
	l := &lexer{src: src}
	toks, err := l.lex(false)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks, src: src}
	if p.peek().kind == tEOF {
		return nIdentity{}, nil
	}
	n, err := p.pipe()
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind != tEOF {
		return nil, p.errorf(t, "unexpected %s", describeTok(t))
	}
	return n, nil
}

func describeTok(t token) string {
	switch t.kind {
	case tEOF:
		return "end of filter"
	case tIdent, tKeyword:
		return fmt.Sprintf("%q", t.text)
	case tField:
		return fmt.Sprintf("%q", "."+t.text)
	case tDot:
		return `"."`
	case tDotDot:
		return `".."`
	case tVar:
		return fmt.Sprintf("%q", "$"+t.text)
	case tFormat:
		return fmt.Sprintf("%q", "@"+t.text)
	case tNumber:
		return fmt.Sprintf("number %s", t.text)
	case tString:
		return "string"
	}
	return fmt.Sprintf("%q", t.text)
}

func (p *parser) peek() token { return p.toks[p.i] }
func (p *parser) next() token  { t := p.toks[p.i]; p.i++; return t }

func (p *parser) isOp(s string) bool {
	t := p.peek()
	return t.kind == tOp && t.text == s
}

func (p *parser) isKw(s string) bool {
	t := p.peek()
	return t.kind == tKeyword && t.text == s
}

func (p *parser) errorf(t token, format string, args ...any) error {
	return &SyntaxError{t.pos, fmt.Sprintf(format, args...)}
}

func (p *parser) expectOp(s string) error {
	if !p.isOp(s) {
		t := p.peek()
		return p.errorf(t, "expected %q, found %s", s, describeTok(t))
	}
	p.i++
	return nil
}

func (p *parser) expectKw(s string) error {
	if !p.isKw(s) {
		t := p.peek()
		return p.errorf(t, "expected %q, found %s", s, describeTok(t))
	}
	p.i++
	return nil
}

// pipe: comma ('|' pipe)?
func (p *parser) pipe() (node, error) {
	if p.isKw("def") {
		return nil, p.errorf(p.peek(), "function definitions (def) aren't supported")
	}
	l, err := p.comma()
	if err != nil {
		return nil, err
	}
	if p.isOp("|") {
		p.i++
		r, err := p.pipe()
		if err != nil {
			return nil, err
		}
		return nPipe{l, r}, nil
	}
	return l, nil
}

func (p *parser) comma() (node, error) {
	l, err := p.alt()
	if err != nil {
		return nil, err
	}
	for p.isOp(",") {
		p.i++
		r, err := p.alt()
		if err != nil {
			return nil, err
		}
		l = nComma{l, r}
	}
	return l, nil
}

// alt: assign ('//' alt)?   (right-associative)
func (p *parser) alt() (node, error) {
	l, err := p.assign()
	if err != nil {
		return nil, err
	}
	if p.isOp("//") {
		p.i++
		r, err := p.alt()
		if err != nil {
			return nil, err
		}
		return nAlt{l, r}, nil
	}
	if p.isOp("?//") {
		return nil, p.errorf(p.peek(), "destructuring alternatives (?//) aren't supported")
	}
	return l, nil
}

var assignOps = map[string]bool{"=": true, "|=": true, "+=": true, "-=": true, "*=": true, "/=": true, "%=": true, "//=": true}

func (p *parser) assign() (node, error) {
	l, err := p.or()
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind == tOp && assignOps[t.text] {
		p.i++
		r, err := p.alt()
		if err != nil {
			return nil, err
		}
		return nAssign{t.text, l, r}, nil
	}
	return l, nil
}

func (p *parser) or() (node, error) {
	l, err := p.and()
	if err != nil {
		return nil, err
	}
	for p.isKw("or") {
		p.i++
		r, err := p.and()
		if err != nil {
			return nil, err
		}
		l = nOr{l, r}
	}
	return l, nil
}

func (p *parser) and() (node, error) {
	l, err := p.compare()
	if err != nil {
		return nil, err
	}
	for p.isKw("and") {
		p.i++
		r, err := p.compare()
		if err != nil {
			return nil, err
		}
		l = nAnd{l, r}
	}
	return l, nil
}

var cmpOps = map[string]bool{"==": true, "!=": true, "<": true, "<=": true, ">": true, ">=": true}

func (p *parser) compare() (node, error) {
	l, err := p.additive()
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind == tOp && cmpOps[t.text] {
		p.i++
		r, err := p.additive()
		if err != nil {
			return nil, err
		}
		if t2 := p.peek(); t2.kind == tOp && cmpOps[t2.text] {
			return nil, p.errorf(t2, "comparisons can't be chained; use parentheses")
		}
		return nBinop{t.text, l, r}, nil
	}
	return l, nil
}

func (p *parser) additive() (node, error) {
	l, err := p.multiplicative()
	if err != nil {
		return nil, err
	}
	for p.isOp("+") || p.isOp("-") {
		op := p.next().text
		r, err := p.multiplicative()
		if err != nil {
			return nil, err
		}
		l = nBinop{op, l, r}
	}
	return l, nil
}

func (p *parser) multiplicative() (node, error) {
	l, err := p.unary()
	if err != nil {
		return nil, err
	}
	for p.isOp("*") || p.isOp("/") || p.isOp("%") {
		op := p.next().text
		r, err := p.unary()
		if err != nil {
			return nil, err
		}
		l = nBinop{op, l, r}
	}
	return l, nil
}

func (p *parser) unary() (node, error) {
	if p.isOp("-") {
		p.i++
		x, err := p.unary()
		if err != nil {
			return nil, err
		}
		return nNeg{x}, nil
	}
	return p.postfix(true)
}

// postfix: primary followed by .name, [..], ?, and "as $x | body".
func (p *parser) postfix(allowAs bool) (node, error) {
	n, err := p.primary()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		switch {
		case t.kind == tField:
			p.i++
			n, err = p.fieldNode(n, t)
			if err != nil {
				return nil, err
			}
		case t.kind == tDot && p.toks[p.i+1].kind == tOp && p.toks[p.i+1].text == "[":
			p.i++ // .[ after a term, as in .a.[0]
		case t.kind == tOp && t.text == "[":
			p.i++
			n, err = p.bracket(n)
			if err != nil {
				return nil, err
			}
		case t.kind == tOp && t.text == "?":
			p.i++
			n = optional(n)
		case allowAs && t.kind == tKeyword && t.text == "as":
			p.i++
			name, err := p.varName()
			if err != nil {
				return nil, err
			}
			if err := p.expectOp("|"); err != nil {
				return nil, err
			}
			body, err := p.pipe()
			if err != nil {
				return nil, err
			}
			return nBind{n, name, body}, nil
		default:
			return n, nil
		}
	}
}

// optional applies a trailing "?" to the last step of n.
func optional(n node) node {
	switch x := n.(type) {
	case nField:
		x.opt = true
		return x
	case nIndex:
		x.opt = true
		return x
	case nSlice:
		x.opt = true
		return x
	case nIterate:
		x.opt = true
		return x
	}
	return nTry{body: n}
}

func (p *parser) varName() (string, error) {
	t := p.peek()
	if t.kind == tOp && (t.text == "[" || t.text == "{") {
		return "", p.errorf(t, "destructuring patterns aren't supported; bind one $name")
	}
	if t.kind != tVar {
		return "", p.errorf(t, "expected $name, found %s", describeTok(t))
	}
	p.i++
	return t.text, nil
}

func (p *parser) fieldNode(target node, t token) (node, error) {
	if t.parts == nil {
		return nField{target: target, name: t.text}, nil
	}
	s, err := p.stringNode(t.parts, "")
	if err != nil {
		return nil, err
	}
	if lit, ok := s.(nLiteral); ok {
		return nField{target: target, name: lit.v.(string)}, nil
	}
	return nIndex{target: target, index: s}, nil
}

// bracket parses after '[': ']' | expr ']' | expr? ':' expr? ']'
func (p *parser) bracket(target node) (node, error) {
	if p.isOp("]") {
		p.i++
		return nIterate{target: target}, nil
	}
	var from, to node
	var err error
	if !p.isOp(":") {
		from, err = p.pipe()
		if err != nil {
			return nil, err
		}
	}
	if p.isOp(":") {
		p.i++
		if !p.isOp("]") {
			to, err = p.pipe()
			if err != nil {
				return nil, err
			}
		}
		if err := p.expectOp("]"); err != nil {
			return nil, err
		}
		return nSlice{target: target, from: from, to: to}, nil
	}
	if err := p.expectOp("]"); err != nil {
		return nil, err
	}
	return nIndex{target: target, index: from}, nil
}

func (p *parser) primary() (node, error) {
	t := p.next()
	switch t.kind {
	case tDot:
		if p.isOp("[") {
			return nIdentity{}, nil
		}
		if p.peek().kind == tString {
			// . "x" is not a thing; leave the string to the caller
			return nIdentity{}, nil
		}
		return nIdentity{}, nil
	case tDotDot:
		return nRecurse{}, nil
	case tField:
		return p.fieldNode(nIdentity{}, t)
	case tNumber:
		return nLiteral{numberLiteral(t.text)}, nil
	case tString:
		return p.stringNode(t.parts, "")
	case tFormat:
		if p.peek().kind == tString {
			s := p.next()
			return p.stringNode(s.parts, t.text)
		}
		return nFormat{t.text}, nil
	case tVar:
		if t.text == "__loc__" {
			o := jv.NewObject(2)
			o.Set("file", "<top-level>")
			o.Set("line", jv.Num(float64(1+strings.Count(p.src[:t.pos], "\n"))))
			return nLiteral{o}, nil
		}
		return nVar{t.text}, nil
	case tIdent:
		switch t.text {
		case "true":
			return nLiteral{true}, nil
		case "false":
			return nLiteral{false}, nil
		case "null":
			return nLiteral{nil}, nil
		}
		var args []node
		if p.isOp("(") {
			p.i++
			for {
				a, err := p.pipe()
				if err != nil {
					return nil, err
				}
				args = append(args, a)
				if p.isOp(";") {
					p.i++
					continue
				}
				if err := p.expectOp(")"); err != nil {
					return nil, err
				}
				break
			}
		}
		return nCall{t.text, args}, nil
	case tKeyword:
		switch t.text {
		case "if":
			return p.ifNode()
		case "try":
			body, err := p.postfix(false)
			if err != nil {
				return nil, err
			}
			var catch node = nCall{name: "empty"}
			if p.isKw("catch") {
				p.i++
				catch, err = p.postfix(false)
				if err != nil {
					return nil, err
				}
			}
			return nTry{body: body, catch: catch}, nil
		case "reduce", "foreach":
			return p.reduceNode(t.text)
		case "def":
			return nil, p.errorf(t, "function definitions (def) aren't supported")
		case "label":
			return nil, p.errorf(t, "label and break aren't supported")
		case "import", "include":
			return nil, p.errorf(t, "modules aren't supported")
		}
		return nil, p.errorf(t, "unexpected %s", describeTok(t))
	case tOp:
		switch t.text {
		case "(":
			n, err := p.pipe()
			if err != nil {
				return nil, err
			}
			if err := p.expectOp(")"); err != nil {
				return nil, err
			}
			return n, nil
		case "[":
			if p.isOp("]") {
				p.i++
				return nArray{}, nil
			}
			n, err := p.pipe()
			if err != nil {
				return nil, err
			}
			if err := p.expectOp("]"); err != nil {
				return nil, err
			}
			return nArray{n}, nil
		case "{":
			return p.objectNode()
		}
	}
	return nil, p.errorf(t, "unexpected %s", describeTok(t))
}

func numberLiteral(text string) any {
	if _, err := jv.Valid([]byte(text), 0); err == nil {
		if end, _ := jv.Valid([]byte(text), 0); end == len(text) {
			return jv.NumberFromLit(text)
		}
	}
	// jq accepts forms such as 1. and .5; they are plain numbers
	n := jv.NumberFromLit(text)
	n.Lit = ""
	return n
}

func (p *parser) ifNode() (node, error) {
	cond, err := p.pipe()
	if err != nil {
		return nil, err
	}
	if err := p.expectKw("then"); err != nil {
		return nil, err
	}
	then, err := p.pipe()
	if err != nil {
		return nil, err
	}
	n := nIf{cond: cond, then: then}
	switch {
	case p.isKw("elif"):
		p.i++
		els, err := p.ifNode()
		if err != nil {
			return nil, err
		}
		n.els = els
		return n, nil
	case p.isKw("else"):
		p.i++
		n.els, err = p.pipe()
		if err != nil {
			return nil, err
		}
	}
	if err := p.expectKw("end"); err != nil {
		return nil, err
	}
	return n, nil
}

func (p *parser) reduceNode(kw string) (node, error) {
	source, err := p.postfix(false)
	if err != nil {
		return nil, err
	}
	if err := p.expectKw("as"); err != nil {
		return nil, err
	}
	name, err := p.varName()
	if err != nil {
		return nil, err
	}
	if err := p.expectOp("("); err != nil {
		return nil, err
	}
	init, err := p.pipe()
	if err != nil {
		return nil, err
	}
	if err := p.expectOp(";"); err != nil {
		return nil, err
	}
	update, err := p.pipe()
	if err != nil {
		return nil, err
	}
	if kw == "reduce" {
		if err := p.expectOp(")"); err != nil {
			return nil, err
		}
		return nReduce{source, name, init, update}, nil
	}
	var extract node
	if p.isOp(";") {
		p.i++
		extract, err = p.pipe()
		if err != nil {
			return nil, err
		}
	}
	if err := p.expectOp(")"); err != nil {
		return nil, err
	}
	return nForeach{source, name, init, update, extract}, nil
}

func (p *parser) objectNode() (node, error) {
	var entries []objEntry
	if p.isOp("}") {
		p.i++
		return nObject{}, nil
	}
	for {
		t := p.next()
		var e objEntry
		switch {
		case t.kind == tVar:
			if t.text == "__loc__" {
				return nil, p.errorf(t, "$__loc__ can't be used as an object key")
			}
			e = objEntry{nLiteral{t.text}, nVar{t.text}}
			if p.isOp(":") {
				return nil, p.errorf(p.peek(), "a $variable key can't take a value")
			}
			entries = append(entries, e)
		case t.kind == tIdent || t.kind == tKeyword:
			e.key = nLiteral{t.text}
			if err := p.objValue(&e, nField{target: nIdentity{}, name: t.text}); err != nil {
				return nil, err
			}
			entries = append(entries, e)
		case t.kind == tNumber:
			return nil, p.errorf(t, "object keys must be strings; put the number in quotes")
		case t.kind == tString || t.kind == tFormat && p.peek().kind == tString:
			format := ""
			if t.kind == tFormat {
				format = t.text
				t = p.next()
			}
			k, err := p.stringNode(t.parts, format)
			if err != nil {
				return nil, err
			}
			e.key = k
			if err := p.objValue(&e, nIndex{target: nIdentity{}, index: k}); err != nil {
				return nil, err
			}
			entries = append(entries, e)
		case t.kind == tOp && t.text == "(":
			k, err := p.pipe()
			if err != nil {
				return nil, err
			}
			if err := p.expectOp(")"); err != nil {
				return nil, err
			}
			e.key = k
			if err := p.expectOp(":"); err != nil {
				return nil, err
			}
			if e.value, err = p.objVal(); err != nil {
				return nil, err
			}
			entries = append(entries, e)
		default:
			return nil, p.errorf(t, "unexpected %s in object", describeTok(t))
		}
		if p.isOp(",") {
			p.i++
			continue
		}
		if err := p.expectOp("}"); err != nil {
			return nil, err
		}
		return nObject{entries}, nil
	}
}

// objValue reads ": value", or uses the shorthand value when there is none.
func (p *parser) objValue(e *objEntry, shorthand node) error {
	if !p.isOp(":") {
		e.value = shorthand
		return nil
	}
	p.i++
	v, err := p.objVal()
	if err != nil {
		return err
	}
	e.value = v
	return nil
}

// objVal is an object value: an expression without a top-level comma,
// with pipes allowed.
func (p *parser) objVal() (node, error) {
	l, err := p.alt()
	if err != nil {
		return nil, err
	}
	if p.isOp("|") {
		p.i++
		r, err := p.objVal()
		if err != nil {
			return nil, err
		}
		return nPipe{l, r}, nil
	}
	return l, nil
}

// stringNode builds a string literal or interpolation.
func (p *parser) stringNode(parts []strPart, format string) (node, error) {
	if len(parts) == 1 && parts[0].toks == nil {
		return nLiteral{parts[0].lit}, nil
	}
	var out []node
	for _, part := range parts {
		if part.toks == nil {
			out = append(out, nLiteral{part.lit})
			continue
		}
		sub := &parser{toks: part.toks, src: p.src}
		if sub.peek().kind == tEOF {
			return nil, p.errorf(sub.peek(), "empty string interpolation")
		}
		n, err := sub.pipe()
		if err != nil {
			return nil, err
		}
		if t := sub.peek(); t.kind != tEOF {
			return nil, sub.errorf(t, "unexpected %s in string interpolation", describeTok(t))
		}
		out = append(out, n)
	}
	return nString{out, format}, nil
}
