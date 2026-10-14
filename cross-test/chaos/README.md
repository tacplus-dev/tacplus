# Model Stress / Resilience / Interop Harness — Profile A

Implements the **Profile A** (Compiler/Model) harness from
`docs/MODEL_STRESS_TEST_PLAN.md`: a reusable chaos/soak skeleton that exercises
the tacplus-generated **`model`** runtime under failure, consumer-agnostic,
against **stubbed rivets** (test token/JWK signer + in-memory deterministic
state source). No kernel, no state store, no consumer identity/state service.

The harness is **deployment-profile-driven** (`profile.Profile` is the input),
so a future **Profile B** (Schematic system/deployment) can point the same
orchestrator / fault engine / oracle framework / report at a real service stack
without reimplementing the chaos/verification machinery. Profile B is **not**
implemented here and is rejected (`kind: system` -> error).

## Layout

| Path | Role |
|---|---|
| `profile/` | Deployment-profile config (YAML-loadable). `DefaultProfileA()` + `testdata/profile-a.yaml`. |
| `node/` | Synthetic multi-language model node: SM lifecycle, identity rivet (`identity.go`), in-memory monotonic state source + replay ring + WATCH stream (`state.go`, `watch.go`), unary echo-transform. The **COLL-1 dropped-delta bug** lives here behind `StateConfig.DropDeltaBug`. |
| `fault/` | Chaos/fault engine: `Fault` interface + menu (SIGTERM, SIGKILL, network partition, dependency-down, token-expiry) + seeded `Schedule`. |
| `oracle/` | Deterministic invariant oracles: WATCH gapless/ordered/no-dup, auth fail-closed (negative), no-leak trend, unary echo-transform. **Oracles drive the verdict; randomness only drives the schedule.** |
| `orchestrator/` | Deploys the node mesh from a profile, drives the NxN interop matrix + WATCH + auth + leak-soak + seeded fault schedule, emits a report. |
| `report/` | Report rendering (§7): interop matrix, recovery p50/p99, invariant violations, resource trend, injected-fault vs unexpected-failure log, verdict + seed. |
| `cmd/chaos/` | CLI: `go run ./cross-test/chaos/cmd/chaos [-profile f.yaml] [-inject-coll1-bug] [-seed N]`. |

## Run

```sh
go test ./cross-test/chaos/...                      # all oracles + acceptance test
go run  ./cross-test/chaos/cmd/chaos                # healthy run -> PASS (exit 0)
go run  ./cross-test/chaos/cmd/chaos -inject-coll1-bug   # WATCH oracle RED (exit 1)
```

## Phase-1 acceptance (the key requirement)

`orchestrator.TestPhase1Acceptance_Coll1BugTurnsWatchOracleRed` re-introduces the
COLL-1 dropped-delta bug behind a flag and asserts the WATCH oracle goes **RED**
(and stays GREEN with the bug off). This proves the harness detects a known bug
— the §9.1 acceptance bar. The bug is a real in-process simulation of the WATCH
stream: `StateSource.Commit` advances the revision but silently omits every Nth
change from the replay ring, leaving a gap the gapless oracle catches.

## Scope notes

- Everything here builds and tests in isolation: `go build ./cross-test/chaos/...`
  and `go test ./cross-test/chaos/...` are green. (A repo-wide `go build ./...`
  needs generated code under the gitignored `target/`, unrelated to this tree.)
- Pure Go, no new module dependencies (`gopkg.in/yaml.v3` is already required).

## TODO — remaining phases (this is a Phase-1 skeleton)

Phase 1 (prove it catches a known bug) is **done**. Remaining from the plan:

- **Phase 2 — full interop matrix over real generated runtimes.** The current
  mesh is an in-process Go simulation of the generated model contract. Replace
  the synthetic `node.Node` with real per-language generated nodes (Go + Rust +
  Java + TS + Python) compiled via the existing `cross-test/*/run.sh` recipes,
  wired over real gRPC/mTLS, exercising S2S + C2S + C2D. Keep the oracles —
  they consume observed events regardless of source.
- **Phase 3 — fault menu over the wire + leak soak.** The fault menu interfaces
  exist; back them with real process SIGTERM/SIGKILL, real netem/blackhole, real
  issuer restart, real slow-consumer backpressure. Run a multi-hour resource-
  trend soak scraping real goroutines/threads/FDs/memory per pod (the leak
  oracle's trend logic is already in place; feed it real scrapes).
- **Phase 4 — `kind`/`k3d` then cloud nightly.** Add the outer cluster-lifecycle
  job (create/destroy) separate from the in-cluster orchestrator (RBAC to
  deploy/kill/scrape only); pull the report artifact then tear down.
- **Backpressure / slow-consumer fault** is enumerated in the menu (`Kind`) but
  not yet a concrete `Fault` impl — add a bounded-queue/flow-control simulation.
- **C2D (intra-pod local socket)** shape is not yet modeled (Phase 2 territory).
- **PAGE/SNAPSHOT-tier oracles** — the state source supports snapshots; add a
  PAGE keyset-cursor oracle (malformed-token = INVALID_ARGUMENT, no silent
  reset) per docs/API.md.
