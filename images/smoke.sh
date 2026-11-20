#!/usr/bin/env bash
# Smoke test for the tacplus image: every tool a build needs answers, and
# tacplus answers. Shipped in the image as /usr/local/bin/tacplus-smoke.
set -euo pipefail
# scripts/images_smoke_test.go sources this file for go_stdlib_check.
# A Go program copied from upstream must be built with at least the fixed
# release of its own Go line (floors oldest line first): a line older than
# every floor is unfixed, a line newer than the oldest floor with none of its
# own never had the bug.
go_stdlib_floor() {
  local bin=$1 floor have min=; shift
  have=$(go version "$bin" | awk '{ print $NF }')
  for floor in "$@"; do
    [[ ${floor%.*} == "${have%.*}" ]] && min=$floor
  done
  if [[ -z $min ]]; then
    [[ "$(printf '%s\n%s\n' "${1#go}" "${have#go}" | sort -V | head -n 1)" == "${1#go}" ]] && return 0
    min=$1
  fi
  [[ "$(printf '%s\n%s\n' "${min#go}" "${have#go}" | sort -V | head -n 1)" == "${min#go}" ]] \
    || { echo "${bin##*/} is built with ${have:-no Go}, below $min" >&2; return 1; }
}
go_stdlib_check() {
  # CVE-2026-97054 (#901): the docker CLI comes from docker:DOCKER_VERSION-cli
  # (build.Dockerfile), built with whatever Go upstream used; 29.9.3's was go1.26.10.
  go_stdlib_floor "${TACPLUS_SMOKE_DOCKER:-$(command -v docker)}" go1.26.11 go1.27.4
}
(return 0 2>/dev/null) && return 0
for t in protoc protoc-gen-go protoc-gen-go-grpc protoc-gen-grpc-java protoc-gen-es protoc-gen-prost protoc-gen-tonic protoc-gen-prost-serde protoc-gen-prost-crate go java mvn node npm cargo rustc just jq docker gcloud tacplus; do
  command -v "$t" >/dev/null || { echo "missing: $t" >&2; exit 1; }
done
# gcloud ships without its bundled Python; it must still start on the system one.
gcloud --version >/dev/null || { echo "gcloud does not start" >&2; exit 1; }
# protoc executes each plugin, so a plugin must be a program for this image's
# architecture. A file of another architecture, or a saved download, fails only
# at generation time, with a shell syntax error. ELF e_machine is b7 00 for
# aarch64 and 3e 00 for x86-64; a script (a shebang) is accepted as it is.
case "$(uname -m)" in
  aarch64 | arm64) machine=b700 ;;
  x86_64) machine=3e00 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
for t in protoc-gen-go protoc-gen-go-grpc protoc-gen-grpc-java protoc-gen-es protoc-gen-prost protoc-gen-tonic protoc-gen-prost-serde protoc-gen-prost-crate; do
  p=$(command -v "$t")
  case "$(head -c 4 "$p" | od -An -tx1 | tr -d ' \n')" in
    7f454c46) [[ "$(od -An -tx1 -j18 -N2 "$p" | tr -d ' \n')" == "$machine" ]] || { echo "wrong architecture: $t" >&2; exit 1; } ;;
    2321*) ;;
    *) echo "not a program: $t" >&2; exit 1 ;;
  esac
done
# docker-buildx is built from source with dependencies bumped past advisories
# its release has not taken yet (build.Dockerfile, buildx stage): it must
# start, and carry each module at least at its floor.
docker buildx version
buildx=/usr/local/libexec/docker/cli-plugins/docker-buildx
for floor in github.com/moby/go-archive@v0.3.0 golang.org/x/net@v0.62.0; do
  mod=${floor%@*} min=${floor#*@}
  have=$(go version -m "$buildx" | awk -v m="$mod" '$1 == "dep" && $2 == m { print $3 }')
  [[ -n "$have" && "$(printf '%s\n%s\n' "$min" "$have" | sort -V | head -n 1)" == "$min" ]] \
    || { echo "docker-buildx carries $mod ${have:-none}, below $min" >&2; exit 1; }
done
# The docker CLI, copied from upstream's image, is built with a fixed Go.
go_stdlib_check
# Maven's guava is replaced past its advisories (base.Dockerfile, #222): one
# guava in Maven's lib/, at least at its floor.
guava=(/usr/share/maven/lib/guava-*.jar)
[[ ${#guava[@]} -eq 1 ]] || { echo "Maven's lib/ carries ${#guava[@]} guava jars" >&2; exit 1; }
have=${guava[0]##*/guava-} have=${have%-jre.jar}
[[ "$(printf '%s\n%s\n' 33.7.2 "$have" | sort -V | head -n 1)" == 33.7.2 ]] \
  || { echo "Maven carries guava $have, below 33.7.2" >&2; exit 1; }
mvn --version
protoc --version; go version; java -version 2>&1 | head -1; node --version; cargo --version; just --version; docker --version; gcloud --version | head -1
tacplus version
