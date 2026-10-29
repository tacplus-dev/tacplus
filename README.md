<!-- Generated from README.src.md by cmd/sitegen: do not edit. -->
# tacplus

Documentation, quick start and reference: **[tacplus.dev](https://tacplus.dev)**.

**t**ypes, **a**ttributes, **c**onstraints, **plus** more: a compiler that turns
annotated Protocol Buffer definitions into complete services, with the
contracts on rails. Validation, state ownership, authorization, identity,
lifecycle and clients are generated from one source of truth: full services in
Go, Rust and Java, and clients in TypeScript, Python, Swift and C#. Every
user-facing RPC is also an MCP tool, automatically, carrying the same
authorization as the RPC. Your code is the handler.

## What you write

A proto file with `spec` annotations. This is the service from the
[example](example/), shortened:

```protobuf
syntax = "proto3";
package notesapi;
import "sys/spec.proto";

option (spec.service) = {name: "notes"};

message Note {
  string id = 1 [(spec.assert_required) = true, (spec.indexed) = true];
  string tenant_namespace = 2 [(spec.assert_required) = true];
  string text = 3 [(spec.assert_required) = true, (spec.assert_max_size) = 1024];
}

message CreateNoteRequest {
  string tenant_namespace = 1 [(spec.assert_required) = true, (spec.assert_min_size) = 1];
  string text = 2 [(spec.assert_required) = true, (spec.assert_max_size) = 1024];
}
message CreateNoteResponse { Note note = 1; }

// Called by authenticated users through the gateway.
service NotesApi {
  option (spec.service_c2s) = {auth_entitled_users: true};

  // Create a note in the caller's tenant namespace.
  rpc CreateNote(CreateNoteRequest) returns (CreateNoteResponse) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_USER, exposure: RPC_EXPOSURE_EXTERNAL,
                         flags: [RPC_FLAG_AUTH_ENTITLED_USERS], target_namespace_field: "tenant_namespace"};
  }
  // ... GetNote, and a paged ListNotes
}

// A state root: a partition of distributed state this service owns.
message NotesState {
  option (spec.service_state) = {name: "NotesState", label: ["notesapi"], export: true};
  repeated Note notes = 1 [(spec.indexed) = true];
}
```

plus a `tacplus.yaml` saying which language, package and output directory.

## What you get

For Go, `tacplus build` writes a module under `model/`:

```text
model/
├── s2sm/
│   ├── s2sm.go                              # ServiceManager: identity, TLS, health, state, clients
│   ├── grpc/servers/notes/notesapi/
│   │   ├── notes_api.go                     # NotesApiHandler interface + validating, authorizing wrapper
│   │   ├── notes_api_mcp.go                 # the same rpcs as MCP tools
│   │   └── notes_state_service.go           # state service: Get / Set / Watch / Delete
│   └── state/notes/notes_state/             # typed accessors for NotesState
├── types/notes/notesapi/                    # protobuf types with validation helpers
├── chassis/                                 # runtime libraries the generated code calls
├── registry.json                            # the whole model as data, tool catalogue included
└── AGENT.md                                 # the architectural norms, for agents
```

You implement the handler interface. The wrapper validates every request and
response against the `assert_*` annotations, runs authentication and
authorization, builds the caller identity, and only then calls your code.

## Install

Release binaries for Linux, macOS and Windows (amd64 and arm64) are on
[GitHub Releases](https://github.com/tacplus-dev/tacplus/releases), each with
`checksums.txt`; `tacplus upgrade` keeps an installed binary on the newest
patch of its MAJOR.MINOR line. A repository's builds run the exact release it
pins in `tacplus.lock` (`tacplus pin` writes it; moving it is a commit), in
the build image of that release's line, which carries every toolchain and
verifier and whose `tacplus` runs the pinned binary:

```bash
# Once, and whenever you move to a newer release: pin it, and commit the pin.
docker run --rm -v "$PWD:/workspace" -w /workspace \
  ghcr.io/tacplus-dev/tacplus/build:2.6 tacplus pin
git add tacplus.lock && git commit -m "Pin tacplus"

# Every build: the pinned image, by digest, running the pinned tacplus.
image="$(sed -n 's/^build_image: //p' tacplus.lock)"
docker run --rm -v "$PWD:/workspace" -w /workspace "$image" \
  tacplus build --config tacplus.yaml --src proto
```

From source, with Go, `just` and `protoc`:

```bash
git clone https://github.com/tacplus-dev/tacplus
cd tacplus
just protoc-check   # is your protoc in the supported range?
just install        # generate, build and go install the tacplus binary
```

`go install github.com/tacplus-dev/tacplus@latest` does not work: the
compiled spec package is generated at build time, not committed.
[docs/INSTALL.md](docs/INSTALL.md) has the per-platform commands and what
`tacplus build` needs on `PATH`.

## Learn more

Everything else:

- [Quick start](example/README.md): generate and run the example, a Go server and a TypeScript client.
- [Languages](https://tacplus.dev/build/#pick-a-language): what each of the seven targets gets.
- [User guide](docs/USER.md): annotations, `tacplus.yaml`, generated output, implementer rules.
- [Annotation reference](pkg/proto/sys/spec.proto): every `(spec.*)` option, in the proto that defines them.
- [tacplus.dev](https://tacplus.dev/): the whole fleet the compiler is part of, and how its pieces fit.

## Contributing and security

See [CONTRIBUTING.md](CONTRIBUTING.md) and [SECURITY.md](SECURITY.md).

**Is this related to TACACS+ or tac_plus?** No. Different project, different
problem; the name collision is a coincidence.

## License

Apache License 2.0. See [LICENSE](LICENSE).
