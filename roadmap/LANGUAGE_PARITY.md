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
| **Generated W2S REST/SSE server dispatch.** `rest_dispatch.rs` with per-route `RouteInfo`, request and response validation on the unary and SSE legs, `form_body` and `redirect_field` handling. | `restdispatch_gen.go`, `rest_dispatch.rs.tmpl` | No generated REST server; the REST surface is the gateway's translation atop gRPC | Same as Go | `form_body` / `redirect_field` appear only in the Rust generator; the `w2s-validate` cross-test exists only for Rust |
| **Typed record readers.** For each `(spec.record_read)` a generated reader with `get` and `list` that drives the `Create<T>Query -> List<T> -> Delete<T>Query` lifecycle and closes the query handle on both paths; `include_deleted` threaded through. | `record_reader_gen.go`, `record_readers.rs` | Consumers get the raw plane clients only | Same as Go | `record_read` appears in the Go and Java generators only as comments on C2S injection |
| **Generated handler for the client-facing record read plane** (`<Svc>Records`): namespace resolution, tenant pin and row scope generated rather than left to the author. | `record_c2s_gen.go` | The C2S service is injected into the protos (shared `AppendRecordC2SServiceToProtos`), the handler body is not generated | Same as Go | No Go or Java counterpart to `record_c2s_gen.go` |
| **MCP follow-ups after the shared termination.** MCP-only services (tools with no gRPC twin) mounted onto the ServiceManager, the carrier's own auth policy registered, and a session-free dispatch entry point on the generated surface. | Commits of 2026-09-02 (gaps #3b, #3c, session-free dispatch) | Not found | Not found | The strings `MCP-only` and `session-free` occur only in the Rust generator. Go, Java and Rust all have the carrier, tool catalogue, surface and session transport. |
| **C2D server hosting.** A generated C2D server wrapper that skips token auth because the socket ACL is the boundary. | `C2dServer` in `service_manager.rs.tmpl` | C2D clients only, by design | C2D clients only, by design | `docs/API.md`, C2D server auth-skip scope |

## Go or Java has it, the others do not

| Capability | Who has it | Who lacks it | Note |
|------------|-----------|--------------|------|
| `jwkview` chassis: watch client for signing-key views | Go | Java, Rust | Listed as "go only" in `docs/USER.md` |
| Formal verification harnesses (`assert_invariant`, `tacplus verify`) | Rust (kani), Java (JBMC) | Go | By design so far; Go has no model-checker backend |
| `(spec.record).min_group_size` enforcement in the service wrapper | Java | Go, Rust refuse generation on a stamped method instead of serving it unguarded | Documented in `docs/USER.md`; the refusal is the honest alternative, not a silent gap |
| Per-type REST CRUD clients | Java, Rust (`restclient_crud_gen.go`) | Go emits per-service REST clients (`restclient_gen.go`) | Different shape, not necessarily missing capability; worth a one-hour check |

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

- `w2s-validate` (W2S server dispatch) exists only for Rust, because only Rust
  hosts one.
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
5. **W2S REST server dispatch.** Only if a Go or Java service needs to host
   REST directly instead of behind the gateway; otherwise document the
   difference as by design, next to C2D.
