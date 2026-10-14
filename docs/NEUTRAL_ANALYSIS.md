# Neutral Analysis Entry Point

This document is the entry point for neutral tacplus analysis. It is meant to
guide reviewers, agents, and maintainers toward evidence-backed findings while
filtering out personal preference, language preference, and implementation
religion.

For downstream tacplus consumers, the common neutral-analysis contract is
`CONSUMER_NEUTRAL_ANALYSIS.md`. Consumer repositories might not have that file
on disk, so reviewers must read the canonical repository copy:

https://github.com/tacplus-dev/tacplus/blob/main/docs/CONSUMER_NEUTRAL_ANALYSIS.md

When reviewing tacplus itself, read that same downstream-consumer contract and
verify that tacplus actually gives consumers generated surfaces that let them
comply with it. If a consumer is forced to violate the downstream rule because
tacplus has not generated the needed model surface, report the consumer code as
a forced deviation and report the missing generated surface as a tacplus
self-compliance finding.

## Goal

Review tacplus as a generator and as a producer of generated production code.
The analysis should identify concrete engineering risks: bugs, parity gaps,
deadlocks, races, gray failures, fail-open paths, swallowed failures, retry and
timeout gaps, missing crash-on-irrecoverable behavior, and security
vulnerabilities.

The desired output is a list of confirmed or carefully qualified findings with
clear reproduction paths and recommended fixes.

## Ground Rules

- Do not treat any supported language as the reference implementation.
- Do not evaluate whether tacplus should have been written in another language.
- Do not include taste, style, or tool preference unless it creates a concrete
  correctness, security, operability, or maintainability risk.
- Do not assume downstream service names belong in tacplus. tacplus is generic;
  downstream services are consumers.
- Do not drop a finding because it is already known. A known, unfixed issue is
  an open finding: when the local ledger is present, it stays in
  `todo/OPEN_FINDINGS.md` and appears in every analysis report until it is
  actually fixed, with evidence.
- Do not modify files, commit, push, tag, or change downstream repositories as
  part of the analysis unless explicitly asked.
- Keep generated output disposable. Generate into temporary or ignored target
  directories only.
- Separate confirmed findings from hypotheses and discarded leads.

## Contract Inputs

Read these first:

- `README.md`
- `AGENTS.md`
- `roadmap/README.md`
- `docs/API.md`
- `docs/CONSUMER_NEUTRAL_ANALYSIS.md`, or the canonical repository copy at
  https://github.com/tacplus-dev/tacplus/blob/main/docs/CONSUMER_NEUTRAL_ANALYSIS.md
- `docs/DEVELOPMENT.md`
- local `todo/OPEN_FINDINGS.md`, when present — the working ledger of known,
  unfixed findings
- `docs/THEORY.md`
- `docs/USER.md`
- `the archived GENERATED_CODE_ISSUES plan` — the closed historical TCG fix ledger
  (background evidence only; the active ledger is `todo/OPEN_FINDINGS.md`)

Use `docs/API.md` and the annotated proto model as the generated API contract.
Use `docs/DEVELOPMENT.md` for compiler and soundness expectations. Use
`docs/CONSUMER_NEUTRAL_ANALYSIS.md` as the contract tacplus expects downstream
consumers and LLM reviewers to follow. If that file is not present locally, use
the repository URL above. The tacplus analysis must verify that the compiler
and generated code actually make those consumer rules practical, consistent,
and enforceable. Use `roadmap/README.md` for future tracks, historical baseline
references, and plan archives.

## tacplus Assumptions

- `tacplus.yaml` declares only settings that cannot be logically defined in
  proto, such as package naming, language target, output location, and enabled
  generated surfaces.
- Downstream consumers should be able to use the generated model rather than
  hand-roll lifecycle, auth, validation, serialization, state, persistence,
  EventBus, and client wiring. If a downstream consumer must bypass generated
  infrastructure for a common pattern, that is evidence of a possible tacplus
  gap.
- Consumer-local tools should consume `registry.json` or embedded generated
  metadata for tacplus semantics, not parse proto files directly. The registry
  must therefore contain enough information for downstream codegen, policy,
  docs, and LLM analysis tools to avoid ad hoc proto parsing.
- A change to a `spec.vservice` proto should not require manual service
  infrastructure edits. Tacplus should expose enough generated model/registry
  data and generated seams for consumers to handle vservice fallout through
  automated codegen/tooling patterns.
- C2S and W2S are independently enabled generated surfaces, not language
  defaults. TypeScript may use W2S for browser consumers, but TypeScript also
  needs C2S when used on the server and C2S-capable languages need W2S when
  enabled.
- Auth-disabled methods skip authentication and authorization only; validation
  still applies.
- Auth-enabled methods must validate messages, require a valid token, enforce
  authorization, and enforce namespace policy before dispatch.
- Long-lived authenticated streams (client-streaming/bidi) carry exactly one
  token, attached at STREAM OPEN — gRPC metadata cannot change mid-stream, so
  a per-send token preflight is prohibited (it mints a token the server never
  sees and can fail a legitimate send). Servers terminate streams carrying
  expired credentials; generated reconnect paths mint a fresh token per
  attempt. See docs/API.md "Client Send Contract".
- A subject may act only on an object namespace at or below the subject's
  namespace level.
- If tacplus cannot generate a full supported surface for a declared proto
  shape, generation should fail loudly.

## Model Smells

A model smell is any generated surface that is computed from inference,
build-inclusion, or hardcoded literals instead of a scoped first-class
declaration. Per `docs/THEORY.md`, a healthy model has:

- Single source of truth: every constraint, boundary, auth policy, and RPC
  exists in one place — the proto declarations.
- Generation scoped to the owner: a service's surface is a function of what it
  declares and owns, not of which protos its build happens to include.
- First-class declarations, not inference: "who publishes, consumes, or owns X"
  is declared, not recovered from package layout, message names, or a code
  literal.

Recurring variants to sweep for, across `pkg/internal/**`, `pkg/templates/**`,
and `pkg/proto/sys/spec.proto`:

- a build-wide scan that emits per-service output without an ownership filter,
  so an unrelated proto leaks into another service's generated surface;
- two mechanisms encoding one concept, so the generated surface depends on
  which incidental encoding the author chose;
- an owner or role recovered by package, service, or message name inference —
  or a hardcoded literal — where a first-class declaration already exists or
  should exist.

The tell is the cross-entity collapse: adding, removing, or renaming an
unrelated entity silently changes another entity's generated output.

## Generated Output Parity Audit

This section absorbs the retired `deep-parity` harness. When the analysis
needs generated-output evidence (workflow steps 3-5), build a disposable
kitchensink and compare the outputs directly:

1. Create a temporary project directory per generating language, each with a
   minimal `tacplus.yaml` (same `pkg`, explicit client-surface settings) and
   the language's project file (`go.mod` / `package.json` / `pyproject.toml`
   / `pom.xml` / `Cargo.toml`).
2. Run `tacplus build --src <proto-src>` in each. Every supported language
   must build; a generation failure is itself a finding.
3. Compare directory structures across languages for each major generated
   directory (`types/`, `sys/`, `s2sm/`, `c2sm/`, `w2sm/`, `c2dm/`,
   `rest/`): the same services and packages must exist in every language
   that supports the surface, minus documented target-class differences.
4. For each service, compare the generated files — server wrappers, gRPC
   clients, state wrappers, REST clients. Flag any service present in some
   languages and missing in others.
5. Deep-compare two or three representative services: same methods, same
   features (auth, validation, accounting, streaming, TLS), same
   error-code mapping.
6. Compare `sys/` runtime public API surfaces (functions, methods,
   classes) across languages.

Keep the kitchensink in a temporary or ignored directory; never commit the
outputs.

## Consistency And Documentation Pass

Every full analysis run includes this pass over the compiler source and the
documentation: thoroughly look through the code and determine whether there
are any inconsistent patterns, naming, or layout, and make sure the
documentation is correct.

Patterns and layout:

- Layout parity across `pkg/internal/<lang>/` packages: the same concern
  should live in the same-named file in every language generator. Flag
  drift, and flag any gate (for example `cmd/soundness`) forced to hand-map
  per-language file exceptions — a hand-map that goes stale hides defects.
- Emitter mechanism: sibling languages should use the same mechanism
  (template file vs string-building) for the same generated surface; flag
  outliers.
- Duplicated helpers that belong in `common/` — especially copies whose
  behavior has diverged (case conversion, name mapping).
- Package visibility: any `pkg/` package not reachable from `cmd/` belongs
  in `pkg/internal/`.
- Dead code: unused functions, types, constants, and parameters; orphaned
  templates; dangling template references; dead skip lists.
- Committed artifacts that violate stated invariants (for example generated
  output committed under `cross-test/`).

Naming:

- `spec.proto` annotation families: suffix conventions, synonym pairs, enum
  value naming, `reserved` statements for retired field numbers, and the
  option-number range scheme.
- Registry schema (`tacplus.proto`): one name per concept — flag same-name
  different-meaning fields and different-name same-meaning fields.
- Cross-artifact concept drift: a concept should carry the same name in
  `spec.proto`, the registry, `tacplus.yaml`, generated APIs, and the docs.
- Exported identifiers named consistently for what they do.

Documentation correctness — verify every checkable claim against code:

- `README.md`, `AGENTS.md`, and every file in `docs/`: CLI commands and
  flags, `tacplus.yaml` keys, annotation tables, code examples (they must
  compile as written), generated-library tables, architecture and directory
  maps, CI/gate descriptions, and numeric limits.
- Docs must not document surfaces that do not exist, and must not omit
  supported surfaces consumers need.
- Cross-references must resolve; tooling script headers and embedded prompts
  must match current paths and behavior.
- The same standard applies to comments in `spec.proto`/`tacplus.proto` that
  carry examples or annotation references.

Report the results in the standard Finding Format and record them in
`todo/OPEN_FINDINGS.md` like any other finding.

## Analysis Workflow

1. Read the contract documents and the relevant generator code.
2. Re-verify every entry in local `todo/OPEN_FINDINGS.md`, when present,
   against current source.
   Report each entry that is still present as an open finding; move an entry
   out only with fix evidence (commit plus passing regression), and update the
   ledger with new confirmed findings from this run.
3. Generate representative output for each full language when source review is
   not enough to prove behavior (the kitchensink recipe in "Generated Output
   Parity Audit" is the standard fixture).
4. Review generated output as production code, not as an implementation detail.
5. Compare generated surfaces across languages for semantic parity:
   - handler seams;
   - service wrappers;
   - generated clients;
   - C2S and W2S transport behavior;
   - C2D behavior where applicable;
   - collection snapshot and watch behavior;
   - validation, auth, tracing, accounting, retry, timeout, and teardown.
6. **Promise-coverage pass.** For each behavioral guarantee stated in
   `docs/API.md` (and AGENT.md where it makes claims about generated
   behavior), enumerate the code paths where the promise must hold and demand
   mechanical evidence on each path, per full language:
   - a generated/contract test that exercises the path (including the
     NEGATIVE case — the promise observably refusing a violation);
   - or an enforced `cmd/soundness` row;
   - or an explicitly documented, rationale-bearing carve-out in API.md.
   Path enumeration matters more than mechanism existence: a promise like
   "outbound messages are validated" must be checked on EVERY send path
   (unary response, server-stream item, client-stream/bidi outbound, W2S
   REST/SSE/WebSocket writes, C2D responses), not declared satisfied because
   validation code exists somewhere near one of them. A promise with no
   mechanical evidence on some path is AUTOMATICALLY a finding — absence of
   proof is the finding, even with no observed failure and no cross-language
   divergence. (Motivating miss: OF-30 — egress validation existed on some
   paths, was promised in API.md, and had no per-path evidence; two full
   analysis runs passed it because existence checks and divergence checks
   both came back clean.) Two reviewer stances to hold during this pass:
   read each promise from the RECEIVER's perspective (W2S browser apps and
   external consumers are not tacplus-generated — egress guarantees are
   their only protection), and treat a green harness as evidence only for
   behaviors the harness actually exercises.
7. Run a model smell check: sweep the generator and template code for the
   smell variants defined in "Model Smells" above. Report each smell even when
   no runtime failure has been observed yet; the collapse mode is the finding.
8. Run the consistency and documentation pass defined in "Consistency And
   Documentation Pass": inconsistent patterns, naming, layout, and
   documentation correctness.
9. Cross-check tacplus against the downstream-consumer rules in
   `docs/CONSUMER_NEUTRAL_ANALYSIS.md`:
   - generated ServiceManager surfaces must make handwritten lifecycle,
     transport, auth, validation, persistence, EventBus, and helper plumbing
     unnecessary for common service patterns;
   - generated auth and namespace enforcement must be available at the wrapper
     seam so consumers do not need parallel auth systems;
   - generated registry metadata must be sufficient for consumer-local
     generators and LLM tools to avoid parsing proto text for tacplus
     semantics;
   - `spec.vservice` changes should have generated/registry-driven paths for
     service-local wiring fallout;
   - generated identity surfaces (`sys/spiffe`, token wrappers, authorizers,
     service-manager identity) must make the consumer SPIFFE identity rules
     followable: construction, self-describing parsing, comparison, and
     distinct `azp` exposure all come from generated code, so a consumer never
     needs to hand-format or string-compare a SPIFFE ID;
   - generated API and registry metadata should support verb/capability
     analysis and expose enough evidence to detect method overload.
10. Reproduce suspected failures with the smallest useful fixture.
11. Record confirmed findings with severity, evidence, and a recommended fix.
12. Record unconfirmed hypotheses separately, with the missing evidence
    clearly stated.

## Finding Criteria

A finding should be included when it has at least one concrete failure mode:

- generated code fails to compile for a supported declared surface;
- generated code violates `docs/API.md`;
- a behavioral guarantee documented in `docs/API.md` lacks mechanical evidence
  (test, enforced soundness row, or documented carve-out) on one or more code
  paths where it must hold — absence of proof for a documented promise is a
  finding even without an observed failure (promise-coverage pass, workflow
  step 6);
- one language silently differs from another for the same declared surface;
- validation, auth, authorization, or namespace enforcement can be bypassed;
- a failure is swallowed, downgraded, or hidden from the caller;
- an irrecoverable startup or runtime failure does not fail fast;
- retry, timeout, or cancellation behavior can hang or gray-fail;
- a long-lived stream can leak tasks, goroutines, threads, handles, or tokens;
- generated code can deadlock or race under normal use;
- generated code exposes a security weakness;
- a generated surface is computed from inference, build-inclusion, or a
  hardcoded literal rather than a scoped first-class declaration, so a change
  to an unrelated entity silently changes another entity's generated output;
- tacplus contains stale downstream-specific or deprecated concepts that affect
  generated behavior or confuse the contract.
- generated code or templates format `spiffe://` outside `sys/spiffe` and
  tests, project `namespace` as a complete subject identity, expose a SPIFFE
  identity that is read from an untrusted wire claim rather than derived, or
  compare identities by raw string prefix instead of the generated
  `is_at_or_below` / `is_spiffe_at_or_below` relations. The cross-test
  soundness gate (`assert_spiffe_soundness`) should catch raw formatting; a
  shape that slips past the gate is itself a finding.
- tacplus generated code or registry output forces consumers to violate
  `docs/CONSUMER_NEUTRAL_ANALYSIS.md`, such as parsing proto text for tacplus
  semantics, hand-wiring EventBus publishers, bypassing ServiceManager-owned
  lifecycle, or manually patching infrastructure after a `spec.vservice`
  schema change.
- a consumer violation is unavoidable because tacplus has declared a consumer
  rule but does not yet generate the model surface required to follow it.

## Finding Format

Use this shape for each issue:

```text
ID:
Severity:
Confidence:
Status: confirmed | hypothesis | discarded
Surface:
Language(s):
Source:
Generated evidence:
Failure mode:
Reproduction:
Recommended fix:
Notes:
```

Prefer file and line references relative to the repository root. Avoid absolute
paths in written reports.

## Useful Commands

Run these from the repository root as needed:

```sh
rg "BigData|deprecated|reserved|fail open|TODO|panic|unwrap|expect"
rg "spiffe://|spiffe_id|azp" --glob '!**/sys/spiffe/**'
rg '== "(gateway|eventbus|ServiceHealth|Mint)"' pkg/internal pkg/templates
go test ./...
go run ./cmd/soundness -root .
just consumer-build
just consumer-smoke
```

When generating analysis fixtures, keep outputs outside tracked source paths or
inside ignored target directories.

## Expected Output

The final analysis should lead with confirmed findings ordered by severity. It
should then include hypotheses that need more evidence, discarded leads that
looked suspicious but were ruled out, and any open questions that require a
human decision rather than a technical fix.

Do not use the report to relitigate already withdrawn work. If a concept was
parked, withdrawn, or downstream-only (check `roadmap/README.md` for future
tracks and `archive/` for completed/withdrawn plans), treat that status as the
source of truth unless current code contradicts it. Withdrawn status applies
to features and scope decisions only, never to defects: a previously reported
defect that is still present in the code is an open finding every time, and
prior reports, ledgers, and "already known" status are reasons to fix it, not
reasons to leave it out of the report.
