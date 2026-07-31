#!/usr/bin/env bash
#
# shellcheck disable=SC2317
#
# Run Codex and Claude as independent non-interactive reviewers/fixers until
# both vendors' Readers agree the current tree is clean, or every remaining
# disagreement has been documented by both Writers.

set -euo pipefail

usage() {
    cat <<'EOF'
Usage: scripts/multi-vendor-fix-loop.sh [options]

Options:
  --repo DIR             Repository root. Defaults to the parent of scripts/.
  --source FILE          Reader source of truth. Defaults to roadmap/README.md.
  --channel-id ID        Existing codecoord channel to use. Defaults to
                         CODECOORD_CHANNEL_ID / CODE_COORDINATION_CHANNEL_ID.
                         If omitted, the script uses an existing channel named
                         tacplus when present.
  --channel-name NAME    Name for a new channel. Default:
                         tacplus-review-loop-<run-id>. Passing this option
                         forces creation of a new channel.
  --max-cycles N         Stop after N cycles if no terminal state is reached.
                         Default: 8.
  --clean-cycles N       Consecutive stable terminal cycles required. Default: 1.
  -h, --help             Show this help.

Environment:
  CODECOORD_BIN          codecoord executable. Default: codecoord
  CODEX_BIN              Codex executable. Default: codex
  CLAUDE_BIN             Claude executable. Default: claude
  CODEX_MODEL            Optional Codex model override.
  CLAUDE_MODEL           Optional Claude model override.
  CODEX_WRITER_SANDBOX   Codex writer sandbox. Default: workspace-write
  CLAUDE_READER_MODE     Claude reader permission mode. Default: plan
  CLAUDE_WRITER_MODE     Claude writer permission mode. Default: bypassPermissions

The loop no longer writes report/state files. It posts phase reports, summaries,
and disagreement notes to codecoord.
EOF
}

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="${REPO_ROOT:-$(cd "${script_dir}/.." && pwd)}"
report_source="${REPORT_SOURCE_MD:-${repo_root}/roadmap/README.md}"
max_cycles="${MAX_CYCLES:-8}"
required_clean_cycles="${REQUIRED_CLEAN_CYCLES:-1}"
channel_id="${CODECOORD_CHANNEL_ID:-${CODE_COORDINATION_CHANNEL_ID:-}}"
channel_name="${CODECOORD_CHANNEL_NAME:-}"
channel_name_explicit=no

while [[ $# -gt 0 ]]; do
    case "$1" in
        --repo)
            repo_root="$2"
            shift 2
            ;;
        --source)
            report_source="$2"
            shift 2
            ;;
        --channel-id)
            channel_id="$2"
            shift 2
            ;;
        --channel-name)
            channel_name="$2"
            channel_name_explicit=yes
            shift 2
            ;;
        --state-dir)
            echo "warning: --state-dir is ignored; loop state is posted to codecoord" >&2
            shift 2
            ;;
        --max-cycles)
            max_cycles="$2"
            shift 2
            ;;
        --clean-cycles)
            required_clean_cycles="$2"
            shift 2
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo "error: unknown argument: $1" >&2
            usage >&2
            exit 64
            ;;
    esac
done

repo_root="$(cd "$repo_root" && pwd)"
if [[ "$report_source" != /* ]]; then
    report_source="${repo_root}/${report_source}"
fi

run_id="$(date -u +%Y%m%dT%H%M%SZ)"
if [[ -z "$channel_name" ]]; then
    channel_name="tacplus-review-loop-${run_id}"
fi

codecoord_bin="${CODECOORD_BIN:-codecoord}"
codex_bin="${CODEX_BIN:-codex}"
claude_bin="${CLAUDE_BIN:-claude}"
codex_writer_sandbox="${CODEX_WRITER_SANDBOX:-workspace-write}"
claude_reader_mode="${CLAUDE_READER_MODE:-plan}"
claude_writer_mode="${CLAUDE_WRITER_MODE:-bypassPermissions}"

require_int() {
    local name="$1" value="$2"
    if [[ ! "$value" =~ ^[1-9][0-9]*$ ]]; then
        echo "error: ${name} must be a positive integer, got: ${value}" >&2
        exit 64
    fi
}

require_int "--max-cycles" "$max_cycles"
require_int "--clean-cycles" "$required_clean_cycles"

if ! command -v "$codecoord_bin" >/dev/null 2>&1; then
    echo "error: codecoord executable not found: ${codecoord_bin}" >&2
    exit 127
fi
if ! command -v "$codex_bin" >/dev/null 2>&1; then
    echo "error: Codex executable not found: ${codex_bin}" >&2
    exit 127
fi
if ! command -v "$claude_bin" >/dev/null 2>&1; then
    echo "error: Claude executable not found: ${claude_bin}" >&2
    exit 127
fi
if ! git -C "$repo_root" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    echo "error: not a git repository: ${repo_root}" >&2
    exit 64
fi
if [[ ! -f "$report_source" ]]; then
    echo "error: report source markdown not found: ${report_source}" >&2
    exit 66
fi

"$codecoord_bin" daemon start >/dev/null 2>&1 || true

loop_agent_seed="${CODECOORD_LOOP_AGENT_SEED:-${repo_root}|multi-vendor-fix-loop}"
loop_agent_id="${CODECOORD_LOOP_AGENT_ID:-$("$codecoord_bin" derive-id --seed "$loop_agent_seed")}"
loop_agent_name="${CODECOORD_LOOP_AGENT_NAME:-TacplusReviewLoop}"

"$codecoord_bin" register \
    --agent-id "$loop_agent_id" \
    --provider other \
    --model multi-vendor-fix-loop \
    --name "$loop_agent_name" \
    --plan "Orchestrating Codex/Claude reader-writer loop for ${repo_root}; reports are posted to codecoord." \
    >/dev/null

find_channel_by_name() {
    local wanted="$1"
    "$codecoord_bin" channels | awk -v wanted="$wanted" '
        /^[[:space:]]*{/ {
            id = ""
            name = ""
            archived = 0
        }
        /"channelId":/ {
            id = $0
            sub(/.*"channelId": "/, "", id)
            sub(/".*/, "", id)
        }
        /"name":/ {
            name = $0
            sub(/.*"name": "/, "", name)
            sub(/".*/, "", name)
        }
        /"archived": true/ {
            archived = 1
        }
        /^[[:space:]]*}/ {
            if (name == wanted && archived == 0 && id != "") {
                print id
                exit
            }
        }
    '
}

if [[ -z "$channel_id" && "$channel_name_explicit" == no ]]; then
    channel_id="$(find_channel_by_name tacplus || true)"
fi

if [[ -z "$channel_id" ]]; then
    channel_json="$("$codecoord_bin" create-channel --agent-id "$loop_agent_id" --name "$channel_name")"
    channel_id="$(printf '%s\n' "$channel_json" | sed -n 's/.*"channelId": "\(ch_[^"]*\)".*/\1/p' | head -n1)"
    if [[ -z "$channel_id" ]]; then
        echo "error: could not parse channel id from codecoord create-channel output" >&2
        printf '%s\n' "$channel_json" >&2
        exit 1
    fi
else
    "$codecoord_bin" join-channel --channel-id "$channel_id" --agent-id "$loop_agent_id" >/dev/null 2>&1 || true
fi

send_codecoord() {
    local body="$1"
    local max_chars=12000
    local total="${#body}"
    local offset=0
    local chunk chunk_no=1 chunk_count

    if (( total <= max_chars )); then
        "$codecoord_bin" send --agent-id "$loop_agent_id" --channel-id "$channel_id" -- "$body" >/dev/null
        return
    fi

    chunk_count=$(( (total + max_chars - 1) / max_chars ))
    while (( offset < total )); do
        chunk="${body:offset:max_chars}"
        chunk_body="[chunk ${chunk_no}/${chunk_count}]
${chunk}"
        "$codecoord_bin" send --agent-id "$loop_agent_id" --channel-id "$channel_id" -- \
            "$chunk_body" >/dev/null
        offset=$((offset + max_chars))
        chunk_no=$((chunk_no + 1))
    done
}

post_report() {
    local title="$1" body="$2"
    send_codecoord "## ${title}

${body}"
}

read_marker_from_text() {
    local key="$1" text="$2"
    printf '%s\n' "$text" | grep -oE "^${key}=[A-Za-z_]+" | tail -n1 | cut -d= -f2
}

require_marker() {
    local key="$1" label="$2" text="$3" value
    value="$(read_marker_from_text "$key" "$text" || true)"
    if [[ -z "$value" ]]; then
        post_report "${label} marker failure" "The phase output is missing required final marker \`${key}=...\`.

Last 4000 characters of output:

\`\`\`
${text: -4000}
\`\`\`"
        echo "error: ${label} is missing required final marker ${key}=..." >&2
        echo "       See codecoord channel ${channel_id}" >&2
        exit 65
    fi
    echo "$value"
}

worktree_fingerprint() (
    cd "$repo_root"
    {
        git diff --binary --no-ext-diff HEAD --
        git ls-files --others --exclude-standard -z |
            while IFS= read -r -d '' path; do
                printf 'UNTRACKED\0%s\0' "$path"
                if [[ -f "$path" ]]; then
                    shasum -a 256 "$path"
                else
                    printf 'not-regular-file\n'
                fi
            done
    } | shasum -a 256 | awk '{print $1}'
)

tree_summary() {
    local max_status=40
    local max_diffstat=40
    local status diffstat status_lines diffstat_lines

    (
        cd "$repo_root"
        status="$(git status --short --branch)"
        diffstat="$(git diff --stat HEAD -- || true)"
        status_lines="$(printf '%s\n' "$status" | wc -l | tr -d ' ')"
        diffstat_lines="$(printf '%s\n' "$diffstat" | wc -l | tr -d ' ')"

        echo "Status lines: ${status_lines}"
        echo "Diffstat lines: ${diffstat_lines}"
        echo
        echo "Status preview:"
        echo "\`\`\`"
        printf '%s\n' "$status" | sed -n "1,${max_status}p"
        if (( status_lines > max_status )); then
            echo "... (${status_lines} total status lines; truncated for codecoord)"
        fi
        echo "\`\`\`"
        echo
        echo "Diffstat preview:"
        echo "\`\`\`"
        printf '%s\n' "$diffstat" | sed -n "1,${max_diffstat}p"
        if (( diffstat_lines > max_diffstat )); then
            echo "... (${diffstat_lines} total diffstat lines; truncated for codecoord)"
        fi
        echo "\`\`\`"
    )
}

build_reader_prompt() {
    local vendor="$1"
    cat <<EOF
ROLE: ${vendor} Reader

You are one of two independent Reader agents in a local hardening loop for tacplus.
Your job is to audit, not edit. Do not modify files.

Source of truth:
- ${report_source}

Rules:
1. Read ${report_source} first and treat it as the audit contract.
2. Follow repository guidance such as AGENTS.md and relevant source-controlled docs.
3. Derive findings from current source code and generated-code semantics.
4. Do not read or rely on prior review artifacts, codecoord loop messages, or
   loop reports. The Writers can read those; Readers stay independent and
   grounded in ${report_source}.
5. Promote only verified, actionable issues. Include discarded hypotheses.
6. CLEAN means no confirmed actionable issue remains under the review plan in
   the current tree. Open questions without an actionable code change are not
   enough to mark ISSUES.

Return a complete markdown report as your final answer with these sections:
- Summary
- Strengths
- Confirmed findings
- Discarded hypotheses
- Open questions / scope gaps

As the very last line of your final answer, write exactly one marker:
READER_STATUS=CLEAN
or
READER_STATUS=ISSUES
EOF
}

build_writer_prompt() {
    local vendor="$1" cycle="$2"
    cat <<EOF
ROLE: ${vendor} Writer

You are one of two independent Writer agents in a local hardening loop for tacplus.
Your job is to fix verified issues, add focused regression coverage, document
disagreements, and leave an auditable final note. You may read the other
vendor's reports.

Repository:
- ${repo_root}

Source of truth:
- ${report_source}

Codecoord transcript:
- Channel id: ${channel_id}
- Current cycle: ${cycle}
- Read prior loop messages with:
  codecoord history --channel-id ${channel_id} --max 200

Instructions:
1. Read ${report_source}.
2. Read the Reader and Writer reports in the codecoord transcript. Writers may
   use each other's reports as handoff context.
3. Fix concrete, verified, automatable issues. Prefer source templates,
   generators, and regression tests over generated-output churn unless the
   generated output is the true source being maintained.
4. Preserve existing user changes. Do not revert unrelated work.
5. Run focused tests when feasible and report what you ran.
6. If the Readers found no actionable issues and the transcript does not
   identify work you can safely do, make no code changes.
7. If the vendors disagree, write a "Disagreements" section. A disagreement
   includes one Reader saying CLEAN while the other says ISSUES, conflicting
   findings, conflicting severities, or conflicting fix/blocker conclusions.
   Make the ${vendor} case from source evidence, cite concrete files/lines when
   possible, and say what evidence would resolve the dispute.

Return a markdown status note as your final answer with:
- What changed
- Tests run
- Remaining actionable work
- Disagreements
- Blockers or human decisions needed

The last two lines of your final answer must be exactly:
WRITER_STATUS=<value>
DISAGREEMENT_STATUS=<value>

Choose exactly one WRITER_STATUS value:
- CHANGED if you changed repository files.
- NO_CHANGES if you intentionally left the repository unchanged.
- BLOCKED if you could not make required automated progress.

Choose exactly one DISAGREEMENT_STATUS value:
- NONE if there are no vendor disagreements in the reports you can see.
- DOCUMENTED if you documented every vendor disagreement you can see and made your case.
- NEEDS_OTHER_VENDOR if a new disagreement exists but the other vendor has not
  yet had a later Writer pass to make its case.
EOF
}

run_codex_reader() {
    local prompt cmd
    prompt="$(build_reader_prompt "Codex")"
    cmd=("$codex_bin" --ask-for-approval never)
    if [[ -n "${CODEX_MODEL:-}" ]]; then
        cmd+=(--model "$CODEX_MODEL")
    fi
    cmd+=(exec -C "$repo_root" --ephemeral --color never)
    cmd+=(--sandbox read-only "$prompt")
    "${cmd[@]}"
}

run_codex_writer() {
    local cycle="$1" prompt cmd
    prompt="$(build_writer_prompt "Codex" "$cycle")"
    cmd=("$codex_bin" --ask-for-approval never)
    if [[ -n "${CODEX_MODEL:-}" ]]; then
        cmd+=(--model "$CODEX_MODEL")
    fi
    cmd+=(exec -C "$repo_root" --ephemeral --color never)
    cmd+=(--sandbox "$codex_writer_sandbox" "$prompt")
    "${cmd[@]}"
}

run_claude_reader() {
    local prompt cmd
    prompt="$(build_reader_prompt "Claude")"
    cmd=("$claude_bin" -p --output-format text --no-session-persistence)
    if [[ -n "${CLAUDE_MODEL:-}" ]]; then
        cmd+=(--model "$CLAUDE_MODEL")
    fi
    cmd+=(--permission-mode "$claude_reader_mode" "$prompt")
    (cd "$repo_root" && "${cmd[@]}")
}

run_claude_writer() {
    local cycle="$1" prompt cmd
    prompt="$(build_writer_prompt "Claude" "$cycle")"
    cmd=("$claude_bin" -p --output-format text --no-session-persistence)
    if [[ -n "${CLAUDE_MODEL:-}" ]]; then
        cmd+=(--model "$CLAUDE_MODEL")
    fi
    cmd+=(--permission-mode "$claude_writer_mode" "$prompt")
    (cd "$repo_root" && "${cmd[@]}")
}

run_step() {
    local label="$1"
    shift
    echo "[$(date -u +%H:%M:%S)] ${label}"
    if ! RUN_OUTPUT="$("$@" 2>&1)"; then
        post_report "${label} failed" "\`\`\`
${RUN_OUTPUT}
\`\`\`"
        echo "error: ${label} failed; see codecoord channel ${channel_id}" >&2
        exit 1
    fi
    post_report "${label}" "$RUN_OUTPUT"
}

send_codecoord "# tacplus multi-vendor review loop

- run_id: ${run_id}
- repo: ${repo_root}
- source: ${report_source}
- max_cycles: ${max_cycles}
- required_clean_cycles: ${required_clean_cycles}
- loop_agent_id: ${loop_agent_id}

Reports, status summaries, and disagreement notes for this run will be posted in this channel."

echo "run_id=${run_id}"
echo "repo=${repo_root}"
echo "source=${report_source}"
echo "codecoord_channel=${channel_id}"
echo "history_command=codecoord history --channel-id ${channel_id} --max 200"
echo

clean_cycles=0

for ((cycle = 1; cycle <= max_cycles; cycle++)); do
    cycle_label="$(printf 'cycle-%03d' "$cycle")"

    echo "==== ${cycle_label} ===="
    before_fp="$(worktree_fingerprint)"
    before_summary="$(tree_summary)"
    post_report "${cycle_label} before tree" "$before_summary"

    run_step "${cycle_label} Codex Reader" run_codex_reader
    codex_reader_output="$RUN_OUTPUT"
    codex_reader_status="$(require_marker READER_STATUS "${cycle_label} Codex Reader" "$codex_reader_output")"

    run_step "${cycle_label} Codex Writer" run_codex_writer "$cycle"
    codex_writer_output="$RUN_OUTPUT"
    codex_writer_status="$(require_marker WRITER_STATUS "${cycle_label} Codex Writer" "$codex_writer_output")"
    codex_disagreement_status="$(require_marker DISAGREEMENT_STATUS "${cycle_label} Codex Writer" "$codex_writer_output")"

    run_step "${cycle_label} Claude Reader" run_claude_reader
    claude_reader_output="$RUN_OUTPUT"
    claude_reader_status="$(require_marker READER_STATUS "${cycle_label} Claude Reader" "$claude_reader_output")"

    run_step "${cycle_label} Claude Writer" run_claude_writer "$cycle"
    claude_writer_output="$RUN_OUTPUT"
    claude_writer_status="$(require_marker WRITER_STATUS "${cycle_label} Claude Writer" "$claude_writer_output")"
    claude_disagreement_status="$(require_marker DISAGREEMENT_STATUS "${cycle_label} Claude Writer" "$claude_writer_output")"

    after_fp="$(worktree_fingerprint)"
    after_summary="$(tree_summary)"

    stable="$([[ "$before_fp" == "$after_fp" ]] && echo yes || echo no)"
    reader_status_disagreement=no
    if [[ "$codex_reader_status" != "$claude_reader_status" ]]; then
        reader_status_disagreement=yes
    fi

    disagreement_detected=no
    if [[ "$reader_status_disagreement" == yes ||
          "$codex_disagreement_status" != "NONE" ||
          "$claude_disagreement_status" != "NONE" ]]; then
        disagreement_detected=yes
    fi

    if [[ "$disagreement_detected" == yes ]]; then
        post_report "${cycle_label} disagreement summary" "- Codex Reader: ${codex_reader_status}
- Codex Writer: ${codex_writer_status}, disagreement=${codex_disagreement_status}
- Claude Reader: ${claude_reader_status}
- Claude Writer: ${claude_writer_status}, disagreement=${claude_disagreement_status}
- Stable working tree this cycle: ${stable}

The vendor-written cases live in the Writer reports above in this codecoord channel."
    fi

    status_line="${cycle_label}	codex_reader=${codex_reader_status}	codex_writer=${codex_writer_status}	codex_disagreement=${codex_disagreement_status}	claude_reader=${claude_reader_status}	claude_writer=${claude_writer_status}	claude_disagreement=${claude_disagreement_status}	stable=${stable}"
    echo "$status_line"
    post_report "${cycle_label} after tree and status" "${after_summary}

\`\`\`
${status_line}
\`\`\`"

    if [[ "$codex_reader_status" == "CLEAN" &&
          "$claude_reader_status" == "CLEAN" &&
          "$stable" == yes ]]; then
        clean_cycles=$((clean_cycles + 1))
        echo "[${cycle_label}] clean consensus (${clean_cycles}/${required_clean_cycles})"
    elif [[ "$stable" == yes &&
            "$cycle" -gt 1 &&
            "$disagreement_detected" == yes &&
            "$codex_disagreement_status" == "DOCUMENTED" &&
            "$claude_disagreement_status" == "DOCUMENTED" &&
            "$codex_writer_status" != "CHANGED" &&
            "$claude_writer_status" != "CHANGED" ]]; then
        clean_cycles=$((clean_cycles + 1))
        echo "[${cycle_label}] disagreements documented (${clean_cycles}/${required_clean_cycles})"
    else
        clean_cycles=0
        echo "[${cycle_label}] continuing"
    fi

    if (( clean_cycles >= required_clean_cycles )); then
        if [[ "$codex_reader_status" == "CLEAN" && "$claude_reader_status" == "CLEAN" ]]; then
            terminal="clean consensus reached"
        else
            terminal="all visible disagreements documented"
        fi
        send_codecoord "## ${terminal}

Final status:

\`\`\`
${status_line}
\`\`\`"
        echo "==== ${terminal} ===="
        echo "codecoord_channel=${channel_id}"
        exit 0
    fi

    echo
done

send_codecoord "## stopped after ${max_cycles} cycles without clean consensus

Last status:

\`\`\`
${status_line:-none}
\`\`\`"
echo "==== stopped after ${max_cycles} cycles without clean consensus ====" >&2
echo "codecoord_channel=${channel_id}" >&2
exit 2
