#!/usr/bin/env bash
# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# Behavioral fixture for the uni-config (tacplus#262, tacplus-dev/operator#17):
# the generated chassis reads its configuration from config, never from an
# environment variable the uni-config did not render.
#   - The config loader reads the bootstrap config from the platform's fixed
#     path (BOOTSTRAP_CONFIG_PATH) unless -c/--config names another, and no
#     BOOTSTRAP_CONFIG environment variable moves it.
#   - The identity manager takes the projected Kubernetes token file from the
#     config's k8s_token_file, never from KERNEL_K8S_TOKEN_FILE.
#   - Every message has a generated file reader, so a service reads its
#     Secrets from the platform's secrets file (SECRETS_PATH) instead of from
#     environment variables.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=../lib/cargo-target.sh
source "$ROOT_DIR/cross-test/lib/cargo-target.sh"
PROTO_SRC="$ROOT_DIR/cross-test/uni-config/proto"
TESTDATA="$ROOT_DIR/cross-test/uni-config/testdata"
LANGUAGE="go"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --language) LANGUAGE="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

die() { echo "uni-config check FAILED: $*" >&2; exit 1; }

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
  *) die "uni-config fixture supports --language go, rust, or java" ;;
esac

TACPLUS_BIN="${TACPLUS_BIN:-$ROOT_DIR/target/tacplus-uni-config-check}"
if [[ ! -x "$TACPLUS_BIN" || -z "${TACPLUS_BIN_PREBUILT:-}" ]]; then
  echo "==> Building tacplus binary"
  (cd "$ROOT_DIR" && go generate ./pkg/internal/proto/spec/... && go build -o "$TACPLUS_BIN" .)
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
cat > "$WORK/tacplus.yaml" <<EOF
language: "$LANGUAGE"
pkg: "com.example.uniconfig"
src: "$PROTO_SRC"
output-dir: "tacplus"
EOF

echo "==> Generating uni-config fixture ($LANGUAGE)"
(cd "$WORK" && TACPLUS_PYTHON="${TACPLUS_PYTHON:-python3}" "$TACPLUS_BIN" build --config tacplus.yaml --src "$PROTO_SRC") \
  || die "generation failed for $LANGUAGE"

OUT="$WORK/tacplus/model"
[[ -d "$OUT" ]] || die "no generated output at $OUT"

case "$LANGUAGE" in
  go)
    cp "$TESTDATA/go/grpcconfig_uniconfig_test.go" "$OUT/chassis/grpcconfig/uniconfig_test.go"
    cp "$TESTDATA/go/service_im_uniconfig_test.go" "$OUT/chassis/service_im/uniconfig_test.go"

    cat > "$OUT/go.mod" <<'EOF'
module example.com/uniconfig/tacplus/model

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

    echo "==> Running generated Go uni-config behavior tests"
    (cd "$OUT" && go mod tidy && go vet ./chassis/grpcconfig ./chassis/service_im \
      && go test -count=1 -timeout 300s ./chassis/grpcconfig ./chassis/service_im -run '^TestGo' -v) \
      || die "generated Go uni-config behavior tests failed"
    ;;
  rust)
    cp "$TESTDATA/rust/grpcconfig_uniconfig_test.rs" "$OUT/chassis/grpcconfig/src/uniconfig_test.rs"
    printf '\n#[cfg(test)]\nmod uniconfig_test;\n' >> "$OUT/chassis/grpcconfig/src/lib.rs"
    cp "$TESTDATA/rust/service_im_uniconfig_test.rs" "$OUT/chassis/service_im/src/uniconfig_test.rs"
    printf '\n#[cfg(test)]\nmod uniconfig_test;\n' >> "$OUT/chassis/service_im/src/lib.rs"

    echo "==> Running generated Rust uni-config behavior tests"
    (cd "$OUT" && CARGO_TARGET_DIR="$CROSS_TEST_CARGO_TARGET" cargo test -p grpcconfig -p service_im rust_) \
      || die "generated Rust uni-config behavior tests failed"
    prune_cargo_workspace "$OUT"
    ;;
  java)
    command -v mvn >/dev/null || die "mvn is required to test generated Java output"
    JAVA_TEST="$OUT/src/test/java/com/example/uniconfig/model/chassis"
    mkdir -p "$JAVA_TEST/grpcconfig" "$JAVA_TEST/service_im"
    cp "$TESTDATA/java/GrpcConfigUniConfigTest.java" "$JAVA_TEST/grpcconfig/GrpcConfigUniConfigTest.java"
    cp "$TESTDATA/java/IdentityManagerUniConfigTest.java" "$JAVA_TEST/service_im/IdentityManagerUniConfigTest.java"
    cp "$TESTDATA/java/pom.xml" "$OUT/pom.xml"

    echo "==> Running generated Java uni-config behavior tests"
    java_env="$(java_home_env)"
    (cd "$OUT" && env ${java_env:+"$java_env"} mvn -q -Dtest='GrpcConfigUniConfigTest,IdentityManagerUniConfigTest' test) \
      || die "generated Java uni-config behavior tests failed"
    ;;
esac

echo "==> uni-config check PASSED ($LANGUAGE)"
