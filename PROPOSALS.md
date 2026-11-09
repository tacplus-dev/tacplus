# Proposals: tacplus

Written by loopers. Each proposal: what, why, links. Jody promotes accepted ones into ROADMAP.md (via an agent);
the looper then marks it `promoted` or `declined` here with the date.

## Open
- **Per-language gate sets, and more of the map** (2026-11-08, #266): `gates.yaml` runs the full gates (~3h) for any
  change under `pkg/**`, `cmd/**` or `.github/`. Next: `pkg/internal/<lang>/**` and `pkg/templates/<lang>/**` to that
  language's `current-generation-<lang>`, `determinism-<lang>` and `-<lang>` cross-tests (with `just test`), once each
  language's recipes are confirmed to cover what such a path can break (shared templates and `pkg/internal/common`
  stay full); `cmd/sitegen/**` and `site/**` to `just docs` plus `just site`; `.github/workflows/**` to a workflow
  lint. Also for `just docs`: it checks relative links but not their anchors (GitHub's heading slugs) nor external
  URLs, and does not build the site (`site/docs/*.md` link to site paths; `just site` checks them, needing a venv).
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
- **A release tag never builds on another line's base** (2026-11-08, #283): a release tag's Images run starts its fresh
  `build` from its line's `base:X.Y`, else `base:latest` (main's). Before any line rebuild had pushed a `base:X.Y`, the
  v2.1.13–v2.5.13 runs took main's base (Swift 6.4.2) under their lines' Swift 6.1.3 plugins and failed the smoke test
  (`protoc-gen-swift: undefined symbol`, run 42428041155), and so did every 2.1–2.5 tag cut since (v2.1.14–v2.5.15).
  v2.0.20 passed, so `build:2.0.20` carries main's SDKs instead of 2.0's pins. Once the weekly line rebuild has pushed every `base:X.Y`, the fallback is unreachable for a supported
  line. Still, the plan step should rebuild `base` from the tag when `base:X.Y` is missing, not fall back. That change
  goes in `images.yml` on main and on each `release/*`.
- **`just generated <dir>`: tacplus's security checks scan its generated output** (2026-11-09, #376): the other
  half of tacplus-dev/looper#108 (services' scans skip their `output-dir`, so generated code is scanned nowhere until
  tacplus scans it). A recipe generating Go/Rust/Java (chassis included) and Python/TypeScript into `<dir>/<set>/<lang>/`,
  each loadable by its scanners (`go.sum`, `Cargo.lock`, compiled `target/classes` for SpotBugs, npm lock), touching no
  tracked file; the `current-generation` fixtures until #196's kitchensink replaces the list. First scan's findings
  (#359, #368) fixed in the generator or accepted in a new `security-exceptions.yaml`. Open: exceptions match by id
  repo-wide (a path scope is a looper change), lite clients (Swift, C#), backport to 2.6.x. Approved at hub triage
  2026-11-08; needs a ROADMAP.md line citing #376.
- **One version resolver for `depends`/`src` and the fleet build** (2026-11-06, #214): `pkg/protosrc/resolve.go`
  (`findLatestPatchTag`, `compareSemverTags`, `exactVersionRe`) and `pkg/fleet/version.go` (`parseVersion`,
  `highest`, `resolveVersion`) each parse and rank `vMAJOR.MINOR.PATCH[-pre]` tags. #214 was the protosrc copy
  disagreeing with the fleet one on pre-releases. A small shared package (parse, compare, latest release of a line)
  used by both would keep "a line is its latest patch release, pre-releases only when named" in one place.
- **trace_id in 2.7: field 200 on every sys wire message, additive traced helper forms** (2026-11-06, #313 → #327):
  the hub's split of #313. Every request-building generated helper (state clients' `Fetch`/`Delete`/`Query`, record
  readers, record-plane and kernel state helpers) gets a form that takes `trace_id`, beside today's, and every sys
  contract wire message gets field 200 (not `spec.proto` option schemas, `<T>Public` or `TraceEvent`). Unblocks
  coop#7, oauth#19, blobstore#12, llmproxy#18/#19, mailbox#12, stratum#29. On ROADMAP.md since 2026-11-06.
- **trace_id in 3.0: one traced signature per helper; a message without field 200 fails generation** (2026-11-06,
  #313 → #328): drops the untraced forms #327 adds, and turns today's silent skip of a message that lacks field 200
  or declares `trace_id`/`noop` at another number (`InjectTraceFields`) into an error (a warning from 2.7). Needs a
  ROADMAP.md line citing #313.
- **Trace clearing on queue and command state paths** (2026-11-06, #313): the #313 patch clears `trace_id` where a value is
  stored (state `Put`/`Set`, store-derived subscriptions, store wrappers, disk helpers) and leaves it on topic
  publishes, queue offers and commands, which deliver a message to a consumer that propagates it. If a queue is to
  count as storage (its items persist until taken), the consumer loses the trace; that is a decision, not a fix.
- **Kani harnesses beyond integer bounds** (2026-11-06, #211): the generated Rust harnesses now prove only that
  `validate()` refuses each integer/enum bound, and skip a message whose `validate()` reaches the regex crate (Kani
  0.68 panics compiling it), a nested message, a map field (prost's `HashMap` default makes a syscall Kani does not
  model) or a sensitive check; they stub `std::fmt::format`, which Kani cannot finish. Wider coverage: bounded
  strings/bytes (`kani::vec::any_vec` behind `String::from_utf8`) for the size checks; same-package nested messages
  (the generator can see their validators); a stub of `RandomState::new` for map fields; regex validators stubbed by
  `#[kani::stub]` with the claim restricted to the other fields. The "accepts every value inside the bounds" claim
  needs a model of every check of the message, which #196's kitchensink is the place for.
- **A floor check over every version tacplus emits, and template manifests that take versions from one place**
  (2026-11-06, nightly scan, #338): four nights of scans found advisories in floors the generated manifests declare
  (#264, #309, #312, #338). No scanner sees them: a lockfile resolve, including the proposed `just generated`
  recipe, picks the newest compatible release, so `cargo audit`/`npm audit`/OSV-Scanner on it pass while the
  manifest still promises a vulnerable floor. Two parts: (1) a check (soundness or a gate) that generates the
  cross-test models, collects every declared floor (`Cargo.toml`, `package.json`, `pyproject.toml`, the Go/Java
  inventory) and fails on an OSV advisory at the floor itself, with its database fetched at run time like
  govulncheck's; (2) a soundness rule that a chassis template's manifest names no version literal for a crate or
  package the inventory declares (`{ workspace = true }` in Rust, a template variable in TypeScript/Python), so
  `dependencies.yaml` is the single place a floor moves. #338 found the same crates at three different floors
  across sibling chassis.
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
- **End Rust `additional_services` streams at their token's exp** (2026-11-01, #260): the Go and Java servers end
  every streaming rpc at its opening token's `exp`, handler-provided services included, because gRPC tells their
  interceptors the call type. The Rust `StreamCredentialExpiryLayer` is a tower layer that cannot, so it polices only
  the generated `STREAMING_RPC_PATHS`, and a stream on a handler's own `additional_services` route still outlives its
  token. Reading the streaming paths off the handler's routes (or a handler-declared list) would close it.
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
- **Cargo's retried network errors fail the clean-log check** (2026-11-01, #255): `assert_clean_log` in
  `cross-test/current-generation/run.sh` (and its copies in the other cross-tests) fails on any `warning:` line, so
  cargo's `warning: spurious network error (3 tries remaining): [16] Error in the HTTP2 framing layer`, which cargo
  then retries successfully, failed `current-generation` and the land of #255. Pre-fetching the crates
  (`cargo fetch`) before the checked build, or not counting cargo's retry warnings, would keep a network blip from
  failing a 3h gate run.
- **Refuse a `contribute.webapp` from the gateway itself** (2026-10-31, #195): the gateway's own webapp is the base at
  `/`, not a contribution, but the fleet build does not know which listed service is the gateway until
  `tacplus fleet gateway` requires one (part 3). It should then refuse a `webapp` contribution from it by name.
- **Per-client target subsets** (2026-10-31, #195): each recipe builds its fixed target set (rust/go: four, swift:
  darwin/arm64, dotnet: windows/amd64). A Linux-only CLI or a daemon with no Windows port has no way to say so; an
  optional `targets:` narrowing the recipe's set (never widening it) would cover that, if a service needs it.
- **Cap the member port's established tunnels** (2026-10-30, #212): admission is bounded (a 10-second deadline and at
  most 256 connections proving themselves), but authenticated tunnels are not counted: a member of the same service
  and tenant can open as many as it likes, each with two threads. A configurable cap would bound a misbehaving or
  compromised member. Same design for Go/Rust (#236, #237).
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
- **Drop the from-source docker-buildx once a buildx release carries the fixes** (2026-10-29, #190): the build
  image builds buildx v0.38.1 from source with moby/go-archive 0.3.3 and golang.org/x/net 0.62.0, because no buildx
  release (master included) has taken them yet. When one does, set `BUILDX_VERSION` to it and drop the bumps (and
  then the `buildx` stage, copying it from `docker:${DOCKER_VERSION}-cli` again); `tacplus-smoke` keeps the floors.
- **Drop the from-source gh once a gh release carries golang.org/x/net >= 0.62.0** (2026-11-07, #350): the build
  image builds gh v2.103.2 from source with x/net 0.62.0 (CVE-2026-78661), because gh's releases through 2.103.3
  build with 0.60.0. When one does, set `GH_VERSION` to it and download its release tarball in the `fetch` stage
  again, dropping the `gh` stage; `tacplus-smoke` keeps the floor.
  A nightly check that the image's Go binaries carry no module under a known fix (what Trivy found here) before the
  sweep fails would make the next one a patch rather than a red run.
- **Drop the replaced Maven guava once a Maven release bundles 33.7.2 or later** (2026-11-02, #222): Maven 3.9.16
  (the newest 3.9) ships guava 33.6.0-jre (CVE-2026-102554), so `images/base.Dockerfile` swaps in
  `MAVEN_GUAVA_VERSION` (checksum-pinned). When a Maven release ships a fixed guava, drop the swap;
  `tacplus-smoke` keeps the floor.
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
  advisory database fetched at run time) would find it first. It also reports four unmaintained crates today
  (`rustls-pemfile`, `backoff`, `instant`, `unsafe-libyaml`) worth replacing. GitHub-only advisories (no RUSTSEC id,
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
- **The root `Dockerfile` is a stale image definition** (2026-10-29, #193): it layers a binary on a `tacplus-base`
  built from a `Dockerfile.base` that does not exist, and nothing builds it. With the tacplus image gone it can be
  deleted.
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
  move as a `bug` instead of leaving it to judgment.

## Promoted / declined
- **Member port onto gRPC with make-before-break session rotation (Java)** (2026-11-06, #321): promoted 2026-11-05,
  ROADMAP.md's line for #321 (Jody, #259's split).
