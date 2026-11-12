#!/usr/bin/env bash
# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# Trace-carry gate (issues #313, #327): a projection carries its source's
# trace_id, a generated store path clears it before it persists, and each
# generated helper that builds its own request has a traced form that puts the
# trace on it.
#
# go, java and rust each drive their GENERATED code over the same inputs
# (cross-test/trace-carry/proto):
#   - Extract<Row>From<Source> and <Source>ToProjections copy the source's
#     trace_id onto the projection;
#   - the record dispatch (Handle<Source>) hands its store a row with field 200
#     empty, read back raw from the bytes or JSON the store would hold;
#   - a write through the generated service_store disk helpers (snapshot and
#     redo record), then a raw read of the file and the record, shows no trace,
#     and the caller's own message keeps its trace;
#   - (#327, cross-test/trace-carry/proto-helpers) each state-client helper and
#     the record reader: the traced form puts the trace on every request it
#     sends, today's form none.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=../lib/cargo-target.sh
source "$ROOT_DIR/cross-test/lib/cargo-target.sh"
GATE_DIR="$ROOT_DIR/cross-test/trace-carry"
PROTO_SRC="$GATE_DIR/proto"
# The traced helper forms' model (#327): a state root of every boundary kind the
# state client builds requests for, and a record reader over a record plane.
HELPERS_PROTO_SRC="$GATE_DIR/proto-helpers"
HELPERS_MODEL_PKG="com.gitlab.example.tacplus.trace.tracehelpers"
HELPERS_GO_MODULE="gitlab.com/example/tacplus/trace/tracehelpers"
TESTDATA="$GATE_DIR/testdata"
MODEL_PKG="com.gitlab.example.tacplus.trace.tracecarry"
GO_MODULE="gitlab.com/example/tacplus/trace/tracecarry"
WORK_ROOT="${TACPLUS_TRACE_CARRY_WORKDIR:-}"
TACPLUS_BIN="${TACPLUS_BIN:-}"
KEEP_WORK="${TACPLUS_TRACE_CARRY_KEEP:-false}"
LANGUAGE_FILTER=""

usage() {
  cat <<'USAGE'
Usage:
  cross-test/trace-carry/run.sh [options]

Options:
  --language LANG   Run one leg: go, java, rust
  --workdir DIR     Use a specific temporary work directory
  --tacplus BIN     Use an existing tacplus binary
  --keep            Keep the temporary work directory
  -h, --help        Show this help

Environment:
  TACPLUS_BIN                    Existing tacplus binary to use
  TACPLUS_TRACE_CARRY_WORKDIR    Work directory for generated outputs
  TACPLUS_TRACE_CARRY_KEEP=true  Preserve generated outputs after success
USAGE
}

die() {
  echo "ERROR: trace-carry: $*" >&2
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

if [[ -z "$LANGUAGE_FILTER" ]]; then
  languages=(go java rust)
else
  case "$LANGUAGE_FILTER" in
    go|java|rust ) languages=("$LANGUAGE_FILTER") ;;
    * ) die "unsupported language: $LANGUAGE_FILTER" ;;
  esac
fi

if [[ -z "$WORK_ROOT" ]]; then
  WORK_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/tacplus-trace-carry.XXXXXX")"
else
  mkdir -p "$WORK_ROOT"
  WORK_ROOT="$(cd "$WORK_ROOT" && pwd)"
fi

cleanup() {
  stop_recorder
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

# Every leg prints one line `trace-carry <language>: <n> cases`; asserting it
# proves the leg ran every case rather than skipping.
assert_ran_all() {
  local want="$1"
  local log="$2"
  grep -q "$want" "$log" || die "leg did not report all cases ($want; see $log)"
}

# generate_model LANGUAGE DIR [PKG SRC DEPENDS]: the trace-carry model by
# default, or another proto set (the helpers' model) with its own depends.
generate_model() {
  local language="$1"
  local dir="$2"
  local pkg="${3:-$MODEL_PKG}"
  local src="${4:-$PROTO_SRC}"
  local depends="${5:-}"
  local log="$dir/generate.log"
  echo "==> Generating $language model ($(basename "$src"))"
  mkdir -p "$dir"
  cat > "$dir/tacplus.yaml" <<EOF
language: "$language"
pkg: "$pkg"
src: "$src"
output-dir: "model"
EOF
  if [[ -n "$depends" ]]; then
    echo "depends: $depends" >> "$dir/tacplus.yaml"
  fi
  (cd "$dir" && "$TACPLUS_BIN" build --config tacplus.yaml --src "$src" --tag latest) >"$log" 2>&1 || {
    cat "$log" >&2
    die "$language generation failed"
  }
  assert_clean_log "$log"
}

# Matches the consumer-contract go compile module: the generation dir is the
# module root and model/ is a package tree inside it.
write_go_mod() {
  cat > "$1" <<EOF
module ${2:-$GO_MODULE}

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


run_go() {
  local dir="$WORK_ROOT/go"
  generate_model go "$dir"
  write_go_mod "$dir/go.mod"
  cp "$TESTDATA/trace_carry_test.go" "$dir/model/types/trace/tracecarry/trace_carry_test.go"
  cp "$TESTDATA/trace_store_test.go" "$dir/model/s2sm/diskstore/trace_store_test.go"
  echo "==> Checking the generated go projections, dispatch and disk helpers"
  (cd "$dir" && go mod tidy && go test -count=1 -v -run '^TestTrace' ./model/types/trace/tracecarry ./model/s2sm/diskstore) \
    >"$dir/test.log" 2>&1 || {
    cat "$dir/test.log" >&2
    die "go leg failed"
  }
  grep -h 'trace-carry go' "$dir/test.log"
  assert_ran_all "trace-carry go: 2 cases" "$dir/test.log"
  assert_ran_all "trace-carry go store: 2 cases" "$dir/test.log"
  run_go_helpers
}

# The traced helper forms (#327): each state-client helper and the record
# reader, driven over a recording connection that reads field 200 off every
# request as it leaves. The traced form carries the trace; today's carries none.
run_go_helpers() {
  local dir="$WORK_ROOT/go-helpers"
  generate_model go "$dir" "$HELPERS_MODEL_PKG" "$HELPERS_PROTO_SRC" '["eventbus", "helperstratum"]'
  write_go_mod "$dir/go.mod" "$HELPERS_GO_MODULE"
  local state_pkg="$dir/model/s2sm/state/services/helper_state"
  sed 's/^package recorder$/package helper_state/' "$TESTDATA/helpers/recorder_test.go" >"$state_pkg/recorder_test.go"
  cp "$TESTDATA/helpers/state_test.go" "$state_pkg/trace_helpers_state_test.go"
  sed 's/^package recorder$/package sm/' "$TESTDATA/helpers/recorder_test.go" >"$dir/model/s2sm/recorder_test.go"
  cp "$TESTDATA/helpers/reader_test.go" "$dir/model/s2sm/trace_helpers_reader_test.go"
  # Every message of the generated sys contract has field 200: link each
  # generated sys package into the test so the registry holds them all.
  cp "$TESTDATA/helpers/sys_test.go" "$dir/model/s2sm/trace_helpers_sys_test.go"
  {
    printf 'package sm\n\nimport (\n'
    local sys_pkg
    for sys_pkg in "$dir"/model/types/sys/*/; do
      compgen -G "$sys_pkg*.pb.go" >/dev/null || continue
      sys_pkg="${sys_pkg%/}"
      printf '\t_ "%s/model/types/sys/%s"\n' "$HELPERS_GO_MODULE" "${sys_pkg##*/}"
    done
    printf ')\n'
  } >"$dir/model/s2sm/trace_helpers_sys_imports_test.go"
  echo "==> Checking the generated go traced helper forms"
  (cd "$dir" && go mod tidy && go test -count=1 -v -run '^TestTraceHelpers' ./model/s2sm/state/services/helper_state ./model/s2sm) \
    >"$dir/test.log" 2>&1 || {
    cat "$dir/test.log" >&2
    die "go helpers leg failed"
  }
  grep -h 'trace-helpers go' "$dir/test.log"
  assert_ran_all "trace-helpers go state: 15 cases" "$dir/test.log"
  assert_ran_all "trace-helpers go reader: 2 cases" "$dir/test.log"
  assert_ran_all "trace-helpers go sys: " "$dir/test.log"
}

# The recorder (testdata/helpers/recorder) the Java and Rust helper legs send
# their requests to: a gRPC server for any method that logs "<method>
# <trace_id>" per request. start_recorder LEG_DIR sets TRACE_HELPERS_ADDR and
# TRACE_HELPERS_LOG; the EXIT trap stops it by its PID.
RECORDER_PID=""
build_recorder() {
  local dir="$WORK_ROOT/recorder"
  [[ -x "$dir/recorder" ]] && return
  mkdir -p "$dir"
  cp "$TESTDATA/helpers/recorder/main.go" "$dir/main.go"
  write_go_mod "$dir/go.mod" "trace-helpers-recorder"
  (cd "$dir" && go mod tidy && go build -o recorder .) >"$dir/build.log" 2>&1 || {
    cat "$dir/build.log" >&2
    die "recorder build failed"
  }
}

start_recorder() {
  local leg_dir="$1"
  build_recorder
  rm -f "$leg_dir/recorder.addr"
  "$WORK_ROOT/recorder/recorder" "$leg_dir/recorder.addr" "$leg_dir/recorder.log" 2>"$leg_dir/recorder.err" &
  RECORDER_PID=$!
  local tries=0
  until [[ -s "$leg_dir/recorder.addr" ]]; do
    kill -0 "$RECORDER_PID" 2>/dev/null || { cat "$leg_dir/recorder.err" >&2; die "recorder did not start"; }
    (( tries++ < 100 )) || die "recorder did not report its address"
    sleep 0.1
  done
  export TRACE_HELPERS_ADDR TRACE_HELPERS_LOG
  TRACE_HELPERS_ADDR="$(cat "$leg_dir/recorder.addr")"
  TRACE_HELPERS_LOG="$leg_dir/recorder.log"
}

stop_recorder() {
  if [[ -n "$RECORDER_PID" ]]; then
    kill "$RECORDER_PID" 2>/dev/null || true
    wait "$RECORDER_PID" 2>/dev/null || true
    RECORDER_PID=""
  fi
}

# write_rust_sys_check MODEL_TYPES_DIR CRATE OUT: a Rust module whose
# sys_contract() builds every prost message of the generated sys contract with
# trace_id set, so a sys message without field 200 fails the leg's build (#327).
# Excluded, as in pkg/internal/proto/trace.go: sys/spec.proto's option schemas
# and trace.TraceEvent.
write_rust_sys_check() {
  local types_dir="$1" crate="$2" out="$3"
  {
    echo "// Generated by cross-test/trace-carry/run.sh (#327)."
    echo "pub fn sys_contract() -> usize {"
    echo "    let mut checked = 0;"
    local file pkg
    for file in "$types_dir"/sys/*/*.rs; do
      pkg="$(basename "$(dirname "$file")")"
      [[ "$(basename "$file")" == "$pkg.rs" && "$pkg" != spec ]] || continue
      awk -v crate="$crate" -v pkg="$pkg" '
        /#\[derive\(.*::prost::Message/ { message = 1; next }
        message && /^ *pub struct [A-Za-z0-9_]+ \{/ {
          name = $3
          message = 0
          if (pkg == "trace" && name == "TraceEvent") next
          printf "    { let m = %s::sys::%s::%s { trace_id: \"t\".to_string(), ..Default::default() }; assert_eq!(m.trace_id, \"t\"); checked += 1; }\n", crate, pkg, name
          next
        }
        /^ *pub (struct|enum|mod) / { message = 0 }
      ' "$file"
    done
    echo "    checked"
    echo "}"
  } >"$out"
}

run_rust() {
  command -v cargo >/dev/null || die "cargo is required for the rust leg"
  local dir="$WORK_ROOT/rust"
  generate_model rust "$dir"
  local leg_dir="$dir/trace-carry"
  mkdir -p "$leg_dir/src"
  cp "$TESTDATA/trace_carry.rs" "$leg_dir/src/main.rs"
  cat > "$leg_dir/Cargo.toml" <<EOF
[package]
name = "trace-carry-rust"
version = "0.1.0"
edition = "2021"

[dependencies]
tracecarry-model = { path = "$dir/model" }
tracecarry-model-types = { path = "$dir/model/types" }
diskstore = { path = "$dir/model/chassis/diskstore" }
serde_json = "1.0.149"
EOF
  echo "==> Building the rust leg"
  (cd "$leg_dir" && CARGO_TARGET_DIR="$CROSS_TEST_CARGO_TARGET" cargo build --quiet) >"$dir/build.log" 2>&1 || {
    cat "$dir/build.log" >&2
    die "rust leg build failed"
  }
  assert_clean_log "$dir/build.log"
  echo "==> Checking the generated rust projections, dispatch and disk helpers"
  "$CROSS_TEST_CARGO_TARGET/debug/trace-carry-rust" >"$dir/test.log" 2>&1 || {
    cat "$dir/test.log" >&2
    die "rust leg failed"
  }
  cat "$dir/test.log"
  assert_ran_all "trace-carry rust: 4 cases" "$dir/test.log"
  prune_cargo_workspace "$leg_dir" "$dir/model"
  run_rust_helpers
}

# The traced helper forms (#327), Rust: the state client's and the record
# reader's, against the recorder; and every generated sys message has field 200.
run_rust_helpers() {
  local dir="$WORK_ROOT/rust-helpers"
  generate_model rust "$dir" "$HELPERS_MODEL_PKG" "$HELPERS_PROTO_SRC" '["eventbus", "helperstratum"]'
  local leg_dir="$dir/trace-helpers"
  mkdir -p "$leg_dir/src"
  cp "$TESTDATA/helpers/trace_helpers.rs" "$leg_dir/src/main.rs"
  write_rust_sys_check "$dir/model/types" tracehelpers_model_types "$leg_dir/src/sys_check.rs"
  # tokio and tonic at the model's own workspace versions.
  local tokio_dep tonic_dep
  tokio_dep="$(grep -m1 '^tokio = ' "$dir/model/Cargo.toml")" || die "the rust model pins no tokio"
  tonic_dep="$(grep -m1 '^tonic = ' "$dir/model/Cargo.toml")" || die "the rust model pins no tonic"
  cat > "$leg_dir/Cargo.toml" <<EOF
[package]
name = "trace-helpers-rust"
version = "0.1.0"
edition = "2021"

[dependencies]
tracehelpers-model = { path = "$dir/model" }
tracehelpers-model-types = { path = "$dir/model/types" }
servicemanager = { path = "$dir/model/chassis/servicemanager" }
$tokio_dep
$tonic_dep
EOF
  echo "==> Building the rust helpers leg"
  (cd "$leg_dir" && CARGO_TARGET_DIR="$CROSS_TEST_CARGO_TARGET" cargo build --quiet) >"$dir/build.log" 2>&1 || {
    cat "$dir/build.log" >&2
    die "rust helpers leg build failed"
  }
  assert_clean_log "$dir/build.log"
  echo "==> Checking the generated rust traced helper forms"
  start_recorder "$leg_dir"
  "$CROSS_TEST_CARGO_TARGET/debug/trace-helpers-rust" >"$dir/test.log" 2>&1 || {
    cat "$dir/test.log" >&2
    stop_recorder
    die "rust helpers leg failed"
  }
  stop_recorder
  cat "$dir/test.log"
  assert_ran_all "trace-helpers rust state: 4 cases" "$dir/test.log"
  assert_ran_all "trace-helpers rust reader: 2 cases" "$dir/test.log"
  assert_ran_all "trace-helpers rust sys: " "$dir/test.log"
  prune_cargo_workspace "$leg_dir" "$dir/model"
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
  local leg_dir="$dir/trace-carry"
  mkdir -p "$leg_dir/classes"
  cp "$TESTDATA/TraceCarry.java" "$leg_dir/TraceCarry.java"

  local javac=javac java_bin=java
  if [[ -n "$java_env" ]]; then
    javac="${java_env#JAVA_HOME=}/bin/javac"
    java_bin="${java_env#JAVA_HOME=}/bin/java"
  fi
  echo "==> Building the java leg"
  "$javac" -Xlint:all -Werror --release 17 -cp "$classpath" -d "$leg_dir/classes" \
    "$leg_dir/TraceCarry.java" >"$dir/build.log" 2>&1 || {
    cat "$dir/build.log" >&2
    die "java leg build failed"
  }
  assert_clean_log "$dir/build.log"
  echo "==> Checking the generated java projections, dispatch and disk helpers"
  # --sun-misc-unsafe-memory-access=allow: protobuf-java still uses
  # sun.misc.Unsafe and JDK 24+ warns on first use.
  "$java_bin" --sun-misc-unsafe-memory-access=allow -cp "$leg_dir/classes:$classpath" \
    tracecarry.TraceCarry >"$dir/test.log" 2>&1 || {
    cat "$dir/test.log" >&2
    die "java leg failed"
  }
  cat "$dir/test.log"
  assert_ran_all "trace-carry java: 4 cases" "$dir/test.log"
  run_java_helpers
}

# The traced helper forms (#327), Java: the state client's and the record
# reader's overloads, against the recorder; and every generated sys message has
# field 200, read off the descriptors of the sys outer classes.
run_java_helpers() {
  local dir="$WORK_ROOT/java-helpers"
  generate_model java "$dir" "$HELPERS_MODEL_PKG" "$HELPERS_PROTO_SRC" '["eventbus", "helperstratum"]'
  local compile_dir="$dir/compile"
  mkdir -p "$compile_dir"
  cp "$TESTDATA/pom.xml" "$compile_dir/pom.xml"

  local java_env
  java_env="$(java_home_env)"
  local mvn_env=()
  [[ -n "$java_env" ]] && mvn_env=(env "$java_env")

  echo "==> Compiling generated java helpers model"
  (cd "$compile_dir" && "${mvn_env[@]}" mvn -q -DskipTests -Dtacplus.model.dir="$dir/model" compile) \
    >"$dir/maven-compile.log" 2>&1 || {
    cat "$dir/maven-compile.log" >&2
    die "generated java helpers model compile failed"
  }
  (cd "$compile_dir" && "${mvn_env[@]}" mvn -q -DincludeScope=compile -Dtacplus.model.dir="$dir/model" \
    dependency:build-classpath -Dmdep.outputFile="$dir/classpath.txt") >"$dir/classpath.log" 2>&1 || {
    cat "$dir/classpath.log" >&2
    die "java helpers classpath resolution failed"
  }
  assert_clean_log "$dir/classpath.log"

  local classpath="$compile_dir/target/classes:$(cat "$dir/classpath.txt")"
  local leg_dir="$dir/trace-helpers"
  mkdir -p "$leg_dir/classes"
  cp "$TESTDATA/helpers/TraceHelpers.java" "$leg_dir/TraceHelpers.java"
  # The generated sys outer classes: each declares its file's descriptor.
  local java_root="$dir/model/src/main/java"
  grep -rl 'public static com.google.protobuf.Descriptors.FileDescriptor' "$java_root" \
    | grep '/types/sys/' | sed "s#^$java_root/##; s#\.java\$##; s#/#.#g" | sort >"$leg_dir/sys-outer-classes.txt"

  local javac=javac java_bin=java
  if [[ -n "$java_env" ]]; then
    javac="${java_env#JAVA_HOME=}/bin/javac"
    java_bin="${java_env#JAVA_HOME=}/bin/java"
  fi
  echo "==> Building the java helpers leg"
  "$javac" -Xlint:all -Werror --release 17 -cp "$classpath" -d "$leg_dir/classes" \
    "$leg_dir/TraceHelpers.java" >"$dir/build.log" 2>&1 || {
    cat "$dir/build.log" >&2
    die "java helpers leg build failed"
  }
  assert_clean_log "$dir/build.log"
  echo "==> Checking the generated java traced helper forms"
  start_recorder "$leg_dir"
  "$java_bin" --sun-misc-unsafe-memory-access=allow -cp "$leg_dir/classes:$classpath" \
    tracehelpers.TraceHelpers "$leg_dir/sys-outer-classes.txt" >"$dir/test.log" 2>&1 || {
    cat "$dir/test.log" >&2
    stop_recorder
    die "java helpers leg failed"
  }
  stop_recorder
  grep '^trace-helpers java' "$dir/test.log"
  assert_ran_all "trace-helpers java state: 4 cases" "$dir/test.log"
  assert_ran_all "trace-helpers java reader: 2 cases" "$dir/test.log"
  assert_ran_all "trace-helpers java sys: " "$dir/test.log"
}

for language in "${languages[@]}"; do
  "run_$language"
done

echo "trace-carry OK (${languages[*]})"
