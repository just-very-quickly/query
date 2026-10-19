// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// These tests cover how input is read and split: they don't need jq.

// jvqq runs the command in this process and returns stdout, stderr and the
// exit code. stdin is the standard input ("" for none).
func jvqq(t *testing.T, stdin string, args ...string) (string, string, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(args, strings.NewReader(stdin), &out, &errOut)
	return out.String(), errOut.String(), code
}

// modes are the ways input gets split: they must all give the same result.
var modes = [][]string{
	{"--workers", "1"},
	{"--workers", "4", "--chunk-size", "16"},
	{"--workers", "3", "--chunk-size", "1000"},
	{},
}

func write(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// check runs args over the input as a file and as standard input, in every
// mode, and compares with the expected output and exit code.
func check(t *testing.T, input, wantOut, wantErr string, wantCode int, args ...string) {
	t.Helper()
	path := write(t, "in.jsonl", []byte(input))
	for _, m := range modes {
		for _, viaFile := range []bool{true, false} {
			a := append(append([]string{}, m...), args...)
			stdin := input
			if viaFile {
				a = append(a, path)
				stdin = ""
			}
			out, errOut, code := jvqq(t, stdin, a...)
			if viaFile {
				errOut = strings.ReplaceAll(errOut, path, "<stdin>")
			}
			if out != wantOut || errOut != wantErr || code != wantCode {
				t.Errorf("jvqq %q (file %v):\nstdout %q\nwant   %q\nstderr %q\nwant   %q\nexit %d, want %d",
					a, viaFile, out, wantOut, errOut, wantErr, code, wantCode)
			}
		}
	}
}

func TestOrderKept(t *testing.T) {
	var in, want strings.Builder
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 20000; i++ {
		pad := strings.Repeat("x", r.Intn(300)) // lines of uneven length
		fmt.Fprintf(&in, "{\"id\":%d,\"pad\":%q}\n", i, pad)
		fmt.Fprintf(&want, "%d\n", i)
	}
	check(t, in.String(), want.String(), "", 0, "-c", ".id")
}

func TestLineEndings(t *testing.T) {
	// CRLF, blank lines, spaces, no newline at the end
	in := "{\"a\":1}\r\n\r\n   \n{\"a\":2}  \r\n\t{\"a\":3}"
	check(t, in, "1\n2\n3\n", "", 0, ".a")
	check(t, "", "", "", 0, ".")
	check(t, "\n\n", "", "", 0, ".")
}

func TestSeveralValuesOnALine(t *testing.T) {
	check(t, "1 2 [3]\n{\"a\":4}{\"a\":5}\n", "1\n2\n[3]\n{\"a\":4}\n{\"a\":5}\n", "", 0, "-c", ".")
}

func TestLongLines(t *testing.T) {
	// lines far longer than the chunk size
	big := strings.Repeat("y", 5000)
	in := fmt.Sprintf("{\"s\":%q}\n{\"s\":\"short\"}\n{\"s\":%q}\n", big, big)
	check(t, in, "5000\n5\n5000\n", "", 0, ".s | length")
}

func TestMalformedLines(t *testing.T) {
	in := "{\"a\":1}\n{\"a\":\n{\"a\":2}\nnot json\n{\"a\":3}\n"
	check(t, in, "1\n2\n3\n",
		"jvqq: skipped 2 lines that aren't valid JSON (first at <stdin>:2: Unfinished JSON term at column 6)\n",
		0, ".a")
	check(t, "{\"a\":1}\n[1,}\n", "1\n",
		"jvqq: skipped 1 line that isn't valid JSON (first at <stdin>:2: Unmatched '}' at column 4)\n",
		0, ".a")
}

func TestStrict(t *testing.T) {
	in := "{\"a\":1}\n{\"a\":2}\n{\"a\":\n{\"a\":3}\n"
	path := write(t, "in.jsonl", []byte(in))
	for _, m := range modes {
		a := append(append([]string{}, m...), "--strict", ".a", path)
		out, errOut, code := jvqq(t, "", a...)
		// everything before the bad line is written, nothing after
		if out != "1\n2\n" || code != 2 || !strings.Contains(errOut, ":3: Unfinished JSON term") || !strings.Contains(errOut, "--strict") {
			t.Errorf("jvqq %q: stdout %q, stderr %q, exit %d", a, out, errOut, code)
		}
	}
}

func TestDocuments(t *testing.T) {
	// pretty-printed values spanning lines, as jq writes them
	in := "{\n  \"a\": 1,\n  \"b\": [\n    1,\n    2\n  ]\n}\n{\n  \"a\": 2\n}\n[\n]\n"
	check(t, in, "{\"a\":1,\"b\":[1,2]}\n{\"a\":2}\n[]\n", "", 0, "-c", ".")
	// a large document mode input: lines don't each hold a value
	var b strings.Builder
	var want strings.Builder
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(&b, "{\n  \"i\": %d,\n  \"t\": \"%s\"\n}\n", i, strings.Repeat("z", i%50))
		fmt.Fprintf(&want, "%d\n", i)
	}
	check(t, b.String(), want.String(), "", 0, ".i")
}

func TestCompressed(t *testing.T) {
	var in, want strings.Builder
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&in, "{\"i\":%d}\n", i)
		fmt.Fprintf(&want, "%d\n", i)
	}
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	w.Write([]byte(in.String()))
	w.Close()
	var zs bytes.Buffer
	z, _ := zstd.NewWriter(&zs)
	z.Write([]byte(in.String()))
	z.Close()
	for name, data := range map[string][]byte{"in.jsonl.gz": gz.Bytes(), "in.jsonl.zst": zs.Bytes()} {
		p := write(t, name, data)
		for _, m := range modes {
			a := append(append([]string{}, m...), ".i", p)
			out, errOut, code := jvqq(t, "", a...)
			if out != want.String() || errOut != "" || code != 0 {
				t.Errorf("jvqq %q: %d bytes out, stderr %q, exit %d", a, len(out), errOut, code)
			}
			// and through standard input
			out, _, code = jvqq(t, string(data), append(append([]string{}, m...), ".i")...)
			if out != want.String() || code != 0 {
				t.Errorf("%s on stdin, mode %q: %d bytes out, exit %d", name, m, len(out), code)
			}
		}
	}
	// a broken gzip file is an input error
	p := write(t, "bad.gz", gz.Bytes()[:len(gz.Bytes())/2])
	_, errOut, code := jvqq(t, "", ".i", p)
	if code != 2 || errOut == "" {
		t.Errorf("truncated gzip: exit %d, stderr %q", code, errOut)
	}
}

func TestSeveralFiles(t *testing.T) {
	a := write(t, "a.jsonl", []byte("{\"x\":1}\n{\"x\":2}"))
	b := write(t, "b.jsonl", []byte("{\"x\":3}\n"))
	missing := filepath.Join(t.TempDir(), "missing.jsonl")
	out, errOut, code := jvqq(t, "", "-c", "[.x, input_filename]", a, missing, b)
	want := fmt.Sprintf("[1,%q]\n[2,%q]\n[3,%q]\n", a, a, b)
	if out != want || code != 2 || !strings.Contains(errOut, "missing.jsonl") {
		t.Errorf("stdout %q\nwant   %q\nstderr %q, exit %d", out, want, errOut, code)
	}
	out, _, code = jvqq(t, "", "count", a, b)
	if out != "3\n" || code != 0 {
		t.Errorf("count over two files: %q, exit %d", out, code)
	}
}

func TestAggregations(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	services := []string{"api", "db", "web", "auth"}
	var in strings.Builder
	counts := map[string]int{}
	sums := map[string]float64{}
	n := 0
	total := 0.0
	lo, hi := 1e18, -1e18
	for i := 0; i < 10000; i++ {
		s := services[r.Intn(len(services))]
		ms := r.Intn(100000)
		if i%97 == 0 {
			fmt.Fprintf(&in, "{\"service\":%q,\"ms\":null}\n", s)
		} else {
			fmt.Fprintf(&in, "{\"service\":%q,\"ms\":%d}\n", s, ms)
			sums[s] += float64(ms)
			total += float64(ms)
			n++
			lo = min(lo, float64(ms))
			hi = max(hi, float64(ms))
		}
		counts[s]++
	}
	check(t, in.String(), "10000\n", "", 0, "count")
	check(t, in.String(), fmt.Sprintf("%.0f\n", total), "", 0, "sum(.ms)")
	check(t, in.String(), fmt.Sprintf("%.0f\n%.0f\n", lo, hi), "", 0, "min(.ms), max(.ms)")
	check(t, in.String(), "true\n", "", 0, fmt.Sprintf("avg(.ms) - %v | fabs < 1e-9", total/float64(n)))

	// count_by orders by count, then by key; the result is the same however
	// the input was split
	want, _, code := jvqq(t, in.String(), "--workers", "1", "-c", "count_by(.service)")
	if code != 0 {
		t.Fatalf("count_by: exit %d", code)
	}
	for _, s := range services {
		if !strings.Contains(want, fmt.Sprintf("%q:%d", s, counts[s])) {
			t.Errorf("count_by: %s should be %d in %s", s, counts[s], want)
		}
	}
	check(t, in.String(), want, "", 0, "-c", "count_by(.service)")

	want, _, _ = jvqq(t, in.String(), "--workers", "1", "-c", "group_by(.service; {n: count, total: sum(.ms), mean: avg(.ms)})")
	for _, s := range services {
		if !strings.Contains(want, fmt.Sprintf("{\"key\":%q,\"n\":%d,\"total\":%.0f,", s, counts[s], sums[s])) {
			t.Errorf("group_by: wrong row for %s in %s", s, want)
		}
	}
	check(t, in.String(), want, "", 0, "-c", "group_by(.service; {n: count, total: sum(.ms), mean: avg(.ms)})")
	// steps before and after the aggregation
	check(t, in.String(), fmt.Sprintf("%d\n", counts["db"]*2), "", 0, `select(.service == "db") | count | . * 2`)
}

func TestExactSums(t *testing.T) {
	// the sum is the exact sum, rounded once: it doesn't depend on the
	// order of addition (adding in order would give 0 here)
	var in strings.Builder
	for i := 0; i < 1000; i++ {
		in.WriteString("{\"v\":1}\n{\"v\":1e100}\n{\"v\":-1e100}\n")
	}
	check(t, in.String(), "1000\n", "", 0, "sum(.v)")
}

func TestErrorsAndExitCodes(t *testing.T) {
	in := "{\"a\":1}\n[1]\n{\"a\":3}\n"
	check(t, in, "1\n3\n", "jvqq: error (at <stdin>:2): Cannot index array with string \"a\"\n", 0, ".a")
	check(t, "{\"a\":1}\n[1]\n", "1\n", "jvqq: error (at <stdin>:2): Cannot index array with string \"a\"\n", 5, ".a")
	// with an aggregation, any failed input counts
	check(t, in, "2\n", "jvqq: error (at <stdin>:2): Cannot index array with string \"a\"\n", 5, ".a | count")
	check(t, "1\n2\n", "1\n2\n", "", 0, "-e", ".")
	check(t, "1\nnull\n", "1\nnull\n", "", 1, "-e", ".")
	check(t, "1\n", "", "", 4, "-e", "empty")
	check(t, "1\n", "", "jvqq: error: unexpected end of filter\njvqq: 1 compile error\n", 3, ".a[")
}

func TestHalt(t *testing.T) {
	var in strings.Builder
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&in, "%d\n", i)
	}
	// output stops at the halting input, in every mode
	var want strings.Builder
	for i := 0; i < 1500; i++ {
		fmt.Fprintf(&want, "%d\n", i)
	}
	check(t, in.String(), want.String(), "stop\n", 7, `if . == 1500 then "stop\n" | halt_error(7) else . end`)
	check(t, in.String(), "0\n", "", 0, "if . == 1 then halt else . end")
}

func TestInputs(t *testing.T) {
	// filters that read input themselves run in order
	check(t, "1\n2\n3\n4\n5\n", "[1,2]\n[3,4]\n", "jvqq: error (at <stdin>:5): No more inputs\n", 5, "-c", "[., input]")
	check(t, "1\n2\n3\n", "6\n", "", 0, "-n", "reduce inputs as $x (0; . + $x)")
	check(t, "{\"a\":1}\nbad\n{\"a\":2}\n", "[1,2]\n",
		"jvqq: skipped 1 line that isn't valid JSON (first at <stdin>:2: Invalid literal at column 1)\n", 0, "-nc", "[inputs.a]")
}

func TestSlurpAndRaw(t *testing.T) {
	check(t, "1\n2\n3\n", "6\n", "", 0, "-s", "add")
	check(t, "a\nb\r\n\nc", "\"a\"\n\"b\\r\"\n\"\"\n\"c\"\n", "", 0, "-R", ".")
	check(t, "a\nb\n", "\"a\\nb\\n\"\n", "", 0, "-Rs", ".")
	check(t, "{\"s\":\"x\"}\n{\"s\":\"y\"}\n", "xy", "", 0, "-j", ".s")
}
