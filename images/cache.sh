#!/usr/bin/env bash
# Shipped in the build image as /usr/local/bin/tacplus-cache (#315): points
# the compiler caches of a build at the shared cache its configuration names,
# and does nothing when it names none (a cold build, as for anyone building
# from source). A cache only speeds a build up: the verdict and the artifacts
# are the same cold or warm (images/cache-determinism.sh proves it).
#
# The configuration is the build's, never this script's (the build job sets
# it; tacplus-dev/cloud owns the bucket and the registry):
#   TACPLUS_CACHE_BUCKET  gs://<bucket>[/<prefix>], or file:///<dir> for a
#                         shared directory: sccache's storage, and where the
#                         Go and Maven snapshots are kept.
#   TACPLUS_CACHE_DIR     the local cache directory (GOCACHE, GOMODCACHE, the
#                         Maven repository, and sccache's when there is no
#                         bucket). Defaults to ~/.cache/tacplus-build when a
#                         bucket is set.
#   TACPLUS_CACHE_REGISTRY  BuildKit's registry cache; tacplus-image-build
#                         reads it, not this script.
#
#   eval "$(tacplus-cache env)"  export the compiler cache settings
#   tacplus-cache restore [DIR]  fetch the Go and Maven snapshots DIR's lock
#                                files key (go.sum; pom.xml), when they exist
#   tacplus-cache save [DIR]     store them under that key, once: an entry is
#                                never overwritten
#   tacplus-cache key KIND [DIR] print the key of KIND (go, maven) for DIR
#
# Keys are content: the toolchain's version and every lock file of DIR. Go's
# build cache and Maven's repository are themselves keyed by content (action
# IDs; coordinates checked against their checksums), so a snapshot can make a
# build faster but never different. restore and save never fail a build: a
# missing entry, a missing credential or an unreachable bucket is a cold cache.
set -euo pipefail

bucket=${TACPLUS_CACHE_BUCKET:-}
bucket=${bucket%/}
dir=${TACPLUS_CACHE_DIR:-}
if [[ -z "$dir" && -n "$bucket" ]]; then
  dir="${XDG_CACHE_HOME:-$HOME/.cache}/tacplus-build"
fi

die() { echo "tacplus-cache: $*" >&2; exit 2; }
note() { echo "tacplus-cache: $*" >&2; }

case "$bucket" in
  "" | gs://?* | file:///*) ;;
  *) die "TACPLUS_CACHE_BUCKET must be gs://<bucket>[/<prefix>] or file:///<dir>, not '$bucket'" ;;
esac
[[ -z "$dir" || "$dir" == /* ]] || die "TACPLUS_CACHE_DIR must be an absolute path, not '$dir'"

# What each kind snapshots, under $dir, and the lock files its key reads.
dirs_of() {
  case "$1" in
    go) echo "go-build go-mod" ;;
    maven) echo "m2" ;;
    *) die "unknown cache kind '$1' (go, maven)" ;;
  esac
}
locks_of() {
  case "$1" in
    go) echo "go.sum" ;;
    maven) echo "pom.xml" ;;
  esac
}
tool_of() {
  case "$1" in
    go) go version 2>/dev/null || echo "go: none" ;;
    maven) { java -version 2>&1 | head -n 1; mvn --version 2>/dev/null | head -n 1; } || echo "maven: none" ;;
  esac
}

# key KIND DIR: KIND-<sha256 of the toolchain and DIR's lock files, by path>.
key() {
  local kind=$1 src=$2 lock files
  lock=$(locks_of "$kind")
  if git -C "$src" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    files=$(git -C "$src" ls-files -- ":(glob)**/$lock" | LC_ALL=C sort)
  else
    files=$(cd "$src" && find . -name "$lock" -not -path '*/node_modules/*' -not -path '*/target/*' | sed 's|^\./||' | LC_ALL=C sort)
  fi
  {
    echo "tacplus-cache/1 $kind"
    tool_of "$kind"
    while IFS= read -r f; do
      [[ -n "$f" && -f "$src/$f" ]] || continue
      printf '%s %s\n' "$(sha256sum < "$src/$f" | cut -d' ' -f1)" "$f"
    done <<<"$files"
  } | sha256sum | cut -c1-32 | sed "s/^/$kind-/"
}

# The snapshot object of a key, and the copies to and from it.
object() { echo "$bucket/snapshots/$1.tar.gz"; }
exists() {
  case "$bucket" in
    file://*) [[ -f "${1#file://}" ]] ;;
    gs://*) gcloud storage objects describe "$1" >/dev/null 2>&1 ;;
  esac
}
fetch() { # object -> stdout
  case "$bucket" in
    file://*) cat "${1#file://}" ;;
    gs://*) gcloud storage cat "$1" ;;
  esac
}
store() { # stdin -> object, by way of a temporary name so no reader sees half of it
  case "$bucket" in
    file://*) local f=${1#file://}; mkdir -p "$(dirname "$f")" && cat > "$f.tmp.$$" && mv -n "$f.tmp.$$" "$f"; rm -f "$f.tmp.$$" ;;
    gs://*) local t; t=$(mktemp) && cat > "$t" && gcloud storage cp --quiet --if-generation-match=0 "$t" "$1" >/dev/null 2>&1; local rc=$?; rm -f "$t"; return $rc ;;
  esac
}

cmd_env() {
  [[ -n "$dir" ]] || return 0 # cold: nothing configured, nothing changed
  q() { printf '%q' "$1"; }
  echo "export GOCACHE=$(q "$dir/go-build")"
  echo "export GOMODCACHE=$(q "$dir/go-mod")"
  # Maven 3.9 prepends MAVEN_ARGS to every mvn command line.
  echo "export MAVEN_ARGS=$(q "-Dmaven.repo.local=$dir/m2${MAVEN_ARGS:+ $MAVEN_ARGS}")"
  # sccache caches whole crate compilations, not incremental ones.
  echo "export RUSTC_WRAPPER=sccache"
  echo "export CARGO_INCREMENTAL=0"
  case "$bucket" in
    gs://*)
      local path=${bucket#gs://} name prefix=""
      name=${path%%/*}
      [[ "$path" == */* ]] && prefix="${path#*/}/"
      echo "export SCCACHE_GCS_BUCKET=$(q "$name")"
      echo "export SCCACHE_GCS_KEY_PREFIX=$(q "${prefix}sccache")"
      echo "export SCCACHE_GCS_RW_MODE=READ_WRITE"
      ;;
    file://*) echo "export SCCACHE_DIR=$(q "${bucket#file://}/sccache")" ;;
    "") echo "export SCCACHE_DIR=$(q "$dir/sccache")" ;;
  esac
}

cmd_restore() {
  local src=${1:-.} kind k o
  [[ -n "$bucket" ]] || { note "no TACPLUS_CACHE_BUCKET: nothing to restore (cold)"; return 0; }
  for kind in go maven; do
    k=$(key "$kind" "$src") o=$(object "$k")
    mkdir -p "$dir"
    if exists "$o" && fetch "$o" | tar -xz -C "$dir" 2>/dev/null; then
      note "$kind: restored $k"
    else
      note "$kind: no snapshot $k (cold)"
    fi
  done
}

cmd_save() {
  local src=${1:-.} kind k o d have
  [[ -n "$bucket" ]] || { note "no TACPLUS_CACHE_BUCKET: nothing to save"; return 0; }
  for kind in go maven; do
    k=$(key "$kind" "$src") o=$(object "$k") have=()
    for d in $(dirs_of "$kind"); do [[ -d "$dir/$d" ]] && have+=("$d"); done
    if [[ ${#have[@]} -eq 0 ]]; then
      note "$kind: nothing to save"
    elif exists "$o"; then
      note "$kind: $k is already stored"
    elif tar -cz -C "$dir" "${have[@]}" 2>/dev/null | store "$o"; then
      note "$kind: saved $k"
    else
      note "$kind: could not save $k (the build is unaffected)"
    fi
  done
}

case "${1:-}" in
  env) cmd_env ;;
  restore) shift; cmd_restore "$@" ;;
  save) shift; cmd_save "$@" ;;
  key) [[ $# -ge 2 ]] || die "usage: tacplus-cache key go|maven [DIR]"; dirs_of "$2" >/dev/null; key "$2" "${3:-.}" ;;
  *) die "usage: tacplus-cache env | restore [DIR] | save [DIR] | key go|maven [DIR]" ;;
esac
