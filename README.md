# jvqq

jq filters over big JSON Lines files, on every CPU core.

```sh
jvqq -c 'select(.level == "error") | {ts, service, msg}' app.jsonl
```

jvqq reads the jq language and prints what jq would print, byte for byte,
for the filters people run on logs: `select`, field access, object
construction, `map`, string functions, `test`, `sort_by`, and the rest of
jq's builtins. It splits the file between all cores, keeps the output in
input order, and holds only a few megabytes of the file in memory at a time,
however large it is. It also adds aggregations that run in parallel without
`-s`:

```sh
jvqq 'count_by(.service)' app.jsonl
jvqq 'select(.status >= 500) | group_by(.service; {n: count, ms: avg(.latency_ms)})' app.jsonl
```

It's one static binary written in Go, with no runtime to install. Files can
be gzip or zstd compressed.

**Status: v0.1.0.** jvqq is part of [JVQ.net](https://jvq.net) and is open
source under the Apache License 2.0.

## Why it exists

jq is the tool everybody knows for JSON, and it runs on one core. On a
multi-gigabyte log that means waiting a minute or more for a `select`, and
people end up writing a throwaway Python script instead. jvqq keeps the
filter language and makes the big file fast.

It's not the only fast jq. [qj](https://github.com/6/qj), built on simdjson,
is a little faster than jvqq on two of the benchmarks below, and
[jaq](https://github.com/01mf02/jaq) is a careful reimplementation. jvqq's
angle is exact jq output on a single portable binary, aggregations that
replace `reduce inputs` and `-s` on big files, and plain behavior on messy
input: a bad line is skipped and counted instead of ending the run.

## Install

Download the archive for your system from the
[latest release](https://github.com/just-very-quickly/query/releases/latest).
Each holds the `jvqq` binary, the license files and this README.

```sh
# Linux, x86-64
curl -fsSL https://github.com/just-very-quickly/query/releases/latest/download/jvqq_linux_amd64.tar.gz | tar -xz jvqq
# macOS, Apple silicon
curl -fsSL https://github.com/just-very-quickly/query/releases/latest/download/jvqq_darwin_arm64.tar.gz | tar -xz jvqq
```

The other archives are `jvqq_linux_arm64.tar.gz`, `jvqq_darwin_amd64.tar.gz`,
`jvqq_windows_amd64.zip` and `jvqq_windows_arm64.zip`. SHA256SUMS in the
release lists their checksums. The macOS binaries aren't signed by Apple, so
macOS may ask you to allow them the first time, or run
`xattr -d com.apple.quarantine jvqq`.

With Go 1.24 or later:

```sh
go install github.com/just-very-quickly/query/cmd/jvqq@latest
```

## Using it

jvqq takes jq's command line: a filter, then files (or standard input).

```sh
jvqq '.user.id' app.jsonl                      # one field per line
jvqq -r 'select(.level == "error") | .msg' app.jsonl.gz
jvqq -c '{ts, ms: .latency_ms}' a.jsonl b.jsonl.zst
kubectl logs deploy/api | jvqq -c 'select(.status >= 500)'
jvqq --arg svc billing -c 'select(.service == $svc)' app.jsonl
```

The options are jq's: `-c`, `-r`, `-j`, `-n`, `-s`, `-R`, `-e`, `-S`,
`--tab`, `--indent n`, `--arg`, `--argjson`, `--rawfile`, `--slurpfile`,
`--args`, `--jsonargs`, `-f`, `--unbuffered` and `-M`. jvqq adds two:

- `--strict` stops at the first line that isn't valid JSON, with exit
  code 2. Without it, such lines are skipped and counted, and a note at the
  end says how many there were and where the first one was.
- `--workers n` sets how many cores to use. The default is all of them.

`jvqq --help` lists everything, and [docs/reference.md](docs/reference.md)
has the details.

## Aggregations

jq answers "how many errors per service" with `reduce inputs` (one core, in
order) or `-s` (the whole file in memory). jvqq has aggregations that each
core computes on its share of the input and that are merged at the end:

| Aggregation | Result |
|---|---|
| `count` | how many values reach this step |
| `count_by(f)` | an object of counts per value of `f`, the most frequent first |
| `sum(f)` | the sum of the numbers `f` gives, nulls skipped |
| `avg(f)` | their mean, or null if there were none |
| `min(f)`, `max(f)` | the smallest and largest value of `f`, nulls skipped, in jq's order |
| `group_by(f; agg)` | one row per value of `f`, sorted by it: `{"key": ..., ...agg}` |

An aggregation goes in one step of the filter's top-level pipe. The steps
before it run on every input, in parallel; the steps after it run once, on
the result. Several aggregations can share the step:

```sh
jvqq 'select(.level == "error") | count' app.jsonl
jvqq '{n: count, total_ms: sum(.latency_ms), slowest: max(.latency_ms)}' app.jsonl
jvqq -c 'group_by(.service; {n: count, ms: avg(.latency_ms)}) | .[] | select(.n > 1000)' app.jsonl
jvqq 'count_by(.user.country) | to_entries | .[:5]' app.jsonl
```

Sums are exact: jvqq adds without rounding and rounds once at the end, so the
result doesn't depend on how the input was split between cores. When values
jq considers equal print differently (`1` and `1.0`), `count_by` and
`group_by` show the shorter one, so the output is the same on every run. A
filter error on some input is reported, and the exit code is 5 after the
result is printed.

`group_by(f)` with one argument is still jq's array function.

## How it works

1. **Chunks.** The input is cut into chunks of about 1 MiB at line breaks.
   On Linux a regular file is mapped into memory and cut without copying;
   compressed input and pipes are read through a buffer.
2. **Workers.** One worker per core takes the next chunk, checks each line
   is valid JSON, and runs the filter on it. Lines are parsed lazily: a
   filter that reads two fields of a large object doesn't decode the rest,
   and a line printed unchanged is copied as it was when jq would print the
   same bytes.
3. **In order.** A writer puts the workers' output back in input order. At
   most a few chunks per worker are in flight, so memory stays flat on a file
   of any size.

Input that isn't one value per line, such as pretty-printed JSON, is
detected from the first chunk and read value by value; it is still split
between cores. A filter that calls `input` or `inputs` reads the input
itself, so it runs in order on one core, as in jq. `-s` reads everything into
memory, also as in jq.

## Compared with jq

For the filters jvqq supports, standard output is jq 1.7.1's, byte for byte:
number formatting (literals keep their digits, `1.000` stays `1.000`, `1e2`
prints as `1E+2`), string escapes, invalid UTF-8 replaced the way jq does
it, key order, and the text of most error messages as `try ... catch .`
sees them. Exit codes are jq's too. jvqq also reads what jq reads beyond
strict JSON: numbers like `01` and `.5`, `nan` and `Infinity`, and a byte
order mark at the start. The test suite checks all this by running every
filter in [cmd/jvqq/testdata/filters.txt](cmd/jvqq/testdata/filters.txt)
through jq and through jvqq, from standard input and from a file, with the
input split several ways ([test/results/tests.txt](test/results/tests.txt)
has the latest run).

The differences:

- **Bad lines.** jq stops at the first line that isn't valid JSON. jvqq
  skips it, counts it, and reports the count and the first one's location at
  the end; `--strict` stops instead.
- **Not supported.** Function definitions (`def`), `label` and `break`,
  destructuring (`. as [$a, $b]`, `?//`), modules (`import`, `include`),
  `input_line_number`, `$__prog_args`, `get_search_list` and the other module
  builtins: jvqq refuses these with a message and exit code 3 rather than
  doing something different. The options `--stream`, `--seq`,
  `--raw-output0`, `-a`, `-C` and `-L` are refused with exit code 2.
- **Regular expressions** use Go's RE2 engine instead of Oniguruma: no
  backreferences or lookaround, and empty matches with the `g` or `n` flag
  can come out differently (`"ab" | [match("a?"; "g")]` finds two matches in
  jvqq, three in jq). Regex syntax errors are worded differently.
- **Math functions** use Go's math library instead of the C library, so
  many of them (`log`, `cbrt`, `tan`, `exp10`, the Bessel and gamma
  functions) can differ from jq in the last digit. In `strftime`, `%Z` of a
  `gmtime` time is `UTC`, where jq writes the local zone's name.
- **Messages on standard error** start with `jvqq:` instead of `jq:`.
  Messages about invalid JSON text (from `fromjson`, `tonumber` or bad
  lines) and compile errors are worded differently from jq's.

## Performance

Measured by [scripts/bench.py](scripts/bench.py) on 1 GiB of generated log
lines ([test/bench/gen.go](test/bench/gen.go)), against jq 1.7.1, jaq, gojq
and qj, with every output checked against jq's. The numbers below are from
[test/results/bench.md](test/results/bench.md), which also gives the
machine, the versions and the exact commands. The `bench` workflow records
the same suite on a GitHub-hosted runner; its results go to the
`bench-results` branch, and `scripts/record.sh` copies them into
test/results/github/.

| workload | jq | jvqq | jvqq vs jq | jvqq, 1 core | qj | jaq | gojq |
|---|---:|---:|---:|---:|---:|---:|---:|
| reformat | 67.5 s | **3.3 s** | 20x | 5.7 s | 3.9 s | 37.5 s | 78.6 s |
| select | 38.7 s | **2.4 s** | 16x | 4.1 s | 2.1 s | 23.9 s | 56.9 s |
| field | 33.1 s | **1.9 s** | 17x | 3.9 s | 1.8 s | 20.9 s | 55.1 s |
| project | 42.2 s | **2.8 s** | 15x | 5.3 s | 3.0 s | 33.1 s | 62.2 s |
| count | 30.4 s | **2.1 s** | 14x | 22.0 s ¹ | 7.0 s | 14.8 s | 40.4 s |
| count by | 49.7 s | **2.1 s** | 23x | 27.8 s ¹ | 11.9 s | 18.0 s | 53.6 s |
| sum by | 52.7 s | **3.0 s** | 18x | 28.4 s ¹ | 11.8 s | 21.3 s | 55.6 s |

¹ For count, count by and sum by, jq and the others run `reduce inputs`. The
"jvqq" column is jvqq's aggregation (`count`, `count_by(.service)`,
`group_by(.service; {ms: sum(.latency_ms)})`); this column is jvqq running
jq's `reduce inputs` filter, which reads in order on one core.

Recorded on a 2-core Intel Xeon at 2.80GHz (Linux), median of 3 runs after
a warm-up run. Every output was checked against jq's: jvqq's were the same
bytes for the line-by-line filters and the same values for the
aggregations. jvqq used at most 32 MiB; jaq used about 1 GiB on every
workload. qj is a little faster than jvqq on select and field and slower on
the rest. More cores help jvqq: a single-core run is in the "1 core" column.

On a 6 GiB file (18,440,091 lines), jvqq alone, one run each:

- count 17.8 s at 46 MiB (every line counted)
- count by 22.4 s at 48 MiB (counts add up to the lines)
- select 20.0 s at 49 MiB (2,303,691 lines out)
- reformat 35.4 s at 43 MiB (one output line per input line)

## Exit codes

| Code | Meaning |
|---|---|
| 0 | done |
| 1 | `-e`: the last output was `false` or `null` |
| 2 | bad arguments, a file that can't be read, invalid input with `--strict` |
| 3 | the filter doesn't compile, or uses something jvqq doesn't support |
| 4 | `-e`: there was no output |
| 5 | the filter failed on the last input (with an aggregation, on any input) |

## Building and testing

```sh
go build ./cmd/jvqq
go test ./...                  # needs jq 1.7.x on PATH for the differential test
sh scripts/record.sh           # every test, written to test/results/
```

[RELEASING.md](RELEASING.md) describes how releases go out.

## License

Apache License 2.0; see [LICENSE](LICENSE) and [NOTICE](NOTICE). The
binaries include the Go runtime and the
[klauspost/compress](https://github.com/klauspost/compress) library, whose
licenses are in [THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md).
