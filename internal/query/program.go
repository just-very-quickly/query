// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

// Package query compiles and runs jq filters: the subset of jq people use
// on logs, plus aggregations that work in parallel without -s.
package query

import (
	"fmt"
	"sort"

	"github.com/just-very-quickly/query/internal/jv"
)

// Program is a compiled filter. It is safe for concurrent use; running
// state lives in the State values and in the per-call environment.
type Program struct {
	vars map[string]any // --arg and --argjson values

	run filter // the whole filter, when there is no aggregation

	pre  filter   // the steps before the aggregation step
	agg  *aggSpec // the aggregation step
	post filter   // the steps after it (nil if none)

	usesInputs bool
}

// Compile compiles a filter. vars are the values of $name variables given
// on the command line.
func Compile(src string, vars map[string]any) (*Program, error) {
	ast, err := parse(src)
	if err != nil {
		return nil, err
	}
	p := &Program{vars: vars}
	walkNode(ast, func(n node) {
		if c, ok := n.(nCall); ok && len(c.args) == 0 && (c.name == "input" || c.name == "inputs") {
			p.usesInputs = true
		}
	})
	c := &compiler{vars: map[string]int{}, globalNames: map[string]bool{}}
	for name := range vars {
		c.vars[name] = 1
		c.globalNames[name] = true
	}
	stages := flattenPipe(ast)
	idx := -1
	for i, s := range stages {
		if containsAgg(s) {
			if idx >= 0 {
				return nil, &SyntaxError{0, "aggregations (count, count_by, sum, avg, min, max, group_by(f; ...)) can only be used in one step of the top-level pipe"}
			}
			idx = i
		}
	}
	if idx < 0 {
		if p.run, err = c.compile(ast); err != nil {
			return nil, err
		}
		return p, nil
	}
	if p.pre, err = c.compile(joinPipe(stages[:idx])); err != nil {
		return nil, err
	}
	col := &aggCollector{spec: &aggSpec{}}
	c.agg = col
	stage, err := c.compile(stages[idx])
	c.agg = nil
	if err != nil {
		return nil, err
	}
	col.spec.stage = stage
	p.agg = col.spec
	if idx+1 < len(stages) {
		if p.post, err = c.compile(joinPipe(stages[idx+1:])); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func (c *compiler) globals() map[string]int {
	g := map[string]int{}
	for name := range c.globalNames {
		g[name] = 1
	}
	return g
}

// HasAggregation reports whether the filter aggregates over all inputs.
func (p *Program) HasAggregation() bool { return p.agg != nil }

func (p *Program) rootEnv(sink DebugSink) *env {
	var e *env
	names := make([]string, 0, len(p.vars))
	for name := range p.vars {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		e = e.bind(name, p.vars[name])
	}
	if sink != nil {
		e = e.bind(debugVar, sink)
	}
	return e
}

// Runner runs a program on inputs. Each worker has its own.
type Runner struct {
	p     *Program
	base  *env
	env   *env
	state *aggState
}

// NewRunner makes a runner; debug and stderr messages go to sink (nil for
// standard error).
func (p *Program) NewRunner(sink DebugSink) *Runner {
	base := p.rootEnv(sink)
	r := &Runner{p: p, base: base, env: base}
	if p.agg != nil {
		r.state = newAggState(p.agg)
	}
	return r
}

// InputSource gives input and inputs the next input value.
type InputSource interface {
	Next() (any, bool, error)
}

const inputsVar = "\x00inputs"

// SetInputs gives the filter's input and inputs builtins their values.
func (r *Runner) SetInputs(src InputSource) {
	r.base = r.base.bind(inputsVar, src)
	r.env = r.base
}

// UsesInputs reports whether the filter reads input itself (input,
// inputs), which makes it run in order on one core.
func (p *Program) UsesInputs() bool { return p.usesInputs }

// SetFile names the input being read, for input_filename.
func (r *Runner) SetFile(name string) {
	r.env = r.base.bind(fileVar, name)
}

// Run runs the filter on one input. With an aggregation, the values that
// reach the aggregation step are added to the runner's totals and nothing
// is emitted; the totals come out in Finish.
func (r *Runner) Run(in any, out func(any) error) error {
	if r.p.agg == nil {
		return r.p.run(r.env, in, out)
	}
	return r.p.pre(r.env, in, func(v any) error { return r.state.feed(r.env, v) })
}

// Merge adds the totals of another runner of the same program. Merging in
// input order keeps the results the same however the input was split.
func (r *Runner) Merge(o *Runner) {
	if r.state != nil {
		r.state.merge(o.state)
	}
}

// Finish runs the aggregation step on the totals, and the steps after it.
func (r *Runner) Finish(out func(any) error) error {
	if r.p.agg == nil {
		return nil
	}
	e := r.env.bind(aggVar, r.state.results())
	return r.p.agg.stage(e, nil, func(v any) error {
		if r.p.post == nil {
			return out(v)
		}
		return r.p.post(r.env, v, out)
	})
}

// IsBreak reports an internal early stop; it never reaches the caller.
func IsBreak(err error) bool {
	_, ok := err.(*breakErr)
	return ok
}

func flattenPipe(n node) []node {
	if p, ok := n.(nPipe); ok {
		return append(flattenPipe(p.left), flattenPipe(p.right)...)
	}
	return []node{n}
}

func joinPipe(ns []node) node {
	if len(ns) == 0 {
		return nIdentity{}
	}
	n := ns[len(ns)-1]
	for i := len(ns) - 2; i >= 0; i-- {
		n = nPipe{ns[i], n}
	}
	return n
}

// containsAgg reports whether an aggregation call appears in n.
func containsAgg(n node) bool {
	found := false
	walkNode(n, func(m node) {
		if c, ok := m.(nCall); ok && isAggregation(c.name, len(c.args)) {
			found = true
		}
	})
	return found
}

func walkNode(n node, f func(node)) {
	if n == nil {
		return
	}
	f(n)
	switch x := n.(type) {
	case nField:
		walkNode(x.target, f)
	case nIndex:
		walkNode(x.target, f)
		walkNode(x.index, f)
	case nSlice:
		walkNode(x.target, f)
		walkNode(x.from, f)
		walkNode(x.to, f)
	case nIterate:
		walkNode(x.target, f)
	case nTry:
		walkNode(x.body, f)
		walkNode(x.catch, f)
	case nPipe:
		walkNode(x.left, f)
		walkNode(x.right, f)
	case nComma:
		walkNode(x.left, f)
		walkNode(x.right, f)
	case nAlt:
		walkNode(x.left, f)
		walkNode(x.right, f)
	case nAnd:
		walkNode(x.left, f)
		walkNode(x.right, f)
	case nOr:
		walkNode(x.left, f)
		walkNode(x.right, f)
	case nBinop:
		walkNode(x.left, f)
		walkNode(x.right, f)
	case nNeg:
		walkNode(x.x, f)
	case nString:
		for _, p := range x.parts {
			walkNode(p, f)
		}
	case nArray:
		walkNode(x.body, f)
	case nObject:
		for _, e := range x.entries {
			walkNode(e.key, f)
			walkNode(e.value, f)
		}
	case nIf:
		walkNode(x.cond, f)
		walkNode(x.then, f)
		walkNode(x.els, f)
	case nReduce:
		walkNode(x.source, f)
		walkNode(x.init, f)
		walkNode(x.update, f)
	case nForeach:
		walkNode(x.source, f)
		walkNode(x.init, f)
		walkNode(x.update, f)
		walkNode(x.extract, f)
	case nBind:
		walkNode(x.source, f)
		walkNode(x.body, f)
	case nCall:
		for _, a := range x.args {
			walkNode(a, f)
		}
	case nAssign:
		walkNode(x.lhs, f)
		walkNode(x.rhs, f)
	}
}

// ErrorMessage formats a runtime error the way jq prints it, after
// "jq: error (at <where>)".
func ErrorMessage(err error) (msg string, notString bool) {
	if re, ok := err.(*RuntimeError); ok {
		if _, isStr := re.Value.(string); !isStr {
			return string(jv.Encode(nil, re.Value, jv.Format{Compact: true})), true
		}
		return re.Value.(string), false
	}
	return err.Error(), false
}

var _ = fmt.Sprintf
