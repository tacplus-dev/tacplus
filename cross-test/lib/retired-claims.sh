#!/usr/bin/env bash
# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# The retired `linked` claim (#171): no generated code reads it, in any
# language. Source this file; it defines functions only.
#
# linked_claim_readers OUTPUT prints every line of generated Go, Java or Rust
# under OUTPUT that reads the claim, as PATH:LINE:TEXT with PATH relative to
# OUTPUT, and nothing when there is none.
#
# Two things are not readers, and are excluded by what they are rather than by
# where the gate happens to run (filtering every path containing /target/ made
# the check vacuous under the gate's own work root, tacplus#351):
#   - build output inside OUTPUT (a cargo or Maven target/ directory);
#   - string literals. protoc-gen-go and protoc-gen-java embed the raw
#     descriptor as string literals, and it carries the reserved enum name of
#     pkg/proto/sys/token.proto (reserved "JWT_PAYLOAD_TYPE_LINKED"). A line is
#     matched with its "..." literals removed.

LINKED_CLAIM_READER_RE='GetLinked\(|SetLinked\(|LinkedFromContext|getLinked\(|setLinked\(|\.linked\(\)|set_linked\(|CLAIM_LINKED|JWT_PAYLOAD_TYPE_LINKED|_linked\b'

linked_claim_readers() {
  local output="$1"
  [[ -d "$output" ]] || { echo "linked_claim_readers: no such directory: $output" >&2; return 1; }
  local hit path_line text code
  (cd "$output" && grep -rnE --include='*.go' --include='*.java' --include='*.rs' --exclude-dir=target \
    "$LINKED_CLAIM_READER_RE" . 2>/dev/null || true) \
  | while IFS= read -r hit; do
      path_line="${hit%%:*}"
      text="${hit#*:}"
      path_line="$path_line:${text%%:*}"
      text="${text#*:}"
      code="$(sed -E 's/"([^"\\]|\\.)*"//g' <<<"$text")"
      if grep -qE "$LINKED_CLAIM_READER_RE" <<<"$code"; then
        printf '%s:%s\n' "${path_line#./}" "$text"
      fi
    done
}
