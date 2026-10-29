#!/usr/bin/env bash
# Decide which tags a .github/workflows/images.yml run for a release tag puts
# on the build image of its line (there is no tacplus image since #193).
#
# Usage:  scripts/images-tags.sh <ref_type> <ref_name> <sha>
#
# Run from the repository checkout, with its tags fetched. Prints the tags,
# space-separated. A branch push gets main-<sha7> and main. A release tag vX.Y.Z
# gets X.Y.Z, and each floating tag of docs/VERSIONING.md only when it is the
# highest release that tag covers: X.Y the newest X.Y.x, X the newest X.x,
# latest the newest of all. Releases are cut on several lines in parallel, so a
# 2.0.x patch cut after 2.3.x moves 2.0 and nothing else (#160). A pre-release
# (vX.Y.Z-rc.N) gets only its own tag and is never counted as a release.
set -euo pipefail

ref_type="$1"
ref_name="$2"
sha="$3"

if [[ "$ref_type" != tag ]]; then
  echo "main-${sha::7} main"
  exit 0
fi

v="${ref_name#v}"
if [[ ! "$v" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
  echo "$v"
  exit 0
fi
ma="${BASH_REMATCH[1]}"; mi="${BASH_REMATCH[2]}"

# The highest release among the tags matching a glob, by version, not lexically.
highest() {
  git tag -l "$1" | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -t. -k1.2,1n -k2,2n -k3,3n | tail -1
}

tags="$v"
[[ "$(highest "v$ma.$mi.*")" == "v$v" ]] && tags+=" $ma.$mi"
[[ "$(highest "v$ma.*")" == "v$v" ]] && tags+=" $ma"
[[ "$(highest 'v*')" == "v$v" ]] && tags+=" latest"
echo "$tags"
