#!/usr/bin/env bash
# Copyright (c) 2026 Seabeam LLC
# SPDX-License-Identifier: Apache-2.0
#
# Behavioral check of the generated C2D server in Go and Java.
#
# The fixture is a daemon whose whole server surface is one service_c2d
# service, the shape of a local client daemon. The generated ServiceManager
# hosts it on the declared socket_path with no TLS server and no token auth
# (docs/API.md, C2D server auth-skip scope). This gate generates it, compiles
# the WHOLE generated model (the c2d-only ServiceManager shape included), then
# hosts the generated wrapper through the same generated mount the
# ServiceManager uses (Go newC2DServer, Java ServiceManager.newC2DServer) and
# calls it over the real Unix socket through the generated c2dm client: a
# tokenless valid call, server-side request and response validation, server
# streaming, the 0600 socket mode, stale-socket removal and socket cleanup.
#
# Usage: run.sh [--language go|java]   (default: both)
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PROTO_SRC="$ROOT_DIR/cross-test/c2d-server/proto"
TESTDATA="$ROOT_DIR/cross-test/c2d-server/testdata"
# shellcheck source=../lib/java-compile.sh
source "$ROOT_DIR/cross-test/lib/java-compile.sh"

die() { echo "c2d-server check FAILED: $*" >&2; exit 1; }

LANGUAGES="go java"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --language) LANGUAGES="$2"; shift 2 ;;
    *) die "unknown argument: $1" ;;
  esac
done

TACPLUS_BIN="${TACPLUS_BIN:-$ROOT_DIR/target/tacplus-c2d-server-check}"
if [[ ! -x "$TACPLUS_BIN" || -z "${TACPLUS_BIN_PREBUILT:-}" ]]; then
  echo "==> Building tacplus binary"
  (cd "$ROOT_DIR" && go build -o "$TACPLUS_BIN" .)
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

generate() {
  local language="$1"
  local dir="$WORK/$language"
  mkdir -p "$dir"
  cat > "$dir/tacplus.yaml" <<EOF2
language: "$language"
pkg: "com.example.c2dserver"
src: "$PROTO_SRC"
output-dir: "model"
EOF2
  echo "==> Generating C2D server fixture ($language)"
  (cd "$dir" && "$TACPLUS_BIN" build --config tacplus.yaml --src "$PROTO_SRC") >"$dir/generate.log" 2>&1 \
    || { tail -40 "$dir/generate.log"; die "generation failed ($language)"; }
}

run_go() {
  # The Go import root is the pkg's reverse-DNS domain + the output dir
  # (example.com/c2dserver/model), which the test imports.
  generate go
  local OUT="$WORK/go/model"
  [[ -d "$OUT/s2sm" ]] || die "no generated s2sm package at $OUT/s2sm (c2d-only daemon got no ServiceManager)"

  echo "==> Asserting generated Go C2D server seams"
  grep -q "^func NewC2DServer(" "$OUT/chassis/grpcserver/c2d.go" || die "chassis C2DServer missing"
  grep -q "^func newC2DServer(" "$OUT/s2sm/s2sm.go"              || die "ServiceManager c2d mount missing"
  grep -q "c2dSrv.Serve(taskCtx)" "$OUT/s2sm/s2sm.go"            || die "Run does not serve the c2d server"

  cp "$TESTDATA/c2d_server_test.go" "$OUT/s2sm/"
  printf 'module example.com/c2dserver\n\ngo 1.23\n' > "$WORK/go/go.mod"

  echo "==> Compiling the whole generated Go model"
  (cd "$WORK/go" && go mod tidy && go vet ./model/...) || die "generated Go model does not compile"

  echo "==> Running generated Go C2D server test"
  (cd "$WORK/go" && go test -count=1 -run TestGeneratedC2DServerOverSocket ./model/s2sm/) \
    || die "generated Go C2D server test failed"

  echo "==> c2d-server check PASSED (go)"
}

run_java() {
  command -v mvn >/dev/null || die "mvn is required to compile the generated Java model"
  generate java
  local OUT="$WORK/java/model"
  local SM
  SM="$(find "$OUT/src/main/java" -type f -path '*/s2sm/ServiceManager.java' -print -quit)"
  [[ -n "$SM" ]] || die "no generated s2sm ServiceManager.java under $OUT (c2d-only daemon got no ServiceManager)"
  local C2D
  C2D="$(find "$OUT/src/main/java" -type f -path '*/chassis/grpcserver/C2DServer.java' -print -quit)"

  echo "==> Asserting generated Java C2D server seams"
  [[ -n "$C2D" ]] || die "chassis C2DServer.java missing"
  grep -q "static C2DServer newC2DServer(" "$SM" || die "ServiceManager c2d mount missing"
  grep -q "c2dSrv.start();" "$SM"                || die "run() does not start the c2d server"

  # The self-test lives beside the ServiceManager (it calls the package-private
  # newC2DServer) and compiles under the same pom as the model.
  cp "$TESTDATA/C2DServerSelfTest.java" "$(dirname "$SM")/"
  local compile_dir="$WORK/java/compile"
  mkdir -p "$compile_dir"
  write_java_compile_pom "$compile_dir/pom.xml" "$OUT"
  local java_env
  java_env="$(java_home_env)"
  echo "==> Compiling the whole generated Java model"
  (cd "$compile_dir" && env ${java_env:+"$java_env"} mvn -q -DskipTests compile) >"$WORK/java/maven-compile.log" 2>&1 \
    || { cat "$WORK/java/maven-compile.log"; die "generated Java model did not compile"; }
  (cd "$compile_dir" && env ${java_env:+"$java_env"} mvn -q dependency:build-classpath -Dmdep.outputFile="$compile_dir/classpath") >"$WORK/java/maven-classpath.log" 2>&1 \
    || { cat "$WORK/java/maven-classpath.log"; die "could not resolve the generated Java model's classpath"; }
  local java_cmd="java"
  if [[ -n "$java_env" && -x "${java_env#JAVA_HOME=}/bin/java" ]]; then
    java_cmd="${java_env#JAVA_HOME=}/bin/java"
  fi

  echo "==> Running generated Java C2D server self-test"
  # grpc-netty logs every frame at DEBUG; keep it in a log and show the
  # self-test's own verdict.
  if ! "$java_cmd" --sun-misc-unsafe-memory-access=allow -cp "$compile_dir/target/classes:$(cat "$compile_dir/classpath")" \
      com.example.c2dserver.model.s2sm.C2DServerSelfTest >"$WORK/java/selftest.log" 2>&1; then
    grep -E "FAIL|Exception|check\(s\) failed" "$WORK/java/selftest.log" | head -40
    die "generated Java C2D server self-test failed"
  fi
  grep "C2D server self-test passed" "$WORK/java/selftest.log" || die "self-test exited 0 without its pass line"

  echo "==> c2d-server check PASSED (java)"
}

for language in $LANGUAGES; do
  case "$language" in
    go) run_go ;;
    java) run_java ;;
    *) die "unsupported language: $language" ;;
  esac
done
