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
- `c2d-server` (C2D server hosting) runs for Go and Java; Rust's `C2dServer`
  has no runtime gate (`docs/KNOWN_TEST_GAPS.md` C2D-1).
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
