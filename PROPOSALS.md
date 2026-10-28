# Proposals: tacplus

Written by loopers. Each proposal: what, why, links. Jody promotes accepted ones into ROADMAP.md (via an agent);
the looper then marks it `promoted` or `declined` here with the date.

## Open
- **Rust: the (spec.store_namespace) predicate pin on the record query plane** (2026-10-28, #163): Go and Java pin a
  tenant-held caller's query predicate to its own tenant before the store is asked (`goPredicateNamespacePin`,
  `javaPredicateNamespacePin`); the Rust wrapper renders none (cross-test/current-generation/run.sh says so for the
  principal pin). A /tenants peer's foreign-tenant predicate then reaches the store and trips the egress abort
  backstop instead of being refused. The record write surface carries its own tenant pin in all three languages
  (#163), so writes are covered; reads through Create/Update<T>Query and Get<T>Rows are not, in Rust only.
- **A generated helper that orders Apply's ops** (2026-10-28, #163): Apply refuses ops that are not in canonical lock
  order (record type full name, then key), so every caller sorts by hand. A generated client-side
  `SortRecordOps`/equivalent per language would remove that tax; it must use the same comparison as the wrapper
  (UTF-8 bytes for string keys, numeric for integer keys).
- **Records written only through the store still declare a bus surface** (2026-10-28, #163 D2): in 2.5 a record is
  still an event, so it must declare `c2s`/`s2s` even when its store declares `no_bus_ingest` and nothing publishes
  it. Removing the bus path for records (D2-B) is a major change: propose it for 3.0 with `s2s_write` counting as the
  record's producer surface.
- **A runtime gate against a real record store** (2026-10-28, #163): the write surface's guards are driven in-process
  against each language's generated wrapper with a fake handler (current-generation host leg). The store-side
  semantics the handler owns (InsertIfAbsent/ReplaceIfUnchanged answers, lock order, ack-before-watch) have no
  generated-code gate; Stratum's own tests are where they live today.

## Promoted / declined
