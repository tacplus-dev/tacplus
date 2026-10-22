#!/usr/bin/env bash
# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# Behavioral clean-close fixture for the generated Java subscriber: a peer that
# ends every subscribe stream with OK must not make the subscriber re-resolve
# its channel in a tight loop (issue #32).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PROTO_SRC="$ROOT_DIR/cross-test/consumer-contract/proto"
source "$ROOT_DIR/cross-test/lib/java-compile.sh"

die() { echo "subscriber-clean-close check FAILED: $*" >&2; exit 1; }

TACPLUS_BIN="${TACPLUS_BIN:-$ROOT_DIR/target/tacplus-subscriber-clean-close}"
if [[ ! -x "$TACPLUS_BIN" || -z "${TACPLUS_BIN_PREBUILT:-}" ]]; then
  echo "==> Building tacplus binary"
  (cd "$ROOT_DIR" && go generate ./pkg/internal/proto/spec/... && go build -o "$TACPLUS_BIN" .)
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
cat > "$WORK/tacplus.yaml" <<EOF
language: "java"
pkg: "com.example.contract"
src: "$PROTO_SRC"
output-dir: "tacplus"
EOF

echo "==> Generating Java from the consumer-contract proto"
(cd "$WORK" && "$TACPLUS_BIN" build --config tacplus.yaml --src "$PROTO_SRC") \
  || die "generation failed"

OUT="$WORK/tacplus/model"
[[ -d "$OUT" ]] || die "no generated output at $OUT"
[[ -f "$OUT/src/main/java/com/example/contract/model/s2sm/subscribe/ContractfixturesFixtureStateEventsSubscriber.java" ]] \
  || die "generated events subscriber missing"

java_env="$(java_home_env)"
if [[ -n "$java_env" ]]; then
  export "$java_env"
fi
JDK_BIN=""
if [[ -n "${JAVA_HOME:-}" ]]; then
  JDK_BIN="$JAVA_HOME/bin/"
fi

echo "==> Compiling generated Java model"
write_java_compile_pom "$OUT/pom.xml" "$OUT"
(cd "$OUT" && mvn -q compile) || die "generated Java model did not compile"
(cd "$OUT" && mvn -q dependency:build-classpath -Dmdep.outputFile="$WORK/java.classpath") \
  || die "could not resolve the generated Java classpath"

CP="$OUT/target/classes:$(cat "$WORK/java.classpath")"
mkdir -p "$WORK/testclasses"
"${JDK_BIN}javac" -d "$WORK/testclasses" -cp "$CP" \
  "$ROOT_DIR/cross-test/subscriber-clean-close/testdata/SubscriberCleanCloseTest.java" \
  || die "clean-close test did not compile"

echo "==> Running generated Java clean-close subscriber test"
"${JDK_BIN}java" -cp "$WORK/testclasses:$CP" SubscriberCleanCloseTest \
  || die "generated Java subscriber re-resolves in a tight loop on clean stream closes"

echo "==> subscriber-clean-close check PASSED"
