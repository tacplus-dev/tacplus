#!/usr/bin/env bash
# Copyright (c) 2026 Seabeam LLC
# SPDX-License-Identifier: Apache-2.0
#
# Behavioral W2S request-validation fixture for the generated REST dispatch
# plane, in Rust, Go and Java.
#
# The generated `rest_dispatch::dispatch` / `dispatch_stream` (Rust),
# `restdispatch.Dispatch` / `DispatchStream` (Go) and `RestDispatch.dispatch`
# / `dispatchStream` (Java) are the only generated W2S SERVER surface any
# language emits, and nothing in-repo hosts them, so the ingress (request)
# validation leg was unpinned while the egress (response) leg was already
# covered. This gate generates a real service whose C2S backend
# has an `assert_email` request constraint, hosts the REAL generated dispatch
# functions against a REAL gRPC backend, and drives both an invalid and a valid
# request through each of the unary and SSE legs.
#
# The same fixture carries a (spec.rest_browser) browser form target, whose
# form-urlencoded body and 303 answer are driven through the generated
# `form_body_to_json` / `redirect_response` (Rust), `FormBodyToJSON` /
# `RedirectResponse` (Go) and `formBodyToJson` / `redirectResponse` (Java)
# seams.
#
# Usage: run.sh [--language rust|go|java]   (default: all three)
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=../lib/tree-guard.sh
source "$ROOT_DIR/cross-test/lib/tree-guard.sh"
tree_guard_begin "$ROOT_DIR"
PROTO_SRC="$ROOT_DIR/cross-test/w2s-validate/proto"
TESTDATA="$ROOT_DIR/cross-test/w2s-validate/testdata"
# shellcheck source=../lib/java-compile.sh
source "$ROOT_DIR/cross-test/lib/java-compile.sh"

die() { echo "w2s-validate check FAILED: $*" >&2; exit 1; }

LANGUAGES="rust go java"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --language) LANGUAGES="$2"; shift 2 ;;
    *) die "unknown argument: $1" ;;
  esac
done

TACPLUS_BIN="${TACPLUS_BIN:-$ROOT_DIR/target/tacplus-w2s-validate-check}"
if [[ ! -x "$TACPLUS_BIN" || -z "${TACPLUS_BIN_PREBUILT:-}" ]]; then
  echo "==> Building tacplus binary"
  (cd "$ROOT_DIR" && go build -o "$TACPLUS_BIN" .)
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

generate() {
  local language="$1"
  local output_dir="$2"
  local dir="$WORK/$language"
  mkdir -p "$dir"
  cat > "$dir/tacplus.yaml" <<EOF2
language: "$language"
pkg: "com.example.w2svalidate"
src: "$PROTO_SRC"
output-dir: "$output_dir"
EOF2
  echo "==> Generating W2S request-validation fixture ($language)"
  (cd "$dir" && "$TACPLUS_BIN" build --config tacplus.yaml --src "$PROTO_SRC") \
    || die "generation failed ($language)"
}

run_rust() {
  generate rust tacplus
  local OUT="$WORK/rust/tacplus/model"
  [[ -d "$OUT" ]] || die "no generated output at $OUT"

  local DISPATCH="$OUT/s2sm/rest_dispatch/mod.rs"
  echo "==> Asserting generated Rust REST dispatch seams"
  [[ -f "$DISPATCH" ]] || die "no generated rest_dispatch module at $DISPATCH"
  grep -q "pub async fn dispatch(" "$DISPATCH"        || die "unary dispatch missing"
  grep -q "pub async fn dispatch_stream(" "$DISPATCH" || die "streaming dispatch missing"
  grep -q "pub async fn form_body_to_json(" "$DISPATCH" || die "form body decode missing"
  grep -q "pub fn redirect_response(" "$DISPATCH"      || die "redirect response missing"

  cp "$TESTDATA/w2s_request_validation_test.rs" "$OUT/s2sm/w2s_request_validation_test.rs"
  cp "$TESTDATA/rest_browser_form_test.rs" "$OUT/s2sm/rest_browser_form_test.rs"
  printf '\n#[cfg(test)]\nmod w2s_request_validation_test;\n' >> "$OUT/s2sm/mod.rs"
  printf '\n#[cfg(test)]\nmod rest_browser_form_test;\n' >> "$OUT/s2sm/mod.rs"

  echo "==> Running generated Rust W2S request-validation and REST browser-form tests"
  (cd "$OUT" && CARGO_TARGET_DIR="$OUT/target" cargo test -p w2svalidate-model -- rust_w2s_ rust_rest_browser_) \
    || die "generated Rust W2S request-validation / REST browser-form tests failed"

  echo "==> w2s-validate check PASSED (rust)"
}

run_go() {
  # The Go import root is the pkg's reverse-DNS domain + the output dir
  # (example.com/w2svalidate/model), which the tests import, so the output dir
  # is "model" (which is also the output name, so the two collapse) and the
  # module sits beside it.
  generate go model
  local OUT="$WORK/go/model"
  [[ -d "$OUT" ]] || die "no generated output at $OUT"

  local DISPATCH="$OUT/s2sm/rest_dispatch/rest_dispatch.go"
  echo "==> Asserting generated Go REST dispatch seams"
  [[ -f "$DISPATCH" ]] || die "no generated rest_dispatch package at $DISPATCH"
  grep -q "^func Dispatch(" "$DISPATCH"        || die "unary dispatch missing"
  grep -q "^func DispatchStream(" "$DISPATCH"  || die "streaming dispatch missing"
  grep -q "^func FormBodyToJSON(" "$DISPATCH"  || die "form body decode missing"
  grep -q "^func RedirectResponse(" "$DISPATCH" || die "redirect response missing"

  cp "$TESTDATA/w2s_request_validation_test.go" "$OUT/s2sm/rest_dispatch/"
  cp "$TESTDATA/rest_browser_form_test.go" "$OUT/s2sm/rest_dispatch/"
  printf 'module example.com/w2svalidate\n\ngo 1.23\n' > "$WORK/go/go.mod"

  echo "==> Running generated Go W2S request-validation and REST browser-form tests"
  (cd "$WORK/go" && go mod tidy && go test ./model/s2sm/rest_dispatch/) \
    || die "generated Go W2S request-validation / REST browser-form tests failed"

  echo "==> w2s-validate check PASSED (go)"
}

run_java() {
  command -v mvn >/dev/null || die "mvn is required to compile the generated Java model"
  generate java model
  local OUT="$WORK/java/model"
  [[ -d "$OUT/src/main/java" ]] || die "no generated output at $OUT"

  local DISPATCH
  DISPATCH="$(find "$OUT/src/main/java" -type f -path '*/s2sm/restdispatch/RestDispatch.java' -print -quit)"
  echo "==> Asserting generated Java REST dispatch seams"
  [[ -n "$DISPATCH" ]] || die "no generated RestDispatch.java under $OUT"
  grep -q "public static String dispatch(" "$DISPATCH"        || die "unary dispatch missing"
  grep -q "public static void dispatchStream(" "$DISPATCH"    || die "streaming dispatch missing"
  grep -q "public static String formBodyToJson(" "$DISPATCH"  || die "form body decode missing"
  grep -q "public static boolean redirectResponse(" "$DISPATCH" || die "redirect response missing"

  # The self-test lives beside the plane, in the main()-style the generated
  # Java self-tests use, and compiles under the same pom as the model.
  cp "$TESTDATA/RestDispatchSelfTest.java" "$(dirname "$DISPATCH")/"
  local compile_dir="$WORK/java/compile"
  mkdir -p "$compile_dir"
  write_java_compile_pom "$compile_dir/pom.xml" "$OUT"
  local java_env
  java_env="$(java_home_env)"
  echo "==> Compiling generated Java model"
  (cd "$compile_dir" && env ${java_env:+"$java_env"} mvn -q -DskipTests compile) >"$WORK/java/maven-compile.log" 2>&1 \
    || { cat "$WORK/java/maven-compile.log"; die "generated Java model did not compile"; }
  (cd "$compile_dir" && env ${java_env:+"$java_env"} mvn -q dependency:build-classpath -Dmdep.outputFile="$compile_dir/classpath") >"$WORK/java/maven-classpath.log" 2>&1 \
    || { cat "$WORK/java/maven-classpath.log"; die "could not resolve the generated Java model's classpath"; }
  local java_cmd="java"
  if [[ -n "$java_env" && -x "${java_env#JAVA_HOME=}/bin/java" ]]; then
    java_cmd="${java_env#JAVA_HOME=}/bin/java"
  fi
  local class_name
  class_name="$(awk '/^package /{gsub(";","",$2); print $2; exit}' "$TESTDATA/RestDispatchSelfTest.java").RestDispatchSelfTest"

  echo "==> Running generated Java W2S request-validation and REST browser-form self-test"
  "$java_cmd" --sun-misc-unsafe-memory-access=allow -cp "$compile_dir/target/classes:$(cat "$compile_dir/classpath")" "$class_name" \
    || die "generated Java W2S request-validation / REST browser-form self-test failed"

  echo "==> w2s-validate check PASSED (java)"
}

for language in $LANGUAGES; do
  case "$language" in
    rust) run_rust ;;
    go) run_go ;;
    java) run_java ;;
    *) die "unsupported language: $language" ;;
  esac
done
tree_guard_end
