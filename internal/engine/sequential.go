// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"runtime/debug"

	"github.com/just-very-quickly/query/internal/jv"
	"github.com/just-very-quickly/query/internal/query"
)

// A filter that calls input or inputs reads the input itself, so it can't
// be split between workers. Such filters run in one goroutine, in order,
// as in jq; the reading and cutting into chunks still happen alongside.

// valueIter yields input values one at a time, in order.
type valueIter struct {
	r      *run
	chunks <-chan *chunk
	cur    *chunk
	data   []byte // what is left of the current chunk
	line   int    // line of data's first byte
	pend   []byte // the rest of the current line (line mode)
	where  string // location of the last value returned
	file   string
	bad    int
	first  string
	fatal  error
	runner *query.Runner
}

func (it *valueIter) release() {
	if it.cur != nil {
		it.r.release(it.cur)
		<-it.r.tokens
		it.cur = nil
	}
}

// Next returns the next input value, decoded (values may be kept).
func (it *valueIter) Next() (v any, ok bool, err error) {
	old := debug.SetPanicOnFault(true)
	defer debug.SetPanicOnFault(old)
	defer func() {
		if e := faulted(recover(), it.file); e != nil {
			it.fatal = e
			v, ok, err = nil, false, nil
		}
	}()
	for {
		if it.fatal != nil {
			return nil, false, nil
		}
		if len(it.pend) > 0 {
			l := it.pend
			i := jv.SkipSpace(l, 0)
			if i == len(l) {
				it.pend = nil
				it.line++
				continue
			}
			end, err := jv.Valid(l, i)
			if err != nil {
				col := err.(*jv.SyntaxError).Off + 1
				msg := fmt.Sprintf("%s:%d: %s at column %d", it.file, it.line, err.Error(), col)
				if it.r.cfg.Strict {
					it.fatal = fmt.Errorf("%s (stopped, --strict)", msg)
					return nil, false, nil
				}
				it.bad++
				if it.bad == 1 {
					it.first = msg
				}
				it.pend = nil
				it.line++
				continue
			}
			v, _ := jv.DecodeValid(l, i)
			it.where = fmt.Sprintf("%s:%d", it.file, it.line)
			it.pend = l[end:]
			if jv.SkipSpace(it.pend, 0) == len(it.pend) {
				it.pend = nil
				it.line++
			}
			return v, true, nil
		}
		if len(it.data) == 0 {
			it.release()
			c, ok := <-it.chunks
			if !ok {
				return nil, false, nil
			}
			it.cur, it.data, it.line, it.file = c, c.data, c.line, c.file
			it.runner.SetFile(c.file)
			continue
		}
		if it.cur.document {
			i := jv.SkipSpace(it.data, 0)
			if i == len(it.data) {
				it.data = nil
				continue
			}
			end, _ := jv.Valid(it.data, i)
			v, _ := jv.DecodeValid(it.data, i)
			it.where = fmt.Sprintf("%s:%d", it.file, it.line+bytes.Count(it.data[:i], []byte{'\n'}))
			it.line += bytes.Count(it.data[:end], []byte{'\n'})
			it.data = it.data[end:]
			return v, true, nil
		}
		nl := bytes.IndexByte(it.data, '\n')
		var l []byte
		if nl < 0 {
			l, it.data = it.data, nil
		} else {
			l, it.data = it.data[:nl], it.data[nl+1:]
		}
		if it.r.cfg.RawInput {
			it.where = fmt.Sprintf("%s:%d", it.file, it.line)
			it.line++
			return validString(l), true, nil
		}
		if n := len(l); n > 0 && l[n-1] == '\r' {
			l = l[:n-1]
		}
		if len(l) == 0 {
			l = []byte{' '} // a blank line still counts as a line
		}
		it.pend = l
	}
}

func (r *run) sequential(out *bufio.Writer, inputs []Input) int {
	cfg := r.cfg
	chunks := make(chan *chunk, 4)
	r.tokens = make(chan struct{}, 8)
	readErr := make(chan int, 1)
	go func() {
		readErr <- r.read(inputs, chunks)
		close(chunks)
	}()
	it := &valueIter{r: r, chunks: chunks}
	debug := &bytes.Buffer{}
	runner := cfg.Program.NewRunner(debug)
	runner.SetInputs(it)
	it.runner = runner
	outputs, lastTrue, lastErr, anyErr := 0, false, false, false
	var buf []byte
	emit := func(v any) error {
		buf = r.encode(buf[:0], v)
		out.Write(buf)
		outputs++
		lastTrue = jv.Truthy(v)
		return nil
	}
	flushDebug := func() {
		if debug.Len() > 0 {
			out.Flush()
			cfg.Stderr.Write(debug.Bytes())
			debug.Reset()
		}
	}
	report := func(err error) bool {
		flushDebug()
		if h, ok := err.(*query.HaltError); ok {
			out.Flush()
			io.WriteString(cfg.Stderr, h.Message())
			r.halt()
			r.drain(chunks, it)
			lastErr = false
			outputs = -1 - h.Code // carries the code out
			return true
		}
		out.Flush()
		r.printError(err, it.where)
		lastErr, anyErr = true, true
		return false
	}
	halted := false
	if cfg.NullInput {
		lastErr = false
		if err := runner.Run(nil, emit); err != nil {
			halted = report(err)
		}
	} else {
		for !halted {
			v, ok, _ := it.Next()
			if !ok {
				break
			}
			lastErr = false
			if err := runner.Run(v, emit); err != nil {
				if halted = report(err); halted {
					break
				}
			}
		}
	}
	if !halted {
		if err := runner.Finish(emit); err != nil {
			halted = report(err)
		}
	}
	flushDebug()
	r.drain(chunks, it)
	code := <-readErr
	if outputs < 0 {
		return -1 - outputs
	}
	if it.fatal != nil {
		out.Flush()
		r.fatal(it.fatal)
		return ExitUsage
	}
	if it.bad > 0 {
		out.Flush()
		word := "lines"
		if it.bad == 1 {
			word = "line"
		}
		fmt.Fprintf(cfg.Stderr, "%s: skipped %d %s that %s valid JSON (first at %s)\n", r.name, it.bad, word, isOrAre(it.bad), it.first)
	}
	if code != ExitOK {
		return code
	}
	final := ExitOK
	if lastErr || cfg.Program.HasAggregation() && anyErr {
		final = ExitRuntime
	}
	return r.exitCode(final, outputs, lastTrue)
}

// drain stops the reader and releases what it had sent.
func (r *run) drain(chunks <-chan *chunk, it *valueIter) {
	r.halt()
	it.release()
	for c := range chunks {
		r.release(c)
		<-r.tokens
	}
}
