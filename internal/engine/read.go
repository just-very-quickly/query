// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"runtime/debug"
	"unicode/utf8"

	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"

	"github.com/just-very-quickly/query/internal/jv"
)

func isValidUTF8(b []byte) bool { return utf8.Valid(b) }

type readCloser struct {
	io.Reader
	close func() error
}

func (r readCloser) Close() error { return r.close() }

// openInput opens an input and undoes gzip or zstd compression, found by
// the first bytes rather than the file name.
func openInput(in Input) (io.ReadCloser, error) {
	f, err := in.Open()
	if err != nil {
		return nil, err
	}
	br := bufio.NewReaderSize(f, 256<<10)
	magic, _ := br.Peek(4)
	switch {
	case len(magic) >= 2 && magic[0] == 0x1f && magic[1] == 0x8b:
		gz, err := gzip.NewReader(br)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("%s: %v", in.Name, err)
		}
		return readCloser{gz, func() error { gz.Close(); return f.Close() }}, nil
	case len(magic) == 4 && magic[0] == 0x28 && magic[1] == 0xb5 && magic[2] == 0x2f && magic[3] == 0xfd:
		zr, err := zstd.NewReader(br, zstd.WithDecoderMaxMemory(1<<34))
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("%s: %v", in.Name, err)
		}
		return readCloser{zr, func() error { zr.Close(); return f.Close() }}, nil
	}
	return readCloser{br, f.Close}, nil
}

// read cuts every input into chunks and sends them to the workers. It
// returns ExitUsage if an input couldn't be read.
func (r *run) read(inputs []Input, chunks chan<- *chunk) int {
	code := ExitOK
	seq := 0
	for k, in := range inputs {
		select {
		case <-r.stop:
			return code
		default:
		}
		// As jq: a byte order mark is skipped at the start of the input as
		// a whole (the first file), and kept with -R.
		r.skipBOM = k == 0 && !r.cfg.RawInput
		if in.Path != "" {
			if done, err := r.readMapped(in, chunks, &seq); done {
				if err == errFault {
					return ExitUsage
				}
				if err == errStopped {
					return code
				}
				continue
			}
		}
		rc, err := openInput(in)
		if err != nil {
			code = ExitUsage
			if r.sendErr(chunks, &seq, err, false) != nil {
				return code
			}
			continue
		}
		err = r.readOne(in.Name, rc, chunks, &seq, in.Path == "")
		rc.Close()
		if err == errStopped {
			return code
		}
		if err != nil {
			code = ExitUsage
			if r.sendErr(chunks, &seq, err, false) != nil {
				return code
			}
		}
	}
	return code
}

// readMapped cuts a memory-mapped file into chunks without copying it.
// It reports done=false when the file can't be mapped or isn't JSON Lines
// (compressed, or values spanning lines), to be read normally instead.
func (r *run) readMapped(in Input, chunks chan<- *chunk, seq *int) (done bool, err error) {
	data, unmap, ok := mapFile(in.Path)
	if !ok {
		return false, nil
	}
	old := debug.SetPanicOnFault(true)
	defer debug.SetPanicOnFault(old)
	defer func() {
		if e := faulted(recover(), in.Name); e != nil {
			r.sendErr(chunks, seq, e, true)
			done, err = true, errFault
		}
	}()
	start := 0
	if r.skipBOM && hasBOM(data) {
		start = 3 // as jq, a byte order mark at the start is skipped
	}
	head := data[start:min(len(data), start+r.cfg.ChunkSize)]
	gz := len(head) >= 2 && head[0] == 0x1f && head[1] == 0x8b
	zst := len(head) >= 4 && head[0] == 0x28 && head[1] == 0xb5 && head[2] == 0x2f && head[3] == 0xfd
	if gz || zst || !r.cfg.RawInput && isDocument(head) {
		unmap()
		return false, nil
	}
	m := &mapping{unmap: unmap}
	defer m.seal()
	line := 1
	for pos := start; pos < len(data); {
		end := min(pos+r.cfg.ChunkSize, len(data))
		if end < len(data) {
			if nl := bytes.LastIndexByte(data[pos:end], '\n'); nl >= 0 {
				end = pos + nl + 1
			} else if nl := bytes.IndexByte(data[end:], '\n'); nl >= 0 {
				end += nl + 1 // one line longer than a chunk
			} else {
				end = len(data)
			}
		}
		m.add()
		c := &chunk{seq: *seq, file: in.Name, line: line, data: data[pos:end], mapping: m}
		*seq++
		line += bytes.Count(data[pos:end], []byte{'\n'})
		if err := r.send(chunks, c); err != nil {
			return true, err
		}
		pos = end
	}
	return true, nil
}

func (r *run) send(chunks chan<- *chunk, c *chunk) error {
	select {
	case r.tokens <- struct{}{}:
	case <-r.stop:
		r.release(c)
		return errStopped
	}
	chunks <- c
	return nil
}

// fill reads into b until it is full or the input ends.
// fill reads into b until it is full or the input ends. With stream set
// (standard input, which may be a pipe that a program writes to slowly),
// it also returns as soon as a read comes back short with a whole line in
// b, so lines are handled as they arrive instead of when 1 MiB has.
func fill(rd io.Reader, b []byte, stream bool) (int, bool, error) {
	n := 0
	for n < len(b) {
		k, err := rd.Read(b[n:])
		n += k
		if err == io.EOF {
			return n, true, nil
		}
		if err != nil {
			return n, false, err
		}
		if stream && k > 0 && n < len(b) && bytes.IndexByte(b[n-k:n], '\n') >= 0 {
			return n, false, nil
		}
	}
	return n, false, nil
}

func hasBOM(b []byte) bool { return len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF }

// sendErr reports a read error in its place in the output.
func (r *run) sendErr(chunks chan<- *chunk, seq *int, err error, fatal bool) error {
	c := &chunk{seq: *seq, readErr: err, fatal: fatal}
	*seq++
	return r.send(chunks, c)
}

func (r *run) readOne(name string, rd io.Reader, chunks chan<- *chunk, seq *int, stream bool) error {
	size := r.cfg.ChunkSize
	bp := r.getBuf(size)
	buf := *bp
	n := 0
	line := 1
	first := true
	document := false
	scanned := 0 // document mode: complete values end here
	for {
		k, eof, err := fill(rd, buf[n:], stream)
		n += k
		if err != nil {
			// A broken compressed stream, say: what came before the error
			// is still used, as jq does.
			if n > 0 && !document {
				c := &chunk{seq: *seq, file: name, line: line, data: buf[:n], buf: bp}
				*seq++
				if err := r.send(chunks, c); err != nil {
					return err
				}
			} else {
				r.putBuf(bp)
			}
			return fmt.Errorf("%s: %v", name, err)
		}
		if first {
			first = false
			if r.skipBOM && hasBOM(buf[:n]) {
				copy(buf, buf[3:n])
				n -= 3
			}
			if !r.cfg.RawInput {
				document = isDocument(buf[:n])
			}
		}
		var cut int // bytes that form the next chunk
		if document {
			end, perr := scanValues(buf[:n], scanned, eof)
			scanned = end
			if perr != nil && (!perr.EOF || eof) {
				ln := line + bytes.Count(buf[:perr.Off], []byte{'\n'})
				// Send what was complete before the error.
				if end > 0 {
					c := &chunk{seq: *seq, file: name, line: line, data: buf[:end], buf: bp, document: true}
					*seq++
					if err := r.send(chunks, c); err != nil {
						return err
					}
				} else {
					r.putBuf(bp)
				}
				return fmt.Errorf("%s:%d: %s (input that isn't one JSON value per line stops at its first error)", name, ln, perr.Msg)
			}
			cut = end
			if eof {
				cut = n
			}
		} else {
			cut = n
			if !eof {
				cut = bytes.LastIndexByte(buf[:n], '\n') + 1
			}
		}
		if cut == 0 && !eof {
			// One line (or value) longer than the buffer: grow it.
			nb := r.getBuf(2 * len(buf))
			copy(*nb, buf[:n])
			r.putBuf(bp)
			bp, buf = nb, *nb
			continue
		}
		if cut > 0 {
			c := &chunk{seq: *seq, file: name, line: line, data: buf[:cut], buf: bp, document: document}
			*seq++
			line += bytes.Count(buf[:cut], []byte{'\n'})
			rest := n - cut
			nb := r.getBuf(max(size, rest*2))
			copy(*nb, buf[cut:n])
			if err := r.send(chunks, c); err != nil {
				r.putBuf(nb)
				return err
			}
			bp, buf, n = nb, *nb, rest
			scanned = 0
		}
		if eof {
			r.putBuf(bp)
			return nil
		}
	}
}

// isDocument reports whether the input starts with a value that spans
// lines, so it can't be cut at newlines.
// It looks at every value in b (the first chunk), so a file like jq's
// output, where the first values fit on a line ({} or a number) and later
// ones don't, is still read as a document. Lines that aren't valid JSON
// don't count either way; a value cut off at the end of b counts if its
// first line is a lone { or [.
func isDocument(b []byte) bool {
	i := jv.SkipSpace(b, 0)
	for i < len(b) {
		end, err := jv.Valid(b, i)
		if err == nil {
			if bytes.IndexByte(b[i:end], '\n') >= 0 {
				return true
			}
			i = jv.SkipSpace(b, end)
			continue
		}
		if err.(*jv.SyntaxError).EOF {
			// Cut off by the end of the chunk: pretty-printed JSON if the
			// value's first line is a lone bracket, as jq and most tools
			// write it; otherwise a broken line of JSON Lines.
			first := b[i:]
			if nl := bytes.IndexByte(first, '\n'); nl >= 0 {
				first = first[:nl]
				t := string(bytes.TrimSpace(first))
				return t == "{" || t == "["
			}
			return false
		}
		// a bad line: go on from the next one
		nl := bytes.IndexByte(b[i:], '\n')
		if nl < 0 {
			return false
		}
		i = jv.SkipSpace(b, i+nl+1)
	}
	return false
}

// scanValues checks the values in b from offset from and returns where the
// last complete one ends, with the error that stopped it (an EOF error
// means the last value is cut off and more input may complete it). Unless
// the input has ended, a number running to the end of b may go on.
func scanValues(b []byte, from int, eof bool) (int, *jv.SyntaxError) {
	i := jv.SkipSpace(b, from)
	end := from
	for i < len(b) {
		e, err := jv.Valid(b, i)
		if err != nil {
			se := err.(*jv.SyntaxError)
			return end, se
		}
		if e == len(b) && !eof && (b[i] == '-' || b[i] >= '0' && b[i] <= '9') {
			return end, &jv.SyntaxError{Off: len(b), Msg: "Unfinished JSON term", EOF: true}
		}
		end = e
		i = jv.SkipSpace(b, e)
	}
	if i == len(b) {
		end = len(b)
	}
	return end, nil
}
