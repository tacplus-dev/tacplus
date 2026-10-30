#!/usr/bin/env bash
# Run the scheduled cross-test matrix used by CI.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DRY_RUN="${TACPLUS_NIGHTLY_DRY_RUN:-false}"

print_command() {
  printf '+'
  printf ' %q' "$@"
  printf '\n'
}

run() {
  if [[ "$DRY_RUN" == "true" || "$DRY_RUN" == "1" ]]; then
    print_command "$@"
    return
  fi

  echo "==> Running $*"
  (cd "$ROOT_DIR" && "$@")
}

run_language_matrix() {
  local harness="$1"
  shift

  local language
  for language in "$@"; do
    run "$harness" --language "$language"
  done
}

# These harness defaults intentionally cover their broad client/service matrix.
run cross-test/consumer-contract/run.sh
# current-generation uses the full-service fixture, so its default is the
# service-language set; client-only positives/rejections live in consumer-contract.
run cross-test/current-generation/run.sh

# Determinism gate: same proto graph + config → byte-identical output across two
# separate generations (guards against map-iteration order leaking into codegen).
run cross-test/determinism/run.sh

# Cross-language runtime smoke: go fixture server + python/typescript
# generated clients over localhost (needs go + python + node concurrently).
run cross-test/runtime-smoke/run.sh

# These harnesses default to Go, so fan out the full WS-V1 service-language gate.
run_language_matrix cross-test/smb/run.sh go rust java
run_language_matrix cross-test/cert-stream/run.sh go rust java
run_language_matrix cross-test/memberport/run.sh go rust java
