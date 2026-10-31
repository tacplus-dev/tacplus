#!/usr/bin/env bash
# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# The kitchensink release gate (tacplus#196): the generator is verified once,
# here, and every service built with this release inherits it.
#
# The kitchensink's own members (cross-test/kitchensink/members.yaml, gate:
# kitchensink) are generated with the tacplus of this checkout, without
# --allow-uncovered, and compiled:
#   proto          go, java, rust, typescript, swift, csharp
#   proto-service  go, java, rust
# Then the generated code of cross-test/kitchensink/proto is
#   verified: Rust with Kani, through `tacplus verify`, on the harnesses of
#             cross-test/kitchensink/verify/rust;
#   fuzzed:   Go (go test -fuzz, testdata/go), Rust (cargo fuzz, fuzz/rust)
#             and Java (fuzz/java), KITCHENSINK_FUZZ_SECONDS a target.
# Any failure fails the gate. It needs the build image's tools (Kani,
# cargo-fuzz and its nightly Rust, swift, dotnet): `just kitchensink-container`
# runs it there, and the Release workflow runs it before it publishes.
#
# Usage: run.sh [--language go|java|rust|typescript|swift|csharp]...
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
# shellcheck source=../lib/cargo-target.sh
source "$ROOT_DIR/cross-test/lib/cargo-target.sh"
# shellcheck source=../lib/tree-guard.sh
source "$ROOT_DIR/cross-test/lib/tree-guard.sh"
tree_guard_begin "$ROOT_DIR"
# shellcheck source=../lib/java-compile.sh
source "$ROOT_DIR/cross-test/lib/java-compile.sh"

KS="$ROOT_DIR/cross-test/kitchensink"
FUZZ_SECONDS="${KITCHENSINK_FUZZ_SECONDS:-30}"
all_languages=(go java rust typescript swift csharp)
service_languages=(go java rust)
languages=()

die() {
  echo "kitchensink: $*" >&2
  exit 1
}
step() { echo "==> $*"; }
# run LOG CMD...: runs CMD with its output in LOG; on failure shows the tail.
run() {
  local log="$1"
  shift
  "$@" >"$log" 2>&1 || {
    tail -60 "$log" >&2
    die "failed: $* (log: $log)"
  }
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --language)
      [[ $# -ge 2 ]] || die "--language needs a value"
      languages+=("$2")
      shift 2
      ;;
    -h | --help)
      sed -n '2,24p' "$0"
      exit 0
      ;;
    *) die "unknown argument $1" ;;
  esac
done
[[ ${#languages[@]} -gt 0 ]] || languages=("${all_languages[@]}")

W="$(mktemp -d "${TMPDIR:-/tmp}/tacplus-kitchensink-XXXXXX")"
trap 'rm -rf "$W"' EXIT
TACPLUS="$W/bin/tacplus"
step "building tacplus"
(cd "$ROOT_DIR" && go build -o "$TACPLUS" .)

# generate MEMBER LANG PKG: generates cross-test/kitchensink/MEMBER in LANG
# into $W/gen/MEMBER/LANG/model.
generate() {
  local member="$1" lang="$2" pkg="$3" dir="$W/gen/$1/$2"
  mkdir -p "$dir"
  cp -R "$KS/$member" "$dir/proto"
  printf 'language: %s\npkg: %s\nsrc: proto\noutput-dir: model\ngrpc-servers: all\n' "$lang" "$pkg" >"$dir/tacplus.yaml"
  step "$member: generate $lang"
  (cd "$dir" && run "$dir/generate.log" "$TACPLUS" build --config tacplus.yaml --src proto --tag latest)
  if find "$dir/model" -type f -name 'Proof.java' -print -quit | grep -q . ||
    grep -rqE --include='*.rs' --include='*.java' 'kani::proof|cfg\(kani\)|org\.cprover' "$dir/model"; then
    die "$member: the generated $lang carries a proof harness; services must not get one"
  fi
}

# compile MEMBER LANG MODULE: compiles (and, for Go, vets) the generated LANG.
compile() {
  local member="$1" lang="$2" module="$3" dir="$W/gen/$1/$2"
  local model="$dir/model"
  step "$member: compile $lang"
  case "$lang" in
    go)
      write_go_compile_mod "$dir/go.mod" "$module"
      (cd "$dir" && run "$dir/go-mod.log" go mod tidy)
      (cd "$dir" && run "$dir/go-vet.log" go vet ./...)
      ;;
    java)
      write_java_compile_pom "$model/pom.xml" "$model"
      (cd "$model" && run "$dir/maven-compile.log" mvn -q -DskipTests compile)
      ;;
    rust)
      (cd "$model" && run "$dir/cargo-check.log" cargo check)
      ;;
    typescript)
      (cd "$model" && run "$dir/npm-build.log" bash -c 'npm install && npm run build')
      ;;
    swift)
      (cd "$model" && run "$dir/swift-build.log" swift build)
      ;;
    csharp)
      (cd "$model" && run "$dir/dotnet-build.log" dotnet build --nologo)
      ;;
    *) die "unknown language $lang" ;;
  esac
}

# The Go module the generated code imports itself under: the reversed pkg.
write_go_compile_mod() {
  local mod="$1" module="$2"
  cat >"$mod" <<EOF
module $module

go 1.23
EOF
}

verify_rust() {
  local proj="$W/verify/rust"
  step "proto: verify rust (Kani)"
  mkdir -p "$proj"
  cp -R "$KS/verify/rust" "$proj/proof"
  printf 'language: rust\noutput-dir: model\n' >"$proj/tacplus.yaml"
  # Kani builds with its own compiler, so its artifacts cannot share the cache.
  (cd "$proj" && CARGO_TARGET_DIR="$proj/target" run "$proj/verify.log" "$TACPLUS" verify) # separate-cargo-target: Kani's compiler
  grep -q 'verification passed' "$proj/verify.log" || {
    tail -40 "$proj/verify.log" >&2
    die "tacplus verify ran no Kani harness"
  }
  grep -E 'verification (complete|passed)' "$proj/verify.log"
}

fuzz_go() {
  local dir="$W/gen/proto/go" pkg="./model/types/services/kitchensink/"
  cp "$KS/testdata/go/kitchensink_fuzz_test.go" "$dir/model/types/services/kitchensink/"
  local t
  for t in $(grep -oE '^func (Fuzz[A-Za-z]+)' "$KS/testdata/go/kitchensink_fuzz_test.go" | cut -d' ' -f2); do
    step "proto: fuzz go $t (${FUZZ_SECONDS}s)"
    (cd "$dir" && run "$dir/fuzz-$t.log" go test -run '^$' -fuzz "^$t\$" -fuzztime "${FUZZ_SECONDS}s" "$pkg")
  done
}

fuzz_rust() {
  local types="$W/gen/proto/rust/model/types"
  cp -R "$KS/fuzz/rust" "$types/fuzz"
  local t
  for t in $(sed -n 's/^name = "\([a-z_]*\)"$/\1/p' "$KS/fuzz/rust/Cargo.toml" | grep -v '^kitchensink-fuzz$'); do
    step "proto: fuzz rust $t (${FUZZ_SECONDS}s)"
    # cargo-fuzz builds with sanitizers on nightly, which would thrash the cache.
    export CARGO_TARGET_DIR="$types/fuzz/target" # separate-cargo-target: nightly + sanitizer flags
    (cd "$types" && run "$W/gen/proto/rust/fuzz-$t.log" \
      cargo +"${RUST_NIGHTLY:?the build image sets RUST_NIGHTLY}" fuzz run "$t" -- -max_total_time="$FUZZ_SECONDS")
    export CARGO_TARGET_DIR="$CROSS_TEST_CARGO_TARGET"
  done
}

fuzz_java() {
  local dir="$W/gen/proto/java" model="$W/gen/proto/java/model"
  step "proto: fuzz java (${FUZZ_SECONDS}s)"
  (cd "$model" && run "$dir/classpath.log" mvn -q dependency:build-classpath -Dmdep.outputFile="$dir/classpath.txt")
  local cp
  cp="$model/target/classes:$(cat "$dir/classpath.txt")"
  run "$dir/fuzz-javac.log" javac -nowarn -cp "$cp" -d "$dir/fuzz-classes" "$KS/fuzz/java/Fuzz.java"
  run "$dir/fuzz.log" java -cp "$dir/fuzz-classes:$cp" \
    com.example.kitchensink.model.types.services.kitchensink.Fuzz "$FUZZ_SECONDS" "${KITCHENSINK_FUZZ_SEED:-$RANDOM}"
  cat "$dir/fuzz.log"
}

for lang in "${languages[@]}"; do
  case "$lang" in
    go | java | rust | typescript | swift | csharp) ;;
    *) die "unknown language $lang (${all_languages[*]})" ;;
  esac
  generate proto "$lang" com.example.kitchensink
  compile proto "$lang" example.com/kitchensink
  if [[ " ${service_languages[*]} " == *" $lang "* ]]; then
    generate proto-service "$lang" com.example.kitchensink.service
    compile proto-service "$lang" example.com/kitchensink/service
  fi
  case "$lang" in
    go) fuzz_go ;;
    rust)
      verify_rust
      fuzz_rust
      ;;
    java) fuzz_java ;;
  esac
done

tree_guard_end
echo "kitchensink: OK (${languages[*]})"
