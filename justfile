# Local development recipes.

pkg := "gitlab.com/seabeam/schematic/tacplus/pkg/buildinfo"

# Every recipe below that regenerates or compiles writes this checkout's shared
# target/ and generated-sources/ trees. Several background jobs run these
# against the SAME checkout at once and have corrupted each other's results
# three times (2026-08-19, -08-22, -08-23). So those recipes run through
# scripts/with-tree-lock.sh, which serializes them on a lock scoped to this
# checkout; a second caller waits for the first instead of racing it. The lock
# is re-entrant, so a locked recipe may call `just <other-locked-recipe>`.
# If you add a recipe that regenerates or compiles, prefix it with {{lock}} too.
lock := "bash scripts/with-tree-lock.sh"

# Pinned host protoc. The generator shells out to the host `protoc`, and the
# gencode protoc emits declares a MINIMUM protobuf runtime version; the runtime
# refuses gencode newer than itself. So every protobuf-java pin in this repo
# must move in lockstep with this value:
#   cross-test/current-generation/run.sh  write_java_compile_pom()
#   cross-test/cert-stream/run.sh         write_java_compile_pom()
#   cross-test/consumer-contract/run.sh   (pom heredoc)
#   cross-test/server-runtime/fixture/java/pom.xml
#   cross-test/wire-corpus/decoder/java/pom.xml
#   cross-test/**/dependencies.yaml       com.google.protobuf:protobuf-java{,-util}
# On 2026-08-22 a `brew upgrade` silently moved protoc to 36.0 while the pins
# said 4.35.x, and three mandatory gates died with a three-layers-deep Java
# ExceptionInInitializerError instead of a version error. `just protoc-check`
# exists so that never happens silently again.
protoc_version := "36.0"

default: install

generate:
  {{lock}} go generate ./pkg/internal/proto/spec/...

build: generate
  mkdir -p bin
  {{lock}} go build -trimpath -ldflags="-s -w" -o bin/tacplus .

install: generate
  #!/usr/bin/env bash
  set -euo pipefail
  tag=$(git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")
  sha=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
  # Local build: mark the commit as a local build so it's never mistaken for CI.
  ldflags="-s -w -X {{pkg}}.Version=${tag} -X {{pkg}}.GitSha=${sha}-local"
  {{lock}} go install -trimpath -ldflags="$ldflags" .
  echo "Installed: tacplus ${tag}"

test: soundness
  {{lock}} go test ./...

# Model/template soundness gate. Runs as part of `test`.
soundness:
  {{lock}} go run ./cmd/soundness -root .

vet:
  go vet ./...

current-generation:
  {{lock}} bash cross-test/current-generation/run.sh

current-generation-go:
  {{lock}} bash cross-test/current-generation/run.sh --language go

current-generation-java:
  {{lock}} bash cross-test/current-generation/run.sh --language java

current-generation-rust:
  {{lock}} bash cross-test/current-generation/run.sh --language rust

determinism:
  {{lock}} bash cross-test/determinism/run.sh

determinism-go:
  {{lock}} bash cross-test/determinism/run.sh --language go

determinism-rust:
  {{lock}} bash cross-test/determinism/run.sh --language rust

determinism-python:
  {{lock}} bash cross-test/determinism/run.sh --language python

determinism-typescript:
  {{lock}} bash cross-test/determinism/run.sh --language typescript

determinism-java:
  {{lock}} bash cross-test/determinism/run.sh --language java

# Assert the host protoc is no newer than the pin above. Run as the first
# step of `gates`; run it by hand after any toolchain upgrade.
#
# This is a CEILING, not an equality: protoc gencode declares a MINIMUM
# protobuf runtime version, and the runtime refuses gencode newer than
# itself, so any protoc <= protoc_version is safe (CI's base image uses
# 28.3, well under the ceiling, and is fine). Only a protoc newer than the
# pin is dangerous - confirmed 2026-09-21: protoc 36.2 gencode throws
# ExceptionInInitializerError against the pinned 4.36.0 runtime.
protoc-check:
  #!/usr/bin/env bash
  set -uo pipefail
  if ! command -v protoc >/dev/null 2>&1; then
    echo "protoc-check: protoc not found on PATH (expected libprotoc <= {{protoc_version}})" >&2
    exit 1
  fi
  actual="$(protoc --version | awk '{print $2}')"
  IFS='.' read -r actual_major actual_minor _ <<< "$actual"
  IFS='.' read -r pin_major pin_minor _ <<< "{{protoc_version}}"
  if (( actual_major > pin_major || (actual_major == pin_major && actual_minor > pin_minor) )); then
    {
      echo "protoc-check: host protoc is ${actual}, which is NEWER than the pinned ceiling {{protoc_version}}."
      echo "  protoc gencode declares a minimum protobuf runtime version, and every"
      echo "  protobuf-java pin in this repo is matched to protoc {{protoc_version}}. The"
      echo "  runtime refuses gencode newer than itself, so a protoc above the pin"
      echo "  produces gencode the pinned runtime cannot load."
      echo "  A mismatch does NOT surface as a version error: it surfaces as a Java"
      echo "  ExceptionInInitializerError from the protobuf runtime check, several"
      echo "  layers below anything that names a version."
      echo "  Fix: install protoc {{protoc_version}} or older, or - if you are"
      echo "  deliberately moving the toolchain - update protoc_version in justfile"
      echo "  AND every protobuf-java pin listed in the comment beside it."
    } >&2
    exit 1
  fi
  echo "protoc-check: libprotoc ${actual} (pinned ceiling {{protoc_version}})"

# Run the mandatory gate set (soundness, test, current-generation,
# determinism, cert-stream) and report a trustworthy overall pass/fail.
# A naive `for g in ...; do just "$g"; done` loop whose last command per
# iteration is `echo` always exits 0 regardless of what failed inside —
# do not write that pattern again; this recipe exists so nobody has to.
#
# Runs against the HOST toolchain, so it starts with protoc-check. If your
# host protoc is newer than the pinned ceiling and no older build is
# installable, use `just gates-container` instead: it runs these same gates
# inside the CI base image, whose protoc is pinned well under the ceiling.
gates:
  #!/usr/bin/env bash
  set -uo pipefail
  # Hold the tree lock across the WHOLE suite, not once per gate: taking it per
  # gate would let another job interleave a regeneration between two of them.
  # The nested `just` calls below re-enter this same lock.
  if [[ "${TACPLUS_TREE_LOCK:-}" != "{{justfile_directory()}}/.tacplus-tree.lock" ]]; then
    exec {{lock}} just gates
  fi
  fail=0
  # Toolchain preflight: a protoc/runtime mismatch breaks three of the gates
  # below with an error that names neither protoc nor a version.
  echo "=== protoc-check ==="
  just protoc-check
  rc=$?
  echo "=== protoc-check rc=$rc ==="
  if [[ $rc -ne 0 ]]; then
    echo "=== OVERALL fail=1 (protoc preflight) ==="
    exit 1
  fi
  for g in soundness test current-generation determinism cert-stream claims-provenance; do
    echo "=== $g ==="
    just "$g"
    rc=$?
    echo "=== $g rc=$rc ==="
    [[ $rc -ne 0 ]] && fail=1
  done
  echo "=== OVERALL fail=$fail ==="
  exit $fail

# Run `just gates` inside the CI base image instead of against the host
# toolchain. Use this when the host protoc fails protoc-check and no
# acceptable protoc is installable (e.g. brew only offers a version newer
# than the pinned ceiling): the image pins protoc 28.3, well under it.
# This is the same image CI's test jobs run in
# ($CI_REGISTRY_IMAGE/tacplus-base:${BASE_VERSION}-amd64, built from
# Dockerfile.base), built locally rather than pulled from the registry.
# The first run builds the base image and takes a long time.
gates-container:
  #!/usr/bin/env bash
  set -euo pipefail
  cd {{justfile_directory()}}
  # The base image carries protoc/Go/Java/Rust/Node but not `just` (CI invokes
  # the gate scripts directly). `gates` is a just recipe that calls further
  # just recipes, so add just in a thin layer on top rather than at runtime.
  docker build -f Dockerfile.base -t tacplus-base:gates-local-base .
  docker build -t tacplus-base:gates-local - <<'DOCKERFILE'
  FROM tacplus-base:gates-local-base
  ARG JUST_VERSION=1.58.0
  RUN ARCH=$(dpkg --print-architecture) \
      && if [ "$ARCH" = "arm64" ]; then JUST_ARCH="aarch64"; else JUST_ARCH="x86_64"; fi \
      && curl -fsSL --retry 30 --retry-delay 5 \
         "https://github.com/casey/just/releases/download/${JUST_VERSION}/just-${JUST_VERSION}-${JUST_ARCH}-unknown-linux-musl.tar.gz" \
         | tar -C /usr/local/bin -xz just \
      && just --version
  DOCKERFILE
  # WORKDIR in the image is /workspace; CI's executor checks the repo out into
  # the job workspace the same way, so bind-mount this checkout there.
  docker run --rm -v "$PWD:/workspace" -w /workspace tacplus-base:gates-local just gates

# Behavioral coverage of the generated TLS/SMB cert-stream loops.
cert-stream: cert-stream-go cert-stream-java cert-stream-rust

cert-stream-go:
  {{lock}} bash cross-test/cert-stream/run.sh --language go

cert-stream-java:
  {{lock}} bash cross-test/cert-stream/run.sh --language java

cert-stream-rust:
  {{lock}} bash cross-test/cert-stream/run.sh --language rust

# Generation gate against the REAL platform + app proto repos (not the
# cross-test/proto fixtures every other gate uses). This is the check that
# would have caught A2 (2026-08-22): all other gates build tacplus's own
# fixtures and stayed green while A2 broke generation for every real
# consumer. Requires ../platform and ../testapp checked out as siblings of
# this repo (override with PLATFORM_PROTO / APP_PROTO env vars).
# NOT YET in the mandatory set — proposed, cost not yet approved (~3min
# wall-clock as of 2026-08-22).
platform-generation:
  {{lock}} bash cross-test/platform-generation/run.sh

# Wire-level gate: accounting records carry the identity the Authorizer
# VERIFIED, and carry none at all when nothing verified one. Guards F10 — the
# generated Java wrapper used to emit every record with an empty identity map,
# leaving the caller-written x-request-id as the only correlation key.
# Needs the go and java (maven) toolchains. Not part of `just test`.
accounting-identity:
  {{lock}} bash cross-test/accounting-identity/run.sh

# Wire-level gate for claims provenance in the generated Java chassis (F11):
# identity claims must come from a VERIFIED token, never from a base64 decode of
# the bearer header. Part of `gates` (needs the go and java toolchains).
claims-provenance:
  {{lock}} bash cross-test/claims-provenance/run.sh

# Behavioral coverage of W2S request (ingress) validation in the generated
# Rust REST dispatch module.
w2s-validate:
  {{lock}} bash cross-test/w2s-validate/run.sh

# Compiles the generated Rust MCP dispatch against the EXACT rmcp version
# tacplus declares (rmcpCrateVersion), so a floor lower than the emitted code
# needs fails here instead of in a consumer.
mcp-floor-version:
  {{lock}} bash cross-test/mcp-floor-version/run.sh

# Cross-language serialization-equivalence corpus: one message, encoded once by
# go into checked-in golden bytes, decoded and value-asserted by all five
# generated languages. Local/manual — not part of `test`.
wire-corpus:
  {{lock}} bash cross-test/wire-corpus/run.sh

wire-corpus-go:
  {{lock}} bash cross-test/wire-corpus/run.sh --language go

wire-corpus-java:
  {{lock}} bash cross-test/wire-corpus/run.sh --language java

wire-corpus-python:
  {{lock}} bash cross-test/wire-corpus/run.sh --language python

wire-corpus-rust:
  {{lock}} bash cross-test/wire-corpus/run.sh --language rust

wire-corpus-typescript:
  {{lock}} bash cross-test/wire-corpus/run.sh --language typescript

# Re-encode the golden bytes from the fixture's own expect blocks. Manual only.
wire-corpus-emit:
  {{lock}} bash cross-test/wire-corpus/run.sh --emit

# PC-L-9 chaos soak: generates+builds a driver binary per run, too slow for
# `just test`. Excluded from `go test ./...` via the `soak` build tag.
leak-soak:
  {{lock}} go test -tags soak ./cross-test/chaos/leaksoak/...

consumer-build:
  {{lock}} bash cross-test/consumer-contract/run.sh

consumer-build-go:
  {{lock}} bash cross-test/consumer-contract/run.sh --language go

consumer-build-java:
  {{lock}} bash cross-test/consumer-contract/run.sh --language java

consumer-build-python:
  {{lock}} bash cross-test/consumer-contract/run.sh --language python

consumer-build-typescript:
  {{lock}} bash cross-test/consumer-contract/run.sh --language typescript

consumer-build-rust:
  {{lock}} bash cross-test/consumer-contract/run.sh --language rust

consumer-build-swift:
  {{lock}} bash cross-test/consumer-contract/run.sh --language swift

consumer-smoke:
  {{lock}} bash cross-test/consumer-smoke/run.sh

consumer-smoke-go:
  {{lock}} bash cross-test/consumer-smoke/run.sh --language go

consumer-smoke-java:
  {{lock}} bash cross-test/consumer-smoke/run.sh --language java

consumer-smoke-python:
  {{lock}} bash cross-test/consumer-smoke/run.sh --language python

consumer-smoke-rust:
  {{lock}} bash cross-test/consumer-smoke/run.sh --language rust

consumer-smoke-typescript:
  {{lock}} bash cross-test/consumer-smoke/run.sh --language typescript

clean:
  {{lock}} rm -rf bin target

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
