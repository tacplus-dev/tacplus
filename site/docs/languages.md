---
description: What tacplus generates for each target language, Go, Rust, Java, TypeScript, Python, Swift and C#, surface by surface.
hide:
  - toc
---

# Languages

tacplus targets seven languages in three tiers. The tier decides what a build
may generate: a client-only target given a proto that asks for service-side
output rejects the build loudly instead of emitting a partial server.

| Tier | Languages | Status |
|---|---|---|
| **Full service** | Go, Rust, Java | Every surface, server and client, at parity across the three. Go and Rust lead; Java is close behind. |
| **Full client** | TypeScript, Python | Every client surface with client-side validation. No server runtime, by design. |
| **Lite client** | Swift, C# | C2S, W2S and C2D clients. Compiled in CI, not yet exercised against a live server. |

## What each language gets

:material-check:{ .tp-ok title="Yes" } yes &nbsp; :material-circle-half-full:{ .tp-partial title="Partly" } partly (see notes) &nbsp; :material-minus:{ .tp-no title="No" } not generated

<div class="tp-matrix" markdown>

| | Go | Rust | Java | TypeScript | Python | Swift | C# |
|---|:-:|:-:|:-:|:-:|:-:|:-:|:-:|
| Protobuf message types | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } |
| Validation from `assert_*` annotations[^validation] | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-minus:{ .tp-no title="No" } | :material-minus:{ .tp-no title="No" } |
| Handler interface and server wrapper | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-minus:{ .tp-no title="No" } | :material-minus:{ .tp-no title="No" } | :material-minus:{ .tp-no title="No" } | :material-minus:{ .tp-no title="No" } |
| MCP tools for user-facing rpcs | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-minus:{ .tp-no title="No" } | :material-minus:{ .tp-no title="No" } | :material-minus:{ .tp-no title="No" } | :material-minus:{ .tp-no title="No" } |
| ServiceManager runtime (identity, TLS, health, lifecycle) | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-minus:{ .tp-no title="No" } | :material-minus:{ .tp-no title="No" } | :material-minus:{ .tp-no title="No" } | :material-minus:{ .tp-no title="No" } |
| State service and typed state accessors | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-minus:{ .tp-no title="No" } | :material-minus:{ .tp-no title="No" } | :material-minus:{ .tp-no title="No" } | :material-minus:{ .tp-no title="No" } |
| S2S clients (service to service) | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-minus:{ .tp-no title="No" } | :material-minus:{ .tp-no title="No" } | :material-minus:{ .tp-no title="No" } | :material-minus:{ .tp-no title="No" } |
| C2S clients (client to service, native gRPC) | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } |
| W2S clients (web to service)[^w2s] | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-circle-half-full:{ .tp-partial title="Partly" } | :material-circle-half-full:{ .tp-partial title="Partly" } |
| REST clients | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-minus:{ .tp-no title="No" } | :material-minus:{ .tp-no title="No" } |
| C2D clients (client to local daemon) | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } |
| C2D server (hosting a local daemon) | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-minus:{ .tp-no title="No" } | :material-minus:{ .tp-no title="No" } | :material-minus:{ .tp-no title="No" } | :material-minus:{ .tp-no title="No" } |
| Client identity manager (`client_im`) | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } |
| Formal-verification harness for `assert_invariant`[^verify] | :material-minus:{ .tp-no title="No" } | :material-check:{ .tp-ok title="Yes" } | :material-check:{ .tp-ok title="Yes" } | :material-minus:{ .tp-no title="No" } | :material-minus:{ .tp-no title="No" } | :material-minus:{ .tp-no title="No" } | :material-minus:{ .tp-no title="No" } |

</div>

[^validation]: Server languages validate inbound and outbound at every service boundary; client languages validate a request before sending it. Swift and C# currently emit no client-side request validation and no default request deadline.
[^w2s]: W2S carries unary calls over HTTP/REST, server streaming over SSE, and client and bidi streaming over WebSockets. Swift carries unary and server-streaming methods; C# carries unary methods only. A method the target cannot carry is emitted as a compile-time-unavailable stub, so a mixed service still compiles and no declared method disappears.
[^verify]: Rust harnesses are Kani proofs, Java harnesses are JBMC proofs; `tacplus verify` runs them.

Which clients a build emits is decided by the proto, not by `tacplus.yaml`:
a `grpc_c2s_client` dependency produces a C2S client, a `grpc_w2s_client`
dependency a W2S client, and a `service_c2d` service a C2D client. See
[client transport surfaces](guide/user-guide.md#client-transport-surfaces).

## Generated packages

Each build writes one package in the target's own packaging, named from the
`pkg` in `tacplus.yaml` (the rules are in the
[user guide](guide/user-guide.md#how-pkg-becomes-a-native-package-name)):

| Language | Package | `pkg: com.example.identity` becomes |
|---|---|---|
| Go | Go module | `example.com/identity/model` |
| Rust | Cargo crates | `identity-model` |
| Java | Maven sources | `com.example.identity.model` |
| TypeScript | npm package | `identity-model` |
| Python | `pyproject.toml` package | `identity_model` |
| Swift | Swift package (SwiftPM) | `IdentityModel` |
| C# | .NET project | `IdentityModel` |

## Full service: Go, Rust, Java

The three server backends generate the full service: types and validation,
the handler interface and the wrapper around it (authentication,
authorization, validation, namespace policy, tracing and accounting, status
mapping), MCP dispatch, the state service and accessors, the ServiceManager
runtime and its libraries, and every client surface. None of them is the
reference implementation: the contract is the annotated proto plus the
[generated API](guide/generated-api.md), and a feature added to one server
language is added to all three. The cross-language gates in the repository
hold them to it.

## Full client: TypeScript, Python

The full-client targets generate every client surface (C2S, W2S, C2D and
REST) with the same request validation the servers run, so an invalid request
fails in the client with the server's own message. Asking them for anything
service-side (S2S clients, server wrappers, ServiceManager, EventBus runtime,
health, service state or stores) fails the build.

## Lite client: C# and Swift

The lite targets generate C2S, W2S and C2D clients for apps that call
tacplus services. Swift emits a SwiftPM package whose C2D client dials a
Unix-domain socket; C# emits a .NET project whose C2D client uses named pipes
on Windows and Unix-domain sockets elsewhere, and whose protobuf code comes
from `Grpc.Tools` in your own `dotnet build`. For streaming methods they
cannot carry, and for request validation, use a Go, Java or Rust client.
