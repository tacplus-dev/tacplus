#!/usr/bin/env bash
# Decide which image layers a .github/workflows/images.yml run rebuilds.
#
# Usage:  scripts/images-plan.sh <event_name> <ref_type> <rebuild_input>
#
# Run from the repository checkout. Prints "<base> <build>" (true or false).
# The first-publish rule (no build:latest on the registry yet) needs docker and
# stays in the workflow.
set -euo pipefail

event="$1"
ref_type="$2"
rebuild="$3"

base=false; build=false
case "$event" in
  schedule) base=true ;;
  workflow_dispatch)
    [[ "$rebuild" == base ]] && base=true
    build=true ;;
  push)
    # A release tag diffs against the previous release tag on its line, not its
    # parent commit: a backport can land on the release branch before the tag is
    # cut, and the tag's own commit would not show it.
    from=HEAD~1
    if [[ "$ref_type" == tag ]]; then
      # build:X.Y is rebuilt on every patch of its line, so each release has its
      # own immutable build:X.Y.Z with the line's tacplus launcher (#193).
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
[[ $base == true ]] && build=true
echo "$base $build"
