---
description: Generate and run the tacplus example, a notes service with a Go server and a TypeScript client, from a clean clone.
---

# Quick start

The repository's [`example/`](https://github.com/tacplus-dev/tacplus/tree/main/example)
is one small service, a tenant-scoped notes API, generated twice from one
proto set: a Go server and a TypeScript client. One command builds tacplus,
generates both, starts the server and drives it with the client.

## Run it

You need Go, Node and `protoc`, plus the protoc plugins for Go and
TypeScript (see [what `tacplus build` needs](install.md#what-tacplus-build-needs)),
and [`just`](https://github.com/casey/just).

```bash
git clone https://github.com/tacplus-dev/tacplus
cd tacplus
just example
```

`just example` runs `example/run.sh`. The output lands in `example/target/`
(git-ignored), and the run ends with:

```text
MCP tools: create_note, get_note, list_notes
created note-1 "hello from typescript"
fetched note-1 "hello from typescript"
listed note-1, note-2
rejected empty text: field text below min size of 1 bytes, got: 0
```

The first line is the server listing the MCP tools generated from its rpcs.
The last line is the generated TypeScript client refusing an invalid request
before it reaches the wire; the server would refuse it too, with the same
message, because both sides are generated from the same annotation.

## 1. What you write: the proto

Everything the generator needs to know about the service is an annotation in
`example/proto/notes/notes.proto`:

```protobuf title="example/proto/notes/notes.proto"
--8<-- "example/proto/notes/notes.proto"
```

Reading it top down:

- `option (spec.service)` names the service identity used across the mesh.
- `assert_required`, `assert_min_size` and `assert_max_size` are field
  constraints. They become validation in the server wrapper and in the
  client, in every language.
- `service_c2s` makes `NotesApi` a client-to-service API: called by
  authenticated users through the gateway. Every user rpc on it is also an
  MCP tool, without any further annotation.
- `spec.rpc` sets each rpc's principal, exposure and auth flags, and
  `target_namespace_field` names the field that scopes it to a tenant.
- `method_collection` with `COLLECTION_MODE_PAGE` makes `ListNotes` a paged
  collection: the wrapper decodes the page token for the handler, and the
  clients walk the pages.
- `service_state` makes `NotesState` a state root, a partition of
  distributed state this service owns.

Every option is in the [annotation reference](reference/annotations/index.md).

The client gets its own identity in a second, three-line package that
declares what it calls:

```protobuf title="example/proto/client/notesclient.proto"
--8<-- "example/proto/client/notesclient.proto"
```

The proto directory also holds `registry.yaml` (stable service ids, managed
by tacplus), `dependencies.yaml` (the pinned language dependencies the
generated code builds with; every proto source needs one) and, under
`platform/`, the token-mint and health contracts every hosted service is
generated against.

## 2. What you write: `tacplus.yaml`

`run.sh` writes one config per build. Only the language and package differ:

=== "Go server"

    ```yaml
    language: "go"
    pkg: "com.example.notesapi"
    output-dir: "model"
    ```

=== "TypeScript client"

    ```yaml
    language: "typescript"
    pkg: "com.example.notesclient"
    output-dir: "model"
    ```

The last segment of `pkg` names the proto package the build hosts: the Go
build hosts `notesapi` and gets its server; the TypeScript build hosts
`notesclient` and gets a client of everything `notesclient` declares. Then
each build is one command:

```bash
tacplus build --config tacplus.yaml --src example/proto
```

## 3. What you get

For Go, in `example/target/go/model`:

| Path | What it is |
|---|---|
| `s2sm/grpc/servers/notes/notesapi/notes_api.go` | The `NotesApiHandler` interface the server implements, and the `NotesApi` wrapper that validates every request and response, runs the auth seams, builds the `Caller`, and only then calls the handler. |
| `.../notesapi/notes_api_mcp.go` | The three rpcs as MCP tools (`create_note`, `get_note`, `list_notes`), with JSON schemas derived from the messages and descriptions from the rpc comments. |
| `.../notesapi/notes_state_service.go`, `s2sm/state/notes/notes_state/` | The state service (Get/Set/Watch/Delete) and typed accessors for `NotesState`. |
| `s2sm/s2sm.go` | The ServiceManager that wires identity, TLS, health and state in a deployed service. |
| `types/notes/notesapi/` | The protobuf types with typed validation helpers. |
| `registry.json` | The whole model as data, including the tool catalogue. |
| `chassis/` | The runtime libraries the generated code calls into. |

For TypeScript, in `example/target/ts/model`: `c2sm/`, the client
ServiceManager, whose `notes_api_client()` validates each request, attaches
the bearer token from your token provider, and turns the paged `listNotes`
into an async iterable; and `types/`, the message types with the same
validation.

## 4. The handler

The Go server is three methods on an in-memory map. That is all the Go a
service author writes; the rest of `main.go` hosts the generated wrapper on
a plain gRPC server and stands in for the platform's token verification with
one demo token.

??? example "example/server/main.go"

    ```go
    --8<-- "example/server/main.go"
    ```

## 5. The client

??? example "example/client/client.mjs"

    ```javascript
    --8<-- "example/client/client.mjs"
    ```

## What the example leaves out

- A deployed service calls `s2sm.New()` and `sm.Run(handler)` instead of
  hosting the wrapper by hand. That brings up identity, TLS, health and
  state, and needs the platform's token mint and state store running. The
  example declares those contracts so generation has them, but does not run
  them, so the state service is generated and not exercised.
- `run.sh` lets `go mod tidy` pick the generated Go module's versions, which
  a real service would pin.

## Next

- [Languages](languages.md): what each target gets.
- [User guide](guide/user-guide.md): `tacplus.yaml`, the annotations by topic, and the implementer rules.
- [Generated API](guide/generated-api.md): the contracts and guarantees of the generated surfaces.
