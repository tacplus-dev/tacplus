#!/usr/bin/env bash
# Check an image index .github/workflows/images.yml just pushed (#369).
#
# Usage:  scripts/images-check-index.sh <image-ref> <run-start-epoch>
#
# Fails unless the index holds a linux/amd64 and a linux/arm64 image and each
# one's config was created at or after <run-start-epoch> (the run's plan job):
# an image the registry cache handed back whole is an older run's, with that
# run's OS packages, and must not be published as this run's. v2.6.2's arm64
# image was v2.6.1's, built three days before, without the zlib1g fix.
set -euo pipefail

ref="$1"
since="$2"

image="$(docker buildx imagetools inspect "$ref" --format '{{json .Image}}')"
for platform in linux/amd64 linux/arm64; do
  created="$(jq -r --arg p "$platform" 'if type == "object" and (.[$p] | type) == "object" then .[$p].created // "" else "" end' <<<"$image")"
  if [[ -z $created ]]; then
    echo "::error::$ref has no $platform image" >&2
    exit 1
  fi
  at="$(jq -rn --arg c "$created" '$c | sub("\\.[0-9]+"; "") | fromdateiso8601')"
  if (( at < since )); then
    echo "::error::$ref: $platform was created $created, before this run started ($(date -u -d "@$since" +%Y-%m-%dT%H:%M:%SZ)); it is an older build the cache handed back" >&2
    exit 1
  fi
  echo "$ref: $platform created $created"
done
