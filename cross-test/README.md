# Cross-Test Gates

The active cross-test gates generate current tacplus output into ignored
`target/` directories. Generated language output is not committed here.

## Commands

```bash
just -f cross-test/justfile
just -f cross-test/justfile current-generation
just consumer-build
just -f cross-test/justfile runtime-smoke
just -f cross-test/justfile server-runtime
just -f cross-test/justfile wire-corpus
just consumer-smoke
just consumer-smoke-go
just consumer-smoke-java
just consumer-smoke-python
just consumer-smoke-rust
just consumer-smoke-typescript
```

`current-generation` runs `cross-test/proto` through Go, Java, and Rust
generation, asserts that the output uses the current `sys/spec.proto` model
rather than the old committed `special` fixture layout, and compiles each
generated output with that language's normal local tooling.

`consumer-build` is the stronger public API gate for public surface shape. It
generates a focused consumer-contract fixture into a temporary work directory,
validates client surface selection, compiles generated outputs where supported,
and compiles handwritten downstream consumers.

`runtime-smoke` is the cross-language runtime gate: a go fixture server built
from the generated go servers (canonical consumer-contract fixture) hosts C2S
gRPC and hand-written W2S HTTP/SSE/WebSocket endpoints on localhost, and the
python/typescript generated CLIENTS exercise them over the wire — auth gating,
validation codes, watch/list streams, the PAGE auto-pager, W2S upload/chat
WebSocket framing, the fail-closed unwired-auth wrapper, and the audit
accounting/trace seams (observed via the fixture's `/fixture/stats` endpoint).
It runs in the `runtime-smoke-mr` CI job and the nightly matrix; it lives
outside `consumer-contract` because it needs go build + python + node
runtimes concurrently.

`server-runtime` is the transpose of `runtime-smoke`: real generated JAVA and
RUST `ContractApi` servers are each hosted on a TCP port and driven by the
generated GO C2S client over the wire (unary `auth_disabled`, `auth_any_user`
SNAPSHOT stream, `auth_any_user` PAGE with keyset continuation, and the
fail-closed rejection of an `auth_entitled_users` method before the handler).
It is the only in-repo evidence that java- and rust-hosted generated servers
interoperate with another language's generated client. It is SERVER coverage
only: the full N x N client/server matrix, W2S against java/rust hosts, and the
shared cross-language constraint corpus remain uncovered. Local/manual — it
needs go + maven + cargo concurrently, so it is not in CI, not in the nightly
matrix, and not in `just test`. See `cross-test/server-runtime/README.md`.

`wire-corpus` is the serialization-equivalence gate: one `CorpusRecord`
message is encoded ONCE by go into checked-in golden bytes
(`cross-test/fixtures/wire_corpus.json`), and all five generated languages
decode that same blob and assert every decoded field value against the expected
constants in that fixture. It is the only in-repo evidence of cross-language
wire-VALUE equivalence — every other gate here is source parity, compile, or RPC
behavior. It covers only the shapes this repo actually uses and that actually
differ between runtimes (int64 at the boundary, uint64 above 2^53,
`map<string,string>`, proto3 `optional` present-with-zero vs absent, and
non-UTF-8 bytes); constraint-class enforcement and the encode direction remain
uncovered. Local/manual — it needs go + maven + cargo + python + node
concurrently, so it is not in CI, not in the nightly matrix, and not in
`just test`. See `cross-test/wire-corpus/README.md`.

`consumer-smoke` runs the focused runtime consumer smokes. Go currently covers:
the generated C2S client calling `PublicRead` through a `bufconn` gRPC server
and generated server wrapper; the generated W2S client calling the same method
through local HTTP/JSON; one W2S `Watch` SSE event; W2S `Upload` WebSocket
`start`/`message`/`complete` frames; one bidirectional `Chat` WebSocket message
in each direction; and generated C2D `LocalRead`/`LocalWatch` clients against
a local Unix-socket daemon. Python and TypeScript are client-only targets, so
their smoke coverage should stay limited to supported client surfaces and
service-side rejection behavior.
Java currently runs the generated `both` consumer main after `javac`, proving
runtime classpath compatibility and exercising a local W2S bidirectional
WebSocket smoke with `start`/`message`/`complete` frames, bearer auth, wrapped
message decoding, and auth-required fail-closed behavior.
Rust currently runs the generated `both` consumer binary after `cargo check`,
including a local W2S bidirectional WebSocket smoke that sends from
`on_connect`, verifies bearer auth on outbound frames, decodes a wrapped
message response, sends `complete` with `finish()`, and verifies auth-required
W2S calls fail without a token provider.

The old broad runtime matrix was removed because it depended on committed
generated outputs from a pre-parity layout. Rebuilding wire-level runtime tests
should start from generated scratch output, not checked-in generated trees.
