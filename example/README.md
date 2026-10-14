# Example: a notes service

One small service, generated for a Go server and a TypeScript client, run end
to end from a clean clone:

```bash
just example
```

That builds tacplus, generates both models from `example/proto`, builds and
starts the Go server, and runs the TypeScript client against it. Needs Go,
Node and `protoc` (see [CONTRIBUTING.md](../CONTRIBUTING.md)). Output lands in
`example/target/`, which is git-ignored.

## What you write

| File | What it is |
|------|------------|
| [`proto/notes/notes.proto`](proto/notes/notes.proto) | The service: `NotesApi` with `CreateNote`, `GetNote` and a paged `ListNotes`, plus the `NotesState` state root. Everything the generator needs is an annotation in this file. |
| [`proto/client/notesclient.proto`](proto/client/notesclient.proto) | The client's identity: declares that it calls `NotesApi`. |
| [`proto/platform/notesmint.proto`](proto/platform/notesmint.proto), [`proto/platform/health.proto`](proto/platform/health.proto) | The two platform contracts every hosted service is generated against: the token mint and the service-health state. Declared, not run here. |
| [`proto/registry.yaml`](proto/registry.yaml), [`proto/dependencies.yaml`](proto/dependencies.yaml) | Stable service ids, and the pinned language dependencies the generated code builds with. |
| [`server/main.go`](server/main.go) | The handler: three methods on an in-memory map. That is all the Go a service author writes. |
| [`client/client.mjs`](client/client.mjs) | A few calls through the generated TypeScript client. |

## What you get

From one `tacplus build` per language. For Go (`example/target/go/model`):

- **`s2sm/grpc/servers/notes/notesapi/notes_api.go`**: the `NotesApiHandler`
  interface the server implements, and the `NotesApi` wrapper that validates
  every request and response against the proto's `assert_*` annotations, runs
  the auth seams, builds the `Caller`, and only then calls the handler. The
  paged `ListNotes` hands the handler decoded `PageParams` and encodes the
  cursor it returns.
- **`notes_api_mcp.go`**: the same three rpcs as MCP tools, `create_note`,
  `get_note` and `list_notes`, with JSON schemas derived from the messages and
  descriptions taken from the rpc doc comments. Every user-facing rpc on a
  `service_c2s` service is a tool; nothing is annotated to get this.
- **`s2sm/state/notes/notes_state/`** and **`notes_state_service.go`**: the
  state service (Get/Set/Watch/Delete) and typed accessors for `NotesState`.
- **`s2sm/s2sm.go`**: the ServiceManager that wires all of the above with
  identity, TLS, health and state in a deployed service.
- **`types/notes/notesapi/`**: the protobuf types with typed validation helpers.
- **`registry.json`**: the whole model as data, including the tool catalogue.
- **`chassis/`**: the runtime libraries the generated code calls into.

For TypeScript (`example/target/ts/model`):

- **`c2sm/`**: the client ServiceManager. `sm.grpc().notesapi().notes_api_client()`
  returns a client whose methods validate the request before it goes on the
  wire, attach the bearer token from the token provider, and walk the pages of
  `listNotes` for you (an async iterable).
- **`types/`**: the message types and the same validation helpers.

## What the run shows

```
MCP tools: create_note, get_note, list_notes
created note-1 "hello from typescript"
fetched note-1 "hello from typescript"
listed note-1, note-2
rejected empty text: field text below min size of 1 bytes, got: 0
```

The last line is the generated client refusing an invalid request locally; the
server would refuse it too, with the same message, because both sides are
generated from the same annotation.

## What this example does not do

- It hosts the generated wrapper on a plain gRPC server and verifies one demo
  token. A deployed service calls `s2sm.New()` and `sm.Run(handler)` instead,
  which brings up identity, TLS, health and state, and needs the platform's
  token mint and state store to be running. Those services are declared in
  `proto/platform/` so generation has their contracts, but this example does
  not run them, so the state service is generated and not exercised.
- `run.sh` writes the generated Go module's `go.mod` with no pins and lets
  `go mod tidy` resolve versions, which is simplest for an example and not what
  a real service would do.
