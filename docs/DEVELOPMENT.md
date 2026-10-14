# tacplus Compiler Development Guide

Reference for developers working on the tacplus compiler itself. For using
tacplus to build services, see [USER.md](USER.md). For generated public
contracts, see [API.md](API.md). For the conceptual model, see
[THEORY.md](THEORY.md).

---

## Table of Contents

1. [Development Setup](#development-setup)
2. [Architecture and Data Flow](#architecture-and-data-flow)
3. [Key Directories](#key-directories)
4. [The Model](#the-model)
5. [Embedded Proto Model](#embedded-proto-model)
6. [Code Generation Architecture](#code-generation-architecture)
7. [Error Handling](#error-handling)
8. [Postel's Law](#postels-law)
9. [Language Parity](#language-parity)
10. [No Hardcoded Versions](#no-hardcoded-versions)
11. [Compiler Development Patterns](#compiler-development-patterns)
12. [Soundness And Parity Gates](#soundness-and-parity-gates)
13. [Typed Disk Persistence Invariants](#typed-disk-persistence-invariants)
14. [Checklists](#checklists)
15. [Regression Testing](#regression-testing)

---

## Development Setup

```bash
git clone <tacplus-repo>
cd tacplus
go mod download
just install       # Build and install to GOPATH/bin
go test ./...      # Run tests
```

### Key Dependencies

- `github.com/spf13/cobra` — CLI framework
- `golang.org/x/mod/modfile` — Go module manipulation
- `google.golang.org/protobuf` — Protocol Buffers support

### Building tacplus

tacplus does not manage its own runtime container. Build it in the environment you are using; CI may run inside a container, and local development may be host-native.

```bash
just install       # Generate embedded proto code and install tacplus
```

### Testing Changes

```bash
# After installing, test against a consumer project that has a tacplus.yaml:
cd /path/to/project
tacplus build            # regenerate from ./tacplus.yaml (flags: --tag, --src, --config/-c, --verbose/-v)
```

---

## Architecture and Data Flow

tacplus is a per-project tool. `tacplus build` regenerates code from the project's `tacplus.yaml`. Each invocation generates code for a single language declared in `tacplus.yaml` (`language:` — go, java, rust, python, typescript, swift, csharp). The CLI verbs are `build`, `validate`, `verify`, `registry`, `upgrade`, and `version`.

```
Proto source (local dir or git repo)
  ├── config.yaml          (package names, tool versions)
  ├── dependencies.yaml    (external dep versions)
  └── proto/*.proto        (proto definitions)
                │
                ▼
        ┌──────────────┐
        │  Proto Parse │  (pkg/internal/common/parser/)
        └──────┬───────┘
               │
               ▼
        ┌──────────────┐
        │    Model     │  (pkg/internal/common/model/)
        └──────┬───────┘
               │
               ▼
     Language generator (one per invocation)
     Go / TS / Python / Rust / Java
               │
               ▼
     {project}/tacplus/    (generated output)
```

### Generation Flow

```
tacplus build [--config tacplus.yaml] [--src proto-dir]
  ↓
1. Load config + resolve proto source
2. Language-specific Generate()
3. Copy/rewrite proto files
4. Overlay sys protos (embedded in tacplus binary)
5. Run protoc
6. generateRegistry()
   - Parse service_state annotations
   - CollectRegistryTypes() → scan all types recursively
   - Build TypeDescriptor for each (recursive, with schema JSON)
   - Execute template
   - Output registry.go / Registry.java / registry.py / etc.
7. Per-proto registries (Go: types/services/.../registry.go)
8. Inject TypeID/RpcID enums into processed proto files
9. Generate service wrappers + HTTP clients
```

### Service Detection

`DetectServicesForWrapper` (pkg/internal/common/service/service.go) walks all proto files and identifies services by annotation (`svc.GrpcName != ""`), which is set from `service_s2s`, `service_c2s`, or `service_c2d` annotations.

Service generation is annotation-driven. Proto annotations define the service category, auth mode, allowed callers, state visibility, and client dependencies. `tacplus.yaml` carries project-local generation facts such as language, package name, output directory, proto source, config composition, and which client transport surfaces to emit for proto-declared client dependencies.

Language targets are intentionally split. Full-service generators are `go`,
`rust`, and `java`; they own ServiceManager, server wrappers, EventBus, health,
SMB, stores, states, and C2S/W2S/S2S/C2D clients. Client-only generators split
into two tiers: `swift` and `csharp` are "lite" client targets (C2D, C2S, and
W2S clients; Swift's W2S is unary + server-streaming over SSE, C#'s is
unary-only), while `python` and `typescript` are full-client
targets that own every client surface (C2D/C2S/W2S/REST). Python and TypeScript must reject service-side annotations or
config requests (servers, S2S clients, ServiceManager runtime, EventBus, health,
SMB, stores, states) with clear client-only errors rather than emitting partial
service runtime code.

---

## Key Directories

| Directory | Purpose |
|-----------|---------|
| `cmd/` | CLI command definitions (cobra) |
| `cmd/build/` | Code regeneration (`tacplus build`) |
| `cmd/validate/` | Proto validation (`tacplus validate`) |
| `cmd/verify/` | Verify generated output is up to date (`tacplus verify`) |
| `cmd/registry/` | Service-registry management (`tacplus registry`) |
| `cmd/upgrade/` | Self-upgrade (`tacplus upgrade`) |
| `cmd/version/` | Version info (`tacplus version`) |
| `cmd/soundness/` | Dev-only cross-language soundness gate |
| `pkg/internal/common/model/` | Shared data model (`ProtoFile`, `Message`, `Field`, `Enum`) |
| `pkg/internal/common/parser/` | Proto descriptor parsing, annotation extraction |
| `pkg/internal/common/service/` | Service detection and resolution logic |
| `pkg/internal/go/types/` | Go code generator |
| `pkg/internal/typescript/types/` | TypeScript full-client generator (C2D/C2S/W2S/REST client surfaces) and legacy template helpers |
| `pkg/internal/python/types/` | Python full-client generator (C2D/C2S/W2S/REST client surfaces) and legacy template helpers |
| `pkg/internal/rust/types/` | Rust code generator |
| `pkg/internal/java/types/` | Java code generator |
| `pkg/internal/go/types/restclient_gen.go` | Go HTTP/REST client generator |
| `pkg/templates/<lang>/chassis/` | Per-language runtime library (machinery) templates — the `chassis` namespace, split out of the declarative `sys` namespace |
| `pkg/proto/sys/` | Canonical embedded library proto files (declarative `sys` types) |
| `pkg/internal/proto/spec/` | `go:generate` entrypoint for compiler-internal descriptor code |
| `target/proto/spec/sys/` | Ignored generated Go descriptor code produced before builds |
| `pkg/internal/tactype/` | Internal config type (`Config`, `GrpcEntry`, `StateEntry`) |
| `pkg/depfiles/` | Dependencies.yaml generation and management |
| `pkg/runner/` | Build orchestration |
| `pkg/tacplusyaml/` | tacplus.yaml config file handling |
| `pkg/projectfile/` | Project type detection |
| `pkg/protosrc/` | Proto source resolution (local path or git clone) |

---

## The Model

The shared model in `pkg/internal/common/model/model.go` represents parsed proto files:

```go
type ProtoFile struct {
    Messages []Message
    Enums    []Enum
    Services []Service
    // ...
}

type Message struct {
    Name        string
    Fields      []Field
    Annotations map[string]AnnotationValue  // Message-level annotations
}

type Field struct {
    Name        string
    Type        string
    Annotations map[string]AnnotationValue  // Field-level annotations
    // ...
}
```

---

## Embedded Proto Model

### Embedded Library Protos

Infrastructure proto files are embedded in the tacplus binary under `sys/` and
`common/`:

| File | Purpose |
|------|---------|
| `sys/spec.proto` | Custom annotations, validation enums, service annotations, and state annotations |
| `sys/errors.proto` | Structured error type used across all languages |
| `sys/tags.proto` | Kubernetes-style runtime tag sets and selectors |
| `common/*.proto` | Shared runtime request and support types |

**Characteristics:**
- Embedded via `//go:embed sys/*.proto` and `//go:embed common/*.proto`
- Overlaid into the prepared proto directory during generation
- Changes here affect all generated outputs, so handle them with care
- Generated descriptor code stays under ignored `target/proto/` output

### Authored Proto Packages

Application-level proto files:
- Import `sys/spec.proto` for annotations
- Import `sys/errors.proto` when they need structured errors
- Generated from proto definitions and tacplus's normalized semantic model
- All validation, serialization, and helper code is auto-generated

---

## Code Generation Architecture

### Per-Package Output Structure

Every authored or embedded proto package MUST use a consistent public shape
across all languages:

```
tacplus/types/{category}/{package}/
├── {generated protobuf files}
├── helpers / validation
└── transport stubs when the proto defines services
```

No full-service language is privileged as the model for the others. Full-service
generators must emit the shared package structure from the same proto-derived
model; client-only generators must keep the C2D client contract compatible and
reject service-side requests.

Full-service generators emit top-level service-manager surfaces such as
`s2sm/`, `c2sm/`, `c2dm/`, and `w2sm/` when the corresponding proto annotations
and `tacplus.yaml` client surface settings require them. Client-only generators
emit client-side surfaces only — full-client targets (Python, TypeScript) emit
C2D/C2S/W2S/REST clients; lite targets (Swift, C#) emit C2D/C2S/W2S clients — and
must not emit `s2sm`, `c2sm`, `w2sm`, server wrappers, EventBus
subscribers/publishers, health runtime, or SMB runtime.

### Workspace / Parent Build Files

| Language | File | Purpose |
|----------|------|---------|
| Java | `tacplus/pom.xml` | Parent POM with `<modules>` listing all types/ and clients/ |
| TypeScript | `tacplus/package.json` | npm workspaces listing all packages |
| Rust | `tacplus/Cargo.toml` | Cargo workspace with all crate members |
| Python | `tacplus/pyproject.toml` | Package configuration |
| Swift-lite | `tacplus/Package.swift` | SwiftPM package for local daemon clients |
| C#-lite | `tacplus/*.csproj` | .NET package for local daemon clients |

### Generator Files

All paths are under `pkg/internal/<lang>/types/`. Validation is emitted from
`validation.go` in every language (normalized); Go/Java also have `helpers.go` and
TS/Python `helpers_gen.go` for serialization/enum helpers. REST clients come from
`restclient_gen.go` in every language.

| Language | Types Generator | Validation | REST Client Generator |
|----------|----------------|------------|-----------------------|
| Go | `generator.go` | `validation.go` | `restclient_gen.go` |
| TypeScript | `generator.go` | `validation.go` | `restclient_gen.go`, `grpc_over_rest_gen.go` |
| Python | `generator.go` | `validation.go` | `restclient_gen.go` |
| Java | `generator.go` | `validation.go` | `restclient_gen.go` |
| Rust | `builder.go` | `validation.go` | `restclient_gen.go` |

### Use Annotations, Not Pattern Matching

**CRITICAL**: Never use string suffix matching, file name patterns, or any other heuristic to identify service types (gRPC vs state). Always use the proto annotation system:

- **RPC services**: Identified by `service_s2s`, `service_c2s`, or `service_c2d` annotation on the service block (`svc.GrpcName != ""`)
- **State services**: Identified by `service_state` annotation on messages in the same proto file
- **Service wrapper type** (GrpcServer vs StateClient): Determined by `svc.IsGrpc` field on `ServiceInfo`, set during detection from annotations

Pattern matching like `strings.HasSuffix(name, "StateService")` is fragile and wrong.

---

## Error Handling

**CRITICAL**: All generated types across ALL languages must ONLY raise/throw/return errors based on `sys/errors.proto`.

```protobuf
message Error {
  int32 code = 1;              // gRPC status code
  string message = 2;
  repeated Any details = 3;
}
```

Language wrappers:

| Language | Type | Base Class |
|----------|------|------------|
| Go | `error` interface | wraps `Error` protobuf |
| Java | `ProtoException` | extends `Exception` |
| TypeScript | `ProtoError` | extends `Error` |
| Rust | `ProtoError` | enum |
| Python | `ProtoException` | extends `Exception` |

Error helpers follow gRPC status codes:

```go
utils.NewErrMissingInvalidData(field, message)    // InvalidArgument (code 3)
utils.NewErrNotFound(message)                     // NotFound (code 5)
utils.NewErrExpired(message)                      // DeadlineExceeded (code 4)
utils.NewErrUnauthorized(message)                 // Unauthenticated (code 16)
utils.NewErrGeneral(message)                      // Internal (code 13)
```

**No language-specific exceptions should escape generated code** (no `IllegalArgumentException`, `ValueError`, etc.).

---

## Postel's Law

"Be liberal in what you accept, conservative in what you send." (RFC 1122)

All serialization helpers must accept both camelCase (protobuf JSON canonical) and snake_case field names. Output should use the language's native convention.

| Language | Accepts both? | Mechanism |
|----------|---------------|-----------|
| Go | Yes | `protojson.Unmarshal` fallback in helpers |
| Java | Yes | `JsonFormat.parser()` |
| Python | Yes | `json_format.Parse` |
| TypeScript | Yes | `@bufbuild/protobuf.fromJson` |
| Rust | Yes | `#[serde(alias)]` per field in generated `build.rs` |

---

## Language Parity

The full-service languages must be at feature parity. Client-only targets must
keep their supported client contracts compatible and must reject unsupported
service/runtime generation clearly:

- Enum helpers (parsing, string conversion, validation)
- Constants generation (file-level constants with proper types)
- Serialization (JSON, YAML, TOML, binary with auto-detection)
- Validation (all annotation-based validators)
- Error handling (ProtoException/ProtoError wrapping Error protobuf)
- State accessors (identical interface per boundary type in full-service targets)
- Auth interceptors (identical entitlement enforcement semantics in full-service targets)

Parity is defined against the proto annotations, semantic registry, and generated-contract tests, not against any single language implementation. A service-runtime feature is complete only when the generated behavior is equivalent in Go, Java, and Rust. A client-surface feature is complete only when each supported client-only target either implements the contract or rejects it explicitly by policy.

Generated public surfaces must preserve the contracts in [API.md](API.md). When
changing generator behavior, update the API contract first if the public seam is
intentionally changing; otherwise treat generated API drift as a bug.

---

## No Hardcoded Versions

**CRITICAL**: No hardcoded package versions or language versions anywhere in generator code. ALL versions must come from `dependencies.yaml` and `config.yaml`.

```go
// CORRECT: Look up from registry
registry, err := packages.GetPackageRegistry()
version, ok := registry.GetGoVersion("google.golang.org/protobuf")
if !ok {
    return fmt.Errorf("version not found in config.yaml")
}

// WRONG: Hardcoded
version := "v1.33.0"
```

### Verification

```bash
# Should return NO matches in pkg/internal/.
grep -rn '"v[0-9]\+\.[0-9]\+' pkg/internal/ --include='*.go'
grep -rn '"\^[0-9]\+\.[0-9]\+' pkg/internal/ --include='*.go'
```

---

## Compiler Development Patterns

### Anti-Patterns

- Do not parse generated code or proto text with ad hoc string matching when a
  descriptor, semantic model, or structured parser is available.
- Do not identify services, state roots, clients, or generated package roles
  from filename suffixes or type-name patterns. Use annotations and the parsed
  model.
- Do not treat one language as the source of truth for another. Generate every
  language from the same semantic model.
- Do not add warning-only behavior for safety failures. Auth, validation,
  transport setup, unsafe proto shapes, and compileability failures should fail
  generation, startup, or the call.
- Do not allow generated code to fail open. Missing TLS material, missing auth
  callbacks, invalid runtime config, invalid tokens, and invalid messages must
  fail closed.
- Do not hand-write versions in generators. Package and language versions come
  from dependency/config registries.
- Do not commit generated descriptor output or build products that should be
  produced under ignored target directories.

### Required Patterns

- Start from proto annotations, normalize into the common semantic model, and
  have each language consume that model.
- Centralize policy constants and derive behavior from those constants at
  generation time. Do not repeat policy values in templates.
- Keep request handling behind generated wrappers: validate, authenticate,
  authorize, enforce namespace policy, call the handler, validate outbound data,
  and map errors consistently.
- Keep generated runtime lifecycle under the ServiceManager. Long-lived tasks,
  streams, health reporting, token refresh, TLS renewal, and client pools should
  be owned and shut down by the manager.
- In generated ServiceManager startup, active state-store endpoint selection
  must happen before auto-TLS/X509Mint startup RPCs. If the first cert mint still
  fails, startup must fail closed before serving.
- Generated Go services that already have an X509Mint serving certificate must
  not block initial listen on the `ManageCert` renewal stream. Treat
  `ManageCert` as background renewal/handler delivery unless no X509Mint cert
  exists, in which case startup must fail closed.
- Cert-acquisition model differs by language by design, and each must fail
  closed before serving. **Go** obtains the initial serving cert via a unary
  `X509Mint` call in `ServiceManager.New`, then runs `ManageCert`/the cert
  stream as background renewal (see the previous invariant). **Java and Rust**
  obtain the serving cert solely from the auto-TLS/`ManageCert` bidi stream and
  block on the first delivery before `create()`/the constructor returns
  (`tlsReady.await()` in Java, `tls_ready_rx.await` in Rust); they serve with
  the stream-delivered cert (Java `GrpcServer.CertProvider` reading
  `getTlsCertChain()`, Rust `reloading_tls_incoming(tls_server_certified_key)`),
  not a unary mint. Do not "simplify" the Java/Rust first-delivery await away by
  analogy to the Go non-gating fix: for Java/Rust the stream is the only cert
  source, so removing the await would serve without a cert (fail-open). The
  invariant common to all three is that the serving cert must be present and
  the service must fail closed if it cannot be obtained before serving.
- Generated gRPC bidirectional streams authenticate at stream open. Clients
  attach bearer metadata before opening the stream, and generated server
  wrappers must fail closed before handler dispatch when stream-open identity
  is missing or invalid. Bidi send handles validate/enqueue/forward messages
  only; they must not mint, fetch, or refresh tokens per message because gRPC
  metadata is not updated after stream open. Mid-stream token expiry or
  rotation must be represented by stream teardown and re-open. Per-message
  namespace authorization may reuse the opening metadata, but it is not a
  server-visible token refresh.
- Prefer generation-time errors for contradictory proto shapes. Tacplus should
  make unsafe or ambiguous generated APIs unrepresentable.
- Add source-shape tests, current-generation compile gates, consumer-contract
  runtime gates, or `cmd/soundness` probes whenever a bug class is found.

---

## Soundness And Parity Gates

Tacplus promises typed, bounded, constrained, language-parity generated code.
The compiler must keep that promise with build-asserted checks rather than
manual inspection.

**Read `KNOWN_TEST_GAPS.md` alongside this section.** Several gates are green
without covering what their name suggests, and that file is the record of which
ones. A passing run is not by itself evidence that a behaviour is tested.

Current durable gates:

- `go run ./cmd/soundness -root .` checks constraint parity, field-kind probes,
  and enforced control-plane rows across the supported languages. The
  consolidated snapshot covered 27 data-plane `assert_*` constraints at 5/5
  language parity, 0 partial data-plane gaps, oneof scalar size-bound coverage,
  message nesting-depth guard coverage, unknown enum vocabulary guard coverage,
  and 5/5 enforced coverage for auth, server validation, trace, accounting,
  outbound timeout, bounded outbound retry/backoff, client request validation,
  unary client response validation, unified readiness, and
  ServiceManager-owned task teardown.
- Generated-contract tests must compile or execute current generated output.
  Do not rely on stale committed generated fixtures.
- Consumer-contract runtime tests should cover handler/wrapper/client behavior
  at the generated API seam, including auth-required, auth-disabled, validation,
  namespace mismatch, response validation, stream teardown, and backpressure.

Open soundness work belongs in `../roadmap/README.md` or the local
`todo/` ledger, not in standalone report files.
Examples include runtime proof for streaming/error-path trace/accounting
completeness.

Do not add a generic regex/pattern annotation. Add named, vetted `assert_*`
validators instead so the semantics are portable across generated languages and
the compiler does not emit attacker-controlled regular expressions into
backtracking engines.

### Data entitlement (spec.redact)

`(spec.redact)` drives egress data entitlement. The moving parts:

- **Validation** — `parser.ValidateDataEntitlementAnnotations` (the old
  `ValidateRedactAnnotations` is a thin alias) enforces the mode/type rules in one
  place at parse time. `ValidateReservedFieldNumbers` reserves field numbers
  `>= 50000` for the injected indicators; `ValidateNoNestedRedact` still rejects a
  redact on a *nested type declaration* (hoist it to top level).
- **Recursion** — `parser.BuildEntitlementTree` walks a message and its
  message-typed fields (whole-message OR field-by-field), resolving references via
  `parser.MessageResolver`. It is the single source of truth the registry emitter,
  the entitlement-message generator, and every language's `Sanitize` generator
  consume.
- **Proto generation (before native compilation)** —
  `proto.InjectRedactIndicatorFields` injects `<field>_redacted` bools
  (`50000 + source number`); `proto.AppendEntitlementMessagesToProtos` emits the
  `<T>Entitlement` messages; the shared `EntitlementLevel` enum lives in
  `sys/entitlement.proto`. Both run in every language's `generator.go`.
- **Behavior** — the `Sanitize<T>` functions are emitted per full-service language
  (Go `sanitize_gen.go`, Rust/Java `release_gen.go`/`release.go`); the keyed-HMAC
  primitives (`tacplusPseudonymize` / `tacplusPseudonymizeBytes`) share one
  cross-language wire vector (pinned by the pseudonymize helper tests).

### SPIFFE Identity Invariants

Build-time invariants the generator and gates must hold (contract in
`API.md` → SPIFFE Identity; genealogy in
`../archive/SPIFFE_IDENTITY_NORMALIZATION_PLAN.md`):

- No raw `spiffe://` construction, splitting, or prefix comparison outside the
  generated `sys/spiffe` (and its tests). All identity work goes through the
  generated parse/construct/compare helpers.
- Identity comparisons are normalized and use the right relation: the
  namespace at-or-below relation and the service-path (location-agnostic)
  relation are distinct and must not be cross-applied.
- Trust-domain config is validated, with no fallback default; malformed or
  missing fails generation or startup loudly.
- Black-box (SMB) isolation: the true ROOT CA never enters black-box config;
  only the per-SMB CA cert and the instance's own member cert. Private keys
  never cross the wire (member key stays on the instance, CA key stays with
  the authority).
- Every language's generated SPIFFE tests load the shared vector corpus
  (`cross-test/fixtures/spiffe_vectors.json`) so identity behavior cannot
  drift per language.
- Certificate delivery streams must follow `API.md` → ServiceManager →
  Certificate Delivery Streams:
  first credential delivery is startup-critical, renewals are log-and-retry,
  pending keys promote only after matching cert installation succeeds, and every
  retry re-resolves the authority endpoint while cleaning up the previous stream
  attempt.

### EventBus Model Rules

The user-facing contract is in `API.md` → EventBus Topics. Generator-side
rules (genealogy in `../archive/EVENTBUS_PUBSUB_MODEL_PLAN.md`):

- Canonical topic identity is `<proto-package>.<topic-message>.<topic-field>`,
  stored as structured metadata after validation — never carried around as an
  opaque string.
- Actor ownership bounds the scan: topics come from all loaded descriptors;
  publish/watch/subscribe bindings attach only to the current actor's own
  configured protos. Broker builds include every proto for type generation,
  but that inclusion never creates actor-side subscribers inside the broker
  (the historical `eventbus` self-subscriber failure).
- Derived publish/watch RPCs carry the standard method auth shapes
  (`method_s2s`/`method_c2s`) copied verbatim onto generated RPC metadata.
  There is deliberately no EventBus-specific auth enum: surface kind is a
  property of where the derived RPC is hosted, and a parallel vocabulary would
  turn every future auth change into an annotation migration.
- Generated RPC stems are package-qualified
  (e.g. `PublishTelemetryAgenticCodeEventBusEventStream`) so two packages'
  same-named topics cannot collide; registry guards reject duplicate canonical
  identities; store-derived subscription sugar fails on ambiguous event-type
  matches rather than guessing.

### Verb Drift Is An Audit Norm, Not A Gate

One method = one static verb = one access kind. The build gate (`EnforceVerbs`,
implemented under archived `../archive/MVP_PLAN.md` WS-G12) enforces naming,
registry membership, and verb↔pattern agreement at descriptor construction from
the compiler-read dictionary
`pkg/internal/common/verbs/registry.json`; it cannot see *runtime* drift — a
method whose declared verb is `Get` but whose implementation branches into
create/update behavior on input. That is an architectural-norm violation
surfaced by LLM/agentic audit (see `CONSUMER_NEUTRAL_ANALYSIS.md` → Do Not
Overload RPC Methods): the registry exposes the declared verb, access kind, and
audit meaning precisely so an auditor can compare declared intent against
observed behavior.

### Runtime Chaos Testing Principles

The chaos/soak harness skeleton lives at `cross-test/chaos/` (Phase 1; design
genealogy in `../archive/MODEL_STRESS_TEST_PLAN.md`, later phases in
`../roadmap/README.md`). Principles that outlive any one harness:

- **Randomness drives the schedule; deterministic oracles drive the verdict.**
  Fault injection order/timing is seeded-random (reproducible by seed); pass
  or fail comes only from invariant oracles — WATCH streams gapless, ordered,
  duplicate-free; auth fails closed (negative probes must be rejected);
  unary echo-transform correctness; resource trends flat over the soak.
- **Two profiles, one harness.** Profile A exercises the generated model with
  synthetic nodes and stubbed rivets (no consumer services — the model is the
  unit under test); Profile B reuses the same harness against a real
  deployment stack. Keep the harness deployment-agnostic.
- **Recovery has teeth**: in-flight calls fail fast (no hangs), service
  resumes within a bounded SLA after a fault, and no resource creep
  (goroutines/threads/FDs/memory trend flat) across repeated fault cycles.
- Fault taxonomy: SIGTERM, SIGKILL, network blackhole, dependency restart,
  token expiry, slow consumer/backpressure.

---

## Typed Disk Persistence Invariants

Typed disk persistence is implemented generated runtime behavior. Generators and
templates must preserve these invariants:

- JSON plus newline framing only; no alternate serialization format.
- Write paths use generated `ToJSON`; read paths use generated `FromJSON` and
  then `Validate`.
- Invalid data on read fails closed.
- The runtime assumes single writer, single owning process, local filesystem.
- Replacement writes use same-directory temp files, fsync, atomic rename, and
  parent-directory fsync.
- Redo-log restore must recover in-progress compaction/promote state before
  reading active data, then fold temporary tail records.
- ServiceManager-owned persistence scheduling replaces ad hoc service backup
  loops.

---

## Checklists

### Adding a New Annotation

- [ ] Add the extension to `pkg/proto/sys/spec.proto`
- [ ] Run `just generate` or `go generate ./pkg/internal/proto/spec/...`; generated descriptor code must stay under ignored `target/proto/spec/sys/`
- [ ] Add parsing in `pkg/internal/common/parser/descriptor.go`
- [ ] Add to model if needed (`pkg/internal/common/model/model.go`)
- [ ] Implement code generation for Go
- [ ] Add/adjust TypeScript only when the change affects client surfaces (C2D/C2S/W2S/REST) or rejection behavior
- [ ] Add/adjust Python only when the change affects client surfaces (C2D/C2S/W2S/REST) or rejection behavior
- [ ] Implement code generation for Java
- [ ] Implement code generation for Rust
- [ ] Update USER.md annotation reference
- [ ] Test: `tacplus build` in a consumer project
- [ ] Test: full-service build-tests pass for go/rust/java; full-client tests pass for python/typescript; lite client (C2D/C2S/W2S) and rejection tests pass for swift/csharp

### Modifying Embedded Proto Generation

- [ ] Verify embedded `sys/` and `common/` protos are overlaid into generated output
- [ ] Verify generated descriptor code remains under ignored `target/proto/`
- [ ] Check Go: `pkg/internal/go/types/generator.go`
- [ ] Check Java: `pkg/internal/java/types/generator.go`
- [ ] Check TypeScript: `pkg/internal/typescript/types/generator.go`
- [ ] Check Python: `pkg/internal/python/types/generator.go`
- [ ] Check Rust: `pkg/internal/rust/types/builder.go`

### Modifying Package Structure

- [ ] Ensure `types/` and `clients/` subdirectory pattern maintained
- [ ] Verify manifests are inside subdirectories, not at package root
- [ ] Check parent build files updated (pom.xml, package.json, Cargo.toml)

---

## Distributed Tracing — Typed TraceEvent

`TraceEvent` uses `AccountingMethod rpc` (field 5) plus `TraceEventKind kind`
(field 6) for typed RPC identification and lifecycle classification. Go, Java,
and Rust service wrappers set the typed `rpc` field from the same generated
accounting-method mapping used for accounting events.

Rust gateway proxy dispatch exposes
`trace_rpc_for_path(grpc_path) -> Option<AccountingMethod>` so gateway trace
publishers can set the same typed `rpc` field for proxied requests. The
`message_type` string field (field 3) was removed.

### TODOs

- [ ] Consider moving `sys/trace.proto` to generated so it can use per-service RPC enums
      directly instead of the shared `AccountingMethod` type

---

## Regression Testing

When a bug is found, a corresponding test (unit, integration, or system) must be created before the fix is committed. The test must fail without the fix and pass with it, preventing future regressions.

---

## License

Copyright (c) 2026 Seabeam LLC. Licensed under the Apache License, Version 2.0; see [LICENSE](../LICENSE).
