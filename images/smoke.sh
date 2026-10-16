#!/usr/bin/env bash
# Smoke test shipped in both tacplus images as /usr/local/bin/tacplus-smoke:
# every tool the image promises answers. TACPLUS_IMAGE (slim|full) says which
# promise this image makes.
set -euo pipefail
gen=(protoc protoc-gen-go protoc-gen-go-grpc protoc-gen-grpc-java protoc-gen-es protoc-gen-prost protoc-gen-tonic protoc-gen-prost-serde protoc-gen-prost-crate git node gofmt tacplus)
build=(go java mvn cargo rustc just jq docker gcloud)
want=("${gen[@]}")
[[ "${TACPLUS_IMAGE:-full}" == full ]] && want+=("${build[@]}")
for t in "${want[@]}"; do
  command -v "$t" >/dev/null || { echo "missing: $t" >&2; exit 1; }
done
protoc --version; node --version; git --version
if [[ "${TACPLUS_IMAGE:-full}" == full ]]; then
  go version; java -version 2>&1 | head -1; cargo --version; just --version; docker --version; gcloud --version | head -1
fi
tacplus version
echo "tacplus-smoke: OK (${TACPLUS_IMAGE:-full})"
