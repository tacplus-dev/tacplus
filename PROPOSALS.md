# Proposals: tacplus

Written by loopers. Each proposal: what, why, links. Jody promotes accepted ones into ROADMAP.md (via an agent);
the looper then marks it `promoted` or `declined` here with the date.

## Open
- **One crypto library in the generated Rust service** (2026-10-29, #186): the chassis keys (RSA, ECDSA) are
  aws-lc-rs's, because ring cannot generate RSA keys nor sign with one over 4096 bits, both of which the chassis
  offers in every language (`generate_rsa_8192_key`); TLS stays on rustls's ring provider. A service now compiles
  both. Moving rustls to its aws-lc-rs provider (`rustls/aws_lc_rs`, the rustls default) would leave one; it changes
  the TLS stack of every generated Rust service, so it is a minor's change, with the TLS parity gates.
- **cargo audit of a generated sample, nightly** (2026-10-29, #186): current-generation now refuses the two crates of
  #186 by name in the generated lockfile, but a new advisory on any other crate the chassis locks is found only by
  each service's own scan. A nightly `cargo audit` of a generated sample (cargo-audit in the build image, the
  advisory database fetched at run time) would find it first. It also reports four unmaintained crates today
  (`rustls-pemfile`, `backoff`, `instant`, `unsafe-libyaml`) worth replacing. GitHub-only advisories (no RUSTSEC id,
  like #215's block-buffer) need OSV, not cargo-audit.
- **Bound the shared cross-test cargo target** (2026-10-29, #186): `target/cross-test/cargo` (`CROSS_TEST_CARGO_TARGET`,
  shared by current-generation, server-runtime and wire-corpus) is never pruned; on a looper host it had reached
  108G and filled the disk mid-gate (`No space left on device` in current-generation-rust). Each fixture's
  differently-named crates add artifacts that later runs never reuse. Pruning it when it passes a size (or `cargo
  clean` per fixture after its check) would keep gates from failing on space.
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
- **The nightly gates outgrow the runner's disk** (2026-10-28, #198): the 2026-10-28 nightly run on 5adf7846
  (run 40694076433) passed every gate through c2d-server, then failed zero-rpc-s2s and jwk-alg with "No space left on
  device" in target/cross-test (the current-generation cargo/maven trees alone take two hours to build and stay on
  disk). Nightly is red for infrastructure, not code. Either free disk on the runner before `docker run` (drop the
  preinstalled SDKs the build image already carries), or have each gate clean its own scratch tree; the GitLab move
  (#194) is the moment to size the runner for it.
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

## Promoted / declined
