# Known test gaps

Five areas where the gates are **green but not covering** the thing their name
suggests. Each was reviewed, confirmed still real, and deliberately parked — they
need test infrastructure that does not exist yet, and none is a security or
correctness defect on its own.

This file exists because of a failure mode this codebase produces repeatedly: a
capability is generated, a test asserts the capability *exists*, and everything is
green while the capability has never once run. A gap that is written down is a
gap; a gap that is merely absent from the gate output reads as coverage. If you
are deciding whether some behaviour is tested, check here before trusting a
passing run.

Verified against the tree as of the commit that added this file. Each entry names
what would have to exist to close it, so the cost is visible rather than
rediscovered.

---

## PC-L-3 — Java cert-stream has no exhaustion pin; retry budget isn't testable

**Status:** channel-capture is closed (`CertStreamBehaviorTest.java`,
`...ReResolvesTheAuthorityAfterTheTargetMoves`, TLS+SMB). Exhaustion still has
no behavioral pin, in any language — Go and Rust are also missing it, not just
Java.

The TLS renewal stream can exhaust its retry chain, and a renewed channel can be
captured by a stale reference. The channel-capture leg is now proven: a stale
authority's stream ends, the loop backs off, and by the next attempt the
connection has re-resolved to the moved target — evidenced by per-authority CSR
counters (stale sees exactly one, moved sees the retry's).

Exhaustion remains open in Java specifically because `MAX_INIT_RETRIES = 30` is
a `private static final int`, which `javac` inlines at every call site
(confirmed via `javap` bytecode inspection — the constant is baked in, not
read from the field). There is no seam to inject a smaller budget, so an
honest exhaustion test means ~780s of real `Thread.sleep` per run.

**To close:** make the retry budget non-constant/injectable in the Java
template (and check whether Go/Rust have the same seam gap) so a test can
drive exhaustion in real time instead of wall-clock time.

---

## PC-L-2b — Go cert-stream give-up path's DEGRADED reason is unpinned

**Status:** the retry-target-swap leg (PC-L-2) and the renewal-time `degrade`
seam (PC-L-1's key-mismatch and handler-rejection reasons) are both now
covered by `cert_stream_behavior_test.go`. One path through the same
DEGRADED-marking machinery is not: give-up-before-first-delivery.

That path never calls `degrade(reason string)` directly — it sends a wrapped
error on `readyCh`, and `Run()`'s background drain does the DEGRADED marking,
discriminating the reason by testing the error against sentinels like
`errTlsFirstCertFatal`. `Run()` sits above the `cert-stream` fixture used by
this test file, so it isn't drivable from here without a different harness.

**To close:** a test hosting `Run()` itself (or the smallest slice of it that
performs the sentinel discrimination) that drives the give-up-before-first-
delivery path and asserts which DEGRADED reason gets reported.

---

## PC-L-9 — ServiceManager teardown leak evidence is synthetic

**Status:** closed for goroutines; the open-FD leg runs on Linux only.

`cross-test/chaos/leaksoak` generates a real Go model, runs the generated
`chassis/servicemanager` ServiceManager through eight create/teardown cycles in
that model's own process, and feeds the real `runtime.NumGoroutine()` trend into
the unchanged `oracle.LeakOracle`. A live ServiceManager holds ten goroutines and
settles back to the baseline after `Close()`; the harness fails if the live count
never exceeds the baseline, so it cannot pass green by measuring nothing. Proven
by reverting: a single parked goroutine per cycle turns the oracle red with a
rising per-cycle `no_resource_leak` violation.

**Remaining:** open FDs are scraped from `/proc/self/fd`, which does not exist on
macOS, so the oracle's `OpenFDs` leg is inert on a developer Mac and live in
Linux CI. Closing that would need an FD-count mechanism that is neither cgo nor
`lsof`-shelling.

---

## SEC-2 — SPIFFE soundness checks formatting, not parsing

**Status:** the gate is real and does fire, but its scope is narrower than the
name implies.

`assert_spiffe_soundness` in `cross-test/current-generation/run.sh` rejects raw
`spiffe://` **construction** in generated code outside `sys/spiffe` — printf
verbs, `format!`/`{}`, `${}`, f-strings, and concatenation onto a spiffe literal.
That is genuine coverage of the construction side.

It asserts nothing about **parsing**. Generated code that splits a SPIFFE ID on
`/`, strips a prefix by index, or otherwise decomposes an ID by hand passes
cleanly. Since the whole point is that SPIFFE handling goes through the generated
helpers, half the surface is unguarded.

**To close:** extend the assertion with prefix/split parsing patterns, then prove
it fires on a deliberately hand-rolled parse. A pattern added without that proof
would be indistinguishable from one that can never match — see the parity gate
whose `find` patterns matched nothing and printed SKIP on every run.

---

## NEW-21 — empty-trust-domain classifier is pinned by existence only

**Status:** the typed seam is fixed in both languages. The classifier side has no
behavioural test.

`isPermanentIdentityInitError` exists in the go and java ServiceManager templates
and is referenced from the retry path. The only tests are template regression
pins — `lib_templates_regression_test.go` and
`runtime_templates_regression_test.go` assert the *strings*
`"func isPermanentIdentityInitError(err error) bool"` and
`"if isPermanentIdentityInitError(idErr) {"` appear in the template text.

Nothing calls the function. A classifier that returned the wrong answer for every
input — turning a permanent failure into an infinite retry, or aborting on a
transient one — would keep both pins green. Three tolerance branches downstream
are likewise unexercised.

**To close:** table-driven tests over the real classifier in each language,
covering the permanent case, the transient case, and the wrapped-error case (go's
`errors.Is` branch and Java's cause-chain walk).

---

## NEW-46 — nothing asserts a sys-fold is complete

**Status:** no gate exists.

When a fold subsumes messages or fields, nothing checks that the folded result
preserves the set it replaced. A fold that silently dropped a message or a field
would generate cleanly and pass every gate; the loss would surface later as a
missing type at a call site, far from the cause.

**To close:** a soundness rule that compares the pre-fold and post-fold
message/field sets and fails on anything absent from the result. `cmd/soundness`
is the natural home — it already walks the model and already has a matrix-report
format for per-rule coverage.

---

## PC-W-5 — `proxy_dispatch::validate_request()` still has no in-repo caller

**Status:** the REST leg is now closed; the gRPC-proxy leg is not.

W2S request (ingress) validation in the generated Rust REST dispatch is wired
and pinned: `cross-test/w2s-validate` hosts the real generated
`rest_dispatch::dispatch` / `dispatch_stream` against a real tonic backend and
proves an `assert_email` violation is rejected `400` without reaching the
backend, with a valid request as the negative control.

`proxy_dispatch::validate_request(grpc_path, proto_bytes)` is a different seam:
it decodes raw proto frames, so its caller is the gateway's `proxy_grpc` layer,
which lives in the consuming repo and not here. Nothing in this repo calls it,
and no fixture can call it meaningfully without hosting a gRPC proxy — the
generated code contains the capability but never the call site, so a test that
invoked it directly would prove only that the function compiles.

**To close:** a fixture that generates a gateway (`spec.service.gateway`) and
hosts a real gRPC proxy hop in front of a backend service, so frames reach
`validate_request` the way production frames do. That is a materially larger
harness than the REST fixture, which needs no proxy at all.

---

## How to close one of these

Two things make a fix real here, and only the second is usually skipped:

1. Something must CALL the capability, not merely contain it.
2. A test must FAIL when that call is removed. Verify this by actually removing
   it and watching the test go red.

If a check can only ever skip on current fixtures, label it NOT COVERAGE
explicitly, the way the TLS-degrade parity leg in `current-generation` does —
a silent SKIP reads as a pass to everyone who did not write it.
