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
| `pkg` | string | No | Canonical package name (Java-style reverse DNS, e.g. `com.example.myservice`). Each segment must be a valid identifier (letters, digits, underscores — no hyphens). Converted to native format per language (see table below). |

#### Package Name Conversion

Given a canonical `pkg` (e.g. `com.example.myservice`) and `output-dir` name
(default `model`), the native package/module/crate name is derived per language:

| Language | Rule | Example (`com.example.myservice`, output `model`) |
|----------|------|---------------------------------------------------|
| java | `pkg.output` | `com.example.myservice.model` |
| go | reverse TLD to host, `/`-join rest, append output | `example.com/myservice/model` |
| rust | last segment + `-` + output | `myservice-model` |
| typescript | last segment + `-` + output | `myservice-model` |
| python | last segment + `_` + output | `myservice_model` |
| swift | PascalCase(last segment) + PascalCase(output) | `MyserviceModel` |
| csharp | PascalCase(last segment) + PascalCase(output) | `MyserviceModel` |

Swift and C# use the same PascalCase folding: segments split on `-`, `_`, and
`.` are title-cased and concatenated. This means distinct canonical packages
that differ only in segments before the last (e.g. `com.a.identity` vs
`com.b.identity`) produce **the same** native name for rust/ts/python/swift/csharp.
Proto package names must be globally unique; choose last segments that are
unique across the system to avoid collisions in these languages.
| `src` | string | No | Primary proto source — local path or git URL. No default — must be supplied via `--src` or `tacplus.yaml`. In a GitLab CI job an `https://` URL on `CI_SERVER_HOST` is fetched with `CI_JOB_TOKEN`; no other host receives a credential. |
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
  surfaces. Swift's W2S carries unary methods over HTTP/JSON and
  server-streaming methods over SSE (as an `AsyncThrowingStream`, with no
  deadline — cancel the enclosing `Task` to end it). C#'s W2S is unary-only.
  A streaming method the target cannot carry — client-streaming and bidi for
  Swift, which the gateway serves over WebSocket, and every streaming method
  for C# — is emitted as a **compile-time-unavailable stub**, not omitted, so a
  mixed unary+streaming service still produces a compilable client and no
  declared method silently disappears. Use the Go/Java/Rust W2S client for
  those. Unlike the full-client tier they currently emit no client-side request
  validation and no default request deadline. Any service-side surface is
  rejected loudly.

Full-service targets generate a `c2dm` manager for services marked with
`(spec.service_c2d)`, with typed gRPC clients over local daemon IPC. Go and Rust
dial a Unix-domain socket. Java accepts a caller-supplied local-daemon
`ManagedChannel` so the embedding app chooses the local IPC adapter.

Swift and C# emit lite client packages that include a C2D surface: Swift emits a
SwiftPM package with a Unix-domain socket `C2DClient` helper, while C# emits a
.NET package that defaults to named pipes on Windows and Unix-domain sockets
elsewhere. They also emit C2S and W2S client surfaces (Swift's W2S adds server-streaming
over SSE; C#'s is unary-only). Python
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

### How `pkg` Becomes A Native Package Name

`pkg` is a canonical reverse-DNS name (`com.example.identity`). Each target
language renders it differently, with the `output-dir` name appended as the
generated-model segment (`model` below):

| Language | Transform | `com.example.identity` → |
|---|---|---|
| java | canonical + `.` + output name | `com.example.identity.model` |
| go | TLD and host swapped, rest joined with `/` | `example.com/identity/model` |
| rust, typescript | **last segment** + `-` + output name | `identity-model` |
| python | **last segment** + `_` + output name | `identity_model` |
| swift, csharp | **last segment**, PascalCased, + PascalCased output name | `IdentityModel` |

Only java and go carry the whole canonical name. The other five keep the last
segment alone, and swift/csharp additionally fold `-`, `_` and `.` away, so
`com.example.identity`, `com.other.identity` and `com.other.ident_ity` all render
as the same swift module. tacplus cannot detect that: one build sees one `pkg`.
Distinct services must therefore differ in the LAST segment of `pkg`, not only in
its prefix — the D-5 convention that proto package names are globally unique
applies to that segment.

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
| `tacplus registry` | Update the service / vservice / trait registry from proto definitions |
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

A c2s RPC whose REST route is a browser form target, such as an identity
provider's `form_post` callback, declares `rest_browser`:

```protobuf
rpc BrokerFormPostCallback(FormPostCallbackRequest) returns (FormPostCallbackResponse) {
  option (spec.rest_browser) = {form_body: true, redirect_field: "redirect_url"};
}
```

- `form_body` makes the route accept an `application/x-www-form-urlencoded`
  body as well as JSON. Each pair becomes a string value keyed by proto field
  name. A key sent twice gets a 400.
- `redirect_field` names a singular string field of the response. The route
  answers `303 See Other` with `Location` set to that field and
  `Cache-Control: no-store`, instead of JSON.

Both are emitted into the generated route table (Rust
`rest_dispatch::RouteInfo` `form_body` / `redirect_field`, Go
`restdispatch.RouteInfo` `FormBody` / `RedirectField`, Java
`RestDispatch.RouteInfo` `formBody` / `redirectField`; the redirect field is
spelled as its proto3 JSON name in all three) and carried out by its
`form_body_to_json` and `redirect_response` (Rust), `FormBodyToJSON` and
`RedirectResponse` (Go) or `formBodyToJson` and `redirectResponse` (Java),
which a REST host calls. Generation refuses the option on a streaming RPC, on
an RPC with no c2s REST route, and when `redirect_field` is missing from the
response or is not a singular string.

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
  option (spec.service_state) = {label: ["myservice"], export: true};

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
and convergent (lockless, last-writer-wins / append-only). The repeated `label`
field routes the state to its backing store services. The `export` flag makes the state available
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
  option (spec.service_state) = {label: ["ownerservice"], export: true};

  OwnerRecord current_record = 1;
}
```

**2. The consuming service imports the needed field at field level:**

```protobuf
message ConsumerState {
  option (spec.service_state) = {label: ["consumerservice"]};

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
| `(spec.persistence) = true` | Requests boundary durability. **No store implements it today** — kernel is in-memory coordination and holds no durable user data, and generated state clients write nothing to local disk. On a **kernel-labelled** state root it is a generation error: *durable data belongs in stored records* (use `(spec.record)` + `(spec.record_store)`). Elsewhere it is accepted and carried descriptively to `registry.json` (`BoundaryInfo.persistence`) for a future store to act on. Unrelated to typed disk persistence, which is keyed off `(spec.service_store)`. |
| `(spec.eviction_max_size) = 5000` | Bounds the number of entries the generated Go state wrapper will accept for the boundary. |
| `(spec.epoch_expiration) = true` | An int64 field inside a **collection item** is treated as a Unix epoch expiry timestamp; the state store computes TTL at write time. The future and within-a-year check runs when the value is written or accepted on the wire, not when a stored copy is loaded from disk: stored data ages, so a restarted service loads an expired record and its owner retires it. On a scalar state-root field it is rejected at generation time. On an int64 inside a singleton-only boundary message it is accepted but ignored (documented carve-out — see spec.proto). |
| `(spec.instance_keyed) = true` | Server composes the entry key from the caller's token claims (`service_id@instance_id`), enforcing per-instance write isolation. |

`(spec.eviction_ttl_seconds)`, `(spec.eviction_max_idle_seconds)`, `(spec.eviction_policy)`, `(spec.eviction_max_size_policy)`, `(spec.sync_backup_count)` and `(spec.async_backup_count)` (and the `state_*` message-level defaults for the same three concerns) are declared in `spec.proto` but **not implemented** — no generator reads them. Generation rejects them rather than accept a retention or durability intent it will not honour. For per-entry expiry use `(spec.epoch_expiration)` on an int64 inside the collection item.

### Leader Election

Boundaries annotated with `(spec.leader) = true` participate in distributed leader election. Only the leader processes writes. Lease renewal and failover are generated.

### service_store

A message annotated with `service_store` becomes a durable key-value store backed by the state store with synchronous replication. Unlike `service_state` (convergent, Watch-based), `service_store` is:

- Write-with-quorum — writes acked only after replication to 2+ nodes
- Read-on-demand — no Watch streams, no client-side caching
- No boundary decomposition — the message IS the value

```protobuf
message CredPair {
  option (spec.service_store) = {label: ["myservice"]};
  string client_id = 1 [(spec.store_key) = true];
  Jwk public_key = 2;
}
```

tacplus generates Put/Get/Delete/List RPCs.

### Quorum Writes

Fields annotated with `(spec.quorum_write) = true` require a write to be durable on `(spec.min_durable_copies)` distinct nodes (default 2) before it is acked. Below that floor the write is refused with FAILED_PRECONDITION, never degraded; a single-node development tree declares `min_durable_copies: 1`. Valid on fields within `service_state` or `service_store` messages.

For a `service_store`, the generated disk helpers carry the store's floor (the strictest declared on any of its fields, since a row is written whole) and a gate the store's write path calls with the number of durable copies it can reach: `MinDurableCopies<Type>` / `Require<Type>Quorum(n)` (Go), `MIN_DURABLE_COPIES_<TYPE>` / `require<Type>Quorum(n)` (Java), `MIN_DURABLE_COPIES_<TYPE>` / `require_<type>_quorum(n)` (Rust). A store whose rows are partitioned across a cluster narrows its disk-log roll baseline to its own rows by implementing `diskstore.LocalRows` (Go, Java), or by overriding `Handler::<store>_local_rows` (Rust).

### Commands

Fields annotated with `(spec.command) = true` become mutation RPCs on the state service. The field type is the request message; the response is always `google.protobuf.Empty`. Use `boundary_callers` to restrict which services can invoke the command cross-namespace.

### Automatic TLS (auto_tls)

State roots can opt into automatic TLS certificate lifecycle:

```protobuf
option (spec.service_state) = {label: ["myservice"], auto_tls: TLS_KEY_TYPE_ED25519};
```

tacplus generates a bidi stream subscription to the token minting service's TLS cert endpoint. The service generates keypairs locally; the signing service delivers certs, triggering renewal at half-life.

### Event Bus Integration

State roots can declare event-bus topic subscriptions:

```protobuf
option (spec.subscribe) = {source: "producer.ProducerState.event_stream"};
```

And expose topic RPCs through the API gateway:

```protobuf
option (spec.service_event) = {label: "eventbus", export: true};
```

### File-Level Service Identity

Every service proto declares its identity at file level:

```protobuf
option (spec.service) = {name: "identity", store: true};   // backing store service
option (spec.service) = {name: "gateway", gateway: true};  // API gateway (browser TLS)
option (spec.service) = {name: "dashboard"};               // application service
```

The `name` is the canonical service identity used across the mesh. `store: true` declares a backing store service. `gateway: true` changes TLS handling to use public certificates (for browser-facing TLS termination).

### File-Level Contract Identity (vservice, trait)

A proto file can instead declare a **contract** — not a process, but a named surface
that some physical service implements and that callers are entitled against by that
name. Two options carry the same identity shape (a canonical name used as the
entitlement key, plus an optional `id` — the stable `TokenEntitlements` field
number for that contract):

```protobuf
option (spec.vservice) = {id: 30, name: "telemetry"};   // a DATA contract
```

```protobuf
option (spec.trait) = {                                     // an RPC contract
  name: "query"              // entitlement key AND the audience callers authenticate against
  host: "recordstore"            // routing label of the physical implementor
  s2s_service: "RecordMap"   // emitted peer-facing service name
  c2s_service: "RecordQuery" // emitted client-facing service name
  auth_services: ["argus"]   // peer identities allowed onto the S2S half
};
```

`host` becomes the `(spec.service_s2s)` / `(spec.service_c2s)` `label` on the emitted
services, so the gateway dials the host — but the audience the caller's token is
checked against is `name`, never the host label. **The audience is the contract, not
the implementor**, so a second implementor of the same trait needs no client change.
A `label` that no loaded service serves fails generation, and so does a `label` on a
file that declares no trait.

Everything the surface needs is in the declaration rather than in generator source:
delete the file and the surface is not generated at all. `query` is the shipped
example — it asks the record store for the record query plane; see
[`RECORD_EVENT_MODEL.md`](RECORD_EVENT_MODEL.md).

---

## Auth System

### Service-Level Auth Annotations

**`service_s2s`** — declares a service-to-service gRPC service. Only authenticated service identities can call these RPCs. `label` names the physical service hosting a vservice or trait interface:

```protobuf
service InternalAdmin {
  option (spec.service_s2s) = {};
}

service RecordStoreHosted {
  option (spec.service_s2s) = {label: "recordstore"};
}
```

**`service_c2s`** — declares a client-to-service gRPC service, exposed through the API gateway to end users. `auth_entitled_users` is the service-level default policy. `label` is the twin of the `service_s2s` one — it names the physical host for a trait's client-facing surface, while the audience the gateway checks the caller's token against stays the file's `(spec.trait).name`:

```protobuf
// Entitlement check applies to every RPC (the default C2S policy)
service Dashboard {
  option (spec.service_c2s) = {auth_entitled_users: true};
}

// A trait's client-facing surface: dialed at the record store, entitled against the trait
service RecordQuery {
  option (spec.service_c2s) = {auth_entitled_users: true, label: "recordstore"};
}
```

A service may declare only **one** caller surface (`ValidateServiceSurfaces`), so a contract that must serve both peers and users is two sibling services — which is why the `query` trait declares both an `s2s_service` and a `c2s_service`.

**`rpc`** — declares one RPC's call surface: who calls it (`principal`), where it
is reachable from (`exposure`), its non-exclusive protocol `flags`, the service
identities allowed to call it (`auth_services`), and the request field carrying
the target namespace (`target_namespace_field`). Everything that can legitimately
differ between two RPCs of the same service lives here:

```protobuf
service Auth {
  option (spec.service_c2s) = {auth_entitled_users: true};

  // Public endpoint (no token required at all)
  rpc Discovery(Req) returns (Resp) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_USER, exposure: RPC_EXPOSURE_EXTERNAL, flags: [RPC_FLAG_PUBLIC_NO_AUTH]};
  }

  // Any authenticated user, no entitlement check
  rpc Exchange(Req) returns (Resp) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_USER, exposure: RPC_EXPOSURE_EXTERNAL, flags: [RPC_FLAG_AUTH_ANY_USER]};
  }
}

service RestrictedAPI {
  option (spec.service_s2s) = {};

  // Only these two service identities may call
  rpc SensitiveOperation(Req) returns (Resp) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: ["serviceA", "serviceB"]};
  }

  // Authenticates the caller from the request body, so no bearer token
  rpc Bootstrap(Req) returns (Resp) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, flags: [RPC_FLAG_SELF_AUTHENTICATES], auth_services: ["*"]};
  }
}
```

**One rpc for users AND listed services.** `auth_services` on a user rpc of a
`service_c2s` service adds those service identities to the users it already
admits. It is still one rpc on one surface, with one handler:

```protobuf
service Chat {
  option (spec.service_c2s) = {auth_entitled_users: true};

  // Entitled users post as today; the "assistant" service may post too.
  rpc CreateChatMessage(Req) returns (Resp) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_USER, flags: [RPC_FLAG_AUTH_ENTITLED_USERS], auth_services: ["assistant"]};
  }
}
```

A user token is authorized by its entitlements, a service token by
`auth_services` (matched against the token's location-agnostic service path, so
`"assistant"` matches every tenant's `assistant`), and an unlisted service is refused.
`"*"` admits service callers only. An ephemeral workload (a pigeon, minted as
`<service>/workloads/pigeon-<run_id>`) is admitted only by a pattern that names
`/workloads/`. Each generated server language exports the pattern for any pigeon,
`PIGEON_SERVICE_PATH` (Go `PigeonServicePath`, Rust/Python/Java/TypeScript
`PIGEON_SERVICE_PATH`), and a function that scopes it to one parent service:
`PigeonServicePathFor(service)` in Go, `pigeon_service_path_for(service)` in Rust and
Python, `pigeonServicePathFor(service)` in Java and TypeScript.
The calling service declares `option (spec.grpc_s2s_client) = {service:
"pkg.Chat"};` and uses the generated client. The handler's `Caller` is the
verified token's identity, so `(spec.principal)` / `(spec.namespace)` request
fields bind to the service's SPIFFE ID and its namespace, never to a value the
caller chose.

**A tenant-owned instance refuses another tenant at the door.** A token's `aud`
is the service name, which every tenant's instance of a `tenant_owned` service
accepts, so `target_namespace_field` (which only exists on RPCs that name a
target) cannot be the only tenant check. Every generated server language reads
the namespace of the instance's own service token at startup; when it is exactly
`/tenants/<t>`, the generated Authorizer refuses with PERMISSION_DENIED, before
any handler runs, every caller whose namespace is not at-or-above `/tenants/<t>`
and not `/system` or below (fleet-wide readers keep passing). A shared service's
instance, running in `/system`, is unchanged. An instance that cannot read its
own identity at startup fails to start rather than serve unpinned.

### Every user RPC is also an MCP tool

**An rpc on a `(spec.service_c2s)` service is published as an MCP tool
automatically.** No annotation asks for it. The predicate is:

> a c2s rpc that is not `RPC_PRINCIPAL_SERVICE`, minus `RPC_FLAG_PUBLIC_NO_AUTH`
> and `RPC_FLAG_SELF_AUTHENTICATES`

— that is, an rpc whose caller is an authenticated human. An rpc with **no
`(spec.rpc)` at all**, user-facing only by the service's `auth_entitled_users`,
is the common case and is auto-exposed like any other. Token-minting
(`RPC_FLAG_SELF_AUTHENTICATES`) and public no-auth endpoints never are: the
first mints credentials, the second has no caller identity to scope a tool call
by.

There is deliberately **no opt-out annotation**. A service's whole user surface
is its tool catalogue, and catalogue size is the MCP proxy's
progressive-disclosure problem, not a filter configured here.

Auto-exposure is **additive**. The rpc keeps its gRPC call, its REST route, its
verb classification and its entitlement grant, and the tool shares the handler
method the generated service wrapper already calls — so a service author writes
nothing extra, and the tool cannot drift from the call.

Two shapes are excluded from auto-exposure rather than refused, because
auto-exposure is not a request the author made:

- **streaming**, on either side — a tool call is one request and one response,
  and the generated session carrier is itself a bidi rpc on its own service;
- **`(spec.method_collection)` in any mode but `PAGE`** — a SNAPSHOT read hands
  the handler a materialized collection, a cursor or a stream, none of which a
  tool call has a frame for. `PAGE` **is** auto-exposed; see below.

Declaring `RPC_PRINCIPAL_MCP` on either shape is still a hard generation
failure, because there the tool *was* asked for.

#### A `PAGE` read is a paged tool

A `COLLECTION_MODE_PAGE` rpc is published as a tool like any other user rpc, and
the model drives the pager itself — the same way any other client of the gRPC
call does. Nothing new is annotated and nothing is truncated:

- **`page_size`** and **`page_token`** are already ordinary fields of a PAGE
  request (collection validation requires them), so they are ordinary tool
  arguments. The generated schema states the window: `page_size` carries the
  rpc's resolved `default_page_size` in its description and the trust tier's
  ceiling as its JSON Schema `maximum`. Both are optional.
- The result is the page's items plus **`next_page_token`**, exactly the
  response message the gRPC call returns. An empty `next_page_token` means there
  is no next page; `total_count` is carried when the response declares it.
- The dispatch calls the **same three-argument handler** the gRPC PAGE wrapper
  calls — `(caller, request, PageParams)` — building `PageParams` from the tool
  call's `page_size`/`page_token` with the same token codec and the same
  trust-tier clamp. A malformed `page_token` is `INVALID_ARGUMENT`, never a
  silent reset to page 1, and the `(spec.store_principal)` /
  `(spec.store_namespace)` egress filters run over the page exactly as they do
  on the gRPC path.

So a service author who writes a paged read writes nothing else: the page is
paged on both surfaces, off one handler.

Because carrier injection is a proto-text operation it is language-blind, so
tacplus refuses (**MCP-6**) to generate a service that owns tools for a language
that emits a service wrapper but has no MCP dispatch backend — today Python.
Go, Java and Rust have the backend. TypeScript, Swift and C# are client-only:
they emit no wrapper at all, cannot host, and are not gated — a TypeScript client
of a tool-owning service (the webapp) generates normally.

### `RPC_PRINCIPAL_MCP`: a tool with no gRPC twin

`RPC_PRINCIPAL_MCP` remains, for the tool that has no rpc of its own: the rpc
declares an **MCP tool** and nothing else. tacplus generates its schema and a
handler interface method the service implements by hand, and maps it to no gRPC
RPC — what the handler body does (call its own RPC, call another service, fold a
Watch into one snapshot, nothing) is ordinary application code. Because
`principal` holds one value, such a tool is never *also* a gRPC call; a service
wanting both surfaces for related functionality declares two rpcs, or — far more
often now — just writes the user RPC and lets auto-exposure publish it.

```protobuf
service Tickets {
  option (spec.service_c2s) = {auth_entitled_users: true};

  // GetTicketBrief summarizes one ticket for a model.
  rpc GetTicketBrief(GetTicketRequest) returns (GetTicketResponse) {
    option (spec.rpc) = {
      principal: RPC_PRINCIPAL_MCP,
      exposure: RPC_EXPOSURE_EXTERNAL,
      flags: [RPC_FLAG_AUTH_ENTITLED_USERS]
    };
  }
}
```

Everything else is reuse, deliberately:

- **Authorization is unchanged.** `flags` / `auth_services` /
  `target_namespace_field` mean the same thing against whatever `Caller` the MCP
  session captured at handshake as they do against a gRPC caller. There are no
  MCP-specific authorization fields, because two independently-maintained
  authorization surfaces are exactly what drifts.
- **`(spec.principal)` / `(spec.store_principal)` on the request message are
  honored identically.** That is the mechanism behind "the owner predicate is
  server-computed and never an LLM-writable tool argument": the wrapper stamps
  the field from the caller and refuses a request that supplied a different one.
- **The tool name and description come from the rpc itself** — name = the RPC
  name in snake_case (`GetTicketBrief` -> `get_ticket_brief`), description = the
  rpc's own leading `//` doc comment — so there is no second description to drift
  from the real one. This is true of an auto-exposed user RPC too: its doc
  comment is what the model reads to decide whether to call it, so a c2s rpc
  worth calling is worth a sentence. `mcp_tool_name` overrides only the name, for
  the rare RPC whose name makes a bad tool name; it must be snake_case, and
  declaring it on an rpc that publishes no tool at all fails generation rather
  than being ignored.
- **Streaming is a hard generation failure**, unconditionally: a tool call has no
  wire representation for a stream. A handler may drive a `Watch` internally and
  fold it into one response.
- **The session-carrying transport is never declared.** A service owning at least
  one tool — declared or auto-exposed — is what generates it; its exposure is the
  most-permissive `exposure` across those rpcs. Under auto-exposure that means in
  practice every c2s service gets a carrier, an MCP SDK dependency and an
  entitlement grant for it. The registry records both as
  `ServiceInfo.mcp` and `ServiceInfo.mcp_exposure`.

  The transport itself is one extra rpc on that service,
  `Broker<Service>McpSession`, marked `mcp_session_carrier` in the registry.
  tacplus does not synthesize it in the model: it injects a real declaration

  ```proto
  rpc Broker<Service>McpSession(stream mcp.McpRequest) returns (stream mcp.McpResponse) {
    option (spec.rpc) = {
      principal: RPC_PRINCIPAL_USER,
      exposure: <the service's mcp_exposure>,
      flags: [RPC_FLAG_AUTH_ENTITLED_USERS]
    };
  }
  ```

  into the service's own proto before protoc runs. So it is an ordinary rpc
  everywhere downstream: a real `grpc_path`, the hosting service's `auth_model`,
  a REST route, an entitlement grant, an authorizing server wrapper, a method on
  the tonic trait and on grpc-java's `ImplBase`, and clients in every language —
  none of it MCP-specific. The frames are the shared sys types `mcp.McpRequest`
  and `mcp.McpResponse` (`sys/mcp.proto`), each one complete JSON-RPC 2.0 message
  in a `payload` string, bounded at 1 MiB in and 4 MB out.

  **In Rust, tacplus generates the whole termination above that rpc** and the
  carrier is therefore NOT a handler method: the generated service wrapper
  authorizes the session exactly as it authorizes any bidi rpc, refuses one with
  no verified caller, and hands the stream to a generated `rmcp` worker
  transport. What the service implements is only the tools, as ordinary typed
  handler methods. Alongside the wrapper the Rust generator emits, per service:

  - `<SERVICE>_MCP_TOOLS` — the published tool set: name, the rpc's own doc
    comment, and a JSON Schema derived from the request message. Fields the
    server computes are ABSENT from that schema rather than merely ignored —
    `(spec.principal)`, `(spec.store_principal)`, `(spec.namespace)`,
    `(spec.store_namespace)`, and the injected `trace_id`/`noop` pair — and a
    tool call that names one is refused.
  - `<Service>McpSurface` — an `rmcp::ServerHandler` answering `tools/list` and
    `tools/call`. A call decodes into the rpc's own typed request, runs the same
    request validation, principal/namespace stamps and egress row filters the
    gRPC wrapper runs, dispatches to `<Service>Handler::<rpc>`, and encodes the
    typed response as the tool result.
  - `serve_<service>_mcp_session(...)` and its worker — one MCP session over one
    gRPC stream, bounding outbound frames at `mcp.McpResponse.payload`'s
    `(spec.assert_max_size)` so an over-size tool result fails that one request
    instead of the whole session.
  - a `#[cfg(test)]` module asserting the tool set is published, that a tool call
    reaches its typed handler with the arguments decoded, and that a
    client-supplied server-computed field is refused.

  The Rust model crate depends on `rmcp` only when a service in the build
  declares an MCP tool.

  **In Go, tacplus generates the same termination**, against the official
  `github.com/modelcontextprotocol/go-sdk`. The carrier is likewise NOT a handler
  method: the generated wrapper authorizes it, refuses a session with no verified
  caller, and hands the validated stream to a generated transport. Beside the
  wrapper the Go generator emits `<service>_mcp.go`, holding:

  - `<Service>McpTools()` — the published tool set as `[]*mcp.Tool`, each with
    the generated JSON Schema, omitting the same server-computed fields.
  - `<Service>McpSurface` — the per-session surface. A `tools/call` decodes into
    the rpc's own protobuf request with `protojson` (which REJECTS an unknown
    field, so `additionalProperties: false` is enforced server-side too), runs
    the generated request validation, stamps `(spec.principal)` /
    `(spec.namespace)` from the verified `Caller`, dispatches to
    `<Service>Handler.<Rpc>`, filters `(spec.store_principal)` /
    `(spec.store_namespace)` rows out of the response, and frames the typed
    result. A tool failure is a `CallToolResult` with `IsError`, not a JSON-RPC
    protocol error, so the model can read and correct its own call.
  - `<Service>McpSurface.Call(ctx, name, arguments)` — the session-free entry
    point. An in-process caller (a service acting as its own MCP client, e.g.
    an agentic loop dispatching its own tool calls) reuses the generated
    argument decode, validation and caller-scoping through it; the `tools/call`
    handlers are thin wrappers around it.
  - `serve<Service>McpSession(...)` and an `mcp.Transport` over the gRPC bidi
    stream — one complete JSON-RPC frame per stream message, bounded at
    `mcp.McpResponse.payload`'s `(spec.assert_max_size)`.
  - `<service>_mcp_test.go` — a generated gate that drives a whole session
    (initialize, `tools/list`, one `tools/call`) over that transport and asserts
    the typed handler was reached and a client-supplied caller-scoped field was
    refused.

  Go's generated code lands in the consuming module, so tacplus writes no
  manifest for it: `go mod tidy` resolves the SDK, and the version inventory
  lives in `dependencies.yaml`. The SDK's own module requires `go >= 1.25.0`, so
  a consumer module below that cannot build a service with an MCP tool.

  **In Java, tacplus generates the same termination**, against the official
  `io.modelcontextprotocol.sdk:mcp`. The carrier is likewise NOT a handler
  method: it goes through the wrapper's ordinary bidi branch — same
  authorization, same caller extraction, same frame validation — with a
  fail-closed check that the session has a verified caller, and delegates to a
  generated class. Beside the wrapper the Java generator emits `<Service>Mcp`,
  holding:

  - `<Service>Mcp.tools()` — the published tool set as `List<McpSchema.Tool>`,
    each built from the generated JSON Schema, omitting the same server-computed
    fields.
  - `<Service>Mcp.Surface` — the per-session surface, bound to one verified
    `Caller`. Its `call(name, arguments)` is the session-free entry point an
    in-process caller uses; the `tools/call` handlers are thin wrappers
    around it. It holds the caller as a FIELD rather than reading it off the gRPC
    `Context`, because the SDK dispatches a tool call on its own scheduler
    thread where `Auth.callerFromContext()` is not in scope. A `tools/call`
    decodes into the rpc's own protobuf request with `JsonFormat.parser()`
    (which REJECTS an unknown field, so `additionalProperties: false` is
    enforced server-side too), runs the generated request validation, stamps
    `(spec.principal)` / `(spec.namespace)` from that `Caller`, dispatches to
    `<Service>Handler.<rpc>`, filters `(spec.store_principal)` /
    `(spec.store_namespace)` rows out of the response — with the record plane's
    own `Runtime.getRuntime().halt(1)` backstop on a foreign-tenant row — and
    frames the typed result. A tool failure is a `CallToolResult` with
    `isError`, not a JSON-RPC protocol error, so the model can read and correct
    its own call.
  - `<Service>Mcp.serve(...)` and an `McpServerTransportProvider` over the gRPC
    bidi `StreamObserver` pair — one complete JSON-RPC frame per stream message,
    bounded at `mcp.McpResponse.payload`'s `(spec.assert_max_size)`. An
    undecodable frame is answered with a JSON-RPC parse error and the session
    survives it.
  - `<Service>McpTest` — a generated main-method self-test (the shape every
    other generated Java test takes) that drives each tool through the dispatch,
    asserts the typed handler was reached with the server-STAMPED value the call
    never carried, asserts a client-supplied caller-scoped field is refused
    before the handler is reached, and drives a whole session over the carrier as
    raw JSON-RPC frames.

  Java's generated code lands in the consuming Maven module, so tacplus writes no
  `pom.xml` for it: the consumer declares the SDK, and the version inventory
  lives in `dependencies.yaml`. The SDK requires Java 17 or later.

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
| `(spec.record) = {key, owner, c2s?, s2s?, residency?, min_group_size?}` | **delivery**: an `event` that is also stored in `record_store` and is projectable — `key` is the id field, `owner` the ownership field, `residency` the store's lifecycle as two clocks (`{idle_seconds, deleted_max_age_seconds}`; unset falls back to a live platform default, never "never"), `min_group_size` the smallest result size the record's size-only query surfaces will disclose (see below) |
| `(spec.transformation) = {…}` | **transformation**: a pure typed conversion of this message into a derived row type. Needs no `record`, no topic and no subscription, and carries no residency — declaring a transformation and declaring delivery are independent |
| `(spec.public_view) = true` | **public view**: every field declares `(spec.visibility)`; tacplus generates `<T>Public` (the SHOW fields only) and the conversion to it — see below |

**Field-level (on a `record` message's fields):**

| Annotation | Meaning |
|------------|---------|
| `(spec.query_field) = {op: EQ\|RANGE}` | expose the field as a typed filter in the `query` trait's record query plane |
| `(spec.redact) = {mode: DROP\|PSEUDONYMIZE\|MAY_REDACT\|MAY_ANONYMIZE}` | marks a field for **data entitlement** (see below). DROP/PSEUDONYMIZE always apply (hard floor); MAY_REDACT/MAY_ANONYMIZE let the caller decide at egress. Valid on scalars, message-typed fields (redact clears the whole sub-message), and repeated fields; ANONYMIZE requires string/bytes |
| `(spec.visibility) = VISIBILITY_SHOW\|VISIBILITY_BACKGROUND` | on a field of a `public_view` message: SHOW may be rendered outside the company, BACKGROUND never leaves it (see below) |
| `(spec.deleted_flag) = true` | marks this record type's soft-delete `bool` field (exactly one per record type). The query plane's `Open<T>QueryRequest`/`Refine<T>Request` carry `include_deleted` (default false: flagged-deleted rows excluded); the row is purged `residency.deleted_max_age_seconds` after the flag was set |

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

#### Minimum group size (size-only query refusal)

`(spec.record).min_group_size = N` is the smallest result size this record's
**size-only** query surfaces will disclose. `0` / unset is no guard: the annotation is
additive, so a record that does not declare it gets exactly the plane it got before.

- **What it guards.** The three RPCs of the generated query plane that return a size and
  no rows — `Get<T>Count` and `Subscribe<T>Count` (`RecordCount.count`), and
  `Update<T>Query` (`QueryStats.size`, the match count of the *newly installed*
  predicate; left open, it is a one-RPC bypass of the `Get<T>Count` guard). Both halves of
  the plane are guarded, the S2S `RecordMap` and the C2S `RecordQuery`, unary and
  streaming alike. `Update<T>Query` and `Subscribe<T>Count` are emitted only for a
  filterable `T`, so an unfilterable record is guarded on `Get<T>Count` alone.
- **Where the line is.** It does **not** cover `List<T>`'s `total_count`: that size
  accompanies rows the caller has just been handed and discloses nothing the rows
  themselves do not. `Delete<T>Query`'s `QueryStats.size` is a hardcoded zero and carries
  no stamp. A future change that makes a paged read return counts without rows has crossed
  that line.
- **The refusal is an error, never a value.** The generated wrapper — never the handler —
  throws `PERMISSION_DENIED` when the size is below `N`. There is no sentinel count and no
  `suppressed` flag: a caller that knows nothing about suppression must not be able to
  read the refusal as data. The message names the annotation and the declared threshold
  and nothing tighter — "only 2 matched" would disclose exactly what the guard withholds.
  A stream is **terminated** with that error, on its first frame or on a later crossing,
  rather than silently filtered: a live count that just goes quiet at `N-1` discloses the
  crossing by silence.
- **Declared on the record, enforced off the method.** The record-map injector propagates
  the value onto each guarded RPC as `option (spec.min_group_size) = N;`, and the
  downstream generators read that method option — none of them re-derives `T` from an RPC
  name.
- **Two generation-time errors.** A negative threshold fails generation, and so does a
  threshold declared into a fleet where no `(spec.trait)` hosts a record query plane (no
  trait whose host declares `(spec.record_store)`) — a threshold with no guarded surface
  behind it is decoration, and an unread annotation fails generation here the same way it
  does everywhere else. Enforcement is generated for **Go, Java and Rust** service wrappers; each generator
  fails the build on a stamped method whose guard it cannot render rather than serve a
  size-only RPC unguarded.

#### Public view (SHOW / BACKGROUND fields)

`(spec.public_view) = true` marks a message whose fields split between what may be
shown outside the company (a prospect's page) and what never leaves it. Example:

```protobuf
message Prospect {
  option (spec.public_view) = true;
  string company_name = 1 [(spec.visibility) = VISIBILITY_SHOW];
  string deal_notes   = 2 [(spec.visibility) = VISIBILITY_BACKGROUND];
  Contact champion    = 3 [(spec.visibility) = VISIBILITY_SHOW]; // Contact is itself public_view
}
```

- **No default.** Every field of a `public_view` message must declare a visibility;
  an unannotated field fails generation, so a new field forces a decision.
- **Generated surface.** `<T>Public` (proto, so every language gets the type) holds the
  SHOW fields at their own names and numbers; a SHOW field typed as another
  `public_view` message becomes `<Sub>Public`. The conversion parses T's bytes as
  `<T>Public` with unknown fields discarded: Go `<T>ToPublic`, Java `to<T>Public`,
  Rust/Python `<t>_to_public`, TypeScript `<t>ToPublic`. Swift and C# generate no
  message helpers and get the type only. `<T>Public` carries no injected trace fields.
- **What a renderer is handed.** Only `<T>Public`. T's own read paths (state, record
  plane, MCP, REST, events) are authenticated company surfaces and return T whole —
  BACKGROUND means "never leaves the company", not "never leaves the service".
- **Generation-time errors.** A SHOW field (singular, repeated or map value) typed as
  a message that is not a top-level `public_view` message; a SHOW oneof member;
  `(spec.visibility)` outside a `public_view` message; either annotation on a nested
  declaration; a declared message named `<T>Public`; and any RPC callable without a
  caller token (`RPC_FLAG_PUBLIC_NO_AUTH`, `RPC_FLAG_SELF_AUTHENTICATES`, C2S
  `auth_disabled`, `service_c2d`) whose request or response reaches a `public_view`
  message.

**Service-level (once per service):**

| Annotation | Service |
|------------|---------|
| `(spec.record_store) = {}` | the full-record store |

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
their internal `timeseries_type` marker, the eventbus topic +
publish RPCs, the per-store subscriptions, and the query plane are all
**generated** — never hand-written. The read surface of a `record_store` is the
generated refinable-query plane over record keys —
`Create<T>Query` / `Update<T>Query` / `Get<T>Count` / `Get<T>Sample` / `List<T>` /
`Watch<T>` / `Subscribe<T>Count` / `Delete<T>Query`, the same governed spelling on
both caller surfaces — not a hand-written subscription handler. That plane is asked
for by a `(spec.trait)` declaration, not by the `record_store` annotation alone: see
[`RECORD_EVENT_MODEL.md`](RECORD_EVENT_MODEL.md).

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
- tacplus's own documentation set, embedded verbatim under `docs` — including the
  generated `AGENT.md` (rendered from tacplus's template against this registry).
  Consumer-authored `agent.md` files are NOT read: nothing in tacplus ingests
  them, so authoring one has no effect on generation or on `registry.json`.

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

### AGENT.md (generated)

tacplus generates `AGENT.md` from an internal template (`docs/AGENT.md.tmpl`)
and embeds it into `registry.json` under the `docs` key. It is a generated
artifact — there is no mechanism for consumer projects to supply or override it.
The agentic analysis pass uses it to check for cross-language parity drift,
suspicious service shapes, and architectural anti-patterns.

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

## Well-Known Names (Conventions)

Three names from the platform tacplus was first built for are, for now,
expected by the generator itself rather than declared in the proto graph.
They are conventions: a proto set that follows them generates correctly, one
that does not will not get the behavior described. Replacing each with a
proto-declared equivalent is tracked in [https://github.com/tacplus-dev/tacplus/issues/1](https://github.com/tacplus-dev/tacplus/issues/1).

| Name | Where it is expected | What it means |
|------|---------------------|---------------|
| `eventbus` (proto package) | EventBus wire-contract gate (`pkg/internal/proto/eventbus_wire_contract.go`) | The canonical EventBus proto is found by its package name. The gate compares the synthesized `eventbus_support.proto` against it and fails generation on any field-level drift. If no package named `eventbus` is in the build inputs, the gate has nothing to compare against and is skipped. |
| `"kernel"` (value of `(spec.service_state).label`) | State placement rules (`pkg/internal/proto/state.go`) | A state root carrying this label is a kernel boundary: `(spec.persistence)` on any of its fields is a generation error unless the root also sets `identity_snapshot = true` (Rule 3b, see the `spec.service_state` reference above). Other label values carry no generator meaning. |
| `AZP_KERNEL` (enum `spec.Azp`) | Token claims (`pkg/proto/sys/spec.proto`) | The authorized-party value for tokens minted from trusted internal context by the platform's token service. The name reflects the first platform's service name; the meaning is "internal mint", not a dependency on a service called kernel. |
