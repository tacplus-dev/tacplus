#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
LANG_FILTER="all"
TACPLUS_BIN=""
WORK_ROOT=""
KEEP_WORK=false

usage() {
  cat <<'USAGE'
Usage:
  cross-test/consumer-smoke/run.sh [options]

Options:
  --language LANG   Run one smoke language: go, java, python, rust, typescript, all
  --workdir DIR     Use a specific temporary work directory
  --tacplus BIN     Use an existing tacplus binary
  --keep            Keep the temporary work directory
  -h, --help        Show this help

Environment:
  TACPLUS_BIN                 Existing tacplus binary to use
  TACPLUS_CONSUMER_WORKDIR    Work directory for generated outputs
  TACPLUS_CONSUMER_KEEP=true  Preserve generated outputs after success
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
      LANG_FILTER="$2"
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

case "$LANG_FILTER" in
  ""|all)
    languages=(go java python rust typescript)
    ;;
  go|java|python|rust|typescript)
    languages=("$LANG_FILTER")
    ;;
  *)
    die "unsupported smoke language: $LANG_FILTER"
    ;;
esac

run_contract_case() {
  local language="$1"
  local case_name="$2"
  local cmd=("$ROOT_DIR/cross-test/consumer-contract/run.sh")
  if [[ -n "$WORK_ROOT" ]]; then
    cmd+=(--workdir "$WORK_ROOT")
  fi
  if [[ -n "$TACPLUS_BIN" ]]; then
    cmd+=(--tacplus "$TACPLUS_BIN")
  fi
  if [[ "$KEEP_WORK" == true ]]; then
    cmd+=(--keep)
  fi
  cmd+=(--language "$language" --case "$case_name")
  "${cmd[@]}"
}

for language in "${languages[@]}"; do
  case "$language" in
    go)
      echo "==> consumer-smoke/go: C2S gRPC plus W2S HTTP/SSE/WebSocket runtime checks"
      run_contract_case go both

      echo "==> consumer-smoke/go: C2D Unix-socket runtime checks"
      run_contract_case go c2d
      ;;
    java)
      echo "==> consumer-smoke/java: generated W2S WebSocket/auth runtime check"
      run_contract_case java both
      ;;
    python)
      echo "==> consumer-smoke/python: C2S+W2S client-surface build and wiring checks (client-only)"
      run_contract_case python both
      ;;
    rust)
      echo "==> consumer-smoke/rust: generated W2S WebSocket/auth runtime check"
      run_contract_case rust both
      ;;
    typescript)
      echo "==> consumer-smoke/typescript: C2S+W2S client-surface build and wiring checks (client-only)"
      run_contract_case typescript both
      ;;
  esac
done
