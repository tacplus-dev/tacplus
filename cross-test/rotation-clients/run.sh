#!/usr/bin/env bash
# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# Behavioral fixture for make-before-break stream rotation in the client-only
# targets (tacplus#323). A C2S service's streams outlive the token they opened
# with: the generated server serves the <Svc>Rotating sibling (as for S2S,
# tacplus#322), and the generated client of another language carries its
# streams across token lifetimes on it. The REAL generated Go server (the
# generated wrapper and sibling, the generated Authorizer over real signed user
# tokens, #260's expiry interceptor) meets the REAL generated client, with 3 s
# tokens, through the Go fixture's TestRotationClients, which runs the
# client's leg:
#   - a caller that never mentions rotation runs one stream of each shape
#     across several lifetimes (bidi both ways at once) with no loss,
#     duplication or reordering, on one handler call and one context;
#   - a rotation with another subject's token and the valid session id is
#     refused (PERMISSION_DENIED); a revoked caller is cut off within one
#     lifetime (UNAUTHENTICATED);
#   - against a server without the sibling the client falls back to the plain
#     method, which ends at its token's exp.
# --language typescript (the default): the generated c2sm over grpc-js;
# --language python: the generated c2sm over grpc.aio; --language csharp: the
# generated C2SClient over Grpc.Net; --language swift: the generated C2SClient
# over grpc-swift 2.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PROTO_SRC="$ROOT_DIR/cross-test/rotation-clients/proto"
TESTDATA="$ROOT_DIR/cross-test/rotation-clients/testdata"
LANGUAGE="typescript"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --language) LANGUAGE="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

die() { echo "rotation-clients check FAILED: $*" >&2; exit 1; }

# first_error LOG: the first line of LOG that states an error: a client's
# own one-line summary (`LegError: `), else its uncaught error or failed
# assertion (node, Python, .NET, Swift; a module path such as
# grpc.aio._call.AioRpcError included), else a Go compile or vet error, else the fixture's own t.Fatalf, without its file and line. A build's
# verdict quotes only the first 100 or so characters of the gates' last line,
# which carries this (scripts/gates.sh): it must be short.
first_error() {
  local line
  line="$(grep -m 1 -E '^LegError: ' "$1")" \
    || line="$(grep -m 1 -E '(^|[[:space:]])([A-Za-z0-9_.]*(Error|Exception)|Fatal error|panic): ' "$1")" \
    || line="$(grep -m 1 -E '\.go:[0-9]+:[0-9]+: ' "$1")" \
    || line="$(grep -m 1 -E '_test\.go:[0-9]+: ' "$1" | sed -E 's/^.*_test\.go:[0-9]+: //')" \
    || line="(no error line; see the log)"
  # The verdict's room is the error's own: no `LegError: `, and grpc-swift's
  # "Stream unexpectedly closed" shortened so that what closed it shows.
  echo "$line" | sed -E -e 's/^[[:space:]]+//' -e 's/^LegError: //' -e 's/Stream unexpectedly closed/stream closed/'
}

case "$LANGUAGE" in
  typescript|python|csharp|swift) ;;
  *) die "rotation-clients supports --language typescript, python, csharp or swift" ;;
esac
PYTHON_BIN="${TACPLUS_PYTHON:-python3}"

TACPLUS_BIN="${TACPLUS_BIN:-$ROOT_DIR/target/tacplus-rotation-clients-check}"
if [[ ! -x "$TACPLUS_BIN" || -z "${TACPLUS_BIN_PREBUILT:-}" ]]; then
  echo "==> Building tacplus binary"
  (cd "$ROOT_DIR" && go generate ./pkg/internal/proto/spec/... && go build -o "$TACPLUS_BIN" .)
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

generate() {
  local language="$1" dir="$WORK/$1"
  mkdir -p "$dir"
  cat > "$dir/tacplus.yaml" <<EOF
language: "$language"
pkg: "com.example.rotationclients"
src: "$PROTO_SRC"
output-dir: "tacplus"
EOF
  echo "==> Generating the rotation-clients fixture ($language)"
  (cd "$dir" && TACPLUS_PYTHON="$PYTHON_BIN" "$TACPLUS_BIN" build --config tacplus.yaml --src "$PROTO_SRC") \
    || die "generation failed for $language"
  [[ -d "$dir/tacplus/model" ]] || die "no generated $language output at $dir/tacplus/model"
}

# The server: the generated Go ViewerService wrapper with its sibling.
generate go
GO_OUT="$WORK/go/tacplus/model"
WRAPPER_DIR="$GO_OUT/s2sm/grpc/servers/svc/rotationclients"
[[ -f "$WRAPPER_DIR/viewer_service.go" ]] || die "go ViewerService wrapper missing"
grep -q 'rotation.Register(grpcServer, &types.ViewerService_ServiceDesc' "$WRAPPER_DIR/viewer_service.go" \
  || die "the go ViewerService wrapper does not serve ViewerServiceRotating (a C2S service)"
cp "$TESTDATA/rotation_clients_fixture_test.go" "$WRAPPER_DIR/rotation_clients_fixture_test.go"
cat > "$GO_OUT/go.mod" <<'EOF'
module example.com/rotationclients/tacplus/model

go 1.23

require (
	github.com/dsnet/compress v0.0.2-0.20230904184137-39efe44ab707
	github.com/gin-gonic/gin v1.11.0
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/pelletier/go-toml/v2 v2.2.4
	github.com/soheilhy/cmux v0.1.5
	go.uber.org/zap v1.27.1
	golang.org/x/net v0.62.0
	golang.org/x/sync v0.19.0
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa
	google.golang.org/grpc v1.83.2
	google.golang.org/protobuf v1.36.11
	gopkg.in/yaml.v3 v3.0.1
)
EOF
echo "==> Building the generated Go server"
(cd "$GO_OUT" && go mod tidy && go vet ./s2sm/grpc/servers/svc/rotationclients) 2>&1 | tee "$WORK/go-build.log" \
  || die "the generated Go server does not build: $(first_error "$WORK/go-build.log")"

case "$LANGUAGE" in
  typescript)
    command -v node >/dev/null || die "node is required for the typescript leg"
    generate typescript
    TS_OUT="$WORK/typescript/tacplus/model"
    CLIENT="$(grep -rl --include='*.ts' --exclude-dir=node_modules 'class ViewerServiceGrpcClient' "$TS_OUT" | head -n 1)"
    [[ -n "$CLIENT" ]] || die "typescript ViewerServiceGrpcClient missing"
    [[ -f "$TS_OUT/chassis/servicemanager/Rotation.ts" ]] || die "typescript rotation chassis missing"
    [[ "$(grep -c 'openRotatingStream<' "$CLIENT")" -eq 3 ]] \
      || die "the typescript ViewerServiceGrpcClient does not rotate each of its three streams"
    echo "==> Building the generated TypeScript client"
    (cd "$TS_OUT" && npm install --loglevel=error && npm run build) > "$WORK/npm-build.log" 2>&1 || {
      cat "$WORK/npm-build.log" >&2
      die "the generated TypeScript client does not build"
    }
    TS_C2SM="$TS_OUT/dist/c2sm/index.js"
    [[ -f "$TS_C2SM" ]] || die "typescript c2sm module missing at $TS_C2SM"
    # The module that defines the fixture's Tick (an index.js re-exports it
    # with `export *`, which names nothing).
    TS_TYPES="$(grep -rl --include='*.js' 'export const TickSchema' "$TS_OUT/dist/types" | head -n 1)"
    [[ -n "$TS_TYPES" ]] || die "typescript Tick types missing under $TS_OUT/dist/types"
    cp "$TESTDATA/typescript/rotation_client.mjs" "$TS_OUT/rotation_client.mjs"
    CLIENT_CMD="cd '$TS_OUT' && TS_C2SM='$TS_C2SM' TS_TYPES='$TS_TYPES' node rotation_client.mjs"
    ;;
  python)
    # Python generation runs grpc_tools.protoc.
    if ! "$PYTHON_BIN" -c 'import grpc_tools.protoc' >/dev/null 2>&1; then
      "$PYTHON_BIN" -m pip install -q --disable-pip-version-check --target "$WORK/codegen-deps" grpcio-tools \
        || die "failed to install grpcio-tools for codegen"
      export PYTHONPATH="$WORK/codegen-deps${PYTHONPATH:+:$PYTHONPATH}"
    fi
    generate python
    PY_OUT="$WORK/python/tacplus/model"
    CLIENT="$(grep -rl --include='*.py' 'class ViewerServiceGrpcClient' "$PY_OUT" | head -n 1)"
    [[ -n "$CLIENT" ]] || die "python ViewerServiceGrpcClient missing"
    [[ -n "$(find "$PY_OUT" -path '*chassis/servicemanager/rotation.py' -print -quit)" ]] || die "python rotation chassis missing"
    [[ "$(grep -c 'rotating = await open_rotating_stream(' "$CLIENT")" -eq 3 ]] \
      || die "the python ViewerServiceGrpcClient does not rotate each of its three streams"
    echo "==> Installing the generated Python client"
    "$PYTHON_BIN" -m pip install -q --disable-pip-version-check --target "$WORK/python-deps" "$PY_OUT" > "$WORK/pip-install.log" 2>&1 || {
      cat "$WORK/pip-install.log" >&2
      die "the generated Python client does not install"
    }
    # The modules, as installed: <pkg>.c2sm.service_manager and the fixture's _pb2.
    py_module() { local rel="${1#"$WORK/python-deps/"}"; rel="${rel%.py}"; echo "${rel//\//.}"; }
    PY_C2SM="$(py_module "$(find "$WORK/python-deps" -path '*/c2sm/service_manager.py' -print -quit)")"
    PY_PB="$(py_module "$(find "$WORK/python-deps" -name 'rotation_clients_pb2.py' -print -quit)")"
    [[ "$PY_C2SM" == *.c2sm.service_manager && "$PY_PB" == *rotation_clients_pb2 ]] \
      || die "python modules not found (c2sm: '$PY_C2SM', pb: '$PY_PB')"
    CLIENT_CMD="PYTHONPATH='$WORK/python-deps' PY_C2SM='$PY_C2SM' PY_PB='$PY_PB' '$PYTHON_BIN' '$TESTDATA/python/rotation_client.py'"
    ;;
  csharp)
    command -v dotnet >/dev/null || die "dotnet is required for the csharp leg"
    generate csharp
    CS_OUT="$WORK/csharp/tacplus/model"
    CS_PROJECT="$(find "$CS_OUT" -maxdepth 1 -name '*.csproj' -print -quit)"
    [[ -n "$CS_PROJECT" ]] || die "csharp .csproj missing"
    [[ -f "$CS_OUT/Rotation.cs" ]] || die "csharp Rotation.cs missing"
    grep -q 'new RotationInterceptor(' "$CS_OUT/C2SClient.cs" \
      || die "the csharp C2SClient does not rotate its streams"
    for method in SendTicks GenerateTicks AssistTicks; do
      grep -q "\"/rotationclients.ViewerService/$method\"," "$CS_OUT/C2SClient.cs" \
        || die "the csharp C2SClient does not rotate $method"
    done
    # As TestTenantRouteCSharpBehaviour: the C# closure ships no sys/spec.proto,
    # but a comment in sys/mcp.proto keeps its import through the strip.
    if [[ -f "$CS_OUT/Protos/sys/mcp.proto" ]]; then
      sed -i.bak '/^import "sys\/spec.proto";$/d' "$CS_OUT/Protos/sys/mcp.proto" && rm -f "$CS_OUT/Protos/sys/mcp.proto.bak"
    fi
    CS_NAMESPACE="$(sed -n 's/^namespace \(.*\);$/\1/p' "$CS_OUT/C2SClient.cs" | head -n 1)"
    LEG="$WORK/csharp-leg"
    mkdir -p "$LEG"
    sed "s/^using RotationclientsModel;$/using $CS_NAMESPACE;/" "$TESTDATA/csharp/Program.cs" > "$LEG/Program.cs"
    cat > "$LEG/RotationLeg.csproj" <<EOF
<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <OutputType>Exe</OutputType>
    <TargetFramework>net8.0</TargetFramework>
    <RollForward>Major</RollForward>
    <Nullable>enable</Nullable>
    <ImplicitUsings>enable</ImplicitUsings>
    <TreatWarningsAsErrors>true</TreatWarningsAsErrors>
  </PropertyGroup>
  <ItemGroup>
    <ProjectReference Include="$CS_PROJECT" />
  </ItemGroup>
</Project>
EOF
    echo "==> Building the generated C# client"
    (cd "$LEG" && DOTNET_CLI_TELEMETRY_OPTOUT=1 DOTNET_NOLOGO=1 dotnet build -c Release -o "$LEG/out") > "$WORK/dotnet-build.log" 2>&1 || {
      cat "$WORK/dotnet-build.log" >&2
      die "the generated C# client does not build"
    }
    CLIENT_CMD="DOTNET_CLI_TELEMETRY_OPTOUT=1 dotnet '$LEG/out/RotationLeg.dll'"
    ;;
  swift)
    command -v swift >/dev/null || die "swift is required for the swift leg"
    generate swift
    SW_OUT="$WORK/swift/tacplus/model"
    [[ -f "$SW_OUT/Package.swift" ]] || die "swift Package.swift missing"
    SW_NAME="$(sed -n 's/^    name: "\(.*\)",$/\1/p' "$SW_OUT/Package.swift" | head -n 1)"
    [[ -n "$SW_NAME" ]] || die "swift package name not found in $SW_OUT/Package.swift"
    SW_SOURCES="$SW_OUT/Sources/$SW_NAME"
    [[ -f "$SW_SOURCES/Rotation.swift" ]] || die "swift Rotation.swift missing"
    for method in SendTicks GenerateTicks AssistTicks; do
      grep -q "\"rotationclients.ViewerService/$method\"," "$SW_SOURCES/C2SClient.swift" \
        || die "the swift C2SClient does not rotate $method"
    done
    GRPC_SWIFT_VERSION="$(sed -n 's|.*grpc-swift-2.git", from: "\(.*\)").*|\1|p' "$SW_OUT/Package.swift" | head -n 1)"
    LEG="$WORK/swift-leg"
    mkdir -p "$LEG/Sources/RotationLeg"
    sed "s/^import RotationclientsModel$/import $SW_NAME/" "$TESTDATA/swift/main.swift" > "$LEG/Sources/RotationLeg/main.swift"
    cat > "$LEG/Package.swift" <<EOF
// swift-tools-version: 6.1
import PackageDescription

let package = Package(
    name: "RotationLeg",
    platforms: [
        .macOS(.v15)
    ],
    dependencies: [
        .package(name: "$SW_NAME", path: "$SW_OUT"),
        .package(url: "https://github.com/grpc/grpc-swift-2.git", from: "$GRPC_SWIFT_VERSION")
    ],
    targets: [
        .executableTarget(
            name: "RotationLeg",
            dependencies: [
                .product(name: "$SW_NAME", package: "$SW_NAME"),
                .product(name: "GRPCCore", package: "grpc-swift-2")
            ],
            swiftSettings: [
                .swiftLanguageMode(.v6)
            ]
        )
    ]
)
EOF
    echo "==> Building the generated Swift client"
    (cd "$LEG" && swift build) > "$WORK/swift-build.log" 2>&1 || {
      tail -80 "$WORK/swift-build.log" >&2
      die "the generated Swift client does not build"
    }
    CLIENT_CMD="cd '$LEG' && SWIFT_BACKTRACE=enable=yes,interactive=no swift run --skip-build RotationLeg"
    ;;
esac


echo "==> Running the $LANGUAGE client against the generated Go server"
if ! (cd "$GO_OUT" && ROTATION_CLIENT_CMD="$CLIENT_CMD" \
  go test -count=1 -timeout 600s ./s2sm/grpc/servers/svc/rotationclients -run '^TestRotationClients$' -v) 2>&1 \
  | tee "$WORK/legs.log"; then
  # The client's last leg marker (`--- <leg>` or `--- <leg>/<step>`), not go
  # test's own; none when the client failed before its first leg. The gate's
  # name already says the language.
  reached="$(grep -E '^--- ' "$WORK/legs.log" | grep -vE '^--- (FAIL|PASS|SKIP):' | tail -n 1)" || reached=""
  reached="${reached#--- }"
  die "${reached:+at $reached: }$(first_error "$WORK/legs.log")"
fi
echo "rotation-clients ($LANGUAGE) OK"
