// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

// Package engine runs a filter over JSON input in parallel: the input is
// cut into chunks at line (or value) boundaries, workers run the filter on
// chunks at the same time, and the output is written in input order.
package engine

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/just-very-quickly/query/internal/jv"
	"github.com/just-very-quickly/query/internal/query"
)

// Exit codes, as jq's.
const (
	ExitOK       = 0
	ExitFalse    = 1 // -e: the last output was false or null
	ExitUsage    = 2 // bad arguments, unreadable files, invalid input with --strict
	ExitCompile  = 3 // the filter doesn't compile
	ExitNoOutput = 4 // -e: no output at all
	ExitRuntime  = 5 // a filter error on some input
)

// Config says how to run.
type Config struct {
	Program    *query.Program
	Format     jv.Format
	RawOutput  bool // -r: strings without quotes
	Join       bool // -j: -r without newlines
	NullInput  bool // -n
	Slurp      bool // -s
	RawInput   bool // -R: each line is a string
	ExitStatus bool // -e
	Strict     bool // stop at the first invalid line instead of skipping it
	Workers    int  // 0 means one per CPU
	ChunkSize  int  // bytes per chunk; 0 means the default
	Stdout     io.Writer
	Stderr     io.Writer
	Name       string // program name for messages
}

// DefaultChunkSize is how much input one worker takes at a time.
const DefaultChunkSize = 1 << 20

// Input is one file (or standard input) to read.
type Input struct {
	Name string // as shown in messages; "<stdin>" for standard input
	Path string // the file's path, to map it into memory; "" for stdin
	Open func() (io.ReadCloser, error)
}

// chunk is a piece of input holding whole lines (or whole values).
type chunk struct {
	seq      int
	file     string
	line     int // line number of the first byte
	data     []byte
	buf      *[]byte  // the pooled buffer data lives in, returned after use
	mapping  *mapping // or the mapped file it points into
	document bool     // values may span lines; already checked by the reader
}

// mapping is a file mapped into memory, unmapped once the reader is done
// with it and every chunk cut from it has been written.
type mapping struct {
	mu     sync.Mutex
	refs   int
	sealed bool
	unmap  func()
}

func (m *mapping) add() {
	m.mu.Lock()
	m.refs++
	m.mu.Unlock()
}

func (m *mapping) done() {
	m.mu.Lock()
	m.refs--
	last := m.refs == 0 && m.sealed
	m.mu.Unlock()
	if last {
		m.unmap()
	}
}

func (m *mapping) seal() {
	m.mu.Lock()
	m.sealed = true
	last := m.refs == 0
	m.mu.Unlock()
	if last {
		m.unmap()
	}
}

// fatal reports an error that stopped the run.
func (r *run) fatal(err error) {
	if _, ok := err.(*faultError); ok {
		r.fault.Do(func() { fmt.Fprintf(r.cfg.Stderr, "%s: error: %v\n", r.name, err) })
		return
	}
	fmt.Fprintf(r.cfg.Stderr, "%s: error: %v\n", r.name, err)
}

// faulted turns the panic of a memory fault into an error. Reading a
// mapped file faults when the file gets shorter while it is read (a log
// rotated by truncating it, say). Other panics carry on.
func faulted(p any, file string) error {
	if p == nil {
		return nil
	}
	if _, ok := p.(interface{ Addr() uintptr }); ok {
		return &faultError{file}
	}
	panic(p)
}

type faultError struct{ file string }

func (e *faultError) Error() string {
	return e.file + ": the file got shorter while it was being read"
}

// release gives back what a chunk holds once it has been written.
func (r *run) release(c *chunk) {
	if c.buf != nil {
		r.putBuf(c.buf)
	}
	if c.mapping != nil {
		dropPages(c.data) // keeps memory use flat however large the file
		c.mapping.done()
	}
}

// result is what a worker made of a chunk.
type result struct {
	seq      int
	out      []byte
	errs     []byte
	outputs  int
	lastTrue bool // -e: whether the last output was truthy
	runtime  int  // filter errors
	inputs   int  // values the filter ran on
	lastErr  bool // whether the last of them failed (jq's exit status follows it)
	badLines int
	firstBad string
	values   []any // -s: the decoded values
	chunk    *chunk
	fatal    error            // --strict: stop here
	halt     *query.HaltError // halt or halt_error: stop here
}

type run struct {
	cfg     Config
	name    string
	workers int
	pool    sync.Pool
	outPool sync.Pool // output buffers
	tokens  chan struct{}
	stop    chan struct{}
	stopped sync.Once
	fault   sync.Once // a truncated file is reported once
}

func (r *run) halt() { r.stopped.Do(func() { close(r.stop) }) }

func (r *run) getBuf(n int) *[]byte {
	if p, ok := r.pool.Get().(*[]byte); ok && cap(*p) >= n {
		*p = (*p)[:n]
		return p
	}
	b := make([]byte, n)
	return &b
}

func (r *run) getOut() []byte {
	if p, ok := r.outPool.Get().(*[]byte); ok {
		return (*p)[:0]
	}
	return make([]byte, 0, r.cfg.ChunkSize+r.cfg.ChunkSize/4)
}

func (r *run) putOut(b []byte) {
	if cap(b) <= 8*r.cfg.ChunkSize {
		r.outPool.Put(&b)
	}
}

func (r *run) putBuf(p *[]byte) {
	if cap(*p) <= 4*r.cfg.ChunkSize {
		r.pool.Put(p)
	}
}

// Run runs the filter over the inputs and returns the exit code.
func Run(cfg Config, inputs []Input) int {
	if cfg.ChunkSize <= 0 {
		cfg.ChunkSize = DefaultChunkSize
	}
	if cfg.Workers <= 0 {
		cfg.Workers = runtime.GOMAXPROCS(0)
	}
	if cfg.Name == "" {
		cfg.Name = "jvqq"
	}
	if cfg.Join {
		cfg.RawOutput = true
	}
	r := &run{cfg: cfg, name: cfg.Name, workers: cfg.Workers, stop: make(chan struct{})}
	out := bufio.NewWriterSize(cfg.Stdout, 256<<10)
	defer out.Flush()
	if cfg.Program.UsesInputs() && !cfg.Slurp {
		return r.sequential(out, inputs)
	}
	if cfg.NullInput {
		return r.runOnce(out, nil)
	}
	if cfg.Slurp && cfg.RawInput {
		var all []byte
		code := ExitOK
		for _, in := range inputs {
			rc, err := openInput(in)
			if err != nil {
				fmt.Fprintf(cfg.Stderr, "%s: error: %v\n", r.name, err)
				code = ExitUsage
				continue
			}
			b, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				fmt.Fprintf(cfg.Stderr, "%s: error: %s: %v\n", r.name, in.Name, err)
				code = ExitUsage
			}
			all = append(all, b...)
		}
		if c := r.runOnce(out, string(all)); c != ExitOK {
			return c
		}
		return code
	}
	return r.parallel(out, inputs)
}

// runOnce runs the program on one value (for -n, -s and -Rs).
func (r *run) runOnce(out *bufio.Writer, in any) int {
	runner := r.cfg.Program.NewRunner(nil)
	var buf []byte
	outputs := 0
	lastTrue := false
	emit := func(v any) error {
		buf = r.encode(buf[:0], v)
		out.Write(buf)
		outputs++
		lastTrue = jv.Truthy(v)
		return nil
	}
	code := ExitOK
	if err := runner.Run(in, emit); err != nil {
		if h, ok := err.(*query.HaltError); ok {
			out.Flush()
			io.WriteString(r.cfg.Stderr, h.Message())
			return h.Code
		}
		r.printError(err, "")
		code = ExitRuntime
	}
	if err := runner.Finish(emit); err != nil {
		r.printError(err, "")
		code = ExitRuntime
	}
	return r.exitCode(code, outputs, lastTrue)
}

func (r *run) exitCode(code, outputs int, lastTrue bool) int {
	if code != ExitOK || !r.cfg.ExitStatus {
		return code
	}
	if outputs == 0 {
		return ExitNoOutput
	}
	if !lastTrue {
		return ExitFalse
	}
	return ExitOK
}

func (r *run) printError(err error, where string) {
	msg, notString := query.ErrorMessage(err)
	if where == "" {
		where = "<unknown>"
	}
	if notString {
		fmt.Fprintf(r.cfg.Stderr, "%s: error (at %s) (not a string): %s\n", r.name, where, msg)
		return
	}
	fmt.Fprintf(r.cfg.Stderr, "%s: error (at %s): %s\n", r.name, where, msg)
}

// encode writes one output value the way jq does.
func (r *run) encode(buf []byte, v any) []byte {
	if r.cfg.RawOutput && jv.KindOf(v) == jv.KString {
		if raw, ok := jv.RawOf(v); ok {
			if body, ok := jv.RawPlainString(raw); ok {
				buf = append(buf, body...)
			} else {
				buf = append(buf, jv.RawString(raw)...)
			}
		} else {
			buf = append(buf, v.(string)...)
		}
	} else {
		buf = jv.Encode(buf, v, r.cfg.Format)
	}
	if !r.cfg.Join {
		buf = append(buf, '\n')
	}
	return buf
}

func (r *run) parallel(out *bufio.Writer, inputs []Input) int {
	cfg := r.cfg
	chunks := make(chan *chunk, r.workers)
	results := make(chan *result, r.workers)
	// At most this many chunks are in flight: memory stays bounded however
	// large the input is.
	r.tokens = make(chan struct{}, 2*r.workers+2)

	agg := cfg.Program.HasAggregation()
	runners := make([]*query.Runner, r.workers)
	debugs := make([]*bytes.Buffer, r.workers)
	var wg sync.WaitGroup
	for w := 0; w < r.workers; w++ {
		debugs[w] = &bytes.Buffer{}
		runners[w] = cfg.Program.NewRunner(debugs[w])
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			debug.SetPanicOnFault(true) // a mapped file cut short: see faulted
			for c := range chunks {
				results <- r.work(c, runners[w], debugs[w])
			}
		}(w)
	}

	readErr := make(chan int, 1)
	go func() {
		readErr <- r.read(inputs, chunks)
		close(chunks)
		wg.Wait()
		close(results)
	}()

	// Write results in input order.
	pending := map[int]*result{}
	next := 0
	outputs, runtimeErrs, badLines := 0, 0, 0
	lastErr := false
	firstBad := ""
	lastTrue := false
	var slurped []any
	var fatal error
	var halted *query.HaltError
	for res := range results {
		pending[res.seq] = res
		for {
			p, ok := pending[next]
			if !ok {
				break
			}
			delete(pending, next)
			next++
			if fatal == nil && halted == nil {
				out.Write(p.out)
				if len(p.errs) > 0 {
					out.Flush()
					cfg.Stderr.Write(p.errs)
				}
				if p.outputs > 0 {
					outputs += p.outputs
					lastTrue = p.lastTrue
				}
				runtimeErrs += p.runtime
				if p.inputs > 0 {
					lastErr = p.lastErr
				}
				if p.badLines > 0 && badLines == 0 {
					firstBad = p.firstBad
				}
				badLines += p.badLines
				slurped = append(slurped, p.values...)
				if p.fatal != nil {
					fatal = p.fatal
					r.halt()
				}
				if p.halt != nil {
					halted = p.halt
					r.halt()
				}
			}
			r.release(p.chunk)
			r.putOut(p.out)
			<-r.tokens
		}
	}
	code := <-readErr
	if halted != nil {
		out.Flush()
		io.WriteString(cfg.Stderr, halted.Message())
		return halted.Code
	}
	if fatal != nil {
		out.Flush()
		r.fatal(fatal)
		return ExitUsage
	}
	if badLines > 0 {
		out.Flush()
		word := "lines"
		if badLines == 1 {
			word = "line"
		}
		fmt.Fprintf(cfg.Stderr, "%s: skipped %d %s that %s valid JSON (first at %s)\n", r.name, badLines, word, isOrAre(badLines), firstBad)
	}

	// As in jq, the exit status follows the last input; with an
	// aggregation, any failed input counts.
	finalCode := ExitOK
	if lastErr || agg && runtimeErrs > 0 {
		finalCode = ExitRuntime
	}
	emit := func(v any) error {
		out.Write(r.encode(nil, v))
		outputs++
		lastTrue = jv.Truthy(v)
		return nil
	}
	switch {
	case cfg.Slurp:
		if slurped == nil {
			slurped = []any{}
		}
		finalCode = r.mergeCode(finalCode, r.runOnce(out, slurped))
		if code != ExitOK {
			return code
		}
		return finalCode
	case agg:
		total := runners[0]
		for _, o := range runners[1:] {
			total.Merge(o)
		}
		if err := total.Finish(emit); err != nil {
			out.Flush()
			r.printError(err, "end of input")
			finalCode = ExitRuntime
		}
	}
	for _, d := range debugs {
		if d.Len() > 0 {
			out.Flush()
			cfg.Stderr.Write(d.Bytes())
		}
	}
	if code != ExitOK {
		return code
	}
	return r.exitCode(finalCode, outputs, lastTrue)
}

func isOrAre(n int) string {
	if n == 1 {
		return "isn't"
	}
	return "aren't"
}

func (r *run) mergeCode(a, b int) int {
	if a != ExitOK {
		return a
	}
	return b
}

// work runs the filter on every value in a chunk.
func (r *run) work(c *chunk, runner *query.Runner, debug *bytes.Buffer) (res *result) {
	res = &result{seq: c.seq, chunk: c}
	defer func() {
		if err := faulted(recover(), c.file); err != nil {
			res.fatal = err
		}
	}()
	cfg := r.cfg
	out := r.getOut()
	var errs bytes.Buffer
	line := c.line
	where := func(ln int) string { return fmt.Sprintf("%s:%d", c.file, ln) }
	emit := func(v any) error {
		out = r.encode(out, v)
		res.outputs++
		res.lastTrue = jv.Truthy(v)
		return nil
	}
	process := func(v any, ln int) {
		if cfg.Slurp {
			res.values = append(res.values, decodeDetached(v))
			return
		}
		res.inputs++
		res.lastErr = false
		if err := runner.Run(v, emit); err != nil {
			if query.IsBreak(err) {
				return
			}
			if h, ok := err.(*query.HaltError); ok {
				res.halt = h
				return
			}
			res.runtime++
			res.lastErr = true
			if debug.Len() > 0 {
				errs.Write(debug.Bytes())
				debug.Reset()
			}
			msg, notString := query.ErrorMessage(err)
			if notString {
				fmt.Fprintf(&errs, "%s: error (at %s) (not a string): %s\n", r.name, where(ln), msg)
			} else {
				fmt.Fprintf(&errs, "%s: error (at %s): %s\n", r.name, where(ln), msg)
			}
		}
		if debug.Len() > 0 {
			errs.Write(debug.Bytes())
			debug.Reset()
		}
	}
	runner.SetFile(c.file)
	data := c.data
	doc := &jv.Doc{}
	if c.document {
		// The reader checked these values; values may span lines.
		i := jv.SkipSpace(data, 0)
		for i < len(data) {
			end, _ := jv.ValidDoc(data, i, doc)
			ln := line + bytes.Count(data[:i], []byte{'\n'})
			process(doc, ln)
			if res.halt != nil {
				break
			}
			i = jv.SkipSpace(data, end)
		}
	} else {
		for len(data) > 0 {
			nl := bytes.IndexByte(data, '\n')
			var l []byte
			if nl < 0 {
				l, data = data, nil
			} else {
				l, data = data[:nl], data[nl+1:]
			}
			if cfg.RawInput {
				s := l
				process(validString(s), line)
				if res.halt != nil {
					break
				}
				line++
				continue
			}
			if n := len(l); n > 0 && l[n-1] == '\r' {
				l = l[:n-1]
			}
			i := jv.SkipSpace(l, 0)
			for i < len(l) {
				end, err := jv.ValidDoc(l, i, doc)
				if err != nil {
					col := err.(*jv.SyntaxError).Off + 1
					if cfg.Strict {
						res.fatal = fmt.Errorf("%s:%d: %s at column %d (stopped, --strict)", c.file, line, err.Error(), col)
						break
					}
					res.badLines++
					if res.badLines == 1 {
						res.firstBad = fmt.Sprintf("%s:%d: %s at column %d", c.file, line, err.Error(), col)
					}
					break
				}
				process(doc, line)
				if res.halt != nil {
					break
				}
				i = jv.SkipSpace(l, end)
			}
			if res.fatal != nil || res.halt != nil {
				break
			}
			line++
		}
	}
	res.out = out
	res.errs = errs.Bytes()
	return res
}

func validString(b []byte) string {
	s := string(b)
	if !isValidUTF8(b) {
		s = strings.ToValidUTF8(s, "�")
	}
	return s
}

// decodeDetached decodes a raw value fully (for -s, where values outlive
// their chunk).
func decodeDetached(v any) any {
	if raw, ok := jv.RawOf(v); ok {
		d, _ := jv.DecodeValid(raw, 0)
		return d
	}
	return v
}

var errStopped = errors.New("stopped")

// errFault stops reading a mapped file that got shorter.
var errFault = errors.New("fault")
