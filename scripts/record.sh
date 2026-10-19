#!/bin/sh
# Copyright JVQ.net 2026
# SPDX-License-Identifier: Apache-2.0
#
# Runs every test and writes the results to test/results/, where the numbers
# in the README come from:
#
#   tests.txt         go test -v ./... (the differential test against jq and
#                     the engine tests)
#   race.txt          the same under the race detector
#   release-plan.txt  the release plan's cases
#   smoke.txt         the smoke check on a release-style static build
#   github/           the benchmark recorded on a GitHub-hosted runner
#                     (bench.json, bench.md, COMMIT), copied from the
#                     bench-results branch that .github/workflows/bench.yml
#                     writes
#
# It needs Go and Python 3, and jq 1.7.x on PATH for the differential test
# (without it that test is skipped, and tests.txt says so).
#
# With BENCH_TOOLS set to a folder holding jq, jaq, gojq, qj and peak (see
# scripts/bench.py), it also times them on this machine against a fresh
# build of jvqq, on BENCH_MB MiB of generated logs (default 1024), then runs
# jvqq alone on BENCH_BIG_MB MiB (default 6144; 0 skips it), and writes
# bench.json and bench.md.
#
#   sh scripts/record.sh
set -eu
cd "$(dirname "$0")/.."
out=test/results
mkdir -p "$out"

echo "record: tests"
go test -count=1 -v ./... > "$out/tests.txt" 2>&1
echo "record: race detector"
go test -race -count=1 ./... > "$out/race.txt" 2>&1
echo "record: release plan cases"
python3 -B -m unittest discover -s scripts -p '*_test.py' > "$out/release-plan.txt" 2>&1
echo "record: smoke check"
bin=$(mktemp -d)
CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o "$bin/jvqq" ./cmd/jvqq
python3 scripts/smoke.py "$bin" > "$out/smoke.txt" 2>&1
rm -rf "$bin"

if [ -n "${BENCH_TOOLS:-}" ]; then
  echo "record: benchmark on this machine (this takes a while)"
  CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o "$BENCH_TOOLS/jvqq" ./cmd/jvqq
  data=$(mktemp -d)
  go run test/bench/gen.go -mb "${BENCH_MB:-1024}" -out "$data/logs.jsonl"
  big=""
  if [ "${BENCH_BIG_MB:-6144}" != 0 ]; then
    go run test/bench/gen.go -mb "${BENCH_BIG_MB:-6144}" -seed 2 -out "$data/big.jsonl"
    big="--big $data/big.jsonl"
  fi
  # shellcheck disable=SC2086
  python3 scripts/bench.py --tools "$BENCH_TOOLS" --data "$data/logs.jsonl" $big --out "$out"
  rm -rf "$data"
fi

if git fetch -q origin bench-results 2> /dev/null; then
  mkdir -p "$out/github"
  for f in bench.json bench.md bench-progress.txt COMMIT; do
    if git cat-file -e "FETCH_HEAD:github-ubuntu-latest/$f" 2> /dev/null; then
      git show "FETCH_HEAD:github-ubuntu-latest/$f" > "$out/github/$f"
    fi
  done
  echo "record: copied the GitHub runner recording ($(cut -c1-10 "$out/github/COMMIT"))"
fi
echo "record: done"
