#!/usr/bin/env bash
# Decide whether .github/workflows/release.yml marks the GitHub release of a tag
# Latest (#337).
#
# Usage:  scripts/release-latest.sh <tag>
#
# Run from the repository checkout, with its tags fetched. Prints true or false,
# for `gh release create --latest=<it>`. GitHub otherwise marks whichever release
# was published last Latest, and releases are cut on several lines at once: the
# v2.4.11 backport, published after v2.6.1, took Latest from it. A release is
# Latest only when it is on the newest MAJOR.MINOR line and the newest release
# of that line, by version, not lexically; the rule images-tags.sh applies to the
# latest image tag. A pre-release (vX.Y.Z-rc.N) is never Latest and never counted.
set -euo pipefail

tag="$1"
if [[ ! "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo false
  exit 0
fi

newest="$(git tag -l 'v*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -t. -k1.2,1n -k2,2n -k3,3n | tail -1)"
if [[ "$newest" == "$tag" ]]; then echo true; else echo false; fi
