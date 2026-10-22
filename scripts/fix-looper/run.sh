#!/usr/bin/env bash
# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# fix-looper: the local form of the nightly fix job.
#
# Requests reach tacplus only as GitHub issues (Fix request or Feature
# request templates). This script takes the oldest open one, clones the
# repository fresh, and hands it to Claude Code running under the user's own
# login (no separate API key). Claude triages it first: a bug is reproduced
# with a failing test, fixed in the generator, gated, opened as a pull
# request against main and backported to every supported release branch; a
# feature gets a written review with a recommendation and the needs-decision
# label, and is only built once Jody labels it feature:approved (main only,
# never backported). A bug it cannot reproduce gets needs-repro.
#
#   run.sh                 take the oldest open fix-request (what launchd runs daily)
#   run.sh --issue N       take issue N now
#   run.sh notify [N]      ask the launchd agent to run now (optionally for issue N)
#   run.sh install         install the launchd agent (every 24h + notify)
#   run.sh uninstall
#
# State and logs live under ~/.tacplus-fix-looper. One run at a time.
# Git pushes authenticate through gh (`gh auth setup-git`; its token carries the workflow scope that a fix
# touching .github/workflows needs). No token is written into a clone, and no ~/.netrc github.com entry may
# exist: libcurl reads it before any credential helper.
set -euo pipefail

# Run from a private copy: bash reads a script as it executes, so a git pull that rewrites this file during a
# multi-hour run corrupts the running process (10-20: "syntax error near unexpected token" after #70's run).
if [ -z "${FIX_LOOPER_RUNNING_COPY:-}" ]; then
  copy="$(mktemp -t fix-looper-run.XXXXXX)"
  cp "${BASH_SOURCE[0]}" "$copy"
  FIX_LOOPER_RUNNING_COPY="$copy" FIX_LOOPER_SELF="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")" exec bash "$copy" "$@"
fi

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

SELF="${FIX_LOOPER_SELF}"

case "${1:-}" in
  notify)
    # launchd watches this path; touching it starts a run. An issue number is
    # left in the file for that run to pick up.
    mkdir -p "$HOME_DIR"; printf '%s\n' "${2:-}" > "$TRIGGER"; log "notified (issue: ${2:-oldest})"; exit 0 ;;
  install)
    # Java gates need a JDK; Homebrew's openjdk is keg-only (not in /opt/homebrew/bin), so find its bin dir.
    JDK_BIN=""; for d in /opt/homebrew/opt/openjdk/bin /usr/local/opt/openjdk/bin; do [ -x "$d/java" ] && { JDK_BIN="$d"; break; }; done
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
    <key>PATH</key><string>${PROTOC28:+$PROTOC28/bin:}$HOME/.local/bin:$HOME/.cargo/bin:$HOME/go/bin:/usr/local/go/bin:${JDK_BIN:+$JDK_BIN:}/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>
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
mkdir "$LOCK"; echo $$ > "$LOCK/pid"; trap 'rm -rf "$LOCK" "$FIX_LOOPER_RUNNING_COPY"' EXIT

if [[ -z "$ISSUE" ]]; then
  # Oldest open request of either kind that is waiting on the looper: a fix
  # request, a feature request not yet reviewed, or a feature Jody approved.
  ISSUE="$(gh issue list --repo "$REPO" --state open --search 'label:fix-request,feature-request,feature:approved' \
    --json number,labels,createdAt --jq \
    '[.[] | select(all(.labels[].name; . != "nightly:in-progress" and . != "needs-repro" and . != "nightly:pr-open" and . != "needs-decision"))] | sort_by(.createdAt) | .[0].number // empty')"
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
# Resume a previous run's pushed branch instead of starting over (a run can push and then stop).
if git ls-remote --exit-code --heads origin "fix/issue-$ISSUE" >/dev/null 2>&1; then
  git fetch -q origin "fix/issue-$ISSUE" && git checkout -q -b "fix/issue-$ISSUE" FETCH_HEAD
  log "resuming the pushed branch fix/issue-$ISSUE"
else
  git checkout -q -b "fix/issue-$ISSUE"
fi

PROMPT="$RUN/prompt.md"
cat > "$PROMPT" <<PR
You are the fix looper for tacplus, a proto-to-multi-language code generator. You are in a fresh
clone of $REPO on branch fix/issue-$ISSUE. Take GitHub issue #$ISSUE (read it with \`gh issue view $ISSUE --repo $REPO\`).

Rules, in order:
Step 0, triage. Decide what the issue is before touching code, whatever label the reporter chose:
- BUG: tacplus, its generated code, or a gate does something other than what it documents or plainly
  intends (wrong output, a crash, a compile failure in generated code, a gate that passes while the
  behaviour is wrong). A bug changes no generated surface, annotation, CLI flag or option.
- FEATURE: anything that adds or changes a generated surface, an annotation, a CLI command or flag,
  an option, a supported language or platform, including closing a parity gap by adding a surface to
  one language. Features ship only on a minor, never in a patch (docs/VERSIONING.md).
If the label is wrong, fix it: a bug gets fix-request, a feature gets feature-request.

If it is a FEATURE and the issue is not labeled feature:approved, do NOT change code. Post one comment
on the issue headed "Feature review (fix looper)" with these sections, each a few sentences, grounded
in the repository (cite files, docs/API.md, docs/USER.md, roadmap/LANGUAGE_PARITY.md,
docs/KNOWN_TEST_GAPS.md, docs/VERSIONING.md):
  1. What is asked, in one paragraph.
  2. Does it address a gap: what tacplus does today, where it falls short, which languages, and
     whether any doc already promises it.
  3. Should tacplus do it: is it proto-to-code generation, generated AAA or transport, or a chassis
     concern (inside tacplus's scope), or does it belong in a consumer, the platform, or a
     separate tool. Note any conflict with tacplus's rules: generated code is never hand-edited, AAA
     is generated, reachability is not authorization, parity is in capability not in shape.
  4. Cost: languages and generated surfaces touched, gates and fixtures needed, whether it is a
     breaking change (a major) or additive (a minor).
  5. Alternatives, including doing nothing.
  6. Recommendation: one of ACCEPT for the next minor, ACCEPT later, DECLINE, or NEEDS INFORMATION,
     with the reason in two or three sentences.
Then add the label needs-decision, remove nightly:in-progress, and stop. Jody decides: he adds
feature:approved to accept or closes the issue to decline.

If it is a FEATURE labeled feature:approved, implement it on main only, as below, but never backport it.
An issue labeled feature:approved is never backported, even if you triage it as a BUG: it builds on a feature
that exists only on main (a parity follow-up is the common case), so a release branch does not have its base.
If it is a BUG, fix it as below and backport it.

1. Reproduce first (bugs). Turn the issue's reproduction into a test that fails on main: a unit test under
   pkg/, or a cross-test fixture leg under cross-test/ when the defect is in generated runtime
   behaviour. Building that test is part of the job, including a new fixture leg or a fault-injection
   test (added latency, a restarted or slowed peer, a long-running connection) when the issue needs one;
   you have hours, so "the reproduction is larger than one pass" is not a reason to stop. Only after a
   genuine attempt — the test is written and run and does not fail on main, or it cannot be built for a
   stated reason — comment on the issue with what you built, its output and what you need
   (\`gh issue comment\`), add the label needs-repro and remove nightly:in-progress (\`gh issue edit\`),
   and stop. Never mark needs-repro from reading code alone. For an approved feature, write the tests
   that define it first.
2. Fix it in the generator or its templates, never by editing generated output. Keep the Rust, Go
   and Java backends at parity: a fix to one server language is applied to all three where the
   same defect exists. Parity is in capability, not in shape: do not add complexity to one language
   because another needs it. Surgical changes only; match the existing style; no hacks.
3. Verify: \`go vet ./...\`, \`just test\`, and every gate the change can affect:
   \`just current-generation-go\`, \`just determinism-go\`; \`just current-generation-java\` when Java
   changed; \`just current-generation-rust\` when Rust changed; \`just w2s-validate\` / \`just c2d-server\`
   when those planes changed. Run \`rm -rf target/cross-test\` after each gate. Several gates run longer than
   your shell tool's 10-minute limit: start each one detached (\`nohup just <gate> > /tmp/<gate>.log 2>&1 &\`)
   and poll its log with short foreground checks until it prints its final line; never rely on one long wait.
   You run non-interactively: ending your reply ends the run. Never end a turn while a gate is still running
   or before the pull request is open — keep polling with tool calls until the gate exits.
   If the branch already exists on origin you are resuming a previous run: read its commits and finish it. A green test that does
   not exercise the defect is not evidence: ask what would have failed. Never run \`git stash\` while
   a gate runs.
4. Commit with a message that says why, ending with
   \`Co-Authored-By: Claude <noreply@anthropic.com>\`, push the branch
   (\`git push -u origin fix/issue-$ISSUE\`), and open a pull request against main
   (\`gh pr create\`) whose body starts with \`Fixes #$ISSUE\`, says BUG or FEATURE, describes the
   reproduction, the fix and the gates you ran with their last lines verbatim, and anything you
   could not verify.
5. Backport (bugs only, never features): for every branch named release/MAJOR.MINOR whose .0 tag is
   less than twelve months old (\`git branch -r\`, \`git tag\`), cherry-pick the fix and its test onto a
   branch fix/issue-$ISSUE-MAJOR.MINOR from that release branch, run the gates there, push, and open
   a pull request against that release branch whose body starts with
   \`Backport of #<main PR> for #$ISSUE\`. A patch release is fixes only; carry nothing else over.
   Then remove the nightly:in-progress label from the issue and add nightly:pr-open.
Never push to main or to a release branch. Never commit secrets. Stop when the pull requests (or the
feature review) are posted.
PR

log "running claude (timeout ${CLAUDE_TIMEOUT}s)"
rc=0
timeout "$CLAUDE_TIMEOUT" claude --print --permission-mode acceptEdits \
  --allowedTools "Bash,Read,Edit,Write,Grep,Glob" \
  < "$PROMPT" > "$RUN/claude.log" 2>&1 || rc=$?
log "claude exited rc=$rc"
# Build output is the disk cost (one run's cross-test peaked near 40 GB, 10-22); the logs are what a run keeps.
rm -rf "$RUN/tacplus/target"
# Older runs keep their logs, not their clones.
find "$RUNS" -mindepth 2 -maxdepth 2 -name tacplus -type d -mtime +7 -prune -exec rm -rf {} + 2>/dev/null || true

if gh pr list --repo "$REPO" --state all --head "fix/issue-$ISSUE" --json number --jq '.[0].number' | grep -q .; then
  log "pull request open for #$ISSUE"; exit 0
fi
labels="$(gh issue view "$ISSUE" --repo "$REPO" --json labels --jq '.labels[].name')"
if grep -qx needs-repro <<<"$labels"; then log "#$ISSUE marked needs-repro"; exit 0; fi
if grep -qx needs-decision <<<"$labels"; then log "#$ISSUE is a feature; review posted, waiting on Jody"; exit 0; fi
log "no pull request and no needs-repro; releasing the issue"
release
exit 1
