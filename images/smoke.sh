#!/usr/bin/env bash
# Smoke test shipped as /usr/local/bin/tacplus-smoke. Every tool the image
# promises answers. TACPLUS_IMAGE says which promise: slim (the tacplus
# image: codegen for every language) or build (the build image: that plus
# every SDK and test tool).
set -euo pipefail
gen=(protoc protoc-gen-go protoc-gen-go-grpc protoc-gen-grpc-java protoc-gen-es protoc-gen-prost protoc-gen-tonic protoc-gen-prost-serde protoc-gen-prost-crate protoc-gen-swift protoc-gen-grpc-swift-2 git node gofmt python3)
sdk=(go java mvn cargo rustc swift dotnet npm just jq jbmc gh docker gcloud black)
want=("${gen[@]}")
[[ "${TACPLUS_IMAGE:-build}" == build ]] && want+=("${sdk[@]}")
for t in "${want[@]}"; do
  command -v "$t" >/dev/null || { echo "missing: $t" >&2; exit 1; }
done
# gcloud ships without its bundled Python; it must still start on the system one.
[[ "${TACPLUS_IMAGE:-build}" == build ]] && { gcloud --version >/dev/null || { echo "gcloud does not start" >&2; exit 1; }; }
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
python3 -c 'import grpc_tools.protoc'
protoc --version; protoc-gen-swift --version; protoc-gen-grpc-swift-2 --version; node --version
if [[ "${TACPLUS_IMAGE:-build}" == build ]]; then
  go version; java -version 2>&1 | head -1; cargo --version; swift --version 2>&1 | head -1; dotnet --version
  jbmc --version; just --version
else
  tacplus version
fi
echo "tacplus-smoke: OK (${TACPLUS_IMAGE:-build})"
