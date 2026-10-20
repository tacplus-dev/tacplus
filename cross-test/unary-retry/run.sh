#!/usr/bin/env bash
# Copyright (c) 2026 Seabeam LLC
# SPDX-License-Identifier: Apache-2.0
#
# Fault-injection fixture for the generated unary client retry path (issue #57):
# a declared S2S client retries a retry-safe read through a peer in outage. The
# retries must be jittered and must stay within the retry budget; the Go client
# also must not start a retry once the caller's deadline cannot cover the backoff.
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PROTO_SRC="$ROOT_DIR/cross-test/unary-retry/proto"
TESTDATA="$ROOT_DIR/cross-test/unary-retry/testdata"
source "$ROOT_DIR/cross-test/lib/java-compile.sh"
LANGUAGE="go"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --language) LANGUAGE="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done
die() { echo "unary-retry check FAILED: $*" >&2; exit 1; }
case "$LANGUAGE" in
  go|java|rust) ;;
  *) die "unary-retry fixture supports --language go, java, or rust" ;;
esac

TACPLUS_BIN="${TACPLUS_BIN:-$ROOT_DIR/target/tacplus-unary-retry}"
if [[ ! -x "$TACPLUS_BIN" || -z "${TACPLUS_BIN_PREBUILT:-}" ]]; then
  echo "==> Building tacplus binary"
  (cd "$ROOT_DIR" && go generate ./pkg/internal/proto/spec/... && go build -o "$TACPLUS_BIN" .)
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
cat > "$WORK/tacplus.yaml" <<EOF
language: "$LANGUAGE"
pkg: "com.example.unaryretry"
src: "$PROTO_SRC"
output-dir: "tacplus"
EOF

echo "==> Generating unary-retry fixture ($LANGUAGE)"
(cd "$WORK" && TACPLUS_PYTHON="${TACPLUS_PYTHON:-python3}" "$TACPLUS_BIN" build --config tacplus.yaml --src "$PROTO_SRC") \
  || die "generation failed for $LANGUAGE"
OUT="$WORK/tacplus/model"
[[ -d "$OUT" ]] || die "no generated output at $OUT"

case "$LANGUAGE" in
  go)
    CLIENT="$OUT/s2sm/grpc/clients/contract/unaryretry/retry_service.go"
    [[ -f "$CLIENT" ]] || die "generated RetryServiceClient missing"
    cp "$TESTDATA/unary_retry_behavior_test.go" "$OUT/s2sm/grpc/clients/contract/unaryretry/unary_retry_behavior_test.go"
    cat > "$OUT/go.mod" <<'EOF'
module example.com/unaryretry/tacplus/model
go 1.23
require (
	github.com/dsnet/compress v0.0.2-0.20230904184137-39efe44ab707
	github.com/gin-gonic/gin v1.11.0
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/pelletier/go-toml/v2 v2.2.4
	github.com/soheilhy/cmux v0.1.5
	go.uber.org/zap v1.27.1
	golang.org/x/sync v0.19.0
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260209200024-4cfbd4190f57
	google.golang.org/grpc v1.78.0
	google.golang.org/protobuf v1.36.11
	gopkg.in/yaml.v3 v3.0.1
)
EOF
    echo "==> Running generated Go unary-retry behavior tests"
    (cd "$OUT" && go mod tidy && go vet ./s2sm/grpc/clients/contract/unaryretry && go test -count=1 -timeout 300s ./s2sm/grpc/clients/contract/unaryretry -run '^TestGo' -v) \
      || die "generated Go unary retry behavior tests failed"
    ;;
  java)
    command -v mvn >/dev/null || die "mvn is required to test generated Java output"
    CLIENT="$OUT/src/main/java/com/example/unaryretry/model/s2sm/internal/grpc/clients/contract/unaryretry/RetryServiceGrpcClient.java"
    [[ -f "$CLIENT" ]] || die "generated RetryServiceGrpcClient missing"
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
    "${JDK_BIN}javac" -d "$WORK/testclasses" -cp "$CP" "$TESTDATA/UnaryRetryBehaviorTest.java" \
      || die "unary-retry test did not compile"
    echo "==> Running generated Java unary-retry behavior test"
    "${JDK_BIN}java" -cp "$WORK/testclasses:$CP" UnaryRetryBehaviorTest \
      || die "generated Java unary client retry: budget or jitter behavior failed"
    ;;
  rust)
    command -v cargo >/dev/null || die "cargo is required to test generated Rust output"
    CLIENT="$OUT/s2sm/internal/grpc/clients/contract/unaryretry/retry_service.rs"
    [[ -f "$CLIENT" ]] || die "generated RetryServiceGrpcClient missing"
    cp "$TESTDATA/unary_retry_behavior_test.rs" "$OUT/s2sm/internal/grpc/clients/contract/unaryretry/unary_retry_behavior_test.rs"
    printf '\n#[cfg(test)]\n#[path = "unary_retry_behavior_test.rs"]\nmod unary_retry_behavior_test;\n' >> "$CLIENT"
    echo "==> Running generated Rust unary-retry behavior tests"
    (cd "$OUT" && CARGO_TARGET_DIR="$OUT/target" cargo test -p unaryretry-model rust_) \
      || die "generated Rust unary retry behavior tests failed"
    ;;
esac
echo "==> unary-retry check PASSED ($LANGUAGE)"
