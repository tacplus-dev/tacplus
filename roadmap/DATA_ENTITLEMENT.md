# Data Entitlement Architecture

Status: design discussion. This is not a tacplus-only implementation plan.

## Implemented Surface (as of 2026-06)

The contract-surface pieces this document depends on have landed in tacplus and
are tracked implementation, not design discussion:

- Native `COLLECTION_MODE_PAGE` paged List contracts (request/response paging
  metadata), used for high-cardinality reads and the internal GraphQuery
  `service_s2s` paged List path below.
- Flat `repeated string grants` entitlement vocabulary (the generated
  `TokenEntitlements` shape; generated helpers normalize/validate grant tokens
  and fail loud on unknown names).
- Browser/client-only TypeScript token libraries (structural key/JWK interfaces;
  server-only identity construction such as `opaqueSegment` is fail-closed).
- REST routing uses `{root}/{package}/{service}/{method}`, mirroring the gRPC and
  entitlement-grant identity (collision-free per package).

The remaining sections (asynchronous release flow, release processors,
capability shell, finish-phase LLM check, tenant cells) are still design.

Existing entitlement in tacplus mostly answers whether a bearer may make an RPC
call. Data entitlement asks a different question:

> Is this requester, executor, or recipient entitled to this data, this use of
> the data, and this output?

The answer may be binary, but it is often a constrained release: filtered rows,
redacted fields, anonymized identifiers, aggregate-only output, or use of data
inside a computation without possession by the caller.

## Core Distinction

RPC authorization controls whether a caller can invoke an operation at all.
Data entitlement controls what data may participate in the operation and what
output may leave the service.

These are separate layers:

1. Can the caller invoke this RPC?
2. Can this request initiate a data-returning or data-using job?
3. Can the executor use the sealed data for this purpose?
4. What, if anything, may the recipient receive?

The agentic case makes this split essential. An LLM or service executor may be
entitled to use data that the human requester is not entitled to observe or
possess. The requester may still be entitled to a derived answer, such as an
aggregate count, if the output satisfies release policy.

## External Token Signing Contract

External federation assertions are part of the data-entitlement boundary: the
mint request says which signing-key family the downstream verifier requires,
and the issuer either honors that exact family or rejects the request. It must
not silently downgrade to another algorithm.

The contract is:

| Request field | Kernel behavior |
|---|---|
| `requested_signing_key_type` unset or `KEY_TYPE_UNDEFINED` | Use the default internal family: OKP / Ed25519 / EdDSA. |
| `KEY_TYPE_OKP` | Use OKP / Ed25519 / EdDSA. |
| `KEY_TYPE_EC` | Use EC / P-256 / ES256 for verifiers such as Anthropic WIF. |
| `KEY_TYPE_RSA` | Remains in generated `key.KeyType`, but the current kernel keyring rejects it until RSA signing keys are supported. |

This keeps tacplus-generated clients vendor-neutral while allowing each proxy or
service integration to request the algorithm required by its external verifier.
The runtime rule is fail-closed: if the requested key type is unsupported or no
active key exists for it, the mint fails instead of returning a token signed by
a different family.

## Entitlement Modes

Avoid treating "read" as the only meaningful data right. Useful modes include:

| Mode | Meaning |
|---|---|
| `USE` | Data may influence an operation, but the caller may not see or receive it. |
| `DERIVE` | A controlled computation may produce approved outputs from the data. |
| `OBSERVE` | The recipient may see data values in an approved surface. |
| `POSSESS` | The recipient may receive, export, store, or further process the data. |

These modes are not a simple ladder. `DERIVE` may allow an aggregate answer
without allowing observation. `USE` may allow an opaque data handle to be passed
to a service without allowing enumeration of the underlying records.

## Principals

Data entitlement should name the actors in the data flow, not only the caller:

| Principal | Role |
|---|---|
| Requester | The human, service, or agent initiating the task. |
| Executor | The service, LLM, agent runtime, or release processor using the data. |
| Recipient | The principal that will receive the released output. |
| Tenant | The isolation domain where data and execution live. |
| Data subject | The person, service, account, or resource represented by the data. |

The effective decision can differ by principal. For example, the executor may
use prompt logs, while the requester may only receive a thresholded aggregate.

## Data-Returning RPCs

RPCs that can return protected data should use an asynchronous data entitlement
flow rather than returning raw data directly.

The high-level shape:

1. The caller invokes a data-returning RPC.
2. The service performs normal RPC authorization and preflight entitlement.
3. The service returns a nonce or job id.
4. Candidate results are generated into sealed service-owned storage.
5. A release processor evaluates and transforms the sealed data.
6. The final output is released, denied, or quarantined.
7. The caller fetches the entitled result by nonce.

The nonce is a handle to a pending release job. It is not a capability to fetch
raw candidate data.

Candidate data remains inside the service or tenant cell until the release
processor has produced a validated release envelope.

GraphQuery is an internal exception to the human-facing asynchronous release
flow: tacplus now generates it as a `service_s2s` paged List contract. That
keeps graph traversal behind service authorization while still requiring
downstream callers to page by explicit request/response metadata instead of
holding an unbounded C2S stream. Any user-visible data returned through that
path still needs a recipient entitlement/release layer above the internal graph
read.

## Release Decisions

A release decision should be structured. It should not be only `allow` or
`deny`.

Possible release actions:

| Action | Description |
|---|---|
| `DENY` | Release nothing. |
| `ALLOW_RAW` | Release data without transformation. |
| `FILTER` | Drop rows, objects, events, or partitions. |
| `REDACT` | Remove or mask fields. |
| `PSEUDONYMIZE` | Replace identity with a scoped surrogate. |
| `GENERALIZE` | Reduce precision, such as city to region or timestamp to day. |
| `SUPPRESS` | Drop small cohorts or rare values. |
| `AGGREGATE` | Return aggregate outputs instead of source records. |
| `NOISE` | Add approved noise for privacy protection. |
| `REQUIRE_FINISH_CHECK` | Require a final leak check before release. |

"Anonymization" should be used carefully. True anonymization is difficult.
Many practical cases are de-identification, pseudonymization, generalization,
suppression, aggregation, or noise.

Pseudonyms should declare scope:

| Scope | Meaning |
|---|---|
| Job-scoped | Stable only within one release job. |
| Tenant-scoped | Stable across jobs for one tenant. |
| Global-scoped | Stable across tenants or the whole platform. |

Job-scoped pseudonyms are safer. Tenant or global scopes support longitudinal
analysis but increase re-identification risk.

## Python Release Processors

OPA is a reasonable policy engine for declarative allow/filter/redact
decisions, but Stratum may need richer release processors that can directly act
on sealed data. The preferred direction for that richer path is Python executed
inside the JVM, with Stratum providing the security shell and capability
surface.

In this model, release processors are signed, versioned Python programs that run
inside the JVM and operate on sealed candidate data. They may run through the
state store's distributed executor so computation is distributed and colocated with the
data.

Release processors can:

- filter rows or events
- redact fields
- pseudonymize or generalize identifiers
- compute aggregates
- enforce cohort thresholds
- add approved noise
- deny release
- emit a typed release envelope

The processor should not receive ambient JVM access. It should receive only
explicit Java capability objects supplied by Stratum.

## JVM Capability Shell

The JVM shell is the security kernel for Python release processors.

The Python runtime should have:

- no ambient host class lookup
- no reflection access
- no native access
- no filesystem access unless explicitly mounted
- no network access unless explicitly granted
- no arbitrary process exit path
- bounded CPU, memory, output size, and wall time
- controlled stdout, stderr, logging, metrics, and exceptions

`System.exit(-1)` should be denied because the script has no route to
`java.lang.System` or other ambient host APIs. The allowed surface is the set of
Java methods explicitly exported to the processor.

The exported Java API should be semantic rather than raw:

```java
ctx.rows("prompt_events")
ctx.field(row, "model")
ctx.protectedField(row, "prompt")
ctx.pseudonymize(row, "user_id", scope)
ctx.emitAggregate("unique_users", count)
ctx.emitRedacted(row, policy)
ctx.deny(reason)
```

Avoid passing raw maps or unrestricted objects unless the processor is fully
trusted and the result is still contained by the release envelope. Logs,
exceptions, traces, and metrics are egress paths and must be governed like
normal output.

Distributed execution should return only typed partial release outputs:

```text
partition processor -> PartialReleaseEnvelope
coordinator reducer -> FinalReleaseEnvelope
finish checker -> ReleasedResult | DeniedResult
```

Raw candidate rows should not move from data members to the coordinator unless
they remain inside sealed storage.

## Finish-Phase LLM Check

An LLM finish check can be useful as defense in depth, especially when the
released output includes prose summaries or generated explanations.

The finish checker should not be the primary policy authority. Deterministic
authorization and release processors decide what is allowed. The finish checker
inspects the final releasable output for likely leaks, such as accidental raw
identifiers, rare quasi-identifiers, or prose that makes a protected subject
obvious.

The finish check may:

- allow release
- deny release
- quarantine for manual review
- request a stricter transform

The finish checker should normally see only the transformed release candidate,
not raw sealed data.

## Tenant Cells

Stratum can strengthen data entitlement by running dedicated tenant cells.

One possible shape:

```text
tenant -> Stratum tenant cell -> 3 stateful instances -> tenant-local data,
release processors, sealed result storage, and nonce resolution
```

New tenant cells can be spun up dynamically. Each tenant receives a small
stateful Stratum cluster, such as three instances, and all tenant data and
release processing stay inside that cell.

Benefits:

- stronger tenant isolation than logical multi-tenancy alone
- data locality for state-store-backed release processing
- smaller blast radius for bugs
- per-tenant scaling, upgrades, and migrations
- simpler audit boundaries
- tenant identity derived from routing and instance identity, not from an
  untrusted request argument

This implies two planes:

| Plane | Responsibility |
|---|---|
| Control plane | Tenant registry, placement, routing, provisioning, certs, health, upgrades, quotas. |
| Tenant data plane | Tenant-local Stratum instances, state-store state, release processors, sealed result store. |

Cross-tenant analytics should be a separate privileged plane, not a normal
tenant-cell operation.

## Role Of Tags

The tags feature remains useful, but tags should not be the whole data
entitlement language.

Tags can provide facts to release processors:

- dataset classification
- data domain
- tenant-local resource category
- business unit
- region
- sensitivity label
- processor eligibility

The entitlement engine should still be able to use richer data, schema,
principal, purpose, and output-shape facts that are beyond tag matching.

## Role Of tacplus

This is broader than tacplus. tacplus should not own all of data entitlement or
the Stratum release runtime.

Likely tacplus responsibilities:

- define proto annotations or semantic IR for data-returning RPCs
- generate asynchronous request, nonce, status, and result-fetch surfaces
- preserve data-returning RPC metadata in `registry.json`
- generate typed request and result envelopes
- enforce normal RPC authorization before dispatch
- pass requester, executor, recipient, tenant, purpose, and schema metadata to
  downstream services
- make downstream handoff explicit in generated service contracts

Likely downstream Stratum responsibilities:

- tenant-cell routing and lifecycle
- sealed candidate result storage
- release processor registration, signing, and versioning
- JVM Python execution shell
- state-store distributed execution
- release envelope validation
- anonymization, redaction, aggregation, and privacy transforms
- finish-phase LLM leak checks
- audit logs and release evidence

tacplus should describe and type the contract. Stratum services should execute
the data entitlement pipeline.

## Example

Question:

> How many unique users sent prompt XXX to LLM YYY?

This should not require the requester to read or possess prompt logs.

A possible flow:

1. Requester calls an async analytics RPC with prompt `XXX`, model `YYY`, time
   window, purpose, and recipient.
2. The RPC returns a nonce.
3. Candidate prompt events are located inside the tenant cell.
4. A release processor runs near the data and computes distinct user count.
5. The processor suppresses output if the cohort is too small.
6. The processor emits only an aggregate release envelope.
7. A finish checker verifies no raw prompt logs or user identifiers are present.
8. The requester fetches the aggregate result by nonce.

The executor used protected data. The requester received only entitled derived
output.

## Open Questions

- What should the new data-returning RPC verb or annotation be called?
- Should tacplus generate one generic async surface or per-RPC typed nonce and
  result services?
- What are the nonce semantics for TTL, cancellation, idempotency, retries, and
  revocation?
- How are release processors registered, reviewed, signed, versioned, and
  rolled back?
- Which processor capabilities are stable platform API, and which are
  service-local?
- What is the minimum release envelope schema?
- How are stdout, stderr, logs, traces, metrics, and exceptions governed?
- When is a finish-phase LLM check mandatory?
- What privacy transforms are platform-provided versus service-provided?
- How do tenant cells sleep, wake, migrate, and recover?
- What separate plane handles cross-tenant analytics?

## Design Principles

- RPC authorization and data entitlement are different layers.
- Data may be usable without being observable or possessable.
- LLMs and service executors may have broader use rights than recipients.
- Every output remains constrained by recipient entitlement.
- Data-returning RPCs should release results asynchronously through a nonce.
- Candidate results stay sealed until transformed into a release envelope.
- Python release processors should run through a narrow JVM capability shell.
- Distributed release processing should run near the data.
- Deterministic release policy comes before LLM leak checking.
- Tags are useful metadata, not the whole entitlement language.
- tacplus should type and generate the contract; Stratum should execute the
  tenant-local release pipeline.
