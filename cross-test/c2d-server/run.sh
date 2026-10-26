#!/usr/bin/env bash
# Copyright (c) 2026 Seabeam LLC
# SPDX-License-Identifier: Apache-2.0
#
# Behavioral check of the generated standalone C2D host in Go, Java and Rust.
#
# The fixture is a daemon whose whole server surface is one service_c2d
# service and that needs no cluster, the shape of a local client daemon. For
# that shape the generated ServiceManager emits a standalone host entry (Go
# sm.ServeC2DStandalone, Java ServiceManager.serveC2DStandalone, Rust
# ServiceManager::serve_c2d_standalone) that hosts the generated wrappers on
# the declared socket_path through the same mount Run uses, with no config,
# identity, state store or TLS server (docs/API.md, C2D standalone host). This
# gate generates it, compiles the WHOLE generated model, then starts the
# daemon through that entry from code OUTSIDE the generated packages, exactly
# as a cluster-less daemon's main would, and calls it over the real Unix
# socket through the generated c2dm client: a tokenless valid call,
# server-side request and response validation, server streaming, the 0600
# socket mode, stale-socket removal, and on stop: drain, socket cleanup and
# one Handler shutdown call.
#
# Usage: run.sh [--language go|java|rust]   (default: all three)
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=../lib/cargo-target.sh
source "$ROOT_DIR/cross-test/lib/cargo-target.sh"
PROTO_SRC="$ROOT_DIR/cross-test/c2d-server/proto"
TESTDATA="$ROOT_DIR/cross-test/c2d-server/testdata"
# shellcheck source=../lib/java-compile.sh
source "$ROOT_DIR/cross-test/lib/java-compile.sh"

die() { echo "c2d-server check FAILED: $*" >&2; exit 1; }

LANGUAGES="go java rust"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --language) LANGUAGES="$2"; shift 2 ;;
    *) die "unknown argument: $1" ;;
  esac
done

TACPLUS_BIN="${TACPLUS_BIN:-$ROOT_DIR/target/tacplus-c2d-server-check}"
if [[ ! -x "$TACPLUS_BIN" || -z "${TACPLUS_BIN_PREBUILT:-}" ]]; then
  echo "==> Building tacplus binary"
  (cd "$ROOT_DIR" && go generate ./pkg/internal/proto/spec/... && go build -o "$TACPLUS_BIN" .)
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
  grep -q "c2dSrv := newC2DServer(C2DSocketPath," "$OUT/s2sm/s2sm.go" || die "Run does not mount the c2d server on the declared socket"
  grep -q "c2dSrv.Serve(taskCtx)" "$OUT/s2sm/s2sm.go"            || die "Run does not serve the c2d server"
  grep -q "^func ServeC2DStandalone(ctx context.Context, socketPath string, h Handler) error" "$OUT/s2sm/s2sm.go" \
    || die "standalone c2d host entry missing (c2d-only, cluster-free daemon)"

  cp "$TESTDATA/c2d_server_test.go" "$OUT/s2sm/"
  printf 'module example.com/c2dserver\n\ngo 1.23\n' > "$WORK/go/go.mod"

  echo "==> Compiling the whole generated Go model"
  (cd "$WORK/go" && go mod tidy && go vet ./model/...) || die "generated Go model does not compile"

  echo "==> Running generated Go standalone C2D host test"
  (cd "$WORK/go" && go test -count=1 -v -run TestGeneratedC2DStandaloneHostOverSocket ./model/s2sm/) >"$WORK/go/test.log" 2>&1 \
    || { tail -40 "$WORK/go/test.log"; die "generated Go standalone C2D host test failed"; }
  # A filter that matched nothing would pass vacuously.
  grep -q -- "--- PASS: TestGeneratedC2DStandaloneHostOverSocket" "$WORK/go/test.log" \
    || { cat "$WORK/go/test.log"; die "Go standalone C2D host test did not run"; }

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
  grep -q "C2DServer c2dSrv = newC2DServer(C2D_SOCKET_PATH," "$SM" || die "run() does not mount the c2d server on the declared socket"
  grep -q "c2dSrv.start();" "$SM"                || die "run() does not start the c2d server"
  grep -q "public static void serveC2DStandalone(String socketPath, Handler handler, java.util.concurrent.Future<?> stop) throws Exception" "$SM" \
    || die "standalone c2d host entry missing (c2d-only, cluster-free daemon)"

  # The self-test is a daemon main OUTSIDE the generated packages: it reaches
  # the host only through the public serveC2DStandalone. It compiles under the
  # same pom as the model.
  mkdir -p "$OUT/src/main/java/com/example/c2dserver/daemon"
  cp "$TESTDATA/C2DStandaloneSelfTest.java" "$OUT/src/main/java/com/example/c2dserver/daemon/"
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

  echo "==> Running generated Java standalone C2D host self-test"
  # grpc-netty logs every frame at DEBUG; keep it in a log and show the
  # self-test's own verdict.
  if ! "$java_cmd" --sun-misc-unsafe-memory-access=allow -cp "$compile_dir/target/classes:$(cat "$compile_dir/classpath")" \
      com.example.c2dserver.daemon.C2DStandaloneSelfTest >"$WORK/java/selftest.log" 2>&1; then
    grep -E "FAIL|Exception|check\(s\) failed" "$WORK/java/selftest.log" | head -40
    die "generated Java standalone C2D host self-test failed"
  fi
  grep "C2D standalone host self-test passed" "$WORK/java/selftest.log" || die "self-test exited 0 without its pass line"

  echo "==> c2d-server check PASSED (java)"
}

run_rust() {
  command -v cargo >/dev/null || die "cargo is required to compile the generated Rust model"
  generate rust
  local OUT="$WORK/rust/model"
  [[ -f "$OUT/s2sm/mod.rs" ]] || die "no generated s2sm module at $OUT/s2sm (c2d-only daemon got no ServiceManager)"

  echo "==> Asserting generated Rust C2D server seams"
  grep -q "^fn new_c2d_server(" "$OUT/s2sm/mod.rs"                  || die "ServiceManager c2d mount missing"
  grep -A1 "let c2d_server = new_c2d_server(" "$OUT/s2sm/mod.rs" | grep -q "C2D_SOCKET_PATH," \
    || die "run() does not mount the c2d server on the declared socket"
  grep -q "pub async fn serve_c2d_standalone<F>(" "$OUT/s2sm/mod.rs" \
    || die "standalone c2d host entry missing (c2d-only, cluster-free daemon)"

  # An integration test is its own crate: it reaches the host only through the
  # public serve_c2d_standalone.
  mkdir -p "$OUT/tests"
  cp "$TESTDATA/c2d_standalone_test.rs" "$OUT/tests/"

  echo "==> Running generated Rust standalone C2D host test"
  (cd "$OUT" && CARGO_TARGET_DIR="$CROSS_TEST_CARGO_TARGET" cargo test --test c2d_standalone_test) >"$WORK/rust/test.log" 2>&1 \
    || { tail -60 "$WORK/rust/test.log"; die "generated Rust standalone C2D host test failed"; }
  grep -q "test generated_c2d_standalone_host_over_socket ... ok" "$WORK/rust/test.log" \
    || { tail -40 "$WORK/rust/test.log"; die "Rust standalone C2D host test did not run"; }

  echo "==> c2d-server check PASSED (rust)"
}

for language in $LANGUAGES; do
  case "$language" in
    go) run_go ;;
    java) run_java ;;
    rust) run_rust ;;
    *) die "unsupported language: $language" ;;
  esac
done
