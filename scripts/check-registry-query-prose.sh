#!/usr/bin/env bash
# check-registry-query-prose.sh — PLAN item 13 control.
#
# Asserts that a generated registry.json carries no prose describing the
# `query` TRAIT, or the record-query mechanism generally, as a vservice (or as
# S2S-only, or as argus-owned/federated). `query` is a (spec.trait) hosted by
# stratum; a registry consumer reading otherwise builds against the wrong
# contract shape.
#
# This is deliberately NOT "the string vservice never appears in registry.json".
# (spec.vservice) is a live annotation, separate from (spec.trait) and still
# used by real vservices (agentsensor, accountingrecord), so the word appears
# legitimately many times over. The control targets only the class of prose
# that conflates the two.
#
# Usage: check-registry-query-prose.sh <registry.json>
#
# Exit codes:
#   0 — clean
#   1 — offending prose found (listed on stdout)
#   2 — usage error

set -euo pipefail

REG="${1:-}"
[[ -n "$REG" ]] || { echo "usage: $0 <registry.json>" >&2; exit 2; }
[[ -f "$REG" ]] || { echo "ERROR: not a file: $REG" >&2; exit 2; }

# Candidate set: any line mentioning a vservice AND something that names the
# query trait / record-query mechanism. A "vservice" mention with no query
# connection is out of scope by construction — real vservices exist.
QUERY_MARKERS='query|trait|recordmap|argus|s2s-only|s2s only|federated'

# Exclusions. Each is legitimate prose that survives the candidate filter, and
# each is here for a stated reason — nothing that describes the query
# trait/mechanism ITSELF as a vservice belongs in this list.
EXCLUDE=(
  # PackageNode.vservices and tacplus.VServiceInfo are WIRE-SCHEMA names of the
  # registry format itself, not claims about query.
  '"schema_json": "\{\\n  \\"name\\": \\"PackageNode\\"'
  '"schema_json": "\{\\n  \\"name\\": \\"VServiceInfo\\"'
  '"fqn": "tacplus.VServiceInfo"'

  # spec.VServiceAnnotation's own type/field docs — a real, separate, still-live
  # annotation describing itself.
  '"schema_json": "\{\\n  \\"name\\": \\"VServiceAnnotation\\"'
  '"fqn": "spec.VServiceAnnotation"'

  # TraitAnnotation's cross-reference ("Traits carry the same identity shape as
  # a vservice") — an accurate structural comparison, not a claim that a trait
  # IS a vservice.
  '"description": "TraitAnnotation declares a trait identity'
  '"schema_json": "\{\\n  \\"name\\": \\"TraitAnnotation\\"'

  # Registry-tree paths mirror the on-disk proto directory layout
  # (proto/vservices/query.proto). A directory name is not prose about the
  # mechanism; moving the file is a downstream-consumer change, not this control.
  '"full_path": "vservices'

  # Embedded doc blobs — already-reconciled prose that teaches vservice and
  # trait as two distinct concepts on purpose.
  '"(AGENT|THEORY|USER|RECORD_EVENT_MODEL|NEUTRAL_ANALYSIS|CONSUMER_NEUTRAL_ANALYSIS)\.md": "'
)

candidates="$(grep -n -i 'vservice' "$REG" | grep -i -E "$QUERY_MARKERS" || true)"

hits="$candidates"
for pat in "${EXCLUDE[@]}"; do
  hits="$(printf '%s' "$hits" | grep -v -E "$pat" || true)"
done

cand_count=$(printf '%s' "$candidates" | grep -c . || true)
hit_count=$(printf '%s' "$hits" | grep -c . || true)

echo "registry: $REG"
echo "candidates (vservice + query-mechanism marker): $cand_count"
echo "hits after exclusions: $hit_count"

if [[ "$hit_count" -eq 0 ]]; then
  echo "PASS: no prose describes the query trait/mechanism as a vservice."
  exit 0
fi

echo "FAIL: prose describing the query trait/mechanism as a vservice:"
printf '%s\n' "$hits" | cut -c1-240
exit 1
