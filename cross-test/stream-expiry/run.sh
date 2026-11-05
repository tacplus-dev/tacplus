#!/usr/bin/env bash
# Behavioral fixture for server-side stream credential expiry (issue #260).
# API.md's Client Send Contract says a stream carries exactly one token, the one
# it opened with, and that servers terminate streams carrying expired
# credentials. A client-streaming, a server-streaming and a bidi stream are each
# opened against the REAL generated server (generated wrapper, generated
# Authorizer, generated grpcserver chassis) with a token that expires a few
# seconds out and is never rotated, while the handler keeps the stream open.
# The server must end each one at the token's exp with UNAUTHENTICATED; a server
# that checks the token only at open leaves all three open and the leg fails.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=../lib/cargo-target.sh
source "$ROOT_DIR/cross-test/lib/cargo-target.sh"
PROTO_SRC="$ROOT_DIR/cross-test/stream-expiry/proto"
TESTDATA="$ROOT_DIR/cross-test/stream-expiry/testdata"
LANGUAGE="go"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --language) LANGUAGE="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

die() { echo "stream-expiry check FAILED: $*" >&2; exit 1; }

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
  go|rust|java) ;;
  *) die "stream-expiry fixture supports --language go, rust, or java" ;;
esac

TACPLUS_BIN="${TACPLUS_BIN:-$ROOT_DIR/target/tacplus-stream-expiry-check}"
if [[ ! -x "$TACPLUS_BIN" || -z "${TACPLUS_BIN_PREBUILT:-}" ]]; then
  echo "==> Building tacplus binary"
  (cd "$ROOT_DIR" && go generate ./pkg/internal/proto/spec/... && go build -o "$TACPLUS_BIN" .)
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
cat > "$WORK/tacplus.yaml" <<EOF
language: "$LANGUAGE"
pkg: "com.example.streamexpiry"
src: "$PROTO_SRC"
output-dir: "tacplus"
EOF

echo "==> Generating stream-expiry fixture ($LANGUAGE)"
(cd "$WORK" && TACPLUS_PYTHON="${TACPLUS_PYTHON:-python3}" "$TACPLUS_BIN" build --config tacplus.yaml --src "$PROTO_SRC") \
  || die "generation failed for $LANGUAGE"

OUT="$WORK/tacplus/model"
[[ -d "$OUT" ]] || die "no generated output at $OUT"

case "$LANGUAGE" in
  go)
    WRAPPER_DIR="$OUT/s2sm/grpc/servers/svc/streamexpiry"
    [[ -f "$WRAPPER_DIR/streamer_service.go" ]] || die "go StreamerService wrapper missing"
    cp "$TESTDATA/stream_expiry_behavior_test.go" "$WRAPPER_DIR/stream_expiry_behavior_test.go"

    cat > "$OUT/go.mod" <<'EOF'
module example.com/streamexpiry/tacplus/model

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

    echo "==> Running generated Go stream-expiry behavior tests"
    (cd "$OUT" && go mod tidy && go vet ./s2sm/grpc/servers/svc/streamexpiry ./chassis/grpcserver \
      && go test -race -count=1 -timeout 300s ./s2sm/grpc/servers/svc/streamexpiry -run '^TestGo' -v) \
      || die "generated Go stream-expiry behavior tests failed"
    ;;
  rust)
    grep -q "StreamCredentialExpiryLayer" "$OUT/chassis/grpcserver/src/authorizer.rs" \
      || die "rust grpcserver StreamCredentialExpiryLayer missing"
    grep -q "STREAMING_RPC_PATHS" "$OUT/s2sm/mod.rs" || die "rust ServiceManager STREAMING_RPC_PATHS missing"
    cp "$TESTDATA/stream_expiry_behavior_test.rs" "$OUT/s2sm/stream_expiry_behavior_test.rs"
    printf '\n#[cfg(test)]\nmod stream_expiry_behavior_test;\n' >> "$OUT/s2sm/mod.rs"

    echo "==> Running generated Rust stream-expiry behavior tests"
    (cd "$OUT" && CARGO_TARGET_DIR="$CROSS_TEST_CARGO_TARGET" cargo test -p streamexpiry-model rust_stream_expiry -- --nocapture) \
      || die "generated Rust stream-expiry behavior tests failed"
    prune_cargo_workspace "$OUT"
    ;;
  java)
    command -v mvn >/dev/null || die "mvn is required to test generated Java output"
    mkdir -p "$OUT/src/test/java/com/example/streamexpiry/model/chassis/grpcserver"
    cp "$TESTDATA/StreamExpiryBehaviorTest.java" \
      "$OUT/src/test/java/com/example/streamexpiry/model/chassis/grpcserver/StreamExpiryBehaviorTest.java"
    cp "$TESTDATA/pom.xml" "$OUT/pom.xml"

    echo "==> Running generated Java stream-expiry behavior test"
    java_env="$(java_home_env)"
    (cd "$OUT" && env ${java_env:+"$java_env"} mvn -q -Dtest=StreamExpiryBehaviorTest test) \
      || die "generated Java stream-expiry behavior test failed"
    ;;
esac

echo "==> stream-expiry check PASSED ($LANGUAGE)"
