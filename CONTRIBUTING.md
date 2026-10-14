# Contributing to tacplus

## Build

You need Go (the version in `go.mod`), `just`, and `protoc` at or below the
version pinned in the `justfile` (`just protoc-check` tells you). The
cross-language gates additionally need Java 17, Rust, Node and Python; the
`Dockerfile.base` image carries all of them.

```bash
just install          # builds and installs the tacplus binary
just test             # soundness gate + go test ./...
just gates            # the full mandatory gate set (slow)
just gates-container  # the same, inside the base image
```

`docs/DEVELOPMENT.md` is the compiler developer guide: architecture, the model,
code-generation patterns and the parity rules between language backends.

## Making a change

1. Open an issue first for anything larger than a bug fix, so the design can
   be discussed before the code exists.
2. Every bug fix comes with a test that fails without it. Generator changes
   must keep the language backends at parity: a feature added to one server
   language (Go, Java, Rust) is added to all three, or the gap is documented
   in `docs/KNOWN_TEST_GAPS.md`.
3. Generated code must stay deterministic. `just determinism` checks it.
XX   request.
5. Keep commits focused and describe *why* in the message.

## License

By contributing you agree that your contributions are licensed under the
Apache License 2.0, the same as the project.
