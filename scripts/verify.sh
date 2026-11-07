#!/usr/bin/env bash
# Formal verification of the generated code (tacplus#275): Kani for Rust,
# OpenJML ESC for Java (#257). Go is not formally verified, by decision.
#
# Usage:  scripts/verify.sh [rust|java]...      (default: rust java)
#
# Not a gate: it needs the build image's verifiers and far more memory and time
# than a land. The build pipeline runs it (`just verify`) on every release tag,
# and nightly when scripts/verify-changed.sh says something it depends on
# changed since the last verified commit. Run from the repository checkout, in
# the build image (images/build.Dockerfile).
#
# For each language the base fixture (cross-test/proto) is generated with the
# checkout's tacplus, and `tacplus verify` runs every harness generated into it.
# For Java those are the JML specs on the generated validators, which ESC proves
# in the Maven module that compiles them: a compile pom is written beside the
# generated code (cross-test/lib/java-compile.sh).
# Each language runs even when another failed; a failure prints the language's
# verification-report.md (each harness, its status and its counterexample).
#
#   TACPLUS_BIN          an existing tacplus binary (default: built from the checkout)
#   TACPLUS_VERIFY_WORK  where the fixtures are generated (default: target/verify)
#
# Its inputs are listed in scripts/verify-changed.sh: keep them in step.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="${TACPLUS_VERIFY_WORK:-$ROOT/target/verify}"
PROTO_SRC="$ROOT/cross-test/proto"
languages=("$@")
[[ ${#languages[@]} -gt 0 ]] || languages=(rust java)
for l in "${languages[@]}"; do
  case "$l" in
    rust | java) ;;
    *)
      echo "verify: no formal verification for $l (rust, java; Go is not formally verified, by decision)" >&2
      exit 2
      ;;
  esac
done

TACPLUS_BIN="${TACPLUS_BIN:-}"
if [[ -z "$TACPLUS_BIN" ]]; then
  TACPLUS_BIN="$ROOT/target/tacplus-verify"
  echo "==> Building tacplus from $ROOT"
  (cd "$ROOT" && go generate ./pkg/internal/proto/spec/... && go build -o "$TACPLUS_BIN" .) || {
    echo "verify: building tacplus failed" >&2
    exit 1
  }
fi

fail=0
for l in "${languages[@]}"; do
  dir="$WORK/$l"
  rm -rf "$dir"
  mkdir -p "$dir"
  cat >"$dir/tacplus.yaml" <<EOF
language: "$l"
pkg: "com.gitlab.example.dummy.proto"
src: "$PROTO_SRC"
output-dir: "tacplus"
EOF
  echo "=== verify $l ==="
  echo "==> Generating cross-test/proto ($l)"
  if ! (cd "$dir" && "$TACPLUS_BIN" build --config tacplus.yaml --src "$PROTO_SRC" >generate.log 2>&1); then
    tail -80 "$dir/generate.log" >&2
    echo "=== verify $l: FAIL (generation; log: $dir/generate.log) ===" >&2
    fail=1
    continue
  fi
  if [[ "$l" == java ]]; then
    # shellcheck source=../cross-test/lib/java-compile.sh
    source "$ROOT/cross-test/lib/java-compile.sh"
    write_java_compile_pom "$dir/pom.xml" "$dir/tacplus"
  fi
  echo "==> tacplus verify ($l)"
  (cd "$dir" && "$TACPLUS_BIN" verify) 2>&1 | tee "$dir/verify.log"
  rc=${PIPESTATUS[0]}
  if [[ $rc -ne 0 ]]; then
    [[ -f "$dir/verification-report.md" ]] && cat "$dir/verification-report.md"
    echo "=== verify $l: FAIL (rc=$rc; report: $dir/verification-report.md) ===" >&2
    fail=1
  elif [[ ! -f "$dir/verification-report.md" ]]; then
    # tacplus verify passes when it finds no harness; verification that
    # proved nothing is a failure here.
    echo "=== verify $l: FAIL (no harness verified) ===" >&2
    fail=1
  else
    echo "=== verify $l: OK (report: $dir/verification-report.md) ==="
  fi
done
echo "=== verify OVERALL fail=$fail ==="
exit $fail
