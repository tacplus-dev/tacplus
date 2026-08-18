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

## PC-L-9 — ServiceManager teardown leak evidence is synthetic

**Status:** the leak oracle is real; what it measures is not a generated
ServiceManager.

`cross-test/chaos/oracle` has a `LeakOracle` that asserts resource counters stay
flat across soak cycles, and `oracle_test.go` exercises it against hand-built
counter trends. That tests the ORACLE's arithmetic. No test runs a generated
ServiceManager through a create/teardown cycle and feeds its real counters in, so
an SM that leaks on teardown would not be caught by anything here.

**To close:** an SM lifecycle harness that can construct and tear down a generated
ServiceManager repeatedly in-process and expose goroutine/connection/file-handle
counts to the existing oracle. The oracle side is already built.

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

## How to close one of these

Two things make a fix real here, and only the second is usually skipped:

1. Something must CALL the capability, not merely contain it.
2. A test must FAIL when that call is removed. Verify this by actually removing
   it and watching the test go red.

If a check can only ever skip on current fixtures, label it NOT COVERAGE
explicitly, the way the TLS-degrade parity leg in `current-generation` does —
a silent SKIP reads as a pass to everyone who did not write it.
