# Everything Everywhere Is Typed All At Once

*In tacplus, every field constraint, state boundary, auth policy, and RPC exists in one place — the proto definitions — and every supported generated surface comes from that single source of truth.*

---

## The Problem

Building microservices is repetitive. Every service needs the same infrastructure: typed messages, validated inputs, serialization, authentication, authorization, state management, health reporting. In a multi-language platform, each service needs all of this in its own language, following the same contracts as every other service.

The usual approach is a combination of hand-rolled validation, shared libraries, documented conventions, and discipline. It works — until it doesn't. A validation rule changes in one language and not another. A service starts writing state it doesn't own. An auth check gets added here but not there. Contracts drift. Bugs happen. And somewhere in the system, an important invariant exists only in a developer's head.

The deeper issue is that the *contract* — what values are valid, what services can call what, who owns which state — is scattered across code in multiple languages. There is no single place that says what the system actually is.

---

## The Insight

Proto files already describe the shape of your data. Every message, every field, every type is declared once, in a language the whole system agrees on. What if they described *everything* — not just the shape, but the constraints, the ownership, the auth policy?

Then a compiler could read those richer definitions and generate everything that flows from them. Not just serialization stubs — the complete service infrastructure for the server languages, plus compatible client packages for client-only targets, from a single source of truth.

---

## What tacplus Is

tacplus is that compiler. It reads `.proto` files annotated with custom `spec` options and produces complete, runnable service infrastructure. Not just types — entire services.

**Input:** Annotated proto files + a `tacplus.yaml` declaring project-local generation facts that do not logically belong in proto, such as target language, package name, output directory, and proto source.

**Output:** A `tacplus/` directory with everything a microservice needs to run:

```
tacplus/
  types/        -- Protobuf bindings, validation, serialization
  sys/         -- Generated runtime libraries
  s2sm/         -- Service lifecycle, state accessors, gRPC clients
  proto/        -- Processed proto files
  registry.json -- Semantic IR (types, boundaries, auth, norms)
  AGENT.md      -- Architectural norms for agentic analysis
```

The rule is simple for full-service targets: if tacplus generates service runtime behavior, it does so for Go, Java, and Rust with parity. Swift, C#, Python, and TypeScript are client-only targets and must reject service-side/runtime generation instead of emitting partial servers. They come in two tiers: Swift and C# are "lite" client targets (C2D, C2S, and W2S clients; Swift's W2S is unary + server-streaming over SSE, C#'s is unary-only, and the streaming methods neither can carry are emitted as unavailable stubs), while Python and TypeScript are full-client targets that generate every client surface (C2D, C2S, W2S, REST).

---

## The Type System

Every field carries its constraints. A `string listen_addr` annotated with `(spec.assert_listen_addr)` doesn't just say "this is a string" — it says "this must be a valid `host:port`." A repeated field annotated with `(spec.assert_max_size)` has a hard upper bound. An enum field annotated with `(spec.assert_enum_defined)` cannot be the zero value.

```protobuf
message Config {
  string listen_addr = 1 [
    (spec.assert_required) = true,
    (spec.assert_listen_addr) = true
  ];
  int32 max_connections = 2 [
    (spec.assert_gt) = 0,
    (spec.assert_le) = 10000
  ];
  string cert_pem = 3 [(spec.assert_enc_type) = ENC_TYPE_X509_CERT_ANY];
}
```

tacplus generates native types with protobuf bindings, validation functions for all constraints, and serialization helpers for JSON, YAML, TOML, and binary — per supported target surface, with identical semantics where that surface exists. Full-service targets run generated validation at every service boundary. Client-only targets validate before sending.

The system is intentionally liberal in what it accepts and conservative in what it emits. Input-side annotations allow field aliases and format variations for compatibility. Output-side serializers are typed and normalized.

---

## The State System

Services own their state. A message annotated with `(spec.service_state)` becomes a named partition of distributed state, backed by a state store that tacplus defines as an interface and a downstream runtime implements. tacplus generates the gRPC state service, typed client accessors, Watch streams, and cross-service access control against that interface, independent of any particular backing store. Every message-typed field is a boundary — stored independently and convergent (lockless, last-writer-wins / append-only).

```protobuf
message ServiceState {
  option (spec.service_state) = {label: ["myservice"], export: true};

  Config config = 1;
  Session current_session = 2;
  repeated Session sessions = 3 [
    (spec.indexed) = true
  ];
}
```

A service has read-write access to its own state. Other services declare
read-only imports on the fields that project exported state:

```protobuf
message ConsumerState {
  option (spec.service_state) = {label: ["consumer"]};

  myservice.Session current_session = 1 [
    (spec.state_import) = "myservice.ServiceState.current_session"
  ];
}
```

The import is read-only by design, currently supports singular message
projections, and the access is enforced server-side. The backing store validates
every gRPC call against the proto-declared permissions. There is no way to
bypass this with a custom client — the caller's identity is in the token, and
the server checks it against the declared imports.

This enforces a single-writer model. Services communicate state changes through formal contracts, not direct mutation. Distributed state stays disciplined.

---

## Services, Vservices, And Traits

A **service** is a long-lived deployable unit such as stratum, eventbus, or kernel.
It is a process: something that is built, deployed, and dialed.

A **vservice** and a **trait** are both tacplus-declared, service-implemented
contracts. Neither is a process. Both carry the same identity shape — a canonical
name that is the entitlement key, plus an optional stable `TokenEntitlements` field
id — and both are hosted by a physical service while callers are entitled against the
contract's name, never the host's. They differ in what they declare:

| | declares | hosted by |
|---|---|---|
| `(spec.vservice)` | a **data** contract: event type, projection set, subscribe surface, entitlement model | the service whose `(spec.service_s2s).label` claims it |
| `(spec.trait)` | an **RPC** contract: a named surface with a host, its emitted service names, and its peer audience | the service named in `(spec.trait).host` |

The useful mental model is a database with tables:

| Database concept | tacplus concept |
|---|---|
| database | service |
| table | vservice |
| create a table | add a vservice proto, regenerate tacplus, rebuild the service |
| schema migration | revise the proto for that vservice |

A trait is the other axis. Where a vservice asks "who owns this data", a trait asks
"who implements this surface" — and answers it in the proto rather than in generator
source. The declaration names the host, so the plane exists because of the
declaration, not because of a constant somewhere: delete the trait proto and the
surface is not generated at all. The **audience is the contract, not the
implementor** — the host label is only whom the gateway dials, while the caller's
token is checked against the trait name — so a second implementor of the same trait
needs no client change.

The `query` trait is the worked example: `name: "query"`, `host: "stratum"`, and the
record query plane emitted onto both caller surfaces from it. Note the honest limit —
`query` has exactly one implementor today, so the second-implementor property is
expressible but unproven. See
[`RECORD_EVENT_MODEL.md`](RECORD_EVENT_MODEL.md) for the plane it generates.

Vservices and traits are flexible and service implementations are stable. Adding or
changing one contract should not destabilize unrelated contracts hosted by the same
service. Service-local generators should consume `registry.json`, not parse proto
files directly, so the service and everything it hosts use the same semantic IR that
tacplus used to generate code.

---

## The Auth Model

Auth policy is declared in proto, not hand-coded. A service annotated with `(spec.service_s2s)` accepts only authenticated service-to-service calls. A service annotated with `(spec.service_c2s)` is client-facing — the generated auth interceptor verifies the token and checks entitlements before dispatching.

```protobuf
service InternalAPI {
  option (spec.service_s2s) = {auth_services: ["caller-a", "caller-b"]};
}

service PublicAPI {
  option (spec.service_c2s) = {auth_entitled_users: true};
}
```

The generated auth layer handles JWT verification, entitlement checking, and token propagation. Services declare their auth policy in proto and implement their logic in handlers. Auth is not a cross-cutting concern — it is a generated structural property of the service.

---

## The Identity Model

Identity is typed, derived, and separated from attestation.

**Typed**: a SPIFFE ID's kind (service, workload, user, client, credential, resource) is recoverable from fixed path tags in the URI itself — no out-of-band context needed to know what a principal is. Reserved tags make the parse unambiguous at every depth.

**Derived**: the SPIFFE ID is a computation over token claims (`namespace`, `sub`, `sub_type`) plus deployment config (trust domain, location), never a claim itself. Claims stay small and stable; the full identity is reconstructed wherever needed through one generated helper library with shared cross-language test vectors. Durable keys (X509 URI SANs, workload revocation, health identity, event owners) store the normalized derived form.

**Separated from attestation**: *who* a principal is and *how* it proved itself are different axes. A user is a tenant-scoped role — the same human logging in via OAuth or a credential pair is the same subject; the login path lives in `azp` (authorized party), set only by the minter from trusted context. This prevents the classic smell of identity forking per auth method. Credentials (cred-pairs) are durable resources that *attest* a principal, never principals themselves.

Two further rules keep identity sound: segments are validated against a strict allowlist and **rejected, never sanitized** (a sanitizer maps two different external values onto one identity; a rejector fails closed); and external identifiers enter the tree only through a deterministic opaque-segment hash, domain-separated by kind, byte-identical in every language.

---

## Cross-Language Parity

The same proto definitions generate equivalent full-service infrastructure in Go, Java, and Rust. The same validation rules apply. The same state semantics are honored. When a field is annotated `(spec.assert_required)`, each supported target surface gets the same required check. When a message is annotated `(spec.service_state)`, each full-service target gets the same accessor interface.

Parity is a build-time invariant, not an aspiration. A deep analysis pass can run against the full-service outputs and client-only surfaces and report divergence. The same analysis is used to generate equivalent services in the full-service languages as a pressure test against most-favored-language bias. The generated outputs can be discarded; the purpose is to verify the abstractions are genuinely language-neutral.

No language is treated as universally superior. Each is chosen for its tradeoffs at a particular layer. The non-negotiable bias is toward machine-checkable contracts.

---

## The Semantic IR

tacplus compiles proto annotations into `registry.json`: a normalized, language-neutral semantic model of the service. It includes types, services, state roots, views, auth metadata, generation intent, and architectural norms.

Every language generator emits the same `registry.json` from that model. This centralizes interpretation and prevents per-language semantic drift. `registry.json` is an output — downstream tooling and runtimes consume it, the generators do not. At runtime, a service that serves `registry.json` from a well-known endpoint is self-describing — tooling, agents, and introspection APIs read the same model that generated the code.

This is the same file at build time (for generation, linting, testing, docs, and analysis) and runtime (for discovery, agents, and introspection). One source of truth, two consumers.

---

## Architectural Norms and Agentic Analysis

Some rules are hard guarantees enforced in every build: type correctness, constraint validation, code generation consistency.

Other rules are architectural norms: patterns that should generally hold but are higher-order than type correctness. tacplus carries them in the `AGENT.md` it generates for each build and embeds into `registry.json` alongside the rest of its own documentation set. (tacplus does not read consumer-authored `agent.md` files — there is no ingestion path for them.) The agentic analysis pass checks for cross-language parity drift, suspicious service shapes, and anti-patterns — and can be run on demand without affecting the build.

This is governance carried in machine-readable form. The norms travel with the code.

---

## What "Plus" Means

tacplus generates beyond types — that is what "Plus" means:

- **Validation** — constraint enforcement at every boundary, generated from annotations, not hand-written
- **State** — distributed ownership, Watch streams, cross-service access control
- **Auth** — S2S and client-facing auth, token verification, entitlement checking
- **Discovery** — a canonical semantic IR at build time and runtime
- **Lifecycle** — cluster connection, config subscription, health reporting, graceful shutdown
- **Parity** — the same contracts, enforced the same way, for every supported target surface

The result: services are defined by their proto files. The implementation is a thin handler layer. Everything else is generated, typed, validated, and enforced.

For usage details, CLI reference, and annotation tables, see [USER.md](USER.md).
For generated API contracts, see [API.md](API.md). For compiler development
guidelines, see [DEVELOPMENT.md](DEVELOPMENT.md).
