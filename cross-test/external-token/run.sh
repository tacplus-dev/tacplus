#!/usr/bin/env bash
# Behavioral fixture for the generated external-audience token accessor
# (token_for_external): two calls must reach the token minter twice and return
# two distinct tokens, each requested with the caller's own kernel token, the
# given aud, and an exp no more than 600 s out.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PROTO_SRC="$ROOT_DIR/cross-test/external-token/proto"
TESTDATA="$ROOT_DIR/cross-test/external-token/testdata"
LANGUAGE="go"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --language) LANGUAGE="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

die() { echo "external-token check FAILED: $*" >&2; exit 1; }

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
  *) die "external-token fixture supports --language go, rust, or java" ;;
esac

TACPLUS_BIN="${TACPLUS_BIN:-$ROOT_DIR/target/tacplus-external-token-check}"
if [[ ! -x "$TACPLUS_BIN" || -z "${TACPLUS_BIN_PREBUILT:-}" ]]; then
  echo "==> Building tacplus binary"
  (cd "$ROOT_DIR" && go generate ./pkg/internal/proto/spec/... && go build -o "$TACPLUS_BIN" .)
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
cat > "$WORK/tacplus.yaml" <<EOF
language: "$LANGUAGE"
pkg: "com.example.externaltoken"
src: "$PROTO_SRC"
output-dir: "tacplus"
EOF

echo "==> Generating external-token fixture ($LANGUAGE)"
(cd "$WORK" && TACPLUS_PYTHON="${TACPLUS_PYTHON:-python3}" "$TACPLUS_BIN" build --config tacplus.yaml --src "$PROTO_SRC") \
  || die "generation failed for $LANGUAGE"

OUT="$WORK/tacplus/model"
[[ -d "$OUT" ]] || die "no generated output at $OUT"

case "$LANGUAGE" in
  go)
    grep -q "func (sm \*ServiceManager) TokenForExternal" "$OUT/s2sm/s2sm.go" || die "go ServiceManager.TokenForExternal missing"
    cp "$TESTDATA/external_token_behavior_test.go" "$OUT/chassis/service_im/external_token_behavior_test.go"

    cat > "$OUT/go.mod" <<'EOF'
module example.com/externaltoken/tacplus/model

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

    echo "==> Running generated Go external-token behavior tests"
    (cd "$OUT" && go mod tidy && go vet ./s2sm ./chassis/service_im && go test -count=1 -timeout 300s ./chassis/service_im -run '^TestGo' -v) \
      || die "generated Go external-token behavior tests failed"
    ;;
  rust)
    grep -q "pub async fn token_for_external" "$OUT/s2sm/mod.rs" || die "rust ServiceManager::token_for_external missing"
    cp "$TESTDATA/external_token_behavior_test.rs" "$OUT/s2sm/external_token_behavior_test.rs"
    printf '\n#[cfg(test)]\nmod external_token_behavior_test;\n' >> "$OUT/s2sm/mod.rs"

    echo "==> Running generated Rust external-token behavior tests"
    (cd "$OUT" && CARGO_TARGET_DIR="$OUT/target" cargo test -p externaltoken-model rust_) \
      || die "generated Rust external-token behavior tests failed"
    ;;
  java)
    command -v mvn >/dev/null || die "mvn is required to test generated Java output"
    grep -rq "public String tokenForExternal(String audience)" "$OUT/src/main/java/com/example/externaltoken/model/s2sm" \
      || die "java ServiceManager.tokenForExternal missing"
    mkdir -p "$OUT/src/test/java/com/example/externaltoken/model/chassis/service_im"
    cp "$TESTDATA/ExternalTokenBehaviorTest.java" \
      "$OUT/src/test/java/com/example/externaltoken/model/chassis/service_im/ExternalTokenBehaviorTest.java"
    cp "$TESTDATA/pom.xml" "$OUT/pom.xml"

    echo "==> Running generated Java external-token behavior tests"
    java_env="$(java_home_env)"
    (cd "$OUT" && env ${java_env:+"$java_env"} mvn -q -Dtest=ExternalTokenBehaviorTest test) \
      || die "generated Java external-token behavior tests failed"
    ;;
esac

echo "==> external-token check PASSED ($LANGUAGE)"
