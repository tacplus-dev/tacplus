#!/usr/bin/env bash
# Smoke test for the tacplus image: every tool a build needs answers, and
# tacplus answers. Shipped in the image as /usr/local/bin/tacplus-smoke.
set -euo pipefail
for t in protoc protoc-gen-go protoc-gen-go-grpc protoc-gen-grpc-java protoc-gen-es protoc-gen-prost protoc-gen-tonic protoc-gen-prost-serde protoc-gen-prost-crate go java mvn node npm cargo rustc just jq docker gcloud tacplus; do
  command -v "$t" >/dev/null || { echo "missing: $t" >&2; exit 1; }
done
protoc --version; go version; java -version 2>&1 | head -1; node --version; cargo --version; just --version; docker --version; gcloud --version | head -1
tacplus version
