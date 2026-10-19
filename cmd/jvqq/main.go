// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

// Command jvqq runs jq filters over large JSON and JSON Lines input, using
// every CPU core.
package main

import (
	"fmt"
	"io"
	"os"
	"runtime/pprof"
	"strconv"
	"strings"

	"github.com/just-very-quickly/query/internal/engine"
	"github.com/just-very-quickly/query/internal/jv"
	"github.com/just-very-quickly/query/internal/query"
)

const usage = `jvqq - jq filters over big JSON Lines files, on every core

Usage:
  jvqq [options] <filter> [file...]
  jvqq [options] -f <file> [file...]

With no file (or "-"), jvqq reads standard input. Files may be gzip or
zstd compressed; jvqq notices by their first bytes.

Input is read as one JSON value per line (JSON Lines) and split between
all CPU cores; output keeps the input order. Input that isn't one value per
line, such as pretty-printed JSON, is read value by value and still split
between cores.

Options (as in jq):
  -c, --compact-output   one line per output
  -r, --raw-output       write strings without quotes
  -j, --join-output      like -r, with no newline after each output
  -n, --null-input       run the filter once on null; read no input
  -s, --slurp            read all input into one array (uses memory, as jq)
  -R, --raw-input        each input line is a string, not JSON
  -e, --exit-status      exit 1 if the last output is false or null, 4 if none
  -S, --sort-keys        sort object keys in the output
      --tab              indent with tabs
      --indent n         indent with n spaces (default 2)
      --arg name value   set $name to the string value
      --argjson name json  set $name to the JSON value
  -f, --from-file file   read the filter from file
  -M, --monochrome-output  accepted for jq compatibility (jvqq has no color)

jvqq's own options:
      --strict           stop at the first line that isn't valid JSON
                         (by default such lines are skipped and counted)
      --workers n        number of workers (default: one per CPU)
  -h, --help             show this help
  -V, --version          show the version

Aggregations (jvqq's addition to jq) work in parallel without -s. Put them
in one step of the filter's top-level pipe; the steps after run on the
result:
  count                  how many values reach this step
  count_by(f)            object of counts per value of f, most frequent first
  sum(f), avg(f)         sum and mean of the numbers f gives (nulls skipped)
  min(f), max(f)         smallest and largest value of f (nulls skipped)
  group_by(f; agg)       one row per value of f: {"key": ..., ...agg}
Example:
  jvqq 'select(.level == "error") | group_by(.service; {n: count, ms: avg(.ms)})' logs.jsonl

Exit codes: 0 ok, 1 or 4 with -e, 2 usage or input error, 3 filter doesn't
compile, 5 filter error on some input.
`

func main() {
	if p := os.Getenv("JVQQ_CPUPROFILE"); p != "" {
		f, err := os.Create(p)
		if err == nil {
			pprof.StartCPUProfile(f)
			code := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
			pprof.StopCPUProfile()
			f.Close()
			os.Exit(code)
		}
	}
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cfg := engine.Config{Stdout: stdout, Stderr: stderr, Name: "jvqq"}
	vars := map[string]any{}
	var positional []string
	filterSet := false
	var filterText string
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "jvqq: "+format+"\n", a...)
		fmt.Fprintln(stderr, "Use jvqq --help for help with command-line options.")
		return engine.ExitUsage
	}
	need := func(i, n int, name string) bool { return i+n < len(args) }
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		// Like jq, only -x and --x look like options; -.a or -1 is a filter.
		if !strings.HasPrefix(a, "-") || a == "-" || !(a[1] == '-' || isLetter(a[1])) {
			positional = append(positional, a)
			continue
		}
		if strings.HasPrefix(a, "--") {
			switch a {
			case "--compact-output":
				cfg.Format.Compact = true
			case "--raw-output":
				cfg.RawOutput = true
			case "--join-output":
				cfg.Join = true
			case "--null-input":
				cfg.NullInput = true
			case "--slurp":
				cfg.Slurp = true
			case "--raw-input":
				cfg.RawInput = true
			case "--exit-status":
				cfg.ExitStatus = true
			case "--sort-keys":
				cfg.Format.SortKeys = true
			case "--monochrome-output":
			case "--tab":
				cfg.Format.Tab = true
			case "--indent":
				if !need(i, 1, a) {
					return fail("--indent takes a number")
				}
				n, err := strconv.Atoi(args[i+1])
				if err != nil || n < 0 || n > 7 {
					return fail("--indent takes a number from 0 to 7")
				}
				i++
				if n == 0 {
					cfg.Format.Compact = true
				} else {
					cfg.Format.Indent = n
				}
			case "--arg", "--argjson":
				if !need(i, 2, a) {
					return fail("%s takes two parameters (e.g. %s varname value)", a, a)
				}
				name, val := args[i+1], args[i+2]
				i += 2
				if a == "--arg" {
					vars[name] = val
				} else {
					v, err := jv.Decode([]byte(val))
					if err != nil {
						return fail("invalid JSON text passed to --argjson")
					}
					vars[name] = v
				}
			case "--from-file":
				if !need(i, 1, a) {
					return fail("%s takes a file name", a)
				}
				b, err := os.ReadFile(args[i+1])
				if err != nil {
					return fail("%v", err)
				}
				filterText, filterSet = string(b), true
				i++
			case "--strict":
				cfg.Strict = true
			case "--workers":
				if !need(i, 1, a) {
					return fail("--workers takes a number")
				}
				n, err := strconv.Atoi(args[i+1])
				if err != nil || n < 1 {
					return fail("--workers takes a number above 0")
				}
				cfg.Workers = n
				i++
			case "--chunk-size":
				if !need(i, 1, a) {
					return fail("--chunk-size takes a number of bytes")
				}
				n, err := strconv.Atoi(args[i+1])
				if err != nil || n < 16 {
					return fail("--chunk-size takes a number of bytes, at least 16")
				}
				cfg.ChunkSize = n
				i++
			case "--help":
				fmt.Fprint(stdout, usage)
				return engine.ExitOK
			case "--version":
				fmt.Fprintf(stdout, "jvqq %s\n", Version)
				return engine.ExitOK
			case "--color-output":
				return fail("color output isn't supported")
			default:
				return fail("unknown option: %s", a)
			}
			continue
		}
		// short options, which can be combined: -rc
		for k := 1; k < len(a); k++ {
			switch a[k] {
			case 'c':
				cfg.Format.Compact = true
			case 'r':
				cfg.RawOutput = true
			case 'j':
				cfg.Join = true
			case 'n':
				cfg.NullInput = true
			case 's':
				cfg.Slurp = true
			case 'R':
				cfg.RawInput = true
			case 'e':
				cfg.ExitStatus = true
			case 'S':
				cfg.Format.SortKeys = true
			case 'M':
			case 'C':
				return fail("color output isn't supported")
			case 'h':
				fmt.Fprint(stdout, usage)
				return engine.ExitOK
			case 'V':
				fmt.Fprintf(stdout, "jvqq %s\n", Version)
				return engine.ExitOK
			case 'f':
				if !need(i, 1, "-f") {
					return fail("-f takes a file name")
				}
				b, err := os.ReadFile(args[i+1])
				if err != nil {
					return fail("%v", err)
				}
				filterText, filterSet = string(b), true
				i++
			default:
				return fail("unknown option: -%c", a[k])
			}
		}
	}
	if !filterSet {
		if len(positional) == 0 {
			fmt.Fprint(stderr, usage)
			return engine.ExitUsage
		}
		filterText, positional = positional[0], positional[1:]
	}
	prog, err := query.Compile(filterText, vars)
	if err != nil {
		fmt.Fprintf(stderr, "jvqq: error: %v\njvqq: 1 compile error\n", err)
		return engine.ExitCompile
	}
	cfg.Program = prog

	var inputs []engine.Input
	if len(positional) == 0 {
		positional = []string{"-"}
	}
	for _, name := range positional {
		name := name
		if name == "-" {
			inputs = append(inputs, engine.Input{Name: "<stdin>", Open: func() (io.ReadCloser, error) {
				return io.NopCloser(stdin), nil
			}})
			continue
		}
		inputs = append(inputs, engine.Input{Name: name, Path: name, Open: func() (io.ReadCloser, error) {
			f, err := os.Open(name)
			if err != nil {
				return nil, err
			}
			return f, nil
		}})
	}
	return engine.Run(cfg, inputs)
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
