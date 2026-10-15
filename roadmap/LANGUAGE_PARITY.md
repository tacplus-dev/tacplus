# Language parity: Rust versus Go and Java

Survey of 2026-10-14. The full-service targets are supposed to be at parity
(`docs/DEVELOPMENT.md`, Language Parity). The soundness matrix
(`cmd/soundness`) reports 30/30 constraints enforced in all five languages and
no partial-parity gaps, so the gaps below are not in validation or in the
generic server-wrapper pipeline. They are in the newer runtime and record-plane
features, which landed in Rust first. Evidence is the generator source under
`pkg/internal/<lang>/types`, the templates under `pkg/templates/<lang>`, and
the commit history since August.

## Rust has it, Go and Java do not

| Capability | Rust | Go | Java | Evidence |
|------------|------|----|------|----------|
| ~~**S2S readiness gating and reconnect backoff.**~~ **Closed 2026-10-14**: Go (`chassis/serviceconnection/readiness.go`, `backoff.go`, `driveReadiness` in the ServiceManager) and Java (`RequiredChannels`, `ReconnectBackoff`, `driveReadiness`) now gate readiness on required s2s channels and share one backoff; see `docs/API.md`, Readiness and reconnects. | | | | |
| ~~**Generated W2S REST/SSE server dispatch.**~~ **Closed 2026-10-14**: Go (`s2sm/rest_dispatch/rest_dispatch.go`: `Routes`, `Dispatch`, `DispatchStream`, `FormBodyToJSON`, `RedirectResponse`) and Java (`s2sm/restdispatch/RestDispatch.java`: `ROUTES`, `dispatch`, `dispatchStream`, `formBodyToJson`, `redirectResponse`) render from the same shared planner (`pkg/internal/common/restdispatch`) the Rust module does; the `w2s-validate` cross-test drives all three against a real gRPC backend. | | | | |
| ~~**Typed record readers.**~~ **Closed 2026-10-14**: Go (`s2sm/record_readers.go`, `sm.<Record>RecordReader()`) and Java (`<Record>RecordReader.java`, `sm.<record>RecordReader()`) generate the same readers from one shared planner (`pkg/internal/common/recordread`), each dialing the plane half its record needs; the record-egress gate asserts both. | | | | |
| ~~**Generated handler for the client-facing record read plane**~~ **Closed 2026-10-14**: Go (`s2sm/record_c2s.go`) and Java (`<Svc>RecordsGeneratedHandler.java`) generate the `<Svc>Records` handler from the shared plan, bind it in the ServiceManager, and drop its getter from the author's Handler, as Rust does. | | | | |
| ~~**MCP follow-ups after the shared termination.**~~ **Closed 2026-10-14**: Go and Java mount MCP-only services (a `HasMcp` service with no `service_*` annotation gets a wrapper, a Handler entry and a ServiceManager mount), expose a session-free `Call` / `call` on the generated surface, and were already registering the carrier's own auth policy, which is now pinned by tests in both. | | | | |
| ~~**C2D server hosting.**~~ **Closed 2026-10-14**: Go (`chassis/grpcserver/c2d.go`: `C2DServer`, mounted by `newC2DServer` in the ServiceManager) and Java (`chassis/grpcserver/C2DServer.java`, mounted by `ServiceManager.newC2DServer`) host the package's own `service_c2d` services on the declared `socket_path` (owner-only, fail-closed), off the TLS listener and with no token auth, as Rust's `C2dServer` does; the wrapper set and the socket-path rule are shared (`service.IsC2DServer`, `service.HostedC2DSocketPath`). The `c2d-server` cross-test hosts both over a real socket through the generated c2dm client; see `docs/API.md`, C2D server auth-skip scope, and `docs/KNOWN_TEST_GAPS.md` C2D-1 for what it does not run. | | | | |
| ~~**C2D standalone host (cluster-less daemon).**~~ **Closed 2026-10-15**: all three emit a standalone entry for a c2d-only, cluster-free build, decided by one shared rule (`service.HostsC2DStandalone`): Go `sm.ServeC2DStandalone(ctx, socketPath, h)`, Java `ServiceManager.serveC2DStandalone(socketPath, handler, stop)`, Rust `ServiceManager::serve_c2d_standalone(socket_path, handler, shutdown)`, each through the same c2d mount `Run` uses (`newC2DServer` / `new_c2d_server`, now taking the path, with the declared one exported as `C2DSocketPath` / `C2D_SOCKET_PATH`). Rust's `C2dServer` gained `serve_with_shutdown` for it. The `c2d-server` cross-test starts all three through the entry from outside the generated packages; see `docs/API.md`, C2D standalone host. | | | | |

## Go or Java has it, the others do not

| Capability | Who has it | Who lacks it | Note |
|------------|-----------|--------------|------|
| `jwkview` chassis: in-memory signing-key view | Go, as a separate package | Java and Rust keep the same key map inside their Authorizer (`with_seeded_jwks` in Rust) | **Not a gap.** Packaging differs, the capability does not. |
| Formal verification harnesses (`assert_invariant`, `tacplus verify`) | Rust (kani), Java (JBMC) | Go | By design: Go has no model-checker backend, and an earlier attempt was not successful (Jody, 2026-10-14). Stays open by decision. |
| `(spec.record).min_group_size` enforcement in the service wrapper | All three (Go `mingroupsize_gen.go`, Rust `mingroupsize_gen.go`, Java) | nobody | **Not a gap.** The survey repeated a stale doc sentence; the Q3 port of 2026-09-22 gave Go and Rust the guard. Docs corrected 2026-10-14. |
| Per-type REST CRUD clients | All three | nobody | **Not a gap.** Go's `restclient_gen.go` emits the same per-type clients; only the file name differed. |

## Closed since August (parity reached, for the record)

- Record-plane egress filters (`store_principal`, `store_namespace`,
  `(spec.record).private`) on unary, PAGE and WATCH seams: Rust first, then
  Java, then Go (`recordegress_gen.go`); the `proto-recordegress` fixture leg
  compiles all three side by side.
- `(spec.store_epoch)` request-side stamp and `owner_default` guard: ported to
  Go and Rust (Q3, Q4, G1).
- `(spec.rpc).requires_device`: Go, then Java, then Rust, with one shared
  cross-language SHA vector.
- Timeseries projection codegen (Open/Refine/Sample/Pull): ported to Java (Q7).
- MCP termination (carrier, tool catalogue, surface, session transport, generated
  gate): all three.

## Test coverage asymmetries

- `w2s-validate` (W2S server dispatch) runs for Rust, Go and Java.
- `c2d-server` (C2D server hosting) runs for Go, Java and Rust, through the
  standalone host entry; `Run`'s own c2d line is still not executed
  (`docs/KNOWN_TEST_GAPS.md` C2D-1).
- `server-runtime` hosts Java and Rust servers driven by the Go client; the Go
  server runtime is covered by `runtime-smoke` (Python and TypeScript clients).
- `docs/KNOWN_TEST_GAPS.md` lists the gates that are green without exercising
  the behaviour, per language.

## Suggested order

1. **S2S readiness gating and backoff to Go and Java.** Runtime behaviour a
   deployed fleet notices; the Rust change is small (nine files) and the
   design is settled.
2. **Typed record readers to Go and Java.** The Rust reader exists to stop every
   consumer re-deriving a leak-prone three-call lifecycle; Go and Java consumers
   are re-deriving it today.
3. **Generated record-plane handler to Go and Java.** Same argument, on the
   host side.
4. **MCP follow-ups.** Decide first whether Go and Java services will host
   MCP-only tools at all; if yes, port the three Rust commits.
5. **W2S REST server dispatch.** Decided 2026-10-14 (Jody): this is a real
   parity gap, not by design. Go and Java ports landed 2026-10-14 on a shared
   planner. Go formal verification is the only open item.

## Re-survey of 2026-10-15 (from the sources, not the survey above)

Method: every `spec.proto` extension (143) cross-referenced against
`pkg/internal/{rust,go,java}` and `pkg/templates/{rust,go,java}`, then each
asymmetric hit read; the `pkg/internal/proto` pipeline calls each generator
makes diffed per language; chassis directories listed per language; the public
`ServiceManager` methods diffed across `service_manager.go.tmpl`,
`ServiceManager.java.tmpl` and `service_manager.rs.tmpl`; `docs/KNOWN_TEST_GAPS.md`
read for per-language asymmetries. Excluded by brief: C2D server hosting, Go
formal verification, the W2S REST dispatch plane.

| Capability | Rust | Go | Java | Status | Evidence |
|------------|------|----|------|--------|----------|
| ~~`(spec.owner_default)` guard (gRPC wrapper + MCP dispatch)~~ **Closed 2026-10-15** | yes | yes | yes (was: none) | REAL, closed | `java/types/ownerdefault_gen.go`, `parser.PlanOwnerDefault`, `cross-test/proto-ownerdefault`, `testdata/java/OwnerDefaultTest.java` |
| ~~`per_service_config` auto-subscription (`proto.InjectTenantConfigSubscriptions`)~~ **Closed 2026-10-15** | yes (was: no) | yes | yes (was: no) | REAL, closed | the three generators now run one sequence, `proto.ResolveServiceStates`; `TestResolveServiceStatesSubscribesNamedServiceToItsConfigSlice`. ~~Open, all languages: `GenerateTenantConfig` writes only the per-language proto dir and the flat dir keeps the `TenantConfig` stub (`CopyAndPrepareProtoDir`), so a slice reached through the *generated* `tenant_config.TenantConfig` is not seen by the subscription step; only hand-annotated `per_service_config` messages are~~ **Closed 2026-10-15**: `CopyAndPrepareProtoDir`, which builds the flattened tree for every language, now replaces both stubs with the real `TenantConfig` / `ProjectConfig` (Rust's own now-redundant calls removed; its proto dir is built by the same function). `TestResolveServiceStatesSubscribesToGeneratedTenantConfigSlice` (the only per-service config message is the generated one); new `cross-test/proto-tenantslice` leg asserts the generated `TenancyConfigState` state client for the subscribed service in Go, Java and Rust and compiles each. Red with the fix removed: the unit test (no subscription) and, on the fixture, no state client in any of the three |
| ~~Projection extraction helpers (`Extract<P>From<S>`, `<S>ToProjections`) from `projection_source`~~ **Closed 2026-10-15** | yes | yes | yes (was: no) | REAL, closed | shared planner `common/extraction` (Go and Rust moved onto it, output byte-identical on `cross-test/proto` and on a oneof variant of it); Java `java/types/extraction_gen.go` emits `<Source>Projections`; `TestJavaExtraction*`, `common/extraction/plan_test.go`; `current-generation` asserts the helpers in all three and runs `testdata/java/ExtractionTest.java` |
| `Handle<Source>` event handler + `sm.SetTimeseriesInsertFunc` | no | yes | no | REAL (Go-only), OPEN 2026-10-15: needs a decision before porting | `go/types/handler_event_gen.go`, `service_manager.go.tmpl`. The grouping it needs is now shared (`common/extraction`), but the port adds a public ServiceManager surface whose shape is not settled: Go's callback is synchronous with a `[]interface{}` row; Rust's natural shape is an async callback over the typed `<T>Row` (or a `serde_json` row in column order), Java's a `List<Object>` from `Timeseries<T>.toRow`. The only fixture that runs the dispatch (a subscription to a projected record) is `consumer-contract`, outside the gates this sweep could run |
| ~~Quorum write gate (`quorum_write` / `min_durable_copies`, `QuorumGate`, `LocalRows`)~~ **Closed 2026-10-15** | yes (was: no) | yes (was: no) | yes | REAL, closed | floor shared as `parser.StoreQuorum`; Go `chassis/diskstore/quorum.go.tmpl`, Rust `chassis/diskstore/src/quorum.rs.tmpl` + `Handler::<store>_local_rows`; new `cross-test/proto-quorumwrite` leg asserts all three and runs `testdata/go/quorum_write_test.go`; Go's roll supplier called `List(nil)` against a `List(ctx, caller)` handler and never compiled for a keyed store, fixed here |
| ~~Rust hosts the synthesized `<Msg>StoreService` of a `(spec.service_store)`~~ **Closed 2026-10-15** | yes (was: no) | yes | yes | REAL, closed | the shared own-package filter (`service.filterByPackage`, Rust's only caller of it as a full-service target) dropped every synthesized service but the record read plane, so Rust got no wrapper registration, no `<store>_store_grpc_impl` Handler getter and no roll scheduler, while Go and Java (own resolvers) host it. It now keeps `SynthesizedKindStore` (`TestResolveServerServices_HostsOwnPackageStoreService`). The Rust roll scheduler template had never been rendered for a real store and did not compile (`diskstore_stores` imported twice), fixed; the per-store scheduler is now `s2sm::run_<store>_roll_scheduler`, which `start_disk_store_roll_schedulers` spawns. `current-generation` `proto-quorumwrite` asserts the getter and the spawn and runs `testdata/rust/store_roll_test.rs`: rows put and listed through the generated `CredPairStoreService` wrapper, the unwired wrapper fails closed, and the generated scheduler rolls a baseline of exactly this node's rows (`cred_pair_local_rows`). Red with the filter reverted (no getter, no scheduler: compile errors, and the unit test) and with the `local_records` narrowing removed (baseline `[a, b, c]`) |
| ~~`json_key` / `json_aliases` in JSON serialization~~ **Closed 2026-10-15** | yes (was: no) | yes | yes | REAL, closed | Rust: `json_key` becomes the field's protobuf `json_name` in the tree Rust compiles (`rust/types/jsonname.go`), so pbjson writes and reads it at any depth; aliases are renamed to the field's key in `normalize_<msg>_enums` (`rust/types/serialization.go`). `common2.JsonKeyProbe` added to `cross-test/proto`; `testdata/rust/json_key_test.rs` (run by `current-generation` with `cargo test --test json_key_test`) passed on that generated fixture; Go render tests `TestInjectJsonNameForJsonKey`, `TestRustSerializationHonoursJsonKeyAndAliases`. Rust still writes other keys in lowerCamelCase and int64 as strings, where Go writes the proto name and numbers: a pre-existing difference, not this row |
| ~~EventBus support-proto wire-contract check at generation~~ **Closed 2026-10-15** | yes | yes | yes (was: no) | REAL, closed | Go, Java and Rust now run one sequence, `proto.PrepareEventBusSupportProto` (synthesize, then `ValidateEventBusSupportWireContract`); `TestPrepareEventBusSupportProtoChecksWhatItSynthesizes` |
| ~~`TypeID` / `<Svc>RpcId` registry enums in emitted protos~~ **Closed 2026-10-15** | yes (was: no) | yes | yes | REAL, low impact, closed | Rust calls `InjectRegistryEnums` at the point Go and Java do; that exposed the Rust enum-helper namer spelling a trailing acronym unlike prost (`TypeID` vs `TypeId`), fixed in `toProstEnumName` (`TestToProstEnumNameMatchesProst`); `cargo check` of the generated `cross-test/proto` Rust tree is clean |
| ~~SM accessors: instance id, TLS cert chain / private key~~ **Closed 2026-10-15** | yes | yes (was: no) | yes | REAL, low impact, closed | Go `sm.InstanceID()` (the server-assigned id, as Java's `identity.getInstanceId()` and Rust's `identity.instance_id()`), `sm.TLSCertChain()`, `sm.TLSPrivateKey()` on every ServiceManager, nil without auto-TLS; `TestGoServiceManagerExposesInstanceIDAndTLSMaterial`; compiled by the `current-generation` service fixtures with and without auto-TLS |
| SM `trace_round` / `jwk_cache` / `token_provider_for_service` | yes | no | no | NOT A GAP (Go/Java build the round internally, expose `TokenForService` / `callCredentialsForAudience`) | `service_manager.*.tmpl` |
| Kernel security policies / codegen-config.json (`ComputeSecurityPolicies`, `WriteCodegenConfig`, `disable_auth_message`, `boundary_callers`, `instance_keyed`) | no | no | yes | BY DESIGN: consumed by the Java state server (`StateServerMojo`) | `proto/libs_protos.go` WriteCodegenConfig doc |
| MCP carrier injection, persisted-store enum | via `CopyAndPrepareProtoDir` | direct | direct | NOT A GAP (packaging) | `proto/libs_protos.go:340` |
| `service_im` cloud/k8s minters, credential recovery | 8 files | 1 file | 5 files | NOT A GAP (packaging) | `chassis/service_im` |
| `jwkview` | in Authorizer | package | in Authorizer | NOT A GAP | survey above |
| gateway `proxy_dispatch::validate_request` | yes | no | no | excluded (W2S plane) | `rust/types/restdispatch_gen.go` |

KNOWN_TEST_GAPS entries that are per-language asymmetries: ~~PC-L-2b (Go only:
give-up DEGRADED reason unpinned; still open 2026-10-15, needs a harness hosting
`Run()`)~~ **closed 2026-10-15** (Run's drain is now `drainTLSFirstDelivery`,
driven by the cert-stream fixture), PC-L-9 (still open 2026-10-15: a Java and a Rust leak soak are each a
new harness on the scale of `cross-test/chaos/leaksoak`; leak soak hosts only the Go
ServiceManager; Java and Rust teardown are unmeasured), ~~NEW-21 (Go now emits
`service_manager_identity_error_test.go.tmpl`; Java's classifier is still
pinned by string only, Rust not mentioned)~~ **Java closed 2026-10-15**
(generated `ServiceManagerIdentityErrorTest`, run by `current-generation` in
every service fixture; **Rust closed 2026-10-15**: generated `identity_error_tests`
run by `cargo test --lib` on the model crate in every Rust service fixture),
PC-W-5 (Rust-only seam). PC-L-3b,
SEC-2 and NEW-46 are not language-specific.
