#!/usr/bin/env bash
# Decide whether the nightly formal verification runs (tacplus#275).
#
# Usage:  scripts/verify-changed.sh <last verified sha>
#
# Run from the repository checkout. Prints `run` when something verification
# depends on changed since <sha> (committed, uncommitted or untracked), and
# `skip: unchanged since <sha>, verified then` when nothing did. When in doubt
# (no sha, a sha this history lacks, not a git checkout, git failing) it prints
# `run`, with the reason on stderr. It always exits 0: the output is the answer.
#
# A release tag runs `just verify` without asking this.
set -uo pipefail

# What scripts/verify.sh verifies and with what. A path added to its inputs is
# added here too (scripts/verify_test.go, TestVerifyChanged).
depends=(
  pkg/                      # the generator, its templates and registries, the embedded protos, the verifier driver
  cmd/verify/               # `tacplus verify`
  main.go go.mod go.sum     # the compiler itself
  cross-test/proto/         # the fixture verification generates
  cross-test/dependencies.yaml
  images/base.Dockerfile    # the toolchain pins: the SDKs,
  images/build.Dockerfile   # Kani and OpenJML
  cross-test/lib/java-compile.sh  # the pom the Java fixture compiles under
  scripts/verify.sh scripts/verify-changed.sh
)

run() {
  echo "verify-changed: $1: run" >&2
  echo run
  exit 0
}

[[ $# -eq 1 && -n "$1" ]] || run "no last verified commit given"
sha="$1"
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || run "not a git checkout"
git cat-file -e "${sha}^{commit}" 2>/dev/null || run "$sha is not a commit of this history"

changed="$(git diff --name-only "$sha" -- "${depends[@]}" 2>/dev/null)" || run "git diff against $sha failed"
untracked="$(git ls-files --others --exclude-standard -- "${depends[@]}" 2>/dev/null)" || run "git ls-files failed"
changed+="${untracked:+$'\n'$untracked}"
if [[ -n "${changed//$'\n'/}" ]]; then
  echo "verify-changed: changed since $sha:" >&2
  sed 's/^/  /' <<<"$changed" | grep -v '^  $' | head -20 >&2
  echo run
  exit 0
fi
echo "skip: unchanged since $sha, verified then"
