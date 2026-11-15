#!/usr/bin/env bash
# Smoke test shipped as /usr/local/bin/tacplus-smoke. Every tool the build
# image promises answers: codegen for every language, every SDK, the test
# tools, the verifiers (Kani, JBMC) and the fuzzers (Go's own, cargo-fuzz), and
# its tacplus is the launcher of the pinned release (images/tacplus.sh), not a
# compiler. images/smoke-pipeline.sh then runs generate, test, verify and fuzz.
set -euo pipefail
# docker-buildx and gh are built from source with dependencies bumped past
# advisories their releases have not taken yet (build.Dockerfile, the buildx
# and gh stages): each must carry every bumped module at least at its floor.
# scripts/images_smoke_test.go sources this file for these two functions.
go_floors() {
  local bin=$1 floor mod min have; shift
  for floor in "$@"; do
    mod=${floor%@*} min=${floor#*@}
    have=$(go version -m "$bin" | awk -v m="$mod" '$1 == "dep" && $2 == m { print $3 }')
    [[ -n "$have" && "$(printf '%s\n%s\n' "$min" "$have" | sort -V | head -n 1)" == "$min" ]] \
      || { echo "${bin##*/} carries $mod ${have:-none}, below $min" >&2; return 1; }
  done
}
go_floors_check() {
  go_floors "${TACPLUS_SMOKE_BUILDX:-/usr/local/libexec/docker/cli-plugins/docker-buildx}" \
    github.com/moby/go-archive@v0.3.0 golang.org/x/net@v0.62.0 || return 1
  # CVE-2026-78661 (#350): gh's releases (through 2.103.3) build with x/net 0.60.0.
  go_floors "$(command -v gh)" golang.org/x/net@v0.62.0
}
(return 0 2>/dev/null) && return 0
gen=(protoc protoc-gen-go protoc-gen-go-grpc protoc-gen-grpc-java protoc-gen-es protoc-gen-prost protoc-gen-tonic protoc-gen-prost-serde protoc-gen-prost-crate protoc-gen-swift protoc-gen-grpc-swift-2 git node gofmt python3)
sdk=(go java mvn cargo rustc swift dotnet npm just jq jbmc gh docker gcloud black cargo-kani kani cargo-fuzz curl tacplus)
# Image builds inside the container and the compiler cache (#315).
build=(buildkitd buildctl buildkit-runc rootlesskit newuidmap newgidmap setpriv sccache tacplus-image-build tacplus-cache)
for t in "${gen[@]}" "${sdk[@]}" "${build[@]}"; do
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
for t in "${gen[@]}"; do
  [[ "$t" == protoc-gen-* ]] || continue
  p=$(command -v "$t")
  case "$(head -c 4 "$p" | od -An -tx1 | tr -d ' \n')" in
    7f454c46) [[ "$(od -An -tx1 -j18 -N2 "$p" | tr -d ' \n')" == "$machine" ]] || { echo "wrong architecture: $t" >&2; exit 1; } ;;
    2321*) ;;
    *) echo "not a program: $t" >&2; exit 1 ;;
  esac
done
# docker-buildx and gh, built from source (go_floors_check above), must start
# and carry each bumped module at least at its floor.
docker buildx version
gh --version
go_floors_check
# So do BuildKit, its runc and RootlessKit (build.Dockerfile, buildkit and
# runc stages, #315), for each bumped module they carry.
for bin in buildkitd buildctl buildkit-runc rootlesskit; do
  for floor in github.com/moby/go-archive@v0.3.0 golang.org/x/net@v0.62.0; do
    mod=${floor%@*} min=${floor#*@}
    have=$(go version -m "$(command -v "$bin")" | awk -v m="$mod" '$1 == "dep" && $2 == m { print $3 }')
    [[ -z "$have" || "$(printf '%s\n%s\n' "$min" "$have" | sort -V | head -n 1)" == "$min" ]] \
      || { echo "$bin carries $mod $have, below $min" >&2; exit 1; }
  done
done
# Maven's guava is replaced past its advisories (base.Dockerfile, #222): one
# guava in Maven's lib/, at least at its floor.
guava=(/usr/share/maven/lib/guava-*.jar)
[[ ${#guava[@]} -eq 1 && -f ${guava[0]} ]] || { echo "Maven's lib/ carries ${#guava[@]} guava jars" >&2; exit 1; }
have=${guava[0]##*/guava-} have=${have%-jre.jar}
[[ "$(printf '%s\n%s\n' 33.7.2 "$have" | sort -V | head -n 1)" == 33.7.2 ]] \
  || { echo "Maven carries guava $have, below 33.7.2" >&2; exit 1; }
mvn --version
python3 -c 'import grpc_tools.protoc'
protoc --version; protoc-gen-swift --version; protoc-gen-grpc-swift-2 --version; node --version
go version; java -version 2>&1 | head -1; cargo --version; swift --version 2>&1 | head -1; dotnet --version
jbmc --version; just --version; cargo kani --version; cargo +"$RUST_NIGHTLY" fuzz --version
# Rootless BuildKit runs as `build`, from its subordinate IDs, and emulates a
# foreign platform's RUN steps with its own QEMU helper (images/image-build.sh).
buildkitd --version; buildctl --version; rootlesskit --version; sccache --version
{ grep -q '^build:' /etc/subuid && grep -q '^build:' /etc/subgid; } || { echo "user build has no subordinate IDs" >&2; exit 1; }
[[ "$(stat -c %U /run/buildkit)" == build ]] || { echo "/run/buildkit is not build's" >&2; exit 1; }
case "$(uname -m)" in
  aarch64 | arm64) command -v buildkit-qemu-x86_64 >/dev/null ;;
  x86_64) command -v buildkit-qemu-aarch64 >/dev/null ;;
esac || { echo "missing BuildKit's QEMU helper for the other platform" >&2; exit 1; }
# Nothing configured is a cold build: tacplus-cache changes nothing.
[[ -z "$(env -u TACPLUS_CACHE_BUCKET -u TACPLUS_CACHE_DIR tacplus-cache env)" ]] || { echo "tacplus-cache env is not empty with no cache configured" >&2; exit 1; }
# tacplus is the launcher (a POSIX sh script), never a compiler binary.
[[ "$(head -n 1 "$(command -v tacplus)")" == '#!/bin/sh' ]] || { echo "tacplus is not the launcher (images/tacplus.sh)" >&2; exit 1; }
# No Debian package beyond the image's declared lists (images/*.packages, #554).
tacplus-packages-check /usr/local/share/tacplus/packages/base /usr/local/share/tacplus/packages/build
echo "tacplus-smoke: OK (build image, line ${TACPLUS_LINE:-none})"
