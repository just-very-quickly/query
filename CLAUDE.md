# Notes for Claude

This file says where the next session starts and what comes next. What comes
next is decided with the owner before it goes here, and work starts only when
the owner says go. There is no separate roadmap file.

This repository, `just-very-quickly/query`, holds jvqq, a JSON Lines query
tool, under the same organization as jvq (`just-very-quickly/jvq`, JVQ.net).
Website and branding for jvqq are not decided yet: no site work until the
owner settles them.

jvqq runs on the owner's low maintenance workflow: Claude does the code, tests,
docs, CI, releases and upkeep; the owner gives the go-ahead. The code is
Apache 2.0, the README is the manual, docs/reference.md is the reference, and
there are no Word or PDF documents.

## Releases

Releases are automated. To release, raise `Version` in `cmd/jvqq/version.go`
and push to `main`; never ask the owner to publish one.
[RELEASING.md](RELEASING.md) has the procedure and the checks to run
afterwards.

## Where things stand

v0.1.0 is the first release (October 2026). jvqq runs jq filters over JSON
Lines on every core, with output in input order and flat memory, and prints
what jq 1.7.1 prints. The layout:

- `internal/jv`: JSON values. Validation, lazy raw values (`Raw`, and `Doc`,
  which indexes a line's top-level fields while validating it), decoding,
  jq-exact encoding (number literals kept, jq's float format and escapes),
  jq's ordering and equality.
- `internal/query`: the jq language. Lexer, parser, compiler to closures,
  paths and assignment, about 215 builtins, formats, regex on RE2, dates, and
  the aggregations (`agg.go`: count, count_by, sum, avg, min, max,
  group_by(f; agg), merged across workers; exact sums).
- `internal/engine`: chunks at line breaks (memory-mapped on Linux, buffered
  otherwise, gzip and zstd by magic bytes), the worker pool, the ordered
  writer, bad-line counting and `--strict`, document mode for multi-line
  JSON, and the sequential mode for `input`/`inputs`.
- `cmd/jvqq`: the command line.

Changes from the approved plan, made while building v0.1.0:
- The jq subset grew to nearly the whole jq 1.7.1 language: once exact
  output was required, the missing pieces were the ones users would hit
  first. Not supported, refused with exit 3: `def`, `label`/`break`,
  destructuring and `?//`, modules, `input_line_number`, `$__prog_args`.
- Aggregation syntax: `group_by(f; agg)` where `agg` is any expression of
  aggregations, usually an object (`{n: count, ms: avg(.ms)}`), rather than a
  comma list.
- The benchmark data is generated application logs (test/bench/gen.go)
  rather than GitHub events, since that is what the tool is for.
- Memory mapping is Linux only (other systems lack `madvise` in Go's syscall
  package without golang.org/x/sys). CI runs on Windows as well as Linux and
  macOS; the differential test runs on Linux and macOS.
- Added jq options beyond the plan: `--rawfile`, `--slurpfile`, `--args`,
  `--jsonargs`, `$ARGS`, `--unbuffered`.

Known and documented differences from jq: bad lines skipped and counted;
RE2 instead of Oniguruma (no backreferences or lookaround; empty matches
with `g` or `n` can differ; regex syntax errors are worded differently); Go
math in the last digit for many functions; `%Z` and `%s` of a `gmtime` time
are UTC; messages about invalid JSON text and compile errors are worded
differently. Grouping by a field with about a million distinct values on
1 GiB takes 10 to 16 seconds on 2 cores, mostly in the serial merge and the
garbage collector; it's fine for the usual few-thousand-group case.

An independent review before the release found two crashes (halt_error on
an input value read memory after its chunk was released; tonumber on a
string with leading spaces) and about thirty smaller differences from jq;
all were fixed or documented, with a test or a differential case each. The
differential test now also reads the corpus as a file, so the
memory-mapped path is compared with jq.

## Test results

`sh scripts/record.sh` runs every test (the differential test needs jq 1.7.x
on PATH), the race detector, the release plan cases and the smoke check on a
static build, and writes them to `test/results/`. With `BENCH_TOOLS` set to a
folder holding jq, jaq, gojq, qj and peak (built from test/bench/peak.go), it
also runs the benchmark on this machine. Every number in the README comes from
those files; record again after any change that could move them.

The `bench` workflow records the benchmark on a GitHub-hosted runner when the
engine or the evaluator changes (or by hand) and pushes it to the
`bench-results` branch; record.sh copies it into `test/results/github/`.

The differential test is `cmd/jvqq/diff_test.go` over
`cmd/jvqq/testdata/filters.txt` and `corpus.jsonl`: add a line there for every
new builtin or behavior, and mark unsupported features with `~`.

## Next

Not decided yet. Propose to the owner and wait for the go-ahead.
