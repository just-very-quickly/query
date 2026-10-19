#!/usr/bin/env python3
# Copyright JVQ.net 2026
# SPDX-License-Identifier: Apache-2.0
"""Times jvqq against jq, jaq, gojq and qj on a large JSON Lines file.

    python3 scripts/bench.py --tools DIR --data FILE --out DIR

DIR holds the binaries: jvqq, and any of jq, jaq, gojq, qj (missing ones
are left out), and peak, built from test/bench/peak.go, which runs each
command and measures it. FILE is made by test/bench/gen.go. Each command
runs once to warm the file cache, then --runs times; the median wall time
and the largest peak memory are recorded. Every output is checked against jq's:
the same bytes, the same values printed differently (gojq sorts keys, for
instance), or different. Writes bench.json and bench.md to the out
directory.

Each tool runs the filter a jq user would write. Counting and grouping
have no jq builtin that streams, so jq, jaq, gojq and qj run the usual
reduce over inputs, and jvqq runs both its own aggregation and that same
reduce (which reads input in order, on one core).
"""
import argparse
import hashlib
import json
import math
import os
import platform
import statistics
import subprocess
import sys
import time

STREAM = "stream"  # one output per input line: compared as bytes, then as values

WORKLOADS = [
    # name, what it does, kind, {tool: args}; "*" is the default for every tool
    ("reformat", "print every line compactly", STREAM,
     {"*": ["-c", "."]}),
    ("select", "keep the error lines", STREAM,
     {"*": ["-c", 'select(.level == "error")']}),
    ("field", "one nested field per line", STREAM,
     {"*": [".user.id"]}),
    ("project", "a smaller object per line", STREAM,
     {"*": ["-c", "{ts, service, ms: .latency_ms}"]}),
    ("count", "count the lines", "count",
     {"*": ["-n", "reduce inputs as $x (0; . + 1)"],
      "jvqq": ["count"]}),
    ("count by", "lines per service", "count_by",
     {"*": ["-nc", "reduce inputs as $x ({}; .[$x.service] += 1)"],
      "jvqq": ["-c", "count_by(.service)"]}),
    ("sum by", "total latency per service", "sum_by",
     {"*": ["-nc", "reduce inputs as $x ({}; .[$x.service] += $x.latency_ms)"],
      "jvqq": ["-c", "group_by(.service; {ms: sum(.latency_ms)})"]}),
]

TOOLS = ["jvqq", "qj", "jaq", "gojq", "jq"]


def version(path):
    for flag in ("--version", "-V"):
        try:
            out = subprocess.run([path, flag], capture_output=True, text=True, timeout=30)
        except OSError:
            return None
        if out.returncode == 0 and out.stdout.strip():
            v = out.stdout.strip().splitlines()[0]
            name = os.path.basename(path)
            for prefix in (name + " ", name + "-"):
                if v.startswith(prefix):
                    v = v[len(prefix):]
            return v
    return "unknown"


def run_once(peak_bin, cmd, out_path):
    """Runs cmd with stdout to out_path; returns (seconds, peak MiB, exit, stderr)."""
    with open(out_path, "wb") as out:
        p = subprocess.run([peak_bin] + cmd, stdout=out, stderr=subprocess.PIPE)
    err = p.stderr.decode(errors="replace").splitlines()
    wall, kib = 0.0, 0
    if err and err[-1].startswith("peak: "):
        _, w, k = err.pop().split()
        wall, kib = float(w), int(k)
    return wall, kib / 1024, p.returncode, "\n".join(err)


def digest(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        while True:
            b = f.read(1 << 20)
            if not b:
                break
            h.update(b)
    return h.hexdigest()


def close(a, b):
    if isinstance(a, (int, float)) and isinstance(b, (int, float)):
        return math.isclose(a, b, rel_tol=1e-9, abs_tol=1e-9)
    if isinstance(a, dict) and isinstance(b, dict):
        return a.keys() == b.keys() and all(close(a[k], b[k]) for k in a)
    if isinstance(a, list) and isinstance(b, list):
        return len(a) == len(b) and all(close(x, y) for x, y in zip(a, b))
    return a == b


def values(path):
    """The answer in the output, in one shape for every tool: jvqq's
    group_by rows become the object the reduce builds."""
    with open(path) as f:
        v = json.load(f)
    if isinstance(v, list) and all(isinstance(r, dict) and "key" in r for r in v):
        v = {r["key"]: r["ms"] for r in v}
    return v


def same_stream(path, ref):
    """Whether two outputs hold the same values line by line."""
    with open(path) as a, open(ref) as b:
        for x, y in zip(a, b):
            if x != y and not close(json.loads(x), json.loads(y)):
                return False
        return a.readline() == "" and b.readline() == ""


def compare(kind, path, ref_path, ref_digest):
    if ref_path is None:
        return "no jq output to compare"
    if kind == STREAM:
        if digest(path) == ref_digest:
            return "same bytes"
        return "same values" if same_stream(path, ref_path) else "DIFFERENT"
    return "same values" if close(values(path), values(ref_path)) else "DIFFERENT"


def cpu_model():
    try:
        with open("/proc/cpuinfo") as f:
            for line in f:
                if line.startswith("model name"):
                    return line.split(":", 1)[1].strip()
    except OSError:
        pass
    try:
        return subprocess.run(["sysctl", "-n", "machdep.cpu.brand_string"], capture_output=True, text=True).stdout.strip()
    except OSError:
        return platform.processor()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--tools", required=True)
    ap.add_argument("--data", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--runs", type=int, default=3)
    ap.add_argument("--only", help="comma-separated workload names")
    args = ap.parse_args()
    os.makedirs(args.out, exist_ok=True)
    scratch = os.path.join(args.out, "scratch")
    os.makedirs(scratch, exist_ok=True)

    peak_bin = os.path.join(args.tools, "peak")
    if not os.access(peak_bin, os.X_OK):
        sys.exit("bench: no peak in " + args.tools + " (go build -o DIR/peak test/bench/peak.go)")
    tools = {}
    for t in TOOLS:
        p = os.path.join(args.tools, t)
        if os.access(p, os.X_OK):
            tools[t] = {"path": p, "version": version(p)}
    if "jvqq" not in tools:
        sys.exit("bench: no jvqq in " + args.tools)
    with open(args.data, "rb") as f:
        lines = sum(chunk.count(b"\n") for chunk in iter(lambda: f.read(1 << 24), b""))
    env = {
        "os": platform.platform(),
        "cpu": cpu_model(),
        "cores": os.cpu_count(),
        "file_bytes": os.path.getsize(args.data),
        "file_lines": lines,
        "runs": args.runs,
        "tools": {t: v["version"] for t, v in tools.items()},
    }
    print(f"bench: {env['file_bytes'] >> 20} MiB, {lines} lines, {env['cores']} cores, {env['cpu']}", file=sys.stderr)

    results = []
    only = set(args.only.split(",")) if args.only else None
    for name, what, kind, cmds in WORKLOADS:
        if only and name not in only:
            continue
        variants = []
        for t in ["jq"] + [t for t in TOOLS if t != "jq"]:  # jq first: the others are checked against it
            if t not in tools:
                continue
            a = cmds.get(t, cmds["*"])
            variants.append((t, t, a))
            if t == "jvqq":
                if a != cmds["*"]:
                    variants.append(("jvqq", "jvqq, jq's filter", cmds["*"]))
                else:
                    variants.append(("jvqq", "jvqq, 1 core", ["--workers", "1"] + a))
        ref_path = ref_digest = None
        for tool, label, a in variants:
            cmd = [tools[tool]["path"]] + a + [args.data]
            out_path = os.path.join(scratch, f"{name.replace(' ', '_')}-{label.replace(' ', '_').replace(',', '').replace(chr(39), '')}.out")
            times, peak, code, err = [], 0.0, 0, ""
            for i in range(args.runs + 1):
                wall, rss, code, err = run_once(peak_bin, cmd, out_path)
                if code != 0:
                    break
                if i > 0:  # the first run warms the file cache
                    times.append(wall)
                peak = max(peak, rss)
            row = {"workload": name, "what": what, "tool": label, "command": [tool] + a + ["FILE"]}
            if code != 0:
                row.update({"error": f"exit {code}: {err.strip()[:300]}"})
                print(f"bench: {name:9} {label:18} failed: exit {code}", file=sys.stderr)
            else:
                if tool == "jq" and label == "jq":
                    ref_path, ref_digest = out_path, digest(out_path)
                    check = "reference"
                else:
                    check = compare(kind, out_path, ref_path, ref_digest)
                row.update({
                    "seconds": round(statistics.median(times), 3),
                    "all_seconds": [round(x, 3) for x in times],
                    "peak_mib": round(peak, 1),
                    "output_bytes": os.path.getsize(out_path),
                    "output": check,
                })
                print(f"bench: {name:9} {label:18} {row['seconds']:8.3f}s {peak:7.0f} MiB  {check}", file=sys.stderr)
            results.append(row)
        for tool, label, a in variants:
            p = os.path.join(scratch, f"{name.replace(' ', '_')}-{label.replace(' ', '_').replace(',', '').replace(chr(39), '')}.out")
            if os.path.exists(p) and p != ref_path:
                os.remove(p)
        if ref_path and os.path.exists(ref_path):
            os.remove(ref_path)

    with open(os.path.join(args.out, "bench.json"), "w") as f:
        json.dump({"environment": env, "results": results}, f, indent=2)
        f.write("\n")
    with open(os.path.join(args.out, "bench.md"), "w") as f:
        f.write(markdown(env, results))
    os.rmdir(scratch)


def shell_quote(a):
    if a and all(c.isalnum() or c in "-_./=" for c in a):
        return a
    return "'" + a.replace("'", "'\\''") + "'"


def markdown(env, results):
    out = []
    out.append(f"File: {env['file_bytes'] / (1 << 20):.0f} MiB of JSON Lines, {env['file_lines']:,} lines "
               f"(test/bench/gen.go). Machine: {env['cpu']}, {env['cores']} cores, {env['os']}. "
               f"Each time is the median of {env['runs']} runs, after one run to warm the file cache; "
               f"memory is the peak resident size. \"vs jq\" is how many times faster than jq.\n")
    out.append("Versions: " + ", ".join(f"{t} {v}" for t, v in env["tools"].items()) + ".\n")
    by = {}
    for r in results:
        by.setdefault(r["workload"], []).append(r)
    for name, rows in by.items():
        jq = next((r for r in rows if r["tool"] == "jq" and "seconds" in r), None)
        out.append(f"\n### {name}: {rows[0]['what']}\n")
        out.append("\n| tool | command | seconds | vs jq | peak memory | output |\n|---|---|---:|---:|---:|---|\n")
        for r in rows:
            c = " ".join(shell_quote(x) for x in r["command"]).replace("|", "\\|")
            if "error" in r:
                out.append(f"| {r['tool']} | `{c}` | failed | | | {r['error'].splitlines()[0][:80]} |\n")
                continue
            speed = f"{jq['seconds'] / r['seconds']:.1f}x" if jq and r["seconds"] > 0 else ""
            out.append(f"| {r['tool']} | `{c}` | {r['seconds']:.2f} | {speed} | {r['peak_mib']:.0f} MiB | {r['output']} |\n")
    return "".join(out)


if __name__ == "__main__":
    main()
