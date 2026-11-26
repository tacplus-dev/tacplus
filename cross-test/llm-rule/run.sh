#!/usr/bin/env bash
# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# The LLM rule as a generated build gate (#909): an LLM may propose; it never
# holds a token or a write path. Generates the Rust model of
# cross-test/proto-zerorpc (an S2S client to llmproxy) with tacplus.yaml
# advisor: {path: advisor}, then:
#
#   green: an advisor whose only dependency of the service is the generated
#          model port builds and passes its own test, and a core that hands it
#          the generated ModelPortAdapter builds beside it;
#   red:   for each way an advisor's manifest could reach the service (a
#          chassis crate such as the token, the model crate with its state and
#          S2S clients, the types crate, the core, any other local crate, a
#          renamed or workspace-inherited one, git, a patch), building the
#          model crate (which every build of the core builds) fails, and the
#          failure is the gate's, naming the dependency.
#
# Then the same for Go, on cross-test/proto-advisor: the gate there is the
# generated model/advisor/llm_rule_test.go, over what `go list` resolves.
# Java refuses advisor: until its gate follows (#909).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=../lib/cargo-target.sh
source "$ROOT_DIR/cross-test/lib/cargo-target.sh"
GATE_DIR="$ROOT_DIR/cross-test/llm-rule"
PROTO_SRC="$ROOT_DIR/cross-test/proto-zerorpc"
WORK_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/llm-rule.XXXXXX")"

cleanup() {
  rm -rf "$WORK_ROOT"
}
trap cleanup EXIT

die() {
  echo "ERROR: $*" >&2
  exit 1
}

command -v cargo >/dev/null || die "cargo is required"
command -v git >/dev/null || die "git is required"
[[ -d "$PROTO_SRC" ]] || die "proto fixture not found: $PROTO_SRC"

TACPLUS_BIN="${TACPLUS_BIN:-$WORK_ROOT/tacplus-local}"
if [[ ! -x "$TACPLUS_BIN" ]]; then
  echo "==> Building local tacplus binary"
  (cd "$ROOT_DIR" && go build -o "$TACPLUS_BIN" .)
fi

PROJECT="$WORK_ROOT/zerorpc"
mkdir -p "$PROJECT"
cat > "$PROJECT/tacplus.yaml" <<EOF
language: "rust"
kind: service
pkg: "com.gitlab.example.dummy.zerorpc"
src: "$PROTO_SRC"
output-dir: "model"
depends:
  - llmproxy
advisor:
  path: advisor
EOF
echo "==> Generating rust model with an advisor"
(cd "$PROJECT" && "$TACPLUS_BIN" build --config tacplus.yaml --src "$PROTO_SRC" --tag latest) >"$PROJECT/generate.log" 2>&1 || {
  cat "$PROJECT/generate.log" >&2
  die "rust generation failed"
}
MODEL="$PROJECT/model"
[[ -f "$MODEL/advisor/Cargo.toml" && -f "$MODEL/advisor/build.rs" && -f "$MODEL/advisor_port.rs" ]] ||
  die "generation wrote no model port (model/advisor/) or no adapter (model/advisor_port.rs)"
# registry.json marks a service advisor: true, and proto-zerorpc declares no
# service: the Rust registry is checked on proto-advisor, generation alone.
RUST_ADVISED="$WORK_ROOT/advised-rust"
mkdir -p "$RUST_ADVISED"
cat > "$RUST_ADVISED/tacplus.yaml" <<EOF
language: "rust"
kind: service
pkg: "com.gitlab.example.dummy.advised"
src: "$ROOT_DIR/cross-test/proto-advisor"
output-dir: "model"
depends:
  - llmproxy
advisor:
  path: advisor
EOF
(cd "$RUST_ADVISED" && "$TACPLUS_BIN" build --config tacplus.yaml --src "$ROOT_DIR/cross-test/proto-advisor" --tag latest) >"$RUST_ADVISED/generate.log" 2>&1 || {
  cat "$RUST_ADVISED/generate.log" >&2
  die "rust generation of proto-advisor failed"
}
grep -q '"advisor": *true' "$RUST_ADVISED/model/registry.json" || die "rust registry.json does not mark advised's service advisor: true"

ADVISOR="$PROJECT/advisor"
mkdir -p "$ADVISOR/src"
cp "$GATE_DIR/testdata/rust/advisor/src/lib.rs" "$ADVISOR/src/lib.rs"
GREEN_HEAD='[package]
name = "advisor"
version = "0.1.0"
edition = "2021"

[workspace]
'
GREEN_DEPS='[dependencies]
zerorpc-model-advisor = { path = "../model/advisor" }

[dev-dependencies]
tokio = { version = "1.51.6", features = ["macros", "rt"] }
'
write_advisor() { # extra manifest text, appended to the green one
  printf '%s\n%s\n%s\n' "$GREEN_HEAD" "$GREEN_DEPS" "${1:-}" > "$ADVISOR/Cargo.toml"
}
write_advisor

CORE="$PROJECT/core"
mkdir -p "$CORE/src"
cp "$GATE_DIR/testdata/rust/core/src/main.rs" "$CORE/src/main.rs"
cat > "$CORE/Cargo.toml" <<EOF
[package]
name = "zerorpc-core"
version = "0.1.0"
edition = "2021"

[workspace]

[dependencies]
zerorpc-model = { path = "../model" }
advisor = { path = "../advisor" }
tokio = { version = "1.51.6", features = ["full"] }
EOF

build_core() {
  (cd "$CORE" && CARGO_TARGET_DIR="$CROSS_TEST_CARGO_TARGET" cargo build --quiet) >"$WORK_ROOT/build.log" 2>&1
}
# A red manifest is checked by building the model crate, which every build of
# the core builds: the advisor itself is then outside the build, so what fails
# is the gate, not cargo resolving the advisor (a cycle back to the core, a
# patch it ignores).
build_model() {
  (cd "$MODEL" && CARGO_TARGET_DIR="$CROSS_TEST_CARGO_TARGET" cargo build --quiet -p zerorpc-model) >"$WORK_ROOT/build.log" 2>&1
}

echo "==> green: the advisor's own test, through the port"
(cd "$ADVISOR" && CARGO_TARGET_DIR="$CROSS_TEST_CARGO_TARGET" cargo test --quiet) >"$WORK_ROOT/test.log" 2>&1 || {
  cat "$WORK_ROOT/test.log" >&2
  die "the green advisor's test failed"
}
echo "==> green: the core builds beside an advisor that depends only on the port"
build_core || {
  cat "$WORK_ROOT/build.log" >&2
  die "the core with a green advisor did not build"
}
if [[ -s "$WORK_ROOT/build.log" ]]; then
  cat "$WORK_ROOT/build.log" >&2
  die "warning/error output found in the green build"
fi

# A crate of the repository's own, and a git repository, for the advisor to reach.
mkdir -p "$PROJECT/helper/src"
printf '[package]\nname = "helper"\nversion = "0.1.0"\nedition = "2021"\n\n[workspace]\n' > "$PROJECT/helper/Cargo.toml"
: > "$PROJECT/helper/src/lib.rs"
GITDEP="$WORK_ROOT/gitdep"
mkdir -p "$GITDEP/src"
printf '[package]\nname = "gitdep"\nversion = "0.1.0"\nedition = "2021"\n' > "$GITDEP/Cargo.toml"
: > "$GITDEP/src/lib.rs"
(cd "$GITDEP" && git init -q . && git add -A && git -c user.email=gate@tacplus -c user.name=gate commit -qm fixture)

fails=0
red() { # name, the line the gate must print, extra manifest text
  local name="$1" want="$2" extra="$3"
  write_advisor "$extra"
  if build_model; then
    echo "FAIL red/$name: the core built beside an advisor that $name" >&2
    fails=$((fails + 1))
  elif ! grep -q "tacplus LLM rule" "$WORK_ROOT/build.log" || ! grep -qF -- "$want" "$WORK_ROOT/build.log"; then
    echo "FAIL red/$name: the build failed, but not with the gate's '$want':" >&2
    cat "$WORK_ROOT/build.log" >&2
    fails=$((fails + 1))
  else
    echo "ok   red/$name"
  fi
}

echo "==> red: each way out of the port fails the build"
red "depends on the token chassis crate" "[build-dependencies] token: is the generated crate token" '
[build-dependencies]
token = { path = "../model/chassis/token" }'
red "depends on the servicemanager, renamed" "sm: is the generated crate servicemanager" '
[target.'"'"'cfg(unix)'"'"'.dependencies]
sm = { package = "servicemanager", path = "../model/chassis/servicemanager" }'
red "depends on the model crate (its state and S2S clients)" "is the generated crate zerorpc-model" '
[build-dependencies.zerorpc-model]
path = "../model"'
red "depends on the types crate (its raw transport stubs)" "is the generated crate zerorpc-model-types" '
[build-dependencies]
zerorpc-model-types = { path = "../model/types" }'
red "depends on the core" "core: is the path dependency" '
[build-dependencies]
core = { package = "zerorpc-core", path = "../core" }'
red "depends on another crate of the repository" "helper: is the path dependency" '
[build-dependencies]
helper = { path = "../helper" }'
red "depends on a git crate" "gitdep: comes from git" "
[build-dependencies]
gitdep = { git = \"file://$GITDEP\" }"
red "patches a dependency of its own" "patches tokio" '
[patch.crates-io]
tokio = { path = "../helper" }'

echo "==> red: a dependency inherited from the advisor's workspace is judged by what it inherits"
cat > "$PROJECT/Cargo.toml" <<'EOF'
[workspace]
members = ["advisor"]

[workspace.dependencies]
tok = { package = "token", path = "model/chassis/token" }
EOF
GREEN_HEAD_MEMBER='[package]
name = "advisor"
version = "0.1.0"
edition = "2021"
'
printf '%s\n%s\n%s\n' "$GREEN_HEAD_MEMBER" "$GREEN_DEPS" '
[build-dependencies]
tok = { workspace = true }' > "$ADVISOR/Cargo.toml"
if build_model; then
  echo "FAIL red/workspace-inherited: the core built beside an advisor inheriting the token" >&2
  fails=$((fails + 1))
elif ! grep -qF "tok: is the generated crate token" "$WORK_ROOT/build.log"; then
  echo "FAIL red/workspace-inherited: not the gate's failure:" >&2
  cat "$WORK_ROOT/build.log" >&2
  fails=$((fails + 1))
else
  echo "ok   red/workspace-inherited"
fi
rm "$PROJECT/Cargo.toml"

echo "==> red: an advisor with no manifest fails the build"
rm "$ADVISOR/Cargo.toml"
if build_model; then
  echo "FAIL red/no-manifest: the core built with no advisor manifest to check" >&2
  fails=$((fails + 1))
elif ! grep -q "tacplus LLM rule" "$WORK_ROOT/build.log"; then
  echo "FAIL red/no-manifest: not the gate's failure:" >&2
  cat "$WORK_ROOT/build.log" >&2
  fails=$((fails + 1))
else
  echo "ok   red/no-manifest"
fi

echo "==> green again: the gate passes once the advisor is back to the port alone"
write_advisor
build_core || {
  cat "$WORK_ROOT/build.log" >&2
  die "the restored green advisor did not build"
}

prune_cargo_workspace "$CORE" "$MODEL"

# ---- Go: the gate is the generated test model/advisor/llm_rule_test.go, which
# reads what `go list` resolves for the advisor's packages. On proto-advisor,
# not proto-zerorpc: Go has no zero-rpc host.
echo "==> Go: generating the model of proto-advisor with an advisor"
GO_PROTO_SRC="$ROOT_DIR/cross-test/proto-advisor"
GO_PROJECT="$WORK_ROOT/advised-go"
mkdir -p "$GO_PROJECT"
cat > "$GO_PROJECT/tacplus.yaml" <<YAML
language: "go"
kind: service
pkg: "com.gitlab.example.dummy.advised"
src: "$GO_PROTO_SRC"
output-dir: "model"
depends:
  - llmproxy
advisor:
  path: advisor
YAML
(cd "$GO_PROJECT" && "$TACPLUS_BIN" build --config tacplus.yaml --src "$GO_PROTO_SRC" --tag latest) >"$GO_PROJECT/generate.log" 2>&1 || {
  cat "$GO_PROJECT/generate.log" >&2
  die "go generation failed"
}
[[ -f "$GO_PROJECT/model/advisor/port.go" && -f "$GO_PROJECT/model/advisor/llm_rule_test.go" && -f "$GO_PROJECT/model/s2sm/advisor_port.go" ]] ||
  die "go generation wrote no model port (model/advisor/) or no adapter (model/s2sm/advisor_port.go)"
grep -q '"advisor": *true' "$GO_PROJECT/model/registry.json" || die "go registry.json does not mark advised's service advisor: true"

# shellcheck source=../lib/go-compile.sh
source "$ROOT_DIR/cross-test/lib/go-compile.sh"
write_go_compile_mod "$GO_PROJECT/go.mod" "gitlab.com/example/dummy/advised"
cp -r "$GATE_DIR/testdata/go/advisor" "$GATE_DIR/testdata/go/core" "$GO_PROJECT/"
(cd "$GO_PROJECT" && go mod tidy) >"$WORK_ROOT/go-tidy.log" 2>&1 || {
  cat "$WORK_ROOT/go-tidy.log" >&2
  die "go mod tidy failed"
}

echo "==> Go green: the module builds, the advisor's test passes through the port, the gate passes"
(cd "$GO_PROJECT" && go build ./... && go vet ./advisor ./core ./model/advisor ./model/s2sm && go test -count=1 ./advisor ./model/advisor) >"$WORK_ROOT/go-green.log" 2>&1 || {
  cat "$WORK_ROOT/go-green.log" >&2
  die "the green Go advisor did not pass"
}

go_red() { # name, the line the gate must print, file under advisor/, the package it imports
  local name="$1" want="$2" file="$GO_PROJECT/advisor/$3" pkg="$4" import="$5"
  mkdir -p "$(dirname "$file")"
  printf 'package %s\n\nimport _ "%s"\n' "$pkg" "$import" > "$file"
  if (cd "$GO_PROJECT" && go test -count=1 ./model/advisor) >"$WORK_ROOT/go-red.log" 2>&1; then
    echo "FAIL go red/$name: the gate passed an advisor that $name" >&2
    fails=$((fails + 1))
  elif ! grep -q "tacplus LLM rule" "$WORK_ROOT/go-red.log" || ! grep -qF -- "$want" "$WORK_ROOT/go-red.log"; then
    echo "FAIL go red/$name: the gate failed, but not with '$want':" >&2
    cat "$WORK_ROOT/go-red.log" >&2
    fails=$((fails + 1))
  else
    echo "ok   go red/$name"
  fi
  rm "$file"
}

M="gitlab.com/example/dummy/advised"
echo "==> Go red: each way out of the port fails the gate"
go_red "imports the ServiceManager (its clients, its state)" "imports $M/model/s2sm" sm.go advisor "$M/model/s2sm"
go_red "imports the token chassis, from a test" "imports $M/model/chassis/token" token_test.go advisor "$M/model/chassis/token"
go_red "imports the types package (its raw transport stubs)" "imports $M/model/types/" types.go advisor "$M/model/types/services/upstreamllm"
go_red "imports the key chassis, from a subpackage" "imports $M/model/chassis/key" tools/tools.go tools "$M/model/chassis/key"
go_red "imports the core" "imports $M/core" core.go advisor "$M/core"

echo "==> Go green again"
(cd "$GO_PROJECT" && go test -count=1 ./model/advisor) >"$WORK_ROOT/go-green.log" 2>&1 || {
  cat "$WORK_ROOT/go-green.log" >&2
  die "the restored green Go advisor did not pass the gate"
}

[[ "$fails" -eq 0 ]] || die "$fails red case(s) did not fail with the gate"
echo "==> llm-rule: green and red pass (Rust, Go)"
