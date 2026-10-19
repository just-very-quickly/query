# jvqq reference

This is the full reference for jvqq 0.1. The [README](../README.md) is the
place to start; this page has the details.

## Command line

```
jvqq [options] <filter> [file...]
jvqq [options] -f <file> [file...]
```

The filter is a jq program. The files are read in the order given; with no
file, or with `-`, jvqq reads standard input. As in jq, an argument starting
with `-` followed by a letter is an option, so `-.a` and `-1` are filters,
and `--` ends the options.

### Options shared with jq

| Option | Effect |
|---|---|
| `-c`, `--compact-output` | each output on one line |
| `-r`, `--raw-output` | strings are written without quotes or escapes |
| `-j`, `--join-output` | like `-r`, with no newline after each output |
| `-n`, `--null-input` | run the filter once with `null` as input; `input` and `inputs` read the files |
| `-s`, `--slurp` | read every input value into one array and run the filter on it once |
| `-R`, `--raw-input` | each input line is a string; with `-s`, the whole input is one string |
| `-e`, `--exit-status` | exit 1 if the last output was `false` or `null`, 4 if there was none |
| `-S`, `--sort-keys` | write object keys in sorted order |
| `--tab` | indent with one tab |
| `--indent n` | indent with `n` spaces, 0 to 7 (default 2; 0 is the same as `-c`) |
| `--arg name value` | `$name` is the string `value` |
| `--argjson name json` | `$name` is the JSON value |
| `--rawfile name file` | `$name` is the file's text |
| `--slurpfile name file` | `$name` is an array of the JSON values in the file |
| `--args` | the arguments after the filter are strings in `$ARGS.positional`, not files |
| `--jsonargs` | the same, parsed as JSON |
| `-f file`, `--from-file file` | read the filter from the file; every other argument is an input file |
| `--unbuffered` | write the output as soon as each piece of input is done, instead of in large blocks |
| `-M`, `--monochrome-output` | accepted; jvqq never colors its output |
| `-h`, `--help` | show the help |
| `-V`, `--version` | show the version |

`$ARGS.named` holds the `--arg`, `--argjson`, `--rawfile` and `--slurpfile`
values, in the order given. `$ENV` and `env` hold the environment.

Short options can be combined: `-rc`, `-nc`.

Not supported: `--stream`, `--stream-errors`, `--seq`, `--raw-output0`,
`-a` (`--ascii-output`), `-C` (`--color-output`) and `-L`. They are refused
with exit code 2.

### jvqq's own options

| Option | Effect |
|---|---|
| `--strict` | stop at the first input line that isn't valid JSON (exit 2). Without it, such lines are skipped and counted. |
| `--workers n` | how many workers run the filter; the default is one per CPU (Go's GOMAXPROCS) |
| `--chunk-size bytes` | how much input each worker takes at a time (default 1048576). Small values are for testing. |

## Input

**JSON Lines.** By default each line holds one JSON value. Lines may end in
`\n` or `\r\n`, blank lines are skipped, and the last line doesn't need a
newline. A line may hold several values separated by spaces (`1 2 3`), as jq
allows. A byte order mark at the start of a file is skipped.

**What counts as JSON.** As in jq: numbers may also be written `01`, `.5`,
`1.`, `+1` or `1.e3` (they print as jq prints them: `1`, `0.5`, `1`, `1`,
`1E+3`), `nan`, `NaN` and `-nan` read as NaN (printed `null`), `inf` and
`Infinity` in any case as infinity (printed as the largest double), and
nesting is limited to 256 levels, where an object counts as two. Lines
using the extra forms take a slower path; nothing else changes.

**Standard input** is handled as it arrives: lines from a pipe that a
program writes slowly (`tail -f app.jsonl | jvqq ...`) are processed when
they come, without waiting for a full chunk. Add `--unbuffered` to have the
output written out right away too.

**Other JSON.** If the start of the input holds a value that spans lines,
such as pretty-printed JSON, jvqq reads the whole input value by value
instead. It is still cut into chunks at value boundaries and split between
workers.

**Compression.** gzip and zstd input is recognized by its first bytes, in
files and on standard input; the name doesn't matter.

**Several files** are read one after another, as if joined. A file that can't
be opened is reported (exit code 2 at the end) and the others are still read.

**Bad lines.** A line that isn't valid JSON is skipped. At the end jvqq
writes to standard error how many lines it skipped and where the first one
was:

```
jvqq: skipped 2 lines that aren't valid JSON (first at app.jsonl:812: Unfinished JSON term at column 41)
```

The exit code isn't changed by skipped lines. With `--strict`, the first bad
line stops the run: everything before it is written, then the error, and the
exit code is 2. When the input isn't JSON Lines (the "other JSON" case
above), a syntax error always stops the run with exit code 2, since there
are no lines to skip.

**Raw input.** With `-R` each line is a string, with its `\n` removed (a
`\r` before it is kept, as in jq). `-Rs` gives the whole input as one string.

**Files changing.** On Linux, regular files are mapped into memory. If a file
gets shorter while jvqq reads it (a log rotated by truncating it, say), jvqq
stops with exit code 2 and the message
`<file>: the file got shorter while it was being read`. Lines appended to a
file after jvqq has started on it aren't read on Linux; on other systems,
lines appended before jvqq reaches the end are.

## Output

Values are written the way jq 1.7.1 writes them.

- Numbers from the input keep their text when jq would keep it: `1.000`
  stays `1.000`, `100000000000000000001` keeps all its digits. jq writes
  exponents its own way (`1e2` as `1E+2`) and jvqq does the same. Computed
  numbers use the shortest form that reads back as the same double, with
  jq's choice of when to use an exponent (`1e-05`, `1e+17`).
- Strings use jq's escapes: `\"`, `\\`, `\n`, `\t`, `\r`, `\b`, `\f`, and
  `\u00xx` (lowercase) for other control characters and DEL. `/` and
  non-ASCII characters are written as they are. Invalid UTF-8 in input
  strings becomes U+FFFD by jq's rules: one replacement for a bad byte, for
  a sequence cut short, or for a whole overlong, surrogate or out-of-range
  sequence.
- Objects keep their key order; a key given twice keeps its first position
  and its last value.
- Pretty output indents by 2 spaces, `--indent n` spaces or a tab. Empty
  arrays and objects are `[]` and `{}`.

The output is in input order, whatever the number of workers.

## Errors

A filter error on one input is written to standard error and the run goes on
with the next input:

```
jvqq: error (at app.jsonl:17): Cannot index array with string "level"
```

The location is the file and line of the input. The message after the colon
is jq's, apart from the few noted under "Compared with jq" in the README. An error raised with `error(value)` where the value isn't a string
is written as `jvqq: error (at ...) (not a string): <json>`.

`halt` stops with exit code 0; `halt_error` writes its input to standard
error (a string as it is, `null` as nothing, anything else as JSON with a
newline) and stops with exit code 5, or the code given. Output from inputs before the halting
one is written; nothing after it.

`debug` and `stderr` messages go to standard error, after the output of the
same chunk of input. `stderr` writes a string as it is and anything else as
compact JSON, with no newline, as jq 1.7.1 does.

A file that can't be read is reported in its place: after the output of the
files before it, before the output of the files after it.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | done |
| 1 | with `-e`: the last output was `false` or `null` |
| 2 | bad arguments; a file that couldn't be read or got shorter while read; invalid input with `--strict` or in document mode |
| 3 | the filter doesn't compile, or uses a feature jvqq doesn't support |
| 4 | with `-e`: there was no output |
| 5 | the filter failed on the last input; with an aggregation, on any input; or `halt_error` without a code |

## Aggregations

Aggregations are jvqq's addition to jq. They collect over every value that
reaches their step, in parallel, without holding the input in memory.

| Aggregation | Result | Empty input |
|---|---|---|
| `count` | the number of values reaching the step | `0` |
| `count_by(f)` | an object: for each value of `f`, how many times it came; the most frequent first, ties in jq's order of the values | `{}` |
| `sum(f)` | the sum of the numbers `f` gives | `0` |
| `avg(f)` | the mean of the numbers `f` gives | `null` |
| `min(f)` | the smallest value `f` gives, in jq's order | `null` |
| `max(f)` | the largest value `f` gives, in jq's order | `null` |
| `group_by(f; agg)` | an array with one row per value of `f`, sorted in jq's order: `{"key": <value>}` followed by the fields of `agg` | `[]` |

`f` can give any number of values for each input; each one counts (an
input for which `f` gives nothing adds nothing). `sum`,
`avg`, `min` and `max` skip nulls. `sum` and `avg` fail on a value that
isn't a number. `count_by` keys are the values made into strings, as with
`tostring` (`null` becomes `"null"`).

In `group_by(f; agg)`, `agg` is an expression using aggregations, computed
per group. If it gives an object, its fields come after `key` in each row (a
field named `key` replaces the group's key); otherwise the row is
`{"key": ..., "value": ...}`. If it gives several values
(`group_by(.a; count, sum(.b))`), each makes its own row. Aggregations nest: `group_by(.a; count_by(.b))`
and `group_by(.a; group_by(.b; count))` work.

**Where they go.** Aggregations go in one step of the filter's top-level
pipe: `A | B | C` where only `B` uses them. `A` runs on every input, on all
workers. `B` runs once at the end, with `null` as its input and each
aggregation standing for its result, so `B` can combine them:
`{n: count, mean: (sum(.ms) / count)}`, `count > 0`, `[min(.t), max(.t)]`.
`C` runs on what `B` gives. Aggregations in two steps of the pipe are a
compile error. Since `B` runs on `null`, a step like `map(count)` fails at
the end ("Cannot iterate over null"); write `[count]` instead.

**Exactness.** `sum` and `avg` add exactly (without rounding along the way)
and round once, so the result is the same however the input was split, and
can differ from jq's `add` in the last digit, where jq rounds after every
addition. When values jq considers equal print differently (`1` and `1.0`),
`count_by` and `group_by` show the shorter, so the output is the same on every
run.

**Errors.** A filter error in `A` is reported for that input and the run
goes on; at the end, the result is printed and the exit code is 5. An error
in `B` (in any group's row of a `group_by`, say) is reported as
`error (at end of input)`, nothing is printed for `B`, and the exit code is
5.

Remember that `B` runs on `null`: `if .a > 1 then count else 0 end` tests
`null > 1`, and `.a as $v | count + $v` binds `$v` to `null`. Work on each
input goes in `A`, before the aggregation step.

**Memory.** Each worker keeps one entry per distinct value of `f`, and the
entries are merged at the end. Grouping by a value with millions of distinct
values (a user id on a large log) uses memory in proportion and is slower
than grouping by a value with few.

## The jq language

jvqq supports the jq 1.7.1 language apart from the features listed under
"Not supported". Everything below works as in jq.

**Paths and values.** `.`, `..`, `.foo`, `."foo"`, `.["foo"]`, `.[n]`,
`.[n:m]`, `.[]`, the `?` forms, literals, arrays, objects (including
`{a, $x, "k": v, (expr): v, @base64: v}` style keys), string interpolation
`"\(...)"`, and formats with strings (`@base64 "x\(.)"`).

**Operators.** `|`, `,`, `+ - * / %`, comparisons, `and or not`, `//`,
`try ... catch ...`, `?`, `if ... then ... elif ... else ... end` (the `else`
is optional), `reduce`, `foreach` (with 2 or 3 parts), `. as $x | ...`,
`$__loc__`, and assignments `=`, `|=`, `+=`, `-=`, `*=`, `/=`, `%=`, `//=`.

**Formats.** `@text`, `@json`, `@html`, `@uri`, `@csv`, `@tsv`, `@sh`,
`@base64`, `@base64d`.

**Builtins.** `IN`, `INDEX`, `JOIN`, `abs`, `add`, `all`, `any`, `arrays`,
`ascii_downcase`, `ascii_upcase`, `booleans`, `bsearch`, `builtins`,
`capture`, `combinations`, `contains`, `debug`, `del`, `delpaths`, `empty`,
`endswith`, `env`, `error`, `explode`, `finites`, `first`, `flatten`,
`format`, `from_entries`, `fromdate`, `fromdateiso8601`, `fromjson`,
`fromstream`, `getpath`, `gmtime`, `group_by`, `gsub`, `halt`, `halt_error`,
`has`, `implode`, `in`, `index`, `indices`, `infinite`, `input`,
`input_filename`, `inputs`, `inside`, `isempty`, `isfinite`, `isinfinite`,
`isnan`, `isnormal`, `iterables`, `join`, `keys`, `keys_unsorted`, `last`,
`length`, `limit`, `localtime`, `ltrimstr`, `map`, `map_values`, `match`,
`max`, `max_by`, `min`, `min_by`, `mktime`, `nan`, `normals`, `not`, `now`,
`nth`, `nulls`, `numbers`, `objects`, `path`, `paths`, `pick`, `range`,
`recurse`, `repeat`, `reverse`, `rindex`, `rtrimstr`, `scalars`, `scan`,
`select`, `setpath`, `sort`, `sort_by`, `split`, `splits`, `startswith`,
`stderr`, `strflocaltime`, `strftime`, `strings`, `strptime`, `sub`, `test`,
`to_entries`, `todate`, `todateiso8601`, `tojson`, `tonumber`, `tostream`,
`tostring`, `transpose`, `truncate_stream`, `type`, `unique`, `unique_by`,
`until`, `utf8bytelength`, `values`, `walk`, `while`, `with_entries`, and
the math functions `acos`, `acosh`, `asin`, `asinh`, `atan`, `atan2`,
`atanh`, `cbrt`, `ceil`, `copysign`, `cos`, `cosh`, `drem`, `erf`, `erfc`,
`exp`, `exp10`, `exp2`, `expm1`, `fabs`, `fdim`, `floor`, `fma`, `fmax`,
`fmin`, `fmod`, `frexp`, `gamma`, `hypot`, `j0`, `j1`, `jn`, `ldexp`,
`lgamma`, `lgamma_r`, `log`, `log10`, `log1p`, `log2`, `logb`, `modf`,
`nearbyint`, `nextafter`, `nexttoward`, `pow`, `remainder`, `rint`,
`round`, `scalb`, `scalbln`, `significand`, `sin`, `sinh`, `sqrt`, `tan`,
`tanh`, `tgamma`, `trunc`, `y0`, `y1`, `yn`.

Regular expressions (`test`, `match`, `capture`, `scan`, `split/2`,
`splits`, `sub`, `gsub`) use Go's RE2 engine with jq's flags `g`, `i`, `x`,
`n`, `s`, `l` and `p`. Named groups `(?<name>...)` work as in jq, and match
objects have jq's shape, key order included. jq uses Oniguruma, and some
things differ: RE2 has no backreferences or lookaround (a pattern using them
is an error, and syntax errors are worded differently); with the `n` flag
an empty match is dropped, where Oniguruma looks for a non-empty match at
the same place (`"aaa" | [match("a*?"; "gn")]` gives no matches in jvqq,
three in jq); and with `g`, jq also reports an empty match right after a
non-empty one, and steps through multi-byte characters a byte at a time
after an empty match, so patterns that can match nothing (`a?`, `x*`) can
give more matches in jq.

`input_filename` gives the name of the file being read, as it was given on
the command line, and `"<stdin>"` for standard input, as jq 1.7.1 does.

### Not supported

These are refused with a compile error (exit code 3) that names the feature:

- function definitions: `def f: ...;`
- `label $name | ... break $name`
- destructuring: `. as [$a, $b]`, `. as {a: $x}`, and the `?//` alternatives
- modules: `import`, `include`, `modulemeta`, `get_search_list`,
  `get_prog_origin`, `get_jq_origin`
- `input_line_number`, `$__prog_args`
- `{$__loc__}` as an object key (`$__loc__` itself works)

The math functions use Go's math library and many can differ from jq (which
uses the C library) in the last digit: `log`, `cbrt`, `tan`, `exp10`,
`erfc`, the Bessel and gamma functions, among others. `pow10` isn't there,
as in jq 1.7.1 builds.

Dates use Go's time package, in the C locale. `strftime` supports `%a %A %b
%B %c %C %d %D %e %F %g %G %h %H %I %j %k %l %m %M %n %p %r %R %s %S %t %T
%u %U %V %w %W %x %X %y %Y %z %Z %%`; any other directive is written as it
is. `strptime` reads `%Y %m %d %e %H %I %M %S %y %j %b %B %h %a %A %p %z
%Z %s %T %F %n %t %%`, matches other characters literally (a space matches
any run of spaces), and fails with a message on any other directive. Times
are truncated toward zero to whole seconds, as in jq. For `gmtime` results,
`%Z` is `UTC` and `%s` counts from the epoch in UTC; jq 1.7.1 uses the local
time zone for both, so the two agree only when TZ is UTC.

## Performance notes

- A file on disk is faster than the same data on a pipe on Linux, where
  files are mapped into memory.
- zstd decompresses faster than gzip. Decompression happens on one core, so
  on many cores it can be what limits the speed.
- `-s`, `input` and `inputs` make the run sequential or hold the input in
  memory, as in jq. For counting and grouping, the aggregations are the fast
  way.
- Filters that build a lot of new values (`[.[] | ...]`, large object
  construction) cost more per line than ones that pick fields; `select` and
  field access on large lines are the cheapest.
- `JVQQ_CPUPROFILE=file` writes a Go CPU profile of the run to `file`, for
  looking into a slow filter.
