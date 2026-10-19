#!/usr/bin/env python3
# Copyright JVQ.net 2026
# SPDX-License-Identifier: Apache-2.0
#
# The smoke check: runs a built jvqq binary the way a user would and checks
# the answers. It needs only Python 3.
#
#   python3 scripts/smoke.py <directory holding jvqq or jvqq.exe>
#
# The release workflow runs it on every binary it can run on the machine it
# was built for, before anything is published; the test workflow and
# scripts/record.sh run it too.
#
# Checks:
#   - jvqq --version names the Version in cmd/jvqq/version.go
#   - a filter over a file and over standard input, with output in order
#   - gzip input
#   - an aggregation
#   - a malformed line is skipped and counted; --strict stops at it (exit 2)
#   - a filter that doesn't compile exits 3; -e with a false result exits 1
import gzip
import json
import os
import re
import subprocess
import sys
import tempfile

ROOT = os.path.realpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))


def fail(msg):
    print(f"smoke: FAIL: {msg}", file=sys.stderr, flush=True)
    sys.exit(1)


def ok(msg):
    print(f"smoke: ok: {msg}", flush=True)


def main():
    if len(sys.argv) != 2:
        fail("usage: smoke.py <directory holding jvqq>")
    exe = os.path.join(sys.argv[1], "jvqq.exe" if os.name == "nt" else "jvqq")
    if not os.path.exists(exe):
        fail(f"no binary at {exe}")
    exe = os.path.abspath(exe)

    def run(args, stdin=None):
        p = subprocess.run([exe] + args, input=stdin, capture_output=True, timeout=120)
        return p.stdout.decode(), p.stderr.decode(), p.returncode

    with open(os.path.join(ROOT, "cmd", "jvqq", "version.go"), encoding="utf-8") as f:
        want = re.search(r'^const Version = "(.*)"$', f.read(), re.M).group(1)
    out, _, code = run(["--version"])
    if code != 0 or out.strip() != f"jvqq {want}":
        fail(f"--version said {out.strip()!r} (exit {code}), want 'jvqq {want}'")
    ok(f"version {want}")

    tmp = tempfile.mkdtemp(prefix="jvqq-smoke-")
    lines = []
    for i in range(50000):
        level = "error" if i % 7 == 0 else "info"
        lines.append(json.dumps({"i": i, "level": level, "service": ["api", "db", "web"][i % 3], "ms": i % 100},
                                separators=(",", ":")))
    data = ("\n".join(lines) + "\n").encode()
    path = os.path.join(tmp, "logs.jsonl")
    with open(path, "wb") as f:
        f.write(data)
    want_ids = "".join(f"{i}\n" for i in range(0, 50000, 7))

    out, err, code = run(["--workers", "4", "--chunk-size", "4096", 'select(.level == "error") | .i', path])
    if code != 0 or out != want_ids:
        fail(f"select over a file: exit {code}, {len(out)} bytes, stderr {err!r}")
    ok("select over a file, in order on 4 workers")
    out, err, code = run(['select(.level == "error") | .i'], stdin=data)
    if code != 0 or out != want_ids:
        fail(f"select over standard input: exit {code}, stderr {err!r}")
    ok("select over standard input")

    gz = os.path.join(tmp, "logs.jsonl.gz")
    with open(gz, "wb") as f:
        f.write(gzip.compress(data))
    out, err, code = run(["count", gz])
    if code != 0 or out != "50000\n":
        fail(f"count over gzip: {out!r}, exit {code}, stderr {err!r}")
    ok("gzip input")

    out, err, code = run(["-c", "count_by(.service)", path])
    if code != 0 or json.loads(out) != {"api": 16667, "db": 16667, "web": 16666}:
        fail(f"count_by: {out!r}, exit {code}, stderr {err!r}")
    ok("count_by")

    bad = b'{"a":1}\n{"a":\n{"a":2}\n'
    out, err, code = run([".a"], stdin=bad)
    if code != 0 or out != "1\n2\n" or "skipped 1 line" not in err:
        fail(f"malformed line: {out!r}, exit {code}, stderr {err!r}")
    out, err, code = run(["--strict", ".a"], stdin=bad)
    if code != 2 or out != "1\n":
        fail(f"--strict: {out!r}, exit {code}, stderr {err!r}")
    ok("malformed lines skipped and counted; --strict stops")

    _, _, code = run([".a["], stdin=b"1\n")
    if code != 3:
        fail(f"a filter that doesn't compile exited {code}, want 3")
    _, _, code = run(["-e", ".a"], stdin=b'{"a":false}\n')
    if code != 1:
        fail(f"-e with false exited {code}, want 1")
    ok("exit codes")
    print("smoke: all checks passed")


if __name__ == "__main__":
    main()
