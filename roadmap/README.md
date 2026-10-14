# Roadmap

This directory holds plans and design tracks for work that is not current
tacplus behavior yet. Current behavior lives in [../docs/](../docs/).

## Future Tracks

- [`.tac` sugar format](TAC_SUGAR_FORMAT_PLAN.md) — a lossless keyword dialect
  over annotated `.proto`: five block keywords (`config`/`state`/`s2s`/`c2s`/`c2d`)
  plus a schema-driven bare-`spec`-option rule, transpiled to `.proto` in front of
  the existing single code-generation path. Ships with `lower`/`raise`/`fmt`/`lint`.
- [Data entitlement architecture](DATA_ENTITLEMENT.md) — broader platform
  design notes for async data-returning RPCs, sealed candidate results, release
  processors, tenant cells, and downstream handoff boundaries. This is not a
  tacplus-only implementation commitment.

## Historical Baseline

The MVP exit plan is complete and archived at
[../archive/MVP_PLAN.md](../archive/MVP_PLAN.md). Durable contracts from that
work live in [../docs/API.md](../docs/API.md),
[../docs/DEVELOPMENT.md](../docs/DEVELOPMENT.md),
[../docs/THEORY.md](../docs/THEORY.md), and
[../docs/USER.md](../docs/USER.md).

The collection-paging standard is implemented on the tacplus side and archived at
[../archive/COLLECTION_PAGING_PLAN.md](../archive/COLLECTION_PAGING_PLAN.md); the
`method_collection` PAGE/SNAPSHOT/WATCH contract is enforced in code.

The identity-manager split (D-4) is complete for all five languages and archived at
[../archive/IDENTITY_MANAGER_SPLIT_PLAN.md](../archive/IDENTITY_MANAGER_SPLIT_PLAN.md);
every language emits `chassis/client_im` (C2S-reachable mint) + `chassis/service_im`
(authoritative S2S `MintServiceToken`).

Store materialization decoupling is complete (hard cutover) and archived at
[../archive/STORE_MATERIALIZATION_DECOUPLING_PLAN.md](../archive/STORE_MATERIALIZATION_DECOUPLING_PLAN.md);
tacplus core emits store-neutral semantic projection metadata (retention/dedup/
cardinality; graph node/edge/index intent) and no longer generates ClickHouse DDL
or Dgraph DQL. A `cmd/soundness` guard (`renderStoreVendorSoundness`) fails the build
if a backend-store or transport vendor name (including `kafka`) or store-query
directive reappears.

Client surface determinism is complete and archived at
[../archive/CLIENT_SURFACE_DETERMINISM_PLAN.md](../archive/CLIENT_SURFACE_DETERMINISM_PLAN.md);
client surfaces derive from the proto graph + service identity + language + package
(the `clients.{c2s,w2s,c2d}.enabled` flags are removed and hard-rejected), a
determinism gate enforces byte-identical generation, and GraphQL was removed. The
one remaining item — the swift/c# C2S+W2S buildout — lives in its own future track.

The `libs`→`sys` embedded-namespace rename + chassis de-overload is complete for all
five languages and archived at
[../archive/SYS_RENAME_PLAN.md](../archive/SYS_RENAME_PLAN.md); proto-derived types
live under `types/sys/` and runtime machinery under `chassis/`. Its Phase 3 (C#/Swift
`Sys`/`Chassis` casing) is moot: swift/c# emit no chassis directory (see below).

The Swift & C# client-surface buildout is complete and archived at
[../archive/SWIFT_CSHARP_CLIENT_SURFACES_PLAN.md](../archive/SWIFT_CSHARP_CLIENT_SURFACES_PLAN.md);
swift and c# now generate C2S (native gRPC) and W2S (gRPC-over-REST) clients
alongside C2D, completing the C2S+W2S+C2D matrix for all four client-only targets.
Both are validated by local compilation (`swift build` / `dotnet build`) in
consumer-contract. The SYS_RENAME Phase 3 casing follow-on turned out N-A — the
client-lite targets emit flat, self-contained client code with no chassis/sys dir.

## Local Follow-Ups

Unresolved local ledgers live in `../todo/` and are intentionally ignored by
git for now.
