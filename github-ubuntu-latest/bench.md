File: 1024 MiB of JSON Lines, 3,073,492 lines (test/bench/gen.go). Machine: AMD EPYC 9V74 80-Core Processor, 4 cores, Linux-6.17.0-1022-azure-x86_64-with-glibc2.39. Each time is the median of 3 runs, after one run to warm the file cache; memory is the peak resident size. "vs jq" is how many times faster than jq.
Versions: jvqq 0.1.0, qj 0.3.0, jaq 3.1.1, gojq 0.12.17 (rev: f4c2cfe/go1.23.3), jq 1.7.1.

### reformat: print every line compactly

| tool | command | seconds | vs jq | peak memory | output |
|---|---|---:|---:|---:|---|
| jq | `jq -c . FILE` | 35.40 | 1.0x | 6 MiB | reference |
| jvqq | `jvqq -c . FILE` | 1.77 | 20.0x | 42 MiB | same bytes |
| jvqq, 1 core | `jvqq --workers 1 -c . FILE` | 3.77 | 9.4x | 29 MiB | same bytes |
| qj | `qj -c . FILE` | 1.51 | 23.5x | 59 MiB | same bytes |
| jaq | `jaq -c . FILE` | 28.01 | 1.3x | 1035 MiB | same bytes |
| gojq | `gojq -c . FILE` | 47.95 | 0.7x | 12 MiB | same values |

### select: keep the error lines

| tool | command | seconds | vs jq | peak memory | output |
|---|---|---:|---:|---:|---|
| jq | `jq -c 'select(.level == "error")' FILE` | 20.76 | 1.0x | 6 MiB | reference |
| jvqq | `jvqq -c 'select(.level == "error")' FILE` | 1.01 | 20.6x | 52 MiB | same bytes |
| jvqq, 1 core | `jvqq --workers 1 -c 'select(.level == "error")' FILE` | 2.21 | 9.4x | 33 MiB | same bytes |
| qj | `qj -c 'select(.level == "error")' FILE` | 0.77 | 27.1x | 51 MiB | same bytes |
| jaq | `jaq -c 'select(.level == "error")' FILE` | 17.30 | 1.2x | 1038 MiB | same bytes |
| gojq | `gojq -c 'select(.level == "error")' FILE` | 29.98 | 0.7x | 11 MiB | same values |

### field: one nested field per line

| tool | command | seconds | vs jq | peak memory | output |
|---|---|---:|---:|---:|---|
| jq | `jq .user.id FILE` | 17.99 | 1.0x | 6 MiB | reference |
| jvqq | `jvqq .user.id FILE` | 0.92 | 19.5x | 52 MiB | same bytes |
| jvqq, 1 core | `jvqq --workers 1 .user.id FILE` | 2.03 | 8.9x | 32 MiB | same bytes |
| qj | `qj .user.id FILE` | 0.69 | 26.0x | 46 MiB | same bytes |
| jaq | `jaq .user.id FILE` | 18.45 | 1.0x | 1038 MiB | same bytes |
| gojq | `gojq .user.id FILE` | 36.01 | 0.5x | 12 MiB | same bytes |

### project: a smaller object per line

| tool | command | seconds | vs jq | peak memory | output |
|---|---|---:|---:|---:|---|
| jq | `jq -c '{ts, service, ms: .latency_ms}' FILE` | 23.07 | 1.0x | 6 MiB | reference |
| jvqq | `jvqq -c '{ts, service, ms: .latency_ms}' FILE` | 1.24 | 18.6x | 51 MiB | same bytes |
| jvqq, 1 core | `jvqq --workers 1 -c '{ts, service, ms: .latency_ms}' FILE` | 2.77 | 8.3x | 33 MiB | same bytes |
| qj | `qj -c '{ts, service, ms: .latency_ms}' FILE` | 1.12 | 20.6x | 44 MiB | same bytes |
| jaq | `jaq -c '{ts, service, ms: .latency_ms}' FILE` | 26.91 | 0.9x | 1035 MiB | same bytes |
| gojq | `gojq -c '{ts, service, ms: .latency_ms}' FILE` | 39.40 | 0.6x | 11 MiB | same values |

### count: count the lines

| tool | command | seconds | vs jq | peak memory | output |
|---|---|---:|---:|---:|---|
| jq | `jq -n 'reduce inputs as $x (0; . + 1)' FILE` | 17.32 | 1.0x | 6 MiB | reference |
| jvqq | `jvqq count FILE` | 0.73 | 23.6x | 45 MiB | same values |
| jvqq, jq's filter | `jvqq -n 'reduce inputs as $x (0; . + 1)' FILE` | 10.07 | 1.7x | 25 MiB | same values |
| qj | `qj -n 'reduce inputs as $x (0; . + 1)' FILE` | 5.51 | 3.1x | 19 MiB | same values |
| jaq | `jaq -n 'reduce inputs as $x (0; . + 1)' FILE` | 11.54 | 1.5x | 1036 MiB | same values |
| gojq | `gojq -n 'reduce inputs as $x (0; . + 1)' FILE` | 23.04 | 0.8x | 12 MiB | same values |

### count by: lines per service

| tool | command | seconds | vs jq | peak memory | output |
|---|---|---:|---:|---:|---|
| jq | `jq -nc 'reduce inputs as $x ({}; .[$x.service] += 1)' FILE` | 29.72 | 1.0x | 6 MiB | reference |
| jvqq | `jvqq -c 'count_by(.service)' FILE` | 0.95 | 31.1x | 48 MiB | same values |
| jvqq, jq's filter | `jvqq -nc 'reduce inputs as $x ({}; .[$x.service] += 1)' FILE` | 12.78 | 2.3x | 25 MiB | same values |
| qj | `qj -nc 'reduce inputs as $x ({}; .[$x.service] += 1)' FILE` | 9.56 | 3.1x | 20 MiB | same values |
| jaq | `jaq -nc 'reduce inputs as $x ({}; .[$x.service] += 1)' FILE` | 14.77 | 2.0x | 1038 MiB | same values |
| gojq | `gojq -nc 'reduce inputs as $x ({}; .[$x.service] += 1)' FILE` | 30.25 | 1.0x | 11 MiB | same values |

### sum by: total latency per service

| tool | command | seconds | vs jq | peak memory | output |
|---|---|---:|---:|---:|---|
| jq | `jq -nc 'reduce inputs as $x ({}; .[$x.service] += $x.latency_ms)' FILE` | 30.83 | 1.0x | 6 MiB | reference |
| jvqq | `jvqq -c 'group_by(.service; {ms: sum(.latency_ms)})' FILE` | 1.18 | 26.2x | 50 MiB | same values |
| jvqq, jq's filter | `jvqq -nc 'reduce inputs as $x ({}; .[$x.service] += $x.latency_ms)' FILE` | 12.96 | 2.4x | 25 MiB | same values |
| qj | `qj -nc 'reduce inputs as $x ({}; .[$x.service] += $x.latency_ms)' FILE` | 10.45 | 3.0x | 19 MiB | same values |
| jaq | `jaq -nc 'reduce inputs as $x ({}; .[$x.service] += $x.latency_ms)' FILE` | 16.40 | 1.9x | 1038 MiB | same values |
| gojq | `gojq -nc 'reduce inputs as $x ({}; .[$x.service] += $x.latency_ms)' FILE` | 30.52 | 1.0x | 12 MiB | same values |

### A larger file: jvqq alone

6.0 GiB, 18,440,091 lines, one run each. The checks compare jvqq's answers with a line count made by the benchmark script.

| workload | command | seconds | peak memory | check |
|---|---|---:|---:|---|
| count | `jvqq count FILE` | 4.36 | 68 MiB | every line counted |
| count by | `jvqq -c 'count_by(.level)' FILE` | 5.72 | 69 MiB | counts add up to the lines |
| select | `jvqq -c 'select(.level == "error") \| .ts' FILE` | 5.39 | 68 MiB | 2303691 lines out |
| reformat | `jvqq -c . FILE` | 14.13 | 62 MiB | one output line per input line |
