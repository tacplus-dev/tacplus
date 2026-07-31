# Downstream Consumer Neutral Analysis

This document instructs an LLM reviewer how to run a neutral analysis on a
project that consumes tacplus-generated code. It is not an analysis of the
tacplus compiler. Use `NEUTRAL_ANALYSIS.md` when the target is tacplus itself.

The goal is to review handwritten consumer code against the generated tacplus
model and the consumer's annotated proto contract. Look for concrete bugs,
security risks, operational risks, and places where the consumer has bypassed
generated infrastructure without a clear reason.

## Inputs

Read these before forming findings:

- The consumer repository's `README.md`, `AGENTS.md`, build files, and local
  service docs.
- The consumer's `tacplus.yaml` files.
- The consumer's authored proto files and generated `registry.json` or embedded
  `AGENT.md`, when present.
- The consumer's handwritten handlers, ServiceManager wiring, daemon code,
  proxy code, state access code, persistence code, and transport/client code.
- tacplus `docs/THEORY.md`, `docs/API.md`, `docs/USER.md`, and
  `docs/API.md` (RPC Patterns And Verbs; genealogy
  `../archive/RPC_PATTERNS_AND_VERBS_PLAN.md`).
- CI/build logs for the consumer, especially failures after a tacplus version
  change.

Do not edit generated code as part of the analysis. Generated output should be
treated as disposable evidence of the current model. If generated output is
committed in the consumer repository, flag it unless the repository has a
concrete generated-vendoring rationale.

## Ground Rules

- Do not argue about preferred languages, frameworks, style, or architecture
  unless the choice creates a concrete correctness, security, operability, or
  maintainability risk.
- Do not assume tacplus is wrong just because consumer code is failing. First
  determine whether handwritten consumer code is bypassing or misusing the
  generated surface.
- Do not assume consumer-specific service names belong in tacplus. If a
  consumer pattern seems generic, propose it as a tacplus gap with evidence.
- Separate confirmed findings from hypotheses, accepted deviations, and
  upstream tacplus proposals.
- If a consumer deviates from generated patterns, require the rationale to be
  in code or adjacent docs, not only in the review report.

## Core Contract

The tacplus model is the shared service chassis: lifecycle, transport, state,
auth, validation, serialization, disk persistence, generated helpers,
ServiceManager, registry metadata, and generated clients/wrappers. Consumer
handwritten code should mostly be handler/business logic attached to generated
seams.

When a consumer hand-rolls something tacplus already generates, the reviewer
must ask two questions:

- Why is handwritten code necessary for this consumer?
- If it is generally useful, why should it not be incorporated into tacplus?

If those answers are not concrete and documented, report the deviation.

## Review Patterns

### Prefer The Generated Model

Consumer code should use the generated ServiceManager and generated `model` or
`tacplus` output whenever possible. This includes:

- ServiceManager startup, readiness, health, tracing, shutdown, token refresh,
  TLS renewal, client pools, and long-lived task ownership.
- Generated service wrappers for inbound validation, auth, authorization,
  namespace enforcement, handler dispatch, outbound validation, and error
  mapping.
- Generated clients for C2S, W2S, S2S, and C2D calls.
- Generated state clients, imports, views, event subscriptions, and collection
  APIs.
- Generated serialization and deserialization helpers.
- Generated key, certificate, token, JWK, and TLS helpers such as `newKeys`,
  `newCerts`, token wrappers, and typed config/env helpers.
- Generated typed disk persistence, redo logs, snapshots, and managed
  ServiceManager persistence scheduling.

Flag raw gRPC servers, raw stubs, hand-auth interceptors, custom state clients,
custom backup loops, direct JSON persistence, direct backing-store access, or
manual token/TLS plumbing when a generated surface exists.
Generated header warnings that identify raw transport clients or internal
wiring as tacplus implementation details should be treated as part of this
contract.

### Auth Is Generated Infrastructure

Authentication and authorization are declared in proto and enforced by generated
wrappers. Consumer code should not add a parallel auth system, weaken generated
checks, or manually short-circuit entitlement decisions.

Check that:

- Auth-enabled calls cannot reach handlers without token verification,
  authorization, entitlement checks, and namespace enforcement.
- Auth-disabled calls still validate messages.
- Long-lived authenticated streams require a valid token before each outbound
  generated client send. An open stream is not permission to keep sending after
  token refresh fails.
- Subject namespace and object or target namespace are treated separately; a
  subject may act only on namespaces at or below its own namespace.
- `auth_any_user` is not treated as anonymous access. It means an authenticated
  user is required, with `/` meaning any authenticated cluster user and a tenant
  namespace meaning a user authenticated for that tenant.
- TLS disablement is not used as a broad escape hatch. A consumer must have a
  narrow, documented reason, such as the gateway path for Let's Encrypt
  challenges.

Manual domain checks inside a handler can be valid, but they must not replace or
bypass generated auth. If the consumer needs a new generated policy seam, report
that as a tacplus enhancement candidate.

### SPIFFE Identity Comes From Generated Helpers

`sys/spiffe` is the single canonical SPIFFE surface: construction,
self-describing parsing, normalization, and comparison. Generated token
wrappers expose `namespace_path()`, `namespace_spiffe_id()`, `service_path()`,
`subject_spiffe_id()`, and `resource_owner_spiffe_id()`; generated authorizers
gate S2S on subject identity and gate level checks on `namespace`. Consumer
code should never hand-build or hand-compare identity values.

Flag consumer code that:

- manually formats `spiffe://` strings via printf verbs, `format!`, f-strings,
  template interpolation, or string concatenation instead of calling
  `sys/spiffe` constructors;
- derives resource or owner identity from an email address or another mutable
  external identifier; external identifiers such as GitLab email/username are
  correlation signals, not identity — owners come from
  `resource_owner_spiffe_id` over a stable principal;
- compares SPIFFE IDs by raw string prefix or substring instead of
  `is_at_or_below` (namespace tree) and `is_spiffe_at_or_below` (identity
  path), which are distinct relations and must stay distinct;
- treats `namespace` as the complete subject identity; `namespace` is
  authorization scope, `sub`/`sub_type` is the stable principal, and the
  subject SPIFFE ID is derived from structured claims — never read or sent as
  a wire claim;
- mints or requests a cred-pair-backed user token with the cred-pair ID as
  `sub`; a cred-pair is a credential, `sub` stays the owning principal, and
  the credential context belongs in `azp`;
- lets a broker, upstream service, or caller choose `azp`; `azp` is the
  proto-defined attestation-context enum that kernel assigns from the
  authenticated broker identity;
- drops or rewrites `azp` during token exchange or refresh;
- accepts a malformed SPIFFE ID or namespace by falling back to root, empty,
  or anonymous identity instead of failing closed.

### Validation Stays At Boundaries

Generated wrappers validate inbound requests before handler dispatch and
validate outbound responses or stream items before sending. Consumer code should
not bypass wrappers with raw protobuf messages, unchecked maps, ad hoc JSON, or
direct transport writes.

Flag any path where invalid data can enter or leave:

- Raw request decoding outside generated wrappers.
- Manual response streaming that skips generated response validation.
- Error handling that silently drops invalid items instead of failing closed.
- Persistence restore paths that skip generated deserialization and validation.
- Direct state mutation that bypasses generated state permissions.

### Do Not Overload RPC Methods

One RPC should represent one static capability. A method named like a read must
not become a write because of a request field, mode flag, oneof arm, or
conditional branch. A method named like an action must not sometimes behave like
unbounded read, state mutation, or authorization.

Look for request fields such as `mode`, `op`, `action`, `type`, `force`,
`upsert`, `delete`, or broad oneofs that select materially different behavior.
These are not automatically wrong, but they are strong signals that one RPC may
be doing multiple jobs.

If behavior crosses capability boundaries, recommend splitting the RPC into
canonical single-purpose methods.

### Use Correct Verbs

RPC names should follow the governed verb model:

- Use canonical verbs for existing patterns instead of inventing synonyms.
- Do not force a current verb if the operation genuinely has a new pattern.
- If a new verb is needed, require a concrete rationale and propose the verb as
  a tacplus plan update rather than smuggling it into consumer code.
- The verb should imply the generated handler/client shape, retry safety, audit
  meaning, and entitlement capability.

Report verbs that hide access kind, retry behavior, or audit meaning. For
example, a `Get*` that mutates state, a `List*` that is unbounded but lacks a
collection annotation, or an `Evaluate*` that is really an identity
authorization decision.

### Use Generated State And Event Patterns

Services should use shared state, generated state imports, views, eventbus,
subscriptions, topics, queues, and declared projections before inventing a
custom coordination protocol.

Flag:

- Direct access to another service's owned state or backing store.
- Handwritten service-to-service state mutation where a generated import, view,
  event, command, or store surface exists.
- Custom intra-service or inter-service protocols that duplicate eventbus or
  generated state capabilities.
- Hidden dependencies on concrete backing services that should be behind a
  generated rivet or handler seam.

The SMB/blackbox idea is not permission to write arbitrary intra-service
protocols. Treat blackbox integration as a product-boundary exception for
opaque external systems. Within a tacplus-generated service graph, prefer
declared shared state, generated state imports/views, eventbus, generated
clients, and declared proto surfaces. SMN is withdrawn tacplus work and should
not be revived.

SMB (`spec.smb`, SPIFFE plan D7) secures a service's own instances' black-box
peer protocol. tacplus generates the member-cert seam: the
ServiceManager opens the `SmbCertService` stream to the designated authority
(`spec.smb_authority`), generates the member keypair locally, submits a CSR, and
delivers the signed member cert + the isolated per-SMB CA to the handler's
`NewSmbCert` callback. The consumer's only job is to wire that delivered
credential into the black box's mTLS. Flag consumer code that:

- wires the cluster's **true ROOT CA** (or any trust anchor other than the
  per-SMB CA delivered by `NewSmbCert`) into the black-box mTLS trust store — the
  black box must trust only its isolated per-SMB CA;
- puts a CA private key, or the true ROOT CA, into black-box config;
- sends the member **private key** anywhere — it is generated locally and only
  the CSR (public key) goes to the authority;
- disables mTLS, or adds a non-mTLS fallback, on the SMB peer channel;
- hand-rolls its own SMB CA issuance/distribution instead of using the generated
  seam + the designated authority.

### Consume The Registry, Not Proto Text

Consumer-local tools, service-specific generators, wiring generators, docs
generators, policy generators, and LLM analysis tools should consume
`registry.json` or embedded `AGENT.md` metadata for tacplus semantics. The
consumer contract is with the generated registry, not with ad hoc parsing of
proto files.

Flag consumer code that parses proto text or file paths directly to discover:

- vservices, state roots, stores, projections, subscriptions, or event types;
- auth, entitlement, namespace, or transport semantics;
- generated package paths, service names, method categories, or registry
  metadata;
- field constraints or validation rules.

Direct proto parsing can be acceptable only for generic protobuf concerns that
are not tacplus semantics, and it must be rationalized. If the registry is
missing data a consumer reasonably needs, report that as a tacplus registry gap
instead of duplicating tacplus parsing downstream.

### Automate Vservice Fallout

A proto file annotated as `spec.vservice` is a data contract. If changing that
vservice proto requires handwritten service code changes, the consumer is
probably encoding vservice-specific structure in the wrong place.

Review rule: service fallout from `spec.vservice` changes should be handled by
an automated codegen/tooling pattern that consumes the registry and emits the
needed service-local wiring. Examples include generated handler adapters,
registration tables, config wiring, policy metadata, projection/event mappings,
and docs. The service should not need bespoke manual edits for each vservice
schema change unless the change introduces genuine new business behavior.

Flag:

- service code that switches on vservice package names or proto filenames;
- hand-maintained vservice registration lists when registry-driven generation
  could derive them;
- service-local parsing of vservice protos to recover information already in
  `registry.json`;
- build failures where a vservice schema change requires editing unrelated
  service infrastructure rather than regenerating a service-local artifact.

If manual code is truly required, the code must say why the behavior is
business logic rather than generated wiring, and why it should not be folded
into a tacplus or consumer-local generator.

### Follow Existing Local Patterns

Prefer the consumer's existing tacplus integration style when it is compatible
with the generated contract. Do not introduce a second way to wire handlers,
configure state, emit events, persist data, or call dependencies unless the
first way is incorrect and the replacement is documented.

When the repo already has a working generated pattern, a new hand-rolled variant
is a finding unless it has a clear rationale.

### Fail Closed And Surface Failures

Look for gray failures:

- Swallowed startup, config, TLS, token, state-store, or client-connection
  errors.
- Background tasks that keep running after ServiceManager shutdown.
- Retry loops without bounds, jitter, cancellation, or mutation-safety checks.
- Long-lived streams that ignore cancellation, backpressure, token refresh
  failure, or handler errors.
- Irrecoverable failures that log and continue instead of failing startup.
- Security failures that downgrade to anonymous, unauthenticated, system-root
  TLS, or "best effort" behavior.

If the correct behavior is not obvious, classify it as an open question rather
than guessing.

## Analysis Workflow

1. Identify every `tacplus.yaml` and note the generated language, package name,
   output directory, and enabled C2S/W2S/S2S/C2D surfaces.
2. Map authored proto services to generated surfaces: handlers, wrappers,
   clients, state roots, stores, collections, projections, auth annotations,
   and registry metadata.
3. Identify consumer-local codegen tools and confirm they consume
   `registry.json` rather than parsing tacplus proto semantics directly.
4. Map handwritten code to generated seams. Separate handler business logic
   from lifecycle, auth, transport, state, persistence, and helper code.
5. Search for generated-surface bypasses:
   `grpc`, `ServerBuilder`, `interceptor`, `Authorization`, `Bearer`, `JWT`,
   `Token`, `JWK`, `TLS`, `eventbus`, `FileWriter`, `ObjectMapper`,
   `serde_json`, `fs::write`, `mode`, `action`, `upsert`, `force`, `TODO`,
   `protoreflect`, `FileDescriptor`, `ProtoParser`, and direct `.proto` reads.
6. Review `spec.vservice` consumers: schema changes should regenerate
   service-local wiring rather than require manual infrastructure edits.
7. Review RPC names and handler bodies for verb drift or capability overload.
8. Review long-lived tasks, streams, retries, and background loops for
   ServiceManager ownership, cancellation, and fail-closed behavior.
9. Review persistence and serialization paths for generated helper usage.
10. For each suspected issue, collect code references, the generated contract it
   violates, and the concrete failure mode.
11. If the deviation is rationalized in code, evaluate whether the rationale is
   specific enough. If it is generic, stale, or not tied to a real constraint,
   keep the finding.
12. Propose either a consumer fix, a tacplus enhancement, or an accepted
   deviation with risk controls.

## Useful Searches

Run from the consumer repository root and adapt by language:

```sh
rg -n "grpc|ServerBuilder|interceptor|Authorization|Bearer|JWT|JWK|TLS|Token"
rg -n "spiffe://|spiffe_id|getSpiffeId|GetSpiffeId|azp|owner_spiffe|email"
rg -n "ServiceManager|service manager|shutdown|ready|health|background|daemon|thread|spawn|Task|goroutine"
rg -n "ObjectMapper|serde_json|json|FileWriter|fs::write|open\\(|redo|snapshot|backup"
rg -n "eventbus|publish|subscribe|state|projection|view"
rg -n "registry\\.json|AGENT\\.md|protoreflect|FileDescriptor|ProtoParser|\\.proto"
rg -n "spec\\.vservice|vservice|vservices|protoPackage|proto_package|packageName"
rg -n "\\b(mode|op|action|type|force|upsert|delete)\\b"
rg -n "TODO|FIXME|temporary|bypass|manual|custom|deprecated|legacy"
```

Search results are leads, not findings. Confirm each issue against the
generated contract and handwritten code path.

## Deviation Rationale Standard

When handwritten code intentionally deviates from the generated model, the code
or adjacent docs must explain:

- The exact generated surface that was not used.
- The concrete consumer requirement that forced the deviation.
- Why the behavior is not generally useful enough to add to tacplus.
- If proto text or vservice-specific manual wiring is used, why
  `registry.json` or an automated codegen artifact cannot express it.
- How auth, validation, namespace policy, lifecycle, retries, timeouts, and
  error surfacing remain safe.
- Who owns the deviation and what would make it removable.

Missing or vague rationale is itself a finding.

## Finding Format

Use this format:

```text
ID:
Severity:
Confidence:
Status: confirmed | hypothesis | accepted-deviation | upstream-tacplus-candidate | discarded
Consumer surface:
Generated contract:
Handwritten code path:
Evidence:
Failure mode:
Rationale found in code:
Recommended action:
Owner: consumer | tacplus | both | human decision needed
Notes:
```

Lead with confirmed findings ordered by severity. Then list hypotheses that
need evidence, accepted deviations with their rationale, upstream tacplus
candidates, and discarded leads that were investigated.

## What Good Looks Like

A healthy tacplus consumer has thin handlers, clear proto annotations, generated
wrappers at every transport boundary, generated auth and validation, declared
state/event relationships, ServiceManager-owned lifecycle, typed generated
persistence where persistence is needed, and explicit in-code rationale for the
small number of places where product-specific code must live outside the model.
