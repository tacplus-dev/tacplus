#!/usr/bin/env bash
# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# Cross-language SERIALIZATION-EQUIVALENCE gate (LED-2 phase 2).
#
# Every other cross-test gate is source-parity (`cmd/soundness`), compile
# (`current-generation`, `consumer-contract`) or RPC-behavior (`runtime-smoke`,
# `server-runtime`). None of them ever compared a decoded FIELD VALUE across
# languages. This gate does exactly that and nothing else:
#
#   go encodes cross-test/wire-corpus/proto's CorpusRecord ONCE, deterministically,
#   and the bytes are CHECKED IN at cross-test/fixtures/wire_corpus.json. Go,
#   java, python, rust and typescript then each decode that same blob with their
#   GENERATED CorpusRecord type and assert every field equals the expected
#   constants in that same fixture.
#
# Five decoders, one corpus — not a 5x5 matrix. Go decodes its own encoding as
# the fifth leg (regression coverage) and additionally re-encodes the expected
# values and asserts the bytes are byte-identical to the checked-in golden,
# which is what pins the corpus.
#
# Covered shapes (and only these — see README.md for why): int64 at the +/-2^63
# boundary, uint64 above 2^53, map<string,string> with several entries, proto3
# `optional` in both the present and absent states, and a non-UTF-8 bytes field.
#
# Local/manual, like server-runtime and w2s-validate: it needs the go, maven,
# cargo, python and node toolchains concurrently, so it is NOT part of
# `just test`, `just soundness`, `just determinism` or `just current-generation`,
# and it is not in CI or the nightly matrix.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=../lib/cargo-target.sh
source "$ROOT_DIR/cross-test/lib/cargo-target.sh"
GATE_DIR="$ROOT_DIR/cross-test/wire-corpus"
PROTO_SRC="$GATE_DIR/proto"
FIXTURE="$ROOT_DIR/cross-test/fixtures/wire_corpus.json"
MODEL_PKG="com.gitlab.example.tacplus.corpus.corpusclient"
GO_MODULE="gitlab.com/example/tacplus/corpus/corpusclient"
WORK_ROOT="${TACPLUS_WIRE_CORPUS_WORKDIR:-}"
TACPLUS_BIN="${TACPLUS_BIN:-}"
KEEP_WORK="${TACPLUS_WIRE_CORPUS_KEEP:-false}"
LANGUAGE_FILTER=""
EMIT=false

usage() {
  cat <<'USAGE'
Usage:
  cross-test/wire-corpus/run.sh [options]

Options:
  --language LANG   Run one decoder: go, java, python, rust, typescript
  --emit            Re-encode the golden bytes in cross-test/fixtures/wire_corpus.json
                    from that file's own `expect` blocks (go only), then verify
  --workdir DIR     Use a specific temporary work directory
  --tacplus BIN     Use an existing tacplus binary
  --keep            Keep the temporary work directory
  -h, --help        Show this help

Environment:
  TACPLUS_BIN                        Existing tacplus binary to use
  TACPLUS_WIRE_CORPUS_WORKDIR        Work directory for generated outputs
  TACPLUS_WIRE_CORPUS_KEEP=true      Preserve generated outputs after success
USAGE
}

die() {
  echo "ERROR: $*" >&2
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
    --emit)
      EMIT=true
      shift
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
[[ -f "$FIXTURE" ]] || die "golden corpus not found: $FIXTURE"

if [[ "$EMIT" == true ]]; then
  languages=(go)
elif [[ -z "$LANGUAGE_FILTER" ]]; then
  languages=(go java python rust typescript)
else
  case "$LANGUAGE_FILTER" in
    go|java|python|rust|typescript ) languages=("$LANGUAGE_FILTER") ;;
    * ) die "unsupported language: $LANGUAGE_FILTER" ;;
  esac
fi

if [[ -z "$WORK_ROOT" ]]; then
  WORK_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/tacplus-wire-corpus.XXXXXX")"
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
  (cd "$ROOT_DIR" && go build -o "$TACPLUS_BIN" .)
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
	golang.org/x/net v0.62.0
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
  local decoder_dir="$dir/decoder"
  mkdir -p "$decoder_dir"
  cp "$GATE_DIR/decoder/go/main.go" "$decoder_dir/main.go"
  cat > "$decoder_dir/go.mod" <<EOF
module $GO_MODULE/wire-corpus-decoder

go 1.23

require $GO_MODULE v0.0.0

replace $GO_MODULE => ..
EOF
  echo "==> Building go decoder"
  (cd "$decoder_dir" && go mod tidy && go build -o decoder .) >"$dir/build.log" 2>&1 || {
    cat "$dir/build.log" >&2
    die "go decoder build failed"
  }
  assert_clean_log "$dir/build.log"
  if [[ "$EMIT" == true ]]; then
    echo "==> Emitting golden bytes"
    "$decoder_dir/decoder" -fixture "$FIXTURE" -emit || die "golden emit failed"
  fi
  echo "==> Decoding the golden corpus with the generated go types"
  "$decoder_dir/decoder" -fixture "$FIXTURE" || die "go decoder failed"
}

run_python() {
  command -v python3 >/dev/null || die "python3 is required for the python decoder"
  local dir="$WORK_ROOT/python"
  generate_model python "$dir"
  echo "==> Decoding the golden corpus with the generated python types"
  (cd "$dir/model" && PYTHONPATH="$dir/model" python3 "$GATE_DIR/decoder/python/decode.py" "$FIXTURE") \
    || die "python decoder failed"
}

run_rust() {
  command -v cargo >/dev/null || die "cargo is required for the rust decoder"
  local dir="$WORK_ROOT/rust"
  generate_model rust "$dir"
  local decoder_dir="$dir/decoder"
  mkdir -p "$decoder_dir/src"
  cp "$GATE_DIR/decoder/rust/main.rs" "$decoder_dir/src/main.rs"
  cat > "$decoder_dir/Cargo.toml" <<EOF
[package]
name = "wire-corpus-rust-decoder"
version = "0.1.0"
edition = "2021"

[dependencies]
corpusclient-model-types = { path = "$dir/model/types" }
prost = "0.14.1"
serde_json = "1.0.149"
EOF
  echo "==> Building rust decoder"
  (cd "$decoder_dir" && CARGO_TARGET_DIR="$CROSS_TEST_CARGO_TARGET" cargo build --quiet) >"$dir/build.log" 2>&1 || {
    cat "$dir/build.log" >&2
    die "rust decoder build failed"
  }
  assert_clean_log "$dir/build.log"
  echo "==> Decoding the golden corpus with the generated rust types"
  "$CROSS_TEST_CARGO_TARGET/debug/wire-corpus-rust-decoder" "$FIXTURE" || die "rust decoder failed"
}

run_typescript() {
  command -v npm >/dev/null || die "npm is required for the typescript decoder"
  local dir="$WORK_ROOT/typescript"
  generate_model typescript "$dir"
  echo "==> Building typescript model"
  (cd "$dir/model" && npm install && npm run build) >"$dir/build.log" 2>&1 || {
    cat "$dir/build.log" >&2
    die "typescript model build failed"
  }
  local decoder_dir="$dir/decoder"
  mkdir -p "$decoder_dir"
  cp "$GATE_DIR/decoder/typescript/decode.mjs" "$decoder_dir/decode.mjs"
  ln -s "$dir/model/node_modules" "$decoder_dir/node_modules"
  echo "==> Decoding the golden corpus with the generated typescript types"
  (cd "$decoder_dir" && node decode.mjs "$FIXTURE") || die "typescript decoder failed"
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
  command -v mvn >/dev/null || die "mvn is required for the java decoder"
  local dir="$WORK_ROOT/java"
  generate_model java "$dir"
  local compile_dir="$dir/compile"
  mkdir -p "$compile_dir"
  cp "$GATE_DIR/decoder/java/pom.xml" "$compile_dir/pom.xml"

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
  local decoder_dir="$dir/decoder"
  mkdir -p "$decoder_dir/classes"
  cp "$GATE_DIR/decoder/java/Decoder.java" "$decoder_dir/Decoder.java"

  local javac=javac java_bin=java
  if [[ -n "$java_env" ]]; then
    javac="${java_env#JAVA_HOME=}/bin/javac"
    java_bin="${java_env#JAVA_HOME=}/bin/java"
  fi
  echo "==> Building java decoder"
  "$javac" -Xlint:all -Werror --release 17 -cp "$classpath" -d "$decoder_dir/classes" \
    "$decoder_dir/Decoder.java" >"$dir/build.log" 2>&1 || {
    cat "$dir/build.log" >&2
    die "java decoder build failed"
  }
  assert_clean_log "$dir/build.log"
  echo "==> Decoding the golden corpus with the generated java types"
  # --sun-misc-unsafe-memory-access=allow: protobuf-java still uses
  # sun.misc.Unsafe and JDK 24+ warns on first use.
  "$java_bin" --sun-misc-unsafe-memory-access=allow -cp "$decoder_dir/classes:$classpath" \
    decoder.Decoder "$FIXTURE" || die "java decoder failed"
}

for language in "${languages[@]}"; do
  "run_$language"
done

if [[ "$EMIT" == true ]]; then
  echo "wire-corpus: golden bytes re-emitted — review the diff in $FIXTURE"
else
  echo "wire-corpus OK (${languages[*]} decoded the same golden bytes to the same values)"
fi
