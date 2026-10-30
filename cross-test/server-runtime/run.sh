#!/usr/bin/env bash
# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# Cross-language RUNTIME gate for java and rust (LED-2 phases 1 and 3).
#
# runtime-smoke covers a GO generated server driven by python/typescript
# generated clients. Java and Rust generated code had never crossed a language
# boundary over the wire in either direction — both only ever appeared as
# self-generated client+server pairs inside a single process
# (consumer-contract). This gate closes both halves:
#
#   SERVER half (phase 1):
#   - a real generated JAVA ContractApi server wrapper, hosted on a TCP port,
#     driven by the generated GO C2S client
#   - a real generated RUST ContractApi ValidationWrapper, likewise, driven by
#     the generated GO C2S client
#
#   CLIENT half (phase 3):
#   - the real generated JAVA C2S client driving the RUST-hosted server
#   - the real generated RUST C2S client driving the JAVA-hosted server
#
# The fixtures implement ONLY the generated handler interfaces (and the auth
# seams the generator exposes). Validation, PAGE token encoding, egress
# sanitization, auth gating and dispatch are the generated wrappers' code; the
# drivers are the generated C2S clients and do nothing by hand.
#
# Coverage per leg: auth_disabled unary, auth_any_user server-streaming
# (SNAPSHOT), auth_any_user PAGE with keyset continuation across two pages, and
# the fail-closed rejection of an auth_entitled_users method before the handler.
#
# NOT covered here, deliberately: the full N x N client/server matrix
# (python/typescript/swift/csharp as driving clients against java/rust hosts),
# W2S surfaces against java/rust hosts, and Watch/client-streaming/bidi legs.
#
# Separate from runtime-smoke and NOT part of `just test`: it needs the go, java
# (maven) and rust (cargo) toolchains concurrently.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=../lib/cargo-target.sh
source "$ROOT_DIR/cross-test/lib/cargo-target.sh"
GATE_DIR="$ROOT_DIR/cross-test/server-runtime"
PROTO_SRC="$ROOT_DIR/cross-test/consumer-contract/proto"
WORK_ROOT="${TACPLUS_SERVER_RUNTIME_WORKDIR:-}"
TACPLUS_BIN="${TACPLUS_BIN:-}"
KEEP_WORK="${TACPLUS_SERVER_RUNTIME_KEEP:-false}"
SERVER_FILTER=""
CLIENT_FILTER=""
FIXTURE_PID=""
CLIENT_TIMEOUT_SECONDS=300

usage() {
  cat <<'USAGE'
Usage:
  cross-test/server-runtime/run.sh [options]

Options:
  --server LANG     Run one hosted-server leg (driven by the go client): java, rust
  --client LANG     Run one driving-client leg (against the other language's
                    hosted server): java, rust
  --workdir DIR     Use a specific temporary work directory
  --tacplus BIN     Use an existing tacplus binary
  --keep            Keep the temporary work directory
  -h, --help        Show this help

With no --server/--client filter both halves run. Passing only one of them
narrows the run to that half.

Environment:
  TACPLUS_BIN                          Existing tacplus binary to use
  TACPLUS_SERVER_RUNTIME_WORKDIR       Work directory for generated outputs
  TACPLUS_SERVER_RUNTIME_KEEP=true     Preserve generated outputs after success
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
    --server)
      need_value "$1" "${2:-}"
      SERVER_FILTER="$2"
      shift 2
      ;;
    --client)
      need_value "$1" "${2:-}"
      CLIENT_FILTER="$2"
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

case "$SERVER_FILTER" in
  "" ) servers=(java rust) ;;
  java|rust ) servers=("$SERVER_FILTER") ;;
  * ) die "unsupported server: $SERVER_FILTER" ;;
esac

case "$CLIENT_FILTER" in
  "" ) clients=(java rust) ;;
  java|rust ) clients=("$CLIENT_FILTER") ;;
  * ) die "unsupported client: $CLIENT_FILTER" ;;
esac

# Passing only one filter narrows the run to that half.
if [[ -n "$SERVER_FILTER" && -z "$CLIENT_FILTER" ]]; then
  clients=()
elif [[ -n "$CLIENT_FILTER" && -z "$SERVER_FILTER" ]]; then
  servers=()
fi

# Each driving client is paired with the OTHER language's hosted server: that is
# the pairing this repo had no evidence for at all, and it reuses the servers
# this gate already hosts instead of standing up new fixtures.
peer_server_for_client() {
  case "$1" in
    java) echo rust ;;
    rust) echo java ;;
    *) die "no peer server for client: $1" ;;
  esac
}

if [[ -z "$WORK_ROOT" ]]; then
  WORK_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/tacplus-server-runtime.XXXXXX")"
else
  mkdir -p "$WORK_ROOT"
  WORK_ROOT="$(cd "$WORK_ROOT" && pwd)"
fi

stop_fixture() {
  if [[ -n "$FIXTURE_PID" ]] && kill -0 "$FIXTURE_PID" 2>/dev/null; then
    kill -TERM "$FIXTURE_PID" 2>/dev/null || true
    wait "$FIXTURE_PID" 2>/dev/null || true
  fi
  FIXTURE_PID=""
}

cleanup() {
  stop_fixture
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

write_config() {
  local dir="$1"
  local language="$2"
  mkdir -p "$dir"
  cat > "$dir/tacplus.yaml" <<EOF
language: "$language"
pkg: "com.gitlab.example.tacplus.consumer.contractclient"
src: "$PROTO_SRC"
output-dir: "model"
EOF
}

generate_model() {
  local language="$1"
  local dir="$2"
  local log="$dir/generate.log"
  echo "==> Generating $language model"
  write_config "$dir" "$language"
  (cd "$dir" && "$TACPLUS_BIN" build --config tacplus.yaml --src "$PROTO_SRC" --tag latest) >"$log" 2>&1 || {
    cat "$log" >&2
    die "$language generation failed"
  }
  assert_clean_log "$log"
}

# Matches the consumer-contract go compile module: the generation dir is the
# module root and model/ is a package tree inside it.
write_go_mod() {
  cat > "$1" <<'EOF'
module gitlab.com/example/tacplus/consumer/contractclient

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

write_client_mod() {
  cat > "$1" <<'EOF'
module gitlab.com/example/tacplus/consumer/contractclient/server-runtime-client

go 1.23

require gitlab.com/example/tacplus/consumer/contractclient v0.0.0

replace gitlab.com/example/tacplus/consumer/contractclient => ..
EOF
}

# The go DRIVING client: the generated go C2S client, built once and pointed at
# each hosted server in turn.
build_go_client() {
  local dir="$WORK_ROOT/go-client"
  generate_model go "$dir"
  write_go_mod "$dir/go.mod"
  local client_dir="$dir/client"
  mkdir -p "$client_dir"
  cp "$GATE_DIR/client/go/main.go" "$client_dir/main.go"
  write_client_mod "$client_dir/go.mod"
  echo "==> Building go generated-client driver"
  (cd "$client_dir" && go mod tidy && go build -o client .) >"$dir/client-build.log" 2>&1 || {
    cat "$dir/client-build.log" >&2
    die "go client build failed"
  }
  assert_clean_log "$dir/client-build.log"
  GO_CLIENT_BIN="$client_dir/client"
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

# Generates and compiles the java model once; both the java fixture server and
# the java driving client link against it.
JAVA_MODEL_READY=false
build_java_model() {
  [[ "$JAVA_MODEL_READY" == true ]] && return
  command -v mvn >/dev/null || die "mvn is required to build the java model"
  local dir="$WORK_ROOT/java"
  generate_model java "$dir"
  local output="$dir/model"
  local compile_dir="$dir/compile"
  mkdir -p "$compile_dir"
  cp "$GATE_DIR/fixture/java/pom.xml" "$compile_dir/pom.xml"

  local java_env
  java_env="$(java_home_env)"
  local mvn_env=()
  [[ -n "$java_env" ]] && mvn_env=(env "$java_env")

  echo "==> Compiling generated java model"
  (cd "$compile_dir" && "${mvn_env[@]}" mvn -q -DskipTests -Dtacplus.model.dir="$output" compile) >"$dir/maven-compile.log" 2>&1 || {
    cat "$dir/maven-compile.log" >&2
    die "generated java model compile failed"
  }
  (cd "$compile_dir" && "${mvn_env[@]}" mvn -q -DincludeScope=compile -Dtacplus.model.dir="$output" dependency:build-classpath -Dmdep.outputFile="$dir/classpath.txt") >"$dir/classpath.log" 2>&1 || {
    cat "$dir/classpath.log" >&2
    die "java classpath resolution failed"
  }
  assert_clean_log "$dir/classpath.log"

  JAVA_MODEL_DIR="$dir"
  JAVA_CLASSPATH="$compile_dir/target/classes:$(cat "$dir/classpath.txt")"
  JAVA_JAVAC=javac
  JAVA_BIN=java
  if [[ -n "$java_env" ]]; then
    JAVA_JAVAC="${java_env#JAVA_HOME=}/bin/javac"
    JAVA_BIN="${java_env#JAVA_HOME=}/bin/java"
  fi
  JAVA_MODEL_READY=true
}

# Compiles one java fixture source (server or client) against the model.
compile_java_source() {
  local kind="$1"
  local src="$2"
  local dir="$JAVA_MODEL_DIR/$kind"
  mkdir -p "$dir/classes"
  cp "$src" "$dir/$(basename "$src")"
  echo "==> Compiling java $kind"
  "$JAVA_JAVAC" -Xlint:all -Werror --release 17 -cp "$JAVA_CLASSPATH" -d "$dir/classes" "$dir/$(basename "$src")" \
    >"$JAVA_MODEL_DIR/$kind-build.log" 2>&1 || {
    cat "$JAVA_MODEL_DIR/$kind-build.log" >&2
    die "java $kind build failed"
  }
  assert_clean_log "$JAVA_MODEL_DIR/$kind-build.log"
  JAVA_COMPILED_CLASSPATH="$dir/classes:$JAVA_CLASSPATH"
}

build_java_server() {
  build_java_model
  compile_java_source fixture "$GATE_DIR/fixture/java/Fixture.java"
  JAVA_FIXTURE_CLASSPATH="$JAVA_COMPILED_CLASSPATH"
  JAVA_LOG_DIR="$JAVA_MODEL_DIR"
}

build_java_client() {
  build_java_model
  compile_java_source client "$GATE_DIR/client/java/Client.java"
  JAVA_CLIENT_CLASSPATH="$JAVA_COMPILED_CLASSPATH"
}

# Generates the rust model once; both the rust fixture server and the rust
# driving client link against it.
RUST_MODEL_READY=false
build_rust_model() {
  [[ "$RUST_MODEL_READY" == true ]] && return
  command -v cargo >/dev/null || die "cargo is required to build the rust fixtures"
  local dir="$WORK_ROOT/rust"
  generate_model rust "$dir"
  RUST_MODEL_DIR="$dir"
  RUST_MODEL_READY=true
}

# Builds one rust fixture crate (server or client) against the model.
build_rust_crate() {
  local kind="$1"
  local src="$2"
  local crate="server-runtime-rust-$kind"
  local dir="$RUST_MODEL_DIR/$kind"
  mkdir -p "$dir/src"
  cp "$src" "$dir/src/main.rs"
  cat > "$dir/Cargo.toml" <<EOF
[package]
name = "$crate"
version = "0.1.0"
edition = "2021"

[dependencies]
collection = { path = "$RUST_MODEL_DIR/model/chassis/collection" }
contractclient-model = { path = "$RUST_MODEL_DIR/model" }
futures = "0.3.31"
grpcserver = { path = "$RUST_MODEL_DIR/model/chassis/grpcserver" }
servicemanager = { path = "$RUST_MODEL_DIR/model/chassis/servicemanager" }
tokio = { version = "1.49.0", features = ["rt-multi-thread", "macros", "net", "signal", "sync"] }
tokio-stream = { version = "0.1.17", features = ["net"] }
tonic = "0.14.0"
EOF
  echo "==> Building rust $kind"
  (cd "$dir" && CARGO_TARGET_DIR="$CROSS_TEST_CARGO_TARGET" cargo build --quiet) >"$RUST_MODEL_DIR/$kind-build.log" 2>&1 || {
    cat "$RUST_MODEL_DIR/$kind-build.log" >&2
    die "rust $kind build failed"
  }
  assert_clean_log "$RUST_MODEL_DIR/$kind-build.log"
  RUST_CRATE_BIN="$CROSS_TEST_CARGO_TARGET/debug/$crate"
}

build_rust_server() {
  build_rust_model
  build_rust_crate fixture "$GATE_DIR/fixture/rust/main.rs"
  RUST_FIXTURE_BIN="$RUST_CRATE_BIN"
  RUST_LOG_DIR="$RUST_MODEL_DIR"
}

build_rust_client() {
  build_rust_model
  build_rust_crate client "$GATE_DIR/client/rust/main.rs"
  RUST_CLIENT_BIN="$RUST_CRATE_BIN"
}

# Starts a fixture server, waits for its {"grpc":"host:port"} startup line, and
# exports the address.
start_fixture() {
  local log_dir="$1"
  shift
  local stdout_log="$log_dir/fixture-stdout.log"
  local stderr_log="$log_dir/fixture-stderr.log"
  rm -f "$stdout_log" "$stderr_log"
  "$@" >"$stdout_log" 2>"$stderr_log" &
  FIXTURE_PID=$!
  local waited=0
  while [[ ! -s "$stdout_log" ]]; do
    kill -0 "$FIXTURE_PID" 2>/dev/null || {
      cat "$stderr_log" >&2
      die "fixture server exited before publishing its port"
    }
    sleep 0.1
    waited=$((waited + 1))
    [[ $waited -lt 300 ]] || die "fixture server did not publish its port within 30s"
  done
  SERVER_ADDR="$(sed -n 's/.*"grpc":"\([^"]*\)".*/\1/p' "$stdout_log" | head -n 1)"
  [[ -n "$SERVER_ADDR" ]] || {
    cat "$stdout_log" >&2
    die "could not parse the fixture server port line"
  }
}

# Starts the hosted server for one language and leaves SERVER_ADDR set.
start_server_for() {
  case "$1" in
    java)
      build_java_server
      # --sun-misc-unsafe-memory-access=allow: protobuf-java/netty still use
      # sun.misc.Unsafe; JDK 24+ warns on first use and this gate asserts the
      # fixture server's stderr stays empty.
      start_fixture "$JAVA_LOG_DIR" "$JAVA_BIN" --sun-misc-unsafe-memory-access=allow \
        -Dlogback.configurationFile="$GATE_DIR/fixture/java/logback.xml" \
        -cp "$JAVA_FIXTURE_CLASSPATH" fixture.Fixture
      SERVER_LOG_DIR="$JAVA_LOG_DIR"
      ;;
    rust)
      build_rust_server
      start_fixture "$RUST_LOG_DIR" "$RUST_FIXTURE_BIN"
      SERVER_LOG_DIR="$RUST_LOG_DIR"
      ;;
    *)
      die "unsupported server: $1"
      ;;
  esac
}

finish_fixture() {
  local log_dir="$1"
  stop_fixture
  if [[ -s "$log_dir/fixture-stderr.log" ]]; then
    cat "$log_dir/fixture-stderr.log" >&2
    die "fixture server wrote to stderr"
  fi
}

# Runs one driving client against the currently started server.
run_client_against() {
  local client_lang="$1"
  local server_lang="$2"
  local log_dir="$3"
  shift 3
  local log="$log_dir/client-$client_lang.log"
  echo "==> Driving the $server_lang generated server with the $client_lang generated client ($SERVER_ADDR)"
  TACPLUS_SERVER_ADDR="$SERVER_ADDR" TACPLUS_SERVER_LANG="$server_lang" \
    timeout "$CLIENT_TIMEOUT_SECONDS" "$@" >"$log" 2>&1 || {
    cat "$log" >&2
    cat "$log_dir/fixture-stderr.log" >&2 || true
    die "$client_lang generated client failed against the $server_lang generated server"
  }
  cat "$log"
  assert_clean_log "$log"
}

if [[ ${#servers[@]} -gt 0 ]]; then
  build_go_client
fi

# SERVER half: each hosted server driven by the go generated client.
for server in "${servers[@]:-}"; do
  [[ -n "$server" ]] || continue
  start_server_for "$server"
  run_client_against go "$server" "$SERVER_LOG_DIR" "$GO_CLIENT_BIN"
  finish_fixture "$SERVER_LOG_DIR"
done

# CLIENT half: each generated client driving the OTHER language's hosted server.
for client in "${clients[@]:-}"; do
  [[ -n "$client" ]] || continue
  peer="$(peer_server_for_client "$client")"
  case "$client" in
    java)
      build_java_client
      start_server_for "$peer"
      run_client_against java "$peer" "$SERVER_LOG_DIR" \
        "$JAVA_BIN" --sun-misc-unsafe-memory-access=allow \
        -Dlogback.configurationFile="$GATE_DIR/fixture/java/logback.xml" \
        -cp "$JAVA_CLIENT_CLASSPATH" client.Client
      ;;
    rust)
      build_rust_client
      start_server_for "$peer"
      run_client_against rust "$peer" "$SERVER_LOG_DIR" "$RUST_CLIENT_BIN"
      ;;
  esac
  finish_fixture "$SERVER_LOG_DIR"
done

summary=""
if [[ ${#servers[@]} -gt 0 ]]; then
  summary="${servers[*]} generated servers driven by the go generated client"
fi
if [[ ${#clients[@]} -gt 0 ]]; then
  pairs=()
  for client in "${clients[@]}"; do
    pairs+=("$client-client->$(peer_server_for_client "$client")-server")
  done
  [[ -n "$summary" ]] && summary="$summary; "
  summary="$summary${pairs[*]}"
fi
echo "server-runtime OK ($summary)"
