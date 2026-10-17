# tacplus

**t**ypes, **a**ttributes, **c**onstraints, **plus** more: a compiler that turns
annotated Protocol Buffer definitions into complete services, with the
contracts on rails. Validation, state ownership, authorization, identity,
lifecycle and clients are generated from one source of truth for Go, Rust and
Java servers, with Python and TypeScript clients. Every user-facing RPC is also
an MCP tool, automatically, carrying the same authorization as the RPC.

## What you write

A proto file with `spec` annotations. This is the whole service from the
[example](example/):

```protobuf
syntax = "proto3";
package notesapi;
import "sys/spec.proto";

option (spec.service) = {name: "notes"};

message CreateNoteRequest {
  string tenant_namespace = 1 [(spec.assert_required) = true, (spec.assert_min_size) = 1];
  string text = 2 [(spec.assert_required) = true, (spec.assert_min_size) = 1, (spec.assert_max_size) = 1024];
}
message CreateNoteResponse { Note note = 1; }

// A client-to-service API: called by authenticated users through the gateway.
service NotesApi {
  option (spec.service_c2s) = {auth_entitled_users: true};

  // Create a note in the caller's tenant namespace and return it with its new id.
  rpc CreateNote(CreateNoteRequest) returns (CreateNoteResponse) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_USER, exposure: RPC_EXPOSURE_EXTERNAL,
                         flags: [RPC_FLAG_AUTH_ENTITLED_USERS], target_namespace_field: "tenant_namespace"};
  }
  // ... GetNote, and a paged ListNotes
}

// A state root: a named partition of distributed state this service owns.
message NotesState {
  option (spec.service_state) = {name: "NotesState", label: ["notesapi"], export: true};
  repeated Note notes = 1 [(spec.indexed) = true];
}
```

plus a `tacplus.yaml` saying which language, package and output directory.

## What you get

`tacplus build` emits, per language, everything around the handler:

- **A handler interface** and a **server wrapper** that validates every request
  and response against the `assert_*` annotations, runs the auth seams, builds
  the caller identity, and only then calls your code. You implement three
  methods on an in-memory map; that is the entire Go server in the example.
- **The same RPCs as MCP tools**, with schemas derived from the messages and
  descriptions from the RPC comments. A tool call goes through the same
  wrapper and the same authorization as the gRPC call.
- **Clients** in every target language, validating requests before they hit
  the wire, attaching tokens, and walking pages of paged collections for you.
- **A state service** (Get/Set/Watch/Delete) and typed accessors for each state
  root, and a **ServiceManager** that wires identity, TLS, health and state in a
  deployed service.
- **`registry.json`**: the whole model as data, including the tool catalogue,
  so tooling and agents read the same contract the code was generated from.

The runtime these generated services expect (token mint, state store, API
gateway) is defined by contract here and implemented downstream; tacplus is
the compiler.

## Install

Release binaries for Linux, macOS and Windows (amd64 and arm64) are on
[GitHub Releases](https://github.com/tacplus-dev/tacplus/releases), each with
`checksums.txt`. Once installed, `tacplus upgrade` keeps it on the newest
patch of its MAJOR.MINOR line (`GITHUB_TOKEN` raises the API rate limit).

The container image carries tacplus and every protoc plugin it drives, so a
build can run it as its own generate step:

```bash
docker run --rm -v "$PWD:/w" -w /w ghcr.io/tacplus-dev/tacplus/tacplus:2.0 tacplus build
```

From source, with Go, `just` and `protoc` (`just protoc-check` tells you
whether your `protoc` is in the supported range):

```bash
just install
```

`go install github.com/tacplus-dev/tacplus@latest` does not work: the
compiled spec package is generated at build time, not committed.

Documentation: [tacplus.dev](https://tacplus.dev).

## The example

```bash
git clone https://github.com/tacplus-dev/tacplus
cd tacplus
just example
```

generates a Go server and a TypeScript client from one proto set, starts the
server, and drives it: create, get, a paged list, and a request the generated
client rejects locally. [example/README.md](example/README.md) walks through
what was written and what was generated.

## Targets

| Language | What is generated | Status |
|----------|-------------------|--------|
| Go, Rust, Java | Full service: types, validation, server wrappers, MCP dispatch, state, ServiceManager, S2S/C2S/W2S clients | Full-service targets. Go and Rust lead; Java is close behind. |
| Python, TypeScript | Clients only: C2S, W2S, C2D and REST client surfaces with validation | Full-client targets. No server runtime is emitted, by design. |
| Swift, C# | Thin clients: C2S, W2S and C2D. Streaming methods the target cannot carry are emitted as compile-time-unavailable stubs. C#'s W2S is unary-only. | Lite targets. Compiled in CI, not exercised against a live server. |

## Documentation

- [docs/THEORY.md](docs/THEORY.md): what tacplus is and why. The long read.
- [docs/USER.md](docs/USER.md): the user manual: annotations, `tacplus.yaml`, generated output, implementer rules.
- [docs/API.md](docs/API.md): generated API contracts and behavioral guarantees.
- [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md): compiler internals and the parity rules between backends.
- [roadmap/](roadmap/README.md): what is next. Nearest: a generated direct LLM client with a built-in authorization and accounting intercept.

## Contributing and security

See [CONTRIBUTING.md](CONTRIBUTING.md) and [SECURITY.md](SECURITY.md).

## FAQ

**Is this related to TACACS+ or tac_plus?** No. Different project, different
problem; the name collision is a coincidence.

## License

Apache License 2.0. See [LICENSE](LICENSE).
