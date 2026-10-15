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

## PC-L-3 — cert-stream TLS exhaustion pin — CLOSED (Go, Java, Rust)

**Status:** closed. Channel-capture was closed first
(`CertStreamBehaviorTest.java`, `...ReResolvesTheAuthorityAfterTheTargetMoves`,
TLS+SMB); TLS exhaustion is now closed in all three languages.

- **Go:** `tlsCertStreamLoop` takes its first-delivery retry budget as a
  parameter (falling back to `certStreamMaxInitRetries = 30` when non-positive),
  and `TestGoTlsCertStreamRetryExhaustion` in
  `cross-test/cert-stream/testdata/cert_stream_behavior_test.go` drives a budget
  of 3 to real give-up in ~6s.
- **Java:** `tlsCertStreamLoop` gained a 6-arg overload taking the budget
  (falling back to `MAX_INIT_RETRIES = 30` when non-positive), the 5-arg
  production entry point passes `MAX_INIT_RETRIES`, and
  `javaTlsCertStreamGivesUpAfterTheInjectedBudget` /
  `javaTlsCertStreamDefaultBudgetOutlastsATestBudget` in
  `cross-test/cert-stream/testdata/CertStreamBehaviorTest.java` pin the give-up
  chain and the unchanged default.
- **Rust:** `tls_cert_stream_loop` takes a trailing `max_init_retries: u32`
  (falling back to `MAX_INIT_RETRIES = 30` when 0), the production call site
  passes `MAX_INIT_RETRIES`, and
  `rust_tls_cert_stream_gives_up_after_the_injected_budget` /
  `rust_tls_default_budget_outlasts_a_test_budget` in
  `cross-test/cert-stream/testdata/cert_stream_behavior_test.rs` pin the
  give-up chain (degrade report + startup cause + loop stop) and the unchanged
  default.

Why a seam was needed at all: spending the production budget honestly costs
~812s of backoff in Go/Rust and ~780s of `Thread.sleep` in Java. Java's
`MAX_INIT_RETRIES` is additionally a `private static final int`, which `javac`
inlines at every call site (confirmed via `javap`), so it could not be
monkey-patched from a test even in principle.

Blast radius was deliberately kept at zero: Java's constant is shared with the
general-init loop and `healthReporter.start(...)`, and Rust's is shared with
general-init, the two state-store waits, and the health reporter. Neither seam
splits the constant — every non-TLS-cert-stream call site still reads it
directly and is unchanged. `just determinism` / `just current-generation` pin
that the emitted default behavior is byte-identical.

`cross-test/cert-stream/run.sh` asserts, per language, both that the default
budget is still 30 and that the production call site passes it explicitly.

---

## PC-L-3b — SMB cert-stream exhaustion is unpinned in every language

**Status:** open. Same defect class as PC-L-3's TLS leg, one loop over.

`smbCertStreamLoop` (Go, `service_manager.go.tmpl`), `smbCertStreamLoop` (Java,
`ServiceManager.java.tmpl`) and `smb_cert_stream_loop` (Rust,
`service_manager.rs.tmpl`) all still compare `initAttempts`/`attempt`/
`init_attempts` against the bare budget constant — `certStreamMaxInitRetries`
in Go, `MAX_INIT_RETRIES` in Java and Rust. None of the three took the seam that
PC-L-3 added to its TLS sibling, so the SMB give-up-before-first-delivery path
cannot be driven in a test without the same ~780-812s of real backoff, and
consequently has no behavioral pin in any language.

Note there was no pre-existing Go SMB entry in this file to match wording
against: the SMB gap was undocumented in all three languages until now. PC-L-3's
three legs were each scoped to TLS only by the controller, deliberately — this
entry records what that scoping left behind rather than implying regression.

What is covered today: SMB channel-capture and the handler/renewal paths are
pinned by the `rust_smb_*` / `javaSmb*` / `TestGoSmb*` cases in the cert-stream
behavior tests. Only exhaustion is missing.

**To close:** give each `smb*CertStreamLoop` the same budget seam its TLS
sibling now has — a trailing parameter in Go and Rust, an overload in Java —
with the production call site passing the unchanged constant, then add an
exhaustion test per language mirroring the TLS ones. Keep the shared constants
unsplit, as PC-L-3 did.

---

## PC-L-2b — Go cert-stream give-up path's DEGRADED reason — CLOSED

**Status (2026-10-15):** closed. Run()'s background drain of the TLS stream's
first-delivery signal is now `drainTLSFirstDelivery(ctx, readyCh, degrade)` in
the Go ServiceManager template (Run starts it with `sm.reportTLSDegraded`), so
the cert-stream fixture drives the exact code Run runs.
`TestGoTlsCertStreamRetryExhaustion` "give-up is reported DEGRADED as a
give-up" runs a real `tlsCertStreamLoop` to exhaustion (budget 3) into the
drain and asserts exactly one degrade call starting "TLS renewal stream gave up
before first delivery: " and carrying the give-up error; "first handler failure
is fatal" feeds the handler-rejection signal through the same drain and asserts
the handler wording. Proven red: with the give-up branch's degrade call removed
the subtest fails ("the give-up was never reported DEGRADED").

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

## NEW-21 — empty-trust-domain classifier is pinned by existence only — CLOSED (Go, Java, Rust)

**Status (2026-10-15):** closed in all three. Go emits
`s2sm_identity_error_test.go` and Java `ServiceManagerIdentityErrorTest` beside
every ServiceManager; Rust's ServiceManager now carries
`#[cfg(test)] mod identity_error_tests` over the real
`is_permanent_identity_init_error` (the boxed `TokenMinterNotConfigured` the
retry loop sees is permanent; an error with the same text is not; transient
failures stay retryable), and `current-generation` runs `cargo test --lib` on
the generated model crate in every Rust service fixture and fails unless
`identity_error_tests::missing_token_minter_is_permanent ... ok` is in the log,
so the gate cannot pass by running zero tests. Rust has no trust-domain case:
an empty trust domain is refused by `SpiffeServerCertVerifier::new` before the
retry loop. Proven red: with the downcast replaced by a message match,
`the_same_text_is_not_the_typed_error` fails. The history below is the
original finding.

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

W2S request (ingress) validation in the generated REST dispatch is wired and
pinned in Rust, Go and Java: `cross-test/w2s-validate` hosts the real
generated `rest_dispatch::dispatch` / `dispatch_stream` (Rust),
`restdispatch.Dispatch` / `DispatchStream` (Go) and `RestDispatch.dispatch` /
`dispatchStream` (Java) against a real gRPC backend and proves an
`assert_email` violation is rejected `400` without reaching the backend, with
a valid request as the negative control.

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

## C2D-1 — the C2D server runs through its standalone host, not through `Run`

**Status:** Go, Java and Rust covered up to the c2d mount `Run` shares with the
standalone host; `Run`'s own call of that mount is not executed.

`cross-test/c2d-server` (`just c2d-server`) generates a c2d-only, cluster-free
daemon, compiles the whole generated model, and starts it through the
generated standalone host entry (Go `sm.ServeC2DStandalone`, Java
`ServiceManager.serveC2DStandalone`, Rust `ServiceManager::serve_c2d_standalone`)
from code outside the generated packages, exactly as a cluster-less daemon's
main does, on the declared socket (the generated `C2DSocketPath` /
`C2D_SOCKET_PATH`). It calls it over the socket through the generated c2dm
client: a tokenless valid call, request and response validation, server
streaming, the 0600 mode, stale-socket removal, and on stop the drain, socket
cleanup and exactly one handler shutdown. Proven by reverting, 2026-10-15:
dropping unary request validation from the Go, Java or Rust wrapper generator,
or dropping the handler shutdown call from any of the three entries, turns
the matching leg red. Earlier (2026-10-14): dropping the c2d auth skip from
the Go wrapper, or widening the chmod in either Go or Java chassis, turned it
red.

What it does not run is the `Run()` / `run()` line that mounts the same
server on `C2DSocketPath`: `Run` needs a live cluster (config, identity, state
store) that no in-repo fixture provides, so that line is pinned by the gate's
grep and by the mixed TLS + c2d render tests (`c2d_server_render_test.go`
under go/types and java/types), not executed.

**To close:** a ServiceManager harness that can boot `Run` against a fake
cluster.

---

## How to close one of these

Two things make a fix real here, and only the second is usually skipped:

1. Something must CALL the capability, not merely contain it.
2. A test must FAIL when that call is removed. Verify this by actually removing
   it and watching the test go red.

If a check can only ever skip on current fixtures, label it NOT COVERAGE
explicitly, the way the TLS-degrade parity leg in `current-generation` does —
a silent SKIP reads as a pass to everyone who did not write it.
