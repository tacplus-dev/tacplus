# Cross-Test Suite

The cross-test suite has these gates (see the CI workflow and
`cross-test/nightly-matrix.sh` for exactly where each runs):

LED-2 status: the three mechanical phases of cross-language runtime evidence are
all complete — servers (`server-runtime`, java/rust hosts driven by the go
client), the golden-byte wire corpus (`wire-corpus`, one encode decoded and
value-asserted by five languages), and clients (`server-runtime`'s client half,
java client -> rust server and rust client -> java server). What remains is
breadth, not a missing kind of evidence: the full N x N client/server matrix,
W2S against java/rust hosts, and `Watch`/client-streaming/bidi legs.

- `cmd/soundness` (`go run ./cmd/soundness -root .`): source-level parity —
  derive the canonical `assert_*` constraint set from `sys/spec.proto`, report
  constraint-handler parity across supported target surfaces, and fail if any
  required handler is missing. Runs in the `test-mr` CI job alongside
  `go test ./...`.
- `consumer-contract` (`cross-test/consumer-contract/run.sh`): generate the
  focused consumer-contract fixture, validate `tacplus.yaml` client-surface
  selection, compile generated outputs where supported, run the focused runtime
  checks, and compile handwritten downstream consumers. Runs per-language in the
  `consumer-*-contract-mr` CI jobs and in the nightly matrix.
- `current-generation` (`cross-test/current-generation/run.sh`): generate and
  compile the migrated broad service fixture in Go, Java, and Rust into ignored
  `target/` output. Runs in the nightly matrix.
- `smb` (`cross-test/smb/run.sh`) and `cert-stream`
  (`cross-test/cert-stream/run.sh`): service-mesh-boundary and certificate-stream
  runtime gates for Go, Java, and Rust. Run in the nightly matrix.
- `external-token` (`cross-test/external-token/run.sh`): behavioral gate for the
  generated external-audience token accessor (`token_for_external` /
  `TokenForExternal` / `tokenForExternal`) in Go, Java, and Rust. A mock token
  minter signs ES256 tokens; two calls must reach it twice and return distinct
  jtis, each requested with the caller's own kernel token, the given aud, and
  an exp no more than 600 s out. Local/manual (`just external-token`); not yet
  in CI.
- `w2s-validate` (`cross-test/w2s-validate/run.sh`): W2S request (ingress)
  validation gate for Rust — generates a service whose C2S backend has an
  `assert_*` request constraint, hosts the REAL generated
  `rest_dispatch::dispatch` / `dispatch_stream` against a real tonic backend,
  and asserts an invalid request is rejected `400` without reaching the backend
  while a valid one is forwarded. It also drives a `(spec.rest_browser)` route
  through `form_body_to_json` / `dispatch` / `redirect_response`: the form body
  decodes into the request, and the answer is a 303 to the response's field.
  This is the only in-repo host of a generated
  Rust W2S SERVER surface. Local/manual (`just w2s-validate`); not yet in CI.
- `c2d-server` (`cross-test/c2d-server/run.sh`): behavioral gate for the
  generated Go, Java and Rust standalone C2D host. Generates a c2d-only,
  cluster-free daemon, compiles the whole generated model, starts it through
  the generated standalone entry (`ServeC2DStandalone` /
  `serveC2DStandalone` / `serve_c2d_standalone`) from code outside the
  generated packages on the declared socket, and calls it over the socket
  through the generated c2dm client: a tokenless valid call succeeds, invalid
  requests and constraint-breaking responses are rejected server-side, server
  streaming works, the socket is 0600, a stale file at the path is replaced,
  and on stop the socket is removed and the handler's shutdown runs once.
  Does not run `Run()` itself (`docs/KNOWN_TEST_GAPS.md` C2D-1). Local
  (`just c2d-server`, part of `just gates`); not yet in CI.
- `zero-rpc-s2s` (`cross-test/zero-rpc-s2s/run.sh`): behavioral gate for a
  zero-rpc (client-only) Rust host (#94). Generates `cross-test/proto-zerorpc`
  and runs its real generated `ServiceManager` (`new()` + `run()`) against a
  fake kernel (token minter, `ServiceHealthState`) and a fake `upstreamllm` peer
  over TLS with SPIFFE certs. One s2s call through the generated client must
  reach the peer, and the host must publish no instance health, ask for no
  serving cert and bind no listener. Rust only. Local (`just zero-rpc-s2s`, part
  of `just gates`); not yet in CI.
- `jwk-alg` (`cross-test/jwk-alg/run.sh`): JWK alg-selection gate (#179) for
  the generated jwk and token chassis in Go, Java, Python, Rust and
  TypeScript. Every leg verifies the same pre-signed tokens from
  `cross-test/fixtures/jwk_alg_vectors.json` (written by
  `go run ./cross-test/jwk-alg/genvectors`; no leg signs anything) and must
  reach each case's expect: a key with no `alg` (Microsoft-shaped JWKS) accepts
  RS256/RS384/RS512 tokens it signed, a declared alg is used as declared, and
  `none` in any case, HMAC keyed with the public key, PS*, another family's alg
  and malformed or non-string header algs are refused. Python is client-only and
  does not emit these chassis, so its leg renders them first
  (`pkg/internal/python/pyrender`). Local (`just jwk-alg`, part of
  `just gates`); not yet in CI.
- `mcp-floor-version` (`cross-test/mcp-floor-version/run.sh`): rmcp floor gate
  for the generated Rust MCP dispatch — generates a service owning one
  `RPC_PRINCIPAL_MCP` tool, pins `rmcp` to the EXACT version
  `rmcpCrateVersion` declares (`cargo update --precise`, not `^3.x`
  resolution), and `cargo build`s the generated crate. Catches a declared floor
  lower than the API the emitted `ServerHandler::get_info` uses, which shipped
  once (`63f07a27`). Local/manual (`just mcp-floor-version`); not yet in CI.
- `runtime-smoke` (`cross-test/runtime-smoke/run.sh`): cross-language runtime
  smoke — a go fixture server built from the generated go servers hosts the
  contract on localhost (C2S gRPC + hand-written W2S HTTP/SSE/WebSocket
  endpoints) and the python/typescript generated CLIENTS exercise it over the
  wire. Runs per-consumer in the `runtime-smoke-mr` CI job and in the nightly
  matrix.
- `server-runtime` (`cross-test/server-runtime/run.sh`): cross-language runtime
  gate for java and rust, in BOTH directions. Server half: a real generated JAVA
  `ContractApi` server wrapper and a real generated RUST `ContractApi`
  `ValidationWrapper` are each hosted on a TCP port and driven by the generated
  GO C2S client over the wire. Client half: the real generated JAVA C2S client
  drives the RUST-hosted server, and the real generated RUST C2S client drives
  the JAVA-hosted server. Every leg asserts the same four things (unary
  `auth_disabled`, `auth_any_user` SNAPSHOT stream, `auth_any_user` PAGE with
  keyset continuation across two pages, and the fail-closed rejection of an
  `auth_entitled_users` method before the handler). This is the only in-repo
  evidence that java and rust generated code interoperates with another
  language's generated code over the wire at all; before it, both languages
  appeared only as self-generated client+server pairs inside one process.
  One cross-language pairing per driving client, NOT the full N x N matrix —
  python/typescript/swift/csharp as driving clients against java/rust hosts,
  java/rust against a go host, W2S against java/rust hosts, and
  `Watch`/client-streaming/bidi legs are still NOT covered. Known and
  documented cross-language difference: the fail-closed leg returns
  `UNAUTHENTICATED` from the java host and `PERMISSION_DENIED` from the rust
  host, because java emits a concrete default `Authorizer` while rust emits only
  the trait. Local/manual (`just -f cross-test/justfile server-runtime`, or
  `server-runtime-clients` for the client half alone); not in CI, not in
  `just test`, not in the nightly matrix.
- `keepalive-rust` (`cross-test/keepalive-rust/run.sh`): keepalive regression
  for the generated RUST server (issue #29). The Rust server applies no HTTP/2
  permit policy, unlike Go and Java. A generated Rust C2S client pings every
  `keepaliveconst.TimeSecs` while idle, dialled through a counting proxy; the
  gate asserts ONE connection survives a 600 s idle hold (no GOAWAY, no redial)
  and then serves a second call. A control run (`TACPLUS_KEEPALIVE_CUT_AFTER_SECS`)
  severs connections and must fail. Local/manual (`just -f cross-test/justfile
  keepalive-rust`); about 11 minutes, so not in `just test` or the nightly matrix.
- `outbound-deadline` (`cross-test/outbound-deadline/run.sh`): outbound deadline
  regression for the generated RUST server (issue #38). A three-hop chain of
  generated servers and clients (root 5 s -> mid -> leaf) on loopback. The leaf
  must receive a `grpc-timeout` no larger than the root's budget, and its
  handler must stop by the root's deadline. Local/manual (`just -f
  cross-test/justfile outbound-deadline-rust`); cargo-bound, so not in `just test`.
- `wire-corpus` (`cross-test/wire-corpus/run.sh`): cross-language
  SERIALIZATION-EQUIVALENCE gate. One `CorpusRecord` message is encoded ONCE by
  go, deterministically, into checked-in golden bytes
  (`cross-test/fixtures/wire_corpus.json`); go, java, python, rust and
  typescript then each decode that same blob with their generated type and
  assert every decoded FIELD VALUE equals the expected constants in that same
  fixture. It is the only in-repo evidence that a message encoded in one
  language decodes to identical values in all five — every other gate is source
  parity, compile, or RPC behavior. Local/manual
  (`just -f cross-test/justfile wire-corpus`); not in CI, not in `just test`,
  not in the nightly matrix.
- `consumer-smoke` (`cross-test/consumer-smoke/run.sh`): a local/manual smoke
  target defined in `cross-test/justfile`. It is NOT invoked by CI or the nightly
  matrix — the runtime checks that run in CI live in `consumer-contract` above.
- `chaos` (`cross-test/chaos/`): model stress / resilience harness (Profile A).
  Exercises generated model runtime under seeded fault schedules with stubbed
  rivets. Its Go unit tests DO run in CI — the packages live in this module, so
  the `test-mr` job's `go test ./...` covers `fault`, `node`, `oracle`,
  `orchestrator` and `profile`. The DRIVER
  (`go run ./cross-test/chaos/cmd/chaos`), which is what actually runs a fault
  schedule end to end, is local/manual and is not invoked by CI or the nightly
  matrix.

Generated outputs are disposable build artifacts. They must not be committed
under `cross-test/`.

## Soundness Matrix

```bash
go run ./cmd/soundness -root .
```

This source-level check reports constraint coverage across full-service targets
and supported client-only surfaces. It proves that a required constraint handler
is referenced in each relevant emitter; it does not replace generated runtime
tests for the handler semantics.

## Current Generation

```bash
just -f cross-test/justfile current-generation
just -f cross-test/justfile current-generation-go
just -f cross-test/justfile current-generation-java
just -f cross-test/justfile current-generation-rust
```

This gate uses `cross-test/proto`, which covers the larger 80-type fixture and
the Inventory and Analytics service shapes. It asserts that generated output
contains the current `sys/spec.proto` model and no legacy checked-in
`special` fixture outputs. It then compiles the generated output with Go
`go test`, Java Maven, and Rust `cargo check`, with warning/error output
treated as a failure. Python and TypeScript are full-client targets; their
positive client-surface (C2D/C2S/W2S/REST) and service-side rejection coverage
lives in `consumer-build`.

The generated Go diskstore library tests include redo-log crash recovery for a
leftover `.building` snapshot plus `.temp` tail. The Rust diskstore template
contains the same test and should be exercised with `cargo test -p diskstore`
when runtime-testing generated Rust library crates.

## Consumer Contract

```bash
just consumer-build
just -f cross-test/justfile consumer-build
```

The consumer contract fixture lives under `cross-test/consumer-contract/proto`.
It covers:

- one S2S service with unary, server-streaming, client-streaming, and
  bidirectional RPCs
- one C2S service with entitled-user auth, auth-disabled, auth-any-user, and
  target-namespace method options
- collection-declared C2S server streams for a bounded SNAPSHOT read
  (`ListEvents`) and explicit WATCH read (`Watch`) using
  `ContractWatchRequest` and `ContractWatchEvent`
- one C2D service
- one file-level C2S and W2S client dependency set

The gate exercises full-service surface-selection cases for Go, Java, and Rust,
full-client surface cases for Python and TypeScript, and C2D-only-plus-rejection
cases for Swift-lite:

- C2S enabled, W2S disabled, C2D disabled
- W2S enabled, C2S disabled, C2D disabled
- C2S and W2S enabled together
- C2D enabled
- all client surfaces disabled

For full-service outputs, the gate asserts expected `c2sm`, `w2sm`, and
`c2dm` entry points are present or absent. Full-client targets (Python,
TypeScript) assert their client package shape and rejection of service-side
surfaces; lite targets (Swift, C#) assert their C2D package shape and rejection
of C2S/W2S/S2S and service-side surfaces. The gate also rejects stale generated
symbols such as
`GrpcS2SClients.java`, `S2SClients`, or `crypto.JsonWebKey`.

For S2SM output, the gate also asserts generated mixed-auth wiring for the
contract fixture: method-auth registration, target-namespace metadata for
`Read`, mandatory authorizer/authenticator callback wiring, and fail-fast
startup checks where the language exposes them. This catches parity gaps where a
service compiles but would skip per-method auth metadata at runtime.

Generated outputs are compiled with normal local package tooling where that
target has positive generated output: Go via `go test`, Java via Maven, Rust via
`cargo check`, Python via `compileall`, TypeScript via `npm run build`, and
Swift-lite via `swift build`.

The gate also builds handwritten downstream consumers outside the generated
tree. Full-service consumers touch public type helpers, validation helpers,
C2S managers, W2S managers, C2D managers, and server wrapper surfaces. Client-only
consumers touch only their supported client packages and rejection behavior.

## Runtime Smoke

```bash
just -f cross-test/justfile runtime-smoke
just -f cross-test/justfile runtime-smoke-python
just -f cross-test/justfile runtime-smoke-typescript
```

The cross-language runtime gate. A go fixture server
(`cross-test/runtime-smoke/fixture/main.go`, built against the go generation of
the canonical consumer-contract fixture) hosts on localhost:

- the generated `ContractApi` gRPC wrapper with authorize/authenticate funcs
  wired (bearer-token check, namespace authorization),
- a second, deliberately UNWIRED `ContractApi` wrapper (no auth funcs) so
  clients prove the mandatory-auth fail-closed rejection over the wire,
- the generated `ContractAuditApi` wrapper with a recording accounting
  `Emitter` and trace publisher,
- hand-written W2S HTTP/JSON, SSE, and WebSocket endpoints (tacplus emits no
  W2S server runtime; the endpoints implement the wire protocol asserted by the
  consumer-contract go w2sm client tests), and
- `GET /fixture/stats`, exposing handler call counts, wire-protocol violations,
  and the recorded accounting/trace events.

The python and typescript consumers (`cross-test/runtime-smoke/consumers/`)
generate full-client models from the gate's mirror proto and drive the fixture
over the wire: the C2S battery (token-provider gating, PERMISSION_DENIED /
INVALID_ARGUMENT / INTERNAL codes, watch snapshot+done framing, ListEvents,
the PAGE auto-pager), the W2S battery (JSON unary, SSE streams, WebSocket
upload and chat framing with bearer-auth frames), the unwired fail-closed
check, and the audit accounting/trace battery (every gRPC method kind, success
and failure each, asserted through `/fixture/stats`).

This recovers the runtime-smoke intent of the pre-PAR-3 python/typescript
`both` consumer bodies (see consumer-contract git history), which hosted
python/TS s2sm servers in-process and died with the full-client taxonomy. The
old consumers' WatchHub client-lib contract (resume/relist/idle-timeout) is
intentionally NOT ported: full-client builds emit no server-side collection
runtime; that contract is covered by the consumer-contract go tests. Each
consumer language runs against a fresh fixture instance so stats are isolated.

## Consumer Smoke

```bash
just consumer-smoke
just -f cross-test/justfile consumer-smoke
just consumer-smoke-go
just consumer-smoke-java
just consumer-smoke-python
just consumer-smoke-rust
just consumer-smoke-typescript
```

The active service-runtime smoke paths are Go, Java, and Rust. Python and
TypeScript smoke coverage is limited to their supported client surfaces (they
emit no service runtime).

Go generates the consumer-contract `both` and `c2d` cases and runs these local
runtime checks:

- C2S auth: start an in-process gRPC server with the generated server wrapper,
  prove protected `Read` fails before send without a token provider or with a
  failing token provider, succeeds with auth, and denies a namespace-mismatched
  request before reaching the handler.
- C2S: start an in-process gRPC server with the generated server wrapper and
  call the auth-disabled `PublicRead` method through the generated C2S service
  manager, including with a failing token provider; also prove auth-any-user
  `Watch` requires authentication, reaches the handler with auth, and rejects
  invalid requests before the handler.
- W2S: start a local HTTP/JSON handler, call the same auth-disabled method
  through the generated W2S service manager, and prove the token provider is
  not called for that method; also prove protected W2S `Read` fails closed
  without a token provider before the fake server sees a request.
- W2S server streaming: start a local SSE handler, call `Watch`, verify the
  bearer token/header shape, read one generated `ContractWatchEvent` envelope,
  and observe stream EOF.
- W2S client streaming: start a local WebSocket handler, call `Upload`, verify
  `start`/`message`/`complete` frames and bearer auth, then receive a generated
  `ContractResponse`.
- W2S bidirectional streaming: start a local WebSocket handler, call `Chat`,
  verify `start` and `message` frames with bearer auth, then receive a
  generated `ContractEvent`.
- C2D: start a local Unix-socket gRPC daemon, dial it with the generated C2D
  service manager, call `LocalRead`, and read one `LocalWatch` event.
- S2S audit wrapper: start a generated `ContractAuditApi` wrapper, exercise
  unary, server-streaming, client-streaming, and bidirectional methods through
  generated gRPC clients on success and failure, then assert typed accounting
  `rpc_completed`/`rpc_error` events for every method and typed unary trace
  request/response events on success and handler error.

Python generates the consumer-contract `both` case and runs local runtime
checks: start a local gRPC server with the generated server wrapper, prove
protected `Read` fails before send without a token provider or with a failing
token provider, succeeds with auth, and denies a namespace-mismatched request
before the handler; prove auth-any-user `Watch` requires authentication and
validates requests; call the auth-disabled `PublicRead` method through the
generated C2S service manager, including with a failing token provider; call
generated W2S HTTP/JSON `PublicRead`; read one W2S SSE `Watch` event; send W2S
WebSocket `Upload` frames; and exercise bidirectional WebSocket `Chat`, including
send-from-on-connect behavior; and prove protected W2S `Read` fails closed
without a token provider before the local HTTP handler sees a request. It also
proves an unwired mandatory-auth wrapper fails closed before the protected
handler is called. It starts a generated `ContractAuditApi` wrapper and asserts
success/error typed accounting across unary, server-streaming, client-streaming,
and bidirectional methods plus typed unary trace request/response events on
success and handler error.

Java generates the consumer-contract `both` case, compiles the handwritten
consumer, and runs `consumer.Consumer` on the generated runtime classpath. This
catches protobuf/gRPC runtime dependency drift that compile-only checks miss,
and now starts an in-process generated C2S server to prove protected `Read`
missing-token/failing-token/success/namespace-denial behavior and auth-any-user
`Watch` fail-closed/authenticated behavior. It also proves mandatory-auth
wrappers reject a null `Authorizer` at construction. It runs a local W2S bidirectional
WebSocket smoke that verifies outbound frame shape/auth, wrapped `message`
response decoding, `complete` on close, and auth-required fail-closed behavior
without a token provider.
It also starts a generated `ContractAuditApi` wrapper and asserts success/error
typed accounting across unary, server-streaming, client-streaming, and
bidirectional methods plus typed unary trace request/response events on success
and handler error.

Rust generates the consumer-contract `both` case, runs `cargo check`, and then
runs the handwritten consumer binary. This catches generated manager and wrapper
surfaces that compile but cannot be constructed inside a Tokio runtime, and it
now proves protected C2S `Read`
missing-token/failing-token/success/namespace-denial behavior and auth-any-user
`Watch` no-token/success/invalid-request behavior.
It also runs a local W2S bidirectional WebSocket smoke that sends from
`on_connect`, verifies bearer auth on outbound frames, decodes a wrapped
`message` response, sends `complete` with `finish()`, and proves auth-required
W2S calls fail without a token provider or with a failing token provider.
It also starts a generated `ContractAuditApi` wrapper and asserts success/error
typed accounting across unary, server-streaming, client-streaming, and
bidirectional methods plus typed unary trace request/response events on success
and handler error.

TypeScript generates the consumer-contract `both` case, imports the compiled
`dist` package in Node, proves protected C2S `Read`
missing-token/failing-token/success/namespace-denial behavior, proves
auth-any-user `Watch` no-token/success/invalid-request behavior, imports the generated
`sys/grpcserver` package surface, and runs a fake W2S transport through
generated HTTP/JSON `PublicRead`, SSE `Watch`, WebSocket `Upload`, and
bidirectional WebSocket `Chat` clients, including protected W2S `Read` failures
with no token provider or a failing token provider before JSON transport send.
It also proves an unwired mandatory-auth wrapper fails before the protected
handler is called. It starts a generated `ContractAuditApi` wrapper and asserts
success/error typed accounting across unary, server-streaming, client-streaming,
and bidirectional methods plus typed unary trace request/response events on
success and handler error.

Validation stays enabled on request and response paths where generated wrappers
provide validation.

The consumer-contract runtime smoke also exercises the collection-annotated
`ListEvents` method at the stable proto surface across the full-service
languages: missing-token fail-before-send, successful server-streaming delivery,
invalid request rejection, and invalid stream response rejection. This also
exercises the explicit `Watch` request/envelope shape and rejects invalid nested
watch payloads. The generated runtime smoke also covers WATCH resume, relist,
idle-timeout, teardown, and replay-depth behavior across the full runtime
languages.

Additional smoke paths should be added in this layer rather than reviving
committed generated output trees.

## Wire Corpus (serialization equivalence)

```bash
just -f cross-test/justfile wire-corpus
just -f cross-test/justfile wire-corpus-go
just -f cross-test/justfile wire-corpus-java
just -f cross-test/justfile wire-corpus-python
just -f cross-test/justfile wire-corpus-rust
just -f cross-test/justfile wire-corpus-typescript
just -f cross-test/justfile wire-corpus-emit
```

`cross-test/fixtures/wire_corpus.json` is the single source of truth: each
vector carries the golden `hex` AND the `expect` values it must decode to. The
go leg is the only encoder — `--emit` re-encodes each vector from its own
`expect` block with deterministic marshalling (sorted map keys) and rewrites
`hex`. Every language then generates the model from `cross-test/wire-corpus/proto`
and decodes THAT blob with its generated `CorpusRecord` type. Five decoders
against one corpus, not a 5x5 matrix. The go leg additionally re-encodes
`expect` and asserts the bytes are byte-identical to the checked-in `hex`, which
pins the corpus against a future change in go's encoding.

The message covers exactly the shapes this repo uses that differ between
runtimes: `int64` at the +/-2^63 boundary, `uint64` above 2^53 (and at 2^64-1,
which java can only render with `Long.toUnsignedString`), a
`map<string, string>` with several entries declared out of sorted order and one
empty value, a proto3 `optional int32` in BOTH the present-with-zero and absent
states, and a non-UTF-8 `bytes` field. `sint*`/`sfixed*`, non-string map keys,
`google.protobuf.Timestamp` precision and `fixed32`/`fixed64` are deliberately
out of scope: the repo has zero, zero, zero and three uses of them
respectively. See `cross-test/wire-corpus/README.md`.

A decoder that disagrees with the golden bytes is a finding. `--emit` exists
only for a deliberate change to the corpus, never to make a disagreeing decoder
pass.

## Chaos / Model Stress (Profile A)

```bash
go test ./cross-test/chaos/...
go run ./cross-test/chaos/cmd/chaos [-profile f.yaml] [-seed N]
```

The chaos harness exercises the tacplus-generated model runtime under failure
using stubbed rivets (no kernel, no state store). It drives a synthetic
multi-language node mesh through seeded fault schedules (SIGTERM, SIGKILL,
network partition, dependency-down, token-expiry) and verifies deterministic
invariants: WATCH gapless/ordered/no-dup, auth fail-closed, no-leak trend,
unary echo-transform. Running a fault schedule is **local/manual** — the `chaos`
binary is not invoked by CI or the nightly matrix. The harness's own unit tests
are not exempt, though: they are ordinary packages of this module, so CI runs
them in `test-mr` via `go test ./...`. See `cross-test/chaos/README.md` for
layout and Profile B plans.

## Retired Runtime Harness

The former broad serialization and wire-level runtime matrix was removed
because it imported committed generated outputs from a pre-parity layout. A new
wire-level runtime suite should generate into scratch output first and then
compile or run handwritten tests against those current package roots.
