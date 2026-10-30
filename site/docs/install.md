---
description: Install tacplus from a GitHub release, keep it current with tacplus upgrade, run it as a container image, or build it from source.
---

# Install

tacplus is a single binary. It drives `protoc` and the protoc plugins for the
languages you generate, so a working setup is the binary plus those tools;
the [container image](#container-image) carries all of them.

## Release binaries

Every release on [GitHub Releases](https://github.com/tacplus-dev/tacplus/releases)
carries a binary per platform and a `checksums.txt` of their SHA-256 sums:

| OS | Architectures | Asset |
|---|---|---|
| Linux | `amd64`, `arm64` | `tacplus-<major>-<minor>-<patch>-linux-<arch>` |
| macOS | `amd64`, `arm64` | `tacplus-<major>-<minor>-<patch>-darwin-<arch>` |
| Windows | `amd64`, `arm64` | `tacplus-<major>-<minor>-<patch>-windows-<arch>.exe` |

Download one, check it against `checksums.txt`, and put it on your `PATH`:

=== "macOS"

    ```bash
    VERSION=2.0.1   # the release you want; see the Releases page
    ARCH=arm64      # or amd64
    FILE="tacplus-${VERSION//./-}-darwin-${ARCH}"
    BASE="https://github.com/tacplus-dev/tacplus/releases/download/v${VERSION}"

    curl -fsSLO "$BASE/$FILE"
    curl -fsSLO "$BASE/checksums.txt"
    grep " $FILE\$" checksums.txt | shasum -a 256 -c -
    install -m 0755 "$FILE" /usr/local/bin/tacplus
    tacplus version
    ```

=== "Linux"

    ```bash
    VERSION=2.0.1   # the release you want; see the Releases page
    ARCH=amd64      # or arm64
    FILE="tacplus-${VERSION//./-}-linux-${ARCH}"
    BASE="https://github.com/tacplus-dev/tacplus/releases/download/v${VERSION}"

    curl -fsSLO "$BASE/$FILE"
    curl -fsSLO "$BASE/checksums.txt"
    grep " $FILE\$" checksums.txt | sha256sum -c -
    sudo install -m 0755 "$FILE" /usr/local/bin/tacplus
    tacplus version
    ```

=== "Windows (PowerShell)"

    ```powershell
    $Version = "2.0.1"   # the release you want; see the Releases page
    $File = "tacplus-$($Version -replace '\.', '-')-windows-amd64.exe"
    $Base = "https://github.com/tacplus-dev/tacplus/releases/download/v$Version"

    Invoke-WebRequest "$Base/$File" -OutFile $File
    Invoke-WebRequest "$Base/checksums.txt" -OutFile checksums.txt
    $want = (Select-String -Path checksums.txt -Pattern " $File$").Line.Split(" ")[0]
    if ((Get-FileHash $File -Algorithm SHA256).Hash -ne $want.ToUpper()) { throw "checksum mismatch" }
    Move-Item $File tacplus.exe   # then put tacplus.exe on your PATH
    ```

### What `tacplus build` needs

The binary generates code by running `protoc` and a plugin per language. With
a release binary you install them yourself; the [image](#container-image) has
them all, at the versions tacplus is tested with.

| You generate | Needs on `PATH` |
|---|---|
| any language | `protoc` (at most the version pinned in the repository's `justfile`, 36.0 today), `git` |
| Go | `protoc-gen-go`, `protoc-gen-go-grpc`, `gofmt` |
| Java | `protoc-gen-grpc-java` |
| Rust | `protoc-gen-prost`, `protoc-gen-tonic`, `protoc-gen-prost-serde`, `protoc-gen-prost-crate` |
| TypeScript | `node`, `protoc-gen-es` |
| Python | `python3` with `grpcio-tools` |
| Swift | `protoc-gen-swift`, `protoc-gen-grpc-swift-2` |
| C# | nothing extra: the generated project runs `Grpc.Tools` from NuGet in your own `dotnet build` |

## Staying current: `tacplus upgrade`

```bash
tacplus upgrade
```

replaces the running binary with the **newest patch release of the line it is
already on**: a `2.0.x` binary moves to the newest `2.0.x`, never to `2.1`.
Under the [versioning policy](project/versioning.md) a patch is fixes only, so
this is always safe to run. When a newer line exists, `upgrade` says so on
stderr and prints the command that moves to it; it never moves you on its own.

| Command | Installs |
|---|---|
| `tacplus upgrade` | the newest patch of the current `MAJOR.MINOR` |
| `tacplus upgrade --version 2.1` | the newest patch of `2.1` |
| `tacplus upgrade --version 2.0.1` | exactly `v2.0.1` |

Every download is checked against the release's `checksums.txt` before the
binary is replaced. If `GITHUB_TOKEN` is set, `upgrade` uses it for its
requests to GitHub (and drops it on a redirect to another host).

## Container image

`ghcr.io/tacplus-dev/tacplus/tacplus` is a slim image with the tacplus binary,
`protoc`, and the plugin for every language that needs one. It is built for
`linux/amd64` and `linux/arm64`.

| Tag | Meaning |
|---|---|
| `2.4` | the newest `2.4.x`: **what you pin** |
| `2.4.0` | that release, immutable |
| `2` | the newest `2.x` |
| `latest` | the newest release |
| `main-<sha>` | an unreleased build of `main` |

Use it as its **own generate step**: run it over your proto source to write
the generated model, then compile the result in your usual builder image.
Don't build `FROM` it. Pulling `tacplus:2.4` again is all it takes to pick
up a new patch; your builder image is untouched.

--8<-- "docs/snippets/install-container.md"

The image's working directory is `/workspace`, so `tacplus.yaml` and the
proto source are found where you mounted them. The generated files are
written by the container's user, which is root unless you say otherwise.

In a GitHub Actions workflow the generate step is one `run:` line, ahead of
the compile:

```yaml
steps:
  - uses: actions/checkout@v4
  - name: Generate
    run: >-
      docker run --rm --pull always -v "$PWD:/workspace"
      ghcr.io/tacplus-dev/tacplus/tacplus:2.4
      tacplus build --config tacplus.yaml --src proto
  - name: Build
    run: go build ./...
```

## From source

You need Go (the version in `go.mod`), [`just`](https://github.com/casey/just),
`protoc` and `protoc-gen-go` (the compiler generates its own descriptor code
with them before it builds).

--8<-- "docs/snippets/install-source.md"

A source build reports its version from the nearest tag with a `-local`
commit suffix, so it is never mistaken for a release build.
[Contributing](project/contributing.md) covers the tests and the
cross-language gates.
