#!/usr/bin/env bash
# Copyright (c) 2026 Second Star Cavalier LLC
#
# Wire-level gate for claims PROVENANCE in the generated Java chassis (F11).
#
# Identity claims must come from a token the Authorizer VERIFIED, never from a
# base64 decode of whatever bearer the caller sent. The decode proves only that
# the caller can write base64, and on an RPC that never verifies anything
# (auth_disabled / RPC_FLAG_PUBLIC_NO_AUTH / RPC_FLAG_SELF_AUTHENTICATES) it is
# pure attacker input. The RED-1 egress sanitizer reads exactly those claims to
# decide how much of a record to reveal.
#
# This gate generates the consumer-contract fixture with ONE extra method — a
# no-auth unary read returning a record with a (spec.redact) MAY_REDACT field —
# hosts the real generated wrapper on a real gRPC socket, and drives it with the
# real generated client stub and real Ed25519-signed tokens. Nothing under test
# is mocked.
#
# The server wiring mirrors production: the chassis GrpcServer installs
# Authorizer.interceptor(), and platform/services/kernel's ClusterServer
# additionally installs Auth.requestIdInterceptor(). Both are installed here.
#
# Needs the go and java (maven) toolchains. Not part of `just test`.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GATE_DIR="$ROOT_DIR/cross-test/claims-provenance"
PROTO_SRC="$ROOT_DIR/cross-test/consumer-contract/proto"
WORK_ROOT="${TACPLUS_CLAIMS_PROVENANCE_WORKDIR:-}"
TACPLUS_BIN="${TACPLUS_BIN:-}"
KEEP_WORK="${TACPLUS_CLAIMS_PROVENANCE_KEEP:-false}"

usage() {
  cat <<'USAGE'
Usage:
  cross-test/claims-provenance/run.sh [options]

Options:
  --workdir DIR     Use a specific temporary work directory
  --tacplus BIN     Use an existing tacplus binary
  --keep            Keep the temporary work directory
  -h, --help        Show this help
USAGE
}

die() {
  echo "ERROR: $*" >&2
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --workdir) WORK_ROOT="${2:?--workdir requires a value}"; shift 2 ;;
    --tacplus) TACPLUS_BIN="${2:?--tacplus requires a value}"; shift 2 ;;
    --keep)    KEEP_WORK=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *)         die "unknown argument: $1" ;;
  esac
done

command -v mvn >/dev/null || die "mvn is required"

if [[ -z "$WORK_ROOT" ]]; then
  WORK_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/tacplus-claims-provenance.XXXXXX")"
else
  mkdir -p "$WORK_ROOT"
  WORK_ROOT="$(cd "$WORK_ROOT" && pwd)"
fi
if [[ "$KEEP_WORK" != true ]]; then
  trap 'rm -rf "$WORK_ROOT"' EXIT
fi

# A stale generator binary silently invalidates the whole demonstration, so
# build one from THIS checkout unless the caller named one explicitly.
if [[ -z "$TACPLUS_BIN" ]]; then
  TACPLUS_BIN="$WORK_ROOT/tacplus-local"
  (cd "$ROOT_DIR" && go build -o "$TACPLUS_BIN" .)
fi
TACPLUS_BIN="$(cd "$(dirname "$TACPLUS_BIN")" && pwd)/$(basename "$TACPLUS_BIN")"
echo "==> tacplus binary: $TACPLUS_BIN"

# ---- proto: the canonical fixture tree plus one no-auth redactable read ----
PROTO_DIR="$WORK_ROOT/proto"
rm -rf "$PROTO_DIR"
mkdir -p "$PROTO_DIR"
cp -R "$PROTO_SRC/." "$PROTO_DIR/"
CONTRACT="$PROTO_DIR/contract/contract.proto"
grep -q '^  rpc ListEvents(' "$CONTRACT" || die "contract fixture no longer declares ListEvents; overlay anchor is gone"
awk -v overlay="$GATE_DIR/proto-overlay.proto" '
  /^  rpc ListEvents\(/ && !done {
    while ((getline line < overlay) > 0) print line
    done = 1
  }
  { print }
' "$CONTRACT" > "$CONTRACT.new"
mv "$CONTRACT.new" "$CONTRACT"
grep -q 'rpc GetPublicEvent' "$CONTRACT" || die "overlay was not applied"

# ---- generate ----
OUTPUT="$WORK_ROOT/model"
cat > "$WORK_ROOT/tacplus.yaml" <<EOF
language: "java"
pkg: "com.gitlab.example.tacplus.consumer.contractclient"
src: "$PROTO_DIR"
output-dir: "model"
EOF
echo "==> generating java"
(cd "$WORK_ROOT" && "$TACPLUS_BIN" build --config tacplus.yaml --src "$PROTO_DIR" --tag latest) \
  >"$WORK_ROOT/generate.log" 2>&1 || { cat "$WORK_ROOT/generate.log" >&2; die "generation failed"; }
[[ -d "$OUTPUT/src/main/java" ]] || die "generator produced no java sources"

# ---- compile the generated tree ----
COMPILE_DIR="$WORK_ROOT/compile"
mkdir -p "$COMPILE_DIR"
sed "s|@SOURCE_DIR@|$OUTPUT/src/main/java|" "$GATE_DIR/fixture/pom.xml" > "$COMPILE_DIR/pom.xml"
echo "==> compiling generated java"
(cd "$COMPILE_DIR" && mvn -q -DskipTests compile) >"$WORK_ROOT/maven-compile.log" 2>&1 || {
  cat "$WORK_ROOT/maven-compile.log" >&2; die "generated java did not compile"
}
(cd "$COMPILE_DIR" && mvn -q dependency:build-classpath -Dmdep.outputFile="$WORK_ROOT/classpath.txt") \
  >"$WORK_ROOT/classpath.log" 2>&1 || { cat "$WORK_ROOT/classpath.log" >&2; die "classpath resolution failed"; }

CP="$COMPILE_DIR/target/classes:$(cat "$WORK_ROOT/classpath.txt")"

# ---- compile and run the wire test ----
TEST_CLASSES="$WORK_ROOT/test-classes"
mkdir -p "$TEST_CLASSES"
echo "==> compiling the wire test"
javac -cp "$CP" -d "$TEST_CLASSES" "$GATE_DIR/fixture/ClaimsProvenanceTest.java"

echo "==> running the wire test"
java --sun-misc-unsafe-memory-access=allow \
  -Dlogback.configurationFile="$GATE_DIR/fixture/logback.xml" \
  -cp "$TEST_CLASSES:$CP" \
  tacplus.crosstest.claimsprovenance.ClaimsProvenanceTest

echo "==> claims-provenance passed"
