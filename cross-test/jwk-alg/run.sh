#!/usr/bin/env bash
# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# JWK alg-selection gate (issue #179).
#
# The algorithm a token is verified with comes from the KEY: its family
# (RSA RS256/RS384/RS512; EC by curve; OKP Ed25519 EdDSA), narrowed to the
# key's alg when it declares one. A key with no alg (Microsoft's JWKS publishes
# none) verifies any alg of its family; nothing outside the family, no `none`,
# no HMAC, no PS*, ever.
#
# go, java, python, rust and typescript each build the JWK of every case in the
# ONE checked-in fixture (cross-test/fixtures/jwk_alg_vectors.json), verify the
# case's pre-signed token with the GENERATED jwk and token chassis, and must
# reach the case's expect. No leg signs anything, so no port can drift from
# another in how the vectors were made. Regenerate the fixture with
#   go run ./cross-test/jwk-alg/genvectors
#
# Python is a client-only target and does not emit the jwk/token chassis, so its
# leg renders those templates into the generated model first
# (pkg/internal/python/pyrender).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=../lib/cargo-target.sh
source "$ROOT_DIR/cross-test/lib/cargo-target.sh"
GATE_DIR="$ROOT_DIR/cross-test/jwk-alg"
PROTO_SRC="$GATE_DIR/proto"
TESTDATA="$GATE_DIR/testdata"
FIXTURE="$ROOT_DIR/cross-test/fixtures/jwk_alg_vectors.json"
MODEL_PKG="com.gitlab.example.tacplus.jwkalg.jwkalgclient"
GO_MODULE="gitlab.com/example/tacplus/jwkalg/jwkalgclient"
PY_PACKAGE="jwkalgclient_model"
WORK_ROOT="${TACPLUS_JWK_ALG_WORKDIR:-}"
TACPLUS_BIN="${TACPLUS_BIN:-}"
KEEP_WORK="${TACPLUS_JWK_ALG_KEEP:-false}"
LANGUAGE_FILTER=""

usage() {
  cat <<'USAGE'
Usage:
  cross-test/jwk-alg/run.sh [options]

Options:
  --language LANG   Run one leg: go, java, python, rust, typescript
  --workdir DIR     Use a specific temporary work directory
  --tacplus BIN     Use an existing tacplus binary
  --keep            Keep the temporary work directory
  -h, --help        Show this help

Environment:
  TACPLUS_BIN                    Existing tacplus binary to use
  TACPLUS_JWK_ALG_WORKDIR        Work directory for generated outputs
  TACPLUS_JWK_ALG_KEEP=true      Preserve generated outputs after success
USAGE
}

die() {
  echo "ERROR: jwk-alg: $*" >&2
  exit 1
}

need_value() {
  local flag="$1"
  local value="${2:-}"
  [[ -n "$value" ]] || die "$flag requires a value"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --language)
      need_value "$1" "${2:-}"
      LANGUAGE_FILTER="$2"
      shift 2
      ;;
    --workdir)
      need_value "$1" "${2:-}"
      WORK_ROOT="$2"
      shift 2
      ;;
    --tacplus)
      need_value "$1" "${2:-}"
      TACPLUS_BIN="$2"
      shift 2
      ;;
    --keep)
      KEEP_WORK=true
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown argument: $1"
      ;;
  esac
done

[[ -d "$PROTO_SRC" ]] || die "proto fixture not found: $PROTO_SRC"
[[ -f "$FIXTURE" ]] || die "vector fixture not found: $FIXTURE"

if [[ -z "$LANGUAGE_FILTER" ]]; then
  languages=(go java python rust typescript)
else
  case "$LANGUAGE_FILTER" in
    go|java|python|rust|typescript ) languages=("$LANGUAGE_FILTER") ;;
    * ) die "unsupported language: $LANGUAGE_FILTER" ;;
  esac
fi

if [[ -z "$WORK_ROOT" ]]; then
  WORK_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/tacplus-jwk-alg.XXXXXX")"
else
  mkdir -p "$WORK_ROOT"
  WORK_ROOT="$(cd "$WORK_ROOT" && pwd)"
fi

cleanup() {
  if [[ "$KEEP_WORK" != true ]]; then
    rm -rf "$WORK_ROOT"
  fi
}
trap cleanup EXIT

if [[ -z "$TACPLUS_BIN" ]]; then
  TACPLUS_BIN="$WORK_ROOT/tacplus-local"
  echo "==> Building local tacplus binary"
  (cd "$ROOT_DIR" && go generate ./pkg/internal/proto/spec/... && go build -o "$TACPLUS_BIN" .)
fi

if [[ "$TACPLUS_BIN" == */* ]]; then
  TACPLUS_BIN="$(cd "$(dirname "$TACPLUS_BIN")" && pwd)/$(basename "$TACPLUS_BIN")"
fi

assert_clean_log() {
  local log="$1"
  if [[ ! -s "$log" ]]; then
    return
  fi
  if rg -n '(WARN|WARNING|warning:|ERROR|panic:|fatal:)' "$log"; then
    die "warning/error output found in $log"
  fi
}

# Every leg prints one line `jwk-alg <language>: <n> cases, <m> accepted`;
# asserting it proves the leg ran the whole fixture rather than skipping.
assert_ran_all() {
  local language="$1"
  local log="$2"
  local want
  want="$(python3 -c 'import json,sys; print(len(json.load(open(sys.argv[1]))["cases"]))' "$FIXTURE")"
  grep -q "jwk-alg $language: $want cases" "$log" \
    || die "$language leg did not report all $want fixture cases (see $log)"
}

generate_model() {
  local language="$1"
  local dir="$2"
  local log="$dir/generate.log"
  echo "==> Generating $language model"
  mkdir -p "$dir"
  cat > "$dir/tacplus.yaml" <<EOF
language: "$language"
pkg: "$MODEL_PKG"
src: "$PROTO_SRC"
output-dir: "model"
EOF
  (cd "$dir" && "$TACPLUS_BIN" build --config tacplus.yaml --src "$PROTO_SRC" --tag latest) >"$log" 2>&1 || {
    cat "$log" >&2
    die "$language generation failed"
  }
  assert_clean_log "$log"
}

# Matches the consumer-contract go compile module: the generation dir is the
# module root and model/ is a package tree inside it.
write_go_mod() {
  cat > "$1" <<EOF
module $GO_MODULE

go 1.23

require (
	github.com/caddyserver/certmagic v0.25.1
	github.com/dsnet/compress v0.0.2-0.20230904184137-39efe44ab707
	github.com/gin-contrib/cors v1.7.6
	github.com/gin-contrib/zap v1.1.6
	github.com/gin-gonic/gin v1.11.0
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/hashicorp/go-multierror v1.1.1
	github.com/jinzhu/copier v0.4.0
	github.com/open-policy-agent/opa v1.13.1
	github.com/pelletier/go-toml/v2 v2.2.4
	github.com/soheilhy/cmux v0.1.5
	go.uber.org/zap v1.27.1
	golang.org/x/crypto v0.47.0
	golang.org/x/net v0.49.0
	golang.org/x/sync v0.19.0
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa
	google.golang.org/grpc v1.83.2
	google.golang.org/protobuf v1.36.11
	gopkg.in/yaml.v3 v3.0.1
)
EOF
}

run_go() {
  local dir="$WORK_ROOT/go"
  generate_model go "$dir"
  write_go_mod "$dir/go.mod"
  cp "$TESTDATA/jwk_alg_test.go" "$dir/model/chassis/token/jwk_alg_test.go"
  echo "==> Verifying the vectors with the generated go chassis"
  (cd "$dir" && go mod tidy && JWK_ALG_VECTOR_FIXTURE="$FIXTURE" go test -count=1 -v -run '^TestJwk' ./model/chassis/token) \
    >"$dir/test.log" 2>&1 || {
    cat "$dir/test.log" >&2
    die "go leg failed"
  }
  grep -h 'jwk-alg go:' "$dir/test.log"
  assert_ran_all go "$dir/test.log"
}

run_python() {
  command -v python3 >/dev/null || die "python3 is required for the python leg"
  local dir="$WORK_ROOT/python"
  generate_model python "$dir"
  echo "==> Rendering the python jwk/token chassis into the model"
  (cd "$ROOT_DIR" && go run ./pkg/internal/python/pyrender -package "$PY_PACKAGE" -out "$dir/model/$PY_PACKAGE/chassis") \
    || die "python chassis render failed"
  echo "==> Verifying the vectors with the generated python chassis"
  (cd "$dir/model" && PYTHONPATH="$dir/model" python3 "$TESTDATA/test_jwk_alg.py" "$FIXTURE" "$PY_PACKAGE") \
    >"$dir/test.log" 2>&1 || {
    cat "$dir/test.log" >&2
    die "python leg failed"
  }
  cat "$dir/test.log"
  assert_ran_all python "$dir/test.log"
}

run_rust() {
  command -v cargo >/dev/null || die "cargo is required for the rust leg"
  local dir="$WORK_ROOT/rust"
  generate_model rust "$dir"
  local leg_dir="$dir/jwk-alg"
  mkdir -p "$leg_dir/src"
  cp "$TESTDATA/jwk_alg.rs" "$leg_dir/src/main.rs"
  cat > "$leg_dir/Cargo.toml" <<EOF
[package]
name = "jwk-alg-rust"
version = "0.1.0"
edition = "2021"

[dependencies]
jwk = { path = "$dir/model/chassis/jwk" }
token = { path = "$dir/model/chassis/token" }
serde_json = "1.0.149"
EOF
  echo "==> Building the rust leg"
  (cd "$leg_dir" && CARGO_TARGET_DIR="$CROSS_TEST_CARGO_TARGET" cargo build --quiet) >"$dir/build.log" 2>&1 || {
    cat "$dir/build.log" >&2
    die "rust leg build failed"
  }
  assert_clean_log "$dir/build.log"
  echo "==> Verifying the vectors with the generated rust chassis"
  "$CROSS_TEST_CARGO_TARGET/debug/jwk-alg-rust" "$FIXTURE" >"$dir/test.log" 2>&1 || {
    cat "$dir/test.log" >&2
    die "rust leg failed"
  }
  cat "$dir/test.log"
  assert_ran_all rust "$dir/test.log"
}

run_typescript() {
  command -v npm >/dev/null || die "npm is required for the typescript leg"
  local dir="$WORK_ROOT/typescript"
  generate_model typescript "$dir"
  echo "==> Building typescript model"
  (cd "$dir/model" && npm install && npm run build) >"$dir/build.log" 2>&1 || {
    cat "$dir/build.log" >&2
    die "typescript model build failed"
  }
  local leg_dir="$dir/jwk-alg"
  mkdir -p "$leg_dir"
  cp "$TESTDATA/jwk_alg.mjs" "$leg_dir/jwk_alg.mjs"
  ln -s "$dir/model/node_modules" "$leg_dir/node_modules"
  echo "==> Verifying the vectors with the generated typescript chassis"
  (cd "$leg_dir" && node jwk_alg.mjs "$FIXTURE") >"$dir/test.log" 2>&1 || {
    cat "$dir/test.log" >&2
    die "typescript leg failed"
  }
  cat "$dir/test.log"
  assert_ran_all typescript "$dir/test.log"
}

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

run_java() {
  command -v mvn >/dev/null || die "mvn is required for the java leg"
  local dir="$WORK_ROOT/java"
  generate_model java "$dir"
  local compile_dir="$dir/compile"
  mkdir -p "$compile_dir"
  cp "$TESTDATA/pom.xml" "$compile_dir/pom.xml"

  local java_env
  java_env="$(java_home_env)"
  local mvn_env=()
  [[ -n "$java_env" ]] && mvn_env=(env "$java_env")

  echo "==> Compiling generated java model"
  (cd "$compile_dir" && "${mvn_env[@]}" mvn -q -DskipTests -Dtacplus.model.dir="$dir/model" compile) \
    >"$dir/maven-compile.log" 2>&1 || {
    cat "$dir/maven-compile.log" >&2
    die "generated java model compile failed"
  }
  (cd "$compile_dir" && "${mvn_env[@]}" mvn -q -DincludeScope=compile -Dtacplus.model.dir="$dir/model" \
    dependency:build-classpath -Dmdep.outputFile="$dir/classpath.txt") >"$dir/classpath.log" 2>&1 || {
    cat "$dir/classpath.log" >&2
    die "java classpath resolution failed"
  }
  assert_clean_log "$dir/classpath.log"

  local classpath="$compile_dir/target/classes:$(cat "$dir/classpath.txt")"
  local leg_dir="$dir/jwk-alg"
  mkdir -p "$leg_dir/classes"
  cp "$TESTDATA/JwkAlgTest.java" "$leg_dir/JwkAlgTest.java"

  local javac=javac java_bin=java
  if [[ -n "$java_env" ]]; then
    javac="${java_env#JAVA_HOME=}/bin/javac"
    java_bin="${java_env#JAVA_HOME=}/bin/java"
  fi
  echo "==> Building the java leg"
  "$javac" -Xlint:all -Werror --release 17 -cp "$classpath" -d "$leg_dir/classes" \
    "$leg_dir/JwkAlgTest.java" >"$dir/build.log" 2>&1 || {
    cat "$dir/build.log" >&2
    die "java leg build failed"
  }
  assert_clean_log "$dir/build.log"
  echo "==> Verifying the vectors with the generated java chassis"
  # --sun-misc-unsafe-memory-access=allow: protobuf-java still uses
  # sun.misc.Unsafe and JDK 24+ warns on first use.
  "$java_bin" --sun-misc-unsafe-memory-access=allow -cp "$leg_dir/classes:$classpath" \
    jwkalg.JwkAlgTest "$FIXTURE" >"$dir/test.log" 2>&1 || {
    cat "$dir/test.log" >&2
    die "java leg failed"
  }
  cat "$dir/test.log"
  assert_ran_all java "$dir/test.log"
}

for language in "${languages[@]}"; do
  "run_$language"
done

echo "jwk-alg OK (${languages[*]} reached every expect in $(basename "$FIXTURE"))"
