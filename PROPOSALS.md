# Proposals: tacplus

Written by loopers. Each proposal: what, why, links. Jody promotes accepted ones into ROADMAP.md (via an agent);
the looper then marks it `promoted` or `declined` here with the date.

## Open
- **fleet build locks a service at a main release** (2026-11-26, #981): fleet-seatrials#82 rolls dev to each
  service's green main release, which is tacplus-dev/build's record (a commit and an image by digest), not a tag.
  Swapping only the image would run main's code against the tag's contracts, so the lock has to be resolved at the
  commit: contract, `depends`, `contribute`, `inputs_digest`, `build/proto`. Proposed: a `releases.yaml` beside
  `fleet.yaml` (name, 40-hex commit, image by digest) that pins the named services (`version: main-<12 hex>`,
  `pinned: true`, `image` set even under `images: source`). `--check` reports these services as pinned, and their
  floors are checked against the line `fleet.yaml` states. #950 is on `release/2.6` only, so it needs a forward-port
  to main first, then a 2.6 backport. Design and questions:
  https://github.com/tacplus-dev/tacplus/issues/981#issuecomment-7200159033
- **The LLM-rule gate in Java** (2026-11-20, #909): Rust and Go generate the advisor's model port, the core's
  adapter and the gate; Java refuses `advisor:`. The accepted Java gate ("a Maven module with only those
  dependencies") needs the generated Java tree split into Maven modules, which it is not today (one source tree the
  service's pom compiles). Recommended instead, asked on #909
  (https://github.com/tacplus-dev/tacplus/issues/909#issuecomment-7045044397): a generated JUnit test running the
  JDK's `jdeps` (in process, `ToolProvider`) over the advisor package's compiled classes, failing on any reference to
  the generated tree or the service's other packages but the port's, as Go's `go list` check does.
- **The advisor's port re-exports top-level types only** (2026-11-20, #909): `wire::` (Rust) and the Go aliases
  cover the model service's top-level messages, enums, oneof modules and wrappers and enum values. A message declared
  inside a message (its prost module, its Go `Outer_Inner` type) is not reached, since `parser.MessageDescriptor`
  carries no nested declarations. llmproxy's messages are all top-level today; the parser would need the nesting
  for the port to follow one that is not.
- **A runtime test of the advisor adapter's trace** (2026-11-20, #909): the adapters' `trace_id` on the llmproxy
  call is pinned by render tests (`advisor_gen_test.go`, Rust and Go) and compiled by `cross-test/llm-rule`, which
  does not run them against a peer. `cross-test/zero-rpc-s2s` already runs a fake kernel and a fake llmproxy for
  Rust; an adapter call through it, with the peer asserting the trace it received, would make it a runtime proof.
- **The Go advisor templates in the security scan** (2026-11-20, #909): `scripts/generated.sh` generates the zerorpc
  set (Rust) with an advisor, so the Rust port, its build script and the adapter are scanned. Its sets are the
  current-generation fixtures, and none builds an advisor in Go, so the Go port, its gate test and the adapter are
  not scanned. If current-generation built `cross-test/proto-advisor` in Go, both lists could take it.
- **The advisor gate and third-party network libraries** (2026-11-20, #909): the gate refuses every part of the
  service but the port; it does not refuse a crates.io crate or Go module that opens a network connection (a
  tokenless connection, which a tacplus service refuses). If the platform wants the advisor to have no network
  path at all, the gate could refuse a named set of network crates and modules too; that is a policy choice.
- **Chassis crates' license is tacplus's, not the consumer's header** (2026-11-08, #368): every generated Rust crate
  now declares the SPDX expression of tacplus.yaml's `header:` as its `license` (none when the header names none),
  as #368 asks. The chassis crates (am, grpcserver, httpclient, ...) are tacplus's own Apache-2.0 source copied into
  the output, and their `.rs` files keep tacplus's `Copyright (c) 2026 Jody Scott` / Apache-2.0 header whatever the
  consumer's header says. A consumer with a proprietary header (or none) gets a chassis crate whose manifest says
  otherwise. Declaring `license = "Apache-2.0"` on the chassis crates always, and the header's license only on the
  `<svc>-model` and `<svc>-model-types` crates, would match the source; it is a choice for Jody.
- **cargo-deny on the generated sample in current-generation** (2026-11-08, #368): nothing in the gates runs
  cargo-deny on the Rust tacplus emits, so #368 (22 unlicensed crates) was found by a service's scan
  (tacplus-dev/mailbox#15). `cargo-deny check licenses sources bans` with looper-scan's default policy needs no
  advisory database and is deterministic; with cargo-deny in the build image it could run after `cargo check` in
  current-generation. The advisories check would then follow #308, #319 and #320 (today's four unmaintained crates).
- **Accounting records name their tenant** (2026-11-08, #273 → tacplus-dev/stratum#24): accounting goes to the tenant's
  own Stratum, but the generated sink leaves `AccountingRecord.namespace` (its `store_namespace`) unset and the
  emitter's identity map carries only `sub`/`aud`/`iss`/`azp`/`act`. Either the sink fills it from the verified
  caller's `namespace` claim in Go, Java and Rust, or the event bus stamps it at publish as it stamps the owner;
  which one is the event bus's and Stratum's contract to settle with stratum#24, so it is not guessed here.
- **Handler metrics on Go stream RPCs** (2026-11-08, #273): Go's `am.SetRecordsReturned`/`AddBytes` reach the record
  of a unary call only; a stream handler takes its context from the stream, and the wrapper does not yet hand it a
  stream whose `Context()` carries the call's `RpcMetrics` (Java's io.grpc.Context and Rust's task-local cover
  streams). The record of a stream is still written, with its outcome and reason.
- **Exempt every accounting-topic RPC by type, not only the publish** (2026-11-08, #273): the broker wrapper drops only
  the accounting topic's publish RPC from its accounting map. A subscribe or watch of the accounting topic (Stratum's
  ingest) is one record per stream, not per record, so it cannot amplify, and S2S RPCs are not accounted at all; if
  either changes, widen the exclusion to every RPC whose request or response carries `AccountingRecord`.
- **Release note for 2.7: the `am` free-text emitters are gone** (2026-11-08, #273): `RpcError`, `TokenRejected`,
  `Diagnostic`, the agent-telemetry events and the `ErrorContext` helpers are removed from `sys/accounting.proto` and
  every language's `am`, as #273 asks; a service that called them directly must move to the typed record. The
  2.7 notes should say so, since docs/VERSIONING.md otherwise keeps a minor additive.
- **Durable accounting spool self-test is timing-bound under load** (2026-11-08, #273):
  `TestDurableSpoolReplaysUnacknowledgedAfterRestart` (cross-test/current-generation/testdata/go) failed once with
  the cursor at 2 of 3 while the whole generated module's tests ran in parallel, and passed five times alone. Its
  one-second bound may be too tight on a loaded runner.
- **W2S stream rotation needs a gateway contract** (2026-11-22, #323): #259's design rotates an SSE (W2S) stream
  by "a new request presenting the session id and the new token, the old one drained to its `last` event", but the
  gateway, which turns a W2S request into the backend stream, has no such contract: how a W2S request names a
  session (a header, a query parameter), how `last` is framed as an SSE event, and the WebSocket equivalent for
  client-streaming and bidi. A gateway issue to settle it, then the TypeScript, Python and Swift W2S clients on it.
  Until then W2S streams end at their token's `exp` (KNOWN_TEST_GAPS RT-2).
- **The gateway's rotation leg with a generated backend and client** (2026-11-22, #323, tacplus-dev/gateway#26):
  the gateway's `e2e_rotation` proves routing by session id and the same-subject refusal against a stand-in
  backend. A leg with a generated C2S server (it now serves `<Svc>Rotating`) and a generated client
  (`cross-test/rotation-clients`' fixture) through the gateway would close #323's "through the gateway"
  acceptance end to end; it belongs in the gateway repo, which builds the gateway.
- **The Go, Java and Rust C2S clients rotate as their S2S clients do** (2026-11-22, #323): the generated `c2sm`
  clients of the full-service targets still open the plain method, though the server now serves the sibling for
  C2S services. Their S2S clients' machinery (`rotation.NewConn`, `StreamRotation.channel`, `RotatingChannel`)
  takes a token source; the `c2sm` token provider is one.
- **The Python rotating stream's end must not wait on a full buffer** (2026-11-23, #323): `RotatingStream._finish`
  (`chassis/servicemanager/rotation.py`) ends the caller's iteration with `await self._items.put(_END)` on a
  64-message queue. A caller that stops reading with the buffer full leaves that task waiting for good, and
  `_pump`, which turns a cancelled read into `_finish`, can then hold `asyncio.run`'s shutdown. Putting the end
  without waiting (`put_nowait`, and `__anext__` ending once the queue is empty and the stream finished) closes it.
- **chrono out of the generated Rust graph, the breaking half** (2026-11-18, #855, #862): 2.6.2 drops the unused
  `pbjson-types` and the logger chassis's unused `chrono`. chrono 0.4 (RUSTSEC-2026-0543) still reaches every
  generated Rust service through `prost-wkt-types` (the sys protos' WKT fields) and the `token` chassis's public
  `expires_in() -> Option<chrono::Duration>`. Replacing them changes the generated surface, so it is #862, for the
  next minor or major (its ROADMAP.md line is the hub's).
- **The c2d-server swift leg's socket is one path per machine, not per run** (2026-11-15, #778): the go, java and
  rust self-tests now serve on a socket under a per-run `mktemp -d` (`C2D_SELFTEST_SOCKET`, set by
  `cross-test/c2d-server/run.sh`): `just gates` runs the four legs at once, and on the one declared path
  (`/tmp/tacplus-c2d-server-check/daemon.sock`) a leg's daemon took another's socket, which hung the Java slow-bind
  run on main. The swift leg still serves its Go daemon on the declared path, because it tests the Swift
  `C2DClient()` default dial. Two gate runs in different checkouts on one runner can still collide there:
  `scripts/with-tree-lock.sh` serializes one checkout only. A machine-wide lock around that leg, or a Swift
  default taken from an env override in the test only, would isolate it.
- **Work down the generated tree's security baseline** (2026-11-18, #854): `security-baseline.json` records the
  generated tree's 8827 MEDIUM, LOW and UNKNOWN findings of the 2026-11-18 scan (Jody's decision on #854: baseline
  them, no issue per finding). #854's triage says what to read first, since a few of them may be real defects:
  semgrep `bypass-tls-verification` in the Go `x509.go` and `memberport.go` templates (check them against #359's
  fix), `missing-ssl-minversion` (Go httpclient, x509) and `weak-ssl-context` (Java `ImmutableCertChain`,
  `TypedClient`), spotbugs `REDOS`, `PATH_TRAVERSAL_IN`, `NP_NULL_ON_SOME_PATH_FROM_RETURN_VALUE` and the `AT_*`
  races, `python-logger-credential-disclosure` in the Python `client_im/manager.py`, and RUSTSEC-2026-0543
  (`chrono` unmaintained: a `time` or `jiff` dependency decision, like #308 and #320). Its "accept by design" list
  is the rest: G103 in protoc-gen-go output, `use-snakeyaml-constructor` (SnakeYAML 2.5), G101, G104, G304, B110,
  `DM_DEFAULT_ENCODING`, and the jitter math/rand hits. Each fix lowers its entries' counts: re-record them. The
  baseline keys a finding by file, so one template's finding is an entry per set that renders it (up to 19):
  keying generated findings once per template (looper-scan, the path less its `generated/<set>/`) would shrink
  the file by an order of magnitude and survive a set's rename.
- **stream-expiry Go leg: the short token's 3 s also pay for the first dial** (2026-11-13, #627): the Java leg now
  opens its long-token control stream first and waits for a tick, so the channel's TLS handshake is done before the
  3 s token is minted; a stream the server sees only after its exp is refused at open and its expiry mid-stream is
  never tested. The Go leg (`cross-test/stream-expiry/testdata/stream_expiry_behavior_test.go`) dials lazily
  (`grpc.NewClient`) under `-race`, and its three subtests mint their tokens before that first handshake: on a
  loaded runner they would fail the `earliest` check. `conn.Connect()` and waiting for `Ready` in `startServer` would
  close it. The Rust leg connects before minting.
- **Rust self-tests once per content hash in the other gates that run them** (2026-11-17, #843):
  `current-generation` runs each Rust chassis crate's self-tests once per gate for each source they test
  (`cross-test/lib/rust-selftest-once.sh`). `cross-test/consumer-contract/run.sh` runs some of the same crates'
  tests (`cargo test -p spiffe`, `-p grpcserver`, `-p collection`) per leg; `rust_selftest_reused` and
  `rust_selftest_record` with that gate's own `RUST_SELFTEST_PASSED_DIR` would skip its repeats. Go and Java were
  left as they are because the rule doesn't pay there. A Go fixture's tests take seconds once #841's keepalive test runs in one fixture only, and Go's own test
  cache can't help because the module path differs per fixture. The Java self-tests each run one class's `main`
  after the Maven compile every fixture needs anyway.
- **One dependency resolution for the generated Rust crates in every project** (2026-11-18, #843): a chassis
  self-test is reused only where `cargo tree` resolves the same versions and features, and several fixtures don't
  resolve them the same way. In requires-device the model types crate has `sha2` as a normal dependency, where
  main has it as a dev-dependency (#829's `moveUnusedCrateDependencies` places it by what each project's
  generated code uses). That turns on `memchr`'s `default` feature, so every chassis crate over `regex` or `memchr`
  (key/jwk/token, httpclient, logger, grpcconfig, servicemanager) is a different build there and runs again. If
  the generator pinned the features of such shared dependencies in `[workspace.dependencies]`, every project
  would get one resolution. More fixtures would then reuse their self-tests, and so would a consumer's
  `cargo test` cache. Since #812 runs a fixture's crates in one `cargo test`, features unify over everything it
  selects, the fixture's root package too, so a root package's dependencies can also make a chassis crate's
  build differ. The gate log lists each crate that ran with its hash, so a mismatch can be traced to its cause.
- **OpenJML in the arm64 build image** (2026-11-07, #257): `images/build.Dockerfile` installs the
  `openjml-ubuntu-latest` release on both platforms, and that release's JDK and solvers are x86-64: on
  `build:2.6` for arm64 (the looper's runners) `openjml` fails with `Exec format error`, so Java verification runs
  only on amd64 (the GitHub release and nightly runners). OpenJML publishes no Linux arm64 build. Options: the image
  installs `qemu-user` and wraps `openjml` and its z3 in it (how #257 ran it locally: ~5x slower), or OpenJML is
  built from source for arm64 with an arm64 z3 4.7.1.
- **ESC times out on validators with element loops** (2026-11-07, #257): under qemu emulation (~5x slower than
  amd64), OpenJML ESC proved the kitchensink's `Bounds`, `Level2` and `Level3` validators and 11 of the first 16 of
  cross-test/proto's `product` package, but ended UNKNOWN at the five-minute limit per attempt on validators that
  loop over elements and check each one's UTF-8 size (`Sizes`, `RepeatedScalars`, `Maps`, `Product`, `Supplier`,
  ...). Unmeasured on amd64, where `just verify` runs. If they still time out there: give each element loop a
  `maintaining` invariant (the bound so far), or check element sizes in a helper with its own spec, so ESC proves
  one iteration instead of the whole loop.
- **Retire JBMC from the build image** (2026-11-07, #257): `tacplus verify` runs OpenJML ESC for Java, not JBMC;
  `images/build.Dockerfile` still installs JBMC and `images/smoke.sh` still checks it. #196 removes the Java
  generator's JBMC harnesses (`pkg/internal/java/types/proof.go`).
- **JML specs for string byte bounds** (2026-11-07, #257): the Java validator measures a string with
  `getBytes(UTF_8).length`, which allocates, so no pure JML expression states `assert_max_size`/`assert_min_size` on
  a string and `jmlSpecs` writes none: they are fuzzed only. A validator that measures `getXBytes().size()` (the same
  bytes; protobuf-java caches them) would let ESC prove them. The same holds for `assert_float` (`in`/`not_in` still
  stream over a lambda) and the element bounds of repeated fields (checked in a loop ESC needs an invariant for).
- **TypeScript: `required_if_eq` on an enum sibling declared in another file** (2026-11-09, #464): the fix resolves
  the protobuf-es member against the enums of the message's own file, and for an enum declared elsewhere drops the
  `UPPER_SNAKE(EnumName)_` prefix when the value carries it, which is wrong for an enum whose values do not all share
  it. `helpers.ts` also imports only its own file's enums (`getEnumNames(pf.Enums)`), so such a check may not compile
  at all. Not verified: no fixture has the case. When the kitchensink (#196) lands, give it one, compiled with `tsc`.
- **The generated TypeScript model's `npm install` warns** (2026-11-09, #464): npm 11 reports `protobufjs`'s
  postinstall as an install script not yet covered by `allowScripts`, so every consumer's install of a generated
  model prints `npm warn install-scripts`. `current-generation` now holds only the `tsc` build to a clean log. Either
  the generated `package.json` names the script it allows, or the dependency on `protobufjs` goes, if nothing needs it.
- **CI's own jobs read `build.lock` instead of floating `build:latest`** (2026-11-12, #340): the build pipeline
  (tacplus-dev/build) runs this repository's gates in the image `build.lock` pins by digest, but
  `.github/workflows/nightly-gates.yml`, `nightly-security.yml`, `nightly-fixes.yml`, `site.yml` and
  `just gates-container` still run in `build:latest`, which moves on every release and every main push of
  `images/`. Reading the pin instead makes every gate run in the same image, and moving it one reviewed commit;
  the cost is a pin to bump after each release (a pin older than `images/` on main gates against an image that does
  not match the Dockerfile in the tree).
- **The G115 exception of #340 ends with the last of its four issues** (2026-11-15, #340): `security-exceptions.yaml`
  accepts each rule as a whole (the scanner knows no path key), so a rule's entry stays until every finding of
  the rule is fixed, and a new finding of an excepted rule is accepted silently until 2026-12-31. The issue of
  each entry's reason is the list to work down; the scanner growing a path key (tacplus-dev/looper) would let an
  entry shrink as its issues close instead of lapsing at once.
- **Retire the run-shell-injection bridge entry now that #584 has landed** (2026-11-19, #580): its reason names #584
  as the fix, and the 2026-11-19 scan of main reports no `run-shell-injection` finding. #580 retired its own
  `missing-user` entry the same way; this one is #584's to retire.
- **#340's 2026-12-31 also ends #376's G115, G404, G703 and gRPC-plaintext entries** (2026-11-16, #340): one id has
  one entry for both trees the build scans (the scanner takes the first of a repeated id, and fails on any expired
  one), so the rules #376 accepted in the generated code until 2027-02-14 or 2027-05-14 and #340 in the compiler's
  source until 2026-12-31 share #340's earlier date, Jody's. If #359 has not fixed the generated Go by then, renewing
  the generated code's part is a decision of its own; a path or tree key in the scanner would keep the two apart.
- **`prune_cargo_workspace` and its test assume cargo's old `target/debug/deps` layout** (2026-11-16, #340): with
  cargo 1.100 (the looper hosts' toolchain) a build leaves its rlibs under `debug/build/<crate>/<hash>/out/`, so
  `TestPruneCargoWorkspaceKeepsSharedDependencies` fails on the host ("the build left no rlib to prune") on main as
  well; the build image's pinned Rust still uses the old layout. The cross-test's pruning stops pruning, silently,
  the day the image's Rust reaches the new layout: teach `cross-test/lib/cargo-target.sh` both layouts.
- **A red gates run's security job cannot load the Go modules** (2026-11-18, #340): looper-build keeps the gates'
  built files (built/) only when the gates pass, so after a red run (pipeline 3085075375, the #840 flake) the
  security job scans a bare checkout, where the root module lacks the ignored `target/proto/spec/sys` that `just
  generate` writes, and govulncheck fails `loading packages` (a Problem) for it and every module whose `replace`
  points at generated code. It hides the real cause behind a second failure; keeping built/ on a red run too, or
  generating the descriptors in the scan (tacplus-dev/looper), would leave only the gates' failure.
- **Java state client topics: read which RPCs the state service declares** (2026-11-09, #454): the Go state client now
  calls only the topic RPCs `proto.StateTopicRPCSurface` says the state service declares. The Java one
  (`pkg/internal/java/types/statewrapper_gen.go`, the topic case) classifies a topic before its private check and calls
  both `publishTo<F>` and `subscribeTo<F>` unconditionally, so a private topic, or an event-C2S state service (a
  record/event topic with a `c2s` surface, or an exported root) that omits one of them, would not compile. Unverified
  by a build: no fixture declares those shapes. Read the same surface there and add the shape to the
  eventbus-backpressure fixture (or #196's kitchensink).
- **Concurrent legs inside the other cross-test runners** (2026-11-06, #300): `just gates` runs gates and their
  language legs concurrently, and current-generation its fixtures (#812), but the runners that run several fixtures or
  peers one after another inside one leg (memberport, redact, determinism, ...) still do. The gate wall-time table
  `just gates` prints names the longest; that one would move to a worker pool next.
- **One job scheduler for the gates** (2026-11-18, #300): `scripts/gates.sh` runs the gates on
  `cross-test/lib/jobs.sh` (dependencies, output in the order added, per-slot state, wall times), and
  current-generation its fixtures on `cross-test/lib/parallel-jobs.sh` (#812: labelled output in the order finished,
  stop on the first failure, per-worker cargo directories). The two landed side by side; one library would do both,
  with each caller choosing ordered or labelled output.
- **Ports and timing in cross-tests that now run side by side** (2026-11-06, #300): the first concurrent `just gates`
  (load ~22 on 8 CPUs) failed stream-expiry twice: the Go test dialled before its server listened, and the Java test
  minted a 3 s token before its TLS channel was up, so its streams reached the server after exp. #300 fixed both,
  and the DiskStoreTest flake below as far as a wall-clock bound allows (10 appends, a 60s bound, the writer joined
  before `close()`); the second run hit that flake, and the third a 1s bound in the Java cert-stream test (widened to
  20s). Other self-tests share the pattern of picking a free port, releasing it and binding it later (`freePort`),
  which two concurrent legs can race for; binding port 0 and reading the port back would remove it. And more
  wall-clock bounds sized for an idle machine may surface as concurrent gates load the runner. The rotation fixture's
  Java leg (#322) was one: 3 s tokens floored to whole seconds left its cutover ~0.5 s; it now uses 4 s tokens and a
  connected channel. Its Go and Rust legs keep 3 s and passed under the same load, but share that margin.
- **Land time per gate set in the nightly recap** (2026-11-06, #300): `just gates` now prints each gate's wall time
  and the total; tacplus-dev/looper#57's recap could read them from the land's log to show the gain per gate set.
- **gosec on the generated Go in tacplus's own gates** (2026-11-08, #359): #359 cleared G402, G123, G115, G404,
  G703 and G704 from the generated example service (fixed or `#nosec`-annotated with a reason in the templates),
  but nothing here keeps them out: tacplus-dev/looper#108's scan sees a regression only after a release. A gate
  (or a step of `current-generation`) that runs a pinned gosec over the generated Go of cross-test/proto and the
  example, failing on those rules, would catch it at land. The other rules gosec reports there (G103 in protoc's
  gencode, G104, G304, G101, G117, G301/G302) need a decision on each before they join it.
- **The generated Rust crates' dependencies from the generator, not from a scan** (2026-11-16, #829): cargo 1.100
  warns on a dependency nothing in the crate uses (`cargo::unused_dependencies`) unless another dependency pulls it in,
  and on an unused `[workspace.dependencies]` entry. The generated manifests declared every dependency any
  configuration might need, so #829 moves the ones a crate's generated source does not reference to
  `[dev-dependencies]` (`moveUnusedCrateDependencies`, `pkg/internal/rust/types/cargo.go`), by a textual scan for
  `name::`, `use name` and `extern crate name`. That is exact for the generated code today, but a crate reached only
  through a macro's expansion would read as unused. Each generator stating the crates its output uses would be exact
  by construction. Also: no gate builds the generated Rust with a cargo older than the runner's, so a manifest key a
  pre-1.100 cargo rejects (`[lints.cargo]` is one: "unused manifest key") would pass the gates.
- **One version resolver for `depends`/`src` and the fleet build** (2026-11-06, #214): `pkg/protosrc/resolve.go`
  (`findLatestPatchTag`, `compareSemverTags`, `exactVersionRe`) and `pkg/fleet/version.go` (`parseVersion`,
  `highest`, `resolveVersion`) each parse and rank `vMAJOR.MINOR.PATCH[-pre]` tags. #214 was the protosrc copy
  disagreeing with the fleet one on pre-releases. A small shared package (parse, compare, latest release of a line)
  used by both would keep "a line is its latest patch release, pre-releases only when named" in one place.
- **Point the GitLab build jobs at the `ci` cache, and give them a user namespace** (2026-11-06, #315): the
  build image reads its cache from the build's configuration, and the tacplus-dev/build jobs (looper
  `ci/build.gitlab-ci.yml`) are where that configuration belongs:
  - `TACPLUS_CACHE_BUCKET=gs://tacplus-ci-build-cache`;
  - `TACPLUS_CACHE_REGISTRY=<region>-docker.pkg.dev/tacplus-ci/build-cache` (tacplus-dev/cloud#5);
  - `eval "$(tacplus-cache env)"`, with `tacplus-cache restore` before the build and `tacplus-cache save` after it.

  Rootless BuildKit also needs `security_opt = ["seccomp=unconfined", "apparmor=unconfined"]` on the build jobs'
  containers (cloud's `jobs.build`, kept apart for this) and SETUID/SETGID among their capabilities (no `cap_drop`):
  newuidmap and newgidmap carry them as file capabilities (#830).
  Two things are not verified yet: sccache's GCS backend authenticating through Workload Identity Federation
  (external_account ADC), and gcloud's for the snapshots.
- **Hermetic base and build Dockerfiles** (2026-11-06, #315): the cache keeps an image's digest the same cold and
  warm, but two cold builds on different days still differ, because the inputs move: `apt-get update && upgrade`,
  `rust:1` and the other base tags, `npm install -g` and git clones by tag. Making the same commit give the same
  digest on any day takes snapshot.debian.org at a pinned date, base images by digest and locked tool installs.
  `images/cache-determinism.sh` proves the cache on a hermetic fixture.
- **images.yml: drop the buildx fallback, and `just images` in the build image** (2026-11-07, #315): #315's images.yml
  builds base and build with `tacplus-image-build` in `build:latest` and falls back to buildx
  on the runner while `build:latest` predates the tool. Once a `build:latest` carrying `tacplus-image-build` is published, the fallback
  is dead code: drop it. `just images` still builds with the host's Docker (it bootstraps an image set from
  nothing); a variant that builds inside a published build image would need BuildKit named contexts for base → build.
- **The rest of the way to a 45-minute full gate run** (2026-11-16, 2026-11-18, #812): `current-generation` runs each
  language of each fixture as a job on a CPU-bounded pool, each Rust fixture's self-tests are one `cargo test` (a
  `cargo test -p <crate>` per crate rebuilt the generated types crate a minute each time, for each of ~13 crates), and
  cross-test crates build without debuginfo or incremental caches. On an 8-CPU arm64 runner from a cold
  `target/cross-test` it takes about 33 minutes (47 before the last two, 2h26m serially): Go jobs about a minute,
  Java 4–6 (tenantrecord 13), Rust 3.5–7.5, the two-build Rust fixtures (`tenantrecord`, `tenantroute`, `redo`) 12–15,
  and those start last, so they are the tail. Since #977 (2026-11-26) `just gates` runs current-generation's shards
  in its own pool, `GATES_JOBS` the CPUs, each two-build fixture as a shard per build, every Rust shard as soon as
  `rust/base` passed, and prints each shard's time and the pool's use of the machine (slots busy, CPU time against
  the CPUs'). Next: (1) read those numbers off a full run on an 8-CPU runner and tune from them: the order the shards
  start in (longest first, by measured time), the cap, and the share each job builds with (half the pool's width
  today, so that `go test ./...` alone is not slowed); (2) each slot copies `rust/base`'s target directory (several
  GB) and compiles the dependency feature sets a fixture adds: since #979 every leg's rustc goes through sccache
  when it is on PATH (`cross-test/lib/sccache.sh`; the wrapper drops the slot's `CARGO_TARGET_DIR`, which sccache
  keys on: 0 hits across two slots with it, 522 of 530 without) and the shards copy nothing, but the pinned build
  image (`build.lock`, the 2.6 line's) predates #315 and has no sccache, so the gates still copy until a pinned
  image carries it (#830, #954); then measure. Each Rust step's time now prints in its shard's log (#979): on the
  looper VM beside another build, `cargo check` of the whole workspace took 88-150 s a shard and the self-tests'
  `cargo test` 97-237 s (rust/base 150 s and 237 s), the doc tests 6-20 s. The check compiles every member in check
  mode, the generated types crate among them, and the test then compiles them again: a `cargo build` of the
  workspace in its place would share its units with the test's, at the cost of codegen for the members no test
  builds; measure both on one fixture before choosing, a lighter rust/base among them; (3) `cross-test/consumer-contract/run.sh` still runs a `cargo test -p` per crate
  (not in `just gates`). `gates.yaml`'s `full: minutes` follows. The first sharded run (2026-11-26, #977, a looper VM's
  local build of 41742e1a, shared with other builds: load average about 50 on 8 CPUs, the gates' own processes 2.5 CPUs
  on average) took 3770 s, current-generation 2455 s of it with the slots 97% busy: `rust/base` 850 s, then the other
  Rust shards 365-683 s, Java 245-432 s, Go 68-107 s. Its critical path is `rust/base` and then its slowest
  dependant, about 1530 s there; the rest was the slots' queue, so the 15 minutes need less work per shard (2 above,
  and `rust/base` itself), not only more slots.
- **More gate slots than CPUs** (2026-11-26, #979): a full `just gates` on the looper VM (main 654a7b3, beside
  another build: load average 40-70) took 2457 s with its 8 slots 97% busy but its own processes using 3.9 of 8 CPUs
  (48%), so the slots, not the CPUs, are the queue: legs wait on JVM start-up, sockets and their tests' sleeps. Try
  `GATES_JOBS` at 1.5 or 2 times the CPUs on a machine with nothing else building, against the timing-bound tests
  (rotation's, now bounded by a hang rather than a fixed 5 s, #979).
- **The OS-advisory image fix a looper can run** (2026-11-15, #757, #711): an OS package advisory is fixed by
  rebuilding `base` (docs/VERSIONING.md, Images). That takes a `rebuild: base` dispatch on main and on each
  `release/X.Y`. The looper's token cannot dispatch a workflow (HTTP 403, no `actions: write`). Its only image path is
  a patch tag, which rebuilds `build` alone, so #711 left `base:latest` and every `base:X.Y` behind. Either give the
  looper `actions: write` on images.yml, or have a release tag's run also rebuild `base` and push `base:X.Y` (and
  `base:latest` for the newest release). The second changes the Images contract in VERSIONING.md and costs a base
  build per release.
- **The nightly image sweep scans every `base:X.Y`** (2026-11-15, #757): the Published images job scans `base:latest`
  and each `build:X.Y`, but no `base:X.Y`. A stale line base is found only once a patch builds on it. Add
  `base:${minor#release/}` beside `build:` in the loop of `.github/workflows/nightly-security.yml`.
- **A release tag never builds on another line's base** (2026-11-08, #283): a release tag's Images run starts its fresh
  `build` from its line's `base:X.Y`, else `base:latest` (main's). Before any line rebuild had pushed a `base:X.Y`, the
  v2.1.13–v2.5.13 runs took main's base (Swift 6.4.2) under their lines' Swift 6.1.3 plugins and failed the smoke test
  (`protoc-gen-swift: undefined symbol`, run 42428041155), and so did every 2.1–2.5 tag cut since (v2.1.14–v2.5.15).
  v2.0.20 passed, so `build:2.0.20` carries main's SDKs instead of 2.0's pins. Once the weekly line rebuild has pushed every `base:X.Y`, the fallback is unreachable for a supported
  line (the line rebuilds of 2026-11-09 pushed them, and the 2.1–2.5 tags cut since pass). Still, the plan step should rebuild `base` from the tag when `base:X.Y` is missing, not fall back. That change
  goes in `images.yml` on main and on each `release/*`.
- **Python generation mangles a proto line whose trailing comment holds `[`...`]`** (2026-11-09, #284): an enum value
  with a field option and a comment containing `[/...]` (#284's first `JWT_PAYLOAD_TYPE_TID` comment) came out of the
  Python generator's proto copy cut from the option's `[` to the comment's `]`, so protoc refused `sys/token.proto` and
  the determinism gate failed. The comment was reworded; the option stripping should stop at the option's own `]`
  (not found by name in `pkg/internal/python` in the time this run had). A `bug` to file.
- **Run every generated Java MCP gate in current-generation** (2026-11-04, #284): `<Service>McpTest.java` (the
  main-method gate `mcp_dispatch_tests_gen.go` emits beside each Java MCP dispatch) ran nowhere: no gate executed
  it, so its tool-reach and stamped-field cases were compile-checked only. #284 runs the tenant-routed ones (the
  session tenant from the `tid` claim); running all of them (`run_java_generated_self_tests`) is the rest, after
  checking each fixture's gate passes today.
- **Product layer: a product's owner tenant reads across tenants' custodian data** (2026-11-10, #475; option B of
  roadmap/PRODUCTS_LAYER.md section 5, which Jody chose as the follow-up to A on 2026-11-09): `fleet.yaml` names, per
  product, the tenant that owns it (`products: [{name: ledger, owner_tenant: <tid>}]`); an rpc flagged
  `RPC_FLAG_ACROSS_TENANTS` on a product service admits only **users** of that tenant holding an entitlement to it
  (C2S, run-time policy within build-time entitlements), its `caller_tid` binding is lifted on that rpc only, and
  every call is audited with the owner user's identity. No service principal ever reads across tenants. Why: a
  product owner's billing view over per-tenant usage. Needs its own design and a ROADMAP.md line; after #475.
- **cmd/soundness reads every file through one os.Root** (2026-11-13, #566): #566 moved the store-vendor walk onto
  `os.OpenRoot` (gosec G122, HIGH). gosec still reports eleven G304 (CWE-22, MEDIUM, not blocking) in
  `cmd/soundness/main.go`: the other checks `os.ReadFile` paths joined under `-root` (the SPIFFE walk among them).
  Opening `-root` once and reading through it everywhere would clear them and confine the whole gate to its tree.
- **No PEM header gitleaks reads as a private key in generated service managers** (2026-11-13, #628): the
  Python, TypeScript and Rust `service_manager` templates assemble a PKCS#8 PEM at run time, and gitleaks'
  `private-key` rule matches from the `-----BEGIN PRIVATE KEY-----` literal to the END one across the code between
  them (5 matches; tacplus's `.gitleaks.toml` allowlists the templates, a generating project's scan does not). Build
  the header from a label (`"PRIVATE KEY"`) so no line pair reads as a key, as #628 did for the chassis tests' token
  fixtures (`TestNoTokenShapedLiteralInTemplates` could grow the rule).
- **`just example` in a gate** (2026-11-13, #583): the example (`example/run.sh`: generate, build and run the Go
  server and the TypeScript client, and check the server refuses a non-loopback `NOTES_ADDR`) runs in no gate set,
  not `just gates` nor `gates.yaml`'s map, so the README's quick start can break unnoticed. Add it to the full gates,
  and map `example/**` to a set that runs it.
- **runtime-smoke in a gate, so its setup cannot rot unseen** (2026-11-13, #581 → #657): `cross-test/runtime-smoke/run.sh`
  has failed at setup since #161 removed the `health.proto` it copies, and no land noticed because `just gates` does
  not run it (only `cross-test/justfile` and the nightly matrix do). Once #657 makes it pass, put it in a gate set
  (or `gates.yaml` for `cross-test/runtime-smoke/**`) so a land that breaks it fails.
- **runtime-smoke in the gates** (2026-11-13, #657, #665): `cross-test/runtime-smoke/run.sh` runs only in the nightly
  matrix, so it rotted unnoticed through four contract changes (the 2.4 sys contract #161, MCP-7 doc comments,
  `grpc-servers` scoping, PAR-9's Python error shape). `--consumer typescript` takes a few minutes; add it to
  `just gates`, and `--consumer python` once #665 lets Python generate a client of a C2S service.
- **Work down the security baseline: file modes, unchecked errors, pinned actions** (2026-11-14, #585):
  `security-baseline.json` records the 1331 MEDIUM and LOW findings of the 2026-11-13 security run (Jody's decision
  on #585: a baseline, no per-finding exceptions), so the scan files only new ones. The issue's triage still holds:
  gosec G306 (317) and G301 (233), files and directories the compiler writes wider than 0600/0750, want one policy
  (0644/0755 is intended for generated source: tighten the rest, or `#nosec` with a reason); G104 (46) unchecked
  errors want checking; the 34 `github-actions-mutable-action-tag` want actions pinned by SHA (overlaps #194). Each
  fixed finding lowers its entry's count: re-record with `looper-scan baseline` (tacplus-dev/looper#146).
- **More of the gate map** (2026-11-08, #266; the per-language sets landed in #976): `gates.yaml` runs the full gates
  for any change under `pkg/**` but one language's generator and templates, `cmd/**`, `cross-test/**`, `scripts/**`
  or `.github/`. Next: `cross-test/<gate>/**` to that gate's legs alone (its fixture, its testdata; `cross-test/lib/**`
  and the shared protos stay full); `cmd/sitegen/**` and `site/**` to `just docs` plus `just site`;
  `.github/workflows/**` to a workflow lint. Also for `just docs`: it checks relative links but not their anchors
  (GitHub's heading slugs) nor external URLs, and does not build the site (`site/docs/*.md` link to site paths;
  `just site` checks them, needing a venv).
- **The per-language gate sets' cost** (2026-11-26, #976): a change to two languages runs both sets one after
  another (the looper joins them with `&&`), so soundness, test and floors run twice and go+rust's estimate (120
  minutes) passes full's (100). Either the looper runs full when the chosen sets' minutes reach full's (a
  tacplus-dev/looper change, for every repository's map), or `just gates-lang` takes several languages and runs the
  union of their legs once (a map whose sets are language lists). `floors` generates every language for each set: a
  `--language` filter on `scripts/floors.sh` would save its other six generations. And a set runs
  `current-generation-<lang>` whole, on a pool of its own, not as #977's shards in the gates' pool: a
  `current-generation-<lang>-shards` recipe (`--list-shards` filtered to the language) would let it.
- **The single-account mode of the sign-in config** (2026-11-07, #224): `signin.ACCOUNT_MODEL_SINGLE` is declared
  (one account type, no personal links, no link prompt) but its exact shape is still to be designed (tacplus-dev/roadmap
  `decisions/login-flow-panel-2026-10-29/SYNTHESIS.md`, last note): which `Tenant` fields it ignores or refuses (a
  people tenant, `merge_redirect_seconds`, `registration_grace_seconds`) is a cross-field rule tacplus can generate once
  it is decided.
- **Refresh the embedded Public Suffix List** (2026-11-07, #224): `assert_registrable_domain` matches the snapshot in
  `pkg/internal/common/publicsuffix/public_suffix_list.dat` (VERSION 2026-11-06). The list changes weekly; a stale
  snapshot accepts a suffix added since. A scheduled job (the nightly scan) could download it and open a pull request
  when it moved, so a release always carries a recent one.
- **One copy of the public suffix table per generated module in Python and Rust** (2026-11-07, #224): Go, Java and
  TypeScript emit the ~10k-rule table once, in their utils; the Python emitter appends it to the `helpers.py` of each
  proto file that uses `assert_registrable_domain` (the shared `validation.py` template's helpers raise `ValueError`,
  not the generated error type), and the Rust emitter writes it into each `.rs` module that does. Today only
  `sys/signin` uses it; a shared module in each would keep one copy as consumers adopt the annotation.
- **Field asserts on a oneof member are dropped** (2026-11-07, #224): the Go and TypeScript validation emitters (and,
  by the shape of their code, the others) honour only `assert_max_size` on a field inside a oneof and skip every other
  `assert_*` without an error, so such a field is generated unvalidated. Either validate them or refuse them at
  generation; a silent drop fails open.
- **The enum refusal text of `assert_enum_not_in`** (2026-11-07, #224): Go and Java name the refused value
  (`must not be IDP_PROTOCOL_SAML`); Rust and Python name it when the enum is declared in the same file, else its
  number; TypeScript gives the number. The model carrying enum value names to every emitter would make them one.
- **Kernel holds `signin.Tenant` and `signin.Deployment`** (2026-11-07, #224): tacplus declares the types and their
  validation only; kernel's tenant config slice holding them, the domain lookup, the binding ceremony and the
  per-tenant pairwise identifiers (SYNTHESIS.md A) are tacplus-dev/kernel's to build.
- **Kitchensink members for the sign-in asserts** (2026-11-21, #224): `assert_registrable_domain`,
  `assert_not_address` and `assert_enum_not_in` landed before #196's kitchensink was on main, so no member uses them
  and a service build's coverage check reports them uncovered. A kitchensink member carrying each (Go, Java, Rust,
  Python, TypeScript) and `just kitchensink-manifest` would cover them.
- **Release-line Python floors that need no interpreter drop** (2026-11-08, #264): release/2.0–2.5 keep Python 3.8,
  so their cryptography/requests/urllib3 floors stop at the newest 3.8 releases (47.0.0, 2.32.4, 2.2.3) and 13
  advisories stay accepted in their USER.md. A floor keyed by `python_version` marker (`"urllib3>=2.8.0;
  python_version>='3.10'"` beside the 3.8 floor) would give 3.10+ consumers on those lines the clean floors with no
  interpreter drop; the generator writes one floor per name today. Also: the build image's venv pins
  `cryptography>=49.0.0` (images/build.Dockerfile); PYSEC-2026-3552 now needs 50.0.0.
- **Images on Google Artifact Registry through WIF, ghcr.io deprecated from 2.6** (2026-11-07, #223; parked by
  the hub 2026-11-07): publish `base`, `build` and the services' release images to the public
  `us-docker.pkg.dev/jody1-328023/tacplus`, pushed by the GitLab publish job with a short-lived GCP token from its
  `id_token` (WIF), so no registry token exists to store or renew. Here: `pkg/ghcr` becomes a registry client that
  follows the `WWW-Authenticate` challenge (so it pulls anonymously from Artifact Registry as well as ghcr.io),
  `tacplus pin` and the fleet build name the new images, and CI, the launcher and the docs move to them. Open
  questions in #223: who builds base/build, whether `build` drops amd64 (the GitHub CI that runs inside it is
  amd64), the fleet's images for owners other than tacplus-dev, name collisions in the flat repository, the service
  tag form, and how build:2.6.0 gets there. Parked: Jody chose ghcr.io release images pushed by GitHub Actions with
  `GITHUB_TOKEN` for now (tacplus-dev/roadmap decisions/ci-on-gitlab-2026-10-27.md, addendum); revisited with
  tacplus-dev/looper#17, the GitLab build pipeline.
- **The flock service, and its gossip between clusters** (2026-11-06, #206): tacplus now generates the merge, the
  unique check, the marks and the clock of a flock table, but nothing runs copies yet. The service needs: the
  explicit, versioned membership list that purge counts (not health state); gossip of whole entries over generated
  S2S clients to a peer in another cluster (#204, the ROADMAP entry shared with redo#1); catch-up by comparing the
  generated digests (a Merkle tree over them); serving nothing until a returning copy has caught up; quarantine
  alerts for the overlaps `overlap` refuses to merge; and the panel's rate breaker (stop and quarantine if merges
  exceed N per window). The issue names it a separate follow-up; it needs Jody's design OK, and
  tacplus-dev/people#1 builds on it.
- **flock: `scope: FLEET` instead of the `flock` label** (2026-11-06, #206): the design note's 2026-10-29 *Decided*
  section replaces the flock service with an annotation-driven replication capability (`scope: FLEET` / `CLUSTER` on
  a `(spec.service_state)` root) hosted by kernel. #206 built what the issue and ROADMAP.md say (`label: ["flock"]`),
  behind the single predicate `parser.IsFlockRoot`; moving to `scope` is that predicate plus the annotation, the
  generated merge unchanged. Needs Jody's decision.
- **flock: compaction of set ops** (2026-11-06, #206): every add and remove of a SET_* element is kept as an op in
  `flock_meta`, so an entry's metadata only grows (a device updated daily adds an op a day). The panel asked for
  tombstone cleanup of the causal metadata: an op every copy has acknowledged, and that no live op names, can be
  folded away, with the same every-copy rule as marks. Needs the membership list (the service, above).
- **flock: what the generator refuses today** (2026-11-06, #206): a map of scalars, `oneof` and proto3 `optional`
  fields, IMMUTABLE or nested messages inside a set element, and a map anywhere in a value stored whole (no
  canonical encoding). Each could merge with more design; none is needed by the people table.
- **flock: the consistency contract in the generated type, and new fields** (2026-11-06, #206): the panel's point 10
  (a caller that reads, checks and writes against a table that moved to flock compiles and silently loses the
  guarantee) is still open for Jody; and a new field should take no writes until the whole fleet runs a build that
  knows its rule, which needs the service's membership and versions. Rules are pinned in `registry.yaml` so a
  released rule cannot change.
- **flock: `flock` in `just gates`** (2026-11-21, #206): `just flock` (go, java, rust) passed inside the full
  gates (build pipeline 3096760179), but #206 landed without it on the `gates` line: #262, queued ahead, edits
  that same line (adds `uni-config`), and every batch holding both was sent back on a `justfile` conflict. Once
  #262 is merged or out of the queue, add `flock` before `tacplus-script` (the #206 follow-up commit).
- **trace_id in 2.7: field 200 on every sys wire message, additive traced helper forms** (2026-11-06, #313 → #327):
  the hub's split of #313. Every request-building generated helper (state clients' `Fetch`/`Delete`/`Query`, record
  readers, record-plane and kernel state helpers) gets a form that takes `trace_id`, beside today's, and every sys
  contract wire message gets field 200 (not `spec.proto` option schemas, `<T>Public` or `TraceEvent`). Unblocks
  coop#7, oauth#19, blobstore#12, llmproxy#18/#19, mailbox#12, stratum#29. On ROADMAP.md since 2026-11-06.
- **trace_id beyond #327: generated/ protos, the approval check, the EventBus subscriber, `Set`** (2026-11-08, #327):
  #327 puts field 200 on every `sys/*.proto` message and a traced form on the state clients, record readers and
  `TokenForExternal`. Left: the protos tacplus synthesizes under `generated/` (`entitlements`, `persisted_store`,
  `services`, and `eventbus_support`'s wire values `EventBusRecordMeta`/`*SubscribeRecord`) still get no field 200,
  since `InjectTraceFields` skips every infra proto outside `sys/`; the `(spec.rpc).requires_device` gate's
  `BrokerPendingApprovalRequest` is a call the inbound request causes, and goes out untraced because the gate does not
  pass the request's `trace_id` to the generated check; the EventBus `Subscriber` builds its `SubscribeRequest`
  untraced (a subscription is usually not caused by one request, but Go/Rust already take options it could go in),
  and so does the Go state client's topic `Subscribe` (#454 made it send the caller's `SubscribeRequest`, which
  mirrors `eventbus.SubscribeRequest` and has no field 200);
  a state singleton's `Set` sends the value it stores, so it cannot carry a trace without the state service clearing
  it on the server side; Go's Empty-input gRPC client methods (infra RPCs only, after `SwapEmptyInputs`) build
  `emptypb.Empty`. Each is a design choice for the 3.0 contract (#328).
- **trace_id in 3.0: one traced signature per helper; a message without field 200 fails generation** (2026-11-06,
  #313 → #328): drops the untraced forms #327 adds, and turns today's silent skip of a message that lacks field 200
  or declares `trace_id`/`noop` at another number (`InjectTraceFields`) into an error (a warning from 2.7). Needs a
  ROADMAP.md line citing #313.
- **Trace clearing on queue and command state paths** (2026-11-06, #313): the #313 patch clears `trace_id` where a value is
  stored (state `Put`/`Set`, store-derived subscriptions, store wrappers, disk helpers) and leaves it on topic
  publishes, queue offers and commands, which deliver a message to a consumer that propagates it. If a queue is to
  count as storage (its items persist until taken), the consumer loses the trace; that is a decision, not a fix.
- **State client parity for queues and commands in Rust and Java** (2026-11-09, #456): #456 gave the Java
  `StateClient` the queue accessors Go has (`offerTo<F>`, `consumeFrom<F>`, `peek<F>`, `size<F>` for a `(spec.queue)`
  and a `(spec.priority_queue)`). The Rust state client (`pkg/templates/rust/types/statewrapper.rs.tmpl`) still has
  none: it compiles, but a Rust service reaches its queues only through the raw tonic stub. And the Java generator
  collects `(spec.command)` boundaries (`javaCommandBoundary`) but its template renders none, where Go has one method per
  command. `cross-test/proto-statequeue` is where the Rust queue leg would be asserted.
- **release/2.0: a Go service that is its own store does not compile** (2026-11-09, #456): on 2.0, a service declared
  `{name: "helper", store: true}` with no gRPC service and its own `ServiceHealth` root gets a Go
  `chassis/service_im/service_im.go` whose `audienceFromMethod` switch repeats `case "grpcconfig"` (the fixed
  infrastructure case, then the state-store mapping of `ServiceConfigState`), so `go build` fails. 2.1 and later
  generate the same proto cleanly. #456's 2.0 backport gives its fixture a gRPC service instead. If 2.0 is to get
  the fix, it is a `bug` against that line.
- **release/2.0: its unit tests are red on cargo 1.100** (2026-11-21, #456): `TestPruneCargoWorkspaceKeepsSharedDependencies`
  fails on release/2.0 itself ("the build left no rlib to prune"), so every 2.0 pull request's "Unit tests and
  soundness" check is red (#881's is). Main fixed the test for cargo 1.100's layout in 88caed68 (#829); release/2.0
  never got it. Backporting #829's test change would make 2.0's check meaningful again.
- **Kani harnesses beyond integer bounds** (2026-11-06, #211): the generated Rust harnesses now prove only that
  `validate()` refuses each integer/enum bound, and skip a message whose `validate()` reaches the regex crate (Kani
  0.68 panics compiling it), a nested message, a map field (prost's `HashMap` default makes a syscall Kani does not
  model) or a sensitive check; they stub `std::fmt::format`, which Kani cannot finish. Wider coverage: bounded
  strings/bytes (`kani::vec::any_vec` behind `String::from_utf8`) for the size checks; same-package nested messages
  (the generator can see their validators); a stub of `RandomState::new` for map fields; regex validators stubbed by
  `#[kani::stub]` with the claim restricted to the other fields. The "accepts every value inside the bounds" claim
  needs a model of every check of the message, which #196's kitchensink is the place for.
- **Verification only under the nightly set in gates.yaml** (2026-11-04, #275): once tacplus-dev/looper#48 maps
  tacplus, its `gates.yaml` lists `just verify` under the nightly and release sets only, never a land's. tacplus has no
  gates.yaml yet, so #275 landed without it.
- **`just verify` covers only cross-test/proto** (2026-11-04, #275): it generates the base fixture in Rust and Java and
  verifies its harnesses. The kitchensink (#196) is the set meant to cover every feature; when it lands, `scripts/verify.sh`
  generates it instead and `scripts/verify-changed.sh` watches it.
- **Redact<T> across packages, and the sys types that hold secrets** (2026-11-04, #274): a sensitive tree must stay
  in one proto package (a sensitive field whose type is in another package is refused at generation), and no sys
  type carries `(spec.sensitive)` yet, though several hold secret material (`sys/token.proto` tokens,
  `sys/tlscert.proto` / `sys/key.proto` private keys, `sys/credpair.proto`). A uni-config that embeds one fails the
  sensitive-name gate (as it should) with no way to annotate it. Calling the other package's generated redact (Go
  `pkg.RedactT`, Rust crate path, Java helper class) and annotating the sys types would close it.
- **Redact<T> and sensitive validators in the client-only targets** (2026-11-04, #274): Python, TypeScript, Swift
  and C# generate neither `Redact<T>` nor the required/entropy check of a sensitive field (the issue asks for them
  "where useful"). A client that sends a secret could check its entropy before sending, and a client-side tool could
  show a redacted view. Needs a decision on which targets get which half.
- **Kani harness for a message with a required sensitive field** (2026-11-04, #274): the generated Rust proof harness
  asserts that a default message validates, which a required sensitive field makes false, so `proof.go` skips the
  harness for such a message. A harness that builds a valid value first would keep it covered.
- **Sensitive leaves in a oneof** (2026-11-04, #274): `(spec.sensitive)` on a oneof member, and a sensitive tree that
  reaches a string/bytes/message oneof member, are refused at generation. Supporting them is per-language oneof
  plumbing (Go wrapper structs, prost enums, Java cases), like the legacy Sanitize<T> recursion has.
- **The sensitive-name gate on every message in 3.0** (2026-11-04, #274): the gate checks only the trees of messages
  with a generated `Redact<T>`, so a proto with no sensitive annotation is not gated and no existing consumer's build
  breaks in a 2.x minor (docs/VERSIONING.md). Gating every message in 3.0 would catch a secret in a type nobody has
  marked yet, at the price of a `(spec.not_sensitive)` on each `page_token`/`partition_key`-style name.
- **Remove the legacy egress redaction in 3.0** (2026-11-04, #274): `(spec.redact)`, `(spec.data_entitlement)`,
  `RedactMode`'s egress modes, `Sanitize<T>`, `<T>Entitlement`, `Build<T>Entitlement`, `sys/entitlement.proto`'s
  `EntitlementLevel` and the pseudonymize helpers are deprecated in 2.6 (generation warns); 3.0 removes them. The
  record store's egress (Sample/Pull/Watch) would then apply `Redact<T>` or nothing, a decision for the store.
- **A `just generated <dir>` recipe, so the build's security checks scan what tacplus emits** (2026-11-03, nightly
  scan): the build pipeline scans a repository's generated code only through a `generated` recipe (looper README,
  Security checks; looper#49, which notes "tacplus has no `generated` recipe yet"). tacplus has none, and
  `dependencies.yaml` is not a lockfile, so no scanner reads the versions the generated manifests declare. Tonight's
  scan found advisories in exactly those: #312 (TypeScript, Rust, Go), #309 (Python build/dev floors), #308
  (rustls-pemfile), after #264. A recipe that generates the cross-test models for every language into `<dir>`, with a
  lock-resolved manifest per language, would make OSV-Scanner and cargo-deny catch the next one in the build.
- **golang.org/x/crypto back on a tag** (2026-11-09, #312): GO-2026-5932 (openpgp, unmaintained) is fixed only in
  v0.58.1-0.20261009181718-2f1f834330b2, after v0.58.0, the newest tag, so five fixture dependencies.yaml and eight
  harness go.mods pin that pseudo-version, and `scripts/go_dependency_floor_test.go` floors it. Nothing generated
  imports openpgp. x/crypto tagged v0.59.0 on 2026-11-11 (OSV: clean, go 1.26.0 like the pseudo-version,
  x/net v0.62.0 like the pins): move the pins, `cross-test/lib/go-compile.sh` and the floor to it, and the backports.
- **SpotBugs over the rendered Java chassis, in the `generated` recipe (#376)** (2026-11-14, #712): no gate runs
  SpotBugs on the Java chassis templates, so kernel's security job found `USO_UNSAFE_OBJECT_SYNCHRONIZATION` in the
  generated `CollectionRuntime` (HIGH, failing the fleet's policy) in a consumer's build, not here. #712 fixes it and
  pins the shape with a Go test over the template text, which is a floor, not SpotBugs. When #376's recipe scans the
  kitchensink, it should run `spotbugs -effort:max -low` on the compiled Java chassis; today's remaining findings on
  `CollectionRuntime` are low priority (`DE` on the drain's `changeIter.close()` catch, `THROWS` on its rethrow), so the
  recipe would start green at the fleet's HIGH threshold. Checking for a monitor on `this` in every Java template
  (`synchronized` methods, `synchronized (this)`, `X.this`) under `cmd/soundness` would catch the next one before any
  scanner: `grep -n 'synchronized (\(\w*\.\)\?this)\|^\s*\(public\|private\|protected\)\?\s*synchronized ' pkg/templates/java -r`
  lists the candidates: 15 templates on 2026-11-14 (ServiceConnection, the minters, the subscriber), a sweep of its own.
- **Path-scoped security exceptions, so accepting a rule in the generated code does not accept it in the compiler**
  (2026-11-14, #376): `security-exceptions.yaml` matches a finding by id alone, across both trees the build scans
  (the checkout and `generated/`). Accepting G115, G404, G703 or G704 for the generated Go (#359's Also list) also
  accepts them in tacplus's own source, where nobody reviewed them. A `where` prefix on an exception (say
  `generated/`) would scope it; that is a looper change (looper-scan's `read_exceptions` and policy), to propose
  there. Update (nightly scan 2026-11-21): looper-scan now reads a `where` on an exception (looper README, Security
  checks). Its report (run 20261121T093834Z-scan, summary.md "Warnings") warns on each of the 74 rule-wide
  entries, for example "exception G404 has no where: it accepts G404 in every file (scope it: where)". So what is
  left is tacplus's part: give each entry in `security-exceptions.yaml` the `where` its reason names
  (`generated/` for #359's list, the named compiler files for the #340 bridge), and drop the header comment saying
  the scanner has no path key.
- **Make the generated tree cheaper to scan** (2026-11-14, #376): `just generated` writes 21 proto sets, 57 trees,
  about 1.5 GB, most of it the same chassis and sys types again in each set. On a loaded runner the generation took
  about 25 minutes with 3 sets at once, and `looper-scan local` spent 66 minutes in Semgrep alone (4.4 GB resident). #196's kitchensink (one proto set reaching every emitter) replaces the list; until then, a
  set could keep only the files no earlier set generated identically, at the price of trees that no longer build.
- **Compile every fixture in every language** (2026-11-14, #376): the current-generation gate compiles c2sonly in Go
  only, healthonly and zerorpc in Rust only and eventbus-backpressure in Java only, so `just generated` scans them in
  those languages only. eventbus-backpressure's Go output does not compile (#454); the others are untested.
- **SpotBugs per directory in looper-scan** (2026-11-14, #376): looper-scan runs SpotBugs once over every
  `target/classes` of a tree, in its default 768 MB heap. Four sets of generated Java bytecode run it out of memory
  (and the tool error fails the scan), so `just generated` compiles one set's (requiresdevice). One run per
  directory, as gosec and govulncheck do, or a `-maxHeap`, would let every set's bytecode be read: a looper change.
- **clippy-clean generated Rust** (2026-11-14, #376): the security scan of `just generated` runs clippy with
  `-D clippy::correctness -D clippy::suspicious` and finds `absurd_extreme_comparisons` (the validator checks an
  unsigned field's `assert_ge: 0` as `x < 0`), `empty_docs` and `empty_line_after_doc_comments`, accepted in
  `security-exceptions.yaml` until 2027-02-14. Skip the comparison for unsigned fields, emit no empty doc comment,
  and add a clippy run with those lints to the current-generation gate so they stay out.
- **Run outbound-deadline in the gates** (2026-11-03, #310): `cross-test/outbound-deadline/run.sh` (the issue #38
  deadline and lifetime facts, and with #310 the trace fact) has no recipe and runs in no gate or workflow, so a
  regression there is invisible. #310 adds the recipe; joining the mandatory `gates` set is a cost decision (one
  more Rust model build per gates run). It had already rotted unseen: since #161 the consumer-contract tree
  generates ContractApi only with `grpc-servers: all`, so the fixture no longer compiled until #310 set it.
- **server-runtime and keepalive-rust have likely rotted the same way** (2026-11-08, #310): both generate the
  consumer-contract tree without `grpc-servers: all` and import the `contract::contractclient` modules (and `c2sm`)
  it then drops, as outbound-deadline did, and neither has a recipe or runs in any gate. Each needs the setting, a
  recipe, and a run to confirm; then the same gates cost decision.
- **Decide whether `assert_float` with only `not_in` refuses NaN** (2026-11-02, #256): a field with any of
  `gt`/`ge`/`lt`/`le` now refuses NaN in every validating language, and `in` always refused it (no candidate is
  within epsilon of NaN). A field whose only constraint is `not_in` still accepts NaN, which is arguably right (NaN
  is not one of the forbidden values) but surprising next to the rest. Refusing NaN on any `assert_float` (or adding
  an explicit `allow_nan`) would make it one rule; that changes accepted input, so it belongs in a minor.
- **Release lines still push the deprecated `tacplus/tacplus` image** (2026-11-02, nightly scan): docs/VERSIONING.md
  on main says `tacplus/tacplus` "is never pushed again" and its tags "up to the 2.5 line stay as they are", but each
  patch tag runs its release branch's pre-#193 `images.yml`, which builds and pushes it (`tacplus:2.4.7` and
  `tacplus:2.5.7`, 2026-11-02, Images runs 41460207658 and 41460215925) `FROM` a `build:X.Y` that is never rebuilt
  (#283). Either say in VERSIONING.md that the 2.0–2.5 lines keep pushing it until they leave support, or stop
  pushing it from the lines. Consumers of the 2.x `tacplus` image would then stop getting patches through it.
- **Retry Go module downloads in CI** (2026-11-02, nightly scan): CI on main at ce69bf89 (run 41456279271) failed
  `current-generation-go` on one transient checksum-database error (`verifying go.mod: ... reading
  https://sum.golang.org/tile/8/0/x192/982: stream error: stream ID 139; INTERNAL_ERROR`), not on code. A
  `go mod download` with a short retry before each Go fixture build (or a warmed module cache in the build image)
  would keep one network hiccup from failing a gate.
- **Long MCP sessions end at their token's exp** (2026-11-01, #260): the MCP session carrier is a bidi stream, so
  like every stream it now ends at the opening token's `exp` (Go, Java, Rust). An MCP client holding one session
  for longer than a token lifetime must reconnect with a fresh token; #259's make-before-break rotation is the
  transparent fix if that turns out to matter.
- **`just gates-container` in a git worktree** (2026-10-31, #255): it mounts only the checkout at `/workspace`, and a
  worktree's `.git` is a file pointing at the main checkout's `.git/worktrees/<name>`, so the first git call in the
  gates fails (`fatal: not a git repository`, exit 128). Mounting `$(git rev-parse --git-common-dir)` at its own path
  (and marking it a safe directory) would let loopers, which each work in a worktree, run it as written.
- **Memberport's stalled-mint expiry check is wall-clock bound** (2026-11-01, #255): `MemberPortBehaviorTest.
  stalledMintDropsTheConnectionAtExpiry` (`cross-test/memberport/testdata`) asserts the drop within 8s of real time.
  With two gate runs on one 8-CPU runner it took 10010ms and failed `memberport-java`; the same image, uncontended,
  passed it. A shorter token lifetime with a bound relative to it, or an injected clock, would keep it from
  flaking on a loaded runner.
- **The member port's client counts the channel's connect against its 10s attach wait** (2026-11-15, #722): in the
  Java `MemberPort.OutboundSession.openOnce`, the `ADMISSION_TIMEOUT_MS` wait for the receiver to attach a stream
  starts before the channel to the peer has connected, so every stream opened on a cold channel shares its one TLS
  connect. With the JVM starved of CPU (the member port test pinned to one core beside 16 spinners), the connect
  alone outlasted 10s and 31 of 48 tunnels were refused ("did not attach the stream in time"). When the receiver had
  already attached, the cancel also dropped its session ("stream ended without last (CANCELLED)"), which is how #722
  first showed it. The server side bounds the handshake on its own (`handshakeTimeout`). Starting the client's clock
  once the transport is ready, or a configurable admission deadline, is a product decision; #722 only warmed the
  test's tunnel before its burst.
- **The member port's rotation leaves a quarter of what is left of a stream, with no floor** (2026-11-15, #737): the
  Java `MemberPort.OutboundSession.schedule` rotates at 75% of the stream's remaining lifetime, measured once the
  stream is attached, and the receiver drops the session if the client's `last` has not reached it by the old
  stream's `exp` (API.md, Expiry). A slow open shrinks that remainder: with the test's 3-second tokens on a loaded
  runner, a first open left 1074ms, the rotation got 270ms, and the cutover landed 22ms before `exp`, too late for
  the receiver. With real token lifetimes (minutes) the quarter is ample; #737 gave the two rotation tests 6-second
  tokens. A floor on the time the rotation leaves (say, rotate at least a few seconds before `exp` when the
  lifetime allows), or an early warning when a stream is attached with little of its lifetime left, would make the
  margin hold for short-lived tokens too.
- **The Go and Rust member port rotation tests still run on 3-second tokens** (2026-11-20, #919): #737 gave the
  Java `byteSessionRunsAcrossSeveralTokenLifetimesBothWaysAtOnce` and `aRotationCarriesTheReceiversRenewedToken`
  6-second tokens (a cutover a second or more before `exp`), and #919 the interop leg; their Go twins
  (`memberport_behavior_test.go`, `newMinter(..., 3, 2)`) and Rust twins (`memberport_behavior.rs`,
  `Minter::new(..., 3, 2)`) keep the half-second margin a loaded runner outlasted. The same change there (6, 4,
  1.6 MB, two lifetimes of 12.5 s) would keep them from the same flake.
- **Cargo's retried network errors fail the clean-log check** (2026-11-01, #255): `assert_clean_log` in
  `cross-test/current-generation/run.sh` (and its copies in the other cross-tests) fails on any `warning:` line, so
  cargo's `warning: spurious network error (3 tries remaining): [16] Error in the HTTP2 framing layer`, which cargo
  then retries successfully, failed `current-generation` and the land of #255. Pre-fetching the crates
  (`cargo fetch`) before the checked build, or not counting cargo's retry warnings, would keep a network blip from
  failing a 3h gate run.
- **The macos runner's job in a fleet's pipeline** (2026-11-22, #195): `tacplus fleet gateway --clients-only` on the
  `macos` runner builds the darwin/arm64 `rust:` and `swift:` clients, and the Linux job takes its
  `build/gateway/clients` as `--prebuilt`. No fleet's pipeline wires the two jobs yet, and the Mac needs the
  `macos-build:X.Y` image (#261) to run a pinned tacplus there. A fleet with only `go:` clients and webapp modules
  does not need it.
- **A multi-architecture gateway image** (2026-11-22, #195): `tacplus fleet gateway` builds the image for the
  machine's own `linux/<arch>` (the gateway binary is a native cargo build) and records it as `platform`. A fleet
  whose nodes are another architecture builds on a runner of that architecture. Building both (cargo-zigbuild for
  the other, `tacplus-image-build --platform linux/amd64,linux/arm64`) and locking each digest would remove that.
- **Per-client target subsets** (2026-10-31, #195): each recipe builds its fixed target set (rust/go: four, swift:
  darwin/arm64, dotnet: windows/amd64). A Linux-only CLI or a daemon with no Windows port has no way to say so; an
  optional `targets:` narrowing the recipe's set (never widening it) would cover that, if a service needs it.
- **Cap the member port's established tunnels** (2026-10-30, #212): admission is bounded (a 10-second deadline and at
  most 256 connections proving themselves), but authenticated tunnels are not counted: a member of the same service
  and tenant can open as many as it likes, each with two threads. A configurable cap would bound a misbehaving or
  compromised member. Same design in all three languages (Go and Rust since #322).
- **Check Java's reading of an Ed25519 PKCS#8 v2 key** (2026-11-08, #322): building the Rust↔Java member port
  interop leg, a kernel key that Rust wrote as PKCS#8 v2 (seed plus public key) signed tokens the Java chassis could
  not verify; written as v1 (seed only, as Go writes it) they verify. Unconfirmed: if Java's `ImmutableKey` mishandles
  a v2 key, a consumer holding one gets "failed to verify token". A Java key test with a v2 PEM would settle it (file a
  `bug` if it fails).
- **Run the generated ServiceManager's member port end to end** (2026-10-30, #212; docs/KNOWN_TEST_GAPS.md MP-1):
  `cross-test/memberport` drives `MemberPort` with a fake kernel; the ServiceManager wiring (the `member:<svc>` token,
  the JWKS watch, the auto-TLS certificate) is compiled and grep-asserted only, until a fake-kernel ServiceManager leg
  or Stratum's adoption (stratum#17, after kernel#30).
- **Retire `(spec.smb)`, `(spec.smb_authority)` and `SmbCertService` in 3.0** (2026-10-30, #212): deprecated in 2.6,
  superseded by `(spec.member_port)`; kernel, their only user, is dropping Hazelcast. Remove the parser, the three
  ServiceManagers' SMB cert streams, `sys/smbcert.proto`, `cross-test/smb` and the SMB legs of `cert-stream` then (or
  sooner, if Jody decides once kernel's rewrite drops them).
- **The record plane's tenant checks compare tids against namespace segments** (2026-10-30, #207): the route
  check (`CheckRoute`) takes the instance's tenant from its own token's namespace (`/tenants/<t>`), and the new
  route pin holds an entry's `(spec.store_namespace)` value to `/tenants/<target_tenant>` or below. Both assume what
  tacplus documents today, a tenant's namespace is `/tenants/<tid>`. kernel#23 keeps user and session namespaces at
  `/tenants/<name>` and moves only the instance SAN to the tid; if a row's namespace (stamped from a user's claim)
  carries a name, every write to a tenant's store is refused. #202 owns that switch: the instance tenant should come
  from the SAN, and a writer acting for a user needs a defined name → tid (or a tid claim).
- **Stamp `(spec.identity_spiffe_subject)` / `(spec.identity_spiffe_actor)` on the record write surface**
  (2026-10-30, #207, its "Related" section): eventbus publish stamped them; the write surface does not, so a record
  whose `owner` is its `spiffe_id` (messaging.Message) is stored with an empty owner unless the writer fills its own
  identity in, which the annotation exists to prevent. The generated wrapper should stamp them on Put,
  InsertIfAbsent, ReplaceIfUnchanged and each Apply op, as the publish wrapper did.
- **Apply's inner ops carry an unused `target_tenant`** (2026-10-30, #207): on a tenant-owned record store the
  single-type requests carry the route field, and Apply's `RecordOp` cases reuse those messages, so each op has a
  `target_tenant` the wrapper ignores (the Apply routes by its own). Refusing an op whose value is set and differs
  would make the shape honest; a dedicated op message per case would remove it, at a wire change.
- **current-generation's retired-`linked` check sees nothing under its default workdir** (2026-10-30, #207): the
  record-egress check drops every hit whose path contains `/target/` (meant for cargo's build dirs), and the default
  `WORK_ROOT` is `target/cross-test/current-generation`, so in a normal run it filters out the whole generated tree.
  Run with `--workdir` outside `target/`, it fails on main on the reserved `JWT_PAYLOAD_TYPE_LINKED` name in
  `token.pb.go`'s descriptor bytes. Excluding paths relative to `$output` (and the reserved name in descriptors)
  would make the check real.
- **The Watch<T>Changes wrapper checks its pins before validating the request** (2026-10-30, #207): Go renders the
  tenant pin (and now the route pin) of a WATCH seam ahead of request validation and the route check, so an empty
  `target_tenant` there is refused by the pin (`PERMISSION_DENIED`) rather than as `INVALID_ARGUMENT`. Both fail
  closed; the unary seams check in the documented order.
- **Drop the from-source docker-buildx once a buildx release carries the fixes** (2026-10-29, #190; 2026-11-20,
  #930): the build image builds buildx v0.39.1 from source with moby/go-archive 0.3.3, because no buildx release
  (through 0.39.1) has taken it; 0.39.0 took golang.org/x/net 0.62.0 and containerd v2.4.2 (CVE-2026-95839), so
  that bump is gone. When one does, set `BUILDX_VERSION` to it and drop the stage, copying it from
  `docker:${DOCKER_VERSION}-cli` again; `tacplus-smoke` keeps the floors.
- **The build pipeline scans the pinned build image, and a build-image finding is filed once, on tacplus**
  (2026-11-20, #930; tacplus-dev/roadmap suggestions/2026-11-20-a-high-in-the-build-image-is-tacplus-s-bug-patched-on-the-line-buildx-past-containerd-v2-4-1-cve-2026-95839-on-main-and-2-6-and-a-build-image-finding-filed-once-upstream-not-as-a-needs-decision-per-pinned-repository.md): the half of #930's
  suggestion that is not this repository's. The pipeline's security job reports 0 findings for a repository's
  `build_image` (registry#16, registry's PROPOSALS since 2026-11-12), so consumers find build-image CVEs by hand and
  file needs-decision (registry#16, fleet-seatrials#60). In tacplus-dev/looper: the security job runs `trivy image`
  on build.lock's digest (as fleet-seatrials' ci/security.sh does for its CI image), and looper-scan files a
  build_image finding on tacplus-dev/tacplus (approved, priority:high when a fixed release exists) with a
  `Blocked-by` in each consumer that finds it (looper#129's rule).
- **Drop the from-source gh once a gh release carries golang.org/x/net >= 0.62.0** (2026-11-07, #350): the build
  image builds gh v2.103.2 from source with x/net 0.62.0 (CVE-2026-78661), because gh's releases through 2.103.3
  build with 0.60.0. When one does, set `GH_VERSION` to it and download its release tarball in the `fetch` stage
  again, dropping the `gh` stage; `tacplus-smoke` keeps the floor.
  A nightly check that the image's Go binaries carry no module under a known fix (what Trivy found here) before the
  sweep fails would make the next one a patch rather than a red run.
- **Drop the replaced Maven guava once a Maven release bundles 33.7.2 or later** (2026-11-02, #222): Maven 3.9.16
  (the newest 3.9) ships guava 33.6.0-jre (CVE-2026-102554), so `images/base.Dockerfile` swaps in
  `MAVEN_GUAVA_VERSION` (checksum-pinned). When a Maven release ships a fixed guava, drop the swap;
  `tacplus-smoke` keeps the floor. One does now (2026-11-25, nightly scan): Maven 3.10.2's
  `org/apache/maven/maven/3.10.2/maven-3.10.2.pom` sets `<guavaVersion>33.8.0-jre</guavaVersion>`, while 3.9.16 is
  still the newest 3.9. Moving `MAVEN_VERSION` to 3.10 (a Maven line move: see "Name the line every image pin
  follows" below) would let the swap go.
- **One crypto library in the generated Rust service** (2026-10-29, #186): the chassis keys (RSA, ECDSA) are
  aws-lc-rs's, because ring cannot generate RSA keys nor sign with one over 4096 bits, both of which the chassis
  offers in every language (`generate_rsa_8192_key`); TLS stays on rustls's ring provider. A service now compiles
  both. Moving rustls to its aws-lc-rs provider (`rustls/aws_lc_rs`, the rustls default) would leave one; it changes
  the TLS stack of every generated Rust service, so it is a minor's change, with the TLS parity gates.
- **DiskStoreTest "appends must not wait on a failing upload" flakes under load** (2026-10-31, #200): in
  `pkg/templates/java/chassis/diskstore/DiskStoreTest.java.tmpl` the writer thread's 100 appends (each fsynced)
  outlived the 20s `join` on a runner running two gate suites at once; the test then closed the log under the still
  running thread (an NPE on `RedoLog.writer`). current-generation passed on a re-run. Asserting the appends are not
  blocked by the shipper (a latch, or a bounded append count), rather than a wall-clock bound, would make it
  deterministic, and joining the writer before `close()` would stop the NPE masking the real failure.
- **osv-scanner of the Java harness poms in the gates, and one shared pom** (2026-10-31, #200): the Java harness
  poms are ten hand-kept copies (six checked in, four written by `cross-test/lib/java-compile.sh`,
  `consumer-contract`, `cert-stream` and `memberport`), and `scripts/java_dependency_floor_test.go` holds only the
  floors already named (guava, tools.jackson). A new advisory on any other artifact they resolve is found by the
  nightly scan after it lands. Running `osv-scanner scan source` over the poms the gates write (osv-scanner in the
  build image) would fail on the first one, and one pom template the scripts and fixtures derive from would turn
  the next bump into one edit. The `google-cloud-storage` dependency `add_java_gcs_archive_dependency` splices in
  for the GCS archive build is outside every scan today (it is not in any pom on disk): since #263
  `TestJavaGcsArchivePomMeetsItsFloors` holds the floors already named for it (the SDK, guava -jre,
  opentelemetry), and a new advisory on anything else it resolves is again found only by the nightly scan.
- **govulncheck of the generated Go modules in the gates** (2026-10-30, #199): the harnesses pin the generated
  chassis's Go dependencies by hand, and `scripts/go_dependency_floor_test.go` holds only the floors already named
  (golang.org/x/net). A new advisory on any other module the chassis reaches (grpc, x/crypto, gin) is found by the
  nightly scan after it lands. Running `govulncheck ./...` in `target/cross-test/current-generation/go` at the end
  of current-generation (govulncheck in the build image, its database fetched at run time) would fail the gate on
  the first reachable one; the harness pins could then move to one shared list instead of eleven copies.
- **cargo audit of a generated sample, nightly** (2026-10-29, #186): current-generation now refuses the two crates of
  #186 by name in the generated lockfile, but a new advisory on any other crate the chassis locks is found only by
  each service's own scan. A nightly `cargo audit` of a generated sample (cargo-audit in the build image, the
  advisory database fetched at run time) would find it first. It also reported four unmaintained crates
  (`rustls-pemfile`, `backoff`, `instant`, `unsafe-libyaml`); `backoff` and `instant` left in #319, `unsafe-libyaml`
  with serde_yaml in #320, `rustls-pemfile` is #308. GitHub-only advisories (no RUSTSEC id,
  like #215's block-buffer) need OSV, not cargo-audit.
- **Refuse a W2S `tokio-tungstenite` pin below 0.30** (2026-10-30, #215): the generated W2S client takes
  `tokio-tungstenite` from the consumer's dependencies.yaml, and 0.28 locks sha1 0.10, so digest 0.10 and
  block-buffer 0.10 (GHSA-qwgh-2vcv-g2f7) through the consumer's own pin. The fixtures and the example pin 0.30;
  the generator could refuse an older pin (as it refuses a missing one) or pin it in the template, as it now pins
  x509-cert.
- **A fleet service whose image is not ghcr.io/<owner>/<repo>** (2026-10-29, #180): the fleet build names a
  service's release image from its repo, `github.com/<owner>/<repo>` → `ghcr.io/<owner>/<repo>`, and refuses any
  other forge. A fleet running its own implementation from GitLab (the private-registry case docs/USER.md shows), or
  a service publishing elsewhere, needs a way to say where its image lives: an `image:` per service in fleet.yaml,
  or the image named by the service's own repo (a field in its tacplus.yaml, or the release's OCI labels). It is a
  public fleet.yaml/tacplus.yaml contract, so it needs a decision.
- **Fleet deploys from dev builds** (2026-10-29, #180): the issue allows "a pre-release or a dev build" for an
  unreleased fix; today the only explicit form is a pre-release tag (`version: "0.4.3-rc.1"`), whose image the
  service's release CI must publish. A dev build without a tag (an image by digest from a branch build) has no
  form; if fleets need one, it is a new per-service field and needs design.
- **`tacplus fleet build --check` for CI and bots** (2026-10-29, #180): `--check` prints a table and fails when a
  re-lock would move a service. A `--json` output would let a fleet's CI open the re-lock pull request itself.
- **A per-pod archive member from one shared config** (2026-10-28, #143): `grpcconfig.disk_persistence.archive.member`
  is a config value, but a StatefulSet's pods usually share one ConfigMap, so each pod needs its own rendered config
  (or a per-pod env override) to get its own member. The Java ServiceManager already takes a stable per-instance name
  from `POD_NAME`, else the hostname, for the record topic's consumer group (`perInstanceConsumerGroup`). An opt-in
  source for the member (a `member_from_pod_name` flag, say) would make the one-config case work. The default stays
  the instance id: changing it would move existing archives.
- **Continuity token for the Java Kubernetes minter** (2026-10-28, #143 option d): the instance id is reassigned on
  every bootstrap exchange because the Java k8s minter sends no continuity token, so instance-keyed state (not only
  the archive, which `archive.member` now decouples) starts afresh on each restart. Stabilising the id itself changes
  minting and restore semantics in every language; it was split out of #143 and needs its own decision.
- **Writer-side pruning of the archive** (2026-10-28, #144, deferred by decision of 2026-10-26): retention is a
  bucket lifecycle rule today (docs/API.md, Archive to an object store, Retention), which can only expire by age and
  so deletes a stopped writer's whole chain once the age passes. Pruning after a checkpoint ships (an opt-in
  `archive.retain_checkpoints`, an `ArchiveSink.delete`, delete only after the new checkpoint is durable) would keep
  exactly the live chain. Revisit once #99 brings the archive to Go and Rust, so the sink interface changes once for
  all three; it needs a new issue then.
- **Go and Rust ServiceManager archive wiring honours `archive.member`** (2026-10-28, #143): Go and Rust have the
  archive layer as a library only (`roadmap/LANGUAGE_PARITY.md`); when their ServiceManager reads `archive` (#99),
  it must take the member from `archive.member` before the instance id, as Java's `ArchiveShipper.member` does.
- **The redo contract names its caller and host** (2026-10-28, #163 D5): `sys/redo.proto` admits
  `auth_services: ["stratum"]` and is hosted at label `"redo"`, as decided ("redo hosts it, Stratum calls it"), so a
  fleet that runs its record store under another name cannot call it, and tacplus's sys tree now carries two platform
  service names beside `kernel` (docs/USER.md "Well-Known Names", tacplus#1). `auth_services_from_fleet` cannot stand
  in: the fleet stamps caller lists in the merged tree, and sys protos are not in it, and a standalone build of the
  host would admit no callers. If another record store or a second redo implementor appears, a declared audience
  for sys contracts (say, the fleet stamping sys contracts too) would remove the names.
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
- **MCP on a tenant-owned service for a caller of no tenant** (2026-10-27, tacplus-dev/tacplus#170): an MCP
  session of a tenant-owned service is opened in the tenant of the caller's token namespace, so a `/system`
  caller (or any caller whose namespace names no tenant) has no MCP session there (`PERMISSION_DENIED`), although
  it passes the tenant door for the same rpcs over gRPC. If platform operators or agents need MCP on tenant
  instances, the platform would have to choose that tenant from something other than the model (a token claim
  for the target tenant, say); needs design.
- **MCP sessions are same-tenant only** (2026-10-27, tacplus-dev/tacplus#170): because the session tenant is
  the token's, an `RPC_FLAG_ANY_TENANT` tool is reachable over MCP only in the caller's own tenant, while its
  gRPC/REST twin reaches any tenant. Deliberate (the model must never set the tenant); a cross-tenant MCP path
  would need the platform to choose the target, e.g. from a user-confirmed selection outside the model. Needs
  design if SeaBeam's "talk to another tenant's crew" should work through an assistant.
- **Kernel's linked-claim tests on the 2.6 floor** (2026-10-28, #171): 2.6 generates no `linked` accessor in any
  language (`TokenWrapper.getLinked` and friends are gone; `JwtPayloadType` 29 is reserved). tacplus-dev/kernel's
  `TenantUserLinkTest` (lines 176 and 200 on its main today) asserts `token.getLinked().isEmpty()` and will not
  compile when kernel moves its floor to 2.6. `token.getListField("linked").isEmpty()` keeps the same assertion
  (kernel#4's "no minted token carries linked"). No other tacplus-dev repository reads the claim. Removing a
  generated accessor in a minor goes against docs/VERSIONING.md ("a major is a breaking change"); #171 chose the next
  minor, because kernel stopped stamping the claim first (kernel#4).
- **The query-predicate pin also runs on a record write's row** (2026-10-28, found in #171): in Go and Java the
  `(spec.store_namespace)` predicate pin treats `PutVaultRowRequest.item` (a `VaultRow`, whose namespace is an EQ
  filter criterion) as a query predicate, so a foreign-tenant Put is refused by it, with "item.namespace names a
  tenant other than the caller's; the tenant boundary is not the predicate's to choose", before the write surface's
  own pin ("PutVaultRow: item.namespace ... not the caller's to choose") runs. Rust renders no predicate pin and
  answers with the write pin's message. The outcome is the same refusal; the message differs by language. The
  predicate pin should skip the write surface's requests (`method.RecordEntry` set).
- **Forge credentials beyond github.com and the CI GitLab** (2026-10-29, #191): the fetch layer now sends
  `GH_TOKEN`/`GITHUB_TOKEN` to `https://github.com` and `CI_JOB_TOKEN` to `CI_SERVER_HOST`, and nothing anywhere
  else. A private contract or registry on GitHub Enterprise (`GH_HOST`) or on a GitLab other than the job's own (a
  `GITLAB_TOKEN` for gitlab.com outside CI) still cannot be cloned except through the user's own git credential
  helper, which git already consults. Either is a new host/token pairing (a feature), each needing the same
  exact-host, https-only checks. Not backported to 2.0-2.3: they have no registry or `depends`, so no GitHub fetch
  that 2.4 made necessary.

- **The looper image's tacplus has no 2.6 image to run** (2026-10-29, #193): tacplus-dev/looper's `tacplus` wrapper
  runs `ghcr.io/tacplus-dev/tacplus/tacplus:<floor>`, and from 2.6 there is no tacplus image (deprecated; the tags up
  to 2.5 stay). A repository whose floor moves to 2.6 gets no compiler in its looper. The wrapper can run the
  repository's pinned release instead: `images/tacplus.sh` with `TACPLUS_LINE` unset is the same launcher, POSIX sh,
  and needs only curl and sha256sum (or the build image, though loopers do not run in it by decision). A change in
  tacplus-dev/looper, before any repository's floor reaches 2.6.
- **The compiler does not read tacplus.lock** (2026-10-29, #193): the pin is enforced by the build image's launcher;
  a binary run directly (an installed `tacplus`, `just install`) generates in a pinned repository at whatever version
  it is, and stamps that version into the output. A warning (or a refusal with an override) when the running version
  is not the lock's would make a stray local build visible. Needs a decision: a refusal would break a developer's
  `just install` workflow on the repository itself.
- **Java fuzzing in the build image** (2026-10-29, #193): the image fuzzes Go (`go test -fuzz`) and Rust (cargo-fuzz on
  `$RUST_NIGHTLY`), and verifies Rust (Kani) and Java (JBMC, OpenJML); there is no Java fuzzer (Jazzer, say). The
  kitchensink's per-release fuzzing (#196) is where a Java fuzz target would first be needed.
- **A new release is not pinnable until its build image is pushed** (2026-10-29, #193): `tacplus pin` refuses a
  release without `build:X.Y.Z`, which `images.yml` builds from the tag, the arm64 half under QEMU, for hours, while
  the binaries are out in minutes. Native arm64 runners (or the GitLab pipeline, #194) would close the gap.
- **README's install command names an unreleased build image** (2026-10-30, #201): on main, README.md and
  docs/snippets/install-container.md run `build:2.6 tacplus pin`, and `build:2.6` does not exist until v2.6.0
  (#213); the 2.5 images predate `tacplus pin`. The README on GitHub is main's, so until 2.6.0 ships a user copying
  it gets a pull error. Either cut 2.6.0 soon or have README name the newest released line's workflow. A check that
  the snippet's line equals the newest `v*` tag's MAJOR.MINOR (the issue's optional guard) would fail by design
  between a minor's first doc change and its release, so `TestDocsPinNoTacplusImage` guards only the retired image.
- **GitHub's "Latest" release follows the newest line, not the last one published** (2026-10-31, nightly scan):
  `.github/workflows/release.yml` runs `gh release create` with no `--latest` flag, so whichever patch publishes
  last becomes "Latest". On 2026-10-30 six lines were patched within a minute and `gh release list` now shows
  `v2.2.3 Latest` while v2.5.4 is the newest. Nothing in tacplus reads the flag (`images/tacplus.sh` and
  `tacplus pin` list all releases), but the release page and `/releases/latest` send people to 2.2. Passing
  `--latest=false` unless the tag is the newest `v*` (the rule `scripts/images-tags.sh` already applies to
  `build:latest`, #160) would fix it.
- **A gate that refuses protojson/prototext layout in generators, and a cross-binary determinism check** (2026-11-08,
  #307): `registry.json` flipped between one and two spaces after `:` from release to release, and between amd64 and
  arm64 builds of one release, because protojson's `Multiline` layout adds a space when a hash of the binary is odd
  (protobuf's internal/detrand). #307 gives `registry.json` its own formatter and a golden test. Nothing stops the next
  generator from writing protojson's or prototext's layout (`Multiline`, `Indent`, `protojson.Format`,
  `prototext.Format`) into a generated file: a soundness rule could refuse it under `pkg/internal`. `cross-test/determinism`
  compares two runs of one binary, which agree even with the defect; it could also run a second binary of the same
  source that differs only in a padded `-X` string, so the hash differs.
- **Name the line every image pin follows, not only Go, Node and Swift** (2026-11-11, nightly scan): the rule
  written beside the pins in `images/base.Dockerfile` and `images/build.Dockerfile` (#255: Go the newest release,
  Node the newest LTS, Swift the newest release) is what the nightly scan checks drift against. `JDK_VERSION=25`,
  `DOTNET_CHANNEL=10.0`, `MAVEN_VERSION=3.9`, `DOCKER_VERSION=29` and `RUST_VERSION=1` have no rule, so the scan
  cannot tell drift from a choice. On 2026-11-11 the lines next to them were JDK 27 (newest feature release; 25 is
  the newest LTS, per api.adoptium.net), .NET 11.0 (STS, released 2026-11-10; 10.0 is the newest LTS, per the
  dotnet releases-index.json), Maven 4.0.0-rc-7 (only an rc above 3.9) and Docker 29.9.3. #255 called JDK 25 and
  .NET 10.0 current, which reads as "newest LTS". Writing that down beside the pins (JDK and .NET the newest LTS,
  Maven the newest non-rc release, Docker the newest major, Rust stable via `1`) would let the scan file a missed
  move as a `bug` instead of leaving it to judgment. On 2026-11-25 (nightly scan) Maven 3.10 is out (3.10.0, 3.10.1,
  3.10.2 in Maven Central's `org/apache/maven/maven/maven-metadata.xml`; Docker Hub publishes
  `maven:3.10-eclipse-temurin-25`), so `MAVEN_VERSION=3.9` is now a line behind; with no rule for Maven it is left
  to judgment again, not filed. JDK 27 (`eclipse-temurin:27-jdk`) is still the newest feature release over LTS 25.
- **Every write into a tree the compiler owns goes through `fsroot`, checked by a rule** (2026-11-15, #754): #754 moved
  the 43 writes gosec's G703 taint analysis reaches onto `os.Root` (`pkg/internal/common/fsroot`, or a root the
  function opens), but G703 only follows paths it sees as tainted: many writes into the same output and staged-proto
  trees still go through a joined path (`header.ReplaceProtocHeader`, most template emitters' `os.WriteFile` under
  `config.OutputDir`), and a symlink there would redirect them the same way. A soundness rule refusing
  `os.WriteFile`/`MkdirAll`/`Rename`/`RemoveAll` under `pkg/internal/` and `pkg/fleet`/`pkg/runner`/`pkg/registry` outside
  `fsroot` (as #753 proposes for `filepath.Walk`) would cover them and keep the next one from being written the old way.
- **The security report's `new` keys on `file:line`, so an edit above a finding makes it new again** (2026-11-15,
  nightly scan; tacplus-dev/looper, Security checks): the build's report of 2026-11-15 marked 320 findings `new since
  the previous run`, 216 of them MEDIUM/LOW ones #585 already lists at a line a few above (every G306/G301/G304 of
  `pkg/internal/go/types/generator.go` moved up four lines; `rust/types/builder.go`, `typescript/types/generator.go`
  and `registry/registry.go` likewise), so each night that touches a generator re-files its backlog as new. Keying
  `new` on the finding's identity without its line (tool, rule, file, and the matched source text or a hash of the
  enclosing function, as gosec's `-track-suppressions`/SARIF fingerprints do) would leave `new` meaning new. Until
  then the scan compares the lists by hand; it also means `security-baseline.json`, once written, would go stale on
  every edit above a finding. A change in tacplus-dev/looper.
- **`tacplus registry` checks declared config slices against their registered fields** (2026-11-07, #352): since
  #352 a build whose registry is a lock refuses a `(spec.config_slice)` with no registered `TenantConfig` /
  `ProjectConfig` field, but the lock sync (`tacplus registry --proto-dir .`) scans only service and vservice
  declarations, so it reports a missing slice registration only at build time. Scanning the tree's own slices
  (`configSliceOwner`, today in `pkg/internal/proto`, out of `cmd/`'s reach) in the sync would name it with the
  service ids. A legacy per-repo registry's `tacplus registry` likewise never records slice fields: the build still
  assigns them (now in file order), into whichever registry.yaml the build reads (a merged or fetched build's is a
  temporary copy).
- **A listener seam on the generated Go GrpcServer, so Go tests keep the port they bind** (2026-11-15, #531 → #713):
  #531 fixed the reserve-then-rebind port race in five Rust cross-test fixtures (bind port 0, keep the listener,
  `serve_with_incoming`), and `scripts/cross_test_listener_test.go` now refuses the pattern in cross-test Rust. Two Go
  tests keep it: `cross-test/stream-expiry/testdata/stream_expiry_behavior_test.go` `freePort` and the generated
  `pkg/templates/go/chassis/grpcserver/keepalive_enforcement_test.go.tmpl` `keepaliveTestPort`, because the generated
  `GrpcServer.Listen` only binds the address its config names. The race there is across processes on a shared runner
  (parallel gates, #300; several runs per machine), not between parallel tests. Fix on a minor (a generated surface,
  `docs/VERSIONING.md`): `Listen` takes an optional bound `net.Listener` (or `New` an option), both tests use it, and
  the guard test grows a Go rule (`net.Listen("tcp", "...:0")` then `defer l.Close()` and `return ... Port`) over
  `cross-test/` and `pkg/templates/go/`.
- **Self-tests wait on state, not the clock, wherever the work fsyncs** (2026-11-07, #341): the durable accounting
  spool's Go and Java self-tests waited a fixed 1s (or slept 300ms) for fsync'd appends, and main went red when a
  loaded runner disk was slower. #341 fixed those two. Other hand-written fixture self-tests and generated
  `_test` templates that sleep or poll against an RPC-sized bound for disk work (redo log, append log, cursor and
  snapshot writes) have the same exposure. Audit them: wait on the observable state, with a generous hang bound
  separate from any timing assertion. A cheap way to find them is #341's reproduction: a 100ms sleep before each
  fsync in a scratch copy of the generated diskstore, then run the self-tests. Size the hang bound for a shared
  runner, not a quiet disk: on the looper host with several gate runs' fsync-heavy tests in flight (load average
  40-130, 2026-11-15), one `dsync` 4 KiB write took 0.2s to 3s on both the overlay `/tmp` and the ext4 `/builds`
  volume, so fifty fsync'd appends ran 8s to over 60s and a 60s bound failed 2 runs in 10; #341 uses 180s. The
  same pressure is behind #539, #721 and #723. A standing fix for all of them: run the fixtures' disk tests on a
  tmpfs (`TMPDIR`, which `t.TempDir` and `Files.createTempDirectory` honour), where fsync is a no-op and the
  self-tests test the code, not the host's disk queue.
  One instance left after #341 and #690 landed together (#804): the Rust durable twin,
  `cross-test/current-generation/testdata/rust/accounting_sink_durable.rs`, yields on its own `BOUND` of 10s for the
  same fsync'd appends (Go and Java now wait on a 180s progress bound), so it is the next to fail on that runner.
- **Return the Swift clients to grpc-swift's `withGRPCClient` once Swift 6.3.1 is every consumer's floor** (2026-11-15,
  #803): the generated Swift `C2DClient` and `C2SClient` run each call's `GRPCClient` on a plain `withThrowingTaskGroup`
  (run the connections, call, `beginGracefulShutdown`, wait) because `withGRPCClient` does the same on a
  `withThrowingDiscardingTaskGroup`, whose teardown on Swift toolchains before 6.3.1 (swiftlang/swift#75501, fixed by
  swiftlang/swift#87665) aborts the whole process with "freed pointer was not the last allocation" about once in four
  hundred teardowns. The lines 2.0–2.5 build with Swift 6.1.3, and a consumer's Xcode is what it is, so the
  workaround stays while any supported line or consumer runs an older toolchain; when 6.3.1 is the floor everywhere,
  the ten-line helper can go back to the one-call upstream form.
- **The looper's gates run on a Swift the main build image left behind** (2026-11-15, #803): main's `build` image
  carries Swift 6.4.2 (#255), but the runner the looper's gates run in has Swift 6.1.3, so a land's `c2d-server`
  swift leg compiles the generated package with a toolchain main no longer ships. It is what surfaced #803 (6.4.2
  has the runtime fix), which argues for keeping a 6.1-era toolchain in some gate while 2.0–2.5 are supported;
  but a gate on main should also build with main's image (`just gates-container`), or its verdict says nothing about
  what main's consumers get. A change in tacplus-dev/looper's runner image, or in which image the gates run in.
- **consumer-contract's swift legs fail on the linker's own warnings** (2026-11-15, #803): `cross-test/consumer-contract/run.sh
  --language swift --case c2s` on the looper's runner ends in `ERROR: warning/error output found in .../swift-build.log`
  after `Build complete!` with no `error:` line: the only warnings are `/usr/bin/ld: warning: ... has a LOAD segment with
  RWX permissions`, binutils ≥ 2.39 on SwiftPM's own plugin and manifest binaries, nothing generated. The check should
  look for the compiler's diagnostics (`error:`, `warning:` from swiftc on a generated source), not for any line with
  the word in it, or the leg says nothing on a runner with a recent binutils. It is not in `just gates`, so #803
  compile-checked the generated `C2SClient` by reading that log instead.
- **The generated clients' stream rotation leaves a quarter of a leg's lifetime, with no floor** (2026-11-17, #840):
  `StreamRotation.ClientSession.schedule` (Java; Go's `rotation.RotateAt` and Rust's `ROTATE_AT` alike) rotates a leg at 75% of
  what is left of its token, so the mint, the new leg's open and the cutover get the last quarter before the server
  ends the old leg at its `exp`. With 3-second tokens (exp in whole seconds: 2 to 3 s left) that is 0.5 to 0.75 s,
  and a loaded runner's Java bidi stream at 2 ms ticks missed it 6 times in 6 (`UNAUTHENTICATED, stream credential
  expired`). The quarter must also drain what is in flight: the old leg's `last` comes back behind every message
  queued on it, and a sender that outruns the runner (that test's, unpaced) queues seconds of them; with 6-second
  tokens alone it still missed 1 in 6. #840 gave the Java rotation test 6-second tokens and at most 100 ticks in
  flight (10 in 10 at the same load); at a load average of 100 on 8 CPUs (2026-11-18) that still missed twice, once
  on a cold JVM's opening leg and once on 100 ticks echoed at 20 a second, so it also warms the JVM with one
  throwaway rotated stream before the first test and keeps at most 10 ticks in flight (3 in 3). With real lifetimes (minutes) the quarter is ample; a floor on the time the
  rotation leaves before `exp` (when the lifetime allows) would make it hold for short-lived tokens too. Same shape
  as the member port's entry from #737.
- **Cut the Java AppendLog concurrency self-test's fsyncs too** (2026-11-15, #723): #723 cut the Go
  `TestAppendLogConcurrentAppendsStayWholeLines` from 8x50 to 8x4 appends (each an fsync under one lock: 42s of
  the Go diskstore package's 61s on an unloaded runner, all of go test's 10m on a load-200 one) and still fails
  every time without the lock. Java's `AppendLogTest.testConcurrentAppendsStayWholeAndComplete`
  (`pkg/templates/java/chassis/diskstore/AppendLogTest.java.tmpl`) does 4x200, twice Go's old count, with no
  timeout to hit but the same minutes spent on a loaded `current-generation` run.
- **Proto3 JSON for a Rust `Any`** (2026-11-18, #862): the `wkt` chassis crate writes a `google.protobuf.Any` as
  `{"@type", "value": [bytes]}` (prost-wkt-types' fallback, kept) because the generated Rust has no registry of
  message types; Go's protojson and Java's JsonFormat embed the packed message's fields. A generated registry (every
  message's type URL to its decoder and serde) would let `wkt::Any` write and read the proto3 form, so a
  `sys/errors.proto` `Error.details` reads the same in every language.
- **The other well-known types in Rust** (2026-11-18, #862): only `Any`, `Timestamp`, `Duration`, `Struct`, `Value`
  and `ListValue` map to `wkt::`; a user field of `google.protobuf.FieldMask`, `Empty` or a wrapper (`StringValue`,
  ...) falls to prost-types, which has no serde. Unverified whether the generated serde then compiles; if not, add
  them to `wkt` (wrappers are their inner value in JSON, `FieldMask` a comma-joined string).
- **chrono through rmcp** (2026-11-18, #862): with prost-wkt-types and the token chassis gone, a generated Rust
  service still locks chrono 0.4 (RUSTSEC-2026-0543) if it has MCP tools, i.e. any user-facing c2s rpc: rmcp,
  the MCP SDK the generated MCP dispatch is written against, requires chrono (to its latest, 3.11.0) and names
  `chrono::DateTime<Utc>` in its model, and enables schemars' `chrono04`. Nothing tacplus generates can drop it;
  rmcp would have to make chrono optional upstream (modelcontextprotocol/rust-sdk), or tacplus would carry its
  own MCP termination. Until then those services accept the advisory for that path; current-generation refuses
  chrono through anything else.
- **`expires_in` across languages** (2026-11-18, #862): Rust's `Token::expires_in()` and Python's `expires_in()` are
  zero once a token has expired; Go's `ExpiresIn()` and Java's `expiresIn()` go negative. One rule for all four.
- **The member port's refused rotation can overtake its session's clean end** (2026-11-18, #877): #877 found the
  generated stream rotation's clients (Go, Java, Rust) faulting a stream PERMISSION_DENIED when a rotation reached the
  server just after the handler returned (the session forgotten: "no such live session"), over the handler's own
  end, and made a refused rotation end the stream with its current leg instead. The member port (`memberport.go`
  `rotation refused` fault, `MemberPort.java`, Rust `session.rs`) has the same shape: a session whose both `fin`s
  have just closed it on the server, with a rotation on the way, ends in a reset instead of a clean close. Same
  fix: a refused rotation stops rotating and lets the current stream end (clean, or at its `exp`).
- **A timeout per gate from its measured wall time** (2026-11-19, #893): `scripts/gates.sh` stops any leg past
  `GATES_TIMEOUT` (one value for every leg, 5400 s by default), so a hung leg of a gate that takes two minutes
  still holds its slot for an hour and a half. The wall-time table each run prints gives each gate's usual time; a budget per
  gate (say three times its usual, beside its name in the justfile or gates.yaml) would stop a hung short gate in
  minutes.
- **Run `just consumer-build-typescript` in the gates** (2026-11-17, #839): `just consumer-build` is in no gate, so
  its TypeScript leg rotted on main unseen twice over: the client-only proto lost its MCP-6 description once
  descriptions were enforced, and the c2d case's generation log carried the generator's own w2sm WARN. #839 fixes
  both and the leg passes (some ten minutes, mostly npm and tsc). Its python leg waits on #665, its rust leg on
  #829 and its swift legs on the linker-warning entry above. Once each leg passes, adding it to `gates.yaml` is a
  cost decision.
- **The generated build-system's `wheel` has no floor** (2026-11-19, #309): both generated `pyproject.toml`s
  require `"wheel"` bare, which admits wheel 0.30 (GHSA-qwmp-2cf2-g9g6, HIGH, fixed 0.38.1). setuptools 70.1 and
  later build wheels without it, so on main (`setuptools>=84.1.0`) the entry can go; on the 3.8 release lines,
  `wheel>=0.38.1`.
- **`export_to`: an exported state root lists the packages allowed to import it** (2026-11-20, #928):
  `(spec.service_state).export` is a bool, so once it is true any package that declares a `state_import` (or a
  file-level `state_imports`) of the root gets an RO client, tenant-owned ones included. kernel#71 needs the
  deployment config's redacted copy read by the Admiral only. Proposed: add `repeated string export_to = 8` to
  `StateAnnotation`, holding importer proto packages. Generation fails for an import from an unlisted package, both in
  `ValidateStateConsistency` and in `ResolveStatesFromProto`. The generated state service's `auth_services` is
  intersected with the list, so the owner's server never authorizes an unlisted caller. With no list, `export: true`
  means what it does today. The questions for Jody are on the issue: does `export_to` require or imply `export`; are
  importers named by package; does the list also govern tenant-pinned TenantConfig reads and the event-C2S topics of
  an exported root; does `registry.json` carry it.
- **The versions generator code still names, outside `dependencies.yaml`** (2026-11-22, #949): `just floors`
  checks them where they are emitted, but they move only by a change to tacplus: the C# (`Google.Protobuf`,
  `Grpc.*`) and Swift (`swift-protobuf`, `grpc-swift-*`) defaults in `pkg/internal/{csharp,swift}/types/generator.go`,
  used when `dependencies.yaml` has no `csharp`/`swift` section; the Python `[build-system]` `setuptools` and the
  `dev` extra's `pytest`/`mypy` (`pkg/internal/python/types/{generator,pyproject}.go`); and the Rust chassis crates
  no inventory lists (`rand`, `sha2`, `hmac`, `ed25519-dalek`, `url`, `x509-parser`, `hyper-util`, ...). #949 raised
  the three that admitted advisories (setuptools 84.1.0, pytest 9.0.3, url 2.5.9). Declaring them in
  `dependencies.yaml` would make it the one place every floor moves, at the cost of every consumer's inventory
  growing those entries (a missing one fails generation, as for the crates #949 moved).
- **uv in the build image** (2026-11-22, #949): `just generated` locks the Python tree with `uv pip compile`, and the
  image has no uv, so `scripts/generated.sh` installs its pinned `UV_VERSION` into the work directory on each run.
  Installing it into the image's `/opt/venv` beside black and twine would drop that download.
- **Generated tenant-to-owner config push** (2026-11-20, #927; kernel#70): a tenant-scope `config_slice` declares
  `delivery: PUSH`, and tacplus generates three things. First, a sys contract `configpush.ConfigPush`: one bidi
  stream per (tenant, owner) that carries a snapshot, then versions. Each slice goes as its registered field number
  plus bytes, because the owner's proto is never in the tenant store's run. Second, a publisher in the tenant's
  Stratum that watches the slice's record and uses the record revision as the version. Third, the owner's receiver:
  it holds slices in memory only, fails closed until the first snapshot, accepts only a higher version, and accepts
  only the tenant's own `stratum` subject, which it checks through a tenant-registry hook the owner implements
  (kernel#69). The publisher reconnects and sends the snapshot again after a break or an owner restart. Why: the
  2026-11-19 addendum of roadmap `decisions/skipper-and-admiral-2026-11-01.md` decided that tenant config reaches
  the kernel by push, from tenant to system. Generating the contract keeps the kernel from setting a hand-written
  one that tacplus would then have to follow. Depends on #352 (registered slice field numbers). Open questions are
  on the issue: which Stratum record holds a slice, the version's source, whether the owner keeps a slice after its
  stream breaks, the slice's identity, and Java-first parity.
- **Rebuild the build images when an upstream stage image moves** (2026-11-20, #901): `docker:29-cli` moved to
  29.10.0 (go1.27.4, fixing CVE-2026-97054) on 2026-11-16 22:07Z. Every `build:*` had been built that morning, so the
  nightly sweep failed for days on a fix that was already published. `build:latest` is rebuilt only when its files
  change, and each `build:X.Y` only by the weekly line rebuild or a patch tag. The Images workflow could record
  the digest of each floating `FROM` it copies a binary from (`docker:29-cli`, `golang:1.27-trixie`) and rebuild
  `build` when one moves. Another option: the nightly sweep dispatches a rebuild when a finding is `fixed` upstream.
  Since #966 (2026-11-23) each `build:X.Y` is rebuilt the night after an upstream image it starts from is updated
  (`scripts/images-line-stale.sh`); `build:latest` on main still waits for a change or the weekly run.
- **A line's dated tag is immutable only by the nightly check** (2026-11-23, #966): `scripts/images-line-stale.sh`
  skips a line whose `build:<yyyymmdd>-<sha7>` exists, but a release branch's own images.yml (which a line rebuild
  runs) re-pushes the tag on a second dispatch of the same commit the same day (a manual dispatch, then the
  nightly). Proposed: build-manifest refuses an existing dated tag on a line rebuild, as it refuses an existing
  `X.Y.Z`, shipped with the line's next minor (no backport: rule of the issue flow).
- **TypeScript W2S: `W2SError.message` carries the error body's message** (2026-11-23, #953): `requestJson` in
  `w2sm.ts.tmpl` reads the failed response's body for the gRPC code (`w2sResponseCode`) and then throws
  `gRPC-over-REST error: <status> <statusText>`, dropping the body's `error` text. The Go, Python, Rust and Java W2S
  clients already use that text as the message. So kernel's refusals (the `DeleteAccount` confirm-to-proceed warning that
  gateway#2's console shows, `DeleteIdentityLink`'s and the step-up's) reach a web app as an HTTP status line.
  Proposed: a `w2sResponseMessage` helper beside `w2sResponseCode` uses `error`, then `message`, when either is a
  non-empty string, and today's text otherwise. It does no percent-decoding, because the gateway already decodes. The
  change is the unary path only, is small, and is a 2.6 patch candidate. The questions for Jody are on the issue:
  replace the message or add a field; whether the SSE paths are in scope; a node behaviour test or the template pin test
  only; the 2.6 backport; and whether the other clients' opposite gap (they ignore `grpc-status` and the body's `code`)
  gets its own proposal.

- **A Rust client stream's open stamps a tenant's own tid too** (2026-11-18, #258): the generated S2S client of a
  tenant's own service fills an empty route field with its own `tid` claim on unary, server-streaming and bidi calls
  (`tenant_route::caller_route_tenant`), but a Rust client-streaming open is not async, so it cannot read the token
  first: it still refuses an empty `tenant` (`INVALID_ARGUMENT`) and checks the token only in the stream's task.
  An async open (or one taking the token) would make the three languages agree; Go and Java stamp at open.
- **The typed clients stamp the route field before validating the request** (2026-11-18, #258): Go, Java and Rust
  validate the request (`(spec.assert_required)` and friends) before the route check stamps an empty route field
  with the caller's own tid, so a route field the proto marks required is refused as missing before it is stamped.
  No contract marks its route field required today; swapping the two (stamp, then validate) removes the trap.
- **`looper.yaml`, tacplus's looper/builder VM spec** (2026-11-26, #968): a root file naming tacplus's VM (roadmap
  decisions/looper-build-vms-2026-11-24.md point 9; tacplus is the pilot), read only outside the repository (the
  looper's planner, tacplus-dev/cloud#108, the kicker), and `/looper.yaml` in `docs`' paths in `gates.yaml` so a
  spec change gates on the docs set, not `full`. Jody approved it as a file on 2026-11-25 with `c4a-standard-8`
  (what the VM runs on today, measured in #976, #977, #979), not the issue's `c4a-standard-32`: capacity grows only
  by a change to this file when a measurement says so. Waits for a ROADMAP.md entry. Open: the machine key's name
  (`machine` in the issue, `machine_type` in the approval), and whether `vm:` stays while looper#241's kicker
  switches on `LOOPER_VM_REPOS`.

## Promoted / declined
- **A floor check over every version tacplus emits, and template manifests that take versions from one place**
  (2026-11-06, #338) and **`just generated`: a resolved Python lock, and the C# and Swift trees** (2026-11-18, #854):
  promoted 2026-11-22, ROADMAP.md's line for #949 (Jody's standing authority, hub). Built by #949: `just floors`,
  the soundness rule on chassis manifests, and the three locks.
- **`just generated <dir>`: tacplus's security checks scan its generated output** (2026-11-09, #376): promoted
  2026-11-09, ROADMAP.md's line for #376 (Jody, hub). Built by #376.
- **Member port onto gRPC with make-before-break session rotation (Java)** (2026-11-06, #321): promoted 2026-11-05,
  ROADMAP.md's line for #321 (Jody, #259's split).
