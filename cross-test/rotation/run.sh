#!/usr/bin/env bash
# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# Behavioral fixture for make-before-break stream rotation (tacplus#322).
# Every generated service-to-service stream (client-, server-streaming, bidi)
# outlives the token it opened with: the generated server also serves the
# <Svc>Rotating sibling, and the generated client carries its streams across
# token lifetimes on it. The REAL generated server (generated wrapper,
# generated Authorizer, generated grpcserver with #260's expiry interceptor)
# meets the REAL generated client, with 3 s tokens:
#   - a handler and its caller, neither mentioning rotation, run one stream of
#     each shape across several lifetimes, both directions at once, with no
#     loss, duplication or reordering, on one handler call and one context;
#   - a mint failure ends the stream at its token's exp; a revoked caller is
#     cut off within one lifetime; a rotation with another subject's token
#     ends the stream PERMISSION_DENIED;
#   - on the wire: another subject's token with a valid session id is refused
#     (no description), a cutover with last both ways loses nothing, a leg cut
#     without last ends the logical stream (handler cancelled, caller gets the
#     status), never spliced;
#   - the generated client against a server without the sibling falls back to
#     the plain method, which ends at its token's exp.
# --language go (the default), java or rust: the same legs. Java runs
# RotationBehaviorTest under mvn. The Rust leg serves the generated wrapper and
# its rotating_sibling behind the ServiceManager's own layers
# (AuthEnforcingLayer, StreamCredentialExpiryLayer over STREAMING_RPC_PATHS and
# ROTATING_RPC_PATHS), plaintext, and runs the rotation crate's unit tests.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=../lib/cargo-target.sh
source "$ROOT_DIR/cross-test/lib/cargo-target.sh"
PROTO_SRC="$ROOT_DIR/cross-test/rotation/proto"
TESTDATA="$ROOT_DIR/cross-test/rotation/testdata"
LANGUAGE="go"
TEST_FILTER=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --language) LANGUAGE="$2"; shift 2 ;;
    --test) TEST_FILTER="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

die() { echo "rotation check FAILED: $*" >&2; exit 1; }

java_home_env() {
  if [[ -n "${JAVA_HOME:-}" && -x "$JAVA_HOME/bin/java" ]]; then
    return
  fi
  if [[ -x /usr/libexec/java_home ]]; then
    local resolved=""
    resolved="$(/usr/libexec/java_home -v 21 2>/dev/null || /usr/libexec/java_home -v 17 2>/dev/null || /usr/libexec/java_home 2>/dev/null || true)"
    if [[ -n "$resolved" ]]; then
      printf 'JAVA_HOME=%s\n' "$resolved"
    fi
  fi
}

case "$LANGUAGE" in
  go) TEST_FILTER="${TEST_FILTER:-^TestGoRotation}" ;;
  java) TEST_FILTER="${TEST_FILTER:-RotationBehaviorTest}" ;;
  rust) TEST_FILTER="${TEST_FILTER:-rust_rotation}" ;;
  *) die "rotation fixture supports --language go, java or rust" ;;
esac

TACPLUS_BIN="${TACPLUS_BIN:-$ROOT_DIR/target/tacplus-rotation-check}"
if [[ ! -x "$TACPLUS_BIN" || -z "${TACPLUS_BIN_PREBUILT:-}" ]]; then
  echo "==> Building tacplus binary"
  (cd "$ROOT_DIR" && go generate ./pkg/internal/proto/spec/... && go build -o "$TACPLUS_BIN" .)
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
cat > "$WORK/tacplus.yaml" <<EOF
language: "$LANGUAGE"
pkg: "com.example.rotationfixture"
src: "$PROTO_SRC"
output-dir: "tacplus"
EOF

echo "==> Generating rotation fixture ($LANGUAGE)"
(cd "$WORK" && TACPLUS_PYTHON="${TACPLUS_PYTHON:-python3}" "$TACPLUS_BIN" build --config tacplus.yaml --src "$PROTO_SRC") \
  || die "generation failed for $LANGUAGE"

OUT="$WORK/tacplus/model"
[[ -d "$OUT" ]] || die "no generated output at $OUT"

case "$LANGUAGE" in
  go)
    WRAPPER_DIR="$OUT/s2sm/grpc/servers/svc/rotationfixture"
    CLIENT="$OUT/s2sm/grpc/clients/svc/rotationfixture/ticker_service.go"
    [[ -f "$WRAPPER_DIR/ticker_service.go" ]] || die "go TickerService wrapper missing"
    [[ -f "$CLIENT" ]] || die "go TickerServiceClient missing"
    [[ -f "$OUT/chassis/rotation/rotation.go" ]] || die "go rotation chassis missing"
    grep -q 'rotation.Register(grpcServer, &types.TickerService_ServiceDesc' "$WRAPPER_DIR/ticker_service.go" \
      || die "the go TickerService wrapper does not serve TickerServiceRotating"
    grep -q 'rotation.NewConn(conn, tokenProvider)' "$CLIENT" \
      || die "the go TickerServiceClient does not rotate its streams"
    cp "$TESTDATA/rotation_behavior_test.go" "$WRAPPER_DIR/rotation_behavior_test.go"

    cat > "$OUT/go.mod" <<'EOF'
module example.com/rotationfixture/tacplus/model

go 1.23

require (
	github.com/dsnet/compress v0.0.2-0.20230904184137-39efe44ab707
	github.com/gin-gonic/gin v1.11.0
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/pelletier/go-toml/v2 v2.2.4
	github.com/soheilhy/cmux v0.1.5
	go.uber.org/zap v1.27.1
	golang.org/x/net v0.62.0
	golang.org/x/sync v0.19.0
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa
	google.golang.org/grpc v1.83.2
	google.golang.org/protobuf v1.36.11
	gopkg.in/yaml.v3 v3.0.1
)
EOF

    echo "==> Running generated Go rotation behavior tests"
    (cd "$OUT" && go mod tidy \
      && go vet ./chassis/rotation ./chassis/serviceconnection ./s2sm/grpc/servers/svc/rotationfixture ./s2sm/grpc/clients/svc/rotationfixture \
      && go test -race -count=1 -timeout 600s ./s2sm/grpc/servers/svc/rotationfixture -run "$TEST_FILTER" -v) \
      || die "generated Go rotation behavior tests failed"
    ;;
  java)
    command -v mvn >/dev/null || die "mvn is required to test generated Java output"
    JAVA_BASE="$OUT/src/main/java/com/example/rotationfixture/model"
    WRAPPER="$JAVA_BASE/s2sm/internal/grpc/servers/svc/rotationfixture/TickerService.java"
    CLIENT="$JAVA_BASE/s2sm/internal/grpc/clients/svc/rotationfixture/TickerServiceGrpcClient.java"
    [[ -f "$WRAPPER" ]] || die "java TickerService wrapper missing"
    [[ -f "$CLIENT" ]] || die "java TickerServiceGrpcClient missing"
    [[ -f "$JAVA_BASE/chassis/rotation/StreamRotation.java" ]] || die "java rotation chassis missing"
    grep -q 'grpcServerBuilder.addService(rotatingService())' "$WRAPPER" \
      || die "the java TickerService wrapper does not serve TickerServiceRotating"
    grep -q 'StreamRotation.channel(channel, tokenProvider)' "$CLIENT" \
      || die "the java TickerServiceGrpcClient does not rotate its streams"
    grep -q 'wrapperTicker.registerServices(grpcServerBuilder)' "$JAVA_BASE/s2sm/ServiceManager.java" \
      || die "the java ServiceManager does not register the wrapper with its sibling"
    mkdir -p "$OUT/src/test/java/com/example/rotationfixture/model/chassis/grpcserver"
    cp "$TESTDATA/RotationBehaviorTest.java" \
      "$OUT/src/test/java/com/example/rotationfixture/model/chassis/grpcserver/RotationBehaviorTest.java"
    sed -e 's/com\.example\.streamexpiry/com.example.rotationfixture/' \
      -e 's/stream-expiry-java-fixture/rotation-java-fixture/' \
      "$ROOT_DIR/cross-test/stream-expiry/testdata/pom.xml" > "$OUT/pom.xml"

    echo "==> Running generated Java rotation behavior tests"
    java_env="$(java_home_env)"
    (cd "$OUT" && env ${java_env:+"$java_env"} mvn -q -Dtest="$TEST_FILTER" test) \
      || die "generated Java rotation behavior tests failed"
    ;;
  rust)
    WRAPPER="$OUT/s2sm/internal/grpc/servers/svc/rotationfixture/ticker_service.rs"
    CLIENT="$OUT/s2sm/internal/grpc/clients/svc/rotationfixture/ticker_service.rs"
    [[ -f "$WRAPPER" ]] || die "rust TickerService wrapper missing"
    [[ -f "$CLIENT" ]] || die "rust TickerServiceGrpcClient missing"
    [[ -f "$OUT/chassis/rotation/src/lib.rs" ]] || die "rust rotation chassis missing"
    grep -q 'pub fn rotating_sibling' "$WRAPPER" \
      || die "the rust TickerService wrapper has no TickerServiceRotating sibling"
    grep -q '.rotating_sibling(' "$OUT/s2sm/mod.rs" \
      || die "the rust ServiceManager does not serve TickerServiceRotating"
    grep -q 'ROTATING_RPC_PATHS' "$OUT/s2sm/mod.rs" \
      || die "the rust ServiceManager does not end sibling legs at their exp"
    grep -q 'rotation::RotatingChannel::new(channel' "$CLIENT" \
      || die "the rust TickerServiceGrpcClient does not rotate its streams"
    cp "$TESTDATA/rotation_behavior_test.rs" "$OUT/s2sm/rotation_behavior_test.rs"
    printf '\n#[cfg(test)]\nmod rotation_behavior_test;\n' >> "$OUT/s2sm/mod.rs"

    echo "==> Running generated Rust rotation behavior tests"
    (cd "$OUT" && CARGO_TARGET_DIR="$CROSS_TEST_CARGO_TARGET" cargo test -p rotation \
      && CARGO_TARGET_DIR="$CROSS_TEST_CARGO_TARGET" cargo test -p rotationfixture-model "$TEST_FILTER" -- --nocapture) \
      || die "generated Rust rotation behavior tests failed"
    prune_cargo_workspace "$OUT"
    ;;
esac

echo "==> rotation check PASSED ($LANGUAGE)"
