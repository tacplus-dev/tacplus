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

## Requesting a fix

A fix request is a GitHub issue filed with the **Fix request** template and
nothing else: not a chat message, not a note in another repository, not a
request to an agent working elsewhere. The template asks for the commit, the
area, a reproduction (the smallest proto plus `tacplus.yaml`, or a fixture
under `cross-test/`), expected and actual behaviour, and the gate that should
have caught it.

The **fix looper** takes the oldest open `fix-request`, clones the repository
fresh, reproduces the defect with a failing test, fixes it in the generator,
runs the gates and opens a pull request that closes the issue. A request it
cannot reproduce gets a comment and the `needs-repro` label instead of a
guess. Review and merge of that pull request is a human's.

Today it runs locally: `scripts/fix-looper/run.sh install` puts a launchd
agent on a developer's machine that runs every 24 hours under that
developer's own Claude login, and `scripts/fix-looper/run.sh notify [N]`
makes it run now (for issue N). `.github/workflows/nightly-fixes.yml` is the
same job as a GitHub Action, on demand only until the repository has an
`ANTHROPIC_API_KEY` secret.

## Versions, patches and backports

`docs/VERSIONING.md` is the policy: consumers pin major.minor; a patch is
fixes only; features come in on a minor; fixes are backported to every minor
released within the last twelve months, on its `release/MAJOR.MINOR` branch.
A change that adds or alters a generated surface is a minor, however small.

## Making a change

1. Open an issue first: a **Fix request** for a defect, a plain issue for
   anything larger, so the design can be discussed before the code exists.
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
