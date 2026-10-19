// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package query

import (
	"bytes"
	"encoding/binary"
	"math"
	"slices"
	"strings"

	"github.com/just-very-quickly/query/internal/jv"
)

// Aggregations are jvqq's own addition to jq: count, count_by(f), sum(f),
// avg(f), min(f), max(f) and group_by(f; aggregations). They collect over
// every value reaching their step of the top-level pipe, in parallel and in
// constant memory (one entry per group), where jq would need -s and the
// whole input in memory. The step that holds them runs once at the end with
// their results; the steps after it run on what it yields.

type aggKind int

const (
	aggCount aggKind = iota
	aggCountBy
	aggSum
	aggAvg
	aggMin
	aggMax
	aggGroupBy
)

var aggNames = map[string]struct {
	kind  aggKind
	arity int
}{
	"count":    {aggCount, 0},
	"count_by": {aggCountBy, 1},
	"sum":      {aggSum, 1},
	"avg":      {aggAvg, 1},
	"min":      {aggMin, 1},
	"max":      {aggMax, 1},
	"group_by": {aggGroupBy, 2},
}

func isAggregation(name string, arity int) bool {
	a, ok := aggNames[name]
	return ok && a.arity == arity
}

const aggVar = "\x00agg"

// aggSpec is a set of aggregations and the expression that combines them.
type aggSpec struct {
	slots []*aggSlot
	stage filter // run at the end, with the results bound to aggVar
}

type aggSlot struct {
	kind aggKind
	arg  filter   // the value (or group key) to aggregate
	sub  *aggSpec // group_by: the aggregations per group
}

type aggCollector struct{ spec *aggSpec }

func (a *aggCollector) add(c *compiler, x nCall) (filter, error) {
	slot := &aggSlot{kind: aggNames[x.name].kind}
	// Arguments see only the global variables: they run on every input,
	// before any binding in the step exists.
	saved, savedAgg := c.vars, c.agg
	c.vars = c.globals()
	c.agg = nil
	defer func() { c.vars, c.agg = saved, savedAgg }()
	if len(x.args) > 0 {
		f, err := c.compile(x.args[0])
		if err != nil {
			return nil, err
		}
		slot.arg = f
	}
	if slot.kind == aggGroupBy {
		sub := &aggCollector{spec: &aggSpec{}}
		c.agg = sub
		f, err := c.compile(x.args[1])
		if err != nil {
			return nil, err
		}
		if len(sub.spec.slots) == 0 {
			return nil, &SyntaxError{0, "the second argument of group_by(f; ...) must use aggregations, such as count or avg(.ms)"}
		}
		sub.spec.stage = f
		slot.sub = sub.spec
	}
	idx := len(a.spec.slots)
	a.spec.slots = append(a.spec.slots, slot)
	return func(e *env, _ any, out emitFn) error {
		res, _ := e.lookup(aggVar)
		return out(res.([]any)[idx])
	}, nil
}

// aggState holds running totals for an aggSpec.
type aggState struct {
	spec *aggSpec
	accs []acc
}

type acc interface {
	add(v any) error
	merge(o acc)
	result() any
}

func newAggState(spec *aggSpec) *aggState {
	s := &aggState{spec: spec, accs: make([]acc, len(spec.slots))}
	for i, sl := range spec.slots {
		switch sl.kind {
		case aggCount:
			s.accs[i] = &countAcc{}
		case aggCountBy:
			s.accs[i] = &countByAcc{m: map[string]countEntry{}}
		case aggSum:
			s.accs[i] = &sumAcc{name: "sum"}
		case aggAvg:
			s.accs[i] = &avgAcc{sumAcc{name: "avg"}}
		case aggMin:
			s.accs[i] = &extremeAcc{max: false}
		case aggMax:
			s.accs[i] = &extremeAcc{max: true}
		case aggGroupBy:
			s.accs[i] = &groupAcc{spec: sl.sub, m: map[string]*groupEntry{}}
		}
	}
	return s
}

// feed adds one input value to every aggregation.
func (s *aggState) feed(e *env, v any) error {
	for i, sl := range s.spec.slots {
		switch sl.kind {
		case aggCount:
			s.accs[i].(*countAcc).n++
		case aggGroupBy:
			g := s.accs[i].(*groupAcc)
			if err := sl.arg(e, v, func(k any) error { return g.feed(e, k, v) }); err != nil {
				return err
			}
		default:
			a := s.accs[i]
			if err := sl.arg(e, v, func(x any) error { return a.add(x) }); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *aggState) merge(o *aggState) {
	for i := range s.accs {
		s.accs[i].merge(o.accs[i])
	}
}

func (s *aggState) results() []any {
	out := make([]any, len(s.accs))
	for i, a := range s.accs {
		out[i] = a.result()
	}
	return out
}

type countAcc struct{ n int64 }

func (a *countAcc) add(any) error { a.n++; return nil }
func (a *countAcc) merge(o acc)   { a.n += o.(*countAcc).n }
func (a *countAcc) result() any   { return jv.Num(float64(a.n)) }

// groupKey is a string equal for values jq considers equal.
func groupKey(v any) string {
	return string(appendKey(nil, v))
}

// appendKey appends the group key of v to b: equal for values jq
// considers equal.
func appendKey(b []byte, v any) []byte {
	switch jv.KindOf(v) {
	case jv.KNumber:
		n, _ := jv.AsNumber(v)
		f := n.F
		if f == 0 {
			f = 0 // -0 equals 0
		}
		return binary.LittleEndian.AppendUint64(append(b, 'n'), math.Float64bits(f))
	case jv.KString:
		b = append(b, 's')
		if r, ok := jv.RawOf(v); ok {
			if body, ok := jv.RawPlainString(r); ok {
				return append(b, body...)
			}
		}
		return append(b, jv.Str(v)...)
	case jv.KArray:
		b = append(b, '[')
		for _, e := range jv.Norm(v).([]any) {
			b = appendKey(b, e)
			b = append(b, 0)
		}
		return append(b, ']')
	case jv.KObject:
		o := jv.Norm(v).(*jv.Object)
		b = append(b, '{')
		for _, k := range o.SortedKeys() {
			b = append(b, k...)
			b = append(b, 0)
			x, _ := o.Get(k)
			b = appendKey(b, x)
			b = append(b, 0)
		}
		return append(b, '}')
	}
	return append(b, jv.KindOf(v).String()...)
}

type countEntry struct {
	key any
	n   int64
}

type countByAcc struct {
	m   map[string]countEntry
	buf []byte
}

// preferKey picks between two keys jq considers equal, so the one shown
// doesn't depend on input order: numbers like 1 and 1.0 print differently.
func preferKey(cur, cand any) any {
	if jv.KindOf(cur) != jv.KNumber {
		return cur
	}
	if a, ok := jv.RawOf(cur); ok {
		if b, ok := jv.RawOf(cand); ok && bytes.Equal(a, b) {
			return cur // the same text: nothing to choose
		}
	}
	if toString(cand) < toString(cur) {
		return jv.Detach(cand)
	}
	return cur
}

func (a *countByAcc) add(k any) error {
	a.buf = appendKey(a.buf[:0], k)
	if e, ok := a.m[string(a.buf)]; ok { // looked up without a copy
		e.n++
		e.key = preferKey(e.key, k)
		a.m[string(a.buf)] = e
		return nil
	}
	a.m[string(a.buf)] = countEntry{jv.Detach(k), 1}
	return nil
}

func (a *countByAcc) merge(o acc) {
	for gk, oe := range o.(*countByAcc).m {
		if e, ok := a.m[gk]; ok {
			e.n += oe.n
			e.key = preferKey(e.key, oe.key)
			a.m[gk] = e
		} else {
			a.m[gk] = oe
		}
	}
}

// compareKeys is jv.Compare with quick paths for the usual keys: decoded
// numbers and strings.
func compareKeys(a, b any) int {
	switch x := a.(type) {
	case jv.Number:
		if y, ok := b.(jv.Number); ok && x.F != y.F && x.F == x.F && y.F == y.F {
			if x.F < y.F {
				return -1
			}
			return 1
		}
	case string:
		if y, ok := b.(string); ok {
			return strings.Compare(x, y)
		}
	}
	return jv.Compare(a, b)
}

// result is an object of counts, the most frequent first.
func (a *countByAcc) result() any {
	es := make([]countEntry, 0, len(a.m))
	for _, e := range a.m {
		e.key = jv.Norm(e.key) // decode raw keys once, not per comparison
		es = append(es, e)
	}
	slices.SortFunc(es, func(x, y countEntry) int {
		if x.n != y.n {
			if x.n > y.n {
				return -1
			}
			return 1
		}
		return compareKeys(x.key, y.key)
	})
	o := jv.NewObject(len(es))
	for _, e := range es {
		k := toString(e.key)
		if prev, ok := o.Get(k); ok {
			o.Set(k, jv.Num(prev.(jv.Number).F+float64(e.n)))
			continue
		}
		o.Set(k, jv.Num(float64(e.n)))
	}
	return o
}

// sumAcc adds exactly (see exactSum), so the result doesn't depend on how
// the input was split between workers.
type sumAcc struct {
	name string
	sum  exactSum
	n    int64
}

func (a *sumAcc) add(v any) error {
	if jv.KindOf(v) == jv.KNull {
		return nil
	}
	num, ok := jv.AsNumber(v)
	if !ok {
		return errorf("%s: %s is not a number", a.name, describe(v))
	}
	a.n++
	a.sum.add(num.F)
	return nil
}

func (a *sumAcc) merge(o acc) {
	var b *sumAcc
	switch x := o.(type) {
	case *sumAcc:
		b = x
	case *avgAcc:
		b = &x.sumAcc
	}
	a.n += b.n
	a.sum.merge(&b.sum)
}

func (a *sumAcc) total() float64 { return a.sum.total() }

func (a *sumAcc) result() any { return jv.Num(a.total()) }

type avgAcc struct{ sumAcc }

func (a *avgAcc) merge(o acc) { a.sumAcc.merge(o) }

func (a *avgAcc) result() any {
	if a.n == 0 {
		return nil
	}
	return jv.Num(a.total() / float64(a.n))
}

type extremeAcc struct {
	max  bool
	best any
	set  bool
}

func (a *extremeAcc) add(v any) error {
	if jv.KindOf(v) == jv.KNull {
		return nil
	}
	if !a.set {
		a.best, a.set = jv.Detach(v), true
		return nil
	}
	c := jv.Compare(v, a.best)
	if a.max && c > 0 || !a.max && c < 0 {
		a.best = jv.Detach(v)
	} else if c == 0 {
		a.best = preferKey(a.best, v)
	}
	return nil
}

func (a *extremeAcc) merge(o acc) {
	b := o.(*extremeAcc)
	if !b.set {
		return
	}
	if !a.set {
		a.best, a.set = b.best, true
		return
	}
	c := jv.Compare(b.best, a.best)
	if a.max && c > 0 || !a.max && c < 0 {
		a.best = b.best
	} else if c == 0 {
		a.best = preferKey(a.best, b.best)
	}
}

func (a *extremeAcc) result() any { return a.best }

type groupEntry struct {
	key   any
	state *aggState
}

type groupAcc struct {
	spec *aggSpec
	m    map[string]*groupEntry
	env  *env
	buf  []byte
}

func (a *groupAcc) feed(e *env, k, v any) error {
	a.env = e
	a.buf = appendKey(a.buf[:0], k)
	g, ok := a.m[string(a.buf)]
	if !ok {
		g = &groupEntry{jv.Detach(k), newAggState(a.spec)}
		a.m[string(a.buf)] = g
	} else {
		g.key = preferKey(g.key, k)
	}
	return g.state.feed(e, v)
}

func (a *groupAcc) add(any) error { return nil }

func (a *groupAcc) merge(o acc) {
	b := o.(*groupAcc)
	if a.env == nil {
		a.env = b.env
	}
	for gk, be := range b.m {
		if ae, ok := a.m[gk]; ok {
			ae.state.merge(be.state)
			ae.key = preferKey(ae.key, be.key)
		} else {
			a.m[gk] = be
		}
	}
}

// result is one object per group, sorted by key: {"key": k} followed by
// the fields of the aggregation expression (or "value" when it isn't an
// object).
func (a *groupAcc) result() any {
	es := make([]*groupEntry, 0, len(a.m))
	for _, e := range a.m {
		e.key = jv.Norm(e.key) // decode raw keys once, not per comparison
		es = append(es, e)
	}
	slices.SortFunc(es, func(x, y *groupEntry) int { return compareKeys(x.key, y.key) })
	rows := []any{}
	for _, g := range es {
		e := a.env.bind(aggVar, g.state.results())
		err := a.spec.stage(e, nil, func(v any) error {
			row := jv.NewObject(4)
			row.Set("key", g.key)
			if o, ok := jv.Norm(v).(*jv.Object); ok {
				for i, k := range o.Keys {
					row.Set(k, o.Vals[i])
				}
			} else {
				row.Set("value", v)
			}
			rows = append(rows, row)
			return nil
		})
		if err != nil {
			row := jv.NewObject(2)
			row.Set("key", g.key)
			row.Set("error", err.Error())
			rows = append(rows, row)
		}
	}
	return rows
}

// exactSum keeps a sum without rounding error, as Shewchuk's algorithm
// (Python's math.fsum) does: a list of non-overlapping partial sums. Its
// rounded total is the same whatever order the values were added in, so
// sums and averages don't depend on how the input was split between workers.
type exactSum struct {
	partials []float64
	special  float64 // inf or nan seen
	hasSpec  bool
}

func (s *exactSum) add(x float64) {
	if math.IsInf(x, 0) || math.IsNaN(x) {
		if s.hasSpec {
			s.special += x
		} else {
			s.special, s.hasSpec = x, true
		}
		return
	}
	i := 0
	for _, y := range s.partials {
		if math.Abs(x) < math.Abs(y) {
			x, y = y, x
		}
		hi := x + y
		lo := y - (hi - x)
		if lo != 0 {
			s.partials[i] = lo
			i++
		}
		x = hi
	}
	s.partials = append(s.partials[:i], x)
}

func (s *exactSum) merge(o *exactSum) {
	for _, p := range o.partials {
		s.add(p)
	}
	if o.hasSpec {
		s.add(o.special)
	}
}

// total is the correctly rounded sum (the msum algorithm's final step).
func (s *exactSum) total() float64 {
	if s.hasSpec {
		return s.special
	}
	n := len(s.partials)
	if n == 0 {
		return 0
	}
	hi := s.partials[n-1]
	lo := 0.0
	n--
	for n > 0 {
		x := hi
		y := s.partials[n-1]
		n--
		hi = x + y
		yr := hi - x
		lo = y - yr
		if lo != 0 {
			break
		}
	}
	if n > 0 && (lo < 0 && s.partials[n-1] < 0 || lo > 0 && s.partials[n-1] > 0) {
		y := lo * 2
		x := hi + y
		yr := x - hi
		if y == yr {
			hi = x
		}
	}
	return hi
}
