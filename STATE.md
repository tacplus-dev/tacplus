# State: tacplus

Overwritten to stay current (loopers and agents). Last updated: 2026-11-21 by looper (#327).

- **Version / line:** v2.6.13 (latest release, 2026-11-15, on `release/2.6`; v2.6.0 at 87adda9e, where `release/2.6` was cut); main is the 2.7 line in progress (this repository is the compiler; it declares no tacplus floor)
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
  In 2.6: `grpcconfig.disk_persistence.archive.member` names the Java archive member instead of the instance
  id, so a restarted writer keeps its chain (#143; Go and Rust when their ServiceManager wires the archive).
  Archive retention is a bucket lifecycle rule, documented with its pitfall in docs/API.md; the writer never deletes (#144).
  The `linked` claim is retired: the generated namespace backstop (record egress, page/Watch, query predicate, record
  write tenant pin) accepts the caller's own `namespace` only, no language generates a `linked` accessor, and
  `JwtPayloadType` 29 is reserved (#171).
  The fetch layer authenticates `https://github.com` clones (`src`, `depends`, the private registry) with `GH_TOKEN`
  (else `GITHUB_TOKEN`) and no other host; `CI_JOB_TOKEN` still goes only to `CI_SERVER_HOST` (#191; released as
  v2.4.2 and v2.5.1).
  The generated Rust chassis signs and verifies RSA and ECDSA with aws-lc-rs: the `rsa` crate (RUSTSEC-2023-0071,
  Marvin) and the elliptic-curve 0.13 stack (crypto-bigint 0.5, RUSTSEC-2026-0387) are gone from the generated
  lockfile, which current-generation checks (#186; backported to release/2.0 through release/2.5 by pull request, as
  v2.0.12, v2.1.5, v2.2.2, v2.3.4, v2.4.3 and v2.5.2 once they merge).
  The generated Rust types crate no longer depends on `pbjson-types`, nor the logger chassis on `chrono` (#855;
  backported to release/2.0 through release/2.6 by pull request, #863-#869). chrono 0.4 (RUSTSEC-2026-0543,
  unmaintained) is still in the generated lockfile, through `prost-wkt-types` and the token chassis's public
  `expires_in()`; removing it changes the generated surface, so it waits for the next minor or major (#862).
  The generated Rust chassis is on the digest 0.11 stack (ed25519-dalek 3, sha2 0.11, hmac 0.13, x509-cert 0.3,
  pkcs8 0.11, signature 3), so no generated lockfile locks block-buffer 0.10 (GHSA-qwgh-2vcv-g2f7), which
  current-generation checks; the model crate pins x509-cert 0.3 itself, and the fixtures and the example pin
  tokio-tungstenite 0.30 (#215; backported to release/2.0 through release/2.5 by pull request, each line's next
  patch once it merges).
  The generated Go, Java and Rust servers end every streaming rpc at the `exp` of the token it opened with,
  `UNAUTHENTICATED` (API.md, Client Send Contract); they checked it only at open, so an expired or revoked caller
  kept its stream (#260, `cross-test/stream-expiry`, in `just gates`; backported to release/2.0 through release/2.5
  by pull request, each line's next patch once it merges). Rust covers a handler's own `additional_services`
  streams too (#931): the handler names its streaming paths (`Handler::additional_streaming_paths`), and when it
  names none every call on those routes ends at exp; backported to release/2.0 through release/2.6 without the
  trait method (every handler-route call ends at exp), by pull request.
  No install text names a `tacplus:X.Y` image (`TestDocsPinNoTacplusImage`); the release branches' install docs,
  which still pinned `tacplus:2.0`, pin their own line (#201; release/2.1 through release/2.5 by pull request,
  #238–#242, as v2.1.7, v2.2.4, v2.3.6, v2.4.5 and v2.5.5 once they merge).
  The record plane of a tenant-owned record store is routed by tenant (#207): every request of both halves carries
  `target_tenant` (field 15) and every rpc declares it as its route field, so the generated clients (Go, Java, Rust)
  pool the store per tenant, the typed record readers take the tenant, and the store's wrapper refuses, whoever the
  caller is, a call routed to another tenant and an entry whose tenant field is not in that tenant
  (`PERMISSION_DENIED`), before the handler (store and redo) sees it. A shared record store's plane is unchanged.
  Fixed on main and backported to release/2.5 by pull request (the next 2.5 patch once it merges).
  Tenant identity after kernel#23 (subject `/tenants/<tid>/services/<svc>`, namespace `/tenants/<name>`, #202): a
  caller's tenant is its namespace claim everywhere (Go, Java, Rust); a tenant-owned instance's routing backstop admits
  the tid of its own subject, and the record plane's route pin holds an entry to the instance's own namespace (it
  read both from the namespace claim, which refused every routed call). A caller's tid is the new `tid` claim
  (`JWT_PAYLOAD_TYPE_TID` 30, stamped by kernel, tacplus-dev/kernel#32): the MCP session tenant and the c2s record
  reader read it only, never derive a tid from the namespace (a name), and refuse a caller without it (#284;
  backported to release/2.6 by #326 and to release/2.5 by #316, each the line's next patch once it merges).
  Deterministic builds (#193): a repository pins the exact release and the build image digest in `tacplus.lock`
  (`tacplus pin` writes it; moving it is a commit); there is no tacplus image (the old tags stay, deprecated, and
  2.6 gets none); `build:X.Y` per line, rebuilt on every release as an immutable `build:X.Y.Z`, carries Kani and
  cargo-fuzz too, and its `tacplus` is `images/tacplus.sh`, which runs exactly the pinned binary, checked against
  `checksums.txt`, and refuses a repository with no pin or a pin of another line. `tacplus verify` reports Go as
  skipped. For Java it runs OpenJML ESC (no longer JBMC, #257) over the JML specs on each generated validator, in
  the Maven module that compiles them; the build image's OpenJML runs on amd64 only (PROPOSALS.md).
  This repository, the compiler and no tacplus project, pins its own gates' build image by digest in
  `build.lock` (#340: `build:2.6.13`, what `build:latest` resolves to), which the build pipeline (tacplus-dev/build)
  plans from, so every land runs the gates and the security checks there; `security-exceptions.yaml` accepts the
  first pinned build's 122 HIGH findings rule-wide until 2026-12-31 (Jody, #340), each under its bug issue
  (#565–#584, #753–#756, #813), and the build fails from 2027-01-01 on whatever is left.
  `registry.json`'s layout is tacplus's own (#307): before, protojson chose one or two spaces after each `:` from a
  hash of the binary, so it flipped between patches and between amd64 and arm64. Now it is always two (2.5.8's
  format), pinned by a golden test on every supported line.
  Trace propagation, the patch half of #313 (`tacplus-dev/roadmap:decisions/trace-propagation-2026-11-03.md`): every
  generated projection and transformation output copies its source's `trace_id`, and every generated store path
  clears field 200 before it persists (the record dispatch, the service_store disk helpers, the store wrappers and the
  record write surface, the record store's subscriptions, state `Put`/`Set`), in Go, Java and Rust
  (`cross-test/trace-carry`, in `just gates`; on main for 2.7, and backported to release/2.0 through release/2.6 by pull requests #330-#336, each line's next patch).
  In 2.7 (#327): every message of the sys contract carries field 200 (not `sys/spec.proto`'s option schemas or
  `trace.TraceEvent`), injected like a user's; every generated helper that builds its own request has an additive
  traced form (Go `…Traced`, Java an overload with a trailing `traceId`, Rust `…_traced`): the state clients, the
  record readers (and the generated record C2S handlers pass their request's trace to them) and `TokenForExternal`;
  `cross-test/trace-carry` drives each against a recorder of field 200 and checks every generated sys message.
  Gap: a message without field 200 is not yet refused, and the untraced forms stay (#328, 3.0); the `generated/`
  protos, the approval check, the EventBus subscriber, a Go state topic's `Subscribe` and a singleton's `Set` stay
  untraced (PROPOSALS.md); a Java state queue (#456) does not compile, so the gate's fixture has none.
  Gates scaled to the diff (#266, tacplus-dev/looper#48): `gates.yaml` maps a land's changed paths to gate sets. A
  change to `*.md` or `docs/**` only runs `just docs` (the planning files check, relative links through
  `go run ./cmd/sitegen -check-links`, gitleaks, `just test`: about a minute); `images/` runs `just test`, `just images`
  and `just tacplus-script`; `pkg/**`, `cmd/**`, `docs/embed.go`, `docs/*.tmpl`, the justfile, any path the map does
  not name and a change to the map run `just gates`, as do the nightly gates and every release.
  `just gates` runs its gates and their language legs concurrently (`scripts/gates.sh`), `GATES_JOBS` at once (half
  the CPUs by default), and caps current-generation's own fixture pool (#812) at the same number; each job slot builds
  Rust in its own cargo directory with its share of the CPUs (`CARGO_BUILD_JOBS`, `GOFLAGS -p`, a 40-minute `go test`
  deadline). The log and the verdict are the same whatever the cap, and a table of each gate's wall time comes before
  the verdict: 3396 s on 8 CPUs at `GATES_JOBS=4`, current-generation the longest (#300). Each gate's rc and wall time
  also print as it ends, and a leg past `GATES_TIMEOUT` (5400 s) is stopped and fails naming itself; every Java
  fixture logs io.grpc and io.netty at INFO, which kept the trace under GitLab's 4 MiB (#893).
  The generated code is scanned at the source (#376, tacplus-dev/looper#108): `just generated DIR` writes every proto
  set the current-generation gate builds, in Go, Rust and Java (services) and Python and TypeScript (clients), and
  the C2D fixture as Swift and C# clients, as trees the build's security checks load (a Python
  `requirements.lock.txt`, a C# `packages.lock.json` and a Swift `Package.resolved` among the locks, #949); the
  gates job keeps it as `generated/` and the security job scans it with `security-exceptions.yaml`
  and `security-baseline.json`, which records the generated tree's 8827 MEDIUM and LOW findings of its first scan
  (#854), so a report shows only new ones (docs/DEVELOPMENT.md). Gap: until #196's kitchensink, an emitter no fixture reaches is not scanned.
  `just floors` (#949, in `just gates`) checks every floor tacplus hands a consumer against OSV at run time: each
  dependencies.yaml inventory and each manifest of the cross-test models it generates (Rust, Python, TypeScript,
  C#, Swift), failing on an advisory the floor admits; a chassis manifest template names no version the inventory
  declares (soundness). Versions generator code still names outside the inventory are in PROPOSALS.md.
  The build image's docker-buildx is built from the v0.39.1 source with moby/go-archive 0.3.3 (CVE-2026-17106); its
  release carries golang.org/x/net 0.62.0 (CVE-2026-78669, CVE-2026-78661, CVE-2026-97045) and containerd v2.4.2
  (CVE-2026-95839, #930); `tacplus-smoke` checks the floors
  (#190; backported to release/2.0 through release/2.5 by pull request, as v2.0.13, v2.1.6, v2.2.3, v2.3.5, v2.4.4
  and v2.5.3 once they merge, after #186's).
  The build image's gh is built from the v2.103.2 source with golang.org/x/net 0.62.0 (CVE-2026-78661: gh's
  releases, through 2.103.3, build with 0.60.0); `tacplus-smoke` checks the floor (#350; backported to release/2.1
  through release/2.6 as v2.1.14, v2.2.11, v2.3.13, v2.4.13, v2.5.14 and v2.6.3; release/2.0's image ships no gh).
  The base image's Maven carries guava 33.7.2-jre in place of the 33.6.0-jre Maven 3.9.16 ships (CVE-2026-102554;
  checksum-pinned, `mvn` builds a project in the image build, `tacplus-smoke` checks the floor) (#222; backported
  with #190 on each release line's pull request).
  The base and build images carry Go 1.27, Node 26 (LTS) and Swift 6.4.2 (#255; main only, from 2.6: the
  supported `build:X.Y` lines keep their toolchains). The rule beside the pins: Go follows the newest release, Node
  the newest LTS, Swift the newest release.
  On main (2.7): the build image builds images itself, with rootless BuildKit 0.34.2 under RootlessKit 3.3.0 as the
  user `build` (`tacplus-image-build`; no host Docker socket; multi-arch through BuildKit's QEMU helpers), and carries
  sccache 0.19.0. BuildKit, its runc 1.5.2 and RootlessKit are built from their release tags, each checked against
  a pinned commit, with go-archive and x/net past the advisories of their release binaries (`tacplus-smoke` checks
  the floors); sccache and BuildKit's QEMU helpers are release archives checked against their sha256. The caches come from the build's
  configuration (`TACPLUS_CACHE_REGISTRY`, `TACPLUS_CACHE_BUCKET`, `TACPLUS_CACHE_DIR`; `tacplus-cache env|restore|
  save`, snapshots keyed by content), and a build with none configured is cold. `images/cache-determinism.sh`
  requires that a cold and a warm build give the same verdict and the same digests; `just images` runs it (#315).
  Line rebuild (#283): the weekly Images run on main dispatches images.yml on each supported `release/X.Y`, which
  rebuilds `base:X.Y` and `build:X.Y` from that branch (apt-get upgrade) and moves nothing else; a patch tag builds
  its `build` fresh. A line takes part once its backport of #283 merges (the dispatch skips a branch without it).
  Every Images run upgrades the OS packages (#369): the `apt-get upgrade` layer of base and build depends on
  `APT_REFRESH`, the run's id, so the registry cache never hands back an older run's (v2.6.2's arm64 image was
  v2.6.1's, without the zlib1g fix), and `scripts/images-check-index.sh` fails the run before any release or
  floating tag moves unless the index holds linux/amd64 and linux/arm64, each created during the run.
  The images carry only their declared Debian packages (#554): `images/base.packages` and `images/build.packages`,
  Debian's base system and their dependencies; `tacplus-packages-check`, the last step of each image's build and
  part of `tacplus-smoke`, fails on any other. base starts from `rust:X-slim-trixie`, not buildpack-deps, whose
  ImageMagick -dev packages brought OpenEXR (CVE-2026-42216/-42217, CRITICAL) into every `build:X.Y` before it.
  The cross-test harnesses and the example pin golang.org/x/net at v0.62.0 (GO-2026-6603/6611/6612/6617, the
  HTTP/2 server the generated Go chassis reaches, and GO-2026-6860/6866) in every go.mod they write and every
  dependencies.yaml; `scripts/go_dependency_floor_test.go` refuses a lower or missing pin (#199; backported to
  release/2.0 through release/2.5 by pull request, each line's next patch once it merges).
  Every Java harness pom (the checked-in fixtures and the ones cross-test scripts write) pins, under
  `<dependencyManagement>`, `com.google.guava:guava` 33.7.2-android (GHSA-xxph-c9ww-hj94, through grpc-java) and,
  beside the MCP Java SDK, `tools.jackson.*` 3.1.8 (nine GHSAs in the SDK's 3.1.4); docs/USER.md tells Java
  consumers to pin the same. `scripts/java_dependency_floor_test.go` refuses a lower or missing pin (#200;
  backported to release/2.0 through release/2.5 by pull request, each line's next patch once it merges).
  Every checked-in dependencies.yaml (example/proto's included) pins the TypeScript and Rust floors the generated
  manifests declare past their advisories: axios ^1.20.0, js-yaml ^4.3.2, zod ^4.6.6, rustls 0.23.46, tokio 1.51.6,
  webpki-roots 0.26.11, anyhow 1.0.103, jsonwebtoken 10.3.0 (also the harness Cargo.tomls' tokio), and the fixtures
  and harness go.mods that pin golang.org/x/crypto pin v0.58.1-0.20261009181718-2f1f834330b2 (GO-2026-5932 is fixed
  only after v0.58.0; v0.59.0, tagged 2026-11-11, is in PROPOSALS.md). `scripts/dependency_floor_test.go` and `scripts/go_dependency_floor_test.go`
  refuse a lower pin (#312; backported to release/2.0 through release/2.6 by pull request, each line's next patch
  once it merges).
  Generated member port (#212): `option (spec.member_port) = {}` makes the ServiceManager own a member port (Java;
  Go and Rust since #322, one wire) for the service's black-box member traffic (Hazelcast in Stratum). Since #321 it is a gRPC bidi stream of byte
  chunks (`/memberport.MemberTunnel/Open`, `sys/memberport.proto`) on its own TLS 1.3 listener with the auto-TLS
  certificate: kernel tokens for `member:<svc>` checked at each stream's open, both ways (the receiver's in a
  response header), same service and namespace only; make-before-break session rotation at 75% of a token's
  lifetime with an in-band `last` each way and same-subject session binding; `UNAUTHENTICATED`/`PERMISSION_DENIED`
  with no reason text; streams closed at the earlier `exp` of the client's and the receiver's token (#260, #465), so
  a lapsed member loses the sessions it accepted as well as those it opened; a stream ending without `last` drops
  the session; a dropped session resets its loopback connection (RST); nothing dialled before admission, no
  plaintext.
  `grpcconfig.member_port` places it (loopback target only); `sm.getMemberPort().forward(remote)` (Go
  `sm.MemberPort().Forward`, Rust `sm.member_port()`) tunnels outbound. registry.json carries `member_port`.
  `(spec.smb)`/`(spec.smb_authority)`/`SmbCertService` are deprecated (removed in 3.0) and exclusive with it.
  Pinned by `cross-test/memberport` (`just memberport`, in the gates): per language, the memberport-peer tests
  (rotation across several token lifetimes both ways, no-splice faults, refusals, resets); two tenants'
  Hazelcast 5.7 members through each language's ports; `just stream-expiry` runs each language's expiry leg. Make-before-break rotation of every
  generated S2S stream (#322, `sys/rotation.proto`, the `<Svc>Rotating` sibling): Go, Java and Rust server and
  client (`chassis/rotation`, `just rotation`); a client falls back to the plain method on a server without the sibling.
  Fleets pin services by release line (#180): `fleet.yaml` names a `version: "MAJOR.MINOR"` for every service (or
  an explicit `MAJOR.MINOR.PATCH-pre`), and `tacplus fleet build` resolves it to the line's highest released tag,
  pre-releases excluded, and to that release's image on ghcr.io by digest; `fleet.lock` records version, commit and
  image; `--check` reports newer patches and minors. A service without a version, a plain-directory repo, an
  unresolvable line and a release without its image are refused.
  Gateway contributions, part 1 (#195): a `kind: service` project's `tacplus.yaml` may declare `contribute:` (a
  `webapp` module and `clients`, each by one fixed recipe key: `rust:`, `go:`, `swift:`, `dotnet:`), validated on
  read; `tacplus fleet build` checks each recipe's inputs at the locked commit and records the contribution under
  the service in `fleet.lock` (paths under `/-/apps/<service>/` and `/-/clients/<service>/`, targets, the webapp
  dir's tree). Contract and trust rule in docs/USER.md.
  Gateway contributions, parts 2 and 3 (#195): `tacplus fleet gateway` builds the fleet's gateway image from
  `fleet.lock` alone (the gateway, never a release image, is the one image a fleet builds): the locked gateway
  generated against the merged protos, its own webapp at `/`, each contributed module at `/-/apps/<service>/` and
  the `/-/apps.json` index, the clients by their fixed recipes, the launcher (`fleet.yaml`'s new `launcher:` line),
  then the image by `tacplus-image-build` at the gateway commit's time into an OCI layout. The lock's `gateway:`
  section records the build and base images by digest, each part's sha256 and the image digest; `--check` rebuilds
  and fails on any that differs. darwin/arm64 `rust:` and `swift:` clients come from the macos runner
  (`--clients-only` there, `--prebuilt` here). A gateway `webapp` contribution is refused. The build image carries
  cargo-zigbuild, zig and the three Rust cross targets for the `rust:` recipe. Gap: no gateway release yet serves
  `/-/` (gateway#21 is on main, after v0.2.0), so the end-to-end build of tenantecho's page waits on one.
  Redact for any type (#274): `(spec.sensitive)` on string/bytes/message fields, `(spec.sensitive_message)` (a
  message secret by construction) and `(spec.not_sensitive)`; Go, Java and Rust generate a deterministic
  `Redact<T>` (a set sensitive value becomes `REDACTED`, `<field>_redacted` set, recursion through nested, repeated
  and map fields, `trace_id` kept) and, for `required` / `min_entropy_bits`, a validator (required, placeholders,
  integer entropy estimate) whose errors name the field and rule, never the value. A gate refuses an unannotated
  sensitive-looking field in the tree of any type with a `Redact<T>`. Pinned byte for byte across the three by
  `cross-test/redact` (`just redact`, in the gates) against a hand-built fixture. The legacy egress redaction
  (`(spec.redact)`, `(spec.data_entitlement)`, `Sanitize<T>`, `<T>Entitlement`) is deprecated (generation warns),
  removed in 3.0. Gaps: no cross-package sensitive tree, no oneof leaves, no client-only target (PROPOSALS.md).
  The generated Go chassis pins a TLS peer on every handshake, a resumed one too (#359): `x509.TLSConfig` verifies
  in `VerifyConnection` (crypto/tls skips `VerifyPeerCertificate` on resumption, gosec G123), and the mesh and
  bootstrap clients (servicemanager, service_im) share `x509.SpiffeClientTLSConfig`, whose `InsecureSkipVerify` is
  justified in place (`#nosec G402`: its chain check runs in `VerifyConnection`); `x509_resumption_test.go` refuses a
  resumed session to a peer that no longer passes. gosec 2.30 on the generated example service reports no G402, G123,
  G115, G404, G703 or G704: each was fixed (enum parse bounded to int32, gRPC codes, IP packing via encoding/binary,
  JWK exponent, the AWS STS region validated) or annotated with its reason (wire two's complement, retry jitter,
  operator-named paths). Backported to release/2.0 through release/2.6 by pull request, each line's next patch.
  Sign-in config in proto (#224, main, for 2.7): `sys/signin.proto` (package `signin`) declares the deployment's
  account-model switch (`Deployment`), a tenant's providers offered, authorized OIDC IdP (issuer, client, the exact
  tenant id for a multi-tenant IdP, a declared non-reassignable subject claim), registered domains (subdomains by
  suffix, DNS verification state), join mode and durations (`Tenant`), and `Subject`. Three new field annotations,
  generated in Go, Java, Rust, Python and TypeScript: `assert_registrable_domain` (a public suffix refused, by the
  Public Suffix List snapshot embedded in `pkg/internal/common/publicsuffix`, one table in every language),
  `assert_not_address` and `assert_enum_not_in`; so generated validation refuses a registered public suffix, a SAML
  IdP and an address-shaped subject (`cross-test/current-generation` self-tests). Kernel holding it in its tenant
  config slice and enforcing the flow is kernel's; the single-account mode's shape is still to be designed.
- **Released / deployed:** v2.6.0 is the newest release: the binaries and `checksums.txt`, and
  `ghcr.io/tacplus-dev/tacplus/build:2.6.0@sha256:8e9a18bd7ad51df00c16ebe6a3d8af534dee4271f9758719ced8c30a664da54c`
  (also `build:2.6`); no tacplus image (the first release without one, #193). Not deployed.
- **Known gaps:** The member port needs kernel to mint `member:<svc>` (tacplus-dev/kernel#30) before Stratum adopts
  it (stratum#17); its ServiceManager wiring is compiled, not run (KNOWN_TEST_GAPS MP-1); no cross-language rotation
  leg yet, and the member ports keep their own rotation code (KNOWN_TEST_GAPS RT-1). The redo service and the per-tenant Stratum that implement the redo contract are their repos'
  (tacplus-dev/redo#1, tacplus-dev/stratum#2). The gateway does not yet route by the route field nor stamp `McpRequest.tenant`
  (tacplus-dev/gateway#3 and its MCP follow-up). MCP on a tenant-owned service is same-tenant only (PROPOSALS.md).
  No C2S record writes (by decision, 2.5). The Rust wrapper renders no (spec.store_namespace) predicate pin on the
  query plane (PROPOSALS.md). The looper image's tacplus runs the tacplus image, which 2.6 does not have
  (PROPOSALS.md). The generated Rust Kani harnesses prove only that `validate()` refuses each
  integer/enum field bound, and only for messages whose `validate()` Kani can compile and run (#211): `just verify
  rust` verifies all 52 on cross-test/proto (Kani 0.68); messages with regex-backed, nested-message, map or sensitive
  checks get none, and #196 still moves verification to a kitchensink proto set. Formal verification is not a gate (#275): `just verify` runs it,
  `just verify-changed <sha>` says whether the nightly needs it; the pipeline that runs them on release tags and
  nightly is tacplus-dev/looper#74, its nightly off until #257 passes. Java's (#257) covers
  cross-test/proto until the kitchensink (#196) lands; string byte bounds have no spec (PROPOSALS.md). The new build image (Kani, cargo-fuzz, the launcher) is verified by
  images.yml once pushed; the launcher and `tacplus pin` by the gates. The fleet build's image lookup on a private package is
  verified against a fake registry only: the looper's token has no `read:packages` (a public image resolves live).
  Rootless BuildKit (#315) needs a user namespace (`--security-opt seccomp=unconfined
  --security-opt apparmor=unconfined`), which no looper container may have. images.yml builds base and build with
  `tacplus-image-build` in `build:latest` and runs `images/cache-determinism.sh` in each new build image (#315). Its
  first run (2026-11-16, schedule) failed there: Debian's setuid newuidmap needs CAP_SYS_ADMIN in a container, so
  the image now gives newuidmap and newgidmap file capabilities instead (#830). On GitHub's Ubuntu 26.04 runner,
  with them, buildkitd starts under Docker's default capabilities (a diagnostic run, not images.yml); images.yml
  publishing a build image with `tacplus-image-build` is still to be seen. That run builds with buildx on the
  runner, as the `build:latest` it finds predates `tacplus-image-build`; the rootless path first runs in the run
  after it, or on a dispatch.
  The compiler half of `images/cache-determinism.sh` passed in a build:2.5-derived image. The build jobs in the
  `ci` project (tacplus-dev/looper, tacplus-dev/cloud) do not set the cache variables or the two security options
  yet (PROPOSALS.md).
  The record plane's tenant checks assume a tenant's
  namespace is `/tenants/<tid>`; kernel#23's `/tenants/<name>` namespaces need #202 first (PROPOSALS.md).
  The record write surface stamps no `(spec.identity_spiffe_subject)` (PROPOSALS.md). A fleet's service images must be on ghcr.io from a
  github.com repo (PROPOSALS.md).
- **Blocked on:** #195's gateway build (`tacplus fleet gateway`: the merged webapp, the clients, the image and the
  lock's `gateway:` section) waits on tacplus-dev/gateway#6 (its first release) and tacplus-dev/gateway#21 (serving
  `/-/`). The example module is done (tenantecho v0.2.0).
