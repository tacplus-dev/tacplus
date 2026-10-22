---
title: Annotated protobuf in, complete services out
description: tacplus compiles annotated Protocol Buffer definitions into complete, validated, authorized services and clients.
hide:
  - navigation
  - toc
---

<div class="tp-hero" markdown>

# tacplus

<p class="tp-tagline">Annotated protobuf in. Complete services and clients out.</p>

tacplus is a compiler for Protocol Buffer definitions that carry `spec`
annotations. From one proto set it generates the validation, state
ownership, authorization, identity, lifecycle and clients around your
handler: full services in Go, Rust and Java, and clients in TypeScript,
Python, Swift and C#. Every user-facing RPC is also an MCP tool, with the same
authorization as the RPC. Open source under Apache-2.0.

[Install](install.md){ .md-button .md-button--primary }
[Quick start](quickstart.md){ .md-button }
[The deck](deck/index.html){ .md-button }
[GitHub](https://github.com/tacplus-dev/tacplus){ .md-button }

</div>

## The problem

Every service in a multi-language platform needs the same infrastructure:
typed messages, validated inputs, serialization, authentication,
authorization, state, health. The usual answer is hand-rolled validation,
shared libraries and conventions, so the contract (which values are valid,
who may call what, who owns which state) ends up scattered across code in
several languages. A rule changes in one language and not another; a service
writes state it does not own; an auth check exists here but not there.

tacplus puts the whole contract in the proto files, where every language
already agrees on the shape of the data, and generates everything that
follows from it. Your code is the handler. [Read the theory](guide/theory.md).

## Write the contract once

A proto file with `spec` annotations is the whole service definition. This is
the service from the [quick start](quickstart.md), shortened:

--8<-- "docs/snippets/proto.md"

A `tacplus.yaml` says which language and package to generate, and nothing
about the service itself:

```yaml
language: go
pkg: com.example.notesapi
output-dir: model
```

```bash
tacplus build --config tacplus.yaml --src ./proto
```

## What comes out

For Go, the build above writes a module under `model/`:

--8<-- "docs/snippets/generated-tree.md"

You implement the handler interface. The wrapper validates every request and
response against the `assert_*` annotations, runs authentication and
authorization, builds the caller identity, and only then calls your code.

## Four generated surfaces

Which clients are generated follows from the proto: a declared dependency
produces its client, and nothing is switched on in YAML.

<div class="grid cards tp-two" markdown>

-   :material-account-arrow-right:{ .lg .middle } **C2S: client to service**

    ---

    Native gRPC clients for applications calling a service through the
    gateway. Tokens are attached for you, and in every language but Swift
    and C# the request is validated before it leaves the process.

-   :material-web:{ .lg .middle } **W2S: web to service**

    ---

    The browser-friendly surface: unary calls over HTTP/REST,
    server streaming over SSE, client and bidi streaming over WebSockets.

-   :material-server-network:{ .lg .middle } **S2S: service to service**

    ---

    Typed gRPC clients between services, carrying service identity
    tokens. Generated for the server languages only.

-   :material-laptop:{ .lg .middle } **C2D: client to daemon**

    ---

    gRPC over local IPC (Unix-domain sockets, named pipes) to a daemon on
    the same machine. The socket's permissions are the auth boundary.

</div>

## Seven target languages

<div class="grid cards tp-two" markdown>

-   :material-server:{ .lg .middle } **Full service: Go, Rust, Java**

    ---

    Types, validation, server wrappers, MCP dispatch, state, the
    ServiceManager runtime, and S2S, C2S, W2S and C2D clients, at parity
    across the three.

-   :material-application-brackets:{ .lg .middle } **Full client: TypeScript, Python**

    ---

    Every client surface (C2S, W2S, C2D and REST) with client-side
    validation. Service-side generation is rejected, by design.

-   :material-cellphone:{ .lg .middle } **Lite client: Swift, C#**

    ---

    C2S, W2S and C2D clients. Streaming methods the target cannot carry
    become compile-time-unavailable stubs, never silently dropped.

-   :material-table-check:{ .lg .middle } **Compare them**

    ---

    What each language gets, surface by surface.

    [:octicons-arrow-right-24: Languages](languages.md)

</div>

## Where to next

<div class="grid cards tp-two" markdown>

-   [:octicons-download-24: **Install**](install.md): release binaries, `tacplus upgrade`, the container image, or from source.
-   [:octicons-rocket-24: **Quick start**](quickstart.md): generate and run the example: a Go server and a TypeScript client.
-   [:octicons-book-24: **User guide**](guide/user-guide.md): annotations, `tacplus.yaml`, and what gets generated.
-   [:octicons-code-24: **Annotation reference**](reference/annotations/index.md): every `(spec.*)` option, generated from `spec.proto`.
-   [:octicons-project-24: **The deck**](deck/index.html): the problem, how tacplus solves it, and what the hosting platform must provide, in slides.

</div>
