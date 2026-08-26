# Record / Event / Store / Projection model (annotation contract)

This is the authoritative tacplus contract for the data plane shared by **eventbus**
and **stratum**. Both are generic, app-agnostic services: their hand-written code
names no vservice; everything vservice-specific is generated from the annotations
below. `mv <vservice>.proto .off` makes the vservice disappear everywhere with only
a rebuild.

> **timedb and graphdb went to the boneyard 2026-08-25** (BIG GOAL 3, first move
> #1): out of the fleet build, protos deleted. With them went the `graph_store`,
> `graph_projection` and `timeseries_store` annotations' only hosts, and the
> `TimedbQuery` / `GraphdbQuery` / `TimeseriesQuery` / `TimeseriesGroupCounts`
> query planes. The `graph_store` / `graph_type` / `graph_projection` generator
> code — parser, IR, Go graph helpers (`ToGraph`/`FromGraph`), the graph branch of
> the extraction emitters, and the graph legs of the Go templates — was removed
> from tacplus on the same day (BONEYARD_QUESTIONS Q2), along with the field-level
> `spec.graph`, `graph_index` and `graph_edge` options that only ever applied
> inside a `graph_type` message. All those option numbers are retired in
> `spec.proto`, never reused. `timeseries_projection` has since been retired too
> (option 60067, 2026-08-25, PLAN.md acceptance item 3): it bundled a pure typed
> conversion together with a **retention policy** and with eventbus delivery, and
> those are now three separate declarations. The conversion is
> **`spec.transformation`**; retention is **`(spec.record).retention`**; delivery is
> the `record`/`event` producer surface and the topic it synthesizes. Neither the
> transformation nor the delivery declaration requires the other to generate.

## Mental model

- An **event** flows through eventbus: it has a topic and a publish RPC. Producers
  are external clients (C2S, via gateway) and/or internal services (S2S, e.g. the
  `*-proxy` services). The producer surface is **declared, never defaulted** — an
  internal-only event is S2S and is not exposed C2S.
- A **record** *is* an event that is additionally **stored in full** (stratum) and
  is **projectable**. `records ⊂ events`: every record is an event; not every event
  is a record (e.g. a trace event is event-only).
- **stratum** holds the full records. A **projection** is an offshoot derived from a
  record; since the boneyard there is no separate service that hosts one.

```
producer ─[event]→ eventbus ──→ stratum   stores the full record
```

Both producer paths are identical downstream: `client → gateway → eventbus → …`
and `service → eventbus → …`. The store cares only about the type, not the producer.

## Hand-written annotations (the entire surface)

Message-level:

| Annotation | Meaning |
|---|---|
| `event { c2s{…}? , s2s{…}? }` | eventbus **topic + publish RPC**. ≥1 producer surface required. |
| `record { key, owner, c2s{…}? , s2s{…}? , retention{…}? }` | **DELIVERY.** **is-an `event`** + stored in `record_store` + projectable. `key` = id field (default: `event_id`), `owner` = ownership field, `retention` = the store-neutral row retention policy (`retention.timestamp_field` is required on every stratum-stored record). |
| `transformation {…}` | **TRANSFORMATION.** A pure typed conversion of this message into a derived row type. Requires **no** `record`, **no** topic, **no** subscription — and carries **no** retention (a pure function has no lifetime). |

Field-level (on a `record` message's fields):

| Annotation | Meaning |
|---|---|
| `query_field { op: EQ\|RANGE }` | expose this field as a typed filter criterion in the RecordMap query plane (`<T>Filter`). |
| `redact { mode: DROP\|PSEUDONYMIZE\|MAY_REDACT\|MAY_ANONYMIZE }` | field is **egress-redactable** (data entitlement): stored raw, sanitized at `Sample`/`Pull`/`Watch` per caller. DROP/PSEUDONYMIZE always apply; MAY_* are caller-decided. Generates `Sanitize<T>` + `<T>Entitlement`. |

Service-level (once per service):

| Annotation | Service |
|---|---|
| `record_store {}` | stratum (the full-record store) |

On a stored event you write only `record` (it implies `event`). You never write the
derived output types or their markers.

**Transformation and delivery are independent declarations.** A `transformation`
on a plain message generates its derived type with no eventbus involvement at all
(fixture: `FixtureProbe` in `cross-test/proto/common2/user.proto`); a `record`
with a producer surface and retention delivers and stores with no transformation
attached (fixture: `FixtureAudit` in
`cross-test/consumer-contract/proto/services/contractfixtures.proto`). Neither
declaration requires the other to generate.

## Generated (never hand-written)

- The **derived output messages** (`SessionRecord`, …) from the
  `transformation` declarations, stamped with the internal marker
  **`timeseries_type`** so downstream codegen finds them.
- The **timeseries table name**, derived from the message name in `snake_case`
  when `(spec.timeseries_type)` sets none explicitly. The conversion is
  acronym-aware: `HTTPMetric` → `http_metric`, `OpenAIUsage` → `open_ai_usage`.

  > **Migration note.** Before CON-3 a naive converter produced
  > `h_t_t_p_metric` for those names. Any deployment created before that change
  > has tables under the old spelling. This name is **persisted** — it is the
  > table the store writes to — and a change to it fails nothing: generation
  > succeeds, the service starts, and it quietly begins writing to a new table
  > while the old one stops receiving rows. To keep an existing table, set
  > `(spec.timeseries_type).name` explicitly; that value always wins over the
  > derived one. The derivation is pinned by
  > `pkg/internal/common/registry/timeseries_tablename_test.go` — changing it
  > requires a migration, not just an updated expectation.

- The eventbus **topic + publish RPC** from `event`/`record`, with exactly the
  declared C2S/S2S surfaces.
- Per-store **subscriptions**: each store auto-subscribes to the events/projections
  it must ingest.
- The **query plane** (below).

## Query plane

- **record_store (stratum)** — a per-record-type **refinable-query API** is generated
  for every `record`. A caller (an **S2S** front like **argus**, never a direct client)
  opens a query over one record type and gets a **handle** to a per-caller result set
  of matching record **references** (keys). The records themselves are **immutable** and
  the set is never a copy of them — the mutable thing is the query predicate. On a
  handle: **refine** (narrow the predicate), **count** (size), **sample** (a seeded
  monte-carlo draw of `n` records — cheap exploration without pulling everything),
  **pull** (paged read of all matches), and **watch** (a living query streams changes).
  A query is either a **one-time snapshot** or **long-lived / living** (new matching
  records auto-join). Ops run server-side. Three ops return record data — **sample**,
  **pull**, and **watch** (each stream item is a record) — and **all three apply egress
  redaction**; the caller's entitlements shape the released view at read time, and
  redaction is **never** baked into stored state. **count** returns only a size, never
  record data. tacplus knows the typed query and its shape, not the query→execution
  transform (impl-side). argus owns handle lifecycle / cleanup.
  - **Egress data entitlement** is generated from field-level `(spec.redact)`: for each
    record type T with redactable fields, tacplus emits `Sanitize<T>(record, <T>Entitlement, hmacKey)`
    — a sanitized **copy** (REDACT → zeroed + indicator set, ANONYMIZE → a keyed
    per-value token). The rule is **FAIL-CLOSED**: `DROP`/`PSEUDONYMIZE` fields are
    hard floors, always applied; a `MAY_*` field defaults to its most-protective
    level (`MAY_REDACT` → REDACT, `MAY_ANONYMIZE` → ANONYMIZE) and egresses raw
    only under an explicit `RAW` level. The **generated WRAPPER** (never the
    handler, never impl code) applies it on **every record-bearing egress path —
    `Sample`, `Pull`, AND each `Watch` snapshot item and delta** (a living query
    must not leak what a `Pull` would redact), after the handler returns, so a
    handler physically cannot bypass it. The **policy** (which fields a caller may
    see raw) is an identity property of the caller's token: OPA stamps
    `data_grants` into the `service_entitlements` claim at token-exchange time,
    the generated `Build<T>Entitlement` maps those grants onto `<T>Entitlement`
    (a matching grant loosens the field to RAW; never below a hard floor), and
    the wrapper supplies the per-deployment ANONYMIZE secret from config
    (`grpcconfig.Config.anonymize_hmac_key`) — **mechanism in tacplus, policy in
    OPA**. The caller passes no redaction knobs. Two-stage (stratum→argus,
    argus→user) can reuse the same seam later.
- Combining records of **multiple types** into one working set is done **service-side**
  — argus / the LLM-fronting service pulls several single-type sets and aggregates the
  already-redacted results in its own memory; stratum holds no cross-type sets.

## Validations (tacplus errors)

- an `event`/`record` that declares zero producer surfaces;
- a **store whose source type resolves to no EventBus topic** — the generated
  store would subscribe to nothing and never ingest a row. This is the paired
  control for the transformation/delivery split: a transformation alone is fine
  and a store alone is fine, but a store that claims a source with no delivery
  behind it is a silent dead store, and now fails generation
  (`DeriveStoreSubscriptionsFromRegistry` in
  `pkg/internal/common/boundaries/boundaries.go`).

> A `transformation` on a message that is not a `record` is **explicitly legal**
> since 2026-08-25. It used to be rejected; requiring a record made every
> transformation drag the eventbus-driven store path in with it, which is the
> coupling acceptance item 3 removed.

> **Not implemented, despite having been listed here:** "a `record` or projection
> with no hosting store service". `pkg/internal/proto/validate_virtual.go` records
> that the check was deliberately not enforced — the host store lives in a separate
> service proto and is absent from isolated/partial proto sets, so enforcing it
> per-service-build produced false failures. It was reinstated as neither a
> per-build nor a whole-fleet lint, so **it has never fired**. Listed here as a
> guarantee it does not provide, it was decoration; see
> `BONEYARD_QUESTIONS.md` Q1.

## Implementation neutrality

The contract names no backing store. stratum's distribution/compute (currently
in-memory + a distributed executor; later a unified "fast-store" tier and python3
transformers) is an implementation detail, never present in proto/tacplus. Do not introduce impl names (engine names, vendors, internal
codenames) into annotations or generated output.
