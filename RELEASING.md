# Releasing jvqq

Releases go out on their own. Nobody clicks Publish. This file is the record of
how that works, for whoever (person or Claude session) works on this repo next.

## The short version

1. Raise `const Version = "X.Y.Z"` in `cmd/jvqq/version.go`.
2. Write what changed into the commit message.
3. Push the commit to `main`.

When the `test` workflow passes on that commit (the full suite and the smoke
check on Linux, macOS and Windows), the `release` workflow builds the binaries,
smoke-tests them, then tags the commit `vX.Y.Z` and publishes the release
"jvqq vX.Y.Z" with the archives and SHA256SUMS attached. If any build or check
fails, nothing is tagged and nothing goes public. Fix it, push again, and the
next green run picks it up.

Pushing without raising the version is fine: the release workflow sees the tag
already exists and stops quietly.

## What it does, step by step

[.github/workflows/release.yml](.github/workflows/release.yml) starts when a
`test` run finishes; the push alone doesn't start it.

- **plan.** Goes on only if the `test` run passed, came from a push to `main` in
  this repo, and `main` still points at that commit (if `main` moved on, the run
  for the newer commit decides). Reads `Version` from `cmd/jvqq/version.go`.
  Stops quietly if `vX.Y.Z` is already a tag. Fails loudly if the version is
  lower than the newest tag. [scripts/release-plan.py](scripts/release-plan.py)
  has the rules, and [scripts/release_plan_test.py](scripts/release_plan_test.py)
  its cases.
- **build.** jvqq is pure Go, so one Linux job cross-builds all six binaries
  without cgo (`-trimpath -ldflags '-s -w'`), checks each one's architecture
  with `file` and that the Linux ones are static, and packages them.
- **check.** Runs [scripts/smoke.py](scripts/smoke.py) on each binary its
  runner can run: Linux x86-64, Linux ARM64, macOS Apple silicon and Windows
  x86-64, each on its own kind of machine. The macOS Intel and Windows ARM64
  builds are checked for their architecture only.
- **release.** Runs only after the build and every check passed. Writes
  SHA256SUMS and the notes, then creates the tag and the release in one step
  with `gh release create --target <commit>`, on the repo's own `GITHUB_TOKEN`
  (`contents: write` on this job only). A `concurrency` group per tag means two
  runs for the same version can't publish twice.

The smoke check, on each binary: `jvqq --version` names the version in the
source; a `select` over a 50,000-line file on four workers gives every
expected line in order, and the same over standard input; a gzip file is
read; `count_by` gives the right counts; a malformed line is skipped and
counted, and `--strict` stops at it with exit 2; a filter that doesn't
compile exits 3, and `-e` with a false result exits 1.

## Files that go out

| System | File |
|---|---|
| Linux, x86-64 (static) | jvqq_linux_amd64.tar.gz |
| Linux, ARM64 (static) | jvqq_linux_arm64.tar.gz |
| macOS, Apple silicon | jvqq_darwin_arm64.tar.gz |
| macOS, Intel | jvqq_darwin_amd64.tar.gz |
| Windows, x86-64 | jvqq_windows_amd64.zip |
| Windows, ARM64 | jvqq_windows_arm64.zip |
| Checksums | SHA256SUMS |

Each archive holds `jvqq` (or `jvqq.exe`), LICENSE, NOTICE,
THIRD_PARTY_LICENSES.md and README.md. Archive names carry no version on
purpose: links like
`https://github.com/just-very-quickly/query/releases/latest/download/jvqq_linux_amd64.tar.gz`
keep working from one release to the next, so the README never needs editing
for a release.

## After a release

A Claude session can't create tags or releases itself, which is why the workflow
does it on the repository's token. After pushing a raised version, check:

1. The `test` run and then the `release` run finished green. If the
   repository is public, `https://api.github.com/repos/just-very-quickly/query/actions/runs`
   shows them without a token.
2. The release exists with all seven files
   (`https://api.github.com/repos/just-very-quickly/query/releases/latest`).
3. The `latest/download` links resolve, and a downloaded archive's SHA256 matches
   SHA256SUMS.
4. A downloaded Linux binary runs: `jvqq --version` names the release.
5. When the change touched the engine, the `bench` workflow ran and pushed
   its results to the `bench-results` branch; `sh scripts/record.sh` copies
   them into test/results/github/, and the README's numbers follow them.

## By hand

- A release published by hand gets the binaries attached by the same workflow.
- To rebuild the files of an existing tag, run the `release` workflow by hand
  with the tag.
