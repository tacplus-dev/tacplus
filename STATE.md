# State: tacplus

Overwritten to stay current (loopers and agents). Last updated: 2026-10-28 by looper (#163).

- **Version / line:** v2.4.1 (latest release, 2026-10-27); main is the 2.5 line in progress (this repository is the compiler; it declares no tacplus floor)
- **Works:** in 2.4 — the global registry and the version floor (#161). On main for 2.5 — the record store's
  generated write surface on the record query plane's S2S half (Get/Put/InsertIfAbsent/ReplaceIfUnchanged/Delete,
  Watch<T>Changes, the atomic Apply with canonical lock order), per-record-type durability (MEMORY/LOCAL/REMOTE),
  `(spec.record).s2s_write`, `(spec.record_store).no_bus_ingest` (#163); Go, Java and Rust wrappers guard durability,
  tenant and Apply.
- **Released / deployed:** v2.4.1 is the newest release; 2.5 is unreleased.
- **Known gaps:** the record store ↔ redo contract (`sys/redo.proto`, #163 D5) is not built yet — it waits for #170's
  target-tenant route field. No C2S record writes (by decision, 2.5). The Rust wrapper renders no
  (spec.store_namespace) predicate pin on the query plane (PROPOSALS.md).
- **Blocked on:** #170 (for the redo contract of #163).
