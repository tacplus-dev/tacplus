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

--8<-- "docs/snippets/proto.md"

plus a `tacplus.yaml` saying which language, package and output directory.

## What you get

For Go, `tacplus build` writes a module under `model/`:

--8<-- "docs/snippets/generated-tree.md"

You implement the handler interface. The wrapper validates every request and
response against the `assert_*` annotations, runs authentication and
authorization, builds the caller identity, and only then calls your code.

## Install

Release binaries for Linux, macOS and Windows (amd64 and arm64) are on
[GitHub Releases](https://github.com/tacplus-dev/tacplus/releases), each with
`checksums.txt`; `tacplus upgrade` keeps an installed binary on the newest
patch of its MAJOR.MINOR line. The container image carries tacplus and every
protoc plugin it drives, so a build can run it as its own generate step:

--8<-- "docs/snippets/install-container.md"

From source, with Go, `just` and `protoc`:

--8<-- "docs/snippets/install-source.md"

`go install github.com/tacplus-dev/tacplus@latest` does not work: the
compiled spec package is generated at build time, not committed.
[tacplus.dev/install](https://tacplus.dev/install/) has the per-platform
commands and what `tacplus build` needs on `PATH`.

## Learn more

Everything else is on [tacplus.dev](https://tacplus.dev):

- [Quick start](https://tacplus.dev/quickstart/): generate and run the example, a Go server and a TypeScript client.
- [Languages](https://tacplus.dev/languages/): what each of the seven targets gets.
- [User guide](https://tacplus.dev/guide/user-guide/): annotations, `tacplus.yaml`, generated output, implementer rules.
- [Annotation reference](https://tacplus.dev/reference/annotations/): every `(spec.*)` option.
- [The deck](https://tacplus.dev/deck/): the problem, how tacplus solves it, and what the hosting platform must provide.

## Contributing and security

See [CONTRIBUTING.md](CONTRIBUTING.md) and [SECURITY.md](SECURITY.md).

**Is this related to TACACS+ or tac_plus?** No. Different project, different
problem; the name collision is a coincidence.

## License

Apache License 2.0. See [LICENSE](LICENSE).
