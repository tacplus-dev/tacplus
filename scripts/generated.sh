#!/usr/bin/env bash
# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# The code tacplus generates, for the build pipeline's security checks
# (tacplus#376; tacplus-dev/looper#108, README "Generated code is scanned
# too"). What tacplus emits is what every service ships, and a service's own
# scan skips its generated tree (tacplus.yaml's output-dir), so tacplus scans
# it here, once, with its own security-exceptions.yaml. After `just gates`
# passes, the gates job runs `just generated <dir>` and keeps <dir> as its
# artifact generated/, which the security job scans as a second tree.
#
# Usage:  scripts/generated.sh DIR
#
# DIR must be empty or absent, and outside the checkout unless git ignores it
# (target/...); its parent must exist. No tracked file changes (the tree guard
# fails the run if one does). Not a gate: `just gates` never runs it
# (scripts/generated_test.go).
#
# Each proto set below is generated into DIR/<set>/<language>/ as a service
# lays itself out (tacplus.yaml, the model under tacplus/), and made loadable
# by the scanners that read it, since the security job compiles nothing:
#   go          go.mod (the chassis's pins, cross-test/lib/go-compile.sh) and
#               go.sum (go mod tidy): gosec, govulncheck
#   rust        Cargo.lock (cargo generate-lockfile): cargo-deny, clippy
#   java        pom.xml (cross-test/lib/java-compile.sh): OSV-Scanner; and in
#               the JAVA_BYTECODE set, target/classes (mvn compile): SpotBugs
#               with FindSecBugs
#   typescript  package-lock.json (npm, the lock only): OSV-Scanner
#   python      the sources and pyproject.toml: Bandit
# Semgrep and gitleaks read every file; gitleaks with DIR/.gitleaks.toml
# (scripts/generated.gitleaks.toml: the root .gitleaks.toml's decisions, for
# what those templates generate). A lock is the newest compatible
# release of each dependency, not the floor a generated manifest declares
# (PROPOSALS.md, "A floor check over every version tacplus emits").
#
#   TACPLUS_BIN             an existing tacplus binary (default: built from the checkout)
#   TACPLUS_GENERATED_WORK  where the logs go (default: target/generated-work)
#   TACPLUS_PYTHON          the Python interpreter tacplus runs grpc_tools.protoc with
#   TACPLUS_GENERATED_JOBS  how many proto sets generate at once (default 3)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
# shellcheck source=../cross-test/lib/tree-guard.sh
source "$ROOT/cross-test/lib/tree-guard.sh"
# shellcheck source=../cross-test/lib/go-compile.sh
source "$ROOT/cross-test/lib/go-compile.sh"
# shellcheck source=../cross-test/lib/java-compile.sh
source "$ROOT/cross-test/lib/java-compile.sh"

usage() {
  sed -n 's/^# Usage:  /Usage: /p' "${BASH_SOURCE[0]}" >&2
}

die() {
  echo "generated: $*" >&2
  exit 1
}

[[ $# -eq 1 && -n "$1" ]] || { usage; exit 2; }

# The proto sets: the fixtures the current-generation gate builds, each there
# because it reaches emitters no other one does (the state singleton, the
# record plane and its host, tenant routing, the redo contract, owners'
# states: all, ...). #196's kitchensink replaces this list.
#   set|proto source (under cross-test/)|pkg|tacplus.yaml lines (;-separated)|languages
# Each set's languages are the ones the gate compiles it in, so every tree here
# is one a service builds (the scanners load it). Python and TypeScript are
# client targets: a service build refuses them, so they generate the base set
# alone.
SETS=(
  "base|proto|com.gitlab.example.dummy.proto||go rust java python typescript"
  "c2sonly|proto-c2sonly|com.gitlab.example.dummy.c2sonly||go"
  "healthonly|proto-healthonly|com.gitlab.example.dummy.healthonly||rust"
  "zerorpc|proto-zerorpc|com.gitlab.example.dummy.zerorpc|depends: [\"llmproxy\"]|rust"
  "requiresdevice|proto-requiresdevice|com.gitlab.example.dummy.vault||go rust java"
  "recordegress|proto-recordegress|com.gitlab.example.dummy.egressvault|depends: [\"eventbus\", \"egressstratum\"]|go rust java"
  "recordegress-host|proto-recordegress|com.gitlab.example.dummy.egressstratum|records: \"all\"|go rust java"
  "eventbus-backpressure|proto-eventbus-backpressure|com.gitlab.example.dummy.agentsensor|depends: [\"eventbus\"]|java"
  "accounting-sink|proto-accounting-sink|com.gitlab.example.dummy.agentsensor|depends: [\"eventbus\", \"accountingrecord\"]|go rust java"
  "ownerdefault|proto-ownerdefault|com.gitlab.example.dummy.ownerdesk||go rust java"
  "quorumwrite|proto-quorumwrite|com.gitlab.example.dummy.quorumvault||go rust java"
  "epochload|proto-epochload|com.gitlab.example.dummy.epochload||go rust java"
  "tenantroute-harbor|proto-tenantroute|com.gitlab.example.dummy.harbor|depends: [\"crew\"]|go rust java"
  "tenantroute-crew|proto-tenantroute|com.gitlab.example.dummy.crew||go rust java"
  "redo-stratum|proto-redo/caller|com.gitlab.example.dummy.stratum||go rust java"
  "redo-host|proto-redo/host|com.gitlab.example.dummy.redo||go rust java"
  "tenantrecord-tstratum|proto-tenantrecord|com.gitlab.example.dummy.tstratum|records: \"all\"|go rust java"
  "tenantrecord-tmessaging|proto-tenantrecord|com.gitlab.example.dummy.tmessaging|depends: [\"eventbus\", \"tstratum\"]|go rust java"
  "tenantslice-ledger|proto-tenantslice|com.gitlab.example.dummy.ledger|depends: [\"tenancy\", \"registrar\"]|go rust java"
  "tenantslice-registrar|proto-tenantslice|com.gitlab.example.dummy.registrar|states: \"all\"|go rust java"
  "tenantslice-tenancy|proto-tenantslice|com.gitlab.example.dummy.tenancy|states: \"all\"|go rust java"
)

# The one set whose Java is compiled for SpotBugs, a full service with the
# state singleton. SpotBugs reads every target/classes of the tree in one run,
# in the looper image's default 768 MB heap: one set's bytecode fits, four do
# not (PROPOSALS.md, "SpotBugs per directory").
JAVA_BYTECODE=requiresdevice

# DIR, absolute (symlinks resolved), checked before anything is written.
target="$1"
parent="$(dirname "$target")"
[[ -d "$parent" ]] || die "$parent (the parent of $target) does not exist"
OUT="$(cd "$parent" && pwd -P)/$(basename "$target")"
if [[ -e "$OUT" ]]; then
  [[ -d "$OUT" && -z "$(ls -A "$OUT")" ]] || die "$OUT exists and is not an empty directory"
fi
case "$OUT/" in
  "$ROOT"/*)
    git -C "$ROOT" check-ignore -q "${OUT#"$ROOT"/}/" ||
      die "$OUT is in the checkout and git does not ignore it: the recipe may not change the tree (use a directory outside it, or under target/)"
    ;;
esac

tree_guard_begin "$ROOT"

WORK="${TACPLUS_GENERATED_WORK:-$ROOT/target/generated-work}"
rm -rf "$WORK"
mkdir -p "$WORK" "$OUT"
cp "$ROOT/scripts/generated.gitleaks.toml" "$OUT/.gitleaks.toml"

TACPLUS_BIN="${TACPLUS_BIN:-}"
if [[ -z "$TACPLUS_BIN" ]]; then
  TACPLUS_BIN="$ROOT/target/tacplus-generated"
  echo "==> Building tacplus from $ROOT"
  (cd "$ROOT" && go generate ./pkg/internal/proto/spec/... && go build -o "$TACPLUS_BIN" .) || die "building tacplus failed"
fi

# step LOG DIR CMD...: runs CMD in DIR, its output appended to LOG; on failure
# shows the log's tail and dies.
step() {
  local log="$1" dir="$2"
  shift 2
  if ! (cd "$dir" && "$@") >>"$log" 2>&1; then
    tail -80 "$log" >&2 || true
    die "$* failed in $dir (log: $log)"
  fi
}

# The Go module a pkg generates: com.gitlab.example.dummy.vault is
# gitlab.com/example/dummy/vault.
go_module() {
  local tld host rest
  IFS=. read -r tld host rest <<<"$1"
  printf '%s.%s/%s\n' "$host" "$tld" "${rest//.//}"
}

generate() { # set src pkg extra language
  local set="$1" src="$ROOT/cross-test/$2" pkg="$3" extra="$4" language="$5"
  local dir="$OUT/$set/$language" log="$WORK/$set-$language.log" output registries
  [[ -d "$src" ]] || die "$set: no proto set at $src"
  mkdir -p "$dir"
  {
    printf 'language: "%s"\npkg: "%s"\nsrc: "%s"\noutput-dir: "tacplus"\n' "$language" "$pkg" "$src"
    [[ -z "$extra" ]] || tr ';' '\n' <<<"$extra"
  } >"$dir/tacplus.yaml"

  echo "==> $set/$language"
  step "$log" "$dir" env TACPLUS_PYTHON="${TACPLUS_PYTHON:-}" "$TACPLUS_BIN" build --config tacplus.yaml --src "$src"
  registries="$(find "$dir/tacplus" -maxdepth 3 -type f -name registry.json)"
  [[ -n "$registries" && "$(wc -l <<<"$registries")" -eq 1 ]] || die "$set/$language: not one generated model under $dir/tacplus"
  output="$(dirname "$registries")"

  case "$language" in
    go)
      write_go_compile_mod "$dir/go.mod" "$(go_module "$pkg")"
      step "$log" "$dir" go mod tidy
      ;;
    rust)
      step "$log" "$output" cargo generate-lockfile
      ;;
    java)
      # The pom names the sources relative to itself, so the tree compiles
      # and reads the same wherever the artifact is unpacked.
      write_java_compile_pom "$dir/pom.xml" "${output#"$dir"/}"
      if [[ -n "$(find "$output/src/main/java" -type f -name GcsArchiveSink.java -print -quit)" ]]; then
        add_java_gcs_archive_dependency "$dir/pom.xml"
      fi
      [[ "$set" == "$JAVA_BYTECODE" ]] || return 0
      command -v mvn >/dev/null || die "mvn is required to compile the generated Java"
      local java_env
      java_env="$(java_home_env)"
      step "$log" "$dir" env ${java_env:+"$java_env"} mvn -q -DskipTests compile
      # The bytecode SpotBugs reads; the rest of Maven's build is no source.
      find "$dir/target" -mindepth 1 -maxdepth 1 ! -name classes -exec rm -rf {} +
      ;;
    typescript)
      step "$log" "$output" npm install --package-lock-only --ignore-scripts --no-audit --no-fund
      ;;
    python) ;;
  esac
}

# The sets generate side by side, JOBS at a time (each set's languages in
# turn): one after another they take about an hour, nearly all of it tacplus.
JOBS="${TACPLUS_GENERATED_JOBS:-3}"
[[ "$JOBS" =~ ^[1-9][0-9]*$ ]] || die "TACPLUS_GENERATED_JOBS=$JOBS is not a positive number"
pids=()
failed=0
for entry in "${SETS[@]}"; do
  if ((${#pids[@]} >= JOBS)); then
    wait "${pids[0]}" || failed=1
    pids=("${pids[@]:1}")
  fi
  IFS='|' read -r set src pkg extra languages <<<"$entry"
  (
    for language in $languages; do
      generate "$set" "$src" "$pkg" "$extra" "$language"
    done
  ) &
  pids+=("$!")
done
for pid in "${pids[@]}"; do
  wait "$pid" || failed=1
done
((failed == 0)) || die "a proto set did not generate (above; logs: $WORK)"

tree_guard_end
echo "==> The generated code is in $OUT (logs: $WORK)"
