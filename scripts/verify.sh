#!/usr/bin/env bash
# Formal verification of the generated code (tacplus#275): Kani for Rust. Java
# has none until its validators can be proved (tacplus#257); Go is not formally
# verified, by decision.
#
# Usage:  scripts/verify.sh [rust]...      (default: rust)
#
# Not a gate: it needs the build image's verifiers and far more memory and time
# than a land. The build pipeline runs it (`just verify`) on every release tag,
# and nightly when scripts/verify-changed.sh says something it depends on
# changed since the last verified commit. Run from the repository checkout, in
# the build image (images/build.Dockerfile).
#
# tacplus generates no proof harness into a service (tacplus#196): the
# generator is verified here, once, on the kitchensink. For each language the
# kitchensink's own member (cross-test/kitchensink/proto) is generated with the
# checkout's tacplus, and `tacplus verify` runs the kitchensink's harnesses
# (cross-test/kitchensink/verify/<language>) over it. Each language runs even
# when another failed; a failure prints the language's verification-report.md
# (each harness, its status and its counterexample).
#
#   TACPLUS_BIN          an existing tacplus binary (default: built from the checkout)
#   TACPLUS_VERIFY_WORK  where the kitchensink is generated (default: target/verify)
#
# Its inputs are listed in scripts/verify-changed.sh: keep them in step.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="${TACPLUS_VERIFY_WORK:-$ROOT/target/verify}"
KS="$ROOT/cross-test/kitchensink"
languages=("$@")
[[ ${#languages[@]} -gt 0 ]] || languages=(rust)
for l in "${languages[@]}"; do
  case "$l" in
    rust) ;;
    java)
      echo "verify: no formal verification for java yet: JBMC cannot model the protobuf-java runtime its validators run on (tacplus#257)" >&2
      exit 2
      ;;
    *)
      echo "verify: no formal verification for $l (rust; Go is not formally verified, by decision)" >&2
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
  # The harness crate names the generated model by a relative path,
  # $dir/verify/proof -> $dir/gen/model (cross-test/kitchensink/verify/rust/Cargo.toml).
  gen="$dir/gen"
  proj="$dir/verify"
  mkdir -p "$gen" "$proj"
  cp -R "$KS/proto" "$gen/proto"
  cat >"$gen/tacplus.yaml" <<EOF
language: "$l"
pkg: "com.example.kitchensink"
src: "proto"
output-dir: "model"
EOF
  echo "=== verify $l ==="
  echo "==> Generating cross-test/kitchensink/proto ($l)"
  if ! (cd "$gen" && "$TACPLUS_BIN" build --config tacplus.yaml --src proto >generate.log 2>&1); then
    tail -80 "$gen/generate.log" >&2
    echo "=== verify $l: FAIL (generation; log: $gen/generate.log) ===" >&2
    fail=1
    continue
  fi
  cp -R "$KS/verify/$l" "$proj/proof"
  printf 'language: "%s"\noutput-dir: "model"\n' "$l" >"$proj/tacplus.yaml"
  echo "==> tacplus verify ($l): cross-test/kitchensink/verify/$l"
  # Kani builds with its own compiler: the harness crate gets a target of its own.
  (cd "$proj" && CARGO_TARGET_DIR="$proj/target" "$TACPLUS_BIN" verify) 2>&1 | tee "$dir/verify.log"
  rc=${PIPESTATUS[0]}
  report="$proj/verification-report.md"
  if [[ $rc -ne 0 ]]; then
    [[ -f "$report" ]] && cat "$report"
    echo "=== verify $l: FAIL (rc=$rc; report: $report) ===" >&2
    fail=1
  elif [[ ! -f "$report" ]]; then
    # tacplus verify passes when it finds no harness; verification that
    # proved nothing is a failure here.
    echo "=== verify $l: FAIL (no harness verified) ===" >&2
    fail=1
  else
    echo "=== verify $l: OK (report: $report) ==="
  fi
done
echo "=== verify OVERALL fail=$fail ==="
exit $fail
