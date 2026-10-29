#!/usr/bin/env bash
# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# Tests of images/tacplus.sh, the build image's tacplus (#193), against a fake
# release forge served from file:// URLs: a pinned version is run exactly; a
# checksum mismatch, a missing pin and a line mismatch are refused; a cached
# version runs offline; outside a repository the newest patch of the line is
# run and re-checked at most hourly; the darwin binary is chosen on macOS.
#
# The determinism leg builds two real tacplus binaries from this checkout
# (2.6.0 and 2.6.1), builds the same commit of a pinned repository twice, in
# two clones with two caches, the second after 2.6.1 is released, and requires
# the same tacplus version and byte-identical generated output.
#
# The script runs under dash (POSIX sh) when it is installed, else sh.
#
# Usage: images/tacplus_test.sh [--quick]   (--quick skips the determinism leg)
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SCRIPT="$ROOT/images/tacplus.sh"
SH="${TACPLUS_TEST_SH:-$(command -v dash || command -v sh)}"
QUICK=false
[[ "${1:-}" == --quick ]] && QUICK=true

W="$(mktemp -d "${TMPDIR:-/tmp}/tacplus-script-test.XXXXXX")"
trap '[[ -n "${KEEP_W:-}" ]] || rm -rf "$W"' EXIT
FORGE="$W/forge"
mkdir -p "$FORGE/download"
PUBLISHED=()

pass=0
failures=0
ok() { echo "ok   $*"; pass=$((pass + 1)); }
bad() { echo "FAIL $*"; failures=$((failures + 1)); }

case "$(uname -s)" in Linux) OS=linux ;; Darwin) OS=darwin ;; esac
case "$(uname -m)" in x86_64 | amd64) ARCH=amd64 ;; aarch64 | arm64) ARCH=arm64 ;; esac

write_releases() {
  # GitHub's order: newest first by date, which is publish order here.
  {
    echo "["
    local i sep=""
    for ((i = ${#PUBLISHED[@]} - 1; i >= 0; i--)); do
      printf '%s  {\n    "tag_name": "v%s",\n    "draft": false,\n    "prerelease": false\n  }' "$sep" "${PUBLISHED[$i]}"
      sep=$',\n'
    done
    printf '\n]\n'
  } >"$FORGE/releases.json"
}

# publish VERSION [FILE] [--bad-checksum]: a release with one binary per
# platform (FILE, else a stub that prints its version and arguments).
publish() {
  local v="$1" file="${2:-}" badsum="${3:-}" d="$FORGE/download/v$1" os arch
  mkdir -p "$d"
  : >"$d/checksums.txt"
  for os in linux darwin; do
    for arch in amd64 arm64; do
      local asset="tacplus-${v//./-}-$os-$arch"
      if [[ -n "$file" ]]; then
        cp "$file" "$d/$asset"
      else
        printf '#!/bin/sh\necho "tacplus %s %s-%s $*"\n' "$v" "$os" "$arch" >"$d/$asset"
      fi
      local sum
      sum="$(sha256sum "$d/$asset" | awk '{print $1}')"
      [[ "$badsum" == --bad-checksum ]] && sum="$(printf '%s' "$sum" | tr 0-9a-f 1-9a-f0)"
      echo "$sum  $asset" >>"$d/checksums.txt"
    done
  done
  PUBLISHED+=("$v")
  write_releases
}

# repo DIR [VERSION [IMAGE]]: a repository with tacplus.yaml, pinned to VERSION.
repo() {
  mkdir -p "$1"
  printf 'language: go\noutput-dir: model\n' >"$1/tacplus.yaml"
  if [[ -n "${2:-}" ]]; then
    printf '# test\ntacplus: %s\nbuild_image: %s\n' "$2" \
      "${3:-ghcr.io/tacplus-dev/tacplus/build:$2@sha256:$(printf '0%.0s' {1..64})}" >"$1/tacplus.lock"
  fi
}

# run DIR [VAR=VALUE ...] -- ARGS...: the script in DIR; sets OUT and RC.
run() {
  local dir="$1"
  shift
  local envs=()
  while [[ "$1" != -- ]]; do
    envs+=("$1")
    shift
  done
  shift
  set +e
  OUT="$(cd "$dir" && env -u TACPLUS_VERSION -u TACPLUS_LINE \
    TACPLUS_CACHE="$W/cache" TACPLUS_LINE=2.6 \
    TACPLUS_RELEASES_URL="file://$FORGE/releases.json" \
    TACPLUS_DOWNLOAD_URL="file://$FORGE/download" \
    "${envs[@]}" "$SH" "$SCRIPT" "$@" 2>&1)"
  RC=$?
  set -e
}

expect() { # NAME RC SUBSTRING
  if [[ "$RC" == "$2" && "$OUT" == *"$3"* ]]; then
    ok "$1"
  else
    bad "$1: rc=$RC (want $2), output: $OUT (want it to contain: $3)"
  fi
}

publish 2.5.0
publish 2.6.0
publish 2.6.1
publish 2.7.0
publish 2.5.1 "" --bad-checksum
OUTSIDE="$W/outside"
mkdir -p "$OUTSIDE"

# --- in a repository: the pin, exactly
repo "$W/pinned" 2.6.0
mkdir -p "$W/pinned/proto/sub"
run "$W/pinned" -- build --x
expect "a pinned version is run exactly (not the newer 2.6.1)" 0 "tacplus 2.6.0 $OS-$ARCH build --x"
run "$W/pinned/proto/sub" -- version
expect "the pin is found from a subdirectory" 0 "tacplus 2.6.0 $OS-$ARCH version"
[[ -x "$W/cache/2.6.0/tacplus" ]] && ok "the verified binary is cached under <cache>/2.6.0" || bad "2.6.0 is not cached"

repo "$W/quoted"
printf 'tacplus: "2.6.1"\nbuild_image: "ghcr.io/tacplus-dev/tacplus/build:2.6.1@sha256:%s"\n' "$(printf 'a%.0s' {1..64})" >"$W/quoted/tacplus.lock"
run "$W/quoted" -- version
expect "a quoted pin is read" 0 "tacplus 2.6.1 $OS-$ARCH version"

# --- refusals
repo "$W/badsum" 2.5.1
run "$W/badsum" TACPLUS_LINE=2.5 -- build
expect "a checksum mismatch is refused" 2 "checksum mismatch for tacplus-2-5-1-$OS-$ARCH"
if [[ ! -e "$W/cache/2.5.1/tacplus" ]] && [[ -z "$(ls -A "$W/cache/2.5.1")" ]]; then
  ok "a refused download leaves nothing in the cache"
else
  bad "a refused download left $(ls -A "$W/cache/2.5.1")"
fi

repo "$W/nopin"
mkdir -p "$W/nopin/sub"
run "$W/nopin/sub" -- build
expect "a missing pin is refused" 2 "has no tacplus.lock"
printf 'tacplus: "2.6"\n' >"$W/fleet.yaml.tmp"
mkdir -p "$W/fleet" && mv "$W/fleet.yaml.tmp" "$W/fleet/fleet.yaml"
run "$W/fleet" -- fleet build
expect "a fleet without a pin is refused" 2 "has no tacplus.lock"

repo "$W/otherline" 2.5.0
run "$W/otherline" -- build
expect "a pin of another line is refused" 2 "of line 2.5, but this is the build image of line 2.6"

repo "$W/conflict" 2.6.0
run "$W/conflict" TACPLUS_VERSION=2.6.1 -- build
expect "TACPLUS_VERSION against the pin is refused" 2 "the pin decides"

repo "$W/nodigest" 2.6.0 ghcr.io/tacplus-dev/tacplus/build:2.6
run "$W/nodigest" -- build
expect "a build image without a digest is refused" 2 "must name the image by digest"

repo "$W/linepin" 2.6
run "$W/linepin" -- build
expect "a line (not a version) in the pin is refused" 2 "must be an exact MAJOR.MINOR.PATCH"

run "$W/nopin" -- pin --version 2.6
expect "tacplus pin runs in a repository without a pin (newest of the line)" 0 "tacplus 2.6.1 $OS-$ARCH pin --version 2.6"

# --- offline, from the cache
run "$W/pinned" TACPLUS_RELEASES_URL=file:///nonexistent TACPLUS_DOWNLOAD_URL=file:///nonexistent -- build
expect "a cached pin runs offline" 0 "tacplus 2.6.0 $OS-$ARCH build"
repo "$W/uncached" 2.7.0
run "$W/uncached" TACPLUS_LINE=2.7 TACPLUS_DOWNLOAD_URL=file:///nonexistent -- build
expect "an uncached pin offline fails, and says why" 1 "cannot download checksums.txt of v2.7.0"

# --- outside a repository
run "$OUTSIDE" -- version
expect "outside a repository: the newest patch of the image's line" 0 "tacplus 2.6.1 $OS-$ARCH version"
publish 2.6.3
run "$OUTSIDE" -- version
expect "the newest patch is re-checked at most hourly" 0 "tacplus 2.6.1 $OS-$ARCH"
touch -t 200001010000 "$W/cache/newest-2.6"
run "$OUTSIDE" -- version
expect "after an hour, a newer patch is taken" 0 "tacplus 2.6.3 $OS-$ARCH"
touch -t 200001010000 "$W/cache/newest-2.6"
run "$OUTSIDE" TACPLUS_RELEASES_URL=file:///nonexistent -- version
expect "offline outside a repository: the last answer" 0 "tacplus 2.6.3 $OS-$ARCH"
run "$OUTSIDE" TACPLUS_VERSION=2.5.0 -- version
expect "outside a repository, TACPLUS_VERSION runs exactly" 0 "tacplus 2.5.0 $OS-$ARCH"
run "$OUTSIDE" TACPLUS_LINE= -- version
expect "no image line (a developer's machine): the newest release" 0 "tacplus 2.7.0 $OS-$ARCH"
run "$OUTSIDE" TACPLUS_LINE=2.9 -- version
expect "a line with no release is refused" 2 "no release of line 2.9"

# --- macOS: uname says Darwin, and there is no sha256sum (shasum instead)
MAC="$W/macbin"
mkdir -p "$MAC"
for t in "$(basename "$SH")" awk cat chmod curl dirname env expr find head ls mkdir mv rm sed sort tail tr shasum perl; do
  p="$(command -v "$t" || true)"
  [[ -n "$p" ]] && ln -sf "$p" "$MAC/$t"
done
printf '#!/bin/sh\ncase "$1" in -s) echo Darwin ;; -m) echo arm64 ;; esac\n' >"$MAC/uname"
chmod +x "$MAC/uname"
if [[ -e "$MAC/shasum" ]]; then
  run "$W/pinned" PATH="$MAC" TACPLUS_CACHE="$W/maccache" -- build
  expect "on macOS the darwin binary is run, verified with shasum" 0 "tacplus 2.6.0 darwin-arm64 build"
else
  bad "shasum is not installed: the macOS case cannot run"
fi

# --- determinism: the same commit, twice, before and after a newer patch
if [[ "$QUICK" == false ]]; then
  echo "==> determinism: building tacplus 2.6.0 and 2.6.1 from this checkout"
  rm -rf "$FORGE" "$W/cache" && mkdir -p "$FORGE/download" && PUBLISHED=()
  PKG=github.com/tacplus-dev/tacplus/pkg/buildinfo
  for v in 2.6.0 2.6.1; do
    (cd "$ROOT" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X $PKG.Version=$v -X $PKG.GitSha=test" -o "$W/tacplus-$v" .)
  done
  publish 2.6.0 "$W/tacplus-2.6.0"

  SRC="$W/origin"
  mkdir -p "$SRC"
  cp -R "$ROOT/cross-test/proto-requiresdevice" "$SRC/proto"
  printf 'language: go\npkg: com.gitlab.example.dummy.vault\nsrc: proto\noutput-dir: model\n' >"$SRC/tacplus.yaml"
  printf 'tacplus: 2.6.0\nbuild_image: ghcr.io/tacplus-dev/tacplus/build:2.6.0@sha256:%s\n' "$(printf '0%.0s' {1..64})" >"$SRC/tacplus.lock"
  git -C "$SRC" init -q
  git -C "$SRC" add -A
  git -C "$SRC" -c user.name=test -c user.email=test@example.com commit -q -m pinned

  build_clone() { # NAME: clone the commit, build it with its own cache
    git clone -q "$SRC" "$W/$1"
    run "$W/$1" TACPLUS_CACHE="$W/cache-$1" -- build --config tacplus.yaml --src proto --tag latest
    [[ "$RC" == 0 ]] || bad "determinism: build $1 failed: $OUT"
  }
  build_clone first
  publish 2.6.1 "$W/tacplus-2.6.1"
  build_clone second

  run "$OUTSIDE" TACPLUS_CACHE="$W/cache-control" -- version
  expect "determinism control: 2.6.1 is the newest release when the second build runs" 0 "tacplus version 2.6.1"
  if [[ -d "$W/first/model" ]] && diff -r "$W/first/model" "$W/second/model" >/dev/null; then
    ok "determinism: the same commit, before and after 2.6.1, generates byte-identical output"
  else
    bad "determinism: the two builds of one commit differ: $(diff -rq "$W/first/model" "$W/second/model" 2>&1 | head -5)"
  fi
  # The generated Go stamps the version that generated it (s2sm.go,
  # "tacplus_version"); embedded docs may mention any version, so only the
  # stamp is read.
  stamps="$(grep -rhoE '"tacplus_version", "[^"]*"' --include='*.go' "$W/second/model" | sort -u)"
  if [[ "$stamps" == '"tacplus_version", "2.6.0"' ]]; then
    ok "determinism: both builds ran the pinned 2.6.0 (the output's version stamp)"
  else
    bad "determinism: the generated output is stamped ${stamps:-with no version}, want exactly 2.6.0"
  fi
  [[ ! -e "$W/cache-second/2.6.1" ]] && ok "determinism: the build never fetched the newer patch" || bad "determinism: the build fetched 2.6.1"
  # The leg can see drift: the newer patch, run directly, generates other output.
  git clone -q "$SRC" "$W/newer"
  (cd "$W/newer" && "$W/tacplus-2.6.1" build --config tacplus.yaml --src proto --tag latest >/dev/null 2>&1)
  if [[ -d "$W/newer/model" ]] && ! diff -r "$W/first/model" "$W/newer/model" >/dev/null; then
    ok "determinism control: 2.6.1 itself generates different output, so the leg would see a moved version"
  else
    bad "determinism control: 2.6.1 generates the same output as 2.6.0; the leg proves nothing"
  fi
fi

echo "tacplus.sh: $pass passed, $failures failed (shell: $SH)"
[[ "$failures" == 0 ]]
