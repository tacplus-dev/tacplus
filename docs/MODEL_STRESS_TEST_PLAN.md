> **ARCHIVED 2026-06-11.** Phase 1 implemented at `cross-test/chaos/` (code comments cite this file's section numbers — kept intact). Testing principles moved to `../DEVELOPMENT.md` (Runtime Chaos Testing Principles). Phases 2–4 are `../MVP_PLAN.md` → Later work.

# Model Stress, Resilience & Interop Test Plan

> **Status: proposed, not yet implemented.** A follow-on testing program. Independent of
> the in-flight findings-fix pass; can be built in parallel but is not a per-commit gate.
> Authored 2026-06-07.

A live, multi-language, multi-pod chaos/soak harness that exercises the tacplus-generated
**`model`** runtime under failure. Every existing gate is static or single-process
(`cross-test/current-generation` = does it compile; `cross-test/consumer-contract` = does the
rivet works; `cmd/soundness` = static parity). **Nothing exercises the model as a running mesh
under churn** — which is exactly where the runtime risks live (WATCH dropped/duplicate deltas,
per-scope task/feed leaks, unbounded shutdown, 30s stream teardown, fail-closed-vs-open auth,
stale caches). ROADMAP Track B blocks promoting the control-plane soundness rows to
build-failing "until the runtime suite corroborates the generated behavior" — **this harness is
that runtime suite.**

## 1. What it tests (four goals)

1. **Cross-language interop** — a Go client ↔ Rust server, TS ↔ Java, etc. The NxN matrix.
   The property unique to tacplus (5 languages from one model); the highest-value goal.
2. **Resilience** — instances recover from faults (SM lifecycle: identity/token, TLS,
   state-store connect, health, graceful + crash recovery, watch reconnect).
3. **Soak / leak** — sustained churn over hours surfaces leaks and gray failures (goroutines/
   threads/FDs/memory trends), e.g. the per-scope ring/drain leaks.
4. **Correctness under churn** — clients get *correct* data; watches resume gapless & ordered;
   auth stays fail-closed.

## 1a. Two profiles, one reusable harness

There are **two distinct tests** with different scopes, and they should **share one harness**
(orchestrator, chaos engine, invariant-oracle framework, report format) parameterized by *what
pods to deploy* and *which invariants apply*:

- **Profile A — Compiler/Model (this document, lives in `schematic/tacplus`).** Tests the
  **tacplus-generated `model` itself**, consumer-agnostic: synthetic multi-language nodes +
  *stubbed rivets* (test token/JWK signer, in-memory state source), with no
  consumer-specific identity service or concrete state backend. Proves the generated model is
  correct, resilient, and at cross-language parity independent of any deployment.
- **Profile B — System/Deployment (future Schematic todo).** Tests a real Schematic service
  stack end-to-end under the same chaos: the reusable services and their real identity, state,
  policy, gateway, and eventing paths. It is **not** tacplus's to own, but it should live at the
  Schematic group level rather than in a consumer repo. See the Schematic group-level todo at
  `todo/SYSTEM_STRESS_TEST_PLAN.md`.

So the **orchestrator, chaos engine, oracle framework, and report must be built reusable**
(deployment-profile is an input, not hardcoded), so future Schematic system profiles can point
them at a real service stack without reimplementing the chaos/verification machinery. That
reusable harness is part of the same Schematic direction as the reusable service move: the
chaos/orchestration engine is a consumer-agnostic platform component, while deployment profiles
supply concrete custom types, vservices, services, and invariants.

The rest of this document specifies **Profile A**. Where a section is profile-specific it says so;
everything in §2 (oracles), §5 (recovery/leak), §6 (orchestrator/cluster), §7 (report) is the
shared harness and applies to both.

## 2. Core principle: random for coverage, deterministic invariants for verification

A pure "sink returns random data" harness can only assert *"it returned and didn't error"* — it
would pass green even with dropped deltas, duplicate deltas, fail-open auth, or a permanently
stale cache. **Randomness drives the schedule; deterministic oracles drive the verdict.**

- **Unary (S2S/C2S):** requests carry a seq/seed; the server response is a pure function of the
  request (echo-with-transform). The client recomputes the expected value and asserts equality —
  catches validation drift, wrong-bytes binding, truncation.
- **State / WATCH (highest value):** the server drives a *known* per-key monotonic revision log.
  Watchers must observe a **gapless, ordered, duplicate-free** sequence with correct
  snapshot→delta framing and correct `OUT_OF_RANGE`/relist on resume. This is the only way to
  catch silent dropped deltas, re-sent ≤R deltas, and stale caches.
- **Auth (negative oracles):** include callers that *must be denied* (wrong namespace, no
  entitlement, expired token / token after a refresh failure). **Fail-open is caught only as an
  expected-deny that succeeded** — you cannot detect fail-open without exercising the deny path.

All randomness is **seeded** (reproducible): a failed run replays deterministically.

## 3. Topology

- **Synthetic node app, one per language (Go, Java, TS, Python, Rust).** Each is *both* a server
  (exposes the test S2S + C2S services) *and* a client (calls peer nodes). Handlers are thin,
  deterministic stubs (the point is the generated model does the work). One container image per
  language; all functionally identical, generated from the same test proto + per-language
  `tacplus.yaml`.
- **Call graph forces cross-language pairs** (e.g. each node calls every other node) so the
  interop matrix is covered for both S2S and C2S. **C2D** is intra-pod (local socket) — tested
  within a pod.
- **Scope: test the tacplus-generated model, NOT a consumer's backing services (corrected 2026-06-07).**
  tacplus does not know about any consumer's concrete identity service or state backing. Those
  live *behind* the model's generated rivets and are the consumer's concern, so they are **out of
  scope**. The harness exercises what tacplus actually generates: the
  ServiceManager lifecycle, the C2S/S2S/C2D clients and server wrappers, the generated auth
  (JWK verification / authorizer / namespace), validation, and the collection/WATCH machinery
  (Hub, replay ring, snapshot→delta, relist). This keeps the harness **consumer-agnostic** — it
  tests the model itself, independent of any one deployment.
- **Stand in the minimal thing each generated rivet requires** — never a concrete consumer
  service. *(A **rivet** is a generated attachment point on the model chassis — identity, state,
  TLS — where a backing service or a handler bolts on. The model defines the rivets; the
  consumer rivets its own backing services and handlers onto them.)*
  - *Identity rivet:* a minimal test token signer + a served JWK set (using tacplus's own
    generated token/JWK primitives), so the generated verify/refresh/identity-manager paths run
    for real. Not a consumer identity service.
  - *State rivet:* the WATCH/collection source is the synthetic node's own in-memory,
    deterministic state machine (the §2 monotonic log) — the generated state service, Hub, ring,
    and watch wrappers are what's under test; the persistence backing is irrelevant. NOT
    Hazelcast.
  - *TLS:* real mTLS/SPIFFE between nodes with test certs/CA (tacplus generates this — in scope).
- **Reconciling "as real as possible":** realism applies to *how the generated model runs* — real
  mTLS, real token issuance/verification/refresh through the generated paths, real K8s scheduling,
  real crash/graceful recovery — **not** to importing a consumer's backing services. The synthetic
  nodes are real generated models (full SM bootstrap, real auth/state usage, real
  readiness/health); only their business logic is a stub.
- The test proto exposes the shapes that stress the runtime: unary S2S + C2S, a WATCH collection,
  a SNAPSHOT/PAGE collection, an auth-required + an auth-denied method, a namespaced method.

## 4. Fault menu (not just SIGKILL)

| Fault | What it exercises |
|---|---|
| **SIGTERM (graceful)** | Bounded graceful shutdown + force-close. Catches unbounded-shutdown / shutdown-hang. |
| **SIGKILL (crash)** | Crash recovery, reschedule, client reconnect. |
| **Network blackhole / latency to state store** | Watch reconnect w/ bounded **jittered** backoff; the per-call-timeout-kills-long-stream bug. |
| **Dependency restart** (identity/state backends) | SM reconnect, cache resync, **auth fails closed** while the issuer is down. |
| **Token expiry / clock skew** | "Open channel isn't enough if refresh failed" send contract; per-message token freshness on streams. |
| **Slow consumer** | WATCH backpressure / flow control; bounded queues. |

## 5. "Recovered" — a definition with teeth, plus leak detection

1. **Fail-fast:** in-flight calls during a kill fail within a bounded deadline — they do **not
   hang** (shutdown/timeout findings).
2. **Resume SLA:** after reschedule, correctness resumes within a bounded time; watch caches
   **re-sync** (no permanent staleness — a terminal-stale cache must be observable, not silent).
3. **No resource creep:** the orchestrator scrapes per-pod goroutines/threads/FDs/memory **as a
   trend across many kill cycles over a soak** and asserts they stay flat. Leaks only show as a
   trend, never a snapshot.

## 6. Orchestrator & cluster

- **Separate cluster lifecycle from test orchestration.** The *outer* CI job creates/destroys the
  cluster and applies the orchestrator; the *in-cluster* orchestrator needs only RBAC to
  deploy/kill pods and scrape telemetry — not cluster-create rights. Safer, cleaner.
- **Cluster:** `kind`/`k3d` for the routine job (cheap, CI-friendly); a real cloud node only for
  periodic large-scale soak (realistic multi-node networking).
- **Orchestrator responsibilities:** deploy the matrix → drive seeded load → inject faults per a
  schedule → consume the model's *own* generated signals (unified readiness
  `Ready = cert ∧ config ∧ first-health`, health, trace/accounting events) **as part of the
  oracle** (so the test also verifies generated observability) → scrape resource trends → assert
  the §2 invariants and §5 recovery → emit a report.
- **Report retrieval then teardown:** report is pulled from the cluster (artifact), then the
  cluster is destroyed.

## 7. Report contents

- Interop matrix result (per language-pair, per RPC shape: pass/fail).
- Recovery-time distribution per fault type (p50/p99); any hangs (bounded-deadline violations).
- Invariant violations: dropped/duplicated/out-of-order deltas, fail-open auth (expected-deny
  succeeded), stale-cache-after-recovery, validation mismatches.
- Resource trends per pod over the soak (leak verdict).
- Injected-fault log vs unexpected-failure log (the report must distinguish *expected* chaos from
  *real* failures).
- Overall verdict + the seed (for deterministic replay).

## 8. Cadence & placement

- **Nightly + on-demand, never per-commit** (too slow/expensive). Per-commit keeps the existing
  compile/contract/soundness gates. A small smoke variant in the PR path is optional (see open
  decisions).
- **Placement (recommended):** a new `cross-test/chaos/` (or `cross-test/stress/`) tree in the
  tacplus repo — it pulls the generated model from the same compiler, so in-repo is natural,
  alongside the other `cross-test/` harnesses. (Alternative: its own repo if the K8s/cloud
  tooling becomes heavy enough to warrant isolation.)

## 9. Phasing (don't boil the ocean)

Maintaining 5 node apps + orchestrator + K8s harness is real ongoing cost. Grow it:

1. **Phase 1 — prove it catches a known bug.** 2 languages (Go + one other), `kind`,
   SIGKILL + SIGTERM, the WATCH monotonic-sequence invariant + one auth-deny oracle.
   **Acceptance bar: re-introduce the COLL-1 dropped-delta bug and confirm the harness goes red.**
   If it can't catch a bug we *know* exists, it isn't yet a test.
2. **Phase 2 — full interop matrix.** All five languages; S2S + C2S + C2D.
3. **Phase 3 — fault menu + leak soak.** Network, dependency-restart, token-expiry, slow-consumer;
   multi-hour resource-trend soak.
4. **Phase 4 — cloud-cluster nightly** at larger scale.

## 10. Open decisions (recommended defaults baked in; confirm or override)

- **Profile-A rivets** — **DECIDED: synthetic/stubbed rivets** (test token/JWK signer,
  in-memory deterministic state source, test mTLS CA; no consumer identity/state/policy services).
  The future Schematic system profile supplies the real reusable service stack.
  Resolved 2026-06-07.
- **`kind`/`k3d` vs cloud node** for v1 — plan assumes **`kind`** first, cloud for big soak.
- **Location** — plan assumes **`cross-test/chaos/` in-repo**; own repo if it grows heavy.
- **Cadence** — plan assumes **nightly + on-demand**; decide whether a trimmed smoke variant also
  runs per-PR.

## 11. Relationship to existing gates

Complements, does not replace: `current-generation` (compiles), `consumer-contract` (rivet works),
`cmd/soundness` (static parity). This is the **runtime / resilience / interop** layer, and it is
the prerequisite ROADMAP Track B names for promoting the advisory control-plane soundness rows
(auth, watch reconnect/teardown, readiness, task teardown) to **build-failing** — once this
corroborates the generated behavior, those rows can graduate.
