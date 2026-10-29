# Install tacplus

tacplus is a single binary. It drives `protoc` and the protoc plugins for the
languages you generate, so a working setup is the binary plus those tools;
the [build image](#the-build-image) carries all of them, and every toolchain
and verifier a build uses.

## Release binaries

Every release on [GitHub Releases](https://github.com/tacplus-dev/tacplus/releases)
carries a binary per platform and a `checksums.txt` of their SHA-256 sums:

| OS | Architectures | Asset |
|---|---|---|
| Linux | `amd64`, `arm64` | `tacplus-<major>-<minor>-<patch>-linux-<arch>` |
| macOS | `amd64`, `arm64` | `tacplus-<major>-<minor>-<patch>-darwin-<arch>` |
| Windows | `amd64`, `arm64` | `tacplus-<major>-<minor>-<patch>-windows-<arch>.exe` |

Download one, check it against `checksums.txt`, and put it on your `PATH`:

#### macOS

```bash
VERSION=2.3.0   # the release you want; see the Releases page
ARCH=arm64      # or amd64
FILE="tacplus-${VERSION//./-}-darwin-${ARCH}"
BASE="https://github.com/tacplus-dev/tacplus/releases/download/v${VERSION}"

curl -fsSLO "$BASE/$FILE"
curl -fsSLO "$BASE/checksums.txt"
grep " $FILE\$" checksums.txt | shasum -a 256 -c -
install -m 0755 "$FILE" /usr/local/bin/tacplus
tacplus version
```

#### Linux

```bash
VERSION=2.3.0   # the release you want; see the Releases page
ARCH=amd64      # or arm64
FILE="tacplus-${VERSION//./-}-linux-${ARCH}"
BASE="https://github.com/tacplus-dev/tacplus/releases/download/v${VERSION}"

curl -fsSLO "$BASE/$FILE"
curl -fsSLO "$BASE/checksums.txt"
grep " $FILE\$" checksums.txt | sha256sum -c -
sudo install -m 0755 "$FILE" /usr/local/bin/tacplus
tacplus version
```

#### Windows (PowerShell)

```powershell
$Version = "2.3.0"   # the release you want; see the Releases page
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
a release binary you install them yourself; the [build image](#the-build-image)
has them all, at the versions tacplus is tested with.

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
| `tacplus upgrade --version 2.3.0` | exactly `v2.3.0` |

Every download is checked against the release's `checksums.txt` before the
binary is replaced. If `GITHUB_TOKEN` is set, `upgrade` uses it for its
requests to GitHub (and drops it on a redirect to another host).

## The build image

`ghcr.io/tacplus-dev/tacplus/build:X.Y` is one image per release line, rebuilt
on every patch of it, for `linux/amd64` and `linux/arm64`. It carries every
toolchain (Go, Rust, Java and Maven, Python, Node, Swift, .NET), `protoc` and
every plugin, the verifiers (Kani for Rust; JBMC and OpenJML for Java) and the
fuzzers (Go's own, cargo-fuzz on the nightly Rust in `$RUST_NIGHTLY`).
Generation, compile, test, fuzzing and `tacplus verify` all run in it. Go is
not formally verified, by design: `tacplus verify` reports it as skipped.

| Tag | Meaning |
|---|---|
| `2.6.1` | the image of that release, immutable |
| `2.6` | the newest `2.6.x` image (a convenience for people; builds use a digest) |
| `2` | the newest `2.x` |
| `latest` | the newest release |

Each release's digest is in the summary of its images workflow run, and
`tacplus pin` looks it up for you.

### Pinning: `tacplus.lock`

A build runs exactly what its commit pins: same commit, same result, whoever
or whatever starts the build. A repository commits a `tacplus.lock` beside its
`tacplus.yaml`, with the exact tacplus release and the build image digest:

```yaml
tacplus: 2.6.1
build_image: ghcr.io/tacplus-dev/tacplus/build:2.6.1@sha256:<64 hex>
```

`tacplus pin` writes it, from the newest patch of the pin's line (or of the
`tacplus.yaml` floor's, the first time):

| Command | Pins |
|---|---|
| `tacplus pin` | the newest patch of the current pin's line |
| `tacplus pin --version 2.7` | the newest patch of `2.7` |
| `tacplus pin --version 2.7.0` | exactly `v2.7.0` |

**Moving a pin is a commit, never something a build does:** run `tacplus pin`,
commit `tacplus.lock`, and the next build runs the new release. A patch is
fixes only ([versioning](project/versioning.md)), so moving a pin within its
line is always safe.

--8<-- "docs/snippets/install-container.md"

### The image's `tacplus`

In the build image, `/usr/local/bin/tacplus` is not the compiler but a
launcher, [`images/tacplus.sh`](https://github.com/tacplus-dev/tacplus/blob/main/images/tacplus.sh)
(POSIX sh):

- **In a repository** (a directory holding `tacplus.lock`, `tacplus.yaml` or
  `fleet.yaml`, or below one) it runs exactly the pinned release: it downloads
  the binary for the platform, checks it against the release's
  `checksums.txt` (a mismatch is refused, and nothing is cached), caches it
  under `~/.cache/tacplus/<version>/` and runs it. It refuses a repository with
  no `tacplus.lock`, a pin of another line than the image's, and a
  `TACPLUS_VERSION` other than the pin. `tacplus pin` is the one command it
  runs without a pin.
- **Outside a repository** (experimenting) it runs `TACPLUS_VERSION=X.Y.Z` when
  set, else the newest patch of the image's line, checking GitHub for a newer
  patch at most hourly.
- A cached release runs offline.

The same script works on a Mac (darwin binaries are published): put it on your
`PATH` as `tacplus` and it behaves the same, with no line check unless you set
`TACPLUS_LINE=X.Y`:

```bash
curl -fsSL -o /usr/local/bin/tacplus \
  https://raw.githubusercontent.com/tacplus-dev/tacplus/v2.6.0/images/tacplus.sh
chmod +x /usr/local/bin/tacplus
```

### The tacplus image (deprecated)

`ghcr.io/tacplus-dev/tacplus/tacplus` is deprecated and no longer built: its
existing tags (up to the 2.5 line) stay, unchanged, but no new release gets
one. Pin the release and run it in the build image as above.

## From source

You need Go (the version in `go.mod`), [`just`](https://github.com/casey/just),
`protoc` and `protoc-gen-go` (the compiler generates its own descriptor code
with them before it builds).

--8<-- "docs/snippets/install-source.md"

A source build reports its version from the nearest tag with a `-local`
commit suffix, so it is never mistaken for a release build.
[Contributing](project/contributing.md) covers the tests and the
cross-language gates.
