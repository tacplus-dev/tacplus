#!/usr/bin/env bash
# Shipped in the build image as /usr/local/bin/tacplus-image-build (#315):
# builds a container image inside the build container with rootless BuildKit,
# with no host Docker socket and no daemon on the host
# (tacplus-dev/roadmap: decisions/builds-in-containers-2026-11-03.md).
#
#   tacplus-image-build [-f FILE] [--platform P[,P...]] [--build-arg K=V]...
#                       [--target STAGE] [--cache NAME] [--output SPEC]
#                       [--metadata-file F] CONTEXT
#   tacplus-image-build start | stop
#
# FILE defaults to CONTEXT/Dockerfile; --output is buildctl's (type=image,
# name=...,push=true; type=oci,dest=x.tar); none builds and caches only.
# Several --platform values build a multi-arch image; a platform other than
# this machine's runs under BuildKit's own QEMU helpers (buildkit-qemu-*),
# which need no binfmt registration on the host.
#
# The daemon is buildkitd under RootlessKit, as the unprivileged user `build`
# when called as root. It needs a user namespace, which a container gets
# with `--security-opt seccomp=unconfined --security-opt apparmor=unconfined`
# (BuildKit's docs, docs/rootless.md), never --privileged and never a socket.
# BUILDKIT_HOST names another daemon instead, and nothing is started.
#
# The cache is the build's configuration, never this script's:
#   TACPLUS_CACHE_REGISTRY  <registry>/<repository>: BuildKit's registry cache
#                           at <it>/<NAME>:buildcache-<platforms>, imported and
#                           exported (mode=max);
#   TACPLUS_CACHE_DIR       otherwise, a local cache at <it>/buildkit/<NAME>.
# Neither builds cold. NAME defaults to FILE's name less .Dockerfile.
#
# The image is reproducible: SOURCE_DATE_EPOCH (else CONTEXT's commit time)
# is passed to the build and every timestamp in the exported image is
# rewritten to it, so a cold and a warm build of one commit give one digest
# (images/cache-determinism.sh).
set -euo pipefail

run_dir=/run/buildkit
sock="$run_dir/buildkitd.sock"

die() { echo "tacplus-image-build: $*" >&2; exit 2; }
note() { echo "tacplus-image-build: $*" >&2; }

ready() { buildctl --addr "$1" debug workers >/dev/null 2>&1; }

start() {
  if [[ -n "${BUILDKIT_HOST:-}" ]]; then
    echo "$BUILDKIT_HOST"
    return 0
  fi
  local addr="unix://$sock"
  if ready "$addr"; then
    echo "$addr"
    return 0
  fi
  local as=() home=$HOME
  if [[ "$(id -u)" == 0 ]]; then
    # Rootless means not root: BuildKit runs as `build`, whose subordinate
    # IDs (/etc/subuid) RootlessKit maps into the daemon's user namespace.
    as=(setpriv --reuid=build --regid=build --init-groups)
    home=$(getent passwd build | cut -d: -f6)
  fi
  [[ -d "$run_dir" && -w "$run_dir" || "$(id -u)" == 0 ]] || die "$run_dir is not writable by $(id -un)"
  # The container's own network (RootlessKit's host driver), and its PID
  # namespace: a nested one needs a procfs mount the container forbids.
  nohup "${as[@]}" env HOME="$home" XDG_RUNTIME_DIR="$run_dir" \
    rootlesskit --net=host --state-dir="$run_dir/rootlesskit" \
    buildkitd --rootless --addr "$addr" --root "$home/.local/share/buildkit" \
    --oci-worker-net=host --oci-worker-no-process-sandbox \
    >"$run_dir/buildkitd.log" 2>&1 &
  echo $! >"$run_dir/buildkitd.pid"
  for _ in $(seq 1 60); do
    if ready "$addr"; then
      echo "$addr"
      return 0
    fi
    kill -0 "$(cat "$run_dir/buildkitd.pid")" 2>/dev/null || break
    sleep 1
  done
  tail -n 20 "$run_dir/buildkitd.log" >&2 || true
  die "rootless BuildKit did not start. It needs a user namespace: run this container with --security-opt seccomp=unconfined --security-opt apparmor=unconfined (and, as a user other than root or build, entries in /etc/subuid and /etc/subgid)"
}

stop() {
  [[ -f "$run_dir/buildkitd.pid" ]] || return 0
  kill "$(cat "$run_dir/buildkitd.pid")" 2>/dev/null || true
  rm -f "$run_dir/buildkitd.pid"
}

case "${1:-}" in
  start) start; exit 0 ;;
  stop) stop; exit 0 ;;
  "" | -h | --help) sed -n '2,32p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
esac

file="" platform="" target="" name="" output="" metadata="" ctx=""
build_args=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    -f | --file) file=$2; shift 2 ;;
    --platform) platform=$2; shift 2 ;;
    --build-arg) build_args+=("$2"); shift 2 ;;
    --target) target=$2; shift 2 ;;
    --cache) name=$2; shift 2 ;;
    --output) output=$2; shift 2 ;;
    --metadata-file) metadata=$2; shift 2 ;;
    -*) die "unknown option $1" ;;
    *) [[ -z "$ctx" ]] || die "one CONTEXT, not '$ctx' and '$1'"; ctx=$1; shift ;;
  esac
done
[[ -n "$ctx" ]] || die "no CONTEXT (tacplus-image-build --help)"
[[ -d "$ctx" ]] || die "CONTEXT $ctx is not a directory"
file=${file:-$ctx/Dockerfile}
[[ -f "$file" ]] || die "no Dockerfile at $file"
if [[ -z "$name" ]]; then
  name=$(basename "$file")
  name=${name%.Dockerfile}
  [[ "$name" == Dockerfile ]] && name=$(basename "$(cd "$ctx" && pwd)")
fi

args=(build --frontend dockerfile.v0
  --local "context=$ctx" --local "dockerfile=$(dirname "$file")"
  --opt "filename=$(basename "$file")")
[[ -z "$platform" ]] || args+=(--opt "platform=$platform")
[[ -z "$target" ]] || args+=(--opt "target=$target")
for a in "${build_args[@]}"; do args+=(--opt "build-arg:$a"); done

epoch=${SOURCE_DATE_EPOCH:-}
if [[ -z "$epoch" ]] && git -C "$ctx" rev-parse HEAD >/dev/null 2>&1; then
  epoch=$(git -C "$ctx" log -1 --format=%ct)
fi
if [[ -n "$epoch" ]]; then
  args+=(--opt "build-arg:SOURCE_DATE_EPOCH=$epoch")
else
  note "no SOURCE_DATE_EPOCH and $ctx is not a git checkout: the image's timestamps are the build's"
fi

if [[ -n "$output" ]]; then
  case "$output" in
    type=image,* | type=oci,* | type=docker,* | type=image | type=oci | type=docker)
      [[ -z "$epoch" || "$output" == *rewrite-timestamp=* ]] || output+=",rewrite-timestamp=true" ;;
  esac
  args+=(--output "$output")
fi
[[ -z "$metadata" ]] || args+=(--metadata-file "$metadata")

# The registry cache of one image is per platform set: the per-architecture
# builds of a release run side by side and must not overwrite each other's.
tag="buildcache"
[[ -z "$platform" ]] || tag+="-$(tr ',/' '--' <<<"$platform")"
if [[ -n "${TACPLUS_CACHE_REGISTRY:-}" ]]; then
  ref="${TACPLUS_CACHE_REGISTRY%/}/$name:$tag"
  args+=(--import-cache "type=registry,ref=$ref"
    --export-cache "type=registry,ref=$ref,mode=max,image-manifest=true,oci-mediatypes=true,ignore-error=true")
  note "cache: registry $ref"
elif [[ -n "${TACPLUS_CACHE_DIR:-}" ]]; then
  local_cache="${TACPLUS_CACHE_DIR%/}/buildkit/$name-$tag"
  [[ ! -f "$local_cache/index.json" ]] || args+=(--import-cache "type=local,src=$local_cache")
  args+=(--export-cache "type=local,dest=$local_cache,mode=max,ignore-error=true")
  note "cache: local $local_cache"
else
  note "cache: none (cold)"
fi

BUILDKIT_HOST=$(start)
export BUILDKIT_HOST
exec buildctl "${args[@]}"
