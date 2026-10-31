# State: tacplus

Overwritten to stay current (loopers and agents). Last updated: 2026-10-30 by looper (#212).

- **Version / line:** v2.5.0 (latest release, 2026-10-28, `release/2.5` cut at it); main is the 2.6 line in progress (this repository is the compiler; it declares no tacplus floor)
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
  On main (2.6): `grpcconfig.disk_persistence.archive.member` names the Java archive member instead of the instance
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
  The generated Rust chassis is on the digest 0.11 stack (ed25519-dalek 3, sha2 0.11, hmac 0.13, x509-cert 0.3,
  pkcs8 0.11, signature 3), so no generated lockfile locks block-buffer 0.10 (GHSA-qwgh-2vcv-g2f7), which
  current-generation checks; the model crate pins x509-cert 0.3 itself, and the fixtures and the example pin
  tokio-tungstenite 0.30 (#215; backported to release/2.0 through release/2.5 by pull request, each line's next
  patch once it merges).
  No install text names a `tacplus:X.Y` image (`TestDocsPinNoTacplusImage`); the release branches' install docs,
  which still pinned `tacplus:2.0`, pin their own line (#201; release/2.1 through release/2.5 by pull request,
  #238–#242, as v2.1.7, v2.2.4, v2.3.6, v2.4.5 and v2.5.5 once they merge).
  The record plane of a tenant-owned record store is routed by tenant (#207): every request of both halves carries
  `target_tenant` (field 15) and every rpc declares it as its route field, so the generated clients (Go, Java, Rust)
  pool the store per tenant, the typed record readers take the tenant, and the store's wrapper refuses, whoever the
  caller is, a call routed to another tenant and an entry whose tenant field is not in that tenant
  (`PERMISSION_DENIED`), before the handler (store and redo) sees it. A shared record store's plane is unchanged.
  Fixed on main and backported to release/2.5 by pull request (the next 2.5 patch once it merges).
  Deterministic builds (#193): a repository pins the exact release and the build image digest in `tacplus.lock`
  (`tacplus pin` writes it; moving it is a commit); there is no tacplus image (the old tags stay, deprecated, and
  2.6 gets none); `build:X.Y` per line, rebuilt on every release as an immutable `build:X.Y.Z`, carries Kani and
  cargo-fuzz too, and its `tacplus` is `images/tacplus.sh`, which runs exactly the pinned binary, checked against
  `checksums.txt`, and refuses a repository with no pin or a pin of another line. `tacplus verify` reports Go as
  skipped.
  The build image's docker-buildx is built from the v0.38.1 source with moby/go-archive 0.3.3 and golang.org/x/net
  0.62.0 (CVE-2026-17106, CVE-2026-78669, CVE-2026-78661, CVE-2026-97045; `tacplus-smoke` checks the floors)
  (#190; backported to release/2.0 through release/2.5 by pull request, as v2.0.13, v2.1.6, v2.2.3, v2.3.5, v2.4.4
  and v2.5.3 once they merge, after #186's).
  The cross-test harnesses and the example pin golang.org/x/net at v0.62.0 (GO-2026-6603/6611/6612/6617, the
  HTTP/2 server the generated Go chassis reaches, and GO-2026-6860/6866) in every go.mod they write and every
  dependencies.yaml; `scripts/go_dependency_floor_test.go` refuses a lower or missing pin (#199; backported to
  release/2.0 through release/2.5 by pull request, each line's next patch once it merges).
  Generated member port (#212): `option (spec.member_port) = {}` makes the Java ServiceManager own a member port
  for the service's black-box member traffic (Hazelcast in Stratum): server TLS 1.3 with the auto-TLS certificate,
  ALPN `tacplus-member/1`, framed `AUTH`/`DATA`/`CLOSE_WRITE`, kernel tokens for `member:<svc>` checked both ways and
  refreshed in-band, same service and namespace only, drop on expiry, nothing forwarded before `AUTH`, no plaintext;
  `grpcconfig.member_port` places it (loopback target only); `sm.getMemberPort().forward(remote)` tunnels outbound.
  registry.json carries `member_port`. Go and Rust refuse the annotation until #236/#237 (required before 2.6.0).
  `(spec.smb)`/`(spec.smb_authority)`/`SmbCertService` are deprecated (removed in 3.0) and exclusive with it.
  Pinned by `cross-test/memberport` (`just memberport`, in the gates): the memberport-peer tests and two tenants'
  Hazelcast 5.7 members.
  Fleets pin services by release line (#180): `fleet.yaml` names a `version: "MAJOR.MINOR"` for every service (or
  an explicit `MAJOR.MINOR.PATCH-pre`), and `tacplus fleet build` resolves it to the line's highest released tag,
  pre-releases excluded, and to that release's image on ghcr.io by digest; `fleet.lock` records version, commit and
  image; `--check` reports newer patches and minors. A service without a version, a plain-directory repo, an
  unresolvable line and a release without its image are refused.
- **Released / deployed:** v2.5.0 is the newest release (binaries and checksums; images as before: from 2.6.0 a
  release publishes binaries and `build:X.Y.Z`, and no tacplus image).
- **Known gaps:** The member port needs kernel to mint `member:<svc>` (tacplus-dev/kernel#30) before Stratum adopts
  it (stratum#17); its ServiceManager wiring is compiled, not run (KNOWN_TEST_GAPS MP-1); Go and Rust do not generate
  it yet (#236, #237). the redo service and the per-tenant Stratum that implement the redo contract are their repos'
  (tacplus-dev/redo#1, tacplus-dev/stratum#2). The gateway does not yet route by the route field nor stamp `McpRequest.tenant`
  (tacplus-dev/gateway#3 and its MCP follow-up). MCP on a tenant-owned service is same-tenant only (PROPOSALS.md).
  No C2S record writes (by decision, 2.5). The Rust wrapper renders no (spec.store_namespace) predicate pin on the
  query plane (PROPOSALS.md). The looper image's tacplus runs the tacplus image, which 2.6 does not have
  (PROPOSALS.md). `tacplus verify` (Kani) fails on every generated Rust crate: the generated harnesses do not
  compile (#211; #196 stops generating them). The new build image (Kani, cargo-fuzz, the launcher) is verified by
  images.yml once pushed; the launcher and `tacplus pin` by the gates. The fleet build's image lookup on a private package is
  verified against a fake registry only: the looper's token has no `read:packages` (a public image resolves live).
  `depends`/`src` at a line can take a pre-release (#214). The record plane's tenant checks assume a tenant's
  namespace is `/tenants/<tid>`; kernel#23's `/tenants/<name>` namespaces need #202 first (PROPOSALS.md).
  The record write surface stamps no `(spec.identity_spiffe_subject)` (PROPOSALS.md). A fleet's service images must be on ghcr.io from a
  github.com repo (PROPOSALS.md).
- **Blocked on:** nothing.
