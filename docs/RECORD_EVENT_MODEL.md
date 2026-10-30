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

Since 2.5 a record also has a **direct path**: the store's generated write surface
(below), `service → record store`, synchronous and final. A store that takes rows only
that way declares `(spec.record_store) = {no_bus_ingest: true}` and derives no bus
subscription; a record is still an event in 2.5 (removing the bus path for records is a
major change).

## Hand-written annotations (the entire surface)

Message-level:

| Annotation | Meaning |
|---|---|
| `event { c2s{…}? , s2s{…}? }` | eventbus **topic + publish RPC**. ≥1 producer surface required. |
| `record { key, owner, c2s{…}? , s2s{…}? , residency{…}? , s2s_write{…}? , durability? }` | **DELIVERY.** **is-an `event`** + stored in `record_store` + projectable. `key` = id field (default: `event_id`), `owner` = ownership field, `residency` = the store's lifecycle as two clocks — `idle_seconds` (memory eviction on no read/write) and `deleted_max_age_seconds` (purge this long after `(spec.deleted_flag)` was set). Unset residency is a live platform default (`parser.DefaultResidencyIdleSeconds`/`DefaultResidencyDeletedMaxAgeSeconds`), never "never". `s2s_write` = who may write the stored rows through the store's write surface (falls back to the trait-wide audience, like `s2s_read`); `durability` = `DURABILITY_MEMORY` \| `LOCAL` (default) \| `REMOTE`, the level a write of the type is acked at (see "Write surface"). |
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
| `record_store { no_bus_ingest? }` | the full-record store; `no_bus_ingest: true` = rows arrive only through its write surface, no EventBus subscription is derived |

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
- The **write surface** and **Watch** (below), on the query plane's S2S half.

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

## Write surface, durability and Watch (2.5)

The record store hosts a generated **write surface** beside the query plane, emitted by
the same injector (`pkg/internal/proto/record_write_surface.go`) into the same trait
proto, on the plane's **S2S half only** (no C2S writes in 2.5: a user-token write needs an
owner-pin rule for writes, which is its own decision). Every language gets the clients
from the injected proto; Go, Java and Rust also get the store-side handler interface and
the guarding wrapper.

| RPC | emitted for | the store answers |
|---|---|---|
| `Get<T>(Get<T>Request{key}) → {item, revision}` | write-capable `T` | `NOT_FOUND` when absent |
| `Put<T>(Put<T>Request{item}) → RecordWriteResult` | write-capable, not APPEND_ONLY | done |
| `InsertIfAbsent<T>(…{item}) → RecordWriteResult` | write-capable `T` | `ALREADY_EXISTS` when present |
| `ReplaceIfUnchanged<T>(…{item, expected_revision \| expected_item}) → RecordWriteResult` | not APPEND_ONLY | `ABORTED` when changed underneath, `NOT_FOUND` when absent |
| `Delete<T>(…{key}) → RecordWriteResult` | not APPEND_ONLY | `NOT_FOUND` when absent |
| `Watch<T>Changes(Watch<T>ChangesRequest{from_revision}) → stream <T>WatchEvent` | every non-private `T` | snapshot then changes (`from_revision` 0), or the tail after it |
| `Apply(RecordApplyRequest{ops, durability}) → RecordApplyResponse{results}` | once, when any `T` is write-capable | all ops or none, refused with the failing op's reason |

*Write-capable* = not `private`, and keyed by a string or integer field. The answer is
**final and synchronous**: done, or refused with the reason. `RecordWriteResult` carries
the entry's new `revision` and the `durability` level the write was acked at.

- **Locks are internal to the store** (decision D1-B). No RPC takes or holds a lock across
  calls. Each write takes its entry's lock inside the store, including on an absent key
  for `InsertIfAbsent`; `Apply` takes every entry lock its ops name, all or nothing, in
  the **canonical order** — record type full name, then key (strings by UTF-8 bytes,
  integers numerically). The order is part of the contract: `Apply`'s ops must be listed
  in it, strictly ascending, each entry once, and the generated wrapper refuses anything
  else with `INVALID_ARGUMENT`. So every implementation takes the same locks in the same
  order and two `Apply`s cannot deadlock. A map lock is the store's own business, taken
  for a whole-type operation; it has no RPC.
- **Revision** (D6). A store-assigned `int64`, increasing per write of an entry, returned
  by every write and `Get<T>`. `ReplaceIfUnchanged` compares against it or against the
  stored value. It is `int64` because it is the revision the generated WATCH collection
  keys its replay ring by and resumes from.
- **Durability**, declared per type by `(spec.record).durability`: `MEMORY` (acked in
  memory on the store's members; lost on a full restart), `LOCAL` (the default: acked
  once the local redo has it on disk), `REMOTE` (acked once the local and the
  cross-region redo have it). It is a floor. The injector stamps it on every write
  (`(spec.record_entry)`) and on each `Apply` op case (`(spec.record_op)`), and the
  generated wrapper, never the handler: refuses a write asking for less
  (`INVALID_ARGUMENT`); hands a write asking for nothing to the handler at the floor; acks
  an `Apply` at the highest level any of its ops involves; and refuses to answer a write
  the store reports acked below the level it owed (`INTERNAL`). It is new and additive,
  and unrelated to `(spec.quorum_write)`/`(spec.min_durable_copies)`, which govern the
  diskstore quorum gate on state/store fields (D4).
- **Watch** is first-class on every non-private record type: `Watch<T>Changes` is the
  record's change feed, unfiltered, in the generated WATCH collection shape (snapshot
  items, `snapshot_done`, then deltas `{type, item, revision}` with egress redaction
  applied like `Watch<T>`). The store feeds it only **acked** writes, so a change is seen
  only after its write's declared ack. A resume point older than the store retains
  answers `OUT_OF_RANGE`; the caller re-watches from 0 (snapshot then tail).
- **Who may call** (D3). Writes: `(spec.record).s2s_write`; `Get<T>`: `s2s_read`;
  `Watch<T>Changes`: `s2s_subscribe` minus its routing-only services. Each falls back to
  the trait-wide `auth_services`. `Apply`'s audience is the union of the write audiences,
  and the wrapper authorizes each op exactly as its single-type RPC.
- **Tenant.** For a record with a `(spec.store_namespace)` field, a caller held to a
  tenant is pinned to its own tenant on every request of the surface: the
  row's field on a write carrying a row, a tenant field beside the key on `Get<T>`,
  `Delete<T>` and `Watch<T>Changes`. Unset is stamped, anything else refused; a
  `/system` peer keeps the fleet-wide reach. The store answers a key-addressed request
  only for an entry in the named tenant.
- **A tenant-owned store** (#207). A record store declaring `(spec.service).tenant_owned`
  is one instance per tenant, so its plane is routed like any surface of a tenant-owned
  service: every request of both halves carries `target_tenant` (field 15), every rpc
  declares it as its route field, and the generated clients pool the store per tenant.
  The instance refuses a call routed to another tenant, and an entry whose tenant field
  is not in `/tenants/<target_tenant>` or below, whoever the caller is, before the
  handler, so a misrouted write never reaches the store or its redo.
- **The redo contract** (D5). The record store's durability is the redo service (the log
  is the database), through the frozen sys contract `sys/redo.proto`, `redolog.Redo`:
  defined by tacplus, hosted by the redo service at label `redo`, called by the store
  (`auth_services: ["stratum"]`) through its generated client, so neither side
  hand-writes it.
  - **Routing.** The redo service is tenant-owned, in the reserved `restore` tenant, and
    the store generates without its proto, so the contract declares `host_tenant_owned`
    and both rpcs route by `tenant` (#170): the redo instance's tid, a tenant-to-tenant
    call, with `RPC_FLAG_ANY_TENANT`. The tenant a write **belongs to** is never a request
    field: it is the caller's identity (`/tenants/<id>`), and the redo service keys every
    log by trust domain and that tenant.
  - **`Append`** (bidi). Each message is one store operation, a single write or a whole
    `Apply`, which the log keeps all or nothing: its `changes` (record type full name,
    typed key, `PUT`/`DELETE`, the record's protobuf bytes, the revision the store
    answered) in canonical lock order, the `durability` it is owed, and `seq`, the
    tenant log's position: 1 for an empty log, then exactly one more each time, any
    other refused (`FAILED_PRECONDITION`), so a replay is exact. The redo service
    answers one `RedoAck{seq, level}` per level reached, in order: `LOCAL` once on local
    disk, `REMOTE` once the second site has it. The store applies a write, answers it,
    and feeds `Watch<T>Changes` only after the ack it owes; while the redo service is
    unreachable it refuses writes. `MEMORY` writes are never appended (an append asking
    for it is `INVALID_ARGUMENT`).
  - **`Replay`** streams the caller's tenant log, the compacted snapshot (one `PUT` per
    live entry, `from_snapshot`) then the tail in `seq` order, and ends. A store replays
    at every start, before it serves; its next `seq` follows the last one replayed.
  - The generated surface fixes the shape, the routing and who may call. Ordering, the
    ack levels, compaction and tenant isolation are the redo service's to implement and
    test (tacplus-dev/redo#1); applying after the ack is the store's.

## Validations (tacplus errors)

- an `event`/`record` that declares zero producer surfaces;
- a `(spec.record).s2s_write` that names no service, sits on a `private` record, or has no
  `(spec.trait)` hosting the plane behind it (`ValidateRecordWriteAnnotations`), or sits on
  a record whose key is not a string or integer field;
- record names that compose to one RPC name twice on a plane half (`Delete<T>` beside
  `Delete<U>Query` for a record `U` with `T = UQuery`), or to the same `RecordOp` field
  number block (derived from each type's full name, so a writer and a store built from
  different record sets agree);
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
  `pkg/internal/common/boundaries/boundaries.go`). A store declaring
  `no_bus_ingest` derives no subscription, so this check does not apply to it.

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
