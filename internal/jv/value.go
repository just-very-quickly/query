// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

// Package jv holds JSON values the way jq sees them: parsing, lazy access
// into raw JSON text, jq's ordering and equality, and output formatted
// exactly as jq 1.7 formats it.
//
// A value is one of:
//
//	nil      null
//	bool     true, false
//	Number   a number, keeping the literal it was read from
//	string   a string (always valid UTF-8)
//	[]any    an array
//	*Object  an object, keys in insertion order
//	Raw      JSON text not decoded yet (valid, no surrounding space)
//
// Raw is what makes the common case fast: a filter such as
// select(.level == "error") looks at one field of a line without decoding
// the rest, and the line is written out without being rebuilt.
package jv

import (
	"sort"
)

// Raw is the text of one valid JSON value, decoded only when needed.
type Raw []byte

// Number is a JSON number. Lit is the literal it was read from (jq 1.7
// keeps a number's literal while it is unchanged); computed numbers have
// no literal.
type Number struct {
	F   float64
	Lit string
}

// Num makes a computed number.
func Num(f float64) Number { return Number{F: f} }

// Object is a JSON object with keys in insertion order. Setting a key that
// exists keeps its place, as jq does.
type Object struct {
	Keys []string
	Vals []any
	idx  map[string]int
}

// NewObject makes an empty object with room for n keys.
func NewObject(n int) *Object {
	return &Object{Keys: make([]string, 0, n), Vals: make([]any, 0, n)}
}

func (o *Object) Len() int { return len(o.Keys) }

func (o *Object) find(k string) int {
	if o.idx != nil {
		if i, ok := o.idx[k]; ok {
			return i
		}
		return -1
	}
	for i, key := range o.Keys {
		if key == k {
			return i
		}
	}
	return -1
}

// Get returns the value under k.
func (o *Object) Get(k string) (any, bool) {
	if i := o.find(k); i >= 0 {
		return o.Vals[i], true
	}
	return nil, false
}

// Set sets k to v.
func (o *Object) Set(k string, v any) {
	if i := o.find(k); i >= 0 {
		o.Vals[i] = v
		return
	}
	o.Keys = append(o.Keys, k)
	o.Vals = append(o.Vals, v)
	if o.idx != nil {
		o.idx[k] = len(o.Keys) - 1
	} else if len(o.Keys) > 16 {
		o.idx = make(map[string]int, len(o.Keys)*2)
		for i, key := range o.Keys {
			o.idx[key] = i
		}
	}
}

// Delete removes k.
func (o *Object) Delete(k string) {
	i := o.find(k)
	if i < 0 {
		return
	}
	o.Keys = append(o.Keys[:i], o.Keys[i+1:]...)
	o.Vals = append(o.Vals[:i], o.Vals[i+1:]...)
	if o.idx != nil {
		o.idx = make(map[string]int, len(o.Keys)*2)
		for j, key := range o.Keys {
			o.idx[key] = j
		}
	}
}

// Copy returns a shallow copy.
func (o *Object) Copy() *Object {
	c := &Object{Keys: append([]string(nil), o.Keys...), Vals: append([]any(nil), o.Vals...)}
	if o.idx != nil {
		c.idx = make(map[string]int, len(o.idx))
		for k, v := range o.idx {
			c.idx[k] = v
		}
	}
	return c
}

// SortedKeys returns the keys in codepoint order, as jq's keys does.
func (o *Object) SortedKeys() []string {
	ks := append([]string(nil), o.Keys...)
	sort.Strings(ks)
	return ks
}

// Kind names a value's type the way jq's type function does.
type Kind int

const (
	KNull Kind = iota
	KFalse
	KTrue
	KNumber
	KString
	KArray
	KObject
)

var kindNames = [...]string{"null", "boolean", "boolean", "number", "string", "array", "object"}

func (k Kind) String() string { return kindNames[k] }

// KindOf returns v's kind, without decoding a Raw.
func KindOf(v any) Kind {
	switch x := v.(type) {
	case nil:
		return KNull
	case bool:
		if x {
			return KTrue
		}
		return KFalse
	case Number:
		return KNumber
	case string:
		return KString
	case []any:
		return KArray
	case *Object:
		return KObject
	case Raw:
		return rawKind(x)
	}
	panic("jv: not a value")
}

// TypeName is jq's type for v.
func TypeName(v any) string { return KindOf(v).String() }

func rawKind(r Raw) Kind {
	switch r[0] {
	case 'n':
		return KNull
	case 'f':
		return KFalse
	case 't':
		return KTrue
	case '"':
		return KString
	case '[':
		return KArray
	case '{':
		return KObject
	}
	return KNumber
}

// Norm decodes v if it is Raw; other values are returned as they are.
// Containers built by a filter may still hold Raw members.
func Norm(v any) any {
	if r, ok := v.(Raw); ok {
		d, err := Decode(r)
		if err != nil {
			// Raw is only made from validated text.
			panic("jv: invalid raw value: " + err.Error())
		}
		return d
	}
	return v
}

// Truthy reports whether jq treats v as true: everything except null and false.
func Truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case Raw:
		return x[0] != 'n' && x[0] != 'f'
	}
	return true
}

// Detach returns v with every Raw copied out of the buffer it points into,
// for values kept after their input line's buffer is reused.
func Detach(v any) any {
	switch x := v.(type) {
	case Raw:
		return Raw(append([]byte(nil), x...))
	case []any:
		var out []any
		for i, e := range x {
			d := Detach(e)
			if out == nil {
				if !sameValue(d, e) {
					out = make([]any, len(x))
					copy(out, x[:i])
					out[i] = d
				}
				continue
			}
			out[i] = d
		}
		if out == nil {
			return x
		}
		return out
	case *Object:
		var out *Object
		for i, e := range x.Vals {
			d := Detach(e)
			if out == nil {
				if !sameValue(d, e) {
					out = x.Copy()
					out.Vals[i] = d
				}
				continue
			}
			out.Vals[i] = d
		}
		if out == nil {
			return x
		}
		return out
	}
	return v
}

// sameValue reports whether Detach left a value untouched (no Raw inside).
func sameValue(a, b any) bool {
	switch x := a.(type) {
	case Raw:
		return false
	case []any:
		y, ok := b.([]any)
		return ok && len(x) == len(y) && (len(x) == 0 || &x[0] == &y[0])
	case *Object:
		y, ok := b.(*Object)
		return ok && x == y
	}
	return true
}
