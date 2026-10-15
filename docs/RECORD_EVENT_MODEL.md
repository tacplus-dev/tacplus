# Record / Event / Store / Projection model (annotation contract)

This is the authoritative tacplus contract for the data plane shared by **eventbus**
and **the record store**. Both are generic, app-agnostic services: their hand-written code
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
> **`spec.transformation`**; the store lifecycle is **`(spec.record).residency`**
> (below); delivery is the `record`/`event` producer surface and the topic it
> synthesizes. Neither the transformation nor the delivery declaration requires
> the other to generate.
>
> **2026-09-20 (record lifecycle collapse):** the `Residency` enum (HOT/WARM/COLD),
> `RetentionPolicy`/`(spec.record).retention`, `(spec.archive)`, `cold_store`/
> `graph_store` and `(spec.ejectionable)` were all retired in favor of a single
> `(spec.record).residency{idle_seconds, deleted_max_age_seconds}` (two clocks, no
> enum — see `pkg/proto/sys/spec.proto:709` for the full model) plus a new
> field-level `(spec.deleted_flag)` soft-delete marker. `idle_seconds` ages a
> record out of memory on no read/write; `deleted_max_age_seconds` purges a
> flagged-deleted row that many seconds after the flag was set. Both fields reuse
> the old `retention`'s field numbers 1/2, so an old proto still saying
> `retention: {window: ...}` fails to compile rather than silently discarding the
> setting.

## Mental model

- An **event** flows through eventbus: it has a topic and a publish RPC. Producers
  are external clients (C2S, via gateway) and/or internal services (S2S, e.g. the
  `*-proxy` services). The producer surface is **declared, never defaulted** — an
  internal-only event is S2S and is not exposed C2S.
- A **record** *is* an event that is additionally **stored in full** (the record store) and
  is **projectable**. `records ⊂ events`: every record is an event; not every event
  is a record (e.g. a trace event is event-only).
- **The record store** holds the full records. A **projection** is an offshoot derived from a
  record; since the boneyard there is no separate service that hosts one.

```
producer ─[event]→ eventbus ──→ the record store   stores the full record
```

Both producer paths are identical downstream: `client → gateway → eventbus → …`
and `service → eventbus → …`. The store cares only about the type, not the producer.

## Hand-written annotations (the entire surface)

Message-level:

| Annotation | Meaning |
|---|---|
| `event { c2s{…}? , s2s{…}? }` | eventbus **topic + publish RPC**. ≥1 producer surface required. |
| `record { key, owner, c2s{…}? , s2s{…}? , residency{…}? }` | **DELIVERY.** **is-an `event`** + stored in `record_store` + projectable. `key` = id field (default: `event_id`), `owner` = ownership field, `residency` = the store's lifecycle as two clocks — `idle_seconds` (memory eviction on no read/write) and `deleted_max_age_seconds` (purge this long after `(spec.deleted_flag)` was set). Unset residency is a live platform default (`parser.DefaultResidencyIdleSeconds`/`DefaultResidencyDeletedMaxAgeSeconds`), never "never". |
| `transformation {…}` | **TRANSFORMATION.** A pure typed conversion of this message into a derived row type. Requires **no** `record`, **no** topic, **no** subscription — and carries **no** residency (a pure function has no lifetime). |

Field-level (on a `record` message's fields):

| Annotation | Meaning |
|---|---|
| `query_field { op: EQ\|RANGE }` | expose this field as a typed filter criterion in the `query` trait's plane (`<T>Filter`). |
| `redact { mode: DROP\|PSEUDONYMIZE\|MAY_REDACT\|MAY_ANONYMIZE }` | field is **egress-redactable** (data entitlement): stored raw, sanitized at `Get<T>Sample`/`List<T>`/`Watch<T>` per caller. DROP/PSEUDONYMIZE always apply; MAY_* are caller-decided. Generates `Sanitize<T>` + `<T>Entitlement`. |
| `(spec.deleted_flag) = true` | marks this record's **soft-delete state**: exactly one non-repeated `bool` field per record type (`ValidateDeletedFlagAnnotations`). The query plane excludes flagged-deleted rows by default (`include_deleted` opt-in, below); the row is purged `residency.deleted_max_age_seconds` after the flag was set. A record type with no marked field has nothing for the plane to exclude and `include_deleted` is inert on it. |

Service-level (once per service):

| Annotation | Service |
|---|---|
| `record_store {}` | the full-record store |

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

- **Asked for by a `(spec.trait)` declaration, not by the generator.** The plane is
  the record store's per-record-type, per-caller **refinable-query API**, generated
  for every `record` — but *which* proto it is emitted into, *who* implements it,
  what the emitted services are called, and *who* may call the peer half all come off
  a trait declaration rather than off generator constants. Today that declaration is
  testapp's `vservices/query.proto`:

  ```protobuf
  package query;

  option (spec.trait) = {
    name: "query"             // entitlement key AND the audience callers authenticate against
    host: "recordstore"           // the physical implementor — it is what declares (spec.record_store)
    s2s_service: "RecordMap"  // the emitted peer-facing service
    c2s_service: "RecordQuery" // the emitted client-facing service
    auth_services: ["argus"]  // peer identities allowed onto the S2S half
  };
  ```

  Delete that file and the plane is not emitted at all — no host to append to, no
  service names, no fallback. That is the paired control the generator constants it
  replaced could not have: the host used to be "whatever declares `record_store`",
  the names were the literals `RecordMap`/`RecordQuery`, and the S2S audience was
  `["argus"]` repeated at eight call sites in platform generator source, where it
  stayed for months after argus left the fleet because nothing in a build could see
  it had gone dead.

  The trait's **proto package is also `query`**, so one word is the trait name, the
  audience (`aud=query`), the proto package and the entitlement prefix (`QUERY_*`).
  The plane still covers **every** `(spec.record)` message in the fleet rather than a
  list kept in the declaration, so a new record type joins the surface by generation.

- **Two services, one plane.** `ValidateServiceSurfaces` refuses a service carrying
  two caller surfaces, so a plane serving both peers and users is emitted twice —
  `RecordMap` (`spec.service_s2s`), reached by a peer and authorized by peer identity
  through the trait's declared `auth_services`; and `RecordQuery` (`spec.service_c2s`
  + `auth_entitled_users`), reached with a user token and authorized by that token's
  entitlements. Both carry `(spec.service_*).label = "recordstore"`: the **host is whom
  the gateway dials, the trait is the audience the caller's token is checked
  against** (`routeIdentityForService`). Pinning the audience to the host would
  re-couple every client to the implementor.

- **Implemented severally, by the record store alone.** `query` has exactly one implementor
  today — not joint or ensemble hosting; timedb and graphdb went to the boneyard
  2026-08-25 and took their query planes with them. A second implementor is
  expressible and would need no client change (the audience is the contract, not the
  concrete service), but none exists, so nothing here has yet been proven to
  generalize beyond the one implementor.

- **Direct clients call it.** The C2S half is reached with a **user token** — through
  the gateway, or straight at the host's gRPC socket — carrying `aud=query` and
  `QUERY_*` grants in `service_entitlements`: the package-wide `QUERY_ANY`, the
  capability wildcards `QUERY_READ_ANY` / `QUERY_WRITE_ANY` / `QUERY_PAGE_ANY` /
  `QUERY_WATCH_ANY`, or an exact grant such as
  `QUERY_RECORD_QUERY_CREATE_TASK_ACTIVITY_QUERY`. testapp's e2e drives exactly that
  path — a logged-in user calling `CreateTaskActivityQuery` and then `ListTaskActivity`
  on `RecordQuery`, which the `query` package makes
  `query.RecordQuery/CreateTaskActivityQuery` on the wire. The S2S half's only
  declared peer is **argus**,
  which left the fleet 2026-08-20, so `RecordMap` has **no live caller today**; it is
  kept because one plane once per audience is the shape, and its audience is now a
  line in a proto that can be read and deleted rather than a literal in generator
  source.

- **The lifecycle.** A caller opens a query over one record type and gets a **handle**
  to a per-caller result set of matching record **references** (keys). The records
  themselves are **immutable** and the set is never a copy of them — the mutable thing
  is the query predicate. Both surfaces spell the ops in the **same governed verbs**
  (the S2S half was `Open`/`Refine`/`Count`/`Sample`/`Pull`/`Close` until the
  2026-08-24 rename and is not any more):

  | RPC | emitted for | meaning |
  |---|---|---|
  | `Create<T>Query(Open<T>QueryRequest) → QueryHandle` | every `T` | open a snapshot or living query; the lifecycle's single entry point |
  | `Update<T>Query(Refine<T>Request) → QueryStats` | filterable `T` | replace the predicate outright; `size` is the new predicate's match count (min_group_size-guarded) |
  | `Get<T>Count(QueryHandle) → RecordCount` | every `T` | size only, never record data (min_group_size-guarded) |
  | `Get<T>Sample(Sample<T>Request) → Sample<T>Response` | every `T` | seeded monte-carlo draw of `n` records — cheap exploration without reading everything |
  | `List<T>(Pull<T>Request) → Pull<T>Response` | every `T` | paged read of all matches (`method_collection` PAGE) |
  | `Watch<T>(QueryHandle) → stream <T>WatchEvent` | filterable `T` | living query: snapshot, then deltas |
  | `Subscribe<T>Count(QueryHandle) → stream RecordCount` | filterable `T` | live size (min_group_size-guarded) |
  | `Delete<T>Query(QueryHandle) → QueryStats` | every `T` | close the handle |

  Only the **RPC names** are verb-governed; the request/response **messages** still
  carry the query vocabulary (`Open…QueryRequest`, `Refine…Request`, `Sample…`,
  `Pull…`). `Open<T>QueryRequest` and `Refine<T>Request` (only the two that install
  or replace a predicate) additionally carry a **`bool include_deleted`**: unset,
  the plane excludes rows with `(spec.deleted_flag)` set; `true` opts back in. It
  has nowhere to go on `Sample`/`Pull`/`Count`/`Watch` — those take only the
  already-opened handle, which is where the flag was recorded. tacplus generates
  only the field; the exclusion itself is enforced by whoever implements the
  handler (the record store, today — see "tacplus knows the typed query and its shape, not
  the query→execution transform" below).

  A query is either a **one-time snapshot** or **long-lived / living** (new
  matching records auto-join). Ops run server-side. `Update<T>Query` replaces the
  predicate outright — nothing compares the new predicate against the old, so a caller
  entitled to create is equally free to widen it; authorization is per-RPC-method, not
  per-filter. **The caller that created a handle owns it** and closes it with
  `Delete<T>Query`; the record store holds the per-caller set, and no intermediary owns handle
  lifecycle or cleanup on the caller's behalf.

  Three ops return record data — **`Get<T>Sample`, `List<T>`, and `Watch<T>`** (each
  stream item is a record) — and **all three apply egress redaction**; the caller's
  entitlements shape the released view at read time, and redaction is **never** baked
  into stored state. `Get<T>Count` and `Subscribe<T>Count` return only a size, never
  record data. tacplus knows the typed query and its shape, not the query→execution
  transform (impl-side).
  - **Egress data entitlement** is generated from field-level `(spec.redact)`: for each
    record type T with redactable fields, tacplus emits `Sanitize<T>(record, <T>Entitlement, hmacKey)`
    — a sanitized **copy** (REDACT → zeroed + indicator set, ANONYMIZE → a keyed
    per-value token). The rule is **FAIL-CLOSED**: `DROP`/`PSEUDONYMIZE` fields are
    hard floors, always applied; a `MAY_*` field defaults to its most-protective
    level (`MAY_REDACT` → REDACT, `MAY_ANONYMIZE` → ANONYMIZE) and egresses raw
    only under an explicit `RAW` level. The **generated WRAPPER** (never the
    handler, never impl code) applies it on **every record-bearing egress path —
    `Get<T>Sample`, `List<T>`, AND each `Watch<T>` snapshot item and delta** (a
    living query must not leak what a `List<T>` would redact), after the handler returns, so a
    handler physically cannot bypass it. The **policy** (which fields a caller may
    see raw) is an identity property of the caller's token: OPA stamps
    `data_grants` into the `service_entitlements` claim at token-exchange time,
    the generated `Build<T>Entitlement` maps those grants onto `<T>Entitlement`
    (a matching grant loosens the field to RAW; never below a hard floor), and
    the wrapper supplies the per-deployment ANONYMIZE secret from config
    (`grpcconfig.Config.anonymize_hmac_key`) — **mechanism in tacplus, policy in
    OPA**. The caller passes no redaction knobs. The seam is per-hop, so a future
    two-stage path (the record store→an intermediary→user) can reuse it unchanged.
  - **Minimum group size** is generated from the message-level
    `(spec.record).min_group_size = N` — a k-anonymity floor on the surfaces that return a
    size and **no rows**. The record-map injector stamps `option (spec.min_group_size) = N`
    onto `Get<T>Count`, `Subscribe<T>Count` (`RecordCount.count`) and `Update<T>Query`
    (`QueryStats.size`, the match count of the newly installed predicate — unguarded, that
    one RPC reproduces the count the guard just refused), on **both** halves of the plane.
    The **generated WRAPPER** (never the handler) refuses a size below `N` with
    **PERMISSION_DENIED**, so a second implementor of `query` cannot ship the plane
    unguarded. The refusal is an error or nothing: no sentinel count, no suppression flag,
    and the message names only the declared threshold, never the actual size. A stream is
    terminated rather than silently filtered — going quiet at exactly `N-1` would disclose
    the crossing by silence. Unset/`0` is no guard, and since `Update<T>Query` /
    `Subscribe<T>Count` are emitted only for a filterable `T`, an unfilterable record is
    guarded on `Get<T>Count` alone.
  - **`total_count` is deliberately outside that guard.** `Pull<T>Response.total_count` —
    the paged read's own size, alongside `items` and `next_page_token` — is **not**
    stamped and **not** refused: it accompanies rows the caller is entitled to and has
    just been handed, so it discloses nothing those rows do not. Neither is
    `Delete<T>Query`'s `QueryStats`, whose `size` is a hardcoded zero. "The count" that
    min_group_size withholds is therefore always a *size-only* count; a future change that
    makes a paged read return counts without rows has crossed that line.
- Combining records of **multiple types** into one working set is done **service-side**
  — the caller opens several single-type queries and aggregates the already-redacted
  results in its own memory; the record store holds no cross-type sets.

## Validations (tacplus errors)

- an `event`/`record` that declares zero producer surfaces;
- a **negative `(spec.record).min_group_size`**, or a threshold declared into a fleet
  where no `(spec.trait)` hosts a record query plane — a guard that can never refuse, and
  a guard with no guarded surface behind it (`ValidateMinGroupSizeAnnotations` in
  `pkg/internal/common/parser/min_group_size_validate.go`). Each service-wrapper generator
  (Go, Java, Rust) additionally fails on a stamped method whose guard it cannot render:
  a client-streaming rpc, or a response with no single integer size field;
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

The contract names no backing store. The record store's distribution/compute (currently
in-memory + a distributed executor; later a unified "fast-store" tier and python3
transformers) is an implementation detail, never present in proto/tacplus. Do not introduce impl names (engine names, vendors, internal
codenames) into annotations or generated output.
