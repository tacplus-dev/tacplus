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
python3 -c 'import grpc_tools.protoc'
protoc --version; protoc-gen-swift --version; protoc-gen-grpc-swift-2 --version; node --version
if [[ "${TACPLUS_IMAGE:-build}" == build ]]; then
  go version; java -version 2>&1 | head -1; cargo --version; swift --version 2>&1 | head -1; dotnet --version
  jbmc --version; just --version
else
  tacplus version
fi
echo "tacplus-smoke: OK (${TACPLUS_IMAGE:-build})"
