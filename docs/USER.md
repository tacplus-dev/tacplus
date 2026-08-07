# tacplus User Guide

Reference for developers using tacplus to build services — writing proto files, configuring `tacplus.yaml`, and understanding what gets generated. For the conceptual model, see [THEORY.md](THEORY.md). For generated API contracts, see [API.md](API.md). For working on the tacplus compiler itself, see [DEVELOPMENT.md](DEVELOPMENT.md).

---

## Table of Contents

1. [Patterns for Service Implementers](#patterns-for-service-implementers)
2. [tacplus.yaml Reference](#tacplusyaml-reference)
3. [CLI Commands](#cli-commands)
4. [Annotation Reference](#annotation-reference)
4. [Conditional Requirements](#conditional-requirements)
5. [Formal Verification Annotations](#formal-verification-annotations)
6. [REST Method Control](#rest-method-control)
7. [Governed RPC Verbs](#governed-rpc-verbs)
8. [State System](#state-system)
9. [Auth System](#auth-system)
10. [Store Service Annotations](#store-service-annotations)
11. [Projection Annotations](#projection-annotations)
12. [registry.json and the Semantic IR](#registryjson-and-the-semantic-ir)
13. [Generated Libraries](#generated-libraries)
14. [Service Lifecycle](#service-lifecycle)
15. [Distributed Tracing](#distributed-tracing)

---

## Patterns for Service Implementers

See [API.md](API.md) for generated API contracts and [DEVELOPMENT.md](DEVELOPMENT.md#compiler-development-patterns) for compiler-side pattern rules. Code examples for each pattern are below.

### Use the Generated Service Manager

```go
// Correct: create the generated manager, then run your handler
func main() {
    sm, err := s2sm.New()
    if err != nil {
        log.Fatal(err)
    }
    if err := sm.Run(internal.NewHandler()); err != nil {
        log.Fatal(err)
    }
}

// Wrong: manually wiring gRPC servers, TLS, auth interceptors
lis, _ := net.Listen("tcp", addr)
grpc.NewServer(grpc.Creds(...), grpc.UnaryInterceptor(...))
```

### Use Generated gRPC Wrappers

```go
// Correct: implement the generated Handler interface and run the manager —
// the generated wrappers dispatch transport/auth/validation to your handler.
sm.Run(&myServiceImpl{sm: sm}) // myServiceImpl implements the generated Handler

// Wrong: manually implementing grpc.ServiceDesc, calling grpc.Dial directly
```

### Use Generated Clients and State Accessors

```go
// Correct: reach downstream service clients and state through the manager.
api := sm.Grpc().Clients().Other().OtherApiClient() // typed downstream client
sc := sm.StateClients()                             // typed access to state boundaries

// Wrong: dialing the service/state gRPC address and calling raw RPCs
```

### Use Generated Serializers and Deserializers

```go
// Correct: use the generated helper
config, err := mypkg.ConfigFromBytes(data)
bytes, err := mypkg.ConfigToJSON(config)

// Wrong: json.Unmarshal(&config, data) — bypasses aliases, defaults, error typing
```

### Use Generated Validation

```go
// Correct: validation helpers are package-level functions, not methods
if err := mypkg.ValidateFooRequest(req); err != nil {
    return nil, err
}

// Wrong: manual string length checks, regex matching, etc. for annotated fields
```

### Hand-Written Code Is Allowed Exactly Here

```go
// internal/handler.go — the ONLY hand-written file: implement the generated
// Handler interface, then run the manager.
func NewHandler(ctx context.Context, sm *s2sm.ServiceManager) error {
    return sm.Run(&myServiceImpl{sm: sm})
}
```

Service implementations (`myServiceImpl`) contain business logic. Everything else — transport, auth, state, serialization, health — is generated.

---

## tacplus.yaml Reference

`tacplus.yaml` is the project-local generation config. It should declare only facts that cannot logically be defined in proto: the target language, generated package/module name, output location, proto source, config composition, and which client transport surfaces this package emits. Service shape belongs in proto annotations: service categories, auth policy, state imports/exports, gRPC client dependencies, REST routes, and store/state behavior are all derived from annotated proto definitions.

Only `language` and `output-dir` are required. `pkg` is commonly set explicitly, but may be derived from the host project when omitted.

### Top-Level Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `language` | string | Yes | Full-service targets: `go`, `rust`, `java`. Full-client targets: `python`, `typescript`. Lite client targets: `swift`, `csharp`. |
| `output-dir` | string | Yes | Output subdirectory relative to project dir |
| `pkg` | string | No | Canonical package name (Java-style reverse DNS, e.g. `com.example.myservice`). Converted to native format per language. |
| `src` | string | No | Primary proto source — local path or git URL. No default — must be supplied via `--src` or `tacplus.yaml`. |
| `sources` | string list | No | Additional proto sources merged with `src` into one generation (e.g. a shared base plus a reusable service's protos). All sources form one proto tree; a file at the same path in two sources must be byte-identical (deduplicated) or it is a hard error, and `dependencies.yaml` is merged across sources (conflicting pins error). Repeatable `--src` on the CLI overrides `src`+`sources`. |
| `import` | string | No | Path to base config to import (resolved relative to this file) |

### Client Transport Surfaces

Client transport surfaces are a function of the proto graph, not YAML config.
Tacplus generates the surface for a declared client dependency; there is **no**
`clients.*.enabled` switch (it is obsolete and rejected — see the note below).

Proto declares the dependency:

- `option (spec.grpc_c2s_client) = {service: "pkg.Service"};` → a C2S
  (native gRPC) client manager (`c2sm`).
- `option (spec.grpc_w2s_client) = {service: "pkg.Service"};` → a W2S
  (gRPC-over-REST / web) client manager (`w2sm`).
- a `service X { option (spec.service_c2d) = {...} }` in the package → a C2D
  (client-to-local-daemon) client manager (`c2dm`).

If a dependency is not declared, no manager for that surface is emitted. C2S and
W2S are distinct surfaces from distinct declarations (they are not merged); W2S
is the runtime web-transport alternative to C2S, and the application chooses
which client to use at runtime.

Language targets do not all support the same surface set:

- Full-service targets (`go`, `rust`, `java`) generate service runtimes and any
  declared C2S/W2S/C2D client surfaces.
- Full-client targets (`python`, `typescript`) generate C2S, W2S, C2D, and REST
  *client* surfaces. Service-side output — S2S clients, server wrappers,
  ServiceManager runtime, EventBus subscriber/publisher runtime, health runtime,
  SMB runtime, service state, or service stores — is rejected loudly.
- Lite client targets (`swift`, `csharp`) generate C2D, C2S, and W2S client
  surfaces. Their W2S is unary-only (HTTP/JSON; no SSE or WebSocket streaming):
  a declared client/server-streaming method is **omitted** from the generated
  W2S client (not a generation error, so a mixed unary+streaming service still
  produces a compilable client) — the omission is logged. Use the
  Go/Java/Rust W2S client for streaming. Unlike the full-client tier they
  currently emit no client-side request validation and no default request
  deadline. Any service-side surface is rejected loudly.

Full-service targets generate a `c2dm` manager for services marked with
`(spec.service_c2d)`, with typed gRPC clients over local daemon IPC. Go and Rust
dial a Unix-domain socket. Java accepts a caller-supplied local-daemon
`ManagedChannel` so the embedding app chooses the local IPC adapter.

Swift and C# emit lite client packages that include a C2D surface: Swift emits a
SwiftPM package with a Unix-domain socket `C2DClient` helper, while C# emits a
.NET package that defaults to named pipes on Windows and Unix-domain sockets
elsewhere. They also emit C2S and (unary-only) W2S client surfaces. Python
and TypeScript emit a C2D client-side surface for `(spec.service_c2d)` services,
alongside their C2S/W2S/REST client surfaces.

W2S is a full client surface, not a REST-only subset. Unary RPCs use HTTP/REST,
server-streaming RPCs use SSE, and client-streaming or bidirectional RPCs use
WebSockets. Tacplus surfaces blocked or timed-out transports as errors; polling
fallback is application behavior.

The obsolete `clients.c2s/w2s/c2d.enabled` YAML block is **rejected** with a
config error. Two builds of the same proto graph, service identity, language, and
package name produce the same surfaces regardless of local build settings —
declare client dependencies in proto, not in `tacplus.yaml`.

### Config Composition

Configs can import a base file and override specific fields:

```yaml
# tacplus-base.yaml
language: go
src: https://gitlab.com/org/proto.git
pkg: com.example.myservice
output-dir: tacplus

# tacplus.yaml
import: tacplus-base.yaml
pkg: com.example.myservice.ci
```

Multiple config files via CLI:

```bash
tacplus build -c tacplus-base.yaml -c tacplus-overrides.yaml
```

Merge rules:
- **Strings**: overlay wins if non-empty
- **Lists**: overlay replaces entirely if non-empty

### CLI Flag Equivalents

| YAML Key | CLI Flag |
|----------|----------|
| `src` | `--src` |

CLI flags override YAML values when both are specified.

---

## CLI Commands

| Command | Purpose |
|---------|---------|
| `tacplus build` | Generate proto types and services for a single language |
| `tacplus validate` | Validate proto files |
| `tacplus verify` | Run formal verification on Rust or Java code |
| `tacplus registry` | Update the service/vservice registry from proto definitions |
| `tacplus upgrade` | Upgrade tacplus to the latest patch release |
| `tacplus version` | Print the version and build information |

### Common Usage

```bash
# Generate for a single language from a config + proto source
tacplus build --config tacplus.yaml --src /path/to/proto --tag latest

# Layer multiple configs (later files override earlier)
tacplus build -c tacplus-base.yaml -c tacplus-overrides.yaml

# Go integration via go:generate
//go:generate tacplus build --config tacplus.yaml --src ./proto --tag latest
go generate ./...
```

---

## Annotation Reference

RPC naming is governed: every RPC is `Verb<Noun>` against a closed verb
registry with fold rules (Search/Query→List, New/Add→Create,
…). The authored compiler dictionary lives at
`pkg/internal/common/verbs/registry.json`; the registry, pattern taxonomy, and
fold test are documented in [API.md → RPC Patterns And Verbs](API.md). Name new
RPCs canonically; the classifier rejects non-canonical names during descriptor
parsing.

### Field Constraint Annotations

| Annotation | What It Validates |
|------------|-------------------|
| `assert_required` | Non-zero value (scalars), non-empty (strings/repeated) |
| `assert_ipaddr_type` | IPv4, IPv6, subnet mask, CIDR network |
| `assert_net_port` | Valid TCP/UDP port (1-65535) |
| `assert_url` | HTTP, HTTPS, or either |
| `assert_email` | Email address format |
| `assert_fqdn` | Fully qualified domain name |
| `assert_host` | IP address OR FQDN |
| `assert_listen_addr` | host:port format |
| `assert_base64` | Valid base64 encoding |
| `assert_enc_type` | RSA/EC/Ed25519 key or X.509 cert in PEM |
| `assert_semver` | Semantic version (e.g., "1.2.3") |
| `assert_duration` | Positive or non-negative duration |
| `assert_file_path` | Absolute, relative, or either |
| `assert_enum_defined` | Enum is not the zero/unspecified value |
| `assert_max_size` | Maximum byte size for string/bytes (default 32KB) or element count for repeated/map (runtime default 1024; user protos should set explicit bounds) |
| `assert_gt` / `assert_ge` / `assert_lt` / `assert_le` | Numeric comparisons |
| `assert_numeric` | Complex numeric constraints (in, not_in, multiple_of) |
| `assert_float` | Float constraints with epsilon tolerance |
| `assert_sql` | Valid SQL statement of permitted type (SELECT, etc.), rejects injection patterns |
| `assert_invariant` | Formal invariant (constrains a `#[kani::proof]` harness via `kani::assume()` in Rust, a JBMC harness via `CProver.assume()` in Java) |
| `assert_min_size` | Minimum byte length (string/bytes) or element count (repeated/map) |

### gRPC Client Dependency Annotations

These are **file-level** options declared once per proto file. Proto package names are globally unique (a gRPC requirement), so no file path is needed — tacplus resolves the package name to its proto file at build time.

```protobuf
// S2S: this service calls identity.Mint (service-to-service, uses service credentials)
option (spec.grpc_s2s_client) = {service: "identity.Mint"};

// Client-facing service dependency. tacplus.yaml selects C2S, W2S, or both.
option (spec.grpc_c2s_client) = {service: "gateway.TokenMint"};

// Older transport-named spelling, also treated as a client-facing dependency.
option (spec.grpc_w2s_client) = {service: "dashboard.Dashboard"};
```

Multiple clients of the same type can be declared with repeated options:

```protobuf
option (spec.grpc_s2s_client) = {service: "identity.Mint"};
option (spec.grpc_s2s_client) = {service: "eventbus.Admin"};
```

tacplus generates typed client wrappers for each declared dependency and makes them available via the service manager (`sm.Grpc().Clients().<Host>().<Service>Client()` in Go; in Rust use `sm.s2s().<host>().<service>_client()` from an S2S service, or `sm.grpc().<host>().<service>_client()` from a c2s/w2s/c2d client).

### Local Daemon Services

Services marked with `(spec.service_c2d) = {socket_path: "/run/mydaemon.sock"};` are local daemon RPC surfaces. `socket_path` is required — there is no default (a shared default would collide between daemons on the same host).
When a package's proto graph declares in-scope `(spec.service_c2d)` services,
full-service targets emit a `c2dm` manager and client-only targets emit their
language-specific C2D helpers for those services (no YAML flag is involved). C2D
clients do not attach generated bearer tokens; the local IPC boundary is the
operating-system transport and its permissions.

### Serialization Annotations

| Annotation | Effect |
|------------|--------|
| `json_key` | Override JSON output key (when proto field was renamed) |
| `json_aliases` | Additional keys accepted during JSON deserialization |
| `default_string` / `default_int` / etc. | Default values generated per language |
| `string_const` / `int_const` / etc. | Constants generated at file level |

### Enum Annotations

| Annotation | Effect |
|------------|--------|
| `json_return_value` | Override JSON representation of an enum value |

---

## Conditional Requirements

Fields can be conditionally required based on sibling field values:

```protobuf
// Required when disable_tls == false
string cert_path = 5 [(spec.required_if_false) = "disable_tls"];

// Required when action == "ACTION_REDIRECT"
string redirect_url = 3 [(spec.required_if_eq) = {field: "action", value: "ACTION_REDIRECT"}];
```

---

## Formal Verification Annotations

A field can carry a formal invariant via `(spec.assert_invariant)`, which is
emitted into a generated proof harness:

```protobuf
string token = 1 [(spec.assert_invariant) = "self.ttl > 0 && self.ttl <= 86400"];
```

In Rust the harness is a `#[kani::proof]` that constrains the field with
`kani::assume(...)`; in Java it is a JBMC harness that constrains it with
`CProver.assume(...)`. The `tacplus verify` command runs these proofs (Rust or
Java only).

> `(spec.proof_requires)` / `(spec.proof_ensures)` exist and are parsed, but no
> generator consumes them yet — they emit nothing today.

---

## REST Method Control

RPC methods can declare additional HTTP methods beyond the implicit POST:

```protobuf
rpc GetConfig(GetConfigRequest) returns (Config) {
  option (spec.rest_method) = {methods: [REST_METHOD_GET]};
  option (spec.rest_path) = ".well-known/openid-configuration";
}
```

`rest_path` creates an alias REST route in addition to the convention path. Available methods: `GET`, `PATCH`, `PUT`, `DELETE`.

---

## Governed RPC Verbs

RPC method names should be `Verb<Noun>` with a canonical verb from the tacplus
registry. The verb defines the method pattern, static access kind, retry
posture, audit meaning, and entitlement capability. Do not invent synonyms for
existing patterns, and do not overload one RPC so a branch changes its
capability, for example a read method that conditionally writes.

Core verbs:

| Verb | Pattern | Access | Use |
|------|---------|--------|-----|
| `Get` | `GET` | `READ` | Fetch one resource by identity |
| `List` | `LIST` | `READ` | Collection read declared with `method_collection` |
| `Watch` | `WATCH` | `READ` | Snapshot/delta change stream |
| `Create` | `CREATE` | `WRITE` | Bring durable resource state into existence |
| `Update` | `UPDATE` | `WRITE` | Modify an existing resource |
| `Delete` | `DELETE` | `WRITE` | Remove resource state |

Action verbs: `Mint`, `Revoke`, `Authorize`, `Send`, `Publish`, `Subscribe`,
`Broker`, `Execute`, `Cancel`, `Retry`, `Reset`, `Ping`, `Proxy`, and `Merge`.

Agent verbs: `Generate`, `Embed`, `Classify`, `Rank`, `Assist`, `Guard`, and
`Evaluate`.

Known synonyms such as `Search`, `Query`, `Fetch`, `Add`,
`Remove`, `Logout`, `Summarize`, and `Rerank` are rejected with canonical
rename guidance. (`Subscribe` and `Broker` are *canonical* action verbs, not
synonyms — a bidi/stream subscription is `Subscribe<Noun>`, not `Watch<Noun>`.) If a real operation does not fit the closed set, document the
new pattern and update tacplus instead of hiding the distinction in consumer
code.

Migration guidance:

- Rename folded verbs to their canonical form: `Search`/`Query`/`Find`/`Filter`
  become `List`, `Fetch`/`Load` become `Get`, and `Add`/`New` become `Create`.
- Match the wire shape to the verb: `List` requires `method_collection`
  (`SNAPSHOT` or `PAGE`), `Watch` requires a server-streaming
  `method_collection` with `WATCH`, and client/bidirectional streams must use
  action or agent verbs.
- Treat the classifier error as the source of truth. Do not rely on missing
  `registry.json` fields or package-wide entitlement wildcards as a migration
  bridge; generation now fails before those artifacts are emitted.

`registry.json` exposes the resolved fields on each `RpcInfo`: `rpc_verb`,
`rpc_noun`, `method_pattern`, `access_kind`, `verb_class`,
`entitlement_scopes`, `retry_policy`, `capability_scope`, `verb_definition`,
`verb_boundary`, `audit_meaning`, `rejected_verbs`, `allowed_patterns`, and
`handler_shape`.

---

## Tags

tacplus embeds `sys/tags.proto` for Kubernetes-style tag sets and selectors.
Tags are runtime model data, not compiler annotations, so they live outside
`sys/spec.proto`. Import `sys/tags.proto` when a service needs portable
key/value grouping or selector matching:

```protobuf
import "sys/tags.proto";

message Workload {
  string id = 1;
  tags.TagSet tags = 2;
}

message ListWorkloadsRequest {
  tags.TagSelector selector = 1;
}
```

The embedded proto shape is:

```protobuf
package tags;

message TagSet {
  map<string, string> tags = 1;
}

message TagSelector {
  map<string, string> match_tags = 1;
  repeated TagSelectorRequirement match_expressions = 2;
}

message TagSelectorRequirement {
  string key = 1;
  TagSelectorOperator operator = 2;
  repeated string values = 3;
}

enum TagSelectorOperator {
  TAG_SELECTOR_OPERATOR_UNSPECIFIED = 0;
  TAG_SELECTOR_OPERATOR_IN = 1;
  TAG_SELECTOR_OPERATOR_NOT_IN = 2;
  TAG_SELECTOR_OPERATOR_EXISTS = 3;
  TAG_SELECTOR_OPERATOR_DOES_NOT_EXIST = 4;
}
```

`sys/` and `chassis/` are reserved tacplus namespaces: `sys/` holds the embedded
declarative proto primitives (import `sys/spec.proto`, `sys/tags.proto`, etc.),
and `chassis/` holds the generated runtime machinery. Tacplus owns and injects
both — a proto authored under `sys/` or `chassis/` in your source tree is
rejected at build time. Import from `sys/`; never author files there.

Generated package locations follow the embedded library convention. Proto-derived
types live under the declarative `sys/` namespace (emitted as `types/sys/...`);
the runtime machinery (helpers and library code) lives under the `chassis/`
namespace, separated from the declarative types:

- Go: protobuf types in `types/sys/tags`, helpers in `chassis/tags`.
- Java: protobuf types in `{package}.types.sys.tags`, helpers in
  `{package}.chassis.tags`.
- Rust: protobuf types under the generated `types::sys::tags` module, helpers
  in the generated `chassis/tags` crate.
- Python: protobuf types under `types.sys.tags`, helpers in `chassis.tags`.
- TypeScript: protobuf types in the generated `types/sys/tags` package, helpers
  in the `chassis/tags` package.

The generated runtimes include helpers for:

- subset checks: every key/value in one `TagSet` exists in another;
- selector matching: `match_tags` plus `match_expressions`;
- explicit key/value and selector validation.

Subset helpers return true when every key/value pair in `want` exists with the
same value in `have`. Empty `want` returns true. Empty or absent `have` matches
only empty `want`. Extra tags in `have` are ignored.

Selector semantics follow Kubernetes:

- `match_tags` is equivalent to an `In` expression with one value;
- all requirements are ANDed;
- `In` requires a present key whose value is in `values`;
- `NotIn` matches when the key is missing or its value is not in `values`;
- `Exists` matches any present key and takes no values;
- `DoesNotExist` matches an absent key and takes no values.
- `UNSPECIFIED` is invalid and validation fails loudly on it.

Validation is explicit. Matching helpers do not validate keys or selectors
implicitly, so callers can separate "is this selector well-formed?" from "does
this selector match these tags?" Validation follows Kubernetes label syntax:

- Key: optional DNS-subdomain prefix plus `/`, then a required name segment.
- Name segment: 1-63 characters, alphanumeric at both ends, with `-`, `_`, and
  `.` allowed between.
- Value: 0-63 characters; when non-empty, it follows the same boundary and
  character rules as the key name segment.
- Kubernetes-reserved prefixes are not reserved by tacplus unless a future
  tacplus-owned prefix policy says otherwise.

---

## State System

### service_state

A message annotated with `(spec.service_state)` becomes a state root — a named partition of distributed state:

```protobuf
message MyServiceState {
  option (spec.service_state) = {tag: "myservice", export: true};

  Config config = 1;

  repeated Session sessions = 2 [(spec.indexed) = true];
}

message Session {
  string id = 1;
  // In a collection, the state store computes TTL = exp - now and auto-evicts
  // the entry on expiry. epoch_expiration is for int64 collection fields only.
  int64 expires_at = 2 [(spec.epoch_expiration) = true];
}
```

Every message-typed field of a state root is a **boundary** — stored independently
and convergent (lockless, last-writer-wins / append-only). The `tag` field routes
the state to its backing store service. The `export` flag makes the state available
for read-only import by other services.

tacplus generates:
- A **gRPC state service** with Get/Set/Watch/Delete RPCs for every boundary
- **state boundary bindings** for each boundary field
- **Typed client wrappers** so other services access state through generated accessors
- **Watch streams** — live subscriptions that push state changes to subscribers

### Boundary Types

Fields within a state root can be:

| Type | How to Declare | Behavior |
|------|----------------|----------|
| **Singleton** | Non-repeated field | One value per service. Singleton: a single value, no entry key. |
| **Collection** | Repeated field with `(spec.indexed)` on one sub-field | The indexed field is the entry key. |
| **Queue** | `(spec.queue) = true` | FIFO delivery. One consumer gets each item. |
| **Priority Queue** | `(spec.priority_queue) = "severity"` | Consumed in priority order. |
| **Topic** | `(spec.topic) = true` | Pub/sub. Every subscriber gets every message. |
| **View** | `(spec.view_sources)` or `(spec.view_member)` | Read-only union of multiple collections. |

### Cross-Service State Access

**1. The owning service exports its state:**

```protobuf
message OwnerState {
  option (spec.service_state) = {tag: "ownerservice", export: true};

  OwnerRecord current_record = 1;
}
```

**2. The consuming service imports the needed field at field level:**

```protobuf
message ConsumerState {
  option (spec.service_state) = {tag: "consumerservice"};

  ownerservice.OwnerRecord current_owner_record = 1 [
    (spec.state_import) = "ownerservice.OwnerState.current_record"
  ];
}
```

tacplus derives the imported state root from the field-level projection, then
generates read-only typed accessors (Get/Watch) for imported state. The
projection currently supports singular message fields. The consuming service
never gets Set/Delete — imports are always read-only. The
access is enforced server-side: the backing store validates the caller's token
against the proto-declared permissions. Writing a custom gRPC client to call
the raw Set RPC will fail.

### Views Across Boundaries

`view_member` aggregates collections across state roots into a single read-only view:

```protobuf
// In ServiceA's state
repeated jwk.Jwk verifying_keys = 1 [
  (spec.view_member) = "JwkVerifyingKeysView"
];

// In ServiceB's state
repeated jwk.Jwk verifying_keys = 1 [
  (spec.view_member) = "JwkVerifyingKeysView"
];
```

tacplus generates a `JwkVerifyingKeysViewService` with Watch/Get RPCs that unions both collections through a single Watch stream.

Views serve two purposes: they expose a single read surface of the same type across multiple producing services, and they let consumers bind against a typed interface without knowing specific implementation details of each producer. The same-type restriction is intentional — views are not arbitrary mashup layers.

### Persistence and Eviction

| Annotation | Effect |
|------------|--------|
| `(spec.persistence) = true` | Boundary is durably stored. Backing store snapshots the boundary state and restores on restart. |
| `(spec.eviction_ttl_seconds) = 120` | Entries expire if not refreshed. Used for per-node metrics that disappear when a node goes down. |
| `(spec.epoch_expiration) = true` | An int64 field inside a **collection item** is treated as a Unix epoch expiry timestamp; the state store computes TTL at write time. On a scalar state-root field it is rejected at generation time. On an int64 inside a singleton-only boundary message it is accepted but ignored (documented carve-out — see spec.proto). |
| `(spec.instance_keyed) = true` | Server composes the entry key from the caller's token claims (`service_id@instance_id`), enforcing per-instance write isolation. |

### Leader Election

Boundaries annotated with `(spec.leader) = true` participate in distributed leader election. Only the leader processes writes. Lease renewal and failover are generated.

### service_store

A message annotated with `service_store` becomes a durable key-value store backed by the state store with synchronous replication. Unlike `service_state` (convergent, Watch-based), `service_store` is:

- Write-with-quorum — writes acked only after replication to 2+ nodes
- Read-on-demand — no Watch streams, no client-side caching
- No boundary decomposition — the message IS the value

```protobuf
message CredPair {
  option (spec.service_store) = {tag: "myservice"};
  string client_id = 1 [(spec.store_key) = true];
  Jwk public_key = 2;
}
```

tacplus generates Put/Get/Delete/List RPCs.

### Quorum Writes

Fields annotated with `(spec.quorum_write) = true` require writes to be acked by 2+ nodes. On a single-node cluster, writes succeed immediately. Valid on fields within `service_state` or `service_store` messages.

### Commands

Fields annotated with `(spec.command) = true` become mutation RPCs on the state service. The field type is the request message; the response is always `google.protobuf.Empty`. Use `boundary_callers` to restrict which services can invoke the command cross-namespace.

### Automatic TLS (auto_tls)

State roots can opt into automatic TLS certificate lifecycle:

```protobuf
option (spec.service_state) = {tag: "myservice", auto_tls: TLS_KEY_TYPE_ED25519};
```

tacplus generates a bidi stream subscription to the token minting service's TLS cert endpoint. The service generates keypairs locally; the signing service delivers certs, triggering renewal at half-life.

### Event Bus Integration

State roots can declare event-bus topic subscriptions:

```protobuf
option (spec.subscribe) = {source: "producer.ProducerState.event_stream"};
```

And expose topic RPCs through the API gateway:

```protobuf
option (spec.service_event) = {tag: "eventbus", export: true};
```

### File-Level Service Identity

Every service proto declares its identity at file level:

```protobuf
option (spec.service) = {name: "identity", store: true};   // backing store service
option (spec.service) = {name: "gateway", gateway: true};  // API gateway (browser TLS)
option (spec.service) = {name: "dashboard"};               // application service
```

The `name` is the canonical service identity used across the mesh. `store: true` declares a backing store service. `gateway: true` changes TLS handling to use public certificates (for browser-facing TLS termination).

---

## Auth System

### Service-Level Auth Annotations

**`service_s2s`** — declares a service-to-service gRPC service. Only authenticated service identities can call these RPCs. The `auth_services` field lists which service identities are allowed:

```protobuf
service InternalAdmin {
  option (spec.service_s2s) = {auth_services: ["*"]};  // any authenticated service
}

service RestrictedAPI {
  option (spec.service_s2s) = {auth_services: ["serviceA", "serviceB"]};
}
```

**`service_c2s`** — declares a client-to-service gRPC service, exposed through the API gateway to end users. `auth_entitled_users` is the service-level default policy:

```protobuf
// Entitlement check applies to every RPC (the default C2S policy)
service Dashboard {
  option (spec.service_c2s) = {auth_entitled_users: true};
}
```

**`method_c2s`** — relaxes the service default for a single RPC. `auth_disabled` and `auth_any_user` are method-level only:

```protobuf
service Auth {
  option (spec.service_c2s) = {auth_entitled_users: true};

  // Public endpoint (no token required)
  rpc Discovery(Req) returns (Resp) {
    option (spec.method_c2s) = {auth_disabled: true};
  }

  // Any authenticated user, no entitlement check
  rpc Exchange(Req) returns (Resp) {
    option (spec.method_c2s) = {auth_any_user: true};
  }
}
```

**`method_s2s`** — overrides `auth_services` for a single RPC within a `service_s2s` service:

```protobuf
rpc SensitiveOperation(Req) returns (Resp) {
  option (spec.method_s2s) = {auth_services: ["serviceA", "serviceB"]};
}
```

### Entitlements Model

For `service_c2s` services with `auth_entitled_users: true`, user tokens carry
the generated `service_entitlements` claim. The claim has the shape of the
generated `entitlements.TokenEntitlements` proto: a flat `grants` list of
generated entitlement tokens, plus optional `azp` scoping.

```json
{
  "sub": "user@example.com",
  "aud": "myservice",
  "service_entitlements": {
    "grants": [
      "DASHBOARD_DASHBOARD_GET_DASHBOARD",
      "DASHBOARD_DASHBOARD_WATCH_DASHBOARD"
    ],
    "azp": ["webapp"]
  }
}
```

Generated grant helpers can expand package/capability wildcard tokens into
exact RPC grant tokens. Do not use a string wildcard such as `"*"` for generated
C2S authorization.

Tacplus now also emits governed RPC metadata (`entitlement_scopes` and
`capability_scope`) and carries a language-agnostic helper contract for
expanding package/capability wildcard grants into exact RPC grants. Generated
per-language helpers expand exact and wildcard grant tokens, and generated
runtime authorizers normalize `service_entitlements.grants` through those
helpers before checking the exact package/service/RPC grant for a call.

### Identity Profiles

Identity profiles are named sets of entitlements. The token minting service resolves profile names to concrete RPCs during token minting.

```protobuf
message IdentityProfile {
  string id = 1;                               // e.g., "admin", "viewer"
  repeated ServiceEntitlement entitlements = 2; // service_name + allowed RPC enum values
}
```

### Service-to-Service Auth

S2S authentication uses credential pairs (Ed25519 key pairs). Each service:

1. Has a public/private Ed25519 key pair
2. Signs a bootstrap JWT with its private key (`sub = service_name`, `kid = public_key.kid`)
3. Presents the bootstrap JWT to the token minting service
4. Receives refresh + access tokens for subsequent S2S calls

The token minting service validates the signature against the registered public key and maps the `kid` to a service identity via `ServiceEntitlementMapping`.

For services running in cloud environments (AWS, GCP, Azure), cloud-native identity mechanisms (SigV4-presigned STS requests, OIDC identity tokens) replace credential pairs.

---

## Record / Event / Store / Projection Annotations

These annotations wire the eventbus, the record stores, and their projections.
The authoritative, exhaustive contract is in
[`RECORD_EVENT_MODEL.md`](RECORD_EVENT_MODEL.md); the hand-written surface is:

**Message-level:**

| Annotation | Meaning |
|------------|---------|
| `(spec.event) = {c2s?, s2s?}` | eventbus topic + publish RPC (at least one producer surface required) |
| `(spec.record) = {key, owner, c2s?, s2s?}` | an `event` that is also stored in `record_store` and is projectable — `key` is the id field, `owner` the ownership field |
| `(spec.graph_projection) = {…}` | derive a graph node from this record (requires `record`) → graph store |
| `(spec.timeseries_projection) = {…}` | derive a timeseries row from this record (requires `record`) → timeseries store |

**Field-level (on a `record` message's fields):**

| Annotation | Meaning |
|------------|---------|
| `(spec.query_field) = {op: EQ\|RANGE}` | expose the field as a typed filter in the RecordMap query plane |
| `(spec.redact) = {mode: DROP\|PSEUDONYMIZE\|MAY_REDACT\|MAY_ANONYMIZE}` | marks a field for **data entitlement** (see below). DROP/PSEUDONYMIZE always apply (hard floor); MAY_REDACT/MAY_ANONYMIZE let the caller decide at egress. Valid on scalars, message-typed fields (redact clears the whole sub-message), and repeated fields; ANONYMIZE requires string/bytes |

#### Data entitlement (egress redaction)

Data entitlement sanitizes a record on its way out of a service. It is a lattice —
`pass-through < anonymize < redact` — where the effective transform for a field is
`max(annotation floor, caller level)`: the caller can only ratchet a field *tighter*.

- **Four annotations.** `DROP` (always cleared), `PSEUDONYMIZE` (always keyed-HMAC'd),
  `MAY_REDACT` / `MAY_ANONYMIZE` (the caller decides per call via an entitlement).
- **Recursive.** A message-typed field can be redacted as a whole (the entire
  sub-message is cleared, cascading to everything beneath it) or field-by-field,
  to any depth.
- **Generated surface.** For each record `T`, tacplus generates the proto message
  `<T>Entitlement` (a per-field `EntitlementLevel` the caller sets, recursing into
  nested `<Sub>Entitlement`) and, in the full-service languages (Go/Rust/Java), the
  transform `Sanitize<T>(m, ent, hmacKey)` returning a redacted copy.
- **Redacted indicator.** Because a redacted scalar is indistinguishable from a
  genuine zero, tacplus injects a companion `<field>_redacted` bool (a stable high
  field number) into the record; `Sanitize` sets it true when it redacts the field.
- **Anonymize** is a keyed HMAC-SHA256 — the same value+key yields the same surrogate
  (so pseudonymized records still join) but cannot be reversed without the key.
- **Format-preserving.** A field with a format constraint anonymizes to a valid
  value of that format: `(spec.assert_email)` → a synthetic email, `assert_ipaddr_type`
  IPV4/IPV6 → a valid IP (private / documentation range), `assert_fqdn` → a valid
  hostname. Fields without a format constraint get the `pseud:<hex>` token.

**Service-level (once per service):**

| Annotation | Service |
|------------|---------|
| `(spec.record_store) = {}` | stratum — the full-record store |
| `(spec.graph_store) = {}` | graph database |
| `(spec.timeseries_store) = {}` | timeseries database |

Example — a stored, projectable record:

```protobuf
message SensorEvent {
  option (spec.record) = {key: "event_id", owner: "email", s2s: {auth_services: ["argus"]}};
  string event_id = 1;
  string email = 2 [(spec.query_field) = {op: EQ}];
  int64 timestamp_ms = 3 [(spec.query_field) = {op: RANGE}];
}
```

You write only the annotations above. The projection **output** messages and
their internal `graph_type` / `timeseries_type` markers, the eventbus topic +
publish RPCs, the per-store subscriptions, and the query plane are all
**generated** — never hand-written. The read surface of a `record_store` is the
generated refinable-query plane (Open/Refine/Count/Sample/Pull/Watch/Close over
record keys), not a hand-written subscription handler.

---

## registry.json and the Semantic IR

tacplus generates a canonical `registry.json` that acts as the normalized semantic model of the service.

`registry.json` includes, in normalized form:
- Services, methods, types
- State roots and views
- Labels and ownership relationships
- Auth metadata and generation intent
- RPC verb, noun, pattern, access-kind, collection, retry policy, capability,
  handler shape, entitlement-scope, and governed dictionary metadata
- A root-level `entitlement_grants` catalog for entitlement-checked C2S methods:
  exact package/service/RPC grants plus package and capability wildcards with
  explicit `expands_to` lists
- Architectural norms from embedded `agent.md` files

Proto files are the authored source. `registry.json` is the compiled semantic
model. Consumer tooling that needs tacplus semantics should read
`registry.json`, not parse proto files directly.

Wildcard entitlement grants are convenience inputs for policy, operators, and
UI. Consumers should expand them to the exact package/service/RPC grants listed
in `entitlement_grants.expands_to` before making an authorization decision.

### Runtime Discovery

When served from a well-known REST endpoint (e.g., by an API gateway), `registry.json` makes the semantic model available at both:
- **Build time** — for generation, linting, testing, docs, and analysis
- **Runtime** — for discovery, tooling, agents, and system introspection

### TypeID / RpcID Enums

After generation, each processed proto file gets injected enums:

```protobuf
enum TypeID {
  TYPE_ID_UNSPECIFIED = 0;
  TYPE_ID_MY_MESSAGE = 1;
  // ... one per message
}

enum RpcID {
  RPC_ID_UNSPECIFIED = 0;
  RPC_ID_MY_SERVICE_MY_METHOD = 1;
  // ... one per RPC method
}
```

These stable numeric IDs are used in the per-proto registry for O(1) type and RPC lookups.

### Norms and agent.md

`agent.md` files are embedded into `registry.json`. The agentic analysis pass checks for cross-language parity drift, suspicious service shapes, and architectural anti-patterns. This is governance carried in machine-readable form.

---

## Generated Libraries

tacplus generates runtime libraries from templates. Full-service targets get
language-appropriate implementations of:

| Library | Purpose |
|---------|---------|
| `servicemanager` | The ServiceManager (`sm`): top-level runtime surface wiring config, clients, state, and health |
| `grpcserver` | gRPC + HTTP server with TLS, auth interceptor, static file serving |
| `serviceconnection` | Managed gRPC endpoint connections (go/java) |
| `service_im` | Service identity manager: acquires the service's own identity (credential pair or cloud-native), mints and refreshes S2S tokens, and pins the bootstrap trust domain |
| `token` | JWT creation, parsing, signature verification |
| `jwk` | JWK key management, immutable key wrapper, key rotation |
| `key` | Ed25519 key generation and management |
| `x509` | X.509 certificate and CA operations |
| `spiffe` | SPIFFE ID parsing/construction and trust-domain helpers |
| `logger` | Structured logging with dynamic level control |
| `grpcconfig` | Runtime config loading from shared state |
| `healthreporter` | Periodic health/metrics publishing to `ServiceHealthState` |
| `httpclient` | Typed HTTP client (CA/mTLS, timeouts, retry) with proto-validated responses |
| `diskstore` | Durable disk-backed snapshot store (UC1 self-persistence) |
| `collection` | Keyset-pagination (PAGE) collection helpers |
| `saml` | SAML request/response assertion helpers |
| `ip` | IP address parsing and validation helpers |
| `tags` | Kubernetes-style tag sets and selectors |
| `am` | Typed accounting event helpers and claim projection support |
| `jwkview` | Watch client for signing key views (go only) |

These are not generic utility libraries. Each is generated with the service's module path baked in and the correct proto imports resolved.

Identity is split into two managers (D-4). `service_im` (above) is emitted for
full-service targets and owns the service's *own* identity. `client_im` is
emitted wherever a client surface is generated — full-client (`python`,
`typescript`) and lite client (`swift`, `csharp`) targets included — and owns
the token lifecycle for non-service code: CLI tools and daemons that
authenticate as users (OAuth) or via credential pairs.

Client-only targets do not have a supported service runtime library surface.
Accepted builds should use only the generated C2D/C2S/W2S client helpers, the
`client_im` identity manager, and any client-side primitives they import.

---

## Service Lifecycle

When tacplus processes a full-service target whose proto definitions declare
services and state, it generates a service entry point and a service-to-service
manager (`s2sm`). The hand-written code is minimal:

```go
// internal/handler.go -- the ONLY hand-written file (Go example): implement the
// generated Handler interface, then run the manager.
func NewHandler(ctx context.Context, sm *s2sm.ServiceManager) error {
    return sm.Run(&myServer{sm: sm})
}
```

The equivalent service runtime exists in the full-service targets (`go`, `rust`,
`java`). The generated `ServiceManager` handles:

1. Connect to the backing store cluster
2. Subscribe to signing key views (for token verification)
3. Fetch runtime config from `ServiceConfigState`
4. Create the Authorizer (token signature verification + entitlement checking)
5. Start the gRPC server with TLS and auth interceptor
6. Start the health reporter (periodic metrics publishing)
7. Call the hand-written entry point

The generated entry point is ~20 lines in any language: parse flags, create `ServiceManager`, call `Run()`.

Hand-written code is allowed only where platform law intentionally ends.

---

## Distributed Tracing

The `trace_id` infrastructure field (field 200) is present on every proto message. When a service receives a request that contains a non-empty `trace_id`, it **must** copy that value into every outgoing RPC it makes in the course of fulfilling that request. The generated wrapper has no visibility into your handler's downstream calls — propagation is the implementer's responsibility.

This applies across all service boundaries: if an external request arrives with a `trace_id` and your handler calls another internal service, that internal call must carry the same `trace_id`.

### Required Pattern

```go
// Go
func (s *myServiceImpl) HandleRequest(ctx context.Context, req *pb.MyRequest) (*pb.MyResponse, error) {
    downstream := &otherpb.OtherRequest{
        // ... business fields ...
        TraceId: req.TraceId,  // propagate — always copy if non-empty
    }
    return s.sm.Grpc().Clients().Other().OtherApiClient().DoSomething(ctx, downstream)
}
```

```rust
// Rust
async fn handle_request(&self, req: MyRequest) -> Result<MyResponse, ProtoError> {
    let downstream = OtherRequest {
        trace_id: req.trace_id.clone(),  // propagate
        ..Default::default()
    };
    self.sm.s2s().other().other_api_client().do_something(downstream).await
}
```

The same pattern applies in Java, TypeScript, and Python. Omitting the `trace_id` on any downstream call breaks the trace chain for that span.
