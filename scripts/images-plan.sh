#!/usr/bin/env bash
# Decide which image layers a .github/workflows/images.yml run rebuilds.
#
# Usage:  scripts/images-plan.sh <event_name> <ref_type> <rebuild_input> [<ref_name> [<before>]]
#
# <before> is a push's previous head (github.event.before): a push to a branch
# diffs against it, so an image change in any of the commits it pushes counts,
# not only in its last one (#315: the manager pushes a batch, STATE.md last).
# Run from the repository checkout. Prints "<base> <build>" (true or false).
# A run on a release/X.Y branch is a line rebuild (#283): base and build, from
# that branch, so the line's images take every OS security fix.
# The first-publish rule (no build:latest on the registry yet) needs docker and
# stays in the workflow.
set -euo pipefail

event="$1"
ref_type="$2"
rebuild="$3"
ref_name="${4:-}"
before="${5:-}"

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
    # A new branch's push has no previous head (all zeros), and a force push's
    # may not be in the checkout: the pushed commit's parent stands in.
    if [[ "$ref_type" == branch && -n "$before" && ! "$before" =~ ^0+$ ]] &&
      git cat-file -e "$before^{commit}" 2>/dev/null; then
      from="$before"
    fi
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
    # An image's declared packages and their check are part of it (#554).
    grep -qE '^images/(base\.Dockerfile|base\.packages|packages-check\.sh)$' <<<"$changed" && base=true
    grep -qE '^images/(build\.Dockerfile|build\.packages)$' <<<"$changed" && build=true ;;
esac
[[ "$ref_type" == branch && "$ref_name" =~ ^release/[0-9]+\.[0-9]+$ ]] && base=true
[[ $base == true ]] && build=true
echo "$base $build"
