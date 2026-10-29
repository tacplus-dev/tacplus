#!/bin/sh
# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# tacplus, as the build image provides it (/usr/local/bin/tacplus): not the
# compiler, but a launcher that runs a released tacplus binary
# (tacplus-dev/roadmap: decisions/tacplus-images-2026-10-27.md and
# deterministic-builds-2026-10-27.md). POSIX sh, so the same file runs on a
# developer's Mac (darwin binaries are published) as on Linux.
#
# In a repository (a directory, or an ancestor of it, holding tacplus.lock,
# tacplus.yaml or fleet.yaml), it runs exactly the release tacplus.lock pins,
# and refuses to run:
#   - when the repository has no tacplus.lock (pin one with `tacplus pin`);
#   - when the pin's MAJOR.MINOR is not this image's line (TACPLUS_LINE);
#   - when TACPLUS_VERSION asks for another version than the pin.
# A build never resolves "newest": the same commit runs the same tacplus.
# `tacplus pin`, which moves the pin, is the one command run as outside one.
#
# Outside a repository (someone experimenting), it runs TACPLUS_VERSION when
# set, else the newest patch of the image's line (of all releases when
# TACPLUS_LINE is unset), asking GitHub at most once per
# TACPLUS_CHECK_MINUTES (60) and otherwise using the last answer.
#
# Every binary is downloaded once, checked against its release's
# checksums.txt (a mismatch is refused, and nothing is cached), and cached
# under $TACPLUS_CACHE/<version>/tacplus (~/.cache/tacplus), so a cached
# version runs offline.
#
# Environment:
#   TACPLUS_LINE           this image's MAJOR.MINOR (set by the build image)
#   TACPLUS_VERSION        exact X.Y.Z to run outside a repository
#   TACPLUS_CACHE          cache directory (default ~/.cache/tacplus)
#   TACPLUS_CHECK_MINUTES  how long a "newest" answer is reused (default 60)
#   TACPLUS_RELEASES_URL   the releases list (default the GitHub API)
#   TACPLUS_DOWNLOAD_URL   the release download base (default GitHub releases)
set -eu

line="${TACPLUS_LINE:-}"
cache="${TACPLUS_CACHE:-${XDG_CACHE_HOME:-${HOME:-/tmp}/.cache}/tacplus}"
check_minutes="${TACPLUS_CHECK_MINUTES:-60}"
releases_url="${TACPLUS_RELEASES_URL:-https://api.github.com/repos/tacplus-dev/tacplus/releases?per_page=100}"
download_url="${TACPLUS_DOWNLOAD_URL:-https://github.com/tacplus-dev/tacplus/releases/download}"

# A refusal (the repository or the request is wrong) exits 2; a failure (the
# network, the platform) exits 1.
refuse() {
  echo "tacplus: $*" >&2
  exit 2
}
fail() {
  echo "tacplus: $*" >&2
  exit 1
}
warn() {
  echo "tacplus: $*" >&2
}

is_version() {
  expr "$1" : '[0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*$' >/dev/null
}

if [ -n "$line" ] && ! expr "$line" : '[0-9][0-9]*\.[0-9][0-9]*$' >/dev/null; then
  fail "TACPLUS_LINE='$line' is not MAJOR.MINOR"
fi
command -v curl >/dev/null 2>&1 || fail "curl is required to fetch tacplus releases"

fetch() {
  curl -fsSL --retry 3 --connect-timeout 10 "$@"
}

# The value of a `key: value` line of tacplus.lock (quotes allowed).
lock_get() {
  sed -n "s/^$1:[[:space:]]*\"\{0,1\}\([^\"[:space:]]*\)\"\{0,1\}[[:space:]]*\$/\1/p" "$2" | head -n 1
}

# Highest X.Y.Z among the version lines on stdin, of line $1 ("" = any).
highest() {
  awk -F. -v want="$1" 'want == "" || $1 "." $2 == want' | sort -t. -k1,1n -k2,2n -k3,3n | tail -n 1
}

# The newest release of line $1 ("" = any), from GitHub at most once per
# check_minutes; offline, the last answer, else the highest cached version.
newest() {
  stamp="$cache/newest-${1:-all}"
  if [ -f "$stamp" ] && [ -n "$(find "$stamp" -mmin -"$check_minutes" 2>/dev/null)" ]; then
    cat "$stamp"
    return
  fi
  if json="$(fetch "$releases_url" 2>/dev/null)"; then
    # Drafts are not listed to an anonymous request; pre-releases (vX.Y.Z-rc.N)
    # do not match.
    v="$(printf '%s\n' "$json" | tr ',' '\n' |
      sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"v\([0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\)".*/\1/p' |
      highest "$1")"
    [ -n "$v" ] || refuse "no release of line ${1:-any} at $releases_url"
    mkdir -p "$cache"
    printf '%s\n' "$v" >"$stamp.$$" && mv -f "$stamp.$$" "$stamp"
    echo "$v"
    return
  fi
  if [ -f "$stamp" ]; then
    warn "cannot list releases ($releases_url); using the last answer, $(cat "$stamp")"
    cat "$stamp"
    return
  fi
  v="$(ls "$cache" 2>/dev/null | while read -r d; do
    if is_version "$d" && [ -x "$cache/$d/tacplus" ]; then echo "$d"; fi
  done | highest "$1")"
  [ -n "$v" ] || fail "cannot list releases ($releases_url) and no tacplus${1:+ $1.x} is cached"
  warn "cannot list releases ($releases_url); using the newest cached, $v"
  echo "$v"
}

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  elif command -v openssl >/dev/null 2>&1; then
    openssl dgst -sha256 -r "$1" | awk '{print $1}'
  else
    fail "sha256sum, shasum or openssl is required to verify tacplus releases"
  fi
}

# Download release $1 into the cache, verified against its checksums.txt.
install_release() {
  case "$(uname -s)" in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    *) fail "no tacplus release binary for $(uname -s); see https://tacplus.dev/install/" ;;
  esac
  case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) fail "no tacplus release binary for $(uname -m)" ;;
  esac
  asset="tacplus-$(echo "$1" | tr . -)-$os-$arch"
  dir="$cache/$1"
  mkdir -p "$dir"
  tmp="$dir/.download.$$"
  trap 'rm -f "$tmp" "$tmp.sums"' EXIT
  trap 'exit 1' HUP INT TERM
  fetch -o "$tmp.sums" "$download_url/v$1/checksums.txt" ||
    fail "cannot download checksums.txt of v$1 ($download_url/v$1/checksums.txt)"
  want="$(awk -v f="$asset" '{ n = $2; sub(/^\*/, "", n) } n == f { print $1; exit }' "$tmp.sums")"
  [ -n "$want" ] || refuse "checksums.txt of v$1 has no entry for $asset"
  fetch -o "$tmp" "$download_url/v$1/$asset" || fail "cannot download $asset of v$1"
  got="$(sha256 "$tmp")"
  [ "$got" = "$want" ] ||
    refuse "checksum mismatch for $asset of v$1: checksums.txt says $want, the download is $got; refusing to run it"
  chmod 0755 "$tmp"
  mv -f "$tmp" "$dir/tacplus"
  rm -f "$tmp.sums"
  trap - EXIT HUP INT TERM
}

project=""
d="$(pwd)"
while :; do
  if [ -f "$d/tacplus.lock" ] || [ -f "$d/tacplus.yaml" ] || [ -f "$d/fleet.yaml" ]; then
    project="$d"
    break
  fi
  [ "$d" = / ] && break
  d="$(dirname "$d")"
done

if [ -n "$project" ] && [ "${1:-}" != pin ]; then
  lock="$project/tacplus.lock"
  [ -f "$lock" ] ||
    refuse "$project has no tacplus.lock: a build runs only the tacplus release its repository pins. Run \`tacplus pin\` there and commit tacplus.lock."
  version="$(lock_get tacplus "$lock")"
  is_version "$version" || refuse "$lock: tacplus: '$version' must be an exact MAJOR.MINOR.PATCH release"
  image="$(lock_get build_image "$lock")"
  case "$image" in
    *@sha256:*) ;;
    *) refuse "$lock: build_image: '$image' must name the image by digest (@sha256:...); run \`tacplus pin\`" ;;
  esac
  if [ -n "$line" ] && [ "${version%.*}" != "$line" ]; then
    refuse "$lock pins tacplus $version, of line ${version%.*}, but this is the build image of line $line: build in $image"
  fi
  if [ -n "${TACPLUS_VERSION:-}" ] && [ "$TACPLUS_VERSION" != "$version" ]; then
    refuse "TACPLUS_VERSION=$TACPLUS_VERSION, but $lock pins $version: in a repository the pin decides (move it with \`tacplus pin\`)"
  fi
elif [ -n "${TACPLUS_VERSION:-}" ]; then
  version="$TACPLUS_VERSION"
  is_version "$version" || refuse "TACPLUS_VERSION='$version' must be an exact MAJOR.MINOR.PATCH release"
else
  version="$(newest "$line")"
fi

[ -x "$cache/$version/tacplus" ] || install_release "$version"
exec "$cache/$version/tacplus" "$@"
