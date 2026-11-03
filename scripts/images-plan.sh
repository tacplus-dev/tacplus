#!/usr/bin/env bash
# Decide which image layers a .github/workflows/images.yml run rebuilds.
#
# Usage:  scripts/images-plan.sh <event_name> <ref_type> <rebuild_input> [<ref_name>]
#
# Run from the repository checkout. Prints "<base> <build>" (true or false).
# A run on a release/X.Y branch is a line rebuild (#283): base and build, from
# that branch, so the line's images take every OS security fix. A release tag
# always rebuilds build, for the same reason, instead of re-tagging the line's
# older image.
# The first-publish rule (no build:latest on the registry yet) needs docker and
# stays in the workflow.
set -euo pipefail

event="$1"
ref_type="$2"
rebuild="$3"
ref_name="${4:-}"

base=false; build=false
case "$event" in
  schedule) base=true ;;
  workflow_dispatch)
    [[ "$rebuild" == base ]] && base=true
    [[ "$rebuild" != tacplus ]] && build=true ;;
  push)
    # A release tag diffs against the previous release tag on its line, not its
    # parent commit: a backport can land on the release branch before the tag is
    # cut, and the tag's own commit would not show it.
    from=HEAD~1
    if [[ "$ref_type" == tag ]]; then
      build=true
      if ! from="$(git describe --tags --abbrev=0 --match 'v*' HEAD^ 2>/dev/null)"; then
        # First release on this line: nothing to diff against, so rebuild everything.
        base=true
      fi
    fi
    changed="$(git diff --name-only "$from" HEAD 2>/dev/null || true)"
    grep -q '^images/base.Dockerfile$' <<<"$changed" && base=true
    grep -q '^images/build.Dockerfile$' <<<"$changed" && build=true ;;
esac
[[ "$ref_type" == branch && "$ref_name" =~ ^release/[0-9]+\.[0-9]+$ ]] && base=true
[[ $base == true ]] && build=true
echo "$base $build"
