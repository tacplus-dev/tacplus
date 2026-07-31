# Consumer Test Plan

This plan adds downstream consumer build tests for tacplus-generated public API
surfaces. It complements the existing integration suite in `cross-test/TESTS.md`:
that suite proves generated serialization, validation, fixtures, and wire-level
client/server behavior. This plan proves that real projects can import and build
against the generated packages without relying on generator internals.

## Goal

Every supported language must have an automated consumer build that imports the
generated output from outside the generated tree and exercises the public API
surface tacplus promises for that language.

The first required gate is build-only everywhere. Runtime smoke and wire tests
are layered on after the build contract is stable.

## Success Definition

The dedicated consumer test phase is successful when all of the following are
true:

- `just consumer-build` runs from a clean checkout and all language consumer
  projects build using their normal package tooling.
- Each full-language consumer builds handwritten code that uses generated types,
  validation helpers, C2SM, W2SM, C2D, and server-wrapper surfaces where the
  language supports them.
- Swift-lite builds and surface-checks its generated C2D package. Full Swift
  downstream consumer coverage is deferred until Swift expands beyond lite
  C2D support.
- The test matrix has no accidental gaps. Unsupported or future surfaces are
  declared explicitly in the matrix instead of silently skipped.
- `tacplus.yaml` client-surface selection is tested: enabled C2S/W2S/C2D
  surfaces are emitted, disabled surfaces are absent, and invalid combinations
  fail with actionable errors.
- Consumer code imports only intended public entry points. It must not import
  generated `internal` packages directly unless the language's generated public
  API explicitly exposes that package as part of the contract.
- Package metadata is validated by downstream tooling: `go mod`, Maven, npm,
  Python packaging/imports, Cargo, and SwiftPM all resolve the generated output.
- The consumer build phase is required before regenerating consumers.

## Target Matrix

`build` means a handwritten downstream project imports the generated output and
passes that language's normal compile/typecheck/import gate.

| Language | Types | Server wrapper | gRPC client | S2SM | C2SM | W2SM | C2D | Package tooling |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Go | build | build | build | build | build | build | build | go test |
| Java | build | build | build | build | build | build | build | Maven/javac |
| Python | build | build | build | build | build | build | build | pip/import |
| TypeScript | build | build | build | build | build | build | build | npm/tsc |
| Rust | build | build | build | build | build | build | build | Cargo |
| Swift-lite | limited | n/a | n/a | n/a | n/a | n/a | build | SwiftPM |

## Step-by-Step Plan

### 1. Add a Consumer Contract Proto Fixture

Create a small proto source set for consumer-contract testing. This fixture
should be separate from the broad `cross-test/proto` fixture so it can focus on
modern service annotations and generated API surfaces.

The fixture should include:

- A shared types proto with nested messages, enums, maps, repeated fields,
  validation annotations, constants, and one structured error field.
- One `service_s2s` service with unary, server-streaming, client-streaming, and
  bidirectional-streaming methods.
- One `service_c2s` service with default entitled-user auth plus method-level
  `auth_disabled`, `auth_any_user`, and `target_namespace_field` cases.
- One `service_c2d` service with at least one unary method and one streaming
  method if supported by the generated C2D clients.
- One consuming proto that declares `grpc_c2s_client` and `grpc_w2s_client`
  dependencies.
- A `dependencies.yaml` with all tool and package versions needed to generate
  every language target.

### 2. Add Surface-Selection Config Fixtures

Add generation configurations that prove `tacplus.yaml` selects transport
surfaces rather than proto annotations hard-coding a transport.

Required cases:

- C2S enabled, W2S disabled, C2D disabled.
- W2S enabled, C2S disabled, C2D disabled.
- C2S and W2S both enabled.
- C2D enabled for languages that support it.
- All client surfaces disabled.

Each case must assert both presence and absence of generated directories and
entry points.

### 3. Generate Language Outputs for the Consumer Fixture

Generate the consumer-contract fixture for:

- Go
- Java
- Python
- TypeScript
- Rust
- Swift-lite for C2D

The generated outputs should be treated as build inputs for consumer projects,
not as the consumer tests themselves.

### 4. Add Handwritten Consumer Projects

Add one downstream consumer project per language. The current implementation
creates those consumers in the temporary contract workdir so the generated
output stays disposable. Each project lives outside the generated output tree
and depends on generated packages the way a real repository would.

Each full-language consumer should include handwritten source files for:

- Types: construct messages, use enums/constants, call validation, and touch
  helper functions.
- Server wrappers: implement generated service handler interfaces for unary and
  streaming methods.
- gRPC clients: construct generated typed clients and compile calls for unary
  and streaming methods.
- S2SM: compile a handler that uses generated service-manager accessors such as
  gRPC clients, state clients when present, auth helpers, and run/lifecycle
  signatures. Full S2SM bootstrap runtime is not required for the first gate.
- C2SM: construct the lightweight client service manager from a channel or
  connection plus a token provider, then access typed clients.
- W2SM: construct the web-to-service manager from a base URL or fake transport
  plus token provider, then access typed clients.
- C2D: where supported, construct the local-daemon client facade and compile a
  representative call.

Swift-lite should build a SwiftPM consumer that imports the generated package
and compiles use of `C2DClient`.

### 5. Add Automated Build Targets

Add these `just` targets:

```bash
just consumer-build
just consumer-build-go
just consumer-build-java
just consumer-build-python
just consumer-build-typescript
just consumer-build-rust
just consumer-build-swift
```

The aggregate `consumer-build` target should fail if any required consumer
project fails to build or if an unsupported surface is skipped without being
declared in the matrix.

Recommended language gates:

- Go: `go test ./...`
- Java: `mvn test` or `mvn -q test`
- Python: install/import generated packages, compile Python sources, and run
  import-focused `pytest` tests
- TypeScript: `npm ci`, `npm run build`, and typecheck
- Rust: `cargo check`
- Swift-lite: `swift build`

### 6. Add Negative Surface-Gating Tests

Add tests that prove generated surfaces match configuration.

Required negative checks:

- `clients.c2s.enabled: false` means no public C2SM entry point is emitted.
- `clients.w2s.enabled: false` means no public W2SM entry point is emitted.
- `clients.c2d.enabled: false` means no public C2D entry point is emitted.
- Swift-lite rejects C2S and W2S requests.
- C2D generation rejects an empty C2D service set where the language requires
  one.
- W2S generation fails clearly when a selected service lacks usable REST route
  metadata.

### 7. Runtime Smoke Tests

`just consumer-smoke` is active for Go, Java, Python, Rust, and TypeScript.
The smoke suite runs local generated-output checks for protected and
auth-disabled C2S calls, W2S HTTP/SSE/WebSocket calls, request/response
validation, mandatory-auth fail-closed wrapper wiring, collection SNAPSHOT and
WATCH behavior, stream teardown, and S2S audit accounting/trace emission across
unary, server-streaming, client-streaming, and bidirectional RPC shapes. Go also
runs a Unix-socket C2D runtime check; Swift-lite remains C2D build-only.

The detailed per-language runtime inventory lives in `cross-test/TESTS.md`.

Smoke tests should be small and local:

- C2SM: in-process gRPC server or fake channel for one unary call and one
  streaming call.
- W2SM: fake HTTP/SSE/WebSocket transport where possible, proving route, token,
  request, response, and streaming method shapes.
- C2D: Unix-domain-socket smoke for Go on Unix-like CI; Swift-lite remains
  build-only.
- Server wrappers: instantiate wrappers with handwritten handlers and prove
  registration compiles or runs locally without full service bootstrap.

### 8. Keep Existing Wire Tests as the Cross-Language Integration Gate

Do not duplicate the broad `cross-test` runtime matrix in the consumer fixture
suite. The existing integration tests remain responsible for cross-language
serialization and gRPC/REST wire compatibility.

The consumer suite is responsible for public API shape, package metadata,
service-manager ergonomics, and transport-surface selection.

### 9. Promote the Gate Before Consumer Regeneration

Before regenerating consumers:

1. Run the existing generator and integration tests.
2. Run `just consumer-build`.
3. Run `just consumer-smoke` once available.
4. Confirm the matrix has no undeclared skips.
5. Regenerate consumers only after the generated API surface passes the
   consumer contract.

## Non-Goals

- Exhaustively re-testing every serialization fixture. That remains covered by
  `cross-test/TESTS.md`.
- Full S2SM bootstrap runtime in the first build gate.
- Full C2D runtime parity on every operating system before the transport
  support decision is made.
- Replacing consumer regeneration as the final real-world validation.
