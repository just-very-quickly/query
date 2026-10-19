File: 1024 MiB of JSON Lines, 3,073,492 lines (test/bench/gen.go). Machine: Intel(R) Xeon(R) Processor @ 2.80GHz, 2 cores, Linux-6.18.44-fc-v139-x86_64-with-glibc2.39. Each time is the median of 3 runs, after one run to warm the file cache; memory is the peak resident size. "vs jq" is how many times faster than jq.
Versions: jvqq 0.1.0, qj 0.3.0, jaq 3.1.1, gojq 0.12.17 (rev: f4c2cfe/go1.23.3), jq 1.7.1.

### reformat: print every line compactly

| tool | command | seconds | vs jq | peak memory | output |
|---|---|---:|---:|---:|---|
| jq | `jq -c . FILE` | 67.47 | 1.0x | 2 MiB | reference |
| jvqq | `jvqq -c . FILE` | 3.34 | 20.2x | 23 MiB | same bytes |
| jvqq, 1 core | `jvqq --workers 1 -c . FILE` | 5.66 | 11.9x | 19 MiB | same bytes |
| qj | `qj -c . FILE` | 3.90 | 17.3x | 37 MiB | same bytes |
| jaq | `jaq -c . FILE` | 37.54 | 1.8x | 1036 MiB | same bytes |
| gojq | `gojq -c . FILE` | 78.60 | 0.9x | 10 MiB | same values |

### select: keep the error lines

| tool | command | seconds | vs jq | peak memory | output |
|---|---|---:|---:|---:|---|
| jq | `jq -c 'select(.level == "error")' FILE` | 38.69 | 1.0x | 2 MiB | reference |
| jvqq | `jvqq -c 'select(.level == "error")' FILE` | 2.36 | 16.4x | 30 MiB | same bytes |
| jvqq, 1 core | `jvqq --workers 1 -c 'select(.level == "error")' FILE` | 4.09 | 9.5x | 22 MiB | same bytes |
| qj | `qj -c 'select(.level == "error")' FILE` | 2.09 | 18.5x | 37 MiB | same bytes |
| jaq | `jaq -c 'select(.level == "error")' FILE` | 23.92 | 1.6x | 1036 MiB | same bytes |
| gojq | `gojq -c 'select(.level == "error")' FILE` | 56.93 | 0.7x | 11 MiB | same values |

### field: one nested field per line

| tool | command | seconds | vs jq | peak memory | output |
|---|---|---:|---:|---:|---|
| jq | `jq .user.id FILE` | 33.09 | 1.0x | 2 MiB | reference |
| jvqq | `jvqq .user.id FILE` | 1.89 | 17.5x | 28 MiB | same bytes |
| jvqq, 1 core | `jvqq --workers 1 .user.id FILE` | 3.87 | 8.5x | 23 MiB | same bytes |
| qj | `qj .user.id FILE` | 1.80 | 18.4x | 30 MiB | same bytes |
| jaq | `jaq .user.id FILE` | 20.88 | 1.6x | 1036 MiB | same bytes |
| gojq | `gojq .user.id FILE` | 55.12 | 0.6x | 11 MiB | same bytes |

### project: a smaller object per line

| tool | command | seconds | vs jq | peak memory | output |
|---|---|---:|---:|---:|---|
| jq | `jq -c '{ts, service, ms: .latency_ms}' FILE` | 42.25 | 1.0x | 2 MiB | reference |
| jvqq | `jvqq -c '{ts, service, ms: .latency_ms}' FILE` | 2.83 | 15.0x | 32 MiB | same bytes |
| jvqq, 1 core | `jvqq --workers 1 -c '{ts, service, ms: .latency_ms}' FILE` | 5.31 | 8.0x | 22 MiB | same bytes |
| qj | `qj -c '{ts, service, ms: .latency_ms}' FILE` | 3.01 | 14.0x | 32 MiB | same bytes |
| jaq | `jaq -c '{ts, service, ms: .latency_ms}' FILE` | 33.05 | 1.3x | 1036 MiB | same bytes |
| gojq | `gojq -c '{ts, service, ms: .latency_ms}' FILE` | 62.18 | 0.7x | 11 MiB | same values |

### count: count the lines

| tool | command | seconds | vs jq | peak memory | output |
|---|---|---:|---:|---:|---|
| jq | `jq -n 'reduce inputs as $x (0; . + 1)' FILE` | 30.39 | 1.0x | 2 MiB | reference |
| jvqq | `jvqq count FILE` | 2.11 | 14.4x | 25 MiB | same values |
| jvqq, jq's filter | `jvqq -n 'reduce inputs as $x (0; . + 1)' FILE` | 22.05 | 1.4x | 22 MiB | same values |
| qj | `qj -n 'reduce inputs as $x (0; . + 1)' FILE` | 6.99 | 4.3x | 18 MiB | same values |
| jaq | `jaq -n 'reduce inputs as $x (0; . + 1)' FILE` | 14.76 | 2.1x | 1036 MiB | same values |
| gojq | `gojq -n 'reduce inputs as $x (0; . + 1)' FILE` | 40.42 | 0.8x | 10 MiB | same values |

### count by: lines per service

| tool | command | seconds | vs jq | peak memory | output |
|---|---|---:|---:|---:|---|
| jq | `jq -nc 'reduce inputs as $x ({}; .[$x.service] += 1)' FILE` | 49.72 | 1.0x | 2 MiB | reference |
| jvqq | `jvqq -c 'count_by(.service)' FILE` | 2.13 | 23.3x | 28 MiB | same values |
| jvqq, jq's filter | `jvqq -nc 'reduce inputs as $x ({}; .[$x.service] += 1)' FILE` | 27.83 | 1.8x | 20 MiB | same values |
| qj | `qj -nc 'reduce inputs as $x ({}; .[$x.service] += 1)' FILE` | 11.85 | 4.2x | 18 MiB | same values |
| jaq | `jaq -nc 'reduce inputs as $x ({}; .[$x.service] += 1)' FILE` | 17.98 | 2.8x | 1036 MiB | same values |
| gojq | `gojq -nc 'reduce inputs as $x ({}; .[$x.service] += 1)' FILE` | 53.64 | 0.9x | 11 MiB | same values |

### sum by: total latency per service

| tool | command | seconds | vs jq | peak memory | output |
|---|---|---:|---:|---:|---|
| jq | `jq -nc 'reduce inputs as $x ({}; .[$x.service] += $x.latency_ms)' FILE` | 52.67 | 1.0x | 2 MiB | reference |
| jvqq | `jvqq -c 'group_by(.service; {ms: sum(.latency_ms)})' FILE` | 3.00 | 17.6x | 29 MiB | same values |
| jvqq, jq's filter | `jvqq -nc 'reduce inputs as $x ({}; .[$x.service] += $x.latency_ms)' FILE` | 28.43 | 1.9x | 19 MiB | same values |
| qj | `qj -nc 'reduce inputs as $x ({}; .[$x.service] += $x.latency_ms)' FILE` | 11.83 | 4.5x | 18 MiB | same values |
| jaq | `jaq -nc 'reduce inputs as $x ({}; .[$x.service] += $x.latency_ms)' FILE` | 21.30 | 2.5x | 1036 MiB | same values |
| gojq | `gojq -nc 'reduce inputs as $x ({}; .[$x.service] += $x.latency_ms)' FILE` | 55.59 | 0.9x | 10 MiB | same values |

### A larger file: jvqq alone

6.0 GiB, 18,440,091 lines, one run each. The checks compare jvqq's answers with a line count made by the benchmark script.

| workload | command | seconds | peak memory | check |
|---|---|---:|---:|---|
| count | `jvqq count FILE` | 17.80 | 46 MiB | every line counted |
| count by | `jvqq -c 'count_by(.level)' FILE` | 22.38 | 48 MiB | counts add up to the lines |
| select | `jvqq -c 'select(.level == "error") \| .ts' FILE` | 19.96 | 49 MiB | 2303691 lines out |
| reformat | `jvqq -c . FILE` | 35.37 | 43 MiB | one output line per input line |
