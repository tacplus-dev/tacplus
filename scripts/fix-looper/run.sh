#!/usr/bin/env bash
# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# fix-looper: the local form of the nightly fix job.
#
# Fix requests reach tacplus only as GitHub issues filed with the Fix request
# template (label fix-request). This script takes the oldest open one, clones
# the repository fresh, hands the issue to Claude Code running under the
# user's own login (no separate API key), and expects it to reproduce the
# defect with a failing test, fix it in the generator, run the gates, push a
# branch and open a pull request that closes the issue. A request it cannot
# reproduce gets a comment and the needs-repro label instead of a guess.
#
#   run.sh                 take the oldest open fix-request (what launchd runs daily)
#   run.sh --issue N       take issue N now
#   run.sh notify [N]      ask the launchd agent to run now (optionally for issue N)
#   run.sh install         install the launchd agent (every 24h + notify)
#   run.sh uninstall
#
# State and logs live under ~/.tacplus-fix-looper. One run at a time.
set -euo pipefail

REPO="${FIX_LOOPER_REPO:-tacplus-dev/tacplus}"
HOME_DIR="${FIX_LOOPER_HOME:-$HOME/.tacplus-fix-looper}"
RUNS="$HOME_DIR/runs"
LOCK="$HOME_DIR/lock"
TRIGGER="$HOME_DIR/notify"
LABEL_ID="com.tacplus.fix-looper"
PLIST="$HOME/Library/LaunchAgents/$LABEL_ID.plist"
CLAUDE_TIMEOUT="${FIX_LOOPER_TIMEOUT:-14400}"   # 4h wall clock per issue
PROTOC28="${FIX_LOOPER_PROTOC28:-}"              # dir holding protoc 28.x bin/, for the Java gates

mkdir -p "$RUNS"
log() { printf '[%s] %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }
die() { log "FATAL $*" >&2; exit 2; }
need() { command -v "$1" >/dev/null 2>&1 || die "required tool not found: $1"; }

SELF="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"

case "${1:-}" in
  notify)
    # launchd watches this path; touching it starts a run. An issue number is
    # left in the file for that run to pick up.
    mkdir -p "$HOME_DIR"; printf '%s\n' "${2:-}" > "$TRIGGER"; log "notified (issue: ${2:-oldest})"; exit 0 ;;
  install)
    mkdir -p "$(dirname "$PLIST")" "$HOME_DIR"
    cat > "$PLIST" <<PL
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>$LABEL_ID</string>
  <key>ProgramArguments</key><array><string>/bin/bash</string><string>$SELF</string><string>--from-launchd</string></array>
  <key>StartInterval</key><integer>86400</integer>
  <key>WatchPaths</key><array><string>$TRIGGER</string></array>
  <key>EnvironmentVariables</key><dict>
    <key>PATH</key><string>${PROTOC28:+$PROTOC28/bin:}$HOME/.local/bin:$HOME/go/bin:/usr/local/go/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>
    <key>HOME</key><string>$HOME</string>
  </dict>
  <key>StandardOutPath</key><string>$HOME_DIR/launchd.out.log</string>
  <key>StandardErrorPath</key><string>$HOME_DIR/launchd.err.log</string>
</dict></plist>
PL
    launchctl bootout "gui/$(id -u)" "$PLIST" 2>/dev/null || true
    launchctl bootstrap "gui/$(id -u)" "$PLIST"
    log "installed $LABEL_ID: every 24h, and on '$SELF notify'"; exit 0 ;;
  uninstall)
    launchctl bootout "gui/$(id -u)" "$PLIST" 2>/dev/null || true; rm -f "$PLIST"; log "uninstalled"; exit 0 ;;
esac

ISSUE=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --issue) ISSUE="$2"; shift 2 ;;
    --from-launchd) shift ;;
    *) die "unknown argument: $1" ;;
  esac
done
# A notify may name an issue; consume the trigger either way.
if [[ -z "$ISSUE" && -s "$TRIGGER" ]]; then ISSUE="$(head -1 "$TRIGGER" | tr -cd '0-9')"; fi
rm -f "$TRIGGER"

need gh; need git; need claude; need jq; need just
gh auth status >/dev/null 2>&1 || die "gh is not logged in"

# One run at a time; a stale lock from a dead run is removed.
if [[ -d "$LOCK" ]]; then
  pid="$(cat "$LOCK/pid" 2>/dev/null || true)"
  if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then log "a run is already in progress (pid $pid)"; exit 0; fi
  rm -rf "$LOCK"
fi
mkdir "$LOCK"; echo $$ > "$LOCK/pid"; trap 'rm -rf "$LOCK"' EXIT

if [[ -z "$ISSUE" ]]; then
  ISSUE="$(gh issue list --repo "$REPO" --state open --label fix-request --json number,labels,createdAt --jq \
    '[.[] | select(all(.labels[].name; . != "nightly:in-progress" and . != "needs-repro" and . != "nightly:pr-open"))] | sort_by(.createdAt) | .[0].number // empty')"
fi
if [[ -z "$ISSUE" ]]; then log "no open fix-request to take"; exit 0; fi

RUN="$RUNS/$(date -u +%Y%m%dT%H%M%SZ)-issue-$ISSUE"
mkdir -p "$RUN"
exec > >(tee -a "$RUN/run.log") 2>&1
log "taking #$ISSUE in $RUN"
gh issue edit "$ISSUE" --repo "$REPO" --add-label nightly:in-progress >/dev/null

release() {
  gh issue comment "$ISSUE" --repo "$REPO" --body "Local fix looper ($(hostname -s)) ended without opening a pull request; log at $RUN/run.log on that machine." >/dev/null || true
  gh issue edit "$ISSUE" --repo "$REPO" --remove-label nightly:in-progress >/dev/null || true
}

gh repo clone "$REPO" "$RUN/tacplus" -- --quiet
cd "$RUN/tacplus"
git checkout -q -b "fix/issue-$ISSUE"

PROMPT="$RUN/prompt.md"
cat > "$PROMPT" <<PR
You are the fix looper for tacplus, a proto-to-multi-language code generator. You are in a fresh
clone of $REPO on branch fix/issue-$ISSUE. Take GitHub issue #$ISSUE (read it with \`gh issue view $ISSUE --repo $REPO\`).

Rules, in order:
1. Reproduce first. Turn the issue's reproduction into a test that fails on main: a unit test under
   pkg/, or a cross-test fixture leg under cross-test/ when the defect is in generated runtime
   behaviour. If you cannot reproduce it, do not guess: comment on the issue with what you tried
   and what you need (\`gh issue comment\`), add the label needs-repro and remove nightly:in-progress
   (\`gh issue edit\`), and stop.
2. Fix it in the generator or its templates, never by editing generated output. Keep the Rust, Go
   and Java backends at parity: a fix to one server language is applied to all three where the
   same defect exists. Parity is in capability, not in shape: do not add complexity to one language
   because another needs it. Surgical changes only; match the existing style; no hacks.
3. Verify: \`go vet ./...\`, \`just test\`, and every gate the change can affect:
   \`just current-generation-go\`, \`just determinism-go\`; \`just current-generation-java\` when Java
   changed; \`just current-generation-rust\` when Rust changed; \`just w2s-validate\` / \`just c2d-server\`
   when those planes changed. Run \`rm -rf target/cross-test\` after each gate. A green test that does
   not exercise the defect is not evidence: ask what would have failed. Never run \`git stash\` while
   a gate runs.
4. Commit with a message that says why, ending with
   \`Co-Authored-By: Claude <noreply@anthropic.com>\`, push the branch
   (\`git push -u origin fix/issue-$ISSUE\`), and open a pull request against main
   (\`gh pr create\`) whose body starts with \`Fixes #$ISSUE\`, describes the reproduction, the fix and
   the gates you ran with their last lines verbatim, and anything you could not verify.
5. Backport: for every branch named release/MAJOR.MINOR whose .0 tag is less than twelve months old
   (\`git branch -r\`, \`git tag\`), cherry-pick the fix and its test onto a branch
   fix/issue-$ISSUE-MAJOR.MINOR from that release branch, run the gates there, push, and open a pull
   request against that release branch whose body starts with \`Backport of #<main PR> for #$ISSUE\`.
   A patch release is fixes only; carry nothing else over. Then remove the nightly:in-progress label
   from the issue and add nightly:pr-open.
Never push to main or to a release branch. Never commit secrets. Stop when the pull requests are open.
PR

log "running claude (timeout ${CLAUDE_TIMEOUT}s)"
rc=0
timeout "$CLAUDE_TIMEOUT" claude --print --permission-mode acceptEdits \
  --allowedTools "Bash,Read,Edit,Write,Grep,Glob" \
  < "$PROMPT" > "$RUN/claude.log" 2>&1 || rc=$?
log "claude exited rc=$rc"
rm -rf "$RUN/tacplus/target/cross-test"

if gh pr list --repo "$REPO" --head "fix/issue-$ISSUE" --json number --jq '.[0].number' | grep -q .; then
  log "pull request open for #$ISSUE"; exit 0
fi
if gh issue view "$ISSUE" --repo "$REPO" --json labels --jq '.labels[].name' | grep -qx needs-repro; then
  log "#$ISSUE marked needs-repro"; exit 0
fi
log "no pull request and no needs-repro; releasing the issue"
release
exit 1
