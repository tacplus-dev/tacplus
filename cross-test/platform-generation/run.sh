#!/usr/bin/env bash
set -euo pipefail

# Real-consumer generation gate (2026-08-22, A2 incident follow-up).
#
# All other mandatory gates build tacplus's own cross-test fixtures
# (cross-test/proto), never the real platform/testapp proto repos. A2
# (tacplus@a267b081) deleted the method_c2s/method_s2s MethodOptions
# extensions and passed every gate green while breaking generation for every
# real service in schematic/platform and schematic/testapp — the fixtures
# cannot see that class of break because they don't exercise real consumers.
#
# This gate closes that hole: it runs tacplus's own generate step (NOT the
# full testapp/build.sh — no docker, no artifact compile, no image push,
# none of which this gate needs) against the real platform + app proto repos
# for every service in the fleet, and asserts every one generates cleanly.
#
# It never writes into the live platform/testapp checkouts (2026-09-27,
# scotty fleet-build incident follow-up): a concurrent fleet build reads
# generated output from those same directories, so this gate copies each
# service dir into a scratch working copy and generates there instead.

ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
TACPLUS_BIN="${TACPLUS_BIN:-}"

# Defaults mirror testapp/build.sh: local checkouts if present, else clone.
PLATFORM_ROOT="${PLATFORM_ROOT:-$ROOT_DIR/../platform}"
APP_ROOT="${APP_ROOT:-$ROOT_DIR/../testapp}"
PLATFORM_PROTO="${PLATFORM_PROTO:-$PLATFORM_ROOT/proto}"
APP_PROTO="${APP_PROTO:-$APP_ROOT/proto}"
PROTO_TAG="${PROTO_TAG:-latest}"

PLATFORM_SVCS="${PLATFORM_SVCS:-eventbus gateway kernel kmaker oauth opa sentinel stratum}"
APP_SVCS="${APP_SVCS:-echo tasks}"
SVCS="$PLATFORM_SVCS $APP_SVCS"

die() { echo "ERROR: $*" >&2; exit 1; }

[[ -d "$PLATFORM_PROTO" ]] || die "PLATFORM_PROTO not found: $PLATFORM_PROTO"
[[ -d "$APP_PROTO" ]] || die "APP_PROTO not found: $APP_PROTO"

if [[ -z "$TACPLUS_BIN" ]]; then
  TACPLUS_BIN="$ROOT_DIR/target/tacplus-platform-generation"
  echo "==> Building local tacplus binary"
  (cd "$ROOT_DIR" && go build -o "$TACPLUS_BIN" .)
fi

svc_dir() {
  local svc="$1"
  if [[ -d "$APP_ROOT/services/$svc" ]]; then printf '%s' "$APP_ROOT/services/$svc"; return; fi
  printf '%s' "$PLATFORM_ROOT/services/$svc"
}

SCRATCH="$(mktemp -d "${TMPDIR:-/tmp}/platform-generation-gate.XXXXXX")"
trap 'rm -rf "$SCRATCH"' EXIT

echo "==> generating with platform + app protos (real consumer repos, scratch copies)"
echo "    platform: $PLATFORM_PROTO"
echo "    app:      $APP_PROTO"

FAILED=""
for svc in $SVCS; do
  live_d="$(svc_dir "$svc")"
  if [[ ! -d "$live_d" ]]; then
    echo "FAIL $svc — no such service directory: $live_d" >&2
    FAILED="$FAILED $svc"
    continue
  fi

  d="$SCRATCH/$svc"
  cp -a "$live_d" "$d"

  out="$(grep -E '^output-dir:' "$d/tacplus.yaml" 2>/dev/null | awk '{print $2}')"
  if [[ -n "$out" && "$out" != "." ]]; then
    rm -rf "${d:?}/${out:?}"
  elif [[ -n "$out" ]]; then
    rm -rf "${d:?}/model"
  fi

  log="/tmp/platform-generation-$svc.log"
  if ( cd "$d" && "$TACPLUS_BIN" build --tag "$PROTO_TAG" --src "$PLATFORM_PROTO" --src "$APP_PROTO" ) >"$log" 2>&1; then
    echo "  OK   $svc generated"
  else
    echo "  FAIL $svc — generation failed, see $log" >&2
    tail -20 "$log" >&2 || true
    FAILED="$FAILED $svc"
  fi
done

if [[ -n "$FAILED" ]]; then
  echo
  echo "platform-generation FAILED:$FAILED" >&2
  exit 1
fi

echo
echo "==> platform-generation gate passed (all services generated against real platform+app protos)"
