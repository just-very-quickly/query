// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The differential test runs every filter in testdata/filters.txt through
// jq and through jvqq, on testdata/corpus.jsonl, and requires the same
// output and the same exit code. jvqq runs three ways: one worker, several
// workers with tiny chunks (so every few lines is its own chunk), and the
// default. It needs jq 1.7 or later on PATH; without it the test is skipped.
//
// Lines starting with "~" are features jvqq doesn't support: it must refuse
// them with a compile error (exit 3) rather than run them differently.

func jqVersion(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("jq", "--version").Output()
	if err != nil {
		t.Skip("jq isn't installed")
	}
	v := strings.TrimSpace(string(out))
	m := regexp.MustCompile(`^jq-1\.(\d+)`).FindStringSubmatch(v)
	if m == nil {
		t.Skipf("unrecognized jq version %q", v)
	}
	if n, _ := strconv.Atoi(m[1]); n < 7 {
		t.Skipf("jq 1.7 or later is needed, found %s", v)
	}
	return v
}

type diffCase struct {
	line        int
	flags       []string
	filter      string
	unsupported bool
}

func loadCases(t *testing.T) []diffCase {
	f, err := os.Open("testdata/filters.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var cases []diffCase
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		n++
		line := sc.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		c := diffCase{line: n}
		if strings.HasPrefix(line, "~") {
			c.unsupported = true
			line = line[1:]
		}
		if i := strings.IndexByte(line, '\t'); i >= 0 {
			c.flags = strings.Fields(line[:i])
			line = line[i+1:]
		}
		c.filter = line
		cases = append(cases, c)
	}
	return cases
}

func nullInput(flags []string) bool {
	for _, f := range flags {
		if strings.HasPrefix(f, "-") && !strings.HasPrefix(f, "--") && strings.ContainsRune(f, 'n') {
			return true
		}
	}
	return false
}

func runJq(args []string, stdin []byte) (string, int) {
	cmd := exec.Command("jq", args...)
	cmd.Stdin = bytes.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &bytes.Buffer{}
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	}
	return out.String(), code
}

func runJvqq(args []string, stdin []byte) (string, string, int) {
	var out, errOut bytes.Buffer
	code := run(args, bytes.NewReader(stdin), &out, &errOut)
	return out.String(), errOut.String(), code
}

func TestDifferentialAgainstJq(t *testing.T) {
	jqVersion(t)
	corpus, err := os.ReadFile("testdata/corpus.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	modes := [][]string{
		{"--workers", "1"},
		{"--workers", "3", "--chunk-size", "64"},
		nil,
	}
	failures := 0
	for _, c := range loadCases(t) {
		args := append(append([]string{}, c.flags...), c.filter)
		input := corpus
		if nullInput(c.flags) {
			input = nil
		}
		if c.unsupported {
			_, stderr, code := runJvqq(append([]string{}, args...), input)
			if code != 3 {
				t.Errorf("line %d %q: expected a compile error for an unsupported feature, got exit %d (%s)", c.line, c.filter, code, stderr)
				failures++
			}
			continue
		}
		want, wantCode := runJq(args, input)
		for _, mode := range modes {
			got, stderr, code := runJvqq(append(append([]string{}, mode...), args...), input)
			if got != want || code != wantCode {
				failures++
				t.Errorf("line %d %v %q (mode %v):\n jq    exit %d: %s\n jvqq  exit %d: %s\n stderr: %s",
					c.line, c.flags, c.filter, mode, wantCode, short(want), code, short(got), short(stderr))
				break
			}
		}
		if failures > 40 {
			t.Fatal("too many failures")
		}
	}
}

func short(s string) string {
	if len(s) > 600 {
		return s[:600] + "..."
	}
	return s
}
