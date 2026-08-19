# Local development recipes.

pkg := "gitlab.com/secondstarcavalier/schematic/tacplus/pkg/buildinfo"

default: install

generate:
  go generate ./pkg/internal/proto/spec/...

build: generate
  mkdir -p bin
  go build -trimpath -ldflags="-s -w" -o bin/tacplus .

install: generate
  #!/usr/bin/env bash
  set -euo pipefail
  tag=$(git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")
  sha=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
  # Local build: mark the commit as a local build so it's never mistaken for CI.
  ldflags="-s -w -X {{pkg}}.Version=${tag} -X {{pkg}}.GitSha=${sha}-local"
  go install -trimpath -ldflags="$ldflags" .
  echo "Installed: tacplus ${tag}"

test: soundness
  go test ./...

# Model/template soundness gate. Runs as part of `test`.
soundness:
  go run ./cmd/soundness -root .

vet:
  go vet ./...

current-generation:
  bash cross-test/current-generation/run.sh

current-generation-go:
  bash cross-test/current-generation/run.sh --language go

current-generation-java:
  bash cross-test/current-generation/run.sh --language java

current-generation-rust:
  bash cross-test/current-generation/run.sh --language rust

determinism:
  bash cross-test/determinism/run.sh

determinism-go:
  bash cross-test/determinism/run.sh --language go

determinism-rust:
  bash cross-test/determinism/run.sh --language rust

determinism-python:
  bash cross-test/determinism/run.sh --language python

determinism-typescript:
  bash cross-test/determinism/run.sh --language typescript

determinism-java:
  bash cross-test/determinism/run.sh --language java

# Behavioral coverage of the generated TLS/SMB cert-stream loops.
cert-stream: cert-stream-go cert-stream-java cert-stream-rust

cert-stream-go:
  bash cross-test/cert-stream/run.sh --language go

cert-stream-java:
  bash cross-test/cert-stream/run.sh --language java

cert-stream-rust:
  bash cross-test/cert-stream/run.sh --language rust

# PC-L-9 chaos soak: generates+builds a driver binary per run, too slow for
# `just test`. Excluded from `go test ./...` via the `soak` build tag.
leak-soak:
  go test -tags soak ./cross-test/chaos/leaksoak/...

consumer-build:
  bash cross-test/consumer-contract/run.sh

consumer-build-go:
  bash cross-test/consumer-contract/run.sh --language go

consumer-build-java:
  bash cross-test/consumer-contract/run.sh --language java

consumer-build-python:
  bash cross-test/consumer-contract/run.sh --language python

consumer-build-typescript:
  bash cross-test/consumer-contract/run.sh --language typescript

consumer-build-rust:
  bash cross-test/consumer-contract/run.sh --language rust

consumer-build-swift:
  bash cross-test/consumer-contract/run.sh --language swift

consumer-smoke:
  bash cross-test/consumer-smoke/run.sh

consumer-smoke-go:
  bash cross-test/consumer-smoke/run.sh --language go

consumer-smoke-java:
  bash cross-test/consumer-smoke/run.sh --language java

consumer-smoke-python:
  bash cross-test/consumer-smoke/run.sh --language python

consumer-smoke-rust:
  bash cross-test/consumer-smoke/run.sh --language rust

consumer-smoke-typescript:
  bash cross-test/consumer-smoke/run.sh --language typescript

clean:
  rm -rf bin target
