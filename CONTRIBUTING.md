# Contributing to tacplus

## Build

You need Go (the version in `go.mod`), `just`, and `protoc` at or below the
version pinned in the `justfile` (`just protoc-check` tells you). The
cross-language gates additionally need Java, Rust, Node, Python, Swift and
.NET. The tacplus build image (`ghcr.io/tacplus-dev/tacplus/build`,
`images/build.Dockerfile`) carries all of them, and `just gates-container`
runs the gates inside it.

```bash
just install          # builds and installs the tacplus binary
just test             # soundness gate + go test ./...
just gates            # the full mandatory gate set (slow)
just gates-container  # the same, inside the base image
```

`docs/DEVELOPMENT.md` is the compiler developer guide: architecture, the model,
code-generation patterns and the parity rules between language backends.

## Requesting a fix or a feature

Requests are GitHub issues filed with one of the two templates and nothing
else: not a chat message, not a note in another repository, not a request
to an agent working elsewhere.

- **Fix request** (label `fix-request`): a defect. The template asks for the
  commit, the area, a reproduction (the smallest proto plus `tacplus.yaml`,
  or a fixture under `cross-test/`), expected and actual behaviour, and the
  gate that should have caught it.
- **Feature request** (label `feature-request`): new behaviour, including a
  generated surface, annotation, flag, option or a parity port.

The **fix looper** takes the oldest open request and triages it first,
whatever its label: anything that adds or changes a generated surface,
annotation, CLI flag or option is a feature; anything else is a bug.

- A **bug** is reproduced with a failing test, fixed in the generator, run
  through the gates and opened as a pull request against `main` that closes
  the issue, then backported as one pull request per supported
  `release/MAJOR.MINOR` branch. A bug it cannot reproduce gets a comment and
  the `needs-repro` label instead of a guess.
- A **feature** is not built. The looper posts a review on the issue: what
  is asked; whether it addresses a gap; whether tacplus should do it at all
  or it belongs elsewhere; cost; alternatives; and a recommendation (accept
  for the next minor, accept later, decline, or needs information). It is
  labelled `needs-decision`. The maintainer accepts it by adding
  `feature:approved`, after which the looper builds it on `main` only,
  never backported, or declines it by closing the issue.

Review and merge of every pull request is a human's.

Today the looper runs locally: `scripts/fix-looper/run.sh install` puts a
launchd agent on a developer's machine that runs every 24 hours under that
developer's own Claude login, and `scripts/fix-looper/run.sh notify [N]`
makes it run now (for issue N). `.github/workflows/nightly-fixes.yml` is the
same job as a GitHub Action, on demand only until the repository has an
`ANTHROPIC_API_KEY` secret.

## Versions, patches and backports

`docs/VERSIONING.md` is the policy: consumers pin major.minor; a patch is
fixes only; features come in on a minor; fixes are backported to every minor
released within the last twelve months, on its `release/MAJOR.MINOR` branch.
A change that adds or alters a generated surface is a minor, however small.
The current line is 2.0: `release/2.0` takes fixes only (pull requests, no
direct pushes), and a patch release is a `v2.0.N` tag on that branch.

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
