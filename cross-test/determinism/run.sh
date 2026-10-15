#!/usr/bin/env bash
set -euo pipefail

# Determinism gate (CLIENT_SURFACE_DETERMINISM_PLAN.md, Phase 8).
#
# Proves that tacplus generation is a pure function of its semantic inputs:
# generating the SAME proto graph + config into two separate work directories
# must produce BYTE-IDENTICAL model trees. Go map iteration order is randomized,
# so any generator that emits into generated output while ranging over a map
# leaks that randomization here and this gate fails.
#
# Nothing is ignored or normalized: the two model trees are compared with
# `diff -r` and must match exactly, INCLUDING .hash.json and registry.json.

ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
# shellcheck source=../lib/tree-guard.sh
source "$ROOT_DIR/cross-test/lib/tree-guard.sh"
tree_guard_begin "$ROOT_DIR"
PROTO_SRC="$ROOT_DIR/cross-test/proto"
# The requires-device fixture is the only proto graph in the tree that declares a
# (spec.service_state) with a plain singleton boundary (ServiceHealth.lock_settings),
# so it is the only one whose generation reaches the state-singleton emitters at all.
REQUIRESDEVICE_PROTO_SRC="$ROOT_DIR/cross-test/proto-requiresdevice"
WORK_ROOT="${TACPLUS_DETERMINISM_WORKDIR:-$ROOT_DIR/target/cross-test/determinism}"
TACPLUS_BIN="${TACPLUS_BIN:-}"
PYTHON_BIN="${TACPLUS_PYTHON:-}"
LANG_FILTER=""
KEEP=false

usage() {
  cat <<'USAGE'
Usage:
  cross-test/determinism/run.sh [options]

Options:
  --language LANG   Run one language: go, rust, python, typescript
  --workdir DIR     Use a specific work directory for generated outputs
  --tacplus BIN     Use an existing tacplus binary
  --keep            Keep generated outputs after success
  -h, --help        Show this help

Environment:
  TACPLUS_BIN                    Existing tacplus binary to use
  TACPLUS_DETERMINISM_WORKDIR    Work directory for generated outputs
  TACPLUS_PYTHON                 Python interpreter for grpc_tools.protoc
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
      KEEP=true
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

[[ -d "$PROTO_SRC" ]] || die "cross-test proto fixture not found: $PROTO_SRC"
[[ -d "$REQUIRESDEVICE_PROTO_SRC" ]] || die "requires-device proto fixture not found: $REQUIRESDEVICE_PROTO_SRC"

all_languages=(go rust python typescript java)
case "$LANG_FILTER" in
  "" ) languages=("${all_languages[@]}") ;;
  go|rust|python|typescript|java ) languages=("$LANG_FILTER") ;;
  * ) die "unsupported language: $LANG_FILTER" ;;
esac

if [[ -n "$PYTHON_BIN" && "$PYTHON_BIN" == */* && ! -x "$PYTHON_BIN" ]]; then
  die "TACPLUS_PYTHON is not executable: $PYTHON_BIN"
fi

mkdir -p "$WORK_ROOT"
WORK_ROOT="$(cd "$WORK_ROOT" && pwd)"

if [[ -z "$TACPLUS_BIN" ]]; then
  TACPLUS_BIN="$ROOT_DIR/target/tacplus-determinism"
  echo "==> Building local tacplus binary"
  (cd "$ROOT_DIR" && go generate ./pkg/internal/proto/spec/... && go build -o "$TACPLUS_BIN" .)
fi

if [[ "$TACPLUS_BIN" == */* ]]; then
  TACPLUS_BIN="$(cd "$(dirname "$TACPLUS_BIN")" && pwd)/$(basename "$TACPLUS_BIN")"
fi

# generate LANGUAGE into DIR with identical semantic config. Each run gets its
# own src copy + tacplus.yaml so the two runs differ ONLY by absolute path (a
# declared non-input) — nothing else.
generate() {
  local language="$1"
  local dir="$2"
  local proto_src="$3"
  local pkg="$4"

  rm -rf "$dir"
  mkdir -p "$dir/src"
  cp -R "$proto_src/." "$dir/src/"
  cat > "$dir/tacplus.yaml" <<EOF
language: "$language"
pkg: "$pkg"
src: "$dir/src"
output-dir: "model"
EOF
  if ! (cd "$dir" && TACPLUS_PYTHON="$PYTHON_BIN" "$TACPLUS_BIN" build --config tacplus.yaml --src "$dir/src" --tag latest >"$dir/gen.log" 2>&1); then
    tail -120 "$dir/gen.log" >&2 || true
    die "$language generation failed; log: $dir/gen.log"
  fi
}

# name|proto source|pkg|languages. The base fixture is types-only and generates
# for every target; requiresdevice is a full service build (it declares servers
# and S2S clients), so python and typescript — client-only targets — refuse it
# outright and it runs for the three full-service languages only.
fixtures=(
  "base|$PROTO_SRC|com.gitlab.example.dummy.proto|go rust python typescript java"
  "requiresdevice|$REQUIRESDEVICE_PROTO_SRC|com.gitlab.example.dummy.vault|go java rust"
)

for language in "${languages[@]}"; do
  for fixture in "${fixtures[@]}"; do
    IFS='|' read -r fixture_name fixture_src fixture_pkg fixture_langs <<<"$fixture"
    [[ " $fixture_langs " == *" $language "* ]] || continue

    echo "==> $language/$fixture_name: generating twice into separate work dirs"
    dir_a="$WORK_ROOT/$language/$fixture_name/a"
    dir_b="$WORK_ROOT/$language/$fixture_name/b"
    generate "$language" "$dir_a" "$fixture_src" "$fixture_pkg"
    generate "$language" "$dir_b" "$fixture_src" "$fixture_pkg"

    [[ -d "$dir_a/model" ]] || die "$language/$fixture_name: missing model tree in $dir_a"
    [[ -d "$dir_b/model" ]] || die "$language/$fixture_name: missing model tree in $dir_b"

    echo "==> $language/$fixture_name: asserting byte-identical model trees"
    if ! diff_out="$(diff -r "$dir_a/model" "$dir_b/model" 2>&1)"; then
      printf '%s\n' "$diff_out" >&2
      die "$language/$fixture_name generation is NOT deterministic: two runs produced different output"
    fi
    echo "    PASS $language/$fixture_name (byte-identical)"
  done
done

if [[ "$KEEP" != true ]]; then
  rm -rf "$WORK_ROOT"
fi

tree_guard_end
echo "==> Determinism gate passed"
