# Data Search / Query Surface — Implementation Plan

Status: implementation plan (tacplus codegen only). This is a **DELTA plan**.
tacplus already generates most of a per-store query surface — the typed
RecordMap query plane, the timeseries key-query + grouped-count planes, the
projection→record hydration seam, and the `Release<T>` egress-redaction
transform. This document first pins **what already exists** (with file:line
citations) and then plans **only the gap**. Do not read this as a proposal to
rebuild the existing planes.

The entitlement/redaction integration this surface depends on is specified in
`roadmap/DATA_ENTITLEMENT_PLAN.md` (which generalizes `Release<T>` into
`sanitize`); this plan consumes that seam and does not restate it.

Grounding references (read before Phase 1):
- `pkg/proto/sys/spec.proto` — store annotations `graph_store` (`50014`, ~88),
  `timeseries_store` (`50015`, ~94), `record_store` (`50023`, ~130); message
  annotations `record` (~420), `graph_projection` (~435), `timeseries_projection`
  (~447), `graph_type` (~410); field annotations `projection_record_key`
  (`60081`, ~1374), `query_field` (`60080`, ~1483); `QueryOp` enum (~1500) and
  `QueryFieldOption` (~1507).
- `pkg/internal/proto/record_map.go` — the RecordMap record-store query plane
  (read the header comment ~43–73 in full).
- `pkg/internal/proto/timeseries_query.go` — the timeseries key-query plane
  (returns record keys; stratum hydrates).
- `pkg/internal/proto/timeseries_group_counts.go` — the grouped-count analytics
  plane.
- `pkg/internal/proto/projection_gen.go` — projection message generation +
  auto-injected `projection_record_key` field (~110, ~163, ~271).
- `pkg/internal/common/model/model.go` (~29) — `DefaultRecordKeyField = "event_id"`.
- `pkg/internal/common/parser/descriptor.go` — `query_field` parse (~2027),
  `projection_record_key` parse (~2237), store-annotation parse (~453, ~1280).
- The 5 language type generators that run the injectors in a fixed order
  (projections → RecordMap → GroupCounts → TimeseriesQuery):
  `pkg/internal/{go,python,typescript,rust,java}/types/generator.go`
  (Go ~282–291; peers cited in §11).
- `cmd/soundness/main.go` (~1320) — the `storeVendorForbidden` gate.
- `docs/THEORY.md`, `docs/DEVELOPMENT.md` — conventions.
- `roadmap/DATA_ENTITLEMENT_PLAN.md` — the `Release<T>`→`sanitize` seam.

---

## 1. What already exists

tacplus already emits, per store, a typed query surface. **The tacplus/impl
boundary is fixed and identical across all of it: tacplus types the query and
its shape; the store service converts the typed query into its own
application-specific execution (SQL/DQL/whatever). tacplus never emits SQL or DQL
and never learns the query→execution transform** (`record_map.go:62–64`). The
generated services are S2S and reached through **`argus`**, the C2S front — the
`service_s2s` option scopes them to `auth_services: ["argus"]`
(`record_map.go:321`, `timeseries_query.go:135`, `timeseries_group_counts.go:172`).

### 1.1 Record store (stratum) — RecordMap query plane

`AppendRecordMapServiceToProtos` (`record_map.go:89`) scans every `(spec.record)`
message and appends a `RecordMap` service to every `record_store` proto. Per
record type `T` it emits (`record_map.go:320–346`):

- `Open<T>Query(Open<T>QueryRequest) → QueryHandle` — snapshot or living query;
  request carries the initial `<T>Filter` + a `standing` flag (`record_map.go:250`).
- `Refine<T>(Refine<T>Request) → QueryStats` — **narrow-only** predicate mutation
  (`record_map.go:338`; the broaden/union TODO is at `record_map.go:71`).
- `Count<T>(QueryHandle) → RecordCount`.
- `Sample<T>` (seeded monte-carlo draw) and `Pull<T>` (paged, `COLLECTION_MODE_PAGE`,
  `order_by` = record key) (`record_map.go:326–333`).
- `Watch<T>(QueryHandle) → stream <T>WatchEvent` (`COLLECTION_MODE_WATCH`),
  `Subscribe<T>Count`, `Close<T>` (`record_map.go:339–342`).

The typed filter `<T>Filter` is generated from `(spec.query_field)` fields
(`record_map.go:231–246`): `EQ` → `optional <type> <name>`; `RANGE` →
`<name>_from`/`<name>_to`. The caller never writes a raw query. `QueryOp` today
is exactly `{EQ, RANGE}` (`spec.proto:1500`).

**Egress redaction already wired.** `Sample`/`Pull`/`Watch` all return record
data and all apply egress redaction: the store calls the generated
`Release<T>(record, policy, pseudonymKey)` with a per-caller policy before
returning; the released view is never stored (`record_map.go:57–64`). This is the
seam `DATA_ENTITLEMENT_PLAN.md` generalizes into `sanitize`.

**Record-key validation is fail-loud.** `validateRecordKeyExists`
(`record_map.go:79`) rejects a `(spec.record).key` naming a nonexistent field.

### 1.2 Timeseries store (timedb) — key query + grouped counts

`AppendTimeseriesQueryServiceToProtos` (`timeseries_query.go:42`) appends a
`TimeseriesQuery` service to every `timeseries_store`. Per `timeseries_type`
projection it emits `List<P>(List<P>Request) → List<P>Response`
(`timeseries_query.go:134–144`), paged, over a `TimeRange`. Critically, the
response carries **`repeated <keyType> record_keys`, not rows**
(`timeseries_query.go:116–131`): a timeseries query is a **federation index**,
and **stratum hydrates the full immutable records from the record_store**
(`timeseries_query.go:19–23`). The order/return key is the projection's
`projection_record_key` field (`timeseries_query.go:72–83, 174`).

`AppendTimeseriesGroupCountsServiceToProtos` (`timeseries_group_counts.go:54`)
emits, per projection with `CARDINALITY_LOW` columns, `Get<P>GroupCounts` taking
a generated `<P>GroupField` enum (one value per low-cardinality column) + a
`TimeRange`, returning `repeated {key, count}` (`timeseries_group_counts.go:149–178`).
This is the typed fast-path for "count grouped by a low-cardinality field," so
argus stops emitting backend SQL for it (`timeseries_group_counts.go:44–53`).

### 1.3 Graph store (graphdb) — projections only, NO query surface today

**There is no generated graph query surface.** A former
`AppendGraphQueryServiceToProtos` / `graph_query.go` (a `service_s2s` paged List)
was **removed** during the store-materialization decoupling — see
`archive/STORE_MATERIALIZATION_DECOUPLING_PLAN.md:21, 170, 233`. Today a
`graph_store` proto receives only its projected node types; the fixture
(`cross-test/consumer-contract/proto/services/contractgraphdb.proto`) declares
`option (spec.graph_store) = {}` and has no query RPC. So the graphdb delta in
this plan is a **from-scratch, vendor-neutral rebuild**, not an assessment
against an existing GraphQuery. (Prose mentions of "GraphQuery" survive only in
`roadmap/DATA_ENTITLEMENT.md:11,125` and archived docs.)

### 1.4 Projection key injection (the hydration contract)

`GenerateProjectionMessages` (`projection_gen.go:26`) generates graph and
timeseries projection messages. For each, it resolves the source record key via
`sourceRecordKey` (`projection_gen.go:185`) — the record's `(spec.record).key`
or `DefaultRecordKeyField` (`model.go:29`, `= "event_id"`) — and **auto-injects**
a field annotated `(spec.projection_record_key) = true` **when the author did not
already project it** (`projection_gen.go:163–172` graph, `271–280` timeseries).
RecordMap and the projection generators share **one** `defaultRecordKeyField`
constant (`record_map.go:17–23`) so a record and its projections agree on the
key. **This is today a convention/default with an auto-inject fallback, not an
explicit enforced author-declared annotation** — Delta 3 closes that.

### 1.5 The tacplus/impl boundary and vendor neutrality

The `cmd/soundness` gate (`main.go:1320`, `storeVendorForbidden`) forbids vendor
names (`clickhouse`, `dgraph`, `dql`, `nquad`, `mysql`, `kafka`, …) and
store-query directives (`@reverse`, `@upsert`, `CREATE TABLE`) anywhere in the
tree. Everything below must stay inside this boundary.

---

## 2. Decided scope, boundary, and consumer model

- **tacplus generates the typed query TYPE + the generated surface + hands the
  query to the store service, which converts it to application-specific
  SQL/DQL/etc.** tacplus never emits SQL/DQL and stays vendor-neutral (soundness
  gate, §1.5).
- **Persistence, replay, and dashboards are NOT tacplus's job** — a downstream
  service owns them. tacplus's obligation is that the query type is a
  **serializable, parameterizable artifact** a service can persist and replay.
  tacplus provides no persistence/replay/dashboard machinery.
- **Consumers: agents and dashboards.** Agents author queries (build-time
  built-ins, runtime-persisted/config-loaded, or live co-authoring); dashboards
  replay them without the agent in the loop. The authoring agent **freezes the
  query structure**; the viewer supplies **runtime bind-parameters** (time
  window, filter literals, page size). Because an LLM authors queries, the type
  must be **typed + validatable** against the annotated-searchable schema — no
  raw-SQL passthrough string.
- **Three surfaces, one composition.** Each store keeps its own per-store query
  type (record / graph / timeseries), usable standalone (e.g. a direct count).
  Above them sits a **unified stratum query message** that composes per-store
  sub-queries. **Stratum is holder-of-truth (full records) and orchestrator**: it
  searches itself and/or fans out to graphdb/timedb, **merges result sets**, and
  **hydrates the projections those two return into full records**. graphdb/timedb
  hold projections only.
- **Entitlement integration already exists** via `Release<T>` (→ `sanitize`,
  `DATA_ENTITLEMENT_PLAN.md`): search results are egress-redacted per-caller.
  Keep using it; the composed stratum result hydrates to full records and then
  sanitizes exactly as RecordMap does today.

---

## 3. Delta 1 — Unified stratum query type (compose + merge + hydrate)

**Gap.** Today each plane is independent and only **timeseries→record**
hydration exists (`timeseries_query.go:19–23`). There is no message that
composes per-store sub-queries, no general cross-store **merge**, and no
**graph→record** hydration path.

**Design.** A new injector `AppendStratumQueryServiceToProtos` runs on every
`record_store` proto (stratum is the record store), emitting a `StratumSearch`
service. The composed request references the existing per-store request types by
composition, not by duplication:

```proto
// Auto-generated stratum composite search (tacplus). Stratum is holder-of-truth:
// it fans out to the referenced sub-queries, merges their record-key sets, and
// hydrates full records from itself.
message StratumSearchRequest {
  // Zero or more per-store sub-queries. Each names a source and carries that
  // store's own generated request. Absent sub-queries are not consulted.
  repeated StratumSubQuery sub_queries = 1;
  StratumMerge merge = 2;          // how key-sets combine (see below)
  BindParams bind = 3;             // §4 runtime bind-parameters
  int32 page_size = 4;
  string page_token = 5;
}

// A stratum query targets EXACTLY ONE record type T. Every sub-query below is a
// projection *of that same T* (or T's own RecordMap filter); each contributes a
// key set over T. Blending two different record types into one result set is not
// allowed — those are separate typed queries.
message StratumSubQuery {
  oneof source {
    // record sub-query: a RecordMap filter over a record type T (typed, per §5.1).
    RecordSubQuery record = 1;
    // timeseries sub-query: an existing List<P>Request over a projection.
    TimeseriesSubQuery timeseries = 2;
    // graph sub-query: a GraphQuery traversal (Delta 5.3), returns record keys.
    GraphSubQuery graph = 3;
  }
}

enum StratumMergeOp {
  STRATUM_MERGE_OP_UNSPECIFIED = 0; // single sub-query: pass through
  STRATUM_MERGE_OP_INTERSECT  = 1;  // AND of record-key sets
  STRATUM_MERGE_OP_UNION      = 2;  // OR of record-key sets
}
message StratumMerge { StratumMergeOp op = 1; }
```

**Single-type rule.** A stratum query targets **exactly one** record type `T`.
The graph and timeseries projections *of that record type* contribute key sets;
stratum intersects/unions those keys and hydrates to a typed `Set<T>` of full
records. Every result set is **one concrete generated type, fully typed — never
`google.protobuf.Any`**. Blending two different record types (`A` and `B`) into a
single result set is not allowed; those are separate typed queries. Direct
graphdb/timedb calls (§7) likewise return their own single-type typed projection
sets.

**Hydration contract.** Every sub-query result is reduced to a set of **parent
record keys** (`projection_record_key` for graph/timeseries; the record's own key
for record sub-queries). Stratum merges the sets per `StratumMerge`, then
**hydrates** each surviving key into a full record from its own store, then
applies `sanitize`/`Release<T>` per caller (§8). The response is therefore the
typed record type `T` of the query, not `Any`:

```proto
// Emitted per record type T, so the response is a concrete typed set of T.
message StratumSearch<T>Response {
  repeated <T> records = 1;         // hydrated + sanitized full records of T
  string next_page_token = 2;
  int64 total_count = 3;
}
```

tacplus types the composition and the merge/hydrate contract; **stratum's impl
executes the fan-out and the actual store joins** — consistent with the existing
tacplus/impl boundary (§1.5).

**Hook point.** New `pkg/internal/proto/stratum_query.go`; wired into all 5
`generator.go` files immediately after `AppendRecordMapServiceToProtos` (it
depends on RecordMap's `<T>Filter` types being present) and after the timeseries
injectors (it references their request types).

---

## 4. Delta 2 — Serializable + parameterizable query + bind-params

**Gap + tension.** RecordMap today is **handle/session-oriented**: you
`Open<T>Query` to get a `QueryHandle`, then `Refine`/`Pull`/`Watch` against that
live server-side handle (`record_map.go:203–205, 49–55`). A handle is a live
session id, **not a serializable, replayable artifact** — a dashboard cannot
persist a `QueryHandle` and replay it later against a different server, and an
agent cannot freeze a query to config. The consumer model (§2) requires a
standalone serializable query message.

**Reconciliation (decision).** Keep the handle plane as-is for **living/refinable
sessions** (its Watch/Refine/Subscribe semantics genuinely need server-side
state). Introduce a **standalone serializable query message** as the persisted
artifact, and make the handle plane *derivable from it*:

- The serializable artifact is the request bodies themselves — `<T>Filter`,
  `List<P>Request`, `StratumSearchRequest` — which are already plain proto
  messages with no server state. Formalize this by making `Open<T>QueryRequest`
  and `StratumSearchRequest` the **canonical persisted query**: a service persists
  the request bytes, and replay = re-issue `Open<T>Query`/`StratumSearch` with the
  same bytes but fresh bind-params. **No new "query id" indirection is added to
  tacplus**; persistence is the downstream service's job (§2).
- This means: **structure lives in the request message (frozen by the authoring
  agent); values that vary per-viewer live in bind-params.**

**Bind-parameter model.** Add a `BindParams` message carried by
`Open<T>QueryRequest` / `List<P>Request` / `StratumSearchRequest`. A bind-param is
a typed named hole the author leaves open and the viewer fills at run time:

```proto
message BindValue {
  oneof v { string s = 1; int64 i = 2; double d = 3; bool b = 4; bytes by = 5;
            google.protobuf.Timestamp ts = 6; }
}
message BindParams {
  map<string, BindValue> values = 1; // name → viewer-supplied literal
}
```

Filter/time-range/page-size fields may reference a bind name instead of a literal
(a `bind_ref` string on the typed filter slot — the author writes
`spiffe_id = $viewer_spiffe`, freezing the *field* while leaving the *value*
open). Validation (§10) rejects a `bind_ref` whose type does not match the target
field's proto type, and rejects an unbound ref at replay. **The authoring agent
freezes which fields are queried and how they combine; the viewer only supplies
values.** This keeps LLM-authored queries typed and validatable while letting a
dashboard replay with a new time window.

**Hook point.** `BindParams`/`BindValue` are emitted once per store proto (shared
messages) alongside the RecordMap shared messages (`record_map.go:202–222`);
`bind_ref` slots are added to the `<T>Filter` generator (`record_map.go:231–246`).

---

## 5. Delta 3 — Explicit, enforced parent-key annotation on projections

**Gap.** Today the parent-record key on a projection is an **auto-injected
default/convention** (`projection_gen.go:163–172, 271–280`; §1.4). If the author
projects a field that *happens* to collide, or the default `event_id` is wrong
for a record with a non-default key that the author forgot to declare, the
projection can silently key on the wrong field. There is no annotation that says
"this projection field IS the parent record's key," author-declared and enforced.

**Design.** The parent-key on a projection is **mandatory** and enforced at **two
levels (both fail-loud)**: definition time (tacplus generation) and runtime (the
generated hydrate/merge code). A projection with no populated parent-key cannot be
hydrated or merged, so neither level tolerates its absence.

Promote `projection_record_key` from an auto-injected artifact to a **first-class,
author-declarable, generation-enforced** annotation:

1. Author may mark exactly one projection field `(spec.projection_record_key) =
   true` (the annotation already exists, `spec.proto:1374`, and is parsed,
   `descriptor.go:2237`).
2. **Definition-time enforcement (new).** Every projection MUST define the key
   field that carries the parent record's `(spec.record).key`. In
   `GenerateProjectionMessages`, after building each projection, assert **exactly
   one** `projection_record_key` field exists AND its `projection_source` resolves
   to the parent record's `(spec.record).key`. If it is undefined tacplus **HARD
   FAILS at generation** — fold this into the generation/soundness guard, mirroring
   `validateRecordKeyExists` (`record_map.go:79`):
   - zero parent-key fields → error "projection P declares no parent-key field";
   - more than one → error "projection P declares N parent-key fields, want 1";
   - the marked field's source ≠ parent `(spec.record).key` → error.
3. **Runtime enforcement (new).** The generated hydrate/merge code MUST verify the
   parent-key field is **populated** on each projection instance it consumes; if
   the key is unset it raises a **runtime EXCEPTION** (a projection with no key
   cannot be hydrated or merged). This backstops the definition-time guard against
   an instance that type-checks but arrives with an empty key.
4. Keep the auto-inject as a **back-compat convenience** but route it through the
   same definition-time guard so the invariant ("every projection has exactly one
   parent-key field mapping to the parent record key") holds whether the field was
   authored or injected. **The invariant, not the injection, is what's enforced.**
5. Add a `cmd/soundness` probe asserting every `graph_projection` /
   `timeseries_projection` in the tree is provably hydratable (has the one
   parent-key mapping). This makes "every projection is mergeable/hydratable" a
   checked property, which Delta 1's merge relies on.

**Hook point.** `pkg/internal/proto/projection_gen.go` (new
`validateProjectionParentKey` called from `generateGraphProjection` /
`generateTimeseriesProjection`); `cmd/soundness/main.go` new probe.

---

## 6. Delta 4 — Broaden / union for Refine

**Gap.** `Refine<T>` is **narrow-only** (`record_map.go:338`; TODO at
`record_map.go:71`). A living query can only tighten; a dashboard cannot widen a
standing query's predicate without opening a new one.

**Assessment.** Narrow-only is a real semantic simplification: a narrowed set is
a subset of the prior set, so the server can filter in place. Broaden requires
re-scanning the source (the widened set may include records the handle previously
excluded), so a broaden is closer to "re-open with a looser predicate." Two
options:

- **(a) Broaden as re-open (recommend).** Do **not** add a mutable broaden to the
  handle; instead the serializable query artifact (Delta 2) is re-issued with a
  looser predicate — replay *is* the broaden. This keeps the handle's narrow-only
  invariant (cheap in-place filtering) and reuses Delta 2's machinery. Lowest new
  surface.
- **(b) True in-place broaden.** Add `Broaden<T>(Broaden<T>Request) → QueryStats`
  taking a `<T>Filter` interpreted as a **union** with the current predicate. This
  requires the store to re-scan; the generated surface is symmetric with `Refine`
  but the impl cost is higher and it complicates Watch (the snapshot must be
  re-emitted).

**Recommendation:** ship (a) — document broaden as re-issue of the serializable
query — and leave (b) as an opt-in future if a living-query consumer proves it
needs in-place widening. Either way, remove the standalone TODO at
`record_map.go:71` and replace it with a pointer to this section's decision.

---

## 7. Delta 5 — Per-store query vocabularies (needs-driven, vendor-neutral)

**Do NOT model all of SQL/DQL.** Scope each vocabulary to the stated demand:
dashboards (filter / order / paginate / select / top-N) and agents (analytics
like "unique users who sent prompt X in window Y").

### 7.1 Common predicate core (record + shared)

Today `query_field` supports only `EQ`/`RANGE` (`spec.proto:1500`). Extend the
`QueryOp` enum and the `<T>Filter` generator (`record_map.go:231–246`) with the
needs-driven predicates:

- `QUERY_OP_IN` (repeated literal / bind list), `QUERY_OP_CONTAINS` (substring on
  string), `QUERY_OP_IS_NULL` / `QUERY_OP_IS_NOT_NULL`, boolean `QUERY_OP_EQ` on
  bool fields (already expressible).
- **Boolean AND/OR/NOT tree.** Today a `<T>Filter` is an implicit AND of set
  fields. Add an optional `<T>FilterExpr` — a recursive `{ op: AND|OR|NOT,
  children: [...] , leaf: <T>Filter }` — so the author can express OR/NOT. The
  flat `<T>Filter` stays as the common case; the tree is opt-in.
- **Order-by + field selection + paging.** Paging already exists
  (`COLLECTION_MODE_PAGE`, `record_map.go:329`). Add an `order_by` list
  (field + direction, restricted to `query_field`-annotated fields) and a
  `select` field-mask (restrict returned fields — composes with `sanitize`).
- **Bind-params** (§4) apply to every leaf literal.

Each new `QueryOp` is a typed enum value converted by the store into its own
predicate; **no operator maps to a vendor directive** (soundness, §1.5).

### 7.2 Timeseries aggregation vocab (generalize group_counts)

Today only grouped **count** exists (`timeseries_group_counts.go`). Generalize to
an aggregation vocab over projection columns, needs-scoped to dashboards/agents:

- Aggregations: `COUNT`, `COUNT_DISTINCT` (this is the "unique users" case),
  `SUM`, `AVG`, `MIN`, `MAX`, `PERCENTILE(p)`.
- `GROUP BY` one or more low-cardinality columns (the existing `<P>GroupField`
  enum already enumerates the safe group targets, `timeseries_group_counts.go:149`).
- A `TimeRange` (already present) + optional predicate reusing §7.1 leaves over
  the projection's columns.

Generalize `Get<P>GroupCounts` into `Get<P>Aggregate(Get<P>AggregateRequest)`
carrying `{ agg: AggOp, agg_column: <P>Column, group_by: repeated <P>GroupField,
time_range, predicate }` → `repeated { key, value }`. Keep `Get<P>GroupCounts` as
a thin alias (`agg = COUNT`) for back-compat, or regenerate call sites — **open
question §12**.

### 7.3 Graph traversal vocab (from scratch — see §1.3)

Because no GraphQuery exists today, build a minimal vendor-neutral traversal
surface, needs-scoped. New injector `AppendGraphQueryServiceToProtos` on every
`graph_store`, emitting per graph node type `N` (the `graph_type` messages,
`projection_gen.go:99–108`):

- `node-match`: filter nodes by indexed predicate (reuse §7.1 leaves over the
  node's `(spec.graph_index)` fields).
- `edge` traversal: by edge type (`spec.graph_edge`, `projection_gen.go:136`),
  direction (out/in), and **bounded depth** (`max_depth`, required — no unbounded
  traversal).
- `neighbors(N, edge, depth)` and `path_exists(from, to, edge, max_depth)`.
- Result is **record keys** (via the node's `projection_record_key`,
  Delta 3-enforced) so a graph sub-query hydrates through stratum exactly like
  timeseries (§3). graphdb never returns full records.

```proto
message GraphNodeMatch { NFilter filter = 1; }        // §7.1 leaves over indexed fields
message GraphTraversal {
  GraphNodeMatch start = 1;
  string edge_type = 2;                                // (spec.graph_edge) value
  enum Dir { DIR_OUT = 0; DIR_IN = 1; } Dir direction = 3;
  int32 max_depth = 4;                                 // required, bounded
}
message GraphQueryResponse { repeated string record_keys = 1; string next_page_token = 2; }
```

**Hook point.** New `pkg/internal/proto/graph_query.go`, wired into all 5
`generator.go` files after `GenerateProjectionMessages` (it needs the generated
`graph_type` node messages) — the same slot the removed injector used
(archive ref, §1.3).

---

## 8. Entitlement / `Release<T>` (sanitize) integration

Search adds no new egress path — it reuses the existing one. Every surface that
returns record data applies per-caller egress redaction:

- RecordMap `Sample`/`Pull`/`Watch` already call `Release<T>`
  (`record_map.go:57–64`); unchanged.
- **Delta 1 stratum hydration** hydrates to full records, then applies
  `Release<T>` / `sanitize` (`DATA_ENTITLEMENT_PLAN.md` §4) to each hydrated
  record before returning — identical to RecordMap.
- graphdb/timedb return **only record keys**, never record bodies, so they need
  no egress redaction (the key itself is a `query_field`/index value; if a key is
  sensitive it is redacted at hydration by stratum, not at the projection store).

Align the composed result path with `DATA_ENTITLEMENT_PLAN.md`: as `Release<T>`
generalizes to `sanitize(m, entitlement) → (m', redactedFieldIds)`, the stratum
hydrator returns the union of `redactedFieldIds` alongside the records. `select`
field-masks (§7.1) compose with `sanitize` (mask first, then sanitize the
remaining fields).

---

## 9. Vendor neutrality + soundness gate

Every generated artifact above is a **typed proto** (enums, filter messages,
service RPCs). No emitter may contain a vendor name or a store-query directive —
`cmd/soundness` (`main.go:1320`, `storeVendorForbidden`) already forbids
`clickhouse`/`dgraph`/`dql`/`mysql`/`kafka`/`@reverse`/`@upsert`/`CREATE TABLE`
and matches case-insensitively on word boundaries. New enum values (`QUERY_OP_*`,
`AggOp`, graph `Dir`) and message names must use store-neutral vocabulary
(`INTERSECT`/`UNION`/`NEIGHBORS`/`PATH_EXISTS`, not any vendor's operator name).
Add soundness probes for: (a) Delta 3's projection-hydratability invariant; (b)
each store's query service is emitted for the corresponding `*_store` annotation.

---

## 10. Generation-time validations (all fail loud)

- **Record-key exists** — already enforced (`record_map.go:79`).
- **Projection parent-key** (Delta 3): mandatory, enforced at two levels. Def
  time — exactly one `projection_record_key` per projection, mapping to the parent
  `(spec.record).key`; undefined → tacplus HARD FAILS at generation. Runtime — the
  generated hydrate/merge code raises an EXCEPTION if the key is unpopulated on a
  projection instance. Both fail loud.
- **Bind-ref typing** (Delta 2): a `bind_ref` must match the target field's proto
  type; an unbound ref at replay is rejected by the generated validator.
- **Graph traversal bounded** (Delta 5.3): `max_depth` required and > 0.
- **query_field op validity** (Delta 5.1): `IN`/`CONTAINS` only on the field
  types that support them (`CONTAINS` string-only), mirroring the existing
  RANGE/EQ typing in `record_map.go:231`.
- All validators emit every violation at once (`strings.Join` pattern, per
  `redact_validate.go` style referenced in `DATA_ENTITLEMENT_PLAN.md`).

---

## 11. Language parity

The generated **query TYPE** (filters, aggregation/traversal enums, stratum
composite, bind-params) is proto and therefore emitted for **all** target
languages — the injectors run in every language's `generator.go`:
`pkg/internal/go/types/generator.go:282–291`,
`pkg/internal/python/types/generator.go:507–516`,
`pkg/internal/typescript/types/generator.go:371–380`,
`pkg/internal/rust/types/generator.go:108–117`,
`pkg/internal/java/types/generator.go:934–943`. New injectors
(`stratum_query.go`, `graph_query.go`) and the extended
RecordMap/GroupCounts generators hook into that same fixed order in all five —
**parity is structural** because the proto injection is language-agnostic.

- **C2S client surfaces** (agents/dashboards call `argus`, which fronts these S2S
  services): generated in every client language — Go, Java, Rust, Python, TS,
  Swift, C# — because the services carry `service_s2s {auth_services:["argus"]}`
  and argus re-exposes them C2S.
- **Store servers** are the Go/Java/Rust services that *implement* the
  translation (RecordMap/StratumSearch on stratum; GraphQuery on graphdb;
  TimeseriesQuery/Aggregate on timedb). The query→execution transform is
  **impl-side** and out of tacplus (§1.5).
- **Egress `sanitize`** follows `DATA_ENTITLEMENT_PLAN.md` §4.3: full-service
  Go/Rust/Java emit it; client-only TS/Python/Swift/C# treat it as a no-op.

Repo parity law (`feedback_tacplus_all_languages`): any lib primitive added for
one language lands for all in the same change; the query TYPE additions here are
proto-only, so they land everywhere by construction.

---

## 12. Phased task breakdown

Each phase is testable with `go test ./pkg/...` (generator render/regression
tests) before any downstream language build.

**Phase 1 — predicate core + bind-params (Delta 5.1, Delta 2 bind model).**
- `spec.proto`: extend `QueryOp` (`IN`/`CONTAINS`/`IS_NULL`/`IS_NOT_NULL`); add
  `BindValue`/`BindParams`; add `bind_ref` slot. Regenerate + commit checked-in
  `spec_pb` (per the spec_pb regen guard).
- `record_map.go`: extend `<T>Filter` gen (new ops, `bind_ref`), add optional
  `<T>FilterExpr` tree, `order_by`/`select`.
- `descriptor.go`: parse new ops (~2027).
- Tests: filter-render tests per op × type; bind-ref typing reject tests.

**Phase 2 — projection parent-key enforcement (Delta 3).**
- `projection_gen.go`: `validateProjectionParentKey`; route auto-inject through it.
- Generated hydrate/merge code: runtime check that raises an exception when a
  projection instance's parent-key is unpopulated.
- `cmd/soundness/main.go`: hydratability probe.
- Tests: reject zero/multiple/mismatched parent-key fixtures (generation hard-fail);
  a runtime test asserting an unpopulated parent-key raises the hydrate/merge
  exception; auto-inject still passes the guard.

**Phase 3 — timeseries aggregation vocab (Delta 5.2).**
- `timeseries_group_counts.go` → generalize to `Get<P>Aggregate`
  (`COUNT`/`COUNT_DISTINCT`/`SUM`/`AVG`/`MIN`/`MAX`/`PERCENTILE`, multi-column
  group-by, predicate); keep `Get<P>GroupCounts` alias.
- Tests: aggregate-render tests; alias round-trip.

**Phase 4 — graph query surface from scratch (Delta 5.3).**
- New `pkg/internal/proto/graph_query.go`; wire into all 5 `generator.go` after
  `GenerateProjectionMessages`.
- `spec.proto`/descriptor: any new field annotations the traversal needs
  (direction/depth are message fields, likely none new).
- Tests: node-match/neighbors/path-exists render; `max_depth` required reject.

**Phase 5 — unified stratum composite + hydration (Delta 1).**
- New `pkg/internal/proto/stratum_query.go`; wire into all 5 `generator.go` after
  RecordMap + timeseries injectors.
- Tests: composite render with record+ts+graph sub-queries over a single record
  type T; merge INTERSECT/UNION over key sets; hydration to a typed `Set<T>`
  (`StratumSearch<T>Response`), asserting no `google.protobuf.Any` in the surface.

**Phase 6 — broaden decision + docs + soundness (Delta 4 + cleanup).**
- `record_map.go`: remove the narrow-only TODO (`:71`); document broaden = re-issue.
- `docs/USER.md`/`docs/DEVELOPMENT.md`: the search vocabularies + bind-params +
  the serializable-query/replay model; note persistence is downstream.
- `cmd/soundness`: store-query-service emission probes.

---

## 13. Test strategy

- **Generator tests (local, `go test ./pkg/...`):** render/regression tests per
  injector (mirroring `record_map_test.go`, `timeseries_query_test.go`) asserting
  the exact generated proto for fixtures covering: each new `QueryOp`; the
  `<T>FilterExpr` tree; `order_by`/`select`; bind-refs; the stratum composite with
  all three sub-query kinds and both merge ops; the graph traversal surface; the
  aggregation vocab. Validator tests assert every reject path fails loud with a
  naming message. The Delta-3 hydratability soundness probe runs here.
- **Store-service translation is impl-side, out of tacplus tests** — the
  query→SQL/DQL transform lives in the Go/Java/Rust store services, tested where
  those services build, not in tacplus.
- **No local non-Go builds.** Java/Rust/Python/TS/Swift/C# compile is **CI**, per
  the repo's no-local-build convention (`feedback_no_local_builds`); the local
  gate is `go test ./pkg/...` + `cmd/soundness`. Do not `mvn`/`cargo`/`tsc`/`dotnet`
  locally.

---

## 14. Open questions for the maintainer

1. **Hydrated result type (Delta 1). RESOLVED.** Neither `google.protobuf.Any`
   nor a catalog-wide `oneof`: **one typed record type `T` per query**. A stratum
   query targets exactly one record type; its graph/timeseries projections
   contribute key sets, stratum merges over keys and hydrates to a typed `Set<T>`
   of full records. Blending two different record types into one result set is not
   allowed — those are separate typed queries.
2. **Handle plane vs serializable artifact (Delta 2).** Confirm the decision to
   keep the handle plane only for living/refinable sessions and treat the request
   messages as the canonical persisted query, adding no new query-id indirection
   to tacplus. This is the biggest tension (§4).
3. **Broaden (Delta 4).** Accept "broaden = re-issue the serializable query"
   (option a), or is an in-place `Broaden<T>` actually needed by a living-query
   consumer (option b)?
4. **Aggregation back-compat (Delta 5.2).** Keep `Get<P>GroupCounts` as a
   `COUNT` alias, or regenerate its call sites onto `Get<P>Aggregate`?
5. **`COUNT_DISTINCT` cardinality safety.** `COUNT_DISTINCT` over a
   high-cardinality column is expensive; should the aggregation vocab restrict
   `COUNT_DISTINCT`/`GROUP BY` to `CARDINALITY_LOW` columns only (as
   group_counts does today), or allow it with a warning?
6. **Bind-param scope.** Are bind-params only literals (recommended), or may an
   author leave a whole predicate/branch bindable (structure-level holes)? The
   consumer model says the author freezes structure — recommend literals-only.
7. **Merge semantics on paged results (Delta 1).** INTERSECT/UNION over
   *paged* sub-query key-sets needs the impl to page the merge; is a bounded
   materialization ceiling needed in the type, or left entirely to impl?
