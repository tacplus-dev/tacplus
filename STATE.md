# State: tacplus

Overwritten to stay current (loopers and agents). Last updated: 2026-10-29 by looper (#193).

- **Version / line:** v2.5.0 (latest release, 2026-10-28, `release/2.5` cut at it); main is the 2.6 line in progress (this repository is the compiler; it declares no tacplus floor)
- **Works:** in 2.4 — the global registry and the version floor (#161). In 2.5 — the record store's
  generated write surface on the record query plane's S2S half (Get/Put/InsertIfAbsent/ReplaceIfUnchanged/Delete,
  Watch<T>Changes, the atomic Apply with canonical lock order), per-record-type durability (MEMORY/LOCAL/REMOTE),
  `(spec.record).s2s_write`, `(spec.record_store).no_bus_ingest` (#163); Go, Java and Rust wrappers guard durability,
  tenant and Apply. Tenant-owned services routed by a declared `(spec.rpc).route_field` (and
  `RPC_FLAG_ANY_TENANT`) in every language, registry.json carrying it, `Dispatcher.Conn` taking the target tenant;
  `mcp.McpRequest.tenant`, stamped from the caller's token by the Go/Java/Rust MCP carrier, never from the model (#170).
  The record store ↔ redo contract, sys `redolog.Redo` (`sys/redo.proto`: `Append`/`Replay`, tenant-routed through
  the new `(spec.service_s2s).host_tenant_owned`), and the Go server wrapper of a sys contract a service hosts by
  label (`minter.Minter` on kernel had none), and instance health for a service that serves only such a surface
  (#163 D5). JWK verification takes a missing alg from the token within the key's family (#179).
  On main (2.6): `grpcconfig.disk_persistence.archive.member` names the Java archive member instead of the instance
  id, so a restarted writer keeps its chain (#143; Go and Rust when their ServiceManager wires the archive).
  Archive retention is a bucket lifecycle rule, documented with its pitfall in docs/API.md; the writer never deletes (#144).
  The `linked` claim is retired: the generated namespace backstop (record egress, page/Watch, query predicate, record
  write tenant pin) accepts the caller's own `namespace` only, no language generates a `linked` accessor, and
  `JwtPayloadType` 29 is reserved (#171).
  The fetch layer authenticates `https://github.com` clones (`src`, `depends`, the private registry) with `GH_TOKEN`
  (else `GITHUB_TOKEN`) and no other host; `CI_JOB_TOKEN` still goes only to `CI_SERVER_HOST` (#191; backported to
  release/2.4 and release/2.5 by pull request, as v2.4.2 and v2.5.1 once they merge).
  Deterministic builds (#193): a repository pins the exact release and the build image digest in `tacplus.lock`
  (`tacplus pin` writes it; moving it is a commit); there is no tacplus image (the old tags stay, deprecated, and
  2.6 gets none); `build:X.Y` per line, rebuilt on every release as an immutable `build:X.Y.Z`, carries Kani and
  cargo-fuzz too, and its `tacplus` is `images/tacplus.sh`, which runs exactly the pinned binary, checked against
  `checksums.txt`, and refuses a repository with no pin or a pin of another line. `tacplus verify` reports Go as
  skipped.
- **Released / deployed:** v2.5.0 is the newest release (binaries and checksums; images as before: from 2.6.0 a
  release publishes binaries and `build:X.Y.Z`, and no tacplus image).
- **Known gaps:** the redo service and the per-tenant Stratum that implement the redo contract are their repos'
  (tacplus-dev/redo#1, tacplus-dev/stratum#2). The gateway does not yet route by the route field nor stamp `McpRequest.tenant`
  (tacplus-dev/gateway#3 and its MCP follow-up). MCP on a tenant-owned service is same-tenant only (PROPOSALS.md).
  No C2S record writes (by decision, 2.5). The Rust wrapper renders no (spec.store_namespace) predicate pin on the
  query plane (PROPOSALS.md). The looper image's tacplus runs the tacplus image, which 2.6 does not have
  (PROPOSALS.md). `tacplus verify` (Kani) fails on every generated Rust crate: the generated harnesses do not
  compile (#211; #196 stops generating them). The new build image (Kani, cargo-fuzz, the launcher) is verified by
  images.yml once pushed; the launcher and `tacplus pin` by the gates.
- **Blocked on:** nothing.
