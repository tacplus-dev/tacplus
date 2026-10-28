# State: tacplus

Overwritten to stay current (loopers and agents). Last updated: 2026-10-28 by looper (#163).

- **Version / line:** v2.4.1 (latest release, 2026-10-27); main is the 2.5 line in progress (this repository is the compiler; it declares no tacplus floor)
- **Works:** in 2.4 — the global registry and the version floor (#161). On main for 2.5 — the record store's
  generated write surface on the record query plane's S2S half (Get/Put/InsertIfAbsent/ReplaceIfUnchanged/Delete,
  Watch<T>Changes, the atomic Apply with canonical lock order), per-record-type durability (MEMORY/LOCAL/REMOTE),
  `(spec.record).s2s_write`, `(spec.record_store).no_bus_ingest` (#163); Go, Java and Rust wrappers guard durability,
  tenant and Apply. Tenant-owned services routed by a declared `(spec.rpc).route_field` (and
  `RPC_FLAG_ANY_TENANT`) in every language, registry.json carrying it, `Dispatcher.Conn` taking the target tenant;
  `mcp.McpRequest.tenant`, stamped from the caller's token by the Go/Java/Rust MCP carrier, never from the model (#170).
  The record store ↔ redo contract, sys `redolog.Redo` (`sys/redo.proto`: `Append`/`Replay`, tenant-routed through
  the new `(spec.service_s2s).host_tenant_owned`), and the Go server wrapper of a sys contract a service hosts by
  label (`minter.Minter` on kernel had none), and instance health for a service that serves only such a surface
  (#163 D5).
- **Released / deployed:** v2.4.1 is the newest release; 2.5 is unreleased.
- **Known gaps:** the redo service and the per-tenant Stratum that implement the redo contract are their repos'
  (tacplus-dev/redo#1, tacplus-dev/stratum#2). The gateway does not yet route by the route field nor stamp `McpRequest.tenant`
  (tacplus-dev/gateway#3 and its MCP follow-up). MCP on a tenant-owned service is same-tenant only (PROPOSALS.md).
  No C2S record writes (by decision, 2.5). The Rust wrapper renders no (spec.store_namespace) predicate pin on the
  query plane (PROPOSALS.md).
- **Blocked on:** nothing.
