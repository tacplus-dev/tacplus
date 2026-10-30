#!/usr/bin/env bash
# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# Cross-language runtime smoke: a GO fixture server built from the generated
# go servers (canonical consumer-contract proto) hosts C2S gRPC + W2S
# HTTP/SSE/WebSocket endpoints on localhost; the PYTHON and TYPESCRIPT
# generated CLIENTS (full-client taxonomy) exercise them over the wire.
#
# This gate recovers the runtime-smoke intent of the pre-PAR-3 python/TS
# `both` consumers (see consumer-contract git history), whose bodies hosted
# python/TS s2sm servers in-process — impossible under the full-client
# taxonomy (python/TS emit no service runtime). Coverage:
#   - C2S gRPC runtime: token-provider gating, authorize/authenticate seams,
#     PERMISSION_DENIED / INVALID_ARGUMENT / INTERNAL codes, watch + list
#     streams, PAGE auto-pager, handler-not-reached invariants
#   - Fail-closed unwired-auth wrapper over the wire
#   - W2S HTTP/JSON unary, SSE streams, WebSocket upload + chat framing
#   - Audit accounting (rpc_completed/rpc_error per method kind) + trace
#     (request/response events, error propagation), observed via the fixture's
#     /fixture/stats endpoint
#
# Separate from consumer-contract because it needs go build + python + node
# runtimes CONCURRENTLY (server and clients live in different generations).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
GATE_DIR="$ROOT_DIR/cross-test/runtime-smoke"
# Server side generates from the canonical consumer-contract fixture (the
# go `both` path proven by the consumer-contract gate); the client side
# generates from this gate's mirror proto (full-client targets reject the
# canonical fixture's S2S client declarations).
SERVER_PROTO_SRC="$ROOT_DIR/cross-test/consumer-contract/proto"
CLIENT_PROTO_TEMPLATE="$GATE_DIR/proto"
WORK_ROOT="${TACPLUS_RUNTIME_SMOKE_WORKDIR:-}"
TACPLUS_BIN="${TACPLUS_BIN:-}"
KEEP_WORK="${TACPLUS_RUNTIME_SMOKE_KEEP:-false}"
PYTHON_BIN="${TACPLUS_PYTHON:-}"
CONSUMER_FILTER=""
FIXTURE_PID=""
WS_NPM_VERSION="8.18.0"
CONSUMER_TIMEOUT_SECONDS=300

usage() {
  cat <<'USAGE'
Usage:
  cross-test/runtime-smoke/run.sh [options]

Options:
  --consumer LANG   Run one consumer: python, typescript
  --workdir DIR     Use a specific temporary work directory
  --tacplus BIN     Use an existing tacplus binary
  --keep            Keep the temporary work directory
  -h, --help        Show this help

Environment:
  TACPLUS_BIN                       Existing tacplus binary to use
  TACPLUS_RUNTIME_SMOKE_WORKDIR     Work directory for generated outputs
  TACPLUS_RUNTIME_SMOKE_KEEP=true   Preserve generated outputs after success
  TACPLUS_PYTHON                    Python interpreter for grpc_tools.protoc
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
    --consumer)
      need_value "$1" "${2:-}"
      CONSUMER_FILTER="$2"
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

[[ -d "$SERVER_PROTO_SRC" ]] || die "server proto fixture not found: $SERVER_PROTO_SRC"
[[ -d "$CLIENT_PROTO_TEMPLATE" ]] || die "client proto fixture not found: $CLIENT_PROTO_TEMPLATE"

case "$CONSUMER_FILTER" in
  "" ) consumers=(python typescript) ;;
  python|typescript ) consumers=("$CONSUMER_FILTER") ;;
  * ) die "unsupported consumer: $CONSUMER_FILTER" ;;
esac

if [[ -z "$WORK_ROOT" ]]; then
  WORK_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/tacplus-runtime-smoke.XXXXXX")"
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
  if rg -n '(WARN|WARNING|warning:|npm warn|ERROR|panic:|fatal:|error TS[0-9]+|JsonWebKey)' "$log"; then
    die "warning/error output found in $log"
  fi
}

python_runner() {
  if [[ -n "$PYTHON_BIN" ]]; then
    printf '%s\n' "$PYTHON_BIN"
  else
    printf '%s\n' python3
  fi
}

python_has_grpc_tools() {
  local python="$1"
  "$python" - <<'PY' >/dev/null 2>&1
import grpc_tools.protoc
PY
}

prepend_pythonpath() {
  local dir="$1"
  case ":${PYTHONPATH:-}:" in
    *":$dir:"*) ;;
    *) export PYTHONPATH="$dir${PYTHONPATH:+:$PYTHONPATH}" ;;
  esac
}

ensure_python_codegen_env() {
  local python
  python="$(python_runner)"
  if python_has_grpc_tools "$python"; then
    PYTHON_BIN="$python"
    return
  fi
  local deps_dir="$WORK_ROOT/.python-codegen-deps"
  mkdir -p "$deps_dir"
  "$python" -m pip install -q --disable-pip-version-check --upgrade --target "$deps_dir" grpcio-tools >"$WORK_ROOT/python-codegen-install.log" 2>&1 || {
    cat "$WORK_ROOT/python-codegen-install.log" >&2
    die "failed to install grpcio-tools for codegen"
  }
  prepend_pythonpath "$deps_dir"
  PYTHON_BIN="$python"
  python_has_grpc_tools "$PYTHON_BIN" || die "python codegen interpreter lacks grpc_tools.protoc: $PYTHON_BIN"
}

prepare_client_proto() {
  local dst="$1"
  rm -rf "$dst"
  mkdir -p "$dst"
  cp -R "$CLIENT_PROTO_TEMPLATE/." "$dst/"
  cp "$SERVER_PROTO_SRC/dependencies.yaml" "$dst/dependencies.yaml"
  # C2S client generation injects ServiceHealthState (clients resolve service
  # instances via health); reuse the canonical minimal ServiceHealth fixture.
  mkdir -p "$dst/platform"
  cp "$SERVER_PROTO_SRC/platform/health.proto" "$dst/platform/health.proto"
  cat > "$dst/registry.yaml" <<'EOF'
# Managed by tacplus — do not edit IDs manually.
services:
    - name: contractclient
      id: 1
EOF
}

write_config() {
  local dir="$1"
  local language="$2"
  local proto_src="$3"
  mkdir -p "$dir"
  cat > "$dir/tacplus.yaml" <<EOF
language: "$language"
pkg: "com.gitlab.example.tacplus.consumer.contractclient"
src: "$proto_src"
output-dir: "model"
EOF
}

generate_model() {
  local language="$1"
  local dir="$2"
  local proto_src="$3"
  local log="$dir/generate.log"
  echo "==> Generating $language model"
  write_config "$dir" "$language" "$proto_src"
  (cd "$dir" && TACPLUS_PYTHON="$PYTHON_BIN" "$TACPLUS_BIN" build --config tacplus.yaml --src "$proto_src" --tag latest) >"$log" 2>&1 || {
    cat "$log" >&2
    die "$language generation failed"
  }
  assert_clean_log "$log"
}

# Matches the consumer-contract go compile module: the generation dir is the
# module root and model/ is a package tree inside it.
write_go_server_mod() {
  local mod="$1"
  cat > "$mod" <<'EOF'
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

write_fixture_mod() {
  local mod="$1"
  cat > "$mod" <<'EOF'
module gitlab.com/example/tacplus/consumer/contractclient/runtime-fixture

go 1.23

require gitlab.com/example/tacplus/consumer/contractclient v0.0.0

replace gitlab.com/example/tacplus/consumer/contractclient => ..
EOF
}

build_fixture_server() {
  local dir="$WORK_ROOT/go-server"
  generate_model go "$dir" "$SERVER_PROTO_SRC"
  write_go_server_mod "$dir/go.mod"
  local fixture_dir="$dir/fixture"
  mkdir -p "$fixture_dir"
  cp "$GATE_DIR/fixture/main.go" "$fixture_dir/main.go"
  write_fixture_mod "$fixture_dir/go.mod"
  echo "==> Building go fixture server"
  (cd "$fixture_dir" && go mod tidy && go build -o fixture-server .) >"$dir/fixture-build.log" 2>&1 || {
    cat "$dir/fixture-build.log" >&2
    die "go fixture server build failed"
  }
  assert_clean_log "$dir/fixture-build.log"
  FIXTURE_BIN="$fixture_dir/fixture-server"
}

json_port_field() {
  local file="$1"
  local field="$2"
  "$PYTHON_BIN" - "$file" "$field" <<'PY'
import json
import sys

with open(sys.argv[1]) as handle:
    line = handle.readline()
print(json.loads(line)[sys.argv[2]])
PY
}

start_fixture() {
  local log_dir="$1"
  local stdout_log="$log_dir/fixture-stdout.log"
  local stderr_log="$log_dir/fixture-stderr.log"
  "$FIXTURE_BIN" >"$stdout_log" 2>"$stderr_log" &
  FIXTURE_PID=$!
  local waited=0
  while [[ ! -s "$stdout_log" ]]; do
    kill -0 "$FIXTURE_PID" 2>/dev/null || {
      cat "$stderr_log" >&2
      die "fixture server exited before publishing ports"
    }
    sleep 0.1
    waited=$((waited + 1))
    [[ $waited -lt 100 ]] || die "fixture server did not publish ports within 10s"
  done
  TACPLUS_FIXTURE_GRPC="$(json_port_field "$stdout_log" grpc)"
  TACPLUS_FIXTURE_GRPC_UNWIRED="$(json_port_field "$stdout_log" grpcUnwired)"
  TACPLUS_FIXTURE_HTTP="$(json_port_field "$stdout_log" http)"
  export TACPLUS_FIXTURE_GRPC TACPLUS_FIXTURE_GRPC_UNWIRED TACPLUS_FIXTURE_HTTP
}

finish_fixture() {
  local log_dir="$1"
  stop_fixture
  if [[ -s "$log_dir/fixture-stderr.log" ]]; then
    cat "$log_dir/fixture-stderr.log" >&2
    die "fixture server wrote to stderr"
  fi
}

prepare_python_consumer() {
  local dir="$WORK_ROOT/python"
  local client_proto="$WORK_ROOT/client-proto"
  generate_model python "$dir" "$client_proto"
  local deps_dir="$dir/consumer-deps"
  mkdir -p "$deps_dir"
  echo "==> Installing python model"
  "$PYTHON_BIN" -m pip install -q --disable-pip-version-check --upgrade --target "$deps_dir" "$dir/model" >"$dir/pip-install.log" 2>&1 || {
    cat "$dir/pip-install.log" >&2
    die "python model install failed"
  }
  PYTHON_CONSUMER_DIR="$dir"
}

run_python_consumer() {
  local dir="$PYTHON_CONSUMER_DIR"
  local log_dir="$dir/logs"
  mkdir -p "$log_dir"
  start_fixture "$log_dir"
  echo "==> Running python runtime smoke"
  PYTHONPATH="$dir/consumer-deps:$dir/model${PYTHONPATH:+:$PYTHONPATH}" \
    timeout "$CONSUMER_TIMEOUT_SECONDS" "$PYTHON_BIN" "$GATE_DIR/consumers/consumer.py" >"$log_dir/consumer.log" 2>&1 || {
    cat "$log_dir/consumer.log" >&2
    cat "$log_dir/fixture-stderr.log" >&2 || true
    die "python runtime smoke failed"
  }
  assert_clean_log "$log_dir/consumer.log"
  finish_fixture "$log_dir"
}

prepare_typescript_consumer() {
  local dir="$WORK_ROOT/typescript"
  local client_proto="$WORK_ROOT/client-proto"
  generate_model typescript "$dir" "$client_proto"
  echo "==> Building typescript model"
  (cd "$dir/model" && npm install && npm run build) >"$dir/npm-build.log" 2>&1 || {
    cat "$dir/npm-build.log" >&2
    die "typescript model build failed"
  }
  # Node < 21 has no global WebSocket; the consumer polyfills from `ws`.
  (cd "$dir/model" && npm install --no-save "ws@$WS_NPM_VERSION") >"$dir/npm-ws-install.log" 2>&1 || {
    cat "$dir/npm-ws-install.log" >&2
    die "ws install failed"
  }
  local consumer_dir="$dir/consumer"
  rm -rf "$consumer_dir"
  mkdir -p "$consumer_dir"
  cp "$GATE_DIR/consumers/consumer.mjs" "$consumer_dir/consumer.mjs"
  ln -s "$dir/model/node_modules" "$consumer_dir/node_modules"
  TYPESCRIPT_CONSUMER_DIR="$dir"
}

run_typescript_consumer() {
  local dir="$TYPESCRIPT_CONSUMER_DIR"
  local log_dir="$dir/logs"
  mkdir -p "$log_dir"
  start_fixture "$log_dir"
  echo "==> Running typescript runtime smoke"
  (cd "$dir/consumer" && timeout "$CONSUMER_TIMEOUT_SECONDS" node consumer.mjs) >"$log_dir/consumer.log" 2>&1 || {
    cat "$log_dir/consumer.log" >&2
    cat "$log_dir/fixture-stderr.log" >&2 || true
    die "typescript runtime smoke failed"
  }
  assert_clean_log "$log_dir/consumer.log"
  finish_fixture "$log_dir"
}

ensure_python_codegen_env
prepare_client_proto "$WORK_ROOT/client-proto"
build_fixture_server

for consumer in "${consumers[@]}"; do
  case "$consumer" in
    python)
      prepare_python_consumer
      run_python_consumer
      ;;
    typescript)
      prepare_typescript_consumer
      run_typescript_consumer
      ;;
  esac
done

echo "runtime smoke OK (${consumers[*]})"
