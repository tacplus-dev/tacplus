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

# Behavioral coverage of W2S request (ingress) validation in the generated
# Rust REST dispatch module.
w2s-validate:
  bash cross-test/w2s-validate/run.sh

# Cross-language serialization-equivalence corpus: one message, encoded once by
# go into checked-in golden bytes, decoded and value-asserted by all five
# generated languages. Local/manual — not part of `test`.
wire-corpus:
  bash cross-test/wire-corpus/run.sh

wire-corpus-go:
  bash cross-test/wire-corpus/run.sh --language go

wire-corpus-java:
  bash cross-test/wire-corpus/run.sh --language java

wire-corpus-python:
  bash cross-test/wire-corpus/run.sh --language python

wire-corpus-rust:
  bash cross-test/wire-corpus/run.sh --language rust

wire-corpus-typescript:
  bash cross-test/wire-corpus/run.sh --language typescript

# Re-encode the golden bytes from the fixture's own expect blocks. Manual only.
wire-corpus-emit:
  bash cross-test/wire-corpus/run.sh --emit

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

# Scratch dirs are left behind only when a run is SIGKILLed before its cleanup
# can run. Never wired into `test` or any gate. Only touches top-level
# `tacplus-*` entries older than a day, so it can't race a running job.
# Manual housekeeping: remove leaked tacplus-* scratch dirs under ${TMPDIR}.
clean-scratch:
  #!/usr/bin/env bash
  set -euo pipefail
  dir="${TMPDIR:-/tmp}"
  # Strip trailing slashes; "/" collapses to "" and is rejected below.
  while [[ "$dir" == */ ]]; do dir="${dir%/}"; done
  if [[ -z "$dir" ]]; then
    echo "clean-scratch: refusing to run: TMPDIR resolves to '/' or is empty" >&2
    exit 1
  fi
  if [[ ! -d "$dir" ]]; then
    echo "clean-scratch: refusing to run: '$dir' is not a directory" >&2
    exit 1
  fi
  echo "clean-scratch: sweeping ${dir}/tacplus-* older than 1 day"
  find "$dir" -maxdepth 1 -name 'tacplus-*' -mtime +1 -print -exec rm -rf {} +
