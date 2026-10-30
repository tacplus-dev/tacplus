#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
# shellcheck source=../lib/cargo-target.sh
source "$ROOT_DIR/cross-test/lib/cargo-target.sh"
PROTO_SRC="$ROOT_DIR/cross-test/consumer-contract/proto"
WORK_ROOT="${TACPLUS_CONSUMER_WORKDIR:-}"
TACPLUS_BIN="${TACPLUS_BIN:-}"
KEEP_WORK="${TACPLUS_CONSUMER_KEEP:-false}"
PYTHON_BIN="${TACPLUS_PYTHON:-}"
LANG_FILTER=""
CASE_FILTER=""

usage() {
  cat <<'USAGE'
Usage:
  cross-test/consumer-contract/run.sh [options]

Options:
  --language LANG   Run one language: go, java, python, rust, typescript, swift, csharp
  --case CASE       Run one case: c2s, w2s, both, c2d, disabled
  --workdir DIR     Use a specific temporary work directory
  --tacplus BIN     Use an existing tacplus binary
  --keep            Keep the temporary work directory
  -h, --help        Show this help

Environment:
  TACPLUS_BIN                 Existing tacplus binary to use
  TACPLUS_CONSUMER_WORKDIR    Work directory for generated outputs
  TACPLUS_CONSUMER_KEEP=true  Preserve generated outputs after success
  TACPLUS_PYTHON              Python interpreter for grpc_tools.protoc
USAGE
}

die() {
  echo "ERROR: $*" >&2
  exit 1
}

need_value() {
  local flag="$1"
  local value="${2:-}"
  [[ -n "$value" ]] || die "$flag requires a value"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --language)
      need_value "$1" "${2:-}"
      LANG_FILTER="$2"
      shift 2
      ;;
    --case)
      need_value "$1" "${2:-}"
      CASE_FILTER="$2"
      shift 2
      ;;
    --workdir)
      need_value "$1" "${2:-}"
      WORK_ROOT="$2"
      shift 2
      ;;
    --tacplus)
      need_value "$1" "${2:-}"
      TACPLUS_BIN="$2"
      shift 2
      ;;
    --keep)
      KEEP_WORK=true
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown argument: $1"
      ;;
  esac
done

[[ -d "$PROTO_SRC" ]] || die "consumer contract proto fixture not found: $PROTO_SRC"

if [[ -z "$WORK_ROOT" ]]; then
  WORK_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/tacplus-consumer-contract.XXXXXX")"
else
  mkdir -p "$WORK_ROOT"
  WORK_ROOT="$(cd "$WORK_ROOT" && pwd)"
fi

if [[ "$KEEP_WORK" != true ]]; then
  cleanup() {
    rm -rf "$WORK_ROOT"
  }
  trap cleanup EXIT
fi

if [[ -z "$TACPLUS_BIN" ]]; then
  TACPLUS_BIN="$WORK_ROOT/tacplus-local"
  echo "==> Building local tacplus binary"
  (cd "$ROOT_DIR" && go build -o "$TACPLUS_BIN" .)
fi

if [[ "$TACPLUS_BIN" == */* ]]; then
  TACPLUS_BIN="$(cd "$(dirname "$TACPLUS_BIN")" && pwd)/$(basename "$TACPLUS_BIN")"
fi

if [[ -n "$PYTHON_BIN" && "$PYTHON_BIN" == */* && ! -x "$PYTHON_BIN" ]]; then
  die "TACPLUS_PYTHON is not executable: $PYTHON_BIN"
fi

all_languages=(go java python rust typescript swift csharp)
all_cases=(c2s w2s both c2d disabled)

case "$LANG_FILTER" in
  "" ) languages=("${all_languages[@]}") ;;
  go|java|python|rust|typescript|swift|csharp ) languages=("$LANG_FILTER") ;;
  * ) die "unsupported language: $LANG_FILTER" ;;
esac

case "$CASE_FILTER" in
  "" ) cases=("${all_cases[@]}") ;;
  c2s|w2s|both|c2d|disabled ) cases=("$CASE_FILTER") ;;
  * ) die "unsupported case: $CASE_FILTER" ;;
esac

canonical_pkg() {
  if [[ "$1" == swift ]]; then
    printf 'com.gitlab.example.tacplus.consumer.contractclient'
  else
    printf 'com.gitlab.example.tacplus.consumer.contractclient'
  fi
}

case_enabled() {
  local case_name="$1"
  local surface="$2"
  case "$case_name:$surface" in
    c2s:c2s|both:c2s) printf true ;;
    w2s:w2s|both:w2s) printf true ;;
    c2d:c2d) printf true ;;
    *) printf false ;;
  esac
}

is_python_typescript_client_only() {
  local language="$1"
  [[ "$language" == python || "$language" == typescript ]]
}

# Positive client-surface cases per full-client language: everything except
# `disabled`, which keeps the FULL proto so service-side rejection stays covered.
is_positive_client_case() {
  local language="$1" case_name="$2"
  [[ "$case_name" != disabled ]]
}

is_full_service_language() {
  local language="$1"
  [[ "$language" == go || "$language" == rust || "$language" == java ]]
}

# Swift is a client-only (C2D) target whose generation ALWAYS shells out to the
# Swift protoc plugins (config.ResolveProtocPaths runs before any surface
# resolution). Without those plugins even the swift no-service_c2d rejection path
# cannot be reached, so swift is only runnable where the toolchain exists (CI).
# Locally the plugins are typically absent — skip swift cleanly rather than
# reporting a spurious toolchain failure. Set the PROTOC_SWIFT_BINARY /
# PROTOC_GRPC_SWIFT_BINARY env vars (or install the plugins on PATH) to run it.
swift_toolchain_available() {
  local msg_plugin="${PROTOC_SWIFT_BINARY:-}"
  local grpc_plugin="${PROTOC_GRPC_SWIFT_BINARY:-}"
  [[ -n "$msg_plugin" ]] || msg_plugin="$(command -v protoc-gen-swift || true)"
  [[ -n "$grpc_plugin" ]] || grpc_plugin="$(command -v protoc-gen-grpc-swift-2 || true)"
  [[ -n "$msg_plugin" && -n "$grpc_plugin" ]]
}

# apply_case_client_deps rewrites a contract/contract.proto in place so it
# declares ONLY the client dependency surface(s) that the given case expects.
# Client surfaces are now proto-driven (no YAML flags): the proto declaration IS
# the per-case expectation.
#   - grpc_c2s_client option  -> emits the c2sm surface
#   - grpc_w2s_client option  -> emits the w2sm surface
#   - service ContractDaemon (service_c2d) -> emits the c2dm surface
# Any grpc_s2s_client / ContractS2S / ContractApi (service_s2s / service_c2s)
# content is left untouched so full-service server generation stays covered.
apply_case_client_deps() {
  local proto="$1"
  local case_name="$2"
  local keep_c2s=false keep_w2s=false keep_c2d=false
  case "$case_name" in
    c2s)  keep_c2s=true ;;
    w2s)  keep_w2s=true ;;
    both) keep_c2s=true; keep_w2s=true ;;
    c2d)  keep_c2d=true ;;
    disabled) : ;; # no client deps at all
    *) die "apply_case_client_deps: unknown case $case_name" ;;
  esac

  [[ "$keep_c2s" == true ]] || perl -0pi -e 's/^option \(spec\.grpc_c2s_client\)[^\n]*\n//mg' "$proto"
  [[ "$keep_w2s" == true ]] || perl -0pi -e 's/^option \(spec\.grpc_w2s_client\)[^\n]*\n//mg' "$proto"
  # Drop the entire `service ContractDaemon { ... }` block (service_c2d) unless
  # this case keeps the C2D surface.
  [[ "$keep_c2d" == true ]] || perl -0pi -e 's/\nservice ContractDaemon \{.*?\n\}\n//s' "$proto"

  # Sanity: the transform must leave exactly the declarations the case expects.
  if [[ "$keep_c2s" == true ]]; then
    rg -n '\(spec\.grpc_c2s_client\)' "$proto" >/dev/null || die "case $case_name: expected grpc_c2s_client to remain in $proto"
  else
    ! rg -n '\(spec\.grpc_c2s_client\)' "$proto" >/dev/null || die "case $case_name: grpc_c2s_client should have been stripped from $proto"
  fi
  if [[ "$keep_w2s" == true ]]; then
    rg -n '\(spec\.grpc_w2s_client\)' "$proto" >/dev/null || die "case $case_name: expected grpc_w2s_client to remain in $proto"
  else
    ! rg -n '\(spec\.grpc_w2s_client\)' "$proto" >/dev/null || die "case $case_name: grpc_w2s_client should have been stripped from $proto"
  fi
  if [[ "$keep_c2d" == true ]]; then
    rg -n 'service ContractDaemon' "$proto" >/dev/null || die "case $case_name: expected ContractDaemon (service_c2d) to remain in $proto"
  else
    ! rg -n 'service ContractDaemon' "$proto" >/dev/null || die "case $case_name: ContractDaemon should have been stripped from $proto"
  fi
}

# prepare_full_service_case_proto copies the canonical rich proto fixture tree
# (with services/ vservices/ etc.) and rewrites contract/contract.proto so the
# client dependency surfaces match the case. Full-service server generation
# (ContractS2S / fixtures / eventbus) is unchanged, so assert_fixture_coverage
# and assert_eventbus_subscriber_meta_wiring stay meaningful per case.
prepare_full_service_case_proto() {
  local dst="$1"
  local case_name="$2"
  rm -rf "$dst"
  mkdir -p "$dst"
  cp -R "$PROTO_SRC/." "$dst/"
  apply_case_client_deps "$dst/contract/contract.proto" "$case_name"
}

prepare_client_only_proto() {
  local dst="$1"
  local case_name="$2"
  local language="${3:-}"
  rm -rf "$dst"
  mkdir -p "$dst/contract"
  cp "$PROTO_SRC/dependencies.yaml" "$dst/dependencies.yaml"
  # C2S client generation injects ServiceHealthState (clients resolve service
  # instances via health); reuse the canonical minimal ServiceHealth fixture.
  mkdir -p "$dst/platform"
  cp "$PROTO_SRC/platform/health.proto" "$dst/platform/health.proto"
  # The canonical fixture routes ServiceHealth at the "contractfixtures" service,
  # which lives under services/ and is NOT part of a client-only tree. Retarget
  # the routing label at the only service this tree loads (package contractclient)
  # so ValidateRoutingLabels resolves it.
  perl -pi -e 's/label: \["contractfixtures"\]/label: ["contractclient"]/' "$dst/platform/health.proto"
  cat > "$dst/registry.yaml" <<'EOF'
# Managed by tacplus — do not edit IDs manually.
services:
    - name: contractclient
      id: 1
EOF
  cat > "$dst/contract/contract.proto" <<'EOF'
// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

syntax = "proto3";

package contractclient;

import "sys/spec.proto";

option (spec.service) = {name: "contractclient"};
// Declared client dependencies. These option lines and the ContractDaemon
// service below are stripped per case by apply_case_client_deps so each case
// declares ONLY its expected surface(s) — client generation is proto-driven.
option (spec.grpc_c2s_client) = {service: "contractclient.ContractApi"};
option (spec.grpc_w2s_client) = {service: "contractclient.ContractApi"};

enum ContractMode {
  CONTRACT_MODE_UNSPECIFIED = 0;
  CONTRACT_MODE_READ = 1 [(spec.json_return_value) = "read"];
  CONTRACT_MODE_WRITE = 2 [(spec.json_return_value) = "write"];
}

message ContractRequest {
  string tenant_namespace = 1 [
    (spec.assert_required) = true,
    (spec.assert_min_size) = 1
  ];
  string item_id = 2 [
    (spec.assert_required) = true,
    (spec.assert_min_size) = 3
  ];
  ContractMode mode = 3 [(spec.assert_enum_defined) = true];
}

message ContractResponse {
  string result_id = 1 [(spec.assert_required) = true];
  bool accepted = 2;
}

message ContractEvent {
  string event_id = 1 [(spec.assert_required) = true];
  string tenant_namespace = 2 [(spec.assert_required) = true];
  string payload = 3;
}

// C2S service so full-client targets (python/typescript) generate and exercise
// their C2S/W2S/REST *client* surfaces positively (PAR-3) — the handwritten
// consumers reference contract_api_client. Mirrors the canonical full-contract
// ContractApi shape (entitled default + a public-no-auth method override).
service ContractApi {
  option (spec.service_c2s) = {auth_entitled_users: true};

  rpc GetContract(ContractRequest) returns (ContractResponse) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_USER, exposure: RPC_EXPOSURE_EXTERNAL, flags: [RPC_FLAG_AUTH_ENTITLED_USERS], target_namespace_field: "tenant_namespace"};
    option (spec.rest_method) = {methods: [REST_METHOD_GET]};
  }

  rpc GetPublicContract(ContractRequest) returns (ContractResponse) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_USER, exposure: RPC_EXPOSURE_EXTERNAL, flags: [RPC_FLAG_PUBLIC_NO_AUTH]};
    option (spec.rest_path) = "contract/public-read";
  }
}

service ContractDaemon {
  option (spec.service_c2d) = {socket_path: "/tmp/contract-daemon.sock"};

  rpc GetLocal(ContractRequest) returns (ContractResponse);
  rpc GenerateLocalEvents(ContractRequest) returns (stream ContractEvent);
}
EOF
  # C2S proxy token minter (the client-reachable mint FRONT). Client-only
  # targets have no S2S identity and cannot call the authoritative S2S mint;
  # client_im mints through this {proxy: true} minter as an ordinary C2S client
  # (the webapp path). Present in every case so the emitted client_im carries
  # its real mint path (HasClientTokenExchanger = true) and the compile/tsc gate
  # type-checks it. D-4 (IDENTITY_MANAGER_SPLIT_PLAN.md).
  #
  # Gated on languages that have landed client_im: a language whose client-only
  # validator still rejects proxy minters would fail here. python and typescript
  # have landed (D-4).
  if [[ "$language" == python || "$language" == typescript ]]; then
  cat > "$dst/contract/mintfront.proto" <<'EOF'
// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

syntax = "proto3";

package contractclient;

import "sys/spec.proto";
import "sys/tokenrequest.proto";

// Client-reachable (C2S) token-mint front. {proxy: true} marks it as the
// client mint the webapp/CLI reach; it forwards to the authoritative S2S mint
// with its own S2S identity. A client build generates only a C2S *client* stub
// to it (never the server), which client_im uses to mint tokens.
service ContractMintFront {
  option (spec.token_minter) = {proxy: true};

  rpc TokenMint(tokenrequest.TokenMintRequest) returns (tokenrequest.TokenMintResponse);
}
EOF
  fi
  apply_case_client_deps "$dst/contract/contract.proto" "$case_name"
}

prepare_missing_token_minter_proto() {
  local dst="$1"
  rm -rf "$dst"
  mkdir -p "$dst"
  cp -R "$PROTO_SRC/." "$dst/"
  perl -0pi -e 's/\n\s*option \(spec\.token_minter\) = \{\};\n/\n/' "$dst/services/contractfixtures.proto"
  if rg -n '\(spec\.token_minter\)' "$dst" >/dev/null; then
    die "missing-token-minter fixture still contains spec.token_minter"
  fi
}

write_config() {
  local dir="$1"
  local language="$2"
  local case_name="$3"
  local proto_src="$4"
  mkdir -p "$dir"
  # NOTE: clients.{c2s,w2s,c2d}.enabled is obsolete and now HARD-REJECTED by the
  # runner. Client surfaces are driven purely by proto declarations
  # (spec.grpc_c2s_client / spec.grpc_w2s_client / service_c2d). The per-case
  # proto is what selects surfaces now — see prepare_case_proto below.
  cat > "$dir/tacplus.yaml" <<EOF
language: "$language"
pkg: "$(canonical_pkg "$language")"
src: "$proto_src"
output-dir: "model"
EOF
}

surface_path_exists() {
  local output="$1"
  local surface="$2"
  find "$output" -type f -path "*/${surface}/*" -print -quit | grep -q .
}

assert_surface() {
  local output="$1"
  local surface="$2"
  local expected="$3"
  if [[ "$expected" == true ]]; then
    surface_path_exists "$output" "$surface" || die "expected $surface surface in $output"
  else
    if surface_path_exists "$output" "$surface"; then
      find "$output" -type f -path "*/${surface}/*" | sed 's/^/unexpected: /' >&2
      die "did not expect $surface surface in $output"
    fi
  fi
}

assert_swift_c2d_surface() {
  local output="$1"
  [[ -f "$output/Package.swift" ]] || die "expected Swift Package.swift in $output"
  find "$output/Sources" -type f -name C2DClient.swift -print -quit | grep -q . ||
    die "expected Swift C2DClient.swift in $output"
  find "$output/Sources" -type f -name '*.pb.swift' -print -quit | grep -q . ||
    die "expected Swift protobuf output in $output"
  find "$output/Sources" -type f -name '*.grpc.swift' -print -quit | grep -q . ||
    die "expected Swift gRPC output in $output"
}

assert_swift_c2s_surface() {
  local output="$1"
  [[ -f "$output/Package.swift" ]] || die "expected Swift Package.swift in $output"
  find "$output/Sources" -type f -name C2SClient.swift -print -quit | grep -q . ||
    die "expected Swift C2SClient.swift in $output"
  # C2S is native gRPC: the target service's grpc-swift client stubs must exist.
  find "$output/Sources" -type f -name '*.grpc.swift' -print -quit | grep -q . ||
    die "expected Swift gRPC output for C2S in $output"
  # The bearer-token seam must be wired: an interceptor injecting authorization.
  rg -n 'C2STokenProvider|C2SBearerTokenInterceptor|authorization' "$output/Sources"/**/C2SClient.swift >/dev/null ||
    rg -rn 'C2STokenProvider|C2SBearerTokenInterceptor' "$output/Sources" >/dev/null ||
    die "expected Swift C2S bearer-token seam in $output"
}

assert_swift_w2s_surface() {
  local output="$1"
  [[ -f "$output/Package.swift" ]] || die "expected Swift Package.swift in $output"
  find "$output/Sources" -type f -name W2SClient.swift -print -quit | grep -q . ||
    die "expected Swift W2SClient.swift in $output"
  # W2S is gRPC-over-REST via URLSession + protobuf-JSON.
  find "$output/Sources" -type f -name '*.pb.swift' -print -quit | grep -q . ||
    die "expected Swift protobuf output for W2S in $output"
  rg -rn 'W2STokenProvider|jsonUTF8Data|URLSession' "$output/Sources" >/dev/null ||
    die "expected Swift W2S URLSession/protobuf-JSON seam in $output"
}

assert_csharp_c2d_surface() {
  local output="$1"
  find "$output" -type f -name '*.csproj' -print -quit | grep -q . ||
    die "expected C# .csproj in $output"
  [[ -f "$output/C2DClient.cs" ]] || die "expected C# C2DClient.cs in $output"
  [[ -f "$output/NamedPipesConnectionFactory.cs" ]] ||
    die "expected C# NamedPipesConnectionFactory.cs in $output"
  find "$output/Protos" -type f -name '*.proto' -print -quit | grep -q . ||
    die "expected C# proto closure in $output/Protos"
}

assert_csharp_c2s_surface() {
  local output="$1"
  [[ -f "$output/C2SClient.cs" ]] || die "expected C# C2SClient.cs in $output"
  # C2S is native gRPC via Grpc.Net.Client: the bearer-token seam must be wired.
  rg -n 'C2STokenProvider|CallCredentials|authorization' "$output/C2SClient.cs" >/dev/null ||
    die "expected C# C2S bearer-token seam in $output/C2SClient.cs"
  # The c2s service protos must be emitted as client stubs in the .csproj.
  rg -rn 'GrpcServices="Client"' "$output"/*.csproj >/dev/null ||
    die "expected C# c2s Client gRPC stubs in the .csproj"
}

assert_csharp_w2s_surface() {
  local output="$1"
  [[ -f "$output/W2SClient.cs" ]] || die "expected C# W2SClient.cs in $output"
  # W2S is gRPC-over-REST via HttpClient + protobuf-JSON (JsonFormatter/JsonParser).
  rg -n 'HttpClient|JsonFormatter|JsonParser' "$output/W2SClient.cs" >/dev/null ||
    die "expected C# W2S HttpClient/protobuf-JSON seam in $output/W2SClient.cs"
}

assert_no_stale_generated_names() {
  local output="$1"
  if rg -n 'crypto\.JsonWebKey|webcrypto\.JsonWebKey|GrpcS2SClients\.java|class S2SClients|struct S2SClients|export class S2SClients|contract_s2_s|types\.services\.UNKNOWN|UNKNOWN_pb2_grpc|UNKNOWN\.Mint|TOKEN_EXCHANGER_PACKAGE = '\''UNKNOWN'\''|DEFAULT_AUDIENCE = '\''UNKNOWN'\''' "$output"; then
    die "stale generated symbol found in $output"
  fi

  if rg -n '%![A-Za-z]' "$output"; then
    die "fmt formatter artifact found in $output"
  fi

  if rg -n '^import org\.apache\.commons\.compress' "$output"; then
    die "Java generated source must not require commons-compress at compile time"
  fi
}

# assert_client_im_mint_path verifies the client-only build emitted client_im
# WITH its real C2S mint path wired to the {proxy: true} ContractMintFront minter
# (D-4). The client-only proto declares that proxy minter, so client_im must mint
# through it (HasClientTokenExchanger = true), not fall back to the disabled stub.
assert_client_im_mint_path() {
  local language="$1"
  local output="$2"
  case "$language" in
    go)
      # Go full-service builds that also expose a client surface (c2sm/c2dm/w2sm)
      # emit client_im. The canonical fixture declares an authoritative
      # (spec.token_minter) FixtureTokenMint; with no {proxy: true} front, the C2S
      # exchanger discovery falls back to it, so client_im mints through the
      # FixtureTokenMint C2S client stub (hasClientTokenExchanger = true), never
      # the disabled stub.
      local mgr="$output/chassis/client_im/manager.go"
      [[ -f "$mgr" ]] || die "go client_im manager missing: $mgr"
      rg -n 'hasClientTokenExchanger = true' "$mgr" >/dev/null ||
        die "go client_im did not wire the C2S mint path (hasClientTokenExchanger != true)"
      rg -n 'NewFixtureTokenMintClient' "$mgr" >/dev/null ||
        die "go client_im does not reference the FixtureTokenMint C2S mint client"
      rg -n '\.TokenMint\(' "$mgr" >/dev/null ||
        die "go client_im does not call the C2S TokenMint RPC"
      ;;
    python)
      local mgr="$output/contractclient_model/chassis/client_im/manager.py"
      [[ -f "$mgr" ]] || die "python client_im manager missing: $mgr"
      rg -n 'HAS_CLIENT_TOKEN_EXCHANGER = True' "$mgr" >/dev/null ||
        die "python client_im did not wire the C2S mint path (HAS_CLIENT_TOKEN_EXCHANGER != True)"
      rg -n 'ContractMintFrontStub' "$mgr" >/dev/null ||
        die "python client_im does not reference the ContractMintFront C2S mint stub"
      ;;
    typescript)
      local mgr="$output/chassis/client_im/ClientIdentityManager.ts"
      [[ -f "$mgr" ]] || die "typescript client_im manager missing: $mgr"
      rg -n 'HAS_CLIENT_TOKEN_EXCHANGER = true' "$mgr" >/dev/null ||
        die "typescript client_im did not wire the C2S mint path (HAS_CLIENT_TOKEN_EXCHANGER != true)"
      rg -n '/contractclient\.ContractMintFront/TokenMint' "$mgr" >/dev/null ||
        die "typescript client_im does not target the ContractMintFront C2S TokenMint method"
      ;;
    java)
      # Java full-service builds that also expose a client surface (c2sm/c2dm/w2sm)
      # emit client_im. Like go, the canonical fixture declares an authoritative
      # (spec.token_minter) FixtureTokenMint; with no {proxy: true} front, the C2S
      # exchanger discovery falls back to it, so client_im mints through the
      # FixtureTokenMintGrpc C2S client stub (HAS_CLIENT_TOKEN_EXCHANGER = true),
      # never the disabled stub.
      local mgr
      mgr="$(find "$output/src/main/java" -type f -path '*/chassis/client_im/ClientIdentityManager.java' -print -quit)"
      [[ -n "$mgr" && -f "$mgr" ]] || die "java client_im manager missing under $output/src/main/java"
      rg -n 'HAS_CLIENT_TOKEN_EXCHANGER = true' "$mgr" >/dev/null ||
        die "java client_im did not wire the C2S mint path (HAS_CLIENT_TOKEN_EXCHANGER != true)"
      rg -n 'FixtureTokenMintGrpc' "$mgr" >/dev/null ||
        die "java client_im does not reference the FixtureTokenMint C2S mint stub"
      rg -n '\.tokenMint\(' "$mgr" >/dev/null ||
        die "java client_im does not call the C2S TokenMint RPC"
      ;;
  esac
}

assert_fixture_coverage() {
  local output="$1"
  local fixture_proto="$output/proto/services/contractfixtures.proto"
  local generated_services_proto="$output/proto/generated/services.proto"
  [[ -f "$fixture_proto" ]] || die "fixture proto missing from generated output: $fixture_proto"
  [[ -f "$generated_services_proto" ]] || die "generated services proto missing from generated output: $generated_services_proto"

  local patterns=(
    'service FixtureStateService'
    'rpc PublishToEvents'
    'rpc SubscribeToEvents'
    'service FixtureStoreEntryStoreService'
    'rpc Put\(FixtureStoreEntry\)'
    'rpc List\(trace\.TraceRequest\)'
    'message FixtureHealth'
    'service FixtureTokenMint'
    'rpc TokenMint\(tokenrequest\.TokenMintRequest\)'
    'rpc MintServiceToken\(tokenrequest\.MintServiceTokenRequest\)'
    'rpc X509Mint\(tokenrequest\.X509MintRequest\) returns \(x509\.X509CertChain\)'
  )

  local pattern
  for pattern in "${patterns[@]}"; do
    rg -n "$pattern" "$fixture_proto" >/dev/null ||
      die "fixture coverage marker missing from generated proto: $pattern"
  done

  local vservice_patterns=(
    'enum ContracttopicsRPCs'
    'CONTRACTTOPICS_RPCS_PUBLISH_CONTRACTTOPICS_HOSTED_EVENT_BUS_EVENTS'
    'CONTRACTTOPICS_RPCS_SUBSCRIBE_CONTRACTTOPICS_HOSTED_EVENT_BUS_EVENTS'
  )
  for pattern in "${vservice_patterns[@]}"; do
    rg -n "$pattern" "$generated_services_proto" >/dev/null ||
      die "vservice fixture coverage marker missing from generated/services.proto: $pattern"
  done
}

assert_w2s_validation_wiring() {
  local language="$1"
  local output="$2"
  local w2s_dir="$output/w2sm"
  if [[ "$language" == java ]]; then
    w2s_dir="$output"
  fi
  [[ -d "$w2s_dir" ]] || return 0

  case "$language" in
    go)
      rg -n 'ValidateContract(Request|Response|Event)' "$w2s_dir" >/dev/null ||
        die "Go W2S output does not wire contract validators"
      ;;
    rust)
      rg -n 'validation failed|response validation failed|\\.validate\\(\\)' "$w2s_dir" >/dev/null ||
        die "Rust W2S output does not wire contract validators"
      ;;
    java)
      rg -n 'Helpers\.validateContract(Request|Response|Event)' "$w2s_dir" >/dev/null ||
        die "Java W2S output does not wire contract validators"
      ;;
    python)
      rg -n 'validate_contract_(request|response|event)' "$w2s_dir" >/dev/null ||
        die "Python W2S output does not wire contract validators"
      ;;
    typescript)
      rg -n 'types\.validateContract(Request|Response|Event)' "$w2s_dir" >/dev/null ||
        die "TypeScript W2S output does not wire contract validators"
      ;;
  esac
}

assert_service_wrapper_validation_wiring() {
  local language="$1"
  local output="$2"
  [[ -d "$output" ]] || return 0

  case "$language" in
    go)
      rg -n 'Validating(ServerStreaming|ClientStreaming|BidiStreaming)Server|ValidateContract(Request|Response|Event)' "$output" >/dev/null ||
        die "Go service wrappers do not wire streaming validators"
      ;;
    rust)
      rg -n 'Request::from_parts|response validation failed|validation failed|\\.validate\\(\\)' "$output" >/dev/null ||
        die "Rust service wrappers do not wire request/response validators"
      ;;
    java)
      rg -n 'validating(Request|Response)Observer|responseValidationError|Helpers\.validateContract(Request|Response|Event)' "$output" >/dev/null ||
        die "Java service wrappers do not wire request/response validators"
      ;;
    python)
      rg -n '_validating_request_iterator|_abort_response_validation|validate_contract_(request|response|event)' "$output" >/dev/null ||
        die "Python service wrappers do not wire request/response validators"
      ;;
    typescript)
      rg -n 'wrapReadableForRequestValidation|wrapWritableForResponseValidation|validateResponse|types\.validateContract(Request|Response|Event)' "$output" >/dev/null ||
        die "TypeScript service wrappers do not wire request/response validators"
      ;;
  esac
}

assert_service_manager_auth_wiring() {
  local language="$1"
  local output="$2"
  [[ -d "$output/s2sm" ]] || return 0
  # Full-client outputs never carry server wrappers (ResolveServerServices
  # returns nil for client targets); their s2sm dir holds only client-side
  # state/subscription machinery. Server auth wiring applies to server
  # wrappers, so require it only when they exist.
  if is_python_typescript_client_only "$language" && [[ ! -d "$output/s2sm/internal/grpc/servers" ]]; then
    return 0
  fi

  require_generated_pattern() {
    local root="$1"
    local pattern="$2"
    local message="$3"
    rg -n "$pattern" "$root" >/dev/null || die "$message"
  }

  case "$language" in
    go)
      require_generated_pattern "$output/s2sm" 'RegisterContractApiMethodAuth\(sm\.auth\)' "Go service manager does not register ContractApi auth metadata"
      require_generated_pattern "$output/s2sm" 'SetMethodTargetNamespaceField\("contractclient\.ContractApi/GetContract", "tenant_namespace"\)' "Go service wrapper does not register target namespace metadata"
      require_generated_pattern "$output/s2sm" 'SetAuthorizeFunc\(sm\.authorizeFunc\)' "Go service manager does not wire authorizer callbacks"
      require_generated_pattern "$output/s2sm" 'SetAuthenticateFunc\(sm\.authenticateFunc\)' "Go service manager does not wire authenticator callbacks"
      require_generated_pattern "$output/s2sm" 'HasAuthorizer\(\)' "Go service manager does not assert authorizer startup wiring"
      # NEW-21: isPermanentIdentityInitError is private to the generated sm
      # package, so the only thing that can execute it is the self-test emitted
      # beside s2sm.go. `go test ./...` below runs whatever test files exist, so
      # dropping the emission would silently drop the coverage — assert the file.
      [[ -n "$(find "$output/s2sm" -type f -name 's2sm_identity_error_test.go' -print -quit)" ]] ||
        die "Go service manager is missing its startup-classifier self-test (s2sm_identity_error_test.go)"
      ;;
    rust)
      require_generated_pattern "$output/s2sm" 'register_method_auth\(&self\.authorizer\)' "Rust service manager does not register method auth metadata"
      require_generated_pattern "$output/s2sm" 'set_authorize_func\(Some' "Rust service manager does not wire authorizer callbacks"
      require_generated_pattern "$output/s2sm" 'set_authenticate_func\(Some' "Rust service manager does not wire authenticator callbacks"
      require_generated_pattern "$output/s2sm" 'has_authorizer\(\)\.await' "Rust service manager does not assert authorizer startup wiring"
      require_generated_pattern "$output/s2sm" 'check_at_or_below|tenant_namespace' "Rust service wrapper does not enforce target namespace metadata"
      ;;
    java)
      require_generated_pattern "$output" 'new Authorizer\(' "Java service manager does not create the generated authorizer"
      require_generated_pattern "$output" 'registerMethodAuth\(authorizer\)' "Java service manager does not register method auth metadata"
      require_generated_pattern "$output" 'setMethodTargetNamespaceField\("ContractApi/GetContract", "tenant_namespace"\)' "Java service wrapper does not register target namespace metadata"
      ;;
    python)
      require_generated_pattern "$output/s2sm" 'Authorizer\(' "Python service manager does not create the generated authorizer"
      require_generated_pattern "$output/s2sm" 'register_method_auth\(authorizer\)' "Python service manager does not register method auth metadata"
      require_generated_pattern "$output/s2sm" 'set_method_target_namespace_field\("contractclient\.ContractApi/GetContract", "tenant_namespace"\)' "Python service wrapper does not register target namespace metadata"
      require_generated_pattern "$output/s2sm" 'set_authorize_func\(' "Python service manager does not wire authorizer callbacks"
      require_generated_pattern "$output/s2sm" 'set_authenticate_func\(' "Python service manager does not wire authenticator callbacks"
      require_generated_pattern "$output/s2sm" 'has_authorizer\(\)' "Python service manager does not assert authorizer startup wiring"
      ;;
    typescript)
      require_generated_pattern "$output/s2sm" 'new Authorizer\(' "TypeScript service manager does not create the generated authorizer"
      require_generated_pattern "$output/s2sm" 'registerMethodAuth\(authorizer\)' "TypeScript service manager does not register method auth metadata"
      require_generated_pattern "$output/s2sm" 'setMethodTargetNamespaceField\("contractclient\.ContractApi/GetContract", "tenant_namespace"\)' "TypeScript service wrapper does not register target namespace metadata"
      require_generated_pattern "$output/s2sm" 'authorizer\.authorize\(metadata, fullMethod, req\)' "TypeScript service manager does not pass requests to the authorizer"
      require_generated_pattern "$output/s2sm" 'setAuthenticateFunc\(' "TypeScript service manager does not wire authenticator callbacks"
      require_generated_pattern "$output/s2sm" 'hasAuthorizer\(\)' "TypeScript service manager does not assert authorizer startup wiring"
      ;;
  esac
}

assert_eventbus_subscriber_meta_wiring() {
  local language="$1"
  local output="$2"

  require_generated_pattern() {
    local root="$1"
    local pattern="$2"
    local message="$3"
    rg -n "$pattern" "$root" >/dev/null || die "$message"
  }

  case "$language" in
    go|rust|java)
      if [[ -d "$output/proto" ]]; then
        require_generated_pattern "$output/proto" 'message EventBusRecordMeta \{ bytes key = 1; string topic = 2; int32 partition = 3; int64 offset = 4; google\.protobuf\.Timestamp timestamp = 5;' "generated EventBus proto does not define metadata with the documented wire fields"
        require_generated_pattern "$output/proto" 'rpc SubscribeTo[A-Za-z0-9]+\(stream eventbus_support\.SubscribeRequest\) returns \(stream [A-Za-z0-9]+SubscribeRecord\);' "generated topic Subscribe RPC does not stream a metadata-bearing envelope"
        require_generated_pattern "$output/proto" 'message [A-Za-z0-9]+SubscribeRecord \{ EventBusRecordMeta meta = 1; [A-Za-z0-9_.]+ payload = 2;' "generated topic Subscribe envelope does not expose meta field 1 and typed payload field 2"
        if rg -n 'rpc SubscribeTo[A-Za-z0-9]+\(stream eventbus_support\.SubscribeRequest\) returns \(stream FixtureEvent\);' "$output/proto" >/dev/null; then
          die "generated topic Subscribe RPC still streams the bare fixture event payload"
        fi
      fi
      ;;
  esac

  case "$language" in
    go)
      [[ -d "$output/s2sm/subscribe" ]] || return 0
      require_generated_pattern "$output/s2sm" 'decodeSubscribeRecord\(data\)' "Go EventBus subscribers do not decode metadata-bearing subscribe records"
      require_generated_pattern "$output/s2sm" 'proto\.Unmarshal\(metaBytes, r\.Meta\)' "Go EventBus subscribers do not populate RecordMeta from the wire envelope"
      ;;
    rust)
      [[ -d "$output/s2sm/subscribe" ]] || return 0
      require_generated_pattern "$output/s2sm" 'struct SubscribeRecord' "Rust EventBus subscribers do not model metadata-bearing subscribe records"
      require_generated_pattern "$output/s2sm" 'decode_subscribe_record\(raw\.as_ref\(\)\)' "Rust EventBus subscribers do not decode RecordMeta from raw response frames"
      ;;
    java)
      find "$output" -path '*/subscribe/*Subscriber.java' -print -quit | rg . >/dev/null || return 0
      require_generated_pattern "$output" 'DecodedRecord decoded = decodeSubscribeRecord\(response\);' "Java EventBus subscribers do not decode metadata-bearing subscribe records"
      require_generated_pattern "$output" 'decodeRecordMeta\(envelope\.meta\)' "Java EventBus subscribers do not populate subscription metadata from the wire envelope"
      ;;
  esac
}

assert_no_rust_legacy_publishers() {
  local language="$1"
  local output="$2"
  [[ "$language" == rust && -d "$output/s2sm" ]] || return 0

  if rg -n 'create_(trace|llm_event|scm_event|git_lab_connector_event)_publisher' "$output/s2sm"; then
    die "Rust generated output still contains legacy event-type publisher helpers"
  fi
}

write_go_compile_mod() {
  local mod="$1"
  cat > "$mod" <<'EOF'
module gitlab.com/example/tacplus/consumer/contractclient

go 1.23

require (
	github.com/caddyserver/certmagic v0.25.1
	github.com/dsnet/compress v0.0.2-0.20230904184137-39efe44ab707
	github.com/gin-contrib/cors v1.7.6
	github.com/gin-contrib/zap v1.1.6
	github.com/gin-gonic/gin v1.11.0
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/hashicorp/go-multierror v1.1.1
	github.com/jinzhu/copier v0.4.0
	github.com/open-policy-agent/opa v1.13.1
	github.com/pelletier/go-toml/v2 v2.2.4
	github.com/soheilhy/cmux v0.1.5
	go.uber.org/zap v1.27.1
	golang.org/x/crypto v0.47.0
	golang.org/x/net v0.62.0
	golang.org/x/sync v0.19.0
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa
	google.golang.org/grpc v1.83.2
	google.golang.org/protobuf v1.36.11
	gopkg.in/yaml.v3 v3.0.1
)
EOF
}

write_go_consumer_mod() {
  local mod="$1"
  cat > "$mod" <<'EOF'
module gitlab.com/example/tacplus/consumer/contractclient/handwritten-go

go 1.23

require gitlab.com/example/tacplus/consumer/contractclient v0.0.0

replace gitlab.com/example/tacplus/consumer/contractclient => ..
EOF
}

write_go_c2s_consumer_test() {
  local test_file="$1"
  cat > "$test_file" <<'EOF'
package consumer

import (
	"context"
	"testing"

	c2sm "gitlab.com/example/tacplus/consumer/contractclient/model/c2sm"
	types "gitlab.com/example/tacplus/consumer/contractclient/model/types/contract/contractclient"
	"google.golang.org/grpc"
)

type fakeConn struct{}

func (fakeConn) Invoke(context.Context, string, any, any, ...grpc.CallOption) error { return nil }
func (fakeConn) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, nil
}

func validRequest() *types.ContractRequest {
	return &types.ContractRequest{
		TenantNamespace: "tenant.alpha",
		ItemId:          "item-123",
		Mode:            types.ContractMode_CONTRACT_MODE_READ,
		Tags:            []string{"one"},
		Metadata:        map[string]string{"owner": "consumer"},
	}
}

func TestC2SPublicAPICompiles(t *testing.T) {
	if err := types.ValidateContractRequest(validRequest()); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	sm := c2sm.New(fakeConn{}, func() (string, error) { return "token", nil })
	if sm.Conn() == nil {
		t.Fatal("expected underlying connection")
	}
	api := sm.Grpc().Clients().Contract().ContractApiClient()
	if api == nil {
		t.Fatal("expected ContractApi client")
	}
	// PC-C-3: actually CALL the PAGE method as a client. The generated PAGE client
	// is an auto-pager (iter.Seq2[*Item, error]): ranging over it invokes the RPC,
	// reads a page, and yields items. fakeConn.Invoke returns an empty page with no
	// next_page_token, so the pager runs once and terminates cleanly — exercising the
	// client wiring, marshaling, and method path. Previously no smoke called a PAGE
	// method as a client in any language.
	for _, err := range api.ListEventsPaged(context.Background(), &types.ContractPageRequest{
		TenantNamespace: "tenant.alpha",
		PageSize:        50,
	}) {
		if err != nil {
			t.Fatalf("ListEventsPaged (PAGE) iteration error: %v", err)
		}
	}
}
EOF
}

write_go_w2s_consumer_test() {
  local test_file="$1"
  cat > "$test_file" <<'EOF'
package consumer

import (
	"context"
	"testing"
	"time"

	w2sm "gitlab.com/example/tacplus/consumer/contractclient/model/w2sm"
	types "gitlab.com/example/tacplus/consumer/contractclient/model/types/contract/contractclient"
)

func validRequest() *types.ContractRequest {
	return &types.ContractRequest{
		TenantNamespace: "tenant.alpha",
		ItemId:          "item-123",
		Mode:            types.ContractMode_CONTRACT_MODE_READ,
		Tags:            []string{"one"},
		Metadata:        map[string]string{"owner": "consumer"},
	}
}

func TestW2SPublicAPICompiles(t *testing.T) {
	if err := types.ValidateContractRequest(validRequest()); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	sm := w2sm.WithTokenProvider("https://gateway.example", func(context.Context) (string, error) {
		return "token", nil
	})
	if sm.BaseURL() != "https://gateway.example" {
		t.Fatalf("unexpected base URL %q", sm.BaseURL())
	}
	api := sm.W2SClients().Contractclient().ContractApiW2SClient().WithTimeout(time.Second)
	if api == nil {
		t.Fatal("expected ContractApi W2S client")
	}
}
EOF
}

write_go_both_consumer_test() {
  local test_file="$1"
  cat > "$test_file" <<'EOF'
package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	am "gitlab.com/example/tacplus/consumer/contractclient/model/chassis/am"
	c2sm "gitlab.com/example/tacplus/consumer/contractclient/model/c2sm"
	grpcserver "gitlab.com/example/tacplus/consumer/contractclient/model/chassis/grpcserver"
	"gitlab.com/example/tacplus/consumer/contractclient/model/chassis/collection"
	"gitlab.com/example/tacplus/consumer/contractclient/model/chassis/servicemanager"
	wrapperToken "gitlab.com/example/tacplus/consumer/contractclient/model/chassis/token"
	s2sm "gitlab.com/example/tacplus/consumer/contractclient/model/s2sm"
	auditserver "gitlab.com/example/tacplus/consumer/contractclient/model/s2sm/grpc/servers/services/contractclient"
	server "gitlab.com/example/tacplus/consumer/contractclient/model/s2sm/grpc/servers/contract/contractclient"
	services_pb "gitlab.com/example/tacplus/consumer/contractclient/model/types/generated/services"
	accounting_pb "gitlab.com/example/tacplus/consumer/contractclient/model/types/sys/accounting"
	trace_pb "gitlab.com/example/tacplus/consumer/contractclient/model/types/sys/trace"
	audittypes "gitlab.com/example/tacplus/consumer/contractclient/model/types/services/contractclient"
	types "gitlab.com/example/tacplus/consumer/contractclient/model/types/contract/contractclient"
	w2sm "gitlab.com/example/tacplus/consumer/contractclient/model/w2sm"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protojson"
	"golang.org/x/net/websocket"
)

type fakeConn struct{}

func (fakeConn) Invoke(context.Context, string, any, any, ...grpc.CallOption) error { return nil }
func (fakeConn) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, nil
}

type serviceHandler struct{}

func (serviceHandler) Ping(context.Context, *servicemanager.Caller, *types.ContractRequest) (*types.ContractResponse, error) {
	return validResponse(), nil
}
func (serviceHandler) GetContract(context.Context, *servicemanager.Caller, *types.ContractRequest) (*types.ContractResponse, error) {
	return validResponse(), nil
}
func (serviceHandler) GetPublicContract(context.Context, *servicemanager.Caller, *types.ContractRequest) (*types.ContractResponse, error) {
	return validResponse(), nil
}
func (serviceHandler) ListEvents(*servicemanager.Caller, *types.ContractRequest) (iter.Seq2[*types.ContractEvent, error], error) {
	return emptyEventSeq(), nil
}
func (serviceHandler) ListEventsPaged(context.Context, *servicemanager.Caller, *types.ContractPageRequest, collection.PageParams) (collection.Page[*types.ContractEvent], error) {
	return collection.Page[*types.ContractEvent]{}, nil
}
func (serviceHandler) Watch(*servicemanager.Caller, *types.ContractWatchRequest) (*collection.WatchSource[types.ContractEvent, types.ContractWatchChange], error) {
	return emptyWatchSource(), nil
}
func (serviceHandler) SendEvents(*servicemanager.Caller, grpc.ClientStreamingServer[types.ContractEvent, types.ContractResponse]) error {
	return nil
}
func (serviceHandler) AssistEvents(*servicemanager.Caller, grpc.BidiStreamingServer[types.ContractEvent, types.ContractEvent]) error {
	return nil
}

type s2sServiceHandler struct{}

func (s2sServiceHandler) Ping(context.Context, *servicemanager.Caller, *types.ContractRequest) (*types.ContractResponse, error) {
	return validResponse(), nil
}
func (s2sServiceHandler) GenerateEvents(*servicemanager.Caller, *types.ContractRequest, grpc.ServerStreamingServer[types.ContractEvent]) error {
	return nil
}
func (s2sServiceHandler) SendEvents(*servicemanager.Caller, grpc.ClientStreamingServer[types.ContractEvent, types.ContractResponse]) error {
	return nil
}
func (s2sServiceHandler) AssistEvents(*servicemanager.Caller, grpc.BidiStreamingServer[types.ContractEvent, types.ContractEvent]) error {
	return nil
}

type validationServiceHandler struct {
	serviceHandler
	readCalls       *int
	publicReadCalls *int
	listEventsCalls *int
	watchCalls      *int
}

func (h validationServiceHandler) GetContract(ctx context.Context, caller *servicemanager.Caller, req *types.ContractRequest) (*types.ContractResponse, error) {
	*h.readCalls++
	return validResponse(), nil
}

func (h validationServiceHandler) GetPublicContract(ctx context.Context, caller *servicemanager.Caller, req *types.ContractRequest) (*types.ContractResponse, error) {
	*h.publicReadCalls++
	if req.GetItemId() == "bad-response" {
		return &types.ContractResponse{Accepted: true}, nil
	}
	return validResponse(), nil
}

func (h validationServiceHandler) ListEvents(caller *servicemanager.Caller, req *types.ContractRequest) (iter.Seq2[*types.ContractEvent, error], error) {
	if h.listEventsCalls != nil {
		*h.listEventsCalls++
	}
	if req.GetItemId() == "bad-event" {
		return singleEventSeq(&types.ContractEvent{TenantNamespace: "tenant.alpha", Payload: "missing-id"}), nil
	}
	return singleEventSeq(validEvent()), nil
}

func (h validationServiceHandler) ListEventsPaged(_ context.Context, caller *servicemanager.Caller, req *types.ContractPageRequest, page collection.PageParams) (collection.Page[*types.ContractEvent], error) {
	// One full page, then exhausted (nil NextAfter -> empty next_page_token).
	return collection.Page[*types.ContractEvent]{Items: []*types.ContractEvent{validEvent()}, Total: 1}, nil
}

func (h validationServiceHandler) Watch(caller *servicemanager.Caller, req *types.ContractWatchRequest) (*collection.WatchSource[types.ContractEvent, types.ContractWatchChange], error) {
	*h.watchCalls++
	if req.GetItemId() == "bad-event" {
		return watchSource(&types.ContractEvent{TenantNamespace: "tenant.alpha", Payload: "missing-id"}), nil
	}
	return watchSource(validEvent()), nil
}

type appHandler struct{ serviceHandler }

func (a appHandler) ContractS2SGrpcImpl() server.ContractS2SHandler { return s2sServiceHandler{} }
func (a appHandler) ContractApiGrpcImpl() server.ContractApiHandler { return a.serviceHandler }
func (appHandler) ContractAuditApiGrpcImpl() auditserver.ContractAuditApiHandler { return auditServiceHandler{} }
func (appHandler) Shutdown(context.Context)                         {}

type auditServiceHandler struct{}

func (auditServiceHandler) ExecuteAudit(ctx context.Context, caller *servicemanager.Caller, req *audittypes.AuditRequest) (*audittypes.AuditResponse, error) {
	if req.GetItemId() == "err" {
		return nil, status.Error(codes.Internal, "audit unary failed")
	}
	return &audittypes.AuditResponse{ResultId: "audit-result"}, nil
}

func (auditServiceHandler) GenerateAuditEvents(caller *servicemanager.Caller, req *audittypes.AuditRequest, stream grpc.ServerStreamingServer[audittypes.AuditEvent]) error {
	if req.GetItemId() == "err" {
		return status.Error(codes.Internal, "audit stream failed")
	}
	return stream.Send(&audittypes.AuditEvent{EventId: "audit-event"})
}

func (auditServiceHandler) SendAuditEvents(caller *servicemanager.Caller, stream grpc.ClientStreamingServer[audittypes.AuditEvent, audittypes.AuditResponse]) error {
	for {
		event, err := stream.Recv()
		if err == io.EOF {
			return stream.SendAndClose(&audittypes.AuditResponse{ResultId: "audit-sendEvents"})
		}
		if err != nil {
			return err
		}
		if event.GetEventId() == "err" {
			return status.Error(codes.Internal, "audit sendEvents failed")
		}
	}
}

func (auditServiceHandler) AssistAuditEvents(caller *servicemanager.Caller, stream grpc.BidiStreamingServer[audittypes.AuditEvent, audittypes.AuditEvent]) error {
	for {
		event, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if event.GetEventId() == "err" {
			return status.Error(codes.Internal, "audit assistEvents failed")
		}
		if err := stream.Send(&audittypes.AuditEvent{EventId: "audit-reply"}); err != nil {
			return err
		}
	}
}

func validRequest() *types.ContractRequest {
	return &types.ContractRequest{
		TenantNamespace: "tenant.alpha",
		ItemId:          "item-123",
		Mode:            types.ContractMode_CONTRACT_MODE_READ,
		Tags:            []string{"one"},
		Metadata:        map[string]string{"owner": "consumer"},
	}
}

func invalidRequest() *types.ContractRequest {
	return &types.ContractRequest{
		TenantNamespace: "tenant.alpha",
		ItemId:          "no",
		Mode:            types.ContractMode_CONTRACT_MODE_READ,
	}
}

func validWatchRequest() *types.ContractWatchRequest {
	return &types.ContractWatchRequest{
		TenantNamespace: "tenant.alpha",
		ItemId:          "item-123",
		Mode:            types.ContractMode_CONTRACT_MODE_READ,
		FromRevision:    0,
	}
}

func invalidWatchRequest() *types.ContractWatchRequest {
	return &types.ContractWatchRequest{
		TenantNamespace: "tenant.alpha",
		ItemId:          "no",
		Mode:            types.ContractMode_CONTRACT_MODE_READ,
	}
}

func validResponse() *types.ContractResponse {
	return &types.ContractResponse{ResultId: "result-123", Accepted: true}
}

func validEvent() *types.ContractEvent {
	return &types.ContractEvent{EventId: "event-1", TenantNamespace: "tenant.alpha", Payload: "ok"}
}

func validAuditRequest() *audittypes.AuditRequest {
	return &audittypes.AuditRequest{ItemId: "audit-123"}
}

func failingAuditRequest() *audittypes.AuditRequest {
	return &audittypes.AuditRequest{ItemId: "err"}
}

func validAuditEvent() *audittypes.AuditEvent {
	return &audittypes.AuditEvent{EventId: "audit-event"}
}

func failingAuditEvent() *audittypes.AuditEvent {
	return &audittypes.AuditEvent{EventId: "err"}
}

func emptyEventSeq() iter.Seq2[*types.ContractEvent, error] {
	return func(yield func(*types.ContractEvent, error) bool) {}
}

func singleEventSeq(event *types.ContractEvent) iter.Seq2[*types.ContractEvent, error] {
	return func(yield func(*types.ContractEvent, error) bool) {
		_ = yield(event, nil)
	}
}

func emptyWatchChangeSeq() iter.Seq2[*types.ContractWatchChange, error] {
	return func(yield func(*types.ContractWatchChange, error) bool) {}
}

func emptyWatchSource() *collection.WatchSource[types.ContractEvent, types.ContractWatchChange] {
	return &collection.WatchSource[types.ContractEvent, types.ContractWatchChange]{
		Scope:    "tenant.alpha",
		Snapshot: emptyEventSeq(),
		Revision: 1,
		Changes:  emptyWatchChangeSeq,
	}
}

func watchSource(event *types.ContractEvent) *collection.WatchSource[types.ContractEvent, types.ContractWatchChange] {
	return &collection.WatchSource[types.ContractEvent, types.ContractWatchChange]{
		Scope:    "tenant.alpha",
		Snapshot: singleEventSeq(event),
		Revision: 1,
		Changes:  emptyWatchChangeSeq,
	}
}

func validChange(revision int64) *types.ContractWatchChange {
	return &types.ContractWatchChange{
		Type: types.ContractChangeType_CONTRACT_CHANGE_TYPE_MODIFY,
		Item: &types.ContractEvent{
			EventId:          "event-" + strconv.FormatInt(revision, 10),
			TenantNamespace:  "tenant.alpha",
			Payload:          "ok",
		},
		Revision: revision,
	}
}

func changeSeq(changes ...*types.ContractWatchChange) func() iter.Seq2[*types.ContractWatchChange, error] {
	return func() iter.Seq2[*types.ContractWatchChange, error] {
		return func(yield func(*types.ContractWatchChange, error) bool) {
			for _, change := range changes {
				if !yield(change, nil) {
					return
				}
			}
		}
	}
}

func blockingChangeSeq(done <-chan struct{}) func() iter.Seq2[*types.ContractWatchChange, error] {
	return func() iter.Seq2[*types.ContractWatchChange, error] {
		return func(yield func(*types.ContractWatchChange, error) bool) {
			<-done
		}
	}
}

func firstFromSeq[T any](seq iter.Seq2[*T, error]) (*T, error) {
	if seq == nil {
		return nil, io.EOF
	}
	var first *T
	var err error
	seen := false
	seq(func(item *T, seqErr error) bool {
		if seqErr != nil {
			err = seqErr
			return false
		}
		first = item
		seen = true
		return false
	})
	if err != nil {
		return nil, err
	}
	if !seen {
		return nil, io.EOF
	}
	return first, nil
}

func collectSeq[T any](seq iter.Seq2[*T, error]) ([]*T, error) {
	if seq == nil {
		return nil, nil
	}
	var items []*T
	var err error
	seq(func(item *T, seqErr error) bool {
		if seqErr != nil {
			err = seqErr
			return false
		}
		items = append(items, item)
		return true
	})
	return items, err
}

func watchSnapshot(event *types.ContractEvent) *types.ContractWatchEvent {
	return &types.ContractWatchEvent{
		Event: &types.ContractWatchEvent_SnapshotItem{SnapshotItem: event},
	}
}

func validWatchEvent() *types.ContractWatchEvent {
	return watchSnapshot(validEvent())
}

func requireW2SFrame(t *testing.T, ws *websocket.Conn, frameType string, authorization string) map[string]any {
	t.Helper()
	var frame map[string]any
	if err := websocket.JSON.Receive(ws, &frame); err != nil {
		t.Errorf("receive W2S frame: %v", err)
		return nil
	}
	if got, _ := frame["type"].(string); got != frameType {
		t.Errorf("unexpected W2S frame type %q in %+v", got, frame)
		return nil
	}
	if authorization == "" {
		if got, ok := frame["authorization"]; ok {
			t.Errorf("unexpected W2S frame authorization %v", got)
			return nil
		}
	} else if got, _ := frame["authorization"].(string); got != authorization {
		t.Errorf("unexpected W2S frame authorization %q", got)
		return nil
	}
	return frame
}

func requireW2SEvent(t *testing.T, raw any) *types.ContractEvent {
	t.Helper()
	data, err := json.Marshal(raw)
	if err != nil {
		t.Errorf("marshal W2S event payload: %v", err)
		return nil
	}
	var event types.ContractEvent
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, &event); err != nil {
		t.Errorf("decode W2S event payload: %v", err)
		return nil
	}
	if err := types.ValidateContractEvent(&event); err != nil {
		t.Errorf("invalid W2S event payload: %v", err)
		return nil
	}
	return &event
}

func TestGoBothHandwrittenConsumerCompiles(t *testing.T) {
	if err := types.ValidateContractRequest(validRequest()); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	if err := types.ValidateContractResponse(validResponse()); err != nil {
		t.Fatalf("valid response rejected: %v", err)
	}
	if _, err := types.ParseContractMode("read"); err != nil {
		t.Fatalf("enum parser rejected read: %v", err)
	}

	c2s := c2sm.New(fakeConn{}, func() (string, error) { return "token", nil })
	if c2s.Grpc().Clients().Contract().ContractApiClient() == nil {
		t.Fatal("expected C2S ContractApi client")
	}

	w2s := w2sm.WithTokenProvider("https://gateway.example", func(context.Context) (string, error) {
		return "token", nil
	})
	if w2s.W2SClients().Contractclient().ContractApiW2SClient().WithTimeout(time.Second) == nil {
		t.Fatal("expected W2S ContractApi client")
	}

	var _ s2sm.Handler = appHandler{serviceHandler: serviceHandler{}}

	grpcSrv := grpc.NewServer()
	defer grpcSrv.Stop()
	apiWrapper := server.NewContractApi(serviceHandler{})
	apiWrapper.SetAuthorizeFunc(func(context.Context, string, any) (*wrapperToken.TokenWrapper, error) { return nil, nil })
	if err := apiWrapper.RegisterServices(grpcSrv); err != nil {
		t.Fatalf("register api wrapper: %v", err)
	}
	s2sWrapper := server.NewContractS2S(s2sServiceHandler{})
	s2sWrapper.SetAuthorizeFunc(func(context.Context, string, any) (*wrapperToken.TokenWrapper, error) { return nil, nil })
	if err := s2sWrapper.RegisterServices(grpcSrv); err != nil {
		t.Fatalf("register s2s wrapper: %v", err)
	}

	var _ grpcserver.ServiceImplementation = apiWrapper
	var _ grpcserver.ServiceImplementation = s2sWrapper
}

func TestGoCollectionWatchRuntimeContract(t *testing.T) {
	revisionOf := func(change *types.ContractWatchChange) int64 {
		return change.GetRevision()
	}

	resumeHub := collection.NewHub[types.ContractWatchChange](4, 0)
	resumed, err := collectSeq(resumeHub.Watch("tenant.alpha", 1, changeSeq(validChange(1), validChange(2), validChange(3)), revisionOf))
	if err != nil {
		t.Fatalf("resume replay: %v", err)
	}
	if len(resumed) != 2 || resumed[0].GetRevision() != 2 || resumed[1].GetRevision() != 3 {
		t.Fatalf("unexpected resumed changes: %+v", resumed)
	}

	relistHub := collection.NewHub[types.ContractWatchChange](1, 0)
	primary := relistHub.Watch("tenant.alpha", 0, changeSeq(validChange(2), validChange(3)), revisionOf)
	time.Sleep(50 * time.Millisecond)
	_, err = collectSeq(relistHub.Watch("tenant.alpha", 1, nil, revisionOf))
	if status.Code(err) != codes.OutOfRange {
		t.Fatalf("expected relist OutOfRange for evicted revision, got %v", err)
	}
	_, _ = collectSeq(primary)

	idleDone := make(chan struct{})
	idleHub := collection.NewHub[types.ContractWatchChange](2, 1)
	start := time.Now()
	_, err = collectSeq(idleHub.Watch("tenant.alpha", 0, blockingChangeSeq(idleDone), revisionOf))
	close(idleDone)
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("expected idle timeout DeadlineExceeded, got %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("idle timeout took too long: %v", time.Since(start))
	}

	teardownHub := collection.NewHub[types.ContractWatchChange](2, 0)
	first, err := firstFromSeq(teardownHub.Watch("tenant.alpha", 0, changeSeq(validChange(1)), revisionOf))
	if err != nil {
		t.Fatalf("first watcher: %v", err)
	}
	if first.GetRevision() != 1 {
		t.Fatalf("unexpected first watcher revision: %d", first.GetRevision())
	}
	done := make(chan error, 1)
	go func() {
		next, err := firstFromSeq(teardownHub.Watch("tenant.alpha", 0, changeSeq(validChange(2)), revisionOf))
		if err != nil {
			done <- err
			return
		}
		if next.GetRevision() != 2 {
			done <- fmt.Errorf("unexpected fresh watcher revision: %d", next.GetRevision())
			return
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("fresh watcher after teardown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fresh watcher did not start a new drain after teardown")
	}
}

// streamValidationHandler drains/echoes streams so the generated wrapper's
// per-message validation is actually exercised (PC-W-4): a handler that never
// Recvs would let invalid inbound messages pass unvalidated. A "bad-response"
// payload makes it emit an INVALID response so the egress validator trips.
type streamValidationHandler struct{ serviceHandler }

func (streamValidationHandler) SendEvents(_ *servicemanager.Caller, stream grpc.ClientStreamingServer[types.ContractEvent, types.ContractResponse]) error {
	bad := false
	for {
		ev, err := stream.Recv()
		if err == io.EOF {
			if bad {
				// Missing required result_id — the wrapper must reject this
				// before it reaches the client.
				return stream.SendAndClose(&types.ContractResponse{Accepted: true})
			}
			return stream.SendAndClose(validResponse())
		}
		if err != nil {
			return err
		}
		if ev.GetPayload() == "bad-response" {
			bad = true
		}
	}
}

func (streamValidationHandler) AssistEvents(_ *servicemanager.Caller, stream grpc.BidiStreamingServer[types.ContractEvent, types.ContractEvent]) error {
	for {
		ev, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		reply := validEvent()
		if ev.GetPayload() == "bad-response" {
			// Missing required event_id — egress validation must trip.
			reply = &types.ContractEvent{TenantNamespace: "tenant.alpha", Payload: "oops"}
		}
		if err := stream.Send(reply); err != nil {
			return err
		}
	}
}

// TestGoWireStreamValidation (PC-W-4): the generated wrapper validates
// client-stream and bidi messages ON THE WIRE — an invalid inbound message is
// rejected with INVALID_ARGUMENT at the handler's Recv, and an invalid handler
// response is rejected with INTERNAL before it reaches the client.
func TestGoWireStreamValidation(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)
	grpcSrv := grpc.NewServer()
	apiWrapper := server.NewContractApi(streamValidationHandler{})
	apiWrapper.SetAuthorizeFunc(func(context.Context, string, any) (*wrapperToken.TokenWrapper, error) { return nil, nil })
	apiWrapper.SetAuthenticateFunc(func(context.Context) (*wrapperToken.TokenWrapper, error) { return nil, nil })
	apiWrapper.SetAuthenticateAtOrBelowFunc(func(context.Context, string) (*wrapperToken.TokenWrapper, error) { return nil, nil })
	if err := apiWrapper.RegisterServices(grpcSrv); err != nil {
		t.Fatalf("register api wrapper: %v", err)
	}
	go func() { _ = grpcSrv.Serve(lis) }()
	defer grpcSrv.Stop()

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer conn.Close()
	client := types.NewContractApiClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	badEvent := &types.ContractEvent{TenantNamespace: "tenant.alpha", Payload: "no-id"}

	// Client-streaming happy path.
	sOK, err := client.SendEvents(ctx)
	if err != nil {
		t.Fatalf("send events open: %v", err)
	}
	if err := sOK.Send(validEvent()); err != nil {
		t.Fatalf("send valid: %v", err)
	}
	if resp, err := sOK.CloseAndRecv(); err != nil || resp.GetResultId() != "result-123" {
		t.Fatalf("valid client stream: %v %+v", err, resp)
	}

	// Invalid inbound message -> INVALID_ARGUMENT.
	sBad, err := client.SendEvents(ctx)
	if err != nil {
		t.Fatalf("send events open: %v", err)
	}
	_ = sBad.Send(badEvent)
	if _, err := sBad.CloseAndRecv(); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid client-stream message must be INVALID_ARGUMENT, got %v", err)
	}

	// Invalid handler RESPONSE -> INTERNAL.
	sResp, err := client.SendEvents(ctx)
	if err != nil {
		t.Fatalf("send events open: %v", err)
	}
	trigger := validEvent()
	trigger.Payload = "bad-response"
	_ = sResp.Send(trigger)
	if _, err := sResp.CloseAndRecv(); status.Code(err) != codes.Internal {
		t.Fatalf("invalid client-stream response must be INTERNAL, got %v", err)
	}

	// Bidi happy path: echo.
	aOK, err := client.AssistEvents(ctx)
	if err != nil {
		t.Fatalf("assist open: %v", err)
	}
	if err := aOK.Send(validEvent()); err != nil {
		t.Fatalf("assist send: %v", err)
	}
	if ev, err := aOK.Recv(); err != nil || ev.GetEventId() == "" {
		t.Fatalf("valid bidi echo: %v %+v", err, ev)
	}
	_ = aOK.CloseSend()

	// Bidi invalid inbound -> INVALID_ARGUMENT.
	aBad, err := client.AssistEvents(ctx)
	if err != nil {
		t.Fatalf("assist open: %v", err)
	}
	_ = aBad.Send(badEvent)
	if _, err := aBad.Recv(); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid bidi message must be INVALID_ARGUMENT, got %v", err)
	}

	// Bidi invalid handler response -> INTERNAL.
	aResp, err := client.AssistEvents(ctx)
	if err != nil {
		t.Fatalf("assist open: %v", err)
	}
	trigger2 := validEvent()
	trigger2.Payload = "bad-response"
	_ = aResp.Send(trigger2)
	if _, err := aResp.Recv(); status.Code(err) != codes.Internal {
		t.Fatalf("invalid bidi response must be INTERNAL, got %v", err)
	}
}

// egressRedactionHandler serves raw records with ZERO redaction code — the
// generated wrapper alone must redact them at egress (RED-1).
type egressRedactionHandler struct{ serviceHandler }

func (egressRedactionHandler) ListEvents(*servicemanager.Caller, *types.ContractRequest) (iter.Seq2[*types.ContractEvent, error], error) {
	return singleEventSeq(validEvent()), nil
}
func (egressRedactionHandler) ListEventsPaged(context.Context, *servicemanager.Caller, *types.ContractPageRequest, collection.PageParams) (collection.Page[*types.ContractEvent], error) {
	return collection.Page[*types.ContractEvent]{Items: []*types.ContractEvent{validEvent()}, Total: 1}, nil
}
func (egressRedactionHandler) Watch(*servicemanager.Caller, *types.ContractWatchRequest) (*collection.WatchSource[types.ContractEvent, types.ContractWatchChange], error) {
	return watchSource(validEvent()), nil
}

// TestGoWireEgressRedactionFailClosed is the RED-1 wire-level evidence: a
// (spec.redact)-declaring record type, a handler with ZERO redaction code, and
// the GENERATED wrapper ships redacted records to an unentitled caller on every
// record-bearing egress path — SNAPSHOT stream (ListEvents), PAGE
// (ListEventsPaged), and WATCH (snapshot item). Fail-closed: the caller carries
// no token data grants, so the MAY_REDACT payload egresses cleared + indicator.
func TestGoWireEgressRedactionFailClosed(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)
	grpcSrv := grpc.NewServer()
	apiWrapper := server.NewContractApi(egressRedactionHandler{})
	apiWrapper.SetAuthorizeFunc(func(context.Context, string, any) (*wrapperToken.TokenWrapper, error) { return nil, nil })
	apiWrapper.SetAuthenticateFunc(func(context.Context) (*wrapperToken.TokenWrapper, error) { return nil, nil })
	apiWrapper.SetAuthenticateAtOrBelowFunc(func(context.Context, string) (*wrapperToken.TokenWrapper, error) { return nil, nil })
	apiWrapper.SetAnonymizeHmacKey([]byte("per-deployment-key"))
	if err := apiWrapper.RegisterServices(grpcSrv); err != nil {
		t.Fatalf("register api wrapper: %v", err)
	}
	go func() { _ = grpcSrv.Serve(lis) }()
	defer grpcSrv.Stop()

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer conn.Close()

	api := c2sm.New(conn, func() (string, error) { return "token", nil }).Grpc().Clients().Contract().ContractApiClient()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// SNAPSHOT stream (ListEvents): every item redacted on the wire.
	seq, err := api.ListEvents(ctx, validRequest())
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	items, err := collectSeq(seq)
	if err != nil || len(items) == 0 {
		t.Fatalf("collect list events: %v (%d items)", err, len(items))
	}
	for _, it := range items {
		if it.GetPayload() != "" || !it.GetPayloadRedacted() {
			t.Fatalf("SNAPSHOT egress must redact payload fail-closed, got %q redacted=%v", it.GetPayload(), it.GetPayloadRedacted())
		}
		if it.GetEventId() == "" {
			t.Fatalf("unannotated fields must survive sanitization: %+v", it)
		}
	}

	// PAGE (ListEventsPaged): every page item redacted on the wire.
	pageSaw := 0
	for item, err := range api.ListEventsPaged(ctx, &types.ContractPageRequest{TenantNamespace: "tenant.alpha", PageSize: 10}) {
		if err != nil {
			t.Fatalf("paged: %v", err)
		}
		pageSaw++
		if item.GetPayload() != "" || !item.GetPayloadRedacted() {
			t.Fatalf("PAGE egress must redact payload fail-closed, got %q redacted=%v", item.GetPayload(), item.GetPayloadRedacted())
		}
	}
	if pageSaw == 0 {
		t.Fatal("PAGE egress returned no items")
	}

	// WATCH: the snapshot item is redacted on the wire.
	watch, err := api.Watch(ctx, validWatchRequest())
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	events, err := collectSeq(watch)
	if err != nil || len(events) == 0 {
		t.Fatalf("collect watch: %v (%d events)", err, len(events))
	}
	snap := events[0].GetSnapshotItem()
	if snap == nil || snap.GetPayload() != "" || !snap.GetPayloadRedacted() {
		t.Fatalf("WATCH snapshot egress must redact payload fail-closed: %+v", events[0])
	}

	// Loosening: the generated builder maps a matching token data grant onto RAW
	// and Sanitize then egresses the payload untouched — the grant token string
	// is the generated contract (PKG_MESSAGE_FIELD).
	ent := types.BuildContractEventEntitlement(map[string]bool{"CONTRACTCLIENT_CONTRACT_EVENT_PAYLOAD": true})
	out := types.SanitizeContractEvent(validEvent(), ent, []byte("per-deployment-key"))
	if out.GetPayload() != "ok" || out.GetPayloadRedacted() {
		t.Fatalf("RAW grant must egress payload raw, got %q redacted=%v", out.GetPayload(), out.GetPayloadRedacted())
	}
	if noGrant := types.SanitizeContractEvent(validEvent(), types.BuildContractEventEntitlement(map[string]bool{}), []byte("per-deployment-key")); noGrant.GetPayload() != "" || !noGrant.GetPayloadRedacted() {
		t.Fatalf("absent grant must redact, got %q", noGrant.GetPayload())
	}

	// Oneof recursion (executed): a hand-authored oneof envelope embedding the
	// record sanitizes THROUGH the active variant — fail-closed with a nil
	// entitlement, and the scalar variants are untouched.
	env := types.SanitizeContractWatchEvent(watchSnapshot(validEvent()), nil, []byte("per-deployment-key"))
	if env.GetSnapshotItem().GetPayload() != "" || !env.GetSnapshotItem().GetPayloadRedacted() {
		t.Fatalf("oneof envelope must sanitize through the active variant: %+v", env)
	}
	if env.GetSnapshotItem().GetEventId() != "event-1" {
		t.Fatalf("unannotated fields must survive oneof recursion: %+v", env)
	}
}

func TestGoBothRuntimeSmokePublicRead(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)
	grpcSrv := grpc.NewServer()

	publicReadCalls := 0
	readCalls := 0
	listEventsCalls := 0
	watchCalls := 0
	authorizeCalls := 0
	authenticateCalls := 0
	apiWrapper := server.NewContractApi(validationServiceHandler{
		readCalls:       &readCalls,
		publicReadCalls: &publicReadCalls,
		listEventsCalls: &listEventsCalls,
		watchCalls:      &watchCalls,
	})
	apiWrapper.SetAuthorizeFunc(func(ctx context.Context, _ string, req any) (*wrapperToken.TokenWrapper, error) {
		authorizeCalls++
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok || len(md.Get("authorization")) == 0 {
			return nil, status.Error(codes.Unauthenticated, "missing authorization header")
		}
		if got := md.Get("authorization")[0]; got != "Bearer token" {
			return nil, status.Errorf(codes.Unauthenticated, "unexpected authorization header %q", got)
		}
		typed, ok := req.(*types.ContractRequest)
		if !ok || typed == nil {
			return nil, status.Error(codes.Internal, "authorize did not receive the request message")
		}
		if typed.GetTenantNamespace() != "tenant.alpha" {
			return nil, status.Error(codes.PermissionDenied, "caller namespace is not at-or-above target")
		}
		return nil, nil
	})
	apiWrapper.SetAuthenticateFunc(func(ctx context.Context) (*wrapperToken.TokenWrapper, error) {
		authenticateCalls++
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok || len(md.Get("authorization")) == 0 {
			return nil, status.Error(codes.Unauthenticated, "missing authorization header")
		}
		if got := md.Get("authorization")[0]; got != "Bearer token" {
			return nil, status.Errorf(codes.Unauthenticated, "unexpected authorization header %q", got)
		}
		return nil, nil
	})
	apiWrapper.SetAuthenticateAtOrBelowFunc(func(ctx context.Context, targetNamespace string) (*wrapperToken.TokenWrapper, error) {
		authenticateCalls++
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok || len(md.Get("authorization")) == 0 {
			return nil, status.Error(codes.Unauthenticated, "missing authorization header")
		}
		if got := md.Get("authorization")[0]; got != "Bearer token" {
			return nil, status.Errorf(codes.Unauthenticated, "unexpected authorization header %q", got)
		}
		if targetNamespace != "/" && targetNamespace != "" {
			return nil, status.Errorf(codes.PermissionDenied, "unexpected target namespace %q", targetNamespace)
		}
		return nil, nil
	})
	if err := apiWrapper.RegisterServices(grpcSrv); err != nil {
		t.Fatalf("register api wrapper: %v", err)
	}

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- grpcSrv.Serve(lis)
	}()
	t.Cleanup(func() {
		grpcSrv.Stop()
		select {
		case err := <-serveErr:
			if err != nil {
				t.Logf("grpc server stopped: %v", err)
			}
		case <-time.After(time.Second):
			t.Log("grpc server did not stop within timeout")
		}
	})

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Logf("close client: %v", err)
		}
	})

	c2s := c2sm.New(conn, nil)
	c2sAuth := c2sm.New(conn, func() (string, error) { return "token", nil })
	c2sFailingToken := c2sm.New(conn, func() (string, error) {
		return "", errors.New("identity unavailable")
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = c2s.Grpc().Clients().Contract().ContractApiClient().GetContract(ctx, validRequest())
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected protected C2S Read to fail with Unauthenticated, got %v", err)
	}
	if readCalls != 0 {
		t.Fatalf("unauthenticated C2S Read reached handler; calls=%d", readCalls)
	}
	if authorizeCalls != 0 {
		t.Fatalf("expected unauthenticated Read to fail before authorize, got %d authorize calls", authorizeCalls)
	}

	resp, err := c2sAuth.Grpc().Clients().Contract().ContractApiClient().GetContract(ctx, validRequest())
	if err != nil {
		t.Fatalf("authorized protected read: %v", err)
	}
	if resp.GetResultId() != "result-123" || !resp.GetAccepted() {
		t.Fatalf("unexpected protected response: %+v", resp)
	}
	if readCalls != 1 {
		t.Fatalf("expected authorized Read to reach handler once, got %d", readCalls)
	}

	forbiddenReq := validRequest()
	forbiddenReq.TenantNamespace = "tenant.beta"
	_, err = c2sAuth.Grpc().Clients().Contract().ContractApiClient().GetContract(ctx, forbiddenReq)
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected namespace-mismatched C2S Read to fail with PermissionDenied, got %v", err)
	}
	if readCalls != 1 {
		t.Fatalf("namespace-mismatched C2S Read reached handler; calls=%d", readCalls)
	}

	_, err = c2sFailingToken.Grpc().Clients().Contract().ContractApiClient().GetContract(ctx, validRequest())
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected protected C2S Read with failing token provider to fail with Unauthenticated, got %v", err)
	}
	if readCalls != 1 {
		t.Fatalf("failing-token C2S Read reached handler; calls=%d", readCalls)
	}

	resp, err = c2s.Grpc().Clients().Contract().ContractApiClient().GetPublicContract(ctx, validRequest())
	if err != nil {
		t.Fatalf("public read: %v", err)
	}
	if resp.GetResultId() != "result-123" || !resp.GetAccepted() {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if publicReadCalls != 1 {
		t.Fatalf("expected valid GetPublicContract to reach handler once, got %d", publicReadCalls)
	}

	resp, err = c2sFailingToken.Grpc().Clients().Contract().ContractApiClient().GetPublicContract(ctx, validRequest())
	if err != nil {
		t.Fatalf("auth-disabled GetPublicContract with failing token provider: %v", err)
	}
	if resp.GetResultId() != "result-123" || !resp.GetAccepted() {
		t.Fatalf("unexpected failing-token public response: %+v", resp)
	}
	if publicReadCalls != 2 {
		t.Fatalf("expected failing-token GetPublicContract to reach handler once, got %d", publicReadCalls)
	}

	_, err = c2s.Grpc().Clients().Contract().ContractApiClient().GetPublicContract(ctx, invalidRequest())
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected invalid C2S request to fail with InvalidArgument, got %v", err)
	}
	if publicReadCalls != 2 {
		t.Fatalf("invalid C2S request reached handler; calls=%d", publicReadCalls)
	}

	badRespReq := validRequest()
	badRespReq.ItemId = "bad-response"
	_, err = c2s.Grpc().Clients().Contract().ContractApiClient().GetPublicContract(ctx, badRespReq)
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected invalid C2S response to fail with Internal, got %v", err)
	}
	if publicReadCalls != 3 {
		t.Fatalf("expected bad-response request to reach handler once, calls=%d", publicReadCalls)
	}

	watch, err := c2s.Grpc().Clients().Contract().ContractApiClient().Watch(ctx, validWatchRequest())
	if err == nil {
		_, err = firstFromSeq(watch)
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected auth-any-user C2S Watch without token to fail with Unauthenticated, got %v", err)
	}
	if watchCalls != 0 {
		t.Fatalf("unauthenticated C2S Watch reached handler; calls=%d", watchCalls)
	}
	if authenticateCalls != 0 {
		t.Fatalf("expected unauthenticated Watch to fail before authenticate, got %d authenticate calls", authenticateCalls)
	}

	watch, err = c2sAuth.Grpc().Clients().Contract().ContractApiClient().Watch(ctx, validWatchRequest())
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	watchEvents, err := collectSeq(watch)
	if err != nil {
		t.Fatalf("recv watch events: %v", err)
	}
	if len(watchEvents) != 2 {
		t.Fatalf("expected snapshot_item and snapshot_done Watch events, got %d: %+v", len(watchEvents), watchEvents)
	}
	if watchEvents[0].GetSnapshotItem().GetEventId() != "event-1" {
		t.Fatalf("unexpected watch event: %+v", watchEvents[0])
	}
	if watchCalls != 1 {
		t.Fatalf("expected valid Watch to reach handler once, got %d", watchCalls)
	}

	watch, err = c2sAuth.Grpc().Clients().Contract().ContractApiClient().Watch(ctx, invalidWatchRequest())
	if err == nil {
		_, err = firstFromSeq(watch)
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected invalid C2S stream request to fail with InvalidArgument, got %v", err)
	}
	if watchCalls != 1 {
		t.Fatalf("invalid C2S stream request reached handler; calls=%d", watchCalls)
	}

	badEventReq := validWatchRequest()
	badEventReq.ItemId = "bad-event"
	watch, err = c2sAuth.Grpc().Clients().Contract().ContractApiClient().Watch(ctx, badEventReq)
	if err != nil {
		t.Fatalf("watch bad-event: %v", err)
	}
	_, err = firstFromSeq(watch)
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected invalid C2S stream response to fail with Internal, got %v", err)
	}
	if watchCalls != 2 {
		t.Fatalf("expected bad-event stream request to reach handler once, calls=%d", watchCalls)
	}

	listEvents, err := c2s.Grpc().Clients().Contract().ContractApiClient().ListEvents(ctx, validRequest())
	if err == nil {
		_, err = firstFromSeq(listEvents)
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected auth-any-user C2S ListEvents without token to fail with Unauthenticated, got %v", err)
	}
	if listEventsCalls != 0 {
		t.Fatalf("unauthenticated C2S ListEvents reached handler; calls=%d", listEventsCalls)
	}

	listEvents, err = c2sAuth.Grpc().Clients().Contract().ContractApiClient().ListEvents(ctx, validRequest())
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	listItems, err := collectSeq(listEvents)
	if err != nil {
		t.Fatalf("collect list events: %v", err)
	}
	if len(listItems) != 1 {
		t.Fatalf("expected one ListEvents item, got %d", len(listItems))
	}
	listEvent := listItems[0]
	if listEvent == nil {
		t.Fatal("unexpected nil ListEvents item")
	}
	if listEvent.GetEventId() != "event-1" || listEvent.GetTenantNamespace() != "tenant.alpha" {
		t.Fatalf("unexpected list event: %+v", listEvent)
	}
	if listEventsCalls != 1 {
		t.Fatalf("expected valid ListEvents to reach handler once, got %d", listEventsCalls)
	}

	listEvents, err = c2sAuth.Grpc().Clients().Contract().ContractApiClient().ListEvents(ctx, invalidRequest())
	if err == nil {
		_, err = firstFromSeq(listEvents)
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected invalid C2S ListEvents request to fail with InvalidArgument, got %v", err)
	}
	if listEventsCalls != 1 {
		t.Fatalf("invalid C2S ListEvents request reached handler; calls=%d", listEventsCalls)
	}

	badListEventReq := validRequest()
	badListEventReq.ItemId = "bad-event"
	listEvents, err = c2sAuth.Grpc().Clients().Contract().ContractApiClient().ListEvents(ctx, badListEventReq)
	if err != nil {
		t.Fatalf("list events bad-event: %v", err)
	}
	_, err = firstFromSeq(listEvents)
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected invalid C2S ListEvents response to fail with Internal, got %v", err)
	}
	if listEventsCalls != 2 {
		t.Fatalf("expected bad-event ListEvents request to reach handler once, calls=%d", listEventsCalls)
	}
}

func TestGoBothRuntimeSmokeW2SPublicRead(t *testing.T) {
	requests := 0
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost {
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
			t.Errorf("unexpected method %s", r.Method)
			return
		}
		if r.URL.Path != "/contractclient/ContractApi/GetPublicContract" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			t.Errorf("unexpected path %s", r.URL.Path)
			return
		}
		if got := r.Header.Get("authorization"); got != "" {
			http.Error(w, "unexpected authorization header", http.StatusBadRequest)
			t.Errorf("unexpected authorization header %q", got)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusInternalServerError)
			t.Errorf("read body: %v", err)
			return
		}
		var req types.ContractRequest
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(body, &req); err != nil {
			http.Error(w, "decode body", http.StatusBadRequest)
			t.Errorf("decode body: %v", err)
			return
		}
		if err := types.ValidateContractRequest(&req); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			t.Errorf("invalid request: %v", err)
			return
		}

		w.Header().Set("content-type", "application/json")
		if req.GetItemId() == "bad-response" {
			_, _ = w.Write([]byte(`{"result":{"accepted":true}}`))
			return
		}
		_, _ = w.Write([]byte(`{"result":{"resultId":"result-123","accepted":true}}`))
	}))
	t.Cleanup(httpSrv.Close)

	w2s := w2sm.WithTokenProvider(httpSrv.URL, func(context.Context) (string, error) {
		t.Fatal("auth-disabled W2S request asked for a token")
		return "", nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := w2s.W2SClients().Contractclient().ContractApiW2SClient().GetPublicContract(ctx, validRequest())
	if err != nil {
		t.Fatalf("public read over W2S: %v", err)
	}
	if resp.GetResultId() != "result-123" || !resp.GetAccepted() {
		t.Fatalf("unexpected W2S response: %+v", resp)
	}
	if requests != 1 {
		t.Fatalf("expected one W2S request, got %d", requests)
	}

	_, err = w2s.W2SClients().Contractclient().ContractApiW2SClient().GetPublicContract(ctx, invalidRequest())
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected invalid W2S request to fail before send with InvalidArgument, got %v", err)
	}
	if requests != 1 {
		t.Fatalf("invalid W2S request reached fake server; requests=%d", requests)
	}

	badRespReq := validRequest()
	badRespReq.ItemId = "bad-response"
	_, err = w2s.W2SClients().Contractclient().ContractApiW2SClient().GetPublicContract(ctx, badRespReq)
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected invalid W2S response to fail with Internal, got %v", err)
	}
	if requests != 2 {
		t.Fatalf("expected bad-response request to reach fake server once, got %d", requests)
	}

	noToken := w2sm.WithTokenProvider(httpSrv.URL, nil)
	_, err = noToken.W2SClients().Contractclient().ContractApiW2SClient().GetContract(ctx, validRequest())
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected auth-required W2S Read without token provider to fail with Unauthenticated, got %v", err)
	}
	if requests != 2 {
		t.Fatalf("auth-required W2S Read without token provider reached fake server; requests=%d", requests)
	}

	failingToken := w2sm.WithTokenProvider(httpSrv.URL, func(context.Context) (string, error) {
		return "", errors.New("identity unavailable")
	})
	_, err = failingToken.W2SClients().Contractclient().ContractApiW2SClient().GetContract(ctx, validRequest())
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected auth-required W2S Read with failing token provider to fail with Unauthenticated, got %v", err)
	}
	if requests != 2 {
		t.Fatalf("auth-required W2S Read with failing token provider reached fake server; requests=%d", requests)
	}
}

func TestGoBothRuntimeSmokeW2SWatchSSE(t *testing.T) {
	requests := 0
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost {
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
			t.Errorf("unexpected method %s", r.Method)
			return
		}
		if r.URL.Path != "/contractclient/ContractApi/Watch" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			t.Errorf("unexpected path %s", r.URL.Path)
			return
		}
		if got := r.Header.Get("accept"); got != "text/event-stream" {
			http.Error(w, "unexpected accept header", http.StatusBadRequest)
			t.Errorf("unexpected accept header %q", got)
			return
		}
		if got := r.Header.Get("authorization"); got != "Bearer token" {
			http.Error(w, "unexpected authorization header", http.StatusUnauthorized)
			t.Errorf("unexpected authorization header %q", got)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusInternalServerError)
			t.Errorf("read body: %v", err)
			return
		}
		var req types.ContractWatchRequest
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(body, &req); err != nil {
			http.Error(w, "decode body", http.StatusBadRequest)
			t.Errorf("decode body: %v", err)
			return
		}
		if err := types.ValidateContractWatchRequest(&req); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			t.Errorf("invalid request: %v", err)
			return
		}

		w.Header().Set("content-type", "text/event-stream")
		if req.GetItemId() == "bad-event" {
			_, _ = w.Write([]byte("data: {\"result\":{\"snapshotItem\":{\"tenantNamespace\":\"tenant.alpha\",\"payload\":\"missing-id\"}}}\n\n"))
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			return
		}
		_, _ = w.Write([]byte("data: {\"result\":{\"snapshotItem\":{\"eventId\":\"event-1\",\"tenantNamespace\":\"tenant.alpha\",\"payload\":\"ok\"}}}\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	t.Cleanup(httpSrv.Close)

	tokenCalls := 0
	w2s := w2sm.WithTokenProvider(httpSrv.URL, func(context.Context) (string, error) {
		tokenCalls++
		return "token", nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := w2s.W2SClients().Contractclient().ContractApiW2SClient().Watch(ctx, validWatchRequest())
	if err != nil {
		t.Fatalf("watch over W2S: %v", err)
	}
	event, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv W2S event: %v", err)
	}
	if event.GetSnapshotItem().GetEventId() != "event-1" || event.GetSnapshotItem().GetTenantNamespace() != "tenant.alpha" {
		t.Fatalf("unexpected W2S event: %+v", event)
	}
	if _, err := stream.Recv(); err != io.EOF {
		t.Fatalf("expected W2S stream EOF, got %v", err)
	}
	if tokenCalls != 1 {
		t.Fatalf("expected one token call, got %d", tokenCalls)
	}
	if requests != 1 {
		t.Fatalf("expected one W2S SSE request, got %d", requests)
	}

	_, err = w2s.W2SClients().Contractclient().ContractApiW2SClient().Watch(ctx, invalidWatchRequest())
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected invalid W2S SSE request to fail before send with InvalidArgument, got %v", err)
	}
	if requests != 1 {
		t.Fatalf("invalid W2S SSE request reached fake server; requests=%d", requests)
	}

	badEventReq := validWatchRequest()
	badEventReq.ItemId = "bad-event"
	badStream, err := w2s.W2SClients().Contractclient().ContractApiW2SClient().Watch(ctx, badEventReq)
	if err != nil {
		t.Fatalf("open bad-event W2S stream: %v", err)
	}
	if _, err := badStream.Recv(); status.Code(err) != codes.Internal {
		t.Fatalf("expected invalid W2S SSE response to fail with Internal, got %v", err)
	}
	if requests != 2 {
		t.Fatalf("expected bad-event request to reach fake server once, got %d", requests)
	}
}

func TestGoBothRuntimeSmokeW2SUploadWebSocket(t *testing.T) {
	serverDone := make(chan struct{})
	httpSrv := httptest.NewServer(websocket.Handler(func(ws *websocket.Conn) {
		defer close(serverDone)
		if got := ws.Request().URL.Path; got != "/contractclient/ContractApi/SendEvents" {
			t.Errorf("unexpected websocket path %s", got)
			return
		}

		if frame := requireW2SFrame(t, ws, "start", "Bearer token"); frame == nil {
			return
		}
		message := requireW2SFrame(t, ws, "message", "Bearer token")
		if message == nil {
			return
		}
		event := requireW2SEvent(t, message["message"])
		if event == nil {
			return
		}
		if event.GetEventId() != "event-1" || event.GetTenantNamespace() != "tenant.alpha" {
			t.Errorf("unexpected sendEvents event: %+v", event)
			return
		}
		if frame := requireW2SFrame(t, ws, "complete", ""); frame == nil {
			return
		}
		if err := websocket.Message.Send(ws, `{"result":{"resultId":"sendEvents-1","accepted":true}}`); err != nil {
			t.Errorf("send sendEvents response: %v", err)
		}
	}))
	t.Cleanup(httpSrv.Close)

	tokenCalls := 0
	w2s := w2sm.WithTokenProvider(httpSrv.URL, func(context.Context) (string, error) {
		tokenCalls++
		return "token", nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := w2s.W2SClients().Contractclient().ContractApiW2SClient().WithTimeout(5 * time.Second).SendEvents(ctx)
	if err != nil {
		t.Fatalf("sendEvents over W2S: %v", err)
	}
	if err := stream.Send(validEvent()); err != nil {
		t.Fatalf("send W2S sendEvents event: %v", err)
	}
	resp, err := stream.CloseAndRecv()
	if err != nil {
		t.Fatalf("close W2S sendEvents: %v", err)
	}
	if resp.GetResultId() != "sendEvents-1" || !resp.GetAccepted() {
		t.Fatalf("unexpected W2S sendEvents response: %+v", resp)
	}
	select {
	case <-serverDone:
	case <-time.After(5 * time.Second):
		t.Fatal("W2S sendEvents server did not finish")
	}
	if tokenCalls != 2 {
		t.Fatalf("expected two token calls, got %d", tokenCalls)
	}
}

func TestGoBothRuntimeSmokeW2SChatWebSocket(t *testing.T) {
	serverDone := make(chan struct{})
	httpSrv := httptest.NewServer(websocket.Handler(func(ws *websocket.Conn) {
		defer close(serverDone)
		if got := ws.Request().URL.Path; got != "/contractclient/ContractApi/AssistEvents" {
			t.Errorf("unexpected websocket path %s", got)
			return
		}

		if frame := requireW2SFrame(t, ws, "start", "Bearer token"); frame == nil {
			return
		}
		message := requireW2SFrame(t, ws, "message", "Bearer token")
		if message == nil {
			return
		}
		event := requireW2SEvent(t, message["message"])
		if event == nil {
			return
		}
		if event.GetEventId() != "event-1" || event.GetTenantNamespace() != "tenant.alpha" {
			t.Errorf("unexpected assistEvents event: %+v", event)
			return
		}
		if err := websocket.Message.Send(ws, `{"result":{"eventId":"event-echo","tenantNamespace":"tenant.alpha","payload":"reply"}}`); err != nil {
			t.Errorf("send assistEvents response: %v", err)
		}
	}))
	t.Cleanup(httpSrv.Close)

	tokenCalls := 0
	w2s := w2sm.WithTokenProvider(httpSrv.URL, func(context.Context) (string, error) {
		tokenCalls++
		return "token", nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connected := make(chan struct{}, 1)
	received := make(chan *types.ContractEvent, 1)
	handle := w2s.W2SClients().Contractclient().ContractApiW2SClient().WithTimeout(5 * time.Second).AssistEvents(
		ctx,
		func(send func(*types.ContractEvent) error) {
			connected <- struct{}{}
		},
		func(event *types.ContractEvent) {
			received <- event
		},
	)
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("W2S assistEvents did not connect")
	}
	if err := handle.Send(validEvent()); err != nil {
		t.Fatalf("send W2S assistEvents event: %v", err)
	}
	select {
	case event := <-received:
		if event.GetEventId() != "event-echo" || event.GetTenantNamespace() != "tenant.alpha" {
			t.Fatalf("unexpected W2S assistEvents response: %+v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("W2S assistEvents response timed out")
	}
	handle.Close()
	select {
	case <-serverDone:
	case <-time.After(5 * time.Second):
		t.Fatal("W2S assistEvents server did not finish")
	}
	if tokenCalls != 2 {
		t.Fatalf("expected two token calls, got %d", tokenCalls)
	}
}

type recordingAccountingSink struct {
	mu     sync.Mutex
	events []*accounting_pb.Event
}

func (s *recordingAccountingSink) Emit(ctx context.Context, event *accounting_pb.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
	return nil
}

func (s *recordingAccountingSink) snapshot() []*accounting_pb.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*accounting_pb.Event(nil), s.events...)
}

func TestGoAccountingRuntimeCoversMethodKinds(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)
	grpcSrv := grpc.NewServer()
	sink := &recordingAccountingSink{}
	traceEvents := make(chan *trace_pb.TraceEvent, 4)
	wrapper := auditserver.NewContractAuditApi(auditServiceHandler{})
	wrapper.SetEmitter(am.NewEmitter("contractclient-test", sink))
	wrapper.SetTracePublisher(func(ctx context.Context, event *trace_pb.TraceEvent) {
		traceEvents <- event
	})
	if err := wrapper.RegisterServices(grpcSrv); err != nil {
		t.Fatalf("register audit wrapper: %v", err)
	}

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- grpcSrv.Serve(lis)
	}()
	t.Cleanup(func() {
		grpcSrv.Stop()
		select {
		case err := <-serveErr:
			if err != nil {
				t.Logf("grpc server stopped: %v", err)
			}
		case <-time.After(time.Second):
			t.Log("grpc server did not stop within timeout")
		}
	})

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Logf("close client: %v", err)
		}
	})
	client := audittypes.NewContractAuditApiClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tracedSuccess := validAuditRequest()
	tracedSuccess.TraceId = "trace-success"
	if _, err := client.ExecuteAudit(ctx, tracedSuccess); err != nil {
		t.Fatalf("audit unary success: %v", err)
	}
	tracedError := failingAuditRequest()
	tracedError.TraceId = "trace-error"
	if _, err := client.ExecuteAudit(ctx, tracedError); status.Code(err) != codes.Internal {
		t.Fatalf("audit unary error code = %v, want Internal", err)
	}

	stream, err := client.GenerateAuditEvents(ctx, validAuditRequest())
	if err != nil {
		t.Fatalf("audit stream open: %v", err)
	}
	if event, err := stream.Recv(); err != nil || event.GetEventId() != "audit-event" {
		t.Fatalf("audit stream event = %+v, err = %v", event, err)
	}
	if _, err := stream.Recv(); err != io.EOF {
		t.Fatalf("audit stream EOF = %v, want EOF", err)
	}
	stream, err = client.GenerateAuditEvents(ctx, failingAuditRequest())
	if err == nil {
		_, err = stream.Recv()
	}
	if status.Code(err) != codes.Internal {
		t.Fatalf("audit stream error code = %v, want Internal", err)
	}

	sendEvents, err := client.SendAuditEvents(ctx)
	if err != nil {
		t.Fatalf("audit sendEvents open: %v", err)
	}
	if err := sendEvents.Send(validAuditEvent()); err != nil {
		t.Fatalf("audit sendEvents send: %v", err)
	}
	if resp, err := sendEvents.CloseAndRecv(); err != nil || resp.GetResultId() != "audit-sendEvents" {
		t.Fatalf("audit sendEvents response = %+v, err = %v", resp, err)
	}
	sendEvents, err = client.SendAuditEvents(ctx)
	if err != nil {
		t.Fatalf("audit sendEvents error open: %v", err)
	}
	if err := sendEvents.Send(failingAuditEvent()); err != nil {
		t.Fatalf("audit sendEvents error send: %v", err)
	}
	if _, err := sendEvents.CloseAndRecv(); status.Code(err) != codes.Internal {
		t.Fatalf("audit sendEvents error code = %v, want Internal", err)
	}

	assistEvents, err := client.AssistAuditEvents(ctx)
	if err != nil {
		t.Fatalf("audit assistEvents open: %v", err)
	}
	if err := assistEvents.Send(validAuditEvent()); err != nil {
		t.Fatalf("audit assistEvents send: %v", err)
	}
	if event, err := assistEvents.Recv(); err != nil || event.GetEventId() != "audit-reply" {
		t.Fatalf("audit assistEvents response = %+v, err = %v", event, err)
	}
	if err := assistEvents.CloseSend(); err != nil {
		t.Fatalf("audit assistEvents close send: %v", err)
	}
	if _, err := assistEvents.Recv(); err != io.EOF {
		t.Fatalf("audit assistEvents EOF = %v, want EOF", err)
	}
	assistEvents, err = client.AssistAuditEvents(ctx)
	if err != nil {
		t.Fatalf("audit assistEvents error open: %v", err)
	}
	if err := assistEvents.Send(failingAuditEvent()); err != nil {
		t.Fatalf("audit assistEvents error send: %v", err)
	}
	if _, err := assistEvents.Recv(); status.Code(err) != codes.Internal {
		t.Fatalf("audit assistEvents error code = %v, want Internal", err)
	}

	completed := map[services_pb.ContractclientRPCs]int{}
	errorsByMethod := map[services_pb.ContractclientRPCs]int{}
	for _, event := range sink.snapshot() {
		if rpc := event.GetRpcCompleted(); rpc != nil {
			completed[rpc.GetMethod().GetContractclient()]++
			continue
		}
		if rpc := event.GetRpcError(); rpc != nil {
			errorsByMethod[rpc.GetMethod().GetContractclient()]++
		}
	}
	for _, method := range []services_pb.ContractclientRPCs{
		services_pb.ContractclientRPCs_CONTRACTCLIENT_RPCS_EXECUTE_AUDIT,
		services_pb.ContractclientRPCs_CONTRACTCLIENT_RPCS_GENERATE_AUDIT_EVENTS,
		services_pb.ContractclientRPCs_CONTRACTCLIENT_RPCS_SEND_AUDIT_EVENTS,
		services_pb.ContractclientRPCs_CONTRACTCLIENT_RPCS_ASSIST_AUDIT_EVENTS,
	} {
		if completed[method] != 1 {
			t.Fatalf("completed[%s] = %d, want 1; all completed=%v", method, completed[method], completed)
		}
		if errorsByMethod[method] != 1 {
			t.Fatalf("errors[%s] = %d, want 1; all errors=%v", method, errorsByMethod[method], errorsByMethod)
		}
	}

	traceSeen := map[string]map[trace_pb.TraceEventKind]*trace_pb.TraceEvent{}
	for i := 0; i < 4; i++ {
		select {
		case event := <-traceEvents:
			if traceSeen[event.GetTraceId()] == nil {
				traceSeen[event.GetTraceId()] = map[trace_pb.TraceEventKind]*trace_pb.TraceEvent{}
			}
			traceSeen[event.GetTraceId()][event.GetKind()] = event
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for trace event %d; seen=%v", i+1, traceSeen)
		}
	}
	for traceID, wantError := range map[string]string{
		"trace-success": "",
		"trace-error":   "rpc error: code = Internal desc = audit unary failed",
	} {
		events := traceSeen[traceID]
		if events[trace_pb.TraceEventKind_TRACE_EVENT_KIND_INTERNAL_REQUEST] == nil {
			t.Fatalf("missing request trace for %s: %v", traceID, traceSeen)
		}
		response := events[trace_pb.TraceEventKind_TRACE_EVENT_KIND_INTERNAL_RESPONSE]
		if response == nil {
			t.Fatalf("missing response trace for %s: %v", traceID, traceSeen)
		}
		if response.GetRpc().GetContractclient() != services_pb.ContractclientRPCs_CONTRACTCLIENT_RPCS_EXECUTE_AUDIT {
			t.Fatalf("response trace rpc for %s = %s", traceID, response.GetRpc().GetContractclient())
		}
		if response.GetError() != wantError {
			t.Fatalf("response trace error for %s = %q, want %q", traceID, response.GetError(), wantError)
		}
	}
}
EOF
}

write_go_s2sm_startup_invariant_test() {
  local test_file="$1"
  cat > "$test_file" <<'EOF'
package sm

import (
	"context"
	"iter"
	"strings"
	"testing"

	service_im "gitlab.com/example/tacplus/consumer/contractclient/model/chassis/service_im"
	"gitlab.com/example/tacplus/consumer/contractclient/model/chassis/collection"
	"gitlab.com/example/tacplus/consumer/contractclient/model/chassis/servicemanager"
	server "gitlab.com/example/tacplus/consumer/contractclient/model/s2sm/grpc/servers/contract/contractclient"
	auditserver "gitlab.com/example/tacplus/consumer/contractclient/model/s2sm/grpc/servers/services/contractclient"
	types "gitlab.com/example/tacplus/consumer/contractclient/model/types/contract/contractclient"
	audittypes "gitlab.com/example/tacplus/consumer/contractclient/model/types/services/contractclient"
	grpcconfig_pb "gitlab.com/example/tacplus/consumer/contractclient/model/types/sys/grpcconfig"
	"google.golang.org/grpc"
)

type startupInvariantHandler struct{}

func (startupInvariantHandler) ContractS2SGrpcImpl() server.ContractS2SHandler {
	return startupInvariantS2SHandler{}
}

func (startupInvariantHandler) ContractApiGrpcImpl() server.ContractApiHandler {
	return startupInvariantAPIHandler{}
}

func (startupInvariantHandler) ContractAuditApiGrpcImpl() auditserver.ContractAuditApiHandler {
	return startupInvariantAuditHandler{}
}

func (startupInvariantHandler) Shutdown(context.Context) {}

type startupInvariantS2SHandler struct{}

func (startupInvariantS2SHandler) Ping(context.Context, *servicemanager.Caller, *types.ContractRequest) (*types.ContractResponse, error) {
	return &types.ContractResponse{ResultId: "result-1", Accepted: true}, nil
}

func (startupInvariantS2SHandler) GenerateEvents(*servicemanager.Caller, *types.ContractRequest, grpc.ServerStreamingServer[types.ContractEvent]) error {
	return nil
}

func (startupInvariantS2SHandler) SendEvents(*servicemanager.Caller, grpc.ClientStreamingServer[types.ContractEvent, types.ContractResponse]) error {
	return nil
}

func (startupInvariantS2SHandler) AssistEvents(*servicemanager.Caller, grpc.BidiStreamingServer[types.ContractEvent, types.ContractEvent]) error {
	return nil
}

type startupInvariantAPIHandler struct{}

func (startupInvariantAPIHandler) GetContract(context.Context, *servicemanager.Caller, *types.ContractRequest) (*types.ContractResponse, error) {
	return &types.ContractResponse{ResultId: "result-1", Accepted: true}, nil
}

func (startupInvariantAPIHandler) GetPublicContract(context.Context, *servicemanager.Caller, *types.ContractRequest) (*types.ContractResponse, error) {
	return &types.ContractResponse{ResultId: "result-1", Accepted: true}, nil
}

func (startupInvariantAPIHandler) ListEvents(*servicemanager.Caller, *types.ContractRequest) (iter.Seq2[*types.ContractEvent, error], error) {
	return func(yield func(*types.ContractEvent, error) bool) {}, nil
}

func (startupInvariantAPIHandler) Watch(*servicemanager.Caller, *types.ContractWatchRequest) (*collection.WatchSource[types.ContractEvent, types.ContractWatchChange], error) {
	return &collection.WatchSource[types.ContractEvent, types.ContractWatchChange]{
		Scope:    "tenant.alpha",
		Snapshot: func(yield func(*types.ContractEvent, error) bool) {},
		Revision: 1,
		Changes:  func() iter.Seq2[*types.ContractWatchChange, error] { return func(yield func(*types.ContractWatchChange, error) bool) {} },
	}, nil
}

func (startupInvariantAPIHandler) ListEventsPaged(context.Context, *servicemanager.Caller, *types.ContractPageRequest, collection.PageParams) (collection.Page[*types.ContractEvent], error) {
	return collection.Page[*types.ContractEvent]{}, nil
}

func (startupInvariantAPIHandler) SendEvents(*servicemanager.Caller, grpc.ClientStreamingServer[types.ContractEvent, types.ContractResponse]) error {
	return nil
}

func (startupInvariantAPIHandler) AssistEvents(*servicemanager.Caller, grpc.BidiStreamingServer[types.ContractEvent, types.ContractEvent]) error {
	return nil
}

type startupInvariantAuditHandler struct{}

func (startupInvariantAuditHandler) ExecuteAudit(context.Context, *servicemanager.Caller, *audittypes.AuditRequest) (*audittypes.AuditResponse, error) {
	return &audittypes.AuditResponse{ResultId: "audit-result"}, nil
}

func (startupInvariantAuditHandler) GenerateAuditEvents(*servicemanager.Caller, *audittypes.AuditRequest, grpc.ServerStreamingServer[audittypes.AuditEvent]) error {
	return nil
}

func (startupInvariantAuditHandler) SendAuditEvents(*servicemanager.Caller, grpc.ClientStreamingServer[audittypes.AuditEvent, audittypes.AuditResponse]) error {
	return nil
}

func (startupInvariantAuditHandler) AssistAuditEvents(*servicemanager.Caller, grpc.BidiStreamingServer[audittypes.AuditEvent, audittypes.AuditEvent]) error {
	return nil
}

func TestRunFailsFastWhenMandatoryAuthWrapperIsUnwired(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sm := &ServiceManager{
		cfg:      &grpcconfig_pb.Config{},
		ctx:      ctx,
		stop:     cancel,
		identity: &service_im.IdentityManager{},
		sc:       &servicemanager.ServiceManager{},
	}

	err := sm.Run(startupInvariantHandler{})
	if err == nil {
		t.Fatal("expected Run to fail before serving when auth funcs are unwired")
	}
	if !strings.Contains(err.Error(), "authorizer not wired") {
		t.Fatalf("expected authorizer startup invariant, got %v", err)
	}
}
EOF
}

write_go_s2sm_eventbus_publisher_runtime_test() {
  local test_file="$1"
  cat > "$test_file" <<'EOF'
package sm

import (
	"context"
	"errors"
	"testing"
	"time"

	trace_pb "gitlab.com/example/tacplus/consumer/contractclient/model/types/sys/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type eventBusPublisherSentinelConn struct {
	t       *testing.T
	recvErr error
	sent    []*trace_pb.TraceEvent
	auth    []string
}

func (c *eventBusPublisherSentinelConn) Invoke(context.Context, string, any, any, ...grpc.CallOption) error {
	return nil
}

func (c *eventBusPublisherSentinelConn) NewStream(ctx context.Context, _ *grpc.StreamDesc, _ string, _ ...grpc.CallOption) (grpc.ClientStream, error) {
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		c.t.Fatal("publish stream opened without outgoing metadata")
	}
	c.auth = md.Get("authorization")
	return &eventBusPublisherSentinelStream{conn: c, recvErr: c.recvErr}, nil
}

type eventBusPublisherSentinelStream struct {
	grpc.ClientStream
	conn     *eventBusPublisherSentinelConn
	recvErr  error
	sendDone bool
}

func (s *eventBusPublisherSentinelStream) SendMsg(msg any) error {
	event, ok := msg.(*trace_pb.TraceEvent)
	if !ok {
		s.conn.t.Fatalf("unexpected publish message type %T", msg)
	}
	s.conn.sent = append(s.conn.sent, event)
	return nil
}

func (s *eventBusPublisherSentinelStream) CloseSend() error {
	s.sendDone = true
	return nil
}

func (s *eventBusPublisherSentinelStream) RecvMsg(any) error {
	if !s.sendDone {
		s.conn.t.Fatal("publisher waited for ack before closing its send side")
	}
	return s.recvErr
}

func TestTracePublisherReResolvesAfterFailedStreamAndKeepsPendingEvent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	first := &eventBusPublisherSentinelConn{t: t, recvErr: errors.New("broker stream died before ack")}
	second := &eventBusPublisherSentinelConn{t: t}
	var resolves int
	resolve := func(context.Context) (grpc.ClientConnInterface, error) {
		resolves++
		if resolves == 1 {
			return first, nil
		}
		return second, nil
	}

	event := &trace_pb.TraceEvent{TraceId: "pending-trace-event"}
	ch := make(chan *trace_pb.TraceEvent, 1)
	ch <- event
	close(ch)

	tracePublishLoop(ctx, resolve, func() (string, error) { return "publisher-token", nil }, ch)
	if ctx.Err() != nil {
		t.Fatalf("trace publisher sentinel timed out before retry/ack completed: %v", ctx.Err())
	}
	if resolves != 2 {
		t.Fatalf("resolver called %d times, want exactly 2", resolves)
	}
	if len(first.sent) != 1 || first.sent[0] != event {
		t.Fatalf("first broker saw pending event %v, want original event once", first.sent)
	}
	if len(second.sent) != 1 || second.sent[0] != event {
		t.Fatalf("re-resolved broker saw pending event %v, want original event once", second.sent)
	}
	if got := first.auth; len(got) != 1 || got[0] != "Bearer publisher-token" {
		t.Fatalf("first publish auth metadata = %v, want bearer publisher token", got)
	}
	if got := second.auth; len(got) != 1 || got[0] != "Bearer publisher-token" {
		t.Fatalf("second publish auth metadata = %v, want bearer publisher token", got)
	}
}
EOF
}

write_go_s2sm_diskstore_failclosed_test() {
  local test_file="$1"
  cat > "$test_file" <<'EOF'
package sm

import (
	"encoding/json"
	"testing"

	diskstore "gitlab.com/example/tacplus/consumer/contractclient/model/chassis/diskstore"
	diskstores "gitlab.com/example/tacplus/consumer/contractclient/model/s2sm/diskstore"
	contractfixtures_pb "gitlab.com/example/tacplus/consumer/contractclient/model/types/services/contractfixtures"
)

// TestDiskstoreFailClosedRead (PC-L-11): the generated TYPED store must never
// hand a record back from a corrupt or constraint-violating payload — decode
// and validation failures on the read path are LOUD errors, not a silent
// (nil, false, nil) that reads as "empty store".
func TestDiskstoreFailClosedRead(t *testing.T) {
	base := t.TempDir()

	// Happy path through the real files first, so the negatives below fail the
	// read they would otherwise have satisfied.
	entry := &contractfixtures_pb.FixtureStoreEntry{EntryId: "e-1", Value: "v"}
	if err := diskstores.SaveFixtureStoreEntry(base, "ns", entry, diskstore.DefaultSnapshotOpts()); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, ok, err := diskstores.LoadFixtureStoreEntry(base, "ns")
	if err != nil || !ok || got.GetEntryId() != "e-1" {
		t.Fatalf("roundtrip: %v %v %+v", err, ok, got)
	}

	path := diskstore.StorePath(base, "ns", "fixture_store_entry", ".snapshot")

	// Corrupt payload (not JSON), written through the real snapshot container:
	// Load must error (codec), never report an absent/empty store.
	if err := diskstore.WriteSnapshot(path, []byte("{not json"), diskstore.DefaultSnapshotOpts()); err != nil {
		t.Fatalf("write corrupt snapshot: %v", err)
	}
	if m, ok, err := diskstores.LoadFixtureStoreEntry(base, "ns"); err == nil {
		t.Fatalf("corrupt snapshot must fail loud, got %v %+v", ok, m)
	}

	// Constraint-violating payload (valid JSON, empty required store key):
	// validation-on-read must reject it.
	if err := diskstore.WriteSnapshot(path, []byte(`{"entry_id":""}`), diskstore.DefaultSnapshotOpts()); err != nil {
		t.Fatalf("write invalid snapshot: %v", err)
	}
	if m, ok, err := diskstores.LoadFixtureStoreEntry(base, "ns"); err == nil {
		t.Fatalf("constraint-violating snapshot must fail validation, got %v %+v", ok, m)
	}

	// Redo-record legs: corrupt, constraint-violating, and empty payloads all
	// fail loud; a tombstone decodes to (nil, false, nil).
	if _, _, err := diskstores.DecodeFixtureStoreEntry(&diskstore.Record{Key: "k", Payload: json.RawMessage("{oops")}); err == nil {
		t.Fatal("corrupt redo payload must fail loud")
	}
	if _, _, err := diskstores.DecodeFixtureStoreEntry(&diskstore.Record{Key: "k", Payload: json.RawMessage(`{"entry_id":""}`)}); err == nil {
		t.Fatal("constraint-violating redo payload must fail validation")
	}
	if _, _, err := diskstores.DecodeFixtureStoreEntry(&diskstore.Record{Key: "k"}); err == nil {
		t.Fatal("missing redo payload must fail loud")
	}
	if m, live, err := diskstores.DecodeFixtureStoreEntry(&diskstore.Record{Key: "k", Deleted: true}); err != nil || live || m != nil {
		t.Fatalf("tombstone must decode to (nil, false, nil), got %v %v %+v", m, live, err)
	}
}
EOF
}

write_go_s2sm_eventbus_subscriber_runtime_test() {
  local test_file="$1"
  cat > "$test_file" <<'EOF'
package sm

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	contractfixturesfixturestateevents_sub "gitlab.com/example/tacplus/consumer/contractclient/model/subscribe/contractfixtures/FixtureState/events"
	contractfixtures_pb "gitlab.com/example/tacplus/consumer/contractclient/model/types/services/contractfixtures"
	entitlement_pb "gitlab.com/example/tacplus/consumer/contractclient/model/types/sys/entitlement"
	eb_pb "gitlab.com/example/tacplus/consumer/contractclient/model/types/generated/eventbus_support"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type eventBusSubscriberSentinelConn struct {
	t          *testing.T
	frame      []byte
	fullMethod string
	sentReq    *eb_pb.SubscribeRequest
}

func (c *eventBusSubscriberSentinelConn) Invoke(context.Context, string, any, any, ...grpc.CallOption) error {
	return nil
}

func (c *eventBusSubscriberSentinelConn) NewStream(_ context.Context, sd *grpc.StreamDesc, fullMethod string, _ ...grpc.CallOption) (grpc.ClientStream, error) {
	// Per-topic subscribe name (eventbus.PerTopicSubscribeParts), not the flat
	// EventBusTypedService/Subscribe<Stem> this fixture asserted before 38e40786.
	if fullMethod != "/contractfixtures.FixtureStateStateService/SubscribeToEvents" {
		c.t.Fatalf("subscribe full method = %q", fullMethod)
	}
	if sd == nil || sd.StreamName != "SubscribeToEvents" || !sd.ServerStreams || !sd.ClientStreams {
		c.t.Fatalf("unexpected subscribe stream desc: %#v", sd)
	}
	c.fullMethod = fullMethod
	return &eventBusSubscriberSentinelStream{conn: c}, nil
}

type eventBusSubscriberSentinelStream struct {
	grpc.ClientStream
	conn      *eventBusSubscriberSentinelConn
	delivered bool
}

func (s *eventBusSubscriberSentinelStream) SendMsg(msg any) error {
	req, ok := msg.(*eb_pb.SubscribeRequest)
	if !ok {
		s.conn.t.Fatalf("unexpected subscribe request type %T", msg)
	}
	s.conn.sentReq = req
	return nil
}

func (s *eventBusSubscriberSentinelStream) RecvMsg(msg any) error {
	if s.delivered {
		return io.EOF
	}
	s.delivered = true
	target, ok := msg.(interface{ Unmarshal([]byte) error })
	if !ok {
		s.conn.t.Fatalf("subscribe receiver %T does not expose Unmarshal", msg)
	}
	return target.Unmarshal(s.conn.frame)
}

func TestEventBusSubscriberDeliversNonZeroRecordMeta(t *testing.T) {
	meta := &eb_pb.RecordMeta{
		Key:       []byte("fixture-key"),
		Topic:     "contractfixtures.FixtureState.events",
		Partition: 7,
		Offset:    42,
		Timestamp: timestamppb.New(time.Unix(1700000000, 123000000).UTC()),
	}
	event := &contractfixtures_pb.FixtureEvent{TraceId: "fixture-subscribe-event"}

	conn := &eventBusSubscriberSentinelConn{
		t:     t,
		frame: encodeEventBusSubscribeRecordForTest(t, meta, event),
	}
	sub := contractfixturesfixturestateevents_sub.New(conn, "contractclient")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var gotEvent *contractfixtures_pb.FixtureEvent
	var gotMeta *eb_pb.RecordMeta
	err := sub.Subscribe(ctx, func(_ context.Context, event *contractfixtures_pb.FixtureEvent, recMeta *eb_pb.RecordMeta) error {
		gotEvent = event
		gotMeta = recMeta
		cancel()
		return nil
	}, contractfixturesfixturestateevents_sub.WithEarliest())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Subscribe returned %v, want context.Canceled after sentinel delivery", err)
	}
	if conn.sentReq == nil {
		t.Fatalf("subscriber did not send SubscribeRequest")
	}
	if conn.sentReq.GetConsumerGroup() != "contractclient" || !conn.sentReq.GetEarliest() {
		t.Fatalf("SubscribeRequest = %#v, want consumer group contractclient and earliest start", conn.sentReq)
	}
	if gotEvent == nil || gotEvent.GetTraceId() != event.GetTraceId() {
		t.Fatalf("handler event = %#v, want trace_id %q", gotEvent, event.GetTraceId())
	}
	if gotMeta == nil {
		t.Fatalf("handler metadata is nil")
	}
	if gotMeta.GetTopic() != meta.GetTopic() ||
		gotMeta.GetPartition() != meta.GetPartition() ||
		gotMeta.GetOffset() != meta.GetOffset() ||
		!bytes.Equal(gotMeta.GetKey(), meta.GetKey()) ||
		gotMeta.GetTimestamp().AsTime() != meta.GetTimestamp().AsTime() {
		t.Fatalf("handler metadata = %#v, want %#v", gotMeta, meta)
	}
}

// TestFixtureEventRecordDispatchHandsTheTypedRow EXECUTES the generated record
// dispatch the ServiceManager runs on every subscribed FixtureEvent:
// HandleFixtureEvent passes the FixtureEventRecord projection, typed, to its
// insert callback before it returns (synchronous), writes nothing on a
// trace-round noop event, and fails the delivery with the callback's error.
func TestFixtureEventRecordDispatchHandsTheTypedRow(t *testing.T) {
	ctx := context.Background()
	event := &contractfixtures_pb.FixtureEvent{EventId: "evt-dispatch", TenantNamespace: "tenant-a", TimestampMs: 1700000000123}
	var rows []*contractfixtures_pb.FixtureEventRecord
	insert := func(_ context.Context, row *contractfixtures_pb.FixtureEventRecord) error {
		rows = append(rows, row)
		return nil
	}
	if err := contractfixtures_pb.HandleFixtureEvent(ctx, event, insert); err != nil {
		t.Fatalf("HandleFixtureEvent: %v", err)
	}
	if len(rows) != 1 || rows[0].GetEventId() != "evt-dispatch" || rows[0].GetTenantNamespace() != "tenant-a" || rows[0].GetTimestampMs() != 1700000000123 {
		t.Fatalf("typed rows = %v, want one FixtureEventRecord{evt-dispatch, tenant-a, 1700000000123}", rows)
	}

	rows = nil
	noop := &contractfixtures_pb.FixtureEvent{EventId: "evt-noop", Noop: true}
	if err := contractfixtures_pb.HandleFixtureEvent(ctx, noop, insert); err != nil || len(rows) != 0 {
		t.Fatalf("a trace-round noop event must write no row: err %v rows %v", err, rows)
	}

	storeDown := errors.New("store down")
	err := contractfixtures_pb.HandleFixtureEvent(ctx, event, func(context.Context, *contractfixtures_pb.FixtureEventRecord) error { return storeDown })
	if !errors.Is(err, storeDown) {
		t.Fatalf("HandleFixtureEvent error = %v, want the insert callback's error", err)
	}
}

// TestFixtureEventRedactionSanitize EXECUTES the generated data-entitlement
// transform (redact runtime evidence, NEW-8): DROP clears the field, PSEUDONYMIZE
// tokenizes it, and the injected <field>_redacted indicators are set — real
// end-to-end evidence that the proto injection + Sanitize<T> codegen work.
func TestFixtureEventRedactionSanitize(t *testing.T) {
	raw := &contractfixtures_pb.FixtureEvent{
		EventId:      "evt-1",
		Payload:      "secret-payload",
		ContactEmail: "user@example.com",
	}
	key := []byte("per-deployment-key")

	// payload/contact_email are hard-floor (DROP/PSEUDONYMIZE) modes, so they apply
	// regardless of the (empty) entitlement.
	out := contractfixtures_pb.SanitizeFixtureEvent(raw, &contractfixtures_pb.FixtureEventEntitlement{}, key)
	if out.GetPayload() != "" {
		t.Fatalf("DROP should clear payload, got %q", out.GetPayload())
	}
	if !out.GetPayloadRedacted() {
		t.Fatalf("payload_redacted indicator must be set after DROP")
	}
	// contact_email carries (spec.assert_email), so anonymization is FORMAT-PRESERVING
	// — a deterministic valid synthetic email, pinned to the cross-language golden
	// vector for (key, "user@example.com").
	if got := out.GetContactEmail(); got != "u6e0907e2dcbb@h400973be.example" {
		t.Fatalf("format-preserving email surrogate drifted, got %q", got)
	}
	if !out.GetContactEmailRedacted() {
		t.Fatalf("contact_email_redacted indicator must be set after PSEUDONYMIZE")
	}
	// Deterministic: same (value,key) yields the same token.
	if again := contractfixtures_pb.SanitizeFixtureEvent(raw, &contractfixtures_pb.FixtureEventEntitlement{}, key); again.GetContactEmail() != out.GetContactEmail() {
		t.Fatalf("pseudonym must be deterministic for the same key")
	}
	// Input is never mutated.
	if raw.GetPayload() != "secret-payload" || raw.GetContactEmail() != "user@example.com" {
		t.Fatalf("Sanitize must not mutate its input")
	}
	// A nil entitlement is safe — hard-floor fields still apply, no panic.
	if nilOut := contractfixtures_pb.SanitizeFixtureEvent(raw, nil, key); nilOut.GetPayload() != "" || !nilOut.GetPayloadRedacted() {
		t.Fatalf("nil entitlement: DROP must still clear payload and set the indicator")
	}
}

// TestFixtureEventEntitlementLevelsAndRecursion EXECUTES the caller-decided (may-*)
// path and FIELD-BY-FIELD recursion end-to-end under the FAIL-CLOSED model: an
// unset level (UNSPECIFIED, the proto3 default — including a nil or empty
// entitlement) applies each may-* field's most-protective default; only an
// explicit RAW grant egresses a value; recursion is unconditional so nested
// protections apply through a nil sub-entitlement.
func TestFixtureEventEntitlementLevelsAndRecursion(t *testing.T) {
	raw := &contractfixtures_pb.FixtureEvent{
		EventId: "evt-2",
		Notes:   "sensitive-notes",
		Contact: &contractfixtures_pb.FixtureContact{Phone: "555-1234", Label: "home"},
		MoreContacts: []*contractfixtures_pb.FixtureContact{
			{Phone: "111-1111", Label: "a"},
			{Phone: "222-2222", Label: "b"},
		},
	}
	key := []byte("per-deployment-key")

	// FAIL-CLOSED default (empty entitlement): MAY_REDACT fields redact — notes
	// cleared, and the nested/repeated phones cleared THROUGH the nil sub-entitlement.
	// Unannotated siblings (label) pass through untouched.
	def := contractfixtures_pb.SanitizeFixtureEvent(raw, &contractfixtures_pb.FixtureEventEntitlement{}, key)
	if def.GetNotes() != "" || !def.GetNotesRedacted() {
		t.Fatalf("fail-closed: notes must default to REDACT, got %q redacted=%v", def.GetNotes(), def.GetNotesRedacted())
	}
	if def.GetContact().GetPhone() != "" || !def.GetContact().GetPhoneRedacted() {
		t.Fatalf("fail-closed: contact.phone must default to REDACT through a nil sub-entitlement")
	}
	if def.GetContact().GetLabel() != "home" {
		t.Fatalf("fail-closed: contact.label must be preserved, got %q", def.GetContact().GetLabel())
	}
	for i, c := range def.GetMoreContacts() {
		if c.GetPhone() != "" || !c.GetPhoneRedacted() {
			t.Fatalf("fail-closed: more_contacts[%d].phone must default to REDACT", i)
		}
	}
	// A nil entitlement is identical to an empty one — fully protective.
	if nilDef := contractfixtures_pb.SanitizeFixtureEvent(raw, nil, key); nilDef.GetNotes() != "" || nilDef.GetContact().GetPhone() != "" {
		t.Fatalf("fail-closed: nil entitlement must be fully protective")
	}

	// RAW loosens: an explicit grant egresses the value; one sub-entitlement is
	// applied to EVERY element of more_contacts.
	ent := &contractfixtures_pb.FixtureEventEntitlement{
		Notes: entitlement_pb.EntitlementLevel_ENTITLEMENT_LEVEL_RAW,
		ContactSub: &contractfixtures_pb.FixtureContactEntitlement{
			Phone: entitlement_pb.EntitlementLevel_ENTITLEMENT_LEVEL_RAW,
		},
		MoreContactsSub: &contractfixtures_pb.FixtureContactEntitlement{
			Phone: entitlement_pb.EntitlementLevel_ENTITLEMENT_LEVEL_RAW,
		},
	}
	out := contractfixtures_pb.SanitizeFixtureEvent(raw, ent, key)
	if out.GetNotes() != "sensitive-notes" || out.GetNotesRedacted() {
		t.Fatalf("RAW: notes must egress raw with indicator false, got %q redacted=%v", out.GetNotes(), out.GetNotesRedacted())
	}
	if out.GetContact().GetPhone() != "555-1234" || out.GetContact().GetPhoneRedacted() {
		t.Fatalf("RAW: contact.phone must egress raw")
	}
	if len(out.GetMoreContacts()) != 2 {
		t.Fatalf("element-wise: list length must be preserved, got %d", len(out.GetMoreContacts()))
	}
	for i, c := range out.GetMoreContacts() {
		if c.GetPhone() == "" || c.GetPhoneRedacted() {
			t.Fatalf("RAW: more_contacts[%d].phone must egress raw", i)
		}
	}

	// An explicit REDACT behaves exactly like the protective default, and an
	// explicit ANONYMIZE on a MAY_REDACT string loosens to a keyed surrogate.
	mixed := contractfixtures_pb.SanitizeFixtureEvent(raw, &contractfixtures_pb.FixtureEventEntitlement{
		Notes: entitlement_pb.EntitlementLevel_ENTITLEMENT_LEVEL_ANONYMIZE,
		ContactSub: &contractfixtures_pb.FixtureContactEntitlement{
			Phone: entitlement_pb.EntitlementLevel_ENTITLEMENT_LEVEL_REDACT,
		},
	}, key)
	if !strings.HasPrefix(mixed.GetNotes(), "pseud:") || !mixed.GetNotesRedacted() {
		t.Fatalf("ANONYMIZE: notes must be a pseud: surrogate + indicator set, got %q", mixed.GetNotes())
	}
	if mixed.GetContact().GetPhone() != "" || !mixed.GetContact().GetPhoneRedacted() {
		t.Fatalf("explicit REDACT: contact.phone must be cleared + indicator set")
	}

	// Input is never mutated.
	if raw.GetNotes() != "sensitive-notes" || raw.GetContact().GetPhone() != "555-1234" || raw.GetMoreContacts()[0].GetPhone() != "111-1111" {
		t.Fatalf("Sanitize must not mutate its input")
	}
}

func encodeEventBusSubscribeRecordForTest(t *testing.T, meta *eb_pb.RecordMeta, event *contractfixtures_pb.FixtureEvent) []byte {
	t.Helper()
	metaBytes, err := proto.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	eventBytes, err := proto.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	var out []byte
	out = appendLengthDelimitedFieldForTest(out, 1, metaBytes)
	out = appendLengthDelimitedFieldForTest(out, 2, eventBytes)
	return out
}

func appendLengthDelimitedFieldForTest(out []byte, field int, data []byte) []byte {
	out = appendVarintForTest(out, uint64(field<<3|2))
	out = appendVarintForTest(out, uint64(len(data)))
	return append(out, data...)
}

func appendVarintForTest(out []byte, v uint64) []byte {
	for v >= 0x80 {
		out = append(out, byte(v)|0x80)
		v >>= 7
	}
	return append(out, byte(v))
}
EOF
}

write_go_c2d_consumer_test() {
  local test_file="$1"
  cat > "$test_file" <<'EOF'
package consumer

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	c2dm "gitlab.com/example/tacplus/consumer/contractclient/model/c2dm"
	types "gitlab.com/example/tacplus/consumer/contractclient/model/types/contract/contractclient"
	"google.golang.org/grpc"
)

// daemonServer is hand-rolled on purpose: Go has no generated C2D server
// wrapper to host. Go excludes C2D services from server generation by design
// (docs/API.md "C2D server auth-skip scope") and emits C2D clients only, so
// this test can only exercise the generated C2D *client* against a plain
// hand-written gRPC server. Runtime coverage of the generated C2D server wrapper
// lives in the Rust c2d consumer (write_rust_c2d_consumer, PC-W-3).
type daemonServer struct {
	types.UnimplementedContractDaemonServer
}

func validRequest() *types.ContractRequest {
	return &types.ContractRequest{
		TenantNamespace: "tenant.alpha",
		ItemId:          "item-123",
		Mode:            types.ContractMode_CONTRACT_MODE_READ,
	}
}

func validResponse() *types.ContractResponse {
	return &types.ContractResponse{ResultId: "local-123", Accepted: true}
}

func validEvent() *types.ContractEvent {
	return &types.ContractEvent{EventId: "local-event-1", TenantNamespace: "tenant.alpha", Payload: "ok"}
}

func (daemonServer) GetLocal(ctx context.Context, req *types.ContractRequest) (*types.ContractResponse, error) {
	if err := types.ValidateContractRequest(req); err != nil {
		return nil, err
	}
	return validResponse(), nil
}

func (daemonServer) GenerateLocalEvents(req *types.ContractRequest, stream grpc.ServerStreamingServer[types.ContractEvent]) error {
	if err := types.ValidateContractRequest(req); err != nil {
		return err
	}
	return stream.Send(validEvent())
}

func TestC2DPublicAPICompiles(t *testing.T) {
	if err := types.ValidateContractRequest(validRequest()); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	sm, err := c2dm.New("/tmp/tacplus-consumer-contract.sock")
	if err != nil {
		t.Fatalf("new c2d manager: %v", err)
	}
	defer sm.Close()
	if sm.SocketPath() == "" {
		t.Fatal("expected socket path")
	}
	if sm.Grpc().Clients().Contract().ContractDaemonClient() == nil {
		t.Fatal("expected ContractDaemon C2D client")
	}
}

func TestC2DRuntimeSmokeUnixSocket(t *testing.T) {
	socketDir, err := os.MkdirTemp("/tmp", "tc-c2d-")
	if err != nil {
		t.Fatalf("make socket temp dir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(socketDir); err != nil {
			t.Logf("remove socket temp dir: %v", err)
		}
	})
	socketPath := filepath.Join(socketDir, "d.sock")
	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}

	grpcSrv := grpc.NewServer()
	types.RegisterContractDaemonServer(grpcSrv, daemonServer{})
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- grpcSrv.Serve(lis)
	}()
	t.Cleanup(func() {
		grpcSrv.Stop()
		select {
		case err := <-serveErr:
			if err != nil {
				t.Logf("grpc server stopped: %v", err)
			}
		case <-time.After(time.Second):
			t.Log("grpc server did not stop within timeout")
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sm, err := c2dm.Dial(ctx, socketPath)
	if err != nil {
		t.Fatalf("dial c2d manager: %v", err)
	}
	t.Cleanup(func() {
		if err := sm.Close(); err != nil {
			t.Logf("close c2d manager: %v", err)
		}
	})

	client := sm.Grpc().Clients().Contract().ContractDaemonClient()
	resp, err := client.GetLocal(ctx, validRequest())
	if err != nil {
		t.Fatalf("local read: %v", err)
	}
	if resp.GetResultId() != "local-123" || !resp.GetAccepted() {
		t.Fatalf("unexpected local read response: %+v", resp)
	}

	stream, err := client.GenerateLocalEvents(ctx, validRequest())
	if err != nil {
		t.Fatalf("local watch: %v", err)
	}
	event, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv local watch event: %v", err)
	}
	if event.GetEventId() != "local-event-1" || event.GetTenantNamespace() != "tenant.alpha" {
		t.Fatalf("unexpected local watch event: %+v", event)
	}
	if _, err := stream.Recv(); err != io.EOF {
		t.Fatalf("expected local watch EOF, got %v", err)
	}
}
EOF
}

write_go_c2s_stream_auth_failclosed_test() {
  local test_file="$1"
  cat > "$test_file" <<'EOF'
package consumer

// PC-W-7: negative coverage for token failure at stream open.
//
// Proposition: when the identity manager cannot provide a token, a
// client-streaming call FAILS BEFORE ANY RPC LEAVES THE PROCESS (API.md
// §214-223). The test uses a sentinel gRPC connection that records whether
// NewStream was ever called; a failing / nil / empty token provider must
// cause the generated client to return Unauthenticated WITHOUT calling
// NewStream.
//
// Why Go: the mechanism lives in the generated Go client method — the auth
// snippet is inline before the gRPC call. Rust/Java have the same contract
// but are tested via their own regression tests; Go is the reference leg
// and this is the only place the generated Go client is EXECUTED.
//
// Verification that the test fails without the fix: temporarily removing
// the {{- if .AuthRequired}} auth block from grpcclient_gen.go's
// client-streaming branch (line ~738) and regenerating causes these tests
// to fail: the sentinel conn's NewStream is called (asserting the RPC
// escaped the process) and the error code is Internal, not Unauthenticated.

import (
	"context"
	"errors"
	"testing"

	c2sm "gitlab.com/example/tacplus/consumer/contractclient/model/c2sm"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// streamAuthSentinelConn records whether NewStream was called. When the auth
// check works, NewStream must NEVER fire — the method returns before the RPC.
type streamAuthSentinelConn struct {
	streamCalled bool
}

func (*streamAuthSentinelConn) Invoke(context.Context, string, any, any, ...grpc.CallOption) error {
	return nil
}

func (c *streamAuthSentinelConn) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	c.streamCalled = true
	// Return an error the test can distinguish from the expected Unauthenticated.
	return nil, status.Error(codes.Internal, "sentinel: RPC escaped the auth gate")
}

// TestClientStreamSendEventsTokenErrorFailsClosed asserts that a
// client-streaming call (SendEvents) fails with Unauthenticated when the
// token provider returns an error, and that no gRPC stream open is attempted.
func TestClientStreamSendEventsTokenErrorFailsClosed(t *testing.T) {
	conn := &streamAuthSentinelConn{}
	sm := c2sm.New(conn, func() (string, error) {
		return "", errors.New("identity manager unavailable")
	})
	api := sm.Grpc().Clients().Contract().ContractApiClient()

	stream, err := api.SendEvents(context.Background())
	if err == nil {
		_ = stream
		t.Fatal("SendEvents must fail when token provider returns an error")
	}
	if got := status.Code(err); got != codes.Unauthenticated {
		t.Fatalf("SendEvents error code = %v, want Unauthenticated; err = %v", got, err)
	}
	if conn.streamCalled {
		t.Fatal("auth failure must prevent the RPC from leaving the process — NewStream was called")
	}
}

// TestClientStreamSendEventsNilProviderFailsClosed asserts the same contract
// when no token provider is configured at all (nil).
func TestClientStreamSendEventsNilProviderFailsClosed(t *testing.T) {
	conn := &streamAuthSentinelConn{}
	sm := c2sm.New(conn, nil)
	api := sm.Grpc().Clients().Contract().ContractApiClient()

	stream, err := api.SendEvents(context.Background())
	if err == nil {
		_ = stream
		t.Fatal("SendEvents must fail when token provider is nil")
	}
	if got := status.Code(err); got != codes.Unauthenticated {
		t.Fatalf("nil provider error code = %v, want Unauthenticated; err = %v", got, err)
	}
	if conn.streamCalled {
		t.Fatal("nil provider must prevent the RPC from leaving the process")
	}
}

// TestClientStreamSendEventsEmptyTokenFailsClosed asserts the same contract
// when the provider succeeds but returns an empty credential.
func TestClientStreamSendEventsEmptyTokenFailsClosed(t *testing.T) {
	conn := &streamAuthSentinelConn{}
	sm := c2sm.New(conn, func() (string, error) { return "", nil })
	api := sm.Grpc().Clients().Contract().ContractApiClient()

	stream, err := api.SendEvents(context.Background())
	if err == nil {
		_ = stream
		t.Fatal("SendEvents must fail when token is empty")
	}
	if got := status.Code(err); got != codes.Unauthenticated {
		t.Fatalf("empty token error code = %v, want Unauthenticated; err = %v", got, err)
	}
	if conn.streamCalled {
		t.Fatal("empty token must prevent the RPC from leaving the process")
	}
}
EOF
}

compile_go_handwritten_consumer() {
  local output="$1"
  local case_name="$2"
  local log_dir
  log_dir="$(dirname "$output")"

  local consumer_dir="$log_dir/go-consumer"
  rm -rf "$consumer_dir"
  mkdir -p "$consumer_dir"
  write_go_consumer_mod "$consumer_dir/go.mod"

  case "$case_name" in
    c2s)
      write_go_c2s_consumer_test "$consumer_dir/consumer_test.go"
      write_go_c2s_stream_auth_failclosed_test "$consumer_dir/stream_auth_test.go"
      ;;
    w2s)
      write_go_w2s_consumer_test "$consumer_dir/consumer_test.go"
      ;;
    both)
      write_go_both_consumer_test "$consumer_dir/consumer_test.go"
      write_go_c2s_stream_auth_failclosed_test "$consumer_dir/stream_auth_test.go"
      ;;
    c2d)
      write_go_c2d_consumer_test "$consumer_dir/consumer_test.go"
      ;;
    *)
      return
      ;;
  esac

  # Assert the fail-closed stream-auth negatives are actually PRESENT before running.
  # `go test ./...` silently runs whatever *_test.go files happen to exist, so a test
  # that stops being emitted would vanish with a green run -- the exact failure mode
  # this suite exists to prevent (a gate that cannot fire reads as coverage). PC-W-7
  # covers the documented contract that a stream-open token failure must fail before
  # any RPC leaves the process; if that file is gone, fail loudly instead.
  case "$case_name" in
    c2s|both)
      [[ -f "$consumer_dir/stream_auth_test.go" ]] || die "missing generated Go consumer test: stream_auth_test.go (PC-W-7 stream-open auth negatives)"
      grep -q 'func TestClientStreamSendEventsTokenErrorFailsClosed' "$consumer_dir/stream_auth_test.go" \
        || die "stream_auth_test.go present but missing TestClientStreamSendEventsTokenErrorFailsClosed"
      ;;
  esac

  # Run under the race detector: the generated runtime is concurrent (WATCH Hub
  # drain goroutines + shared rings, state watch loops, SM background tasks), and
  # this is the only place that code executes. CGO_ENABLED=1 is required by -race.
  (cd "$consumer_dir" && go mod tidy && CGO_ENABLED=1 go test -race -timeout 20m ./...) >"$log_dir/go-consumer-test.log" 2>&1 || {
    cat "$log_dir/go-consumer-test.log" >&2
    return 1
  }
  assert_clean_log "$log_dir/go-consumer-test.log"
}

write_rust_consumer_cargo() {
  local manifest="$1"
  local output="$2"
  cat > "$manifest" <<EOF
[package]
name = "consumer-contract-rust"
version = "0.1.0"
edition = "2021"

[dependencies]
am = { path = "$output/chassis/am" }
collection = { path = "$output/chassis/collection" }
contractclient-model = { path = "$output" }
diskstore = { path = "$output/chassis/diskstore" }
futures = "0.3.31"
prost = "0.14.1"
prost-wkt-types = "0.7.1"
serde_json = "1.0.149"
servicemanager = { path = "$output/chassis/servicemanager" }
tokio = { version = "1.49.0", features = ["rt-multi-thread", "net", "sync", "time"] }
tokio-stream = { version = "0.1.17", features = ["net"] }
tokio-tungstenite = "0.28.0"
tonic = "0.14.0"
tempfile = "3"
EOF
}

write_rust_c2s_consumer() {
  local main_rs="$1"
  cat > "$main_rs" <<'EOF'
fn main() {
    let rt = tokio::runtime::Runtime::new().expect("tokio runtime");
    rt.block_on(async {
        let channel = tonic::transport::Endpoint::from_static("http://127.0.0.1:1").connect_lazy();
        let sm = contractclient_model::c2sm::ServiceManager::new(channel, None);
        let _api = sm.grpc().contractclient().contract_api_client();
    });
}
EOF
}

write_rust_w2s_consumer() {
  local main_rs="$1"
  cat > "$main_rs" <<'EOF'
fn main() {
    let rt = tokio::runtime::Runtime::new().expect("tokio runtime");
    rt.block_on(async {
        let sm = contractclient_model::w2sm::ServiceManager::with_token_provider(
            "https://gateway.example".to_string(),
            None,
        );
        let _via_w2s = sm.w2s_clients().contractclient().contract_api_client();
        let _via_grpc = sm.grpc().contractclient().contract_api_client();
    });
}
EOF
}

write_rust_both_consumer() {
  local main_rs="$1"
  cat > "$main_rs" <<'EOF'
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use std::time::Duration;
use std::collections::HashMap;
use std::convert::Infallible;
use std::io::{BufRead, BufReader, Read, Write};
use std::net::TcpListener as StdTcpListener;
use std::pin::Pin;
use std::task::{Context, Poll};
use std::thread;

use futures::{SinkExt, StreamExt, TryStreamExt};
use prost::bytes::{Buf, BufMut, Bytes};
use prost::Message as ProstMessage;
use serde_json::Value;
use tokio::net::TcpListener;
use tokio_stream::wrappers::{ReceiverStream, TcpListenerStream};
use tokio_tungstenite::accept_async;
use tokio_tungstenite::tungstenite::Message;
use tonic::codec::{Codec, DecodeBuf, Decoder, EncodeBuf, Encoder};
use tonic::codegen::{Body, BoxFuture, StdError};

use contractclient_model::s2sm::subscribe::contractfixtures_fixture_state_events::{
    Handler as EventBusSubscribeHandler, Subscriber as EventBusSubscriber,
};
use contractclient_model::types::contract::contractclient::{
    contract_watch_event, ContractEvent, ContractMode, ContractPageRequest, ContractRequest,
    ContractResponse, ContractWatchChange, ContractWatchRequest,
};
use contractclient_model::types::generated::services::services as services_pb;
use contractclient_model::types::sys::accounting::accounting::{
    event as accounting_event, Event as AccountingEvent,
};
use contractclient_model::types::sys::trace::{TraceEvent, TraceEventKind};
use contractclient_model::types::services::contractclient::{
    AuditEvent, AuditRequest, AuditResponse,
};
use contractclient_model::types::services::contractclient::contract_audit_api_client::ContractAuditApiClient;
use contractclient_model::types::services::contractclient::contract_audit_api_server::ContractAuditApiServer;
use contractclient_model::types::services::contractfixtures::FixtureEvent;
use contractclient_model::types::generated::eventbus_support::eventbus_support::{RecordMeta, SubscribeRequest};

fn main() {
    let rt = tokio::runtime::Runtime::new().expect("tokio runtime");
    rt.block_on(async {
        let channel = tonic::transport::Endpoint::from_static("http://127.0.0.1:1").connect_lazy();
        let c2s = contractclient_model::c2sm::ServiceManager::new(channel, None);
        let _c2s_api = c2s.grpc().contractclient().contract_api_client();

        let w2s = contractclient_model::w2sm::ServiceManager::with_token_provider(
            "https://gateway.example".to_string(),
            None,
        );
        let _w2s_api = w2s.grpc().contractclient().contract_api_client();

        let _handler_trait = core::any::type_name::<dyn contractclient_model::s2sm::handler::Handler>();

        run_rust_audit_accounting_trace_smoke().await;
        run_collection_watch_runtime_contract().await;
        run_c2s_validation_smoke().await;
        run_eventbus_subscriber_metadata_smoke().await;
        run_record_dispatch_smoke().await;
        run_w2s_http_validation_smoke().await;
        run_w2s_chat_smoke().await;
        run_red1_sanitize_vector();
        run_red1_wire_egress_redaction().await;
        run_pcw4_wire_stream_validation().await;
        run_pcw6_unwired_auth_fail_closed().await;
        run_diskstore_fail_closed_read();
    });
}

// PC-L-11 (Rust leg): the generated TYPED store must never hand a record back
// from a corrupt or constraint-violating payload — decode and validation
// failures on the read path are LOUD errors, not a silent "empty store".
fn run_diskstore_fail_closed_read() {
    use contractclient_model::s2sm::diskstore_stores as stores;
    let dir = tempfile::tempdir().expect("tempdir");
    let base = dir.path();
    let opts = diskstore::SnapshotOpts::default();

    let entry = contractclient_model::types::services::contractfixtures::FixtureStoreEntry {
        entry_id: "e-1".to_string(),
        value: "v".to_string(),
        ..Default::default()
    };
    stores::save_fixture_store_entry(base, "ns", &entry, &opts).expect("save");
    let got = stores::load_fixture_store_entry(base, "ns").expect("load").expect("present");
    assert_eq!(got.entry_id, "e-1");

    let path = diskstore::store_path(base, "ns", "fixture_store_entry", ".snapshot");
    diskstore::write_snapshot(&path, b"{not json", &opts).expect("write corrupt");
    assert!(
        stores::load_fixture_store_entry(base, "ns").is_err(),
        "corrupt snapshot must fail loud"
    );

    diskstore::write_snapshot(&path, br#"{"entry_id":""}"#, &opts).expect("write invalid");
    assert!(
        stores::load_fixture_store_entry(base, "ns").is_err(),
        "constraint-violating snapshot must fail validation"
    );

    let bad = diskstore::Record {
        key: "k".into(),
        deleted: false,
        metadata: None,
        payload: Some(serde_json::json!({"entry_id": ""})),
    };
    assert!(
        stores::decode_fixture_store_entry(&bad).is_err(),
        "constraint-violating redo payload must fail validation"
    );
    let missing = diskstore::Record { key: "k".into(), deleted: false, metadata: None, payload: None };
    assert!(
        stores::decode_fixture_store_entry(&missing).is_err(),
        "missing redo payload must fail loud"
    );
    let tomb = diskstore::Record { key: "k".into(), deleted: true, metadata: None, payload: None };
    assert!(stores::decode_fixture_store_entry(&tomb).expect("tombstone").is_none());
    println!("diskstore fail-closed read ok");
}

// RED-1 wire-level evidence (Rust leg): the GENERATED wrapper — with zero
// handler redaction code — ships redacted records to an unentitled caller over
// a real tonic server on every record-bearing egress path: SNAPSHOT stream
// (list_events), PAGE (list_events_paged), and WATCH (snapshot item).
// Fail-closed: the caller's token carries no data grants.
async fn run_red1_wire_egress_redaction() {
    let handler = Arc::new(C2SHandler {
        read_calls: Arc::new(AtomicUsize::new(0)),
        public_read_calls: Arc::new(AtomicUsize::new(0)),
        list_events_calls: Arc::new(AtomicUsize::new(0)),
        watch_calls: Arc::new(AtomicUsize::new(0)),
    });
    let wrapper =
        contractclient_model::s2sm::internal::grpc::servers::contract::contractclient::contract_api::ValidationWrapper::new(handler);
    let auth = Arc::new(TestAuthorizer::new());
    auth.set_reject_authorize(false);
    wrapper.set_authorize_func(Some(auth)).await;
    wrapper.set_anonymize_hmac_key(b"per-deployment-key".to_vec()).await;
    let wrapper_for_server = wrapper.clone();
    let listener = TcpListener::bind("127.0.0.1:0").await.expect("bind red1 server");
    let addr = listener.local_addr().expect("red1 server addr");
    let incoming = TcpListenerStream::new(listener);
    tokio::spawn(async move {
        tonic::transport::Server::builder()
            .add_service(
                contractclient_model::types::contract::contractclient::contract_api_server::ContractApiServer::new(wrapper_for_server),
            )
            .serve_with_incoming(incoming)
            .await
            .expect("serve red1");
    });

    let channel = tonic::transport::Endpoint::from_shared(format!("http://{}", addr))
        .expect("red1 endpoint")
        .connect_timeout(Duration::from_secs(2))
        .connect()
        .await
        .expect("connect red1");
    let token_provider: contractclient_model::c2sm::internal::grpc::clients::grpc_clients::TokenProvider =
        Arc::new(|| Box::pin(async { Ok("rust-token".to_string()) }));
    let c2s = contractclient_model::c2sm::ServiceManager::new(channel, Some(token_provider));
    let client = c2s.grpc().contractclient().contract_api_client();

    // SNAPSHOT stream (list_events): payload redacted on the wire.
    let mut list_stream = client
        .list_events(valid_request())
        .await
        .expect("red1 list_events")
        .into_inner();
    let event = list_stream
        .message()
        .await
        .expect("red1 list item")
        .expect("one red1 list item");
    assert!(
        event.payload.is_empty() && event.payload_redacted,
        "SNAPSHOT egress must redact payload fail-closed, got {:?}",
        event.payload
    );
    assert_eq!(event.event_id, "event-123", "unannotated fields must survive sanitization");

    // PAGE (list_events_paged): every page item redacted on the wire.
    let (items, _next, _total) = client
        .list_events_paged_page(ContractPageRequest {
            tenant_namespace: "tenant.alpha".to_string(),
            page_size: 10,
            page_token: String::new(),
            trace_id: String::new(),
            noop: false,
        })
        .await
        .expect("red1 paged");
    assert!(!items.is_empty(), "PAGE egress returned no items");
    for item in &items {
        assert!(
            item.payload.is_empty() && item.payload_redacted,
            "PAGE egress must redact payload fail-closed, got {:?}",
            item.payload
        );
    }

    // WATCH: the snapshot item is redacted on the wire.
    let mut stream = client
        .watch(valid_watch_request())
        .await
        .expect("red1 watch")
        .into_inner();
    let event = stream
        .message()
        .await
        .expect("red1 watch event")
        .expect("one red1 watch event");
    match event.event {
        Some(contract_watch_event::Event::SnapshotItem(item)) => {
            assert!(
                item.payload.is_empty() && item.payload_redacted,
                "WATCH snapshot egress must redact payload fail-closed, got {:?}",
                item.payload
            );
        }
        other => panic!("unexpected red1 watch event: {:?}", other),
    }
    println!("red1 wire egress redaction ok");
}

// PC-W-4 wire-level evidence (Rust leg): the GENERATED wrapper validates
// client-stream and bidi messages ON THE WIRE — an invalid inbound message is
// rejected with InvalidArgument at the handler's stream pull, and an invalid
// handler response is rejected with Internal before it reaches the client.
async fn run_pcw4_wire_stream_validation() {
    let wrapper =
        contractclient_model::s2sm::internal::grpc::servers::contract::contractclient::contract_api::ValidationWrapper::new(Arc::new(StreamValidationHandler));
    let auth = Arc::new(TestAuthorizer::new());
    auth.set_reject_authorize(false);
    wrapper.set_authorize_func(Some(auth)).await;
    let wrapper_for_server = wrapper.clone();
    let listener = TcpListener::bind("127.0.0.1:0").await.expect("bind pcw4 server");
    let addr = listener.local_addr().expect("pcw4 server addr");
    let incoming = TcpListenerStream::new(listener);
    tokio::spawn(async move {
        tonic::transport::Server::builder()
            .add_service(
                contractclient_model::types::contract::contractclient::contract_api_server::ContractApiServer::new(wrapper_for_server),
            )
            .serve_with_incoming(incoming)
            .await
            .expect("serve pcw4");
    });

    let channel = tonic::transport::Endpoint::from_shared(format!("http://{}", addr))
        .expect("pcw4 endpoint")
        .connect_timeout(Duration::from_secs(2))
        .connect()
        .await
        .expect("connect pcw4");
    // Raw tonic client (NOT the managed client, which validates outbound before
    // sending) so invalid inbound messages actually reach the SERVER wrapper.
    let mut client =
        contractclient_model::types::contract::contractclient::contract_api_client::ContractApiClient::new(channel);

    fn authed<S>(stream: S) -> tonic::Request<S> {
        let mut req = tonic::Request::new(stream);
        req.metadata_mut().insert("authorization", "Bearer rust-token".parse().unwrap());
        req
    }

    let bad_event = || ContractEvent {
        tenant_namespace: "tenant.alpha".to_string(),
        payload: "no-id".to_string(),
        ..Default::default()
    };
    let trigger_bad_response = || ContractEvent {
        event_id: "event-123".to_string(),
        tenant_namespace: "tenant.alpha".to_string(),
        payload: "bad-response".to_string(),
        ..Default::default()
    };

    // Client-streaming happy path.
    let resp = client
        .send_events(authed(tokio_stream::iter(vec![valid_event()])))
        .await
        .expect("pcw4 valid send_events")
        .into_inner();
    assert_eq!(resp.result_id, "result-123");

    // Invalid inbound client-stream message -> InvalidArgument.
    let err = client
        .send_events(authed(tokio_stream::iter(vec![bad_event()])))
        .await
        .expect_err("pcw4 invalid send_events inbound must fail");
    assert_eq!(err.code(), tonic::Code::InvalidArgument);

    // Invalid handler RESPONSE -> Internal.
    let err = client
        .send_events(authed(tokio_stream::iter(vec![trigger_bad_response()])))
        .await
        .expect_err("pcw4 invalid send_events response must fail");
    assert_eq!(err.code(), tonic::Code::Internal);

    // Bidi happy path: echo one reply.
    let mut echo = client
        .assist_events(authed(tokio_stream::iter(vec![valid_event()])))
        .await
        .expect("pcw4 valid assist_events")
        .into_inner();
    let reply = echo
        .message()
        .await
        .expect("pcw4 assist_events reply")
        .expect("one pcw4 assist_events reply");
    assert!(!reply.event_id.is_empty());

    // Bidi invalid inbound -> InvalidArgument (surfaces on the response stream).
    let mut bad_in = client
        .assist_events(authed(tokio_stream::iter(vec![bad_event()])))
        .await
        .expect("pcw4 assist_events open")
        .into_inner();
    let err = bad_in
        .message()
        .await
        .expect_err("pcw4 invalid assist_events inbound must fail");
    assert_eq!(err.code(), tonic::Code::InvalidArgument);

    // Bidi invalid handler response -> Internal.
    let mut bad_out = client
        .assist_events(authed(tokio_stream::iter(vec![trigger_bad_response()])))
        .await
        .expect("pcw4 assist_events open")
        .into_inner();
    let err = bad_out
        .message()
        .await
        .expect_err("pcw4 invalid assist_events response must fail");
    assert_eq!(err.code(), tonic::Code::Internal);

    println!("pcw4 wire stream validation ok");
}

// PC-W-6 wire-level evidence (Rust leg): a mandatory-auth wrapper with NO
// authorizer wired is fail-CLOSED over the wire — a normal authenticated RPC is
// rejected with Unauthenticated ("authorizer not wired") and never reaches the
// handler, even though the client presents a valid token.
async fn run_pcw6_unwired_auth_fail_closed() {
    // Deliberately DO NOT call set_authorize_func: the authorizer slot stays None.
    let wrapper =
        contractclient_model::s2sm::internal::grpc::servers::contract::contractclient::contract_api::ValidationWrapper::new(Arc::new(StreamValidationHandler));
    let wrapper_for_server = wrapper.clone();
    let listener = TcpListener::bind("127.0.0.1:0").await.expect("bind pcw6 server");
    let addr = listener.local_addr().expect("pcw6 server addr");
    let incoming = TcpListenerStream::new(listener);
    tokio::spawn(async move {
        tonic::transport::Server::builder()
            .add_service(
                contractclient_model::types::contract::contractclient::contract_api_server::ContractApiServer::new(wrapper_for_server),
            )
            .serve_with_incoming(incoming)
            .await
            .expect("serve pcw6");
    });

    let channel = tonic::transport::Endpoint::from_shared(format!("http://{}", addr))
        .expect("pcw6 endpoint")
        .connect_timeout(Duration::from_secs(2))
        .connect()
        .await
        .expect("connect pcw6");
    // A valid token is presented, so any rejection is the SERVER fail-closing,
    // not a missing credential.
    let token_provider: contractclient_model::c2sm::internal::grpc::clients::grpc_clients::TokenProvider =
        Arc::new(|| Box::pin(async { Ok("rust-token".to_string()) }));
    let c2s = contractclient_model::c2sm::ServiceManager::new(channel, Some(token_provider));
    let client = c2s.grpc().contractclient().contract_api_client();

    let err = client
        .get_contract(valid_request())
        .await
        .expect_err("unwired mandatory-auth wrapper must fail closed");
    assert_eq!(err.code(), tonic::Code::Unauthenticated);
    println!("pcw6 unwired auth fail-closed ok");
}

// RED-1 executed evidence (Rust leg): the generated sanitize transform is
// fail-closed, the format-preserving anonymize matches the cross-language
// golden vector byte-for-byte, and the generated builder maps a token data
// grant onto RAW (raw egress) — matching the executed Go leg.
fn run_red1_sanitize_vector() {
    use contractclient_model::types::services::contractfixtures::{
        build_fixture_event_entitlement, sanitize_fixture_event,
    };
    let key: &[u8] = b"per-deployment-key";
    let raw = contractclient_model::types::services::contractfixtures::FixtureEvent {
        event_id: "evt-1".to_string(),
        payload: "secret-payload".to_string(),
        contact_email: "user@example.com".to_string(),
        notes: "sensitive-notes".to_string(),
        ..Default::default()
    };

    // Fail-closed: no entitlement — hard floors apply AND MAY_* defaults protect.
    let out = sanitize_fixture_event(&raw, None, key);
    assert!(out.payload.is_empty() && out.payload_redacted, "DROP must clear payload");
    assert_eq!(
        out.contact_email, "u6e0907e2dcbb@h400973be.example",
        "format-preserving surrogate must match the cross-language golden vector"
    );
    assert!(out.contact_email_redacted, "pseudonymize must set the indicator");
    assert!(out.notes.is_empty() && out.notes_redacted, "MAY_REDACT must default to REDACT (fail-closed)");

    // Loosening: a matching token data grant maps onto RAW via the builder.
    let mut grants = std::collections::HashSet::new();
    grants.insert("CONTRACTFIXTURES_FIXTURE_EVENT_NOTES".to_string());
    let ent = build_fixture_event_entitlement(&grants);
    let loosened = sanitize_fixture_event(&raw, Some(&ent), key);
    assert_eq!(loosened.notes, "sensitive-notes", "RAW grant must egress notes raw");
    assert!(!loosened.notes_redacted, "RAW egress must not set the indicator");
    println!("red1 sanitize vector ok");
}

type EventStream = Pin<Box<dyn tokio_stream::Stream<Item = Result<ContractEvent, tonic::Status>> + Send>>;

fn valid_event() -> ContractEvent {
    ContractEvent {
        event_id: "event-123".to_string(),
        tenant_namespace: "tenant.alpha".to_string(),
        payload: "payload-123".to_string(),
        ..Default::default()
    }
}

fn valid_request() -> ContractRequest {
    ContractRequest {
        tenant_namespace: "tenant.alpha".to_string(),
        item_id: "item-123".to_string(),
        mode: ContractMode::Read as i32,
        tags: Vec::new(),
        metadata: std::collections::HashMap::new(),
        trace_id: String::new(),
        noop: false,
    }
}

fn invalid_request() -> ContractRequest {
    ContractRequest {
        tenant_namespace: "tenant.alpha".to_string(),
        item_id: "no".to_string(),
        mode: ContractMode::Read as i32,
        tags: Vec::new(),
        metadata: std::collections::HashMap::new(),
        trace_id: String::new(),
        noop: false,
    }
}

fn forbidden_request() -> ContractRequest {
    ContractRequest {
        tenant_namespace: "tenant.beta".to_string(),
        item_id: "item-123".to_string(),
        mode: ContractMode::Read as i32,
        tags: Vec::new(),
        metadata: std::collections::HashMap::new(),
        trace_id: String::new(),
        noop: false,
    }
}

fn valid_watch_request() -> ContractWatchRequest {
    ContractWatchRequest {
        tenant_namespace: "tenant.alpha".to_string(),
        item_id: "item-123".to_string(),
        mode: ContractMode::Read as i32,
        from_revision: 0,
        trace_id: String::new(),
        noop: false,
    }
}

fn invalid_watch_request() -> ContractWatchRequest {
    ContractWatchRequest {
        tenant_namespace: "tenant.alpha".to_string(),
        item_id: "no".to_string(),
        mode: ContractMode::Read as i32,
        from_revision: 0,
        trace_id: String::new(),
        noop: false,
    }
}

fn valid_response() -> ContractResponse {
    ContractResponse {
        result_id: "result-123".to_string(),
        accepted: true,
        trace_id: String::new(),
        noop: false,
    }
}

fn invalid_response() -> ContractResponse {
    ContractResponse {
        result_id: String::new(),
        accepted: true,
        trace_id: String::new(),
        noop: false,
    }
}

fn invalid_event() -> ContractEvent {
    ContractEvent {
        event_id: String::new(),
        tenant_namespace: "tenant.alpha".to_string(),
        payload: "missing-id".to_string(),
        ..Default::default()
    }
}

fn watch_source(event: ContractEvent) -> collection::WatchSource<ContractEvent, ContractWatchChange> {
    collection::WatchSource {
        scope: "tenant.alpha".to_string(),
        snapshot: Box::pin(tokio_stream::iter(vec![Ok(event)])),
        revision: 1,
        changes: Box::new(|| Box::pin(tokio_stream::empty())),
    }
}

fn valid_change(revision: i64) -> ContractWatchChange {
    ContractWatchChange {
        r#type: contractclient_model::types::contract::contractclient::ContractChangeType::Modify as i32,
        item: Some(ContractEvent {
            event_id: format!("event-{}", revision),
            tenant_namespace: "tenant.alpha".to_string(),
            payload: "ok".to_string(),
            ..Default::default()
        }),
        revision,
        trace_id: String::new(),
        noop: false,
    }
}

fn change_stream(changes: Vec<ContractWatchChange>) -> collection::WatchStream<ContractWatchChange> {
    Box::pin(tokio_stream::iter(changes.into_iter().map(Ok)))
}

type AuditEventStream = Pin<Box<dyn tokio_stream::Stream<Item = Result<AuditEvent, tonic::Status>> + Send>>;

fn valid_audit_request(trace_id: &str) -> AuditRequest {
    AuditRequest {
        item_id: "audit-item".to_string(),
        trace_id: trace_id.to_string(),
        noop: false,
    }
}

fn failing_audit_request(trace_id: &str) -> AuditRequest {
    AuditRequest {
        item_id: "fail".to_string(),
        trace_id: trace_id.to_string(),
        noop: false,
    }
}

fn valid_audit_event() -> AuditEvent {
    AuditEvent {
        event_id: "audit-event".to_string(),
        trace_id: String::new(),
        noop: false,
    }
}

fn failing_audit_event() -> AuditEvent {
    AuditEvent {
        event_id: "fail".to_string(),
        trace_id: String::new(),
        noop: false,
    }
}

fn valid_audit_response(result_id: &str) -> AuditResponse {
    AuditResponse {
        result_id: result_id.to_string(),
        trace_id: String::new(),
        noop: false,
    }
}

struct AuditHandler;

#[tonic::async_trait]
impl contractclient_model::s2sm::internal::grpc::servers::services::contractclient::contract_audit_api::ContractAuditApiHandler for AuditHandler {
    async fn execute_audit(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        req: AuditRequest,
    ) -> Result<AuditResponse, tonic::Status> {
        if req.item_id == "fail" {
            return Err(tonic::Status::internal("audit unary failed"));
        }
        Ok(valid_audit_response("audit-unary"))
    }

    async fn generate_audit_events(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        req: AuditRequest,
    ) -> Result<tonic::Response<AuditEventStream>, tonic::Status> {
        if req.item_id == "fail" {
            return Err(tonic::Status::internal("audit stream failed"));
        }
        Ok(tonic::Response::new(
            Box::pin(tokio_stream::iter(vec![Ok(AuditEvent {
                event_id: "audit-stream".to_string(),
                trace_id: String::new(),
                noop: false,
            })])) as AuditEventStream,
        ))
    }

    async fn send_audit_events(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        req: tonic::Request<AuditEventStream>,
    ) -> Result<tonic::Response<AuditResponse>, tonic::Status> {
        let mut stream = req.into_inner();
        while let Some(item) = stream.next().await {
            let event = item?;
            if event.event_id == "fail" {
                return Err(tonic::Status::internal("audit send_events failed"));
            }
        }
        Ok(tonic::Response::new(valid_audit_response("audit-send_events")))
    }

    async fn assist_audit_events(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        req: tonic::Request<AuditEventStream>,
    ) -> Result<tonic::Response<AuditEventStream>, tonic::Status> {
        let mut stream = req.into_inner();
        while let Some(item) = stream.next().await {
            let event = item?;
            if event.event_id == "fail" {
                return Err(tonic::Status::internal("audit assist_events failed"));
            }
        }
        Ok(tonic::Response::new(
            Box::pin(tokio_stream::iter(vec![Ok(AuditEvent {
                event_id: "audit-reply".to_string(),
                trace_id: String::new(),
                noop: false,
            })])) as AuditEventStream,
        ))
    }
}

struct RecordingAccountingSink {
    events: Arc<std::sync::Mutex<Vec<AccountingEvent>>>,
}

#[tonic::async_trait]
impl am::Sink for RecordingAccountingSink {
    async fn emit(&self, event: &AccountingEvent) {
        self.events.lock().expect("accounting lock").push(event.clone());
    }
}

fn count_accounting_events(events: &[AccountingEvent]) -> (HashMap<i32, usize>, HashMap<i32, usize>) {
    let mut completed = HashMap::new();
    let mut errors = HashMap::new();
    for event in events {
        match &event.event {
            Some(accounting_event::Event::RpcCompleted(rpc)) => {
                let method = rpc.method.as_ref().map(|m| m.contractclient).unwrap_or_default();
                *completed.entry(method).or_insert(0) += 1;
            }
            Some(accounting_event::Event::RpcError(rpc)) => {
                let method = rpc.method.as_ref().map(|m| m.contractclient).unwrap_or_default();
                *errors.entry(method).or_insert(0) += 1;
            }
            _ => {}
        }
    }
    (completed, errors)
}

async fn expect_rust_audit_stream_error<F, Fut, T>(make_call: F, message: &str)
where
    F: FnOnce() -> Fut,
    Fut: std::future::Future<Output = Result<T, tonic::Status>>,
{
    let err = match make_call().await {
        Ok(_) => panic!("{message}"),
        Err(err) => err,
    };
    assert_eq!(err.code(), tonic::Code::Internal);
}

async fn run_rust_audit_accounting_trace_smoke() {
    let accounting_events = Arc::new(std::sync::Mutex::new(Vec::<AccountingEvent>::new()));
    let trace_events = Arc::new(std::sync::Mutex::new(Vec::<TraceEvent>::new()));
    let wrapper =
        contractclient_model::s2sm::internal::grpc::servers::services::contractclient::contract_audit_api::ValidationWrapper::new(
            Arc::new(AuditHandler),
        );
    wrapper
        .set_emitter(Some(Arc::new(am::Emitter::new(
            "contractclient-test".to_string(),
            Box::new(RecordingAccountingSink {
                events: accounting_events.clone(),
            }),
        ))))
        .await;
    let trace_events_for_publisher = trace_events.clone();
    wrapper
        .set_trace_publisher(Arc::new(move |event: TraceEvent| {
            trace_events_for_publisher
                .lock()
                .expect("trace lock")
                .push(event);
        }))
        .await;

    let listener = TcpListener::bind("127.0.0.1:0")
        .await
        .expect("bind rust audit server");
    let addr = listener.local_addr().expect("rust audit server addr");
    let incoming = TcpListenerStream::new(listener);
    let wrapper_for_server = wrapper.clone();
    let server = tokio::spawn(async move {
        tonic::transport::Server::builder()
            .add_service(ContractAuditApiServer::new(wrapper_for_server))
            .serve_with_incoming(incoming)
            .await
            .expect("serve rust audit");
    });

    let channel = tonic::transport::Endpoint::from_shared(format!("http://{}", addr))
        .expect("rust audit endpoint")
        .connect_timeout(Duration::from_secs(2))
        .connect()
        .await
        .expect("connect rust audit");
    let mut client = ContractAuditApiClient::new(channel);

    let unary = client
        .execute_audit(valid_audit_request("trace-success"))
        .await
        .expect("rust audit unary")
        .into_inner();
    assert_eq!(unary.result_id, "audit-unary");
    let err = client
        .execute_audit(failing_audit_request("trace-error"))
        .await
        .expect_err("failing Rust audit unary should fail");
    assert_eq!(err.code(), tonic::Code::Internal);

    let mut stream = client
        .generate_audit_events(valid_audit_request(""))
        .await
        .expect("rust audit stream")
        .into_inner();
    let event = stream
        .message()
        .await
        .expect("read rust audit stream")
        .expect("one rust audit stream event");
    assert_eq!(event.event_id, "audit-stream");
    assert!(stream.message().await.expect("rust audit stream eof").is_none());
    expect_rust_audit_stream_error(
        || async { client.generate_audit_events(failing_audit_request("")).await.map(|_| ()) },
        "failing Rust audit stream should fail",
    )
    .await;

    let send_events = client
        .send_audit_events(tokio_stream::iter(vec![valid_audit_event()]))
        .await
        .expect("rust audit send_events")
        .into_inner();
    assert_eq!(send_events.result_id, "audit-send_events");
    expect_rust_audit_stream_error(
        || async { client.send_audit_events(tokio_stream::iter(vec![failing_audit_event()])).await.map(|_| ()) },
        "failing Rust audit send_events should fail",
    )
    .await;

    let mut assist_events = client
        .assist_audit_events(tokio_stream::iter(vec![valid_audit_event()]))
        .await
        .expect("rust audit assist_events")
        .into_inner();
    let event = assist_events
        .message()
        .await
        .expect("read rust audit assist_events")
        .expect("one rust audit assist_events event");
    assert_eq!(event.event_id, "audit-reply");
    assert!(assist_events.message().await.expect("rust audit assist_events eof").is_none());
    expect_rust_audit_stream_error(
        || async { client.assist_audit_events(tokio_stream::iter(vec![failing_audit_event()])).await.map(|_| ()) },
        "failing Rust audit assist_events should fail",
    )
    .await;

    server.abort();

    let accounting_snapshot = accounting_events.lock().expect("accounting lock").clone();
    let (completed, errors) = count_accounting_events(&accounting_snapshot);
    for method in [
        services_pb::ContractclientRpCs::ContractclientRpcsExecuteAudit as i32,
        services_pb::ContractclientRpCs::ContractclientRpcsGenerateAuditEvents as i32,
        services_pb::ContractclientRpCs::ContractclientRpcsSendAuditEvents as i32,
        services_pb::ContractclientRpCs::ContractclientRpcsAssistAuditEvents as i32,
    ] {
        assert_eq!(completed.get(&method).copied().unwrap_or_default(), 1, "rust audit rpc_completed[{method}]");
        assert_eq!(errors.get(&method).copied().unwrap_or_default(), 1, "rust audit rpc_error[{method}]");
    }

    let trace_snapshot = trace_events.lock().expect("trace lock").clone();
    let mut trace_seen: HashMap<String, HashMap<i32, TraceEvent>> = HashMap::new();
    for event in trace_snapshot {
        trace_seen
            .entry(event.trace_id.clone())
            .or_default()
            .insert(event.kind, event);
    }
    for (trace_id, want_error) in [
        ("trace-success", ""),
        ("trace-error", "audit unary failed"),
    ] {
        let by_kind = trace_seen.get(trace_id).unwrap_or_else(|| panic!("missing rust trace id {trace_id}"));
        assert!(by_kind.contains_key(&(TraceEventKind::InternalRequest as i32)), "missing rust request trace for {trace_id}");
        let response = by_kind
            .get(&(TraceEventKind::InternalResponse as i32))
            .unwrap_or_else(|| panic!("missing rust response trace for {trace_id}"));
        assert!(response.error.contains(want_error), "rust response trace error for {trace_id} = {}", response.error);
        assert_eq!(
            response.rpc.as_ref().map(|rpc| rpc.contractclient).unwrap_or_default(),
            services_pb::ContractclientRpCs::ContractclientRpcsExecuteAudit as i32,
        );
    }
}

async fn run_eventbus_subscriber_metadata_smoke() {
    let record_meta = RecordMeta {
        key: b"fixture-key".to_vec(),
        topic: "contractfixtures.FixtureState.events".to_string(),
        partition: 7,
        offset: 42,
        timestamp: Some(prost_wkt_types::Timestamp {
            seconds: 1_700_000_000,
            nanos: 123_000_000,
        }),
    };
    let event = FixtureEvent {
        trace_id: "fixture-subscribe-event".to_string(),
        ..Default::default()
    };
    let frame = encode_subscribe_envelope_for_test(&record_meta, &event);
    let captured_request = Arc::new(std::sync::Mutex::new(None::<SubscribeRequest>));
    let listener = TcpListener::bind("127.0.0.1:0")
        .await
        .expect("bind rust EventBus subscribe server");
    let addr = listener.local_addr().expect("rust EventBus subscribe addr");
    let service = EventBusTypedSubscribeService {
        frame: Bytes::from(frame),
        captured_request: captured_request.clone(),
    };
    let server = tokio::spawn(async move {
        tonic::transport::Server::builder()
            .add_service(service)
            .serve_with_incoming(TcpListenerStream::new(listener))
            .await
            .expect("serve rust EventBus subscribe");
    });

    let channel = tonic::transport::Endpoint::from_shared(format!("http://{}", addr))
        .expect("rust EventBus subscribe endpoint")
        .connect_timeout(Duration::from_secs(2))
        .connect()
        .await
        .expect("connect rust EventBus subscribe");
    let subscriber = EventBusSubscriber::new(
        channel,
        "contractclient",
        Arc::new(|| Box::pin(async { Ok("subscriber-token".to_string()) })),
    );

    let (tx, rx) = tokio::sync::oneshot::channel::<(FixtureEvent, RecordMeta)>();
    let tx = Arc::new(std::sync::Mutex::new(Some(tx)));
    let handler: EventBusSubscribeHandler = Arc::new(move |event, meta| {
        let tx = tx.clone();
        Box::pin(async move {
            if let Some(tx) = tx.lock().expect("rust subscriber sender lock").take() {
                let _ = tx.send((event, meta));
            }
            Ok(())
        })
    });
    let sub_task = tokio::spawn(async move {
        subscriber.subscribe(handler).await;
    });
    let (got_event, got_meta) = tokio::time::timeout(Duration::from_secs(5), rx)
        .await
        .expect("rust subscriber metadata delivery timed out")
        .expect("rust subscriber metadata sender dropped");
    sub_task.abort();
    server.abort();

    assert_eq!(got_event.trace_id, event.trace_id);
    assert_eq!(got_meta.key, record_meta.key);
    assert_eq!(got_meta.topic, record_meta.topic);
    assert_eq!(got_meta.partition, record_meta.partition);
    assert_eq!(got_meta.offset, record_meta.offset);
    assert_eq!(got_meta.timestamp, record_meta.timestamp);
    let req = captured_request
        .lock()
        .expect("rust subscribe request lock")
        .clone()
        .expect("rust subscriber did not send SubscribeRequest");
    assert_eq!(req.consumer_group, "contractclient");
    assert!(matches!(
        req.start_from,
        Some(contractclient_model::types::generated::eventbus_support::eventbus_support::subscribe_request::StartFrom::Earliest(true))
    ));
}

// Executes the generated record dispatch the ServiceManager awaits on every
// subscribed FixtureEvent: handle_fixture_event awaits the FixtureEventRecord
// projection's insert with its typed FixtureEventRecordRow, writes nothing on a
// trace-round noop event, and fails the delivery with the insert's error.
async fn run_record_dispatch_smoke() {
    use contractclient_model::types::services::contractfixtures::{
        handle_fixture_event, FixtureEventRecordInsert, FixtureEventRecordRow,
    };
    let rows = Arc::new(std::sync::Mutex::new(Vec::<FixtureEventRecordRow>::new()));
    let sink = rows.clone();
    let insert: FixtureEventRecordInsert = Arc::new(move |row| {
        let sink = sink.clone();
        Box::pin(async move {
            sink.lock().expect("rust dispatch rows lock").push(row);
            Ok(())
        })
    });
    let event = FixtureEvent {
        event_id: "evt-dispatch".to_string(),
        tenant_namespace: "tenant-a".to_string(),
        timestamp_ms: 1_700_000_000_123,
        ..Default::default()
    };
    handle_fixture_event(&event, Some(&insert)).await.expect("rust record dispatch");
    {
        let got = rows.lock().expect("rust dispatch rows lock");
        assert_eq!(got.len(), 1, "rust record dispatch rows = {:?}", *got);
        assert_eq!(got[0].event_id, "evt-dispatch", "rust dispatch row = {:?}", got[0]);
        assert_eq!(got[0].tenant_namespace, "tenant-a", "rust dispatch row = {:?}", got[0]);
        assert_eq!(got[0].timestamp_ms, 1_700_000_000_123, "rust dispatch row = {:?}", got[0]);
    }

    rows.lock().expect("rust dispatch rows lock").clear();
    let noop = FixtureEvent { event_id: "evt-noop".to_string(), noop: true, ..Default::default() };
    handle_fixture_event(&noop, Some(&insert)).await.expect("rust noop dispatch");
    assert!(rows.lock().expect("rust dispatch rows lock").is_empty(), "a trace-round noop event must write no row");

    let failing: FixtureEventRecordInsert = Arc::new(|_row| Box::pin(async { Err("store down".into()) }));
    let err = handle_fixture_event(&event, Some(&failing)).await.expect_err("rust dispatch must fail with the insert");
    assert_eq!(err.to_string(), "store down");
}

fn encode_subscribe_envelope_for_test(meta: &RecordMeta, event: &FixtureEvent) -> Vec<u8> {
    let mut out = Vec::new();
    append_length_delimited_field_for_test(&mut out, 1, meta.encode_to_vec());
    append_length_delimited_field_for_test(&mut out, 2, event.encode_to_vec());
    out
}

fn append_length_delimited_field_for_test(out: &mut Vec<u8>, field: u32, data: Vec<u8>) {
    append_varint_for_test(out, ((field as u64) << 3) | 2);
    append_varint_for_test(out, data.len() as u64);
    out.extend_from_slice(&data);
}

fn append_varint_for_test(out: &mut Vec<u8>, mut value: u64) {
    while value >= 0x80 {
        out.push((value as u8 & 0x7f) | 0x80);
        value >>= 7;
    }
    out.push(value as u8);
}

#[derive(Clone)]
struct EventBusTypedSubscribeService {
    frame: Bytes,
    captured_request: Arc<std::sync::Mutex<Option<SubscribeRequest>>>,
}

impl tonic::server::NamedService for EventBusTypedSubscribeService {
    const NAME: &'static str = "contractfixtures.FixtureStateStateService";
}

impl<B> tonic::codegen::Service<tonic::codegen::http::Request<B>> for EventBusTypedSubscribeService
where
    B: Body + Send + 'static,
    B::Error: Into<StdError> + Send + 'static,
{
    type Response = tonic::codegen::http::Response<tonic::body::Body>;
    type Error = Infallible;
    type Future = BoxFuture<Self::Response, Self::Error>;

    fn poll_ready(&mut self, _cx: &mut Context<'_>) -> Poll<Result<(), Self::Error>> {
        Poll::Ready(Ok(()))
    }

    fn call(&mut self, req: tonic::codegen::http::Request<B>) -> Self::Future {
        match req.uri().path() {
            // Per-topic subscribe name (eventbus.PerTopicSubscribeParts), not the flat
            // EventBusTypedService/Subscribe<Stem> this fixture served before 070d210a.
            "/contractfixtures.FixtureStateStateService/SubscribeToEvents" => {
                struct SubscribeSvc {
                    frame: Bytes,
                    captured_request: Arc<std::sync::Mutex<Option<SubscribeRequest>>>,
                }

                impl tonic::server::StreamingService<SubscribeRequest> for SubscribeSvc {
                    type Response = Bytes;
                    type ResponseStream = Pin<Box<dyn tokio_stream::Stream<Item = Result<Bytes, tonic::Status>> + Send>>;
                    type Future = BoxFuture<tonic::Response<Self::ResponseStream>, tonic::Status>;

                    fn call(&mut self, request: tonic::Request<tonic::Streaming<SubscribeRequest>>) -> Self::Future {
                        let frame = self.frame.clone();
                        let captured_request = self.captured_request.clone();
                        Box::pin(async move {
                            let mut stream = request.into_inner();
                            if let Some(req) = stream.message().await? {
                                *captured_request
                                    .lock()
                                    .expect("rust captured subscribe request lock") = Some(req);
                            }
                            let output = tokio_stream::iter(vec![Ok(frame)]);
                            Ok(tonic::Response::new(Box::pin(output) as Self::ResponseStream))
                        })
                    }
                }

                let method = SubscribeSvc {
                    frame: self.frame.clone(),
                    captured_request: self.captured_request.clone(),
                };
                let fut = async move {
                    let codec = EventBusSubscribeServerCodec::default();
                    let mut grpc = tonic::server::Grpc::new(codec);
                    let res = grpc.streaming(method, req).await;
                    Ok(res)
                };
                Box::pin(fut)
            }
            _ => Box::pin(async move {
                let mut response = tonic::codegen::http::Response::new(tonic::body::Body::default());
                let headers = response.headers_mut();
                headers.insert(
                    tonic::Status::GRPC_STATUS,
                    (tonic::Code::Unimplemented as i32).into(),
                );
                headers.insert(
                    tonic::codegen::http::header::CONTENT_TYPE,
                    tonic::metadata::GRPC_CONTENT_TYPE,
                );
                Ok(response)
            }),
        }
    }
}

#[derive(Clone, Default)]
struct EventBusSubscribeServerCodec;

#[derive(Clone, Default)]
struct EventBusResponseEncoder;

#[derive(Clone, Default)]
struct EventBusRequestDecoder;

impl Codec for EventBusSubscribeServerCodec {
    type Encode = Bytes;
    type Decode = SubscribeRequest;
    type Encoder = EventBusResponseEncoder;
    type Decoder = EventBusRequestDecoder;

    fn encoder(&mut self) -> Self::Encoder {
        EventBusResponseEncoder
    }

    fn decoder(&mut self) -> Self::Decoder {
        EventBusRequestDecoder
    }
}

impl Encoder for EventBusResponseEncoder {
    type Item = Bytes;
    type Error = tonic::Status;

    fn encode(&mut self, item: Self::Item, dst: &mut EncodeBuf<'_>) -> Result<(), Self::Error> {
        dst.put_slice(item.as_ref());
        Ok(())
    }
}

impl Decoder for EventBusRequestDecoder {
    type Item = SubscribeRequest;
    type Error = tonic::Status;

    fn decode(&mut self, src: &mut DecodeBuf<'_>) -> Result<Option<Self::Item>, Self::Error> {
        let bytes = src.copy_to_bytes(src.remaining());
        SubscribeRequest::decode(bytes)
            .map(Some)
            .map_err(|e| tonic::Status::internal(format!("decode SubscribeRequest: {}", e)))
    }
}

async fn run_collection_watch_runtime_contract() {
    let revision_of = |change: &ContractWatchChange| change.revision;

    let resume_hub = collection::WatchHub::new(4, 0);
    let resumed = resume_hub
        .watch(
            "tenant.alpha".to_string(),
            1,
            || change_stream(vec![valid_change(1), valid_change(2), valid_change(3)]),
            revision_of,
        )
        .await
        .try_collect::<Vec<_>>()
        .await
        .expect("rust watch resume replay");
    assert_eq!(resumed.iter().map(|c| c.revision).collect::<Vec<_>>(), vec![2, 3]);

    let relist_hub = collection::WatchHub::new(1, 0);
    let (relist_tx, relist_rx) = tokio::sync::mpsc::channel(4);
    relist_tx.send(Ok(valid_change(2))).await.expect("seed relist revision 2");
    relist_tx.send(Ok(valid_change(3))).await.expect("seed relist revision 3");
    let primary = relist_hub
        .watch(
            "tenant.alpha".to_string(),
            0,
            || Box::pin(ReceiverStream::new(relist_rx)) as collection::WatchStream<ContractWatchChange>,
            revision_of,
        )
        .await;
    tokio::time::sleep(Duration::from_millis(50)).await;
    let mut relist = relist_hub
        .watch("tenant.alpha".to_string(), 1, || change_stream(vec![]), revision_of)
        .await;
    let err = relist
        .next()
        .await
        .expect("rust relist should emit an error")
        .expect_err("rust relist should fail");
    assert_eq!(err.code(), tonic::Code::OutOfRange);
    drop(relist_tx);
    drop(primary);

    let idle_hub = collection::WatchHub::new(2, 1);
    let (_tx, rx) = tokio::sync::mpsc::channel(1);
    let mut idle = idle_hub
        .watch(
            "tenant.alpha".to_string(),
            0,
            || Box::pin(ReceiverStream::new(rx)) as collection::WatchStream<ContractWatchChange>,
            revision_of,
        )
        .await;
    let start = tokio::time::Instant::now();
    let err = idle
        .next()
        .await
        .expect("rust idle timeout should emit an error")
        .expect_err("rust idle timeout should fail");
    assert_eq!(err.code(), tonic::Code::DeadlineExceeded);
    assert!(start.elapsed() < Duration::from_secs(3));

    let teardown_hub = collection::WatchHub::new(2, 0);
    let mut first = teardown_hub
        .watch(
            "tenant.alpha".to_string(),
            0,
            || change_stream(vec![valid_change(1)]),
            revision_of,
        )
        .await;
    assert_eq!(first.next().await.unwrap().unwrap().revision, 1);
    drop(first);
    tokio::time::sleep(Duration::from_millis(50)).await;
    let mut fresh = teardown_hub
        .watch(
            "tenant.alpha".to_string(),
            0,
            || change_stream(vec![valid_change(2)]),
            revision_of,
        )
        .await;
    let next = tokio::time::timeout(Duration::from_secs(2), fresh.next())
        .await
        .expect("rust fresh watcher timed out")
        .expect("rust fresh watcher ended")
        .expect("rust fresh watcher errored");
    assert_eq!(next.revision, 2);
}

struct C2SHandler {
    read_calls: Arc<AtomicUsize>,
    public_read_calls: Arc<AtomicUsize>,
    list_events_calls: Arc<AtomicUsize>,
    watch_calls: Arc<AtomicUsize>,
}

#[tonic::async_trait]
impl contractclient_model::s2sm::internal::grpc::servers::contract::contractclient::contract_api::ContractApiHandler for C2SHandler {
    async fn get_contract(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        _req: ContractRequest,
    ) -> Result<ContractResponse, tonic::Status> {
        self.read_calls.fetch_add(1, Ordering::SeqCst);
        Ok(valid_response())
    }

    async fn get_public_contract(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        req: ContractRequest,
    ) -> Result<ContractResponse, tonic::Status> {
        self.public_read_calls.fetch_add(1, Ordering::SeqCst);
        if req.item_id == "bad-response" {
            return Ok(invalid_response());
        }
        Ok(valid_response())
    }

    async fn list_events(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        req: ContractRequest,
    ) -> Result<tonic::Response<EventStream>, tonic::Status> {
        self.list_events_calls.fetch_add(1, Ordering::SeqCst);
        let event = if req.item_id == "bad-event" {
            invalid_event()
        } else {
            valid_event()
        };
        Ok(tonic::Response::new(Box::pin(tokio_stream::iter(vec![Ok(event)])) as EventStream))
    }

    async fn list_events_paged(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        _req: ContractPageRequest,
        _page: collection::PageParams,
    ) -> Result<collection::Page<ContractEvent>, tonic::Status> {
        // One full page, then exhausted (next_after = None -> empty next_page_token).
        Ok(collection::Page { items: vec![valid_event()], next_after: None, total: 1 })
    }

    async fn watch(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        req: ContractWatchRequest,
    ) -> Result<collection::WatchSource<ContractEvent, ContractWatchChange>, tonic::Status> {
        self.watch_calls.fetch_add(1, Ordering::SeqCst);
        let event = if req.item_id == "bad-event" {
            invalid_event()
        } else {
            valid_event()
        };
        Ok(watch_source(event))
    }

    async fn send_events(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        _req: tonic::Request<EventStream>,
    ) -> Result<tonic::Response<ContractResponse>, tonic::Status> {
        Ok(tonic::Response::new(valid_response()))
    }

    async fn assist_events(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        _req: tonic::Request<EventStream>,
    ) -> Result<tonic::Response<EventStream>, tonic::Status> {
        Ok(tonic::Response::new(Box::pin(tokio_stream::empty()) as EventStream))
    }
}

// StreamValidationHandler drains/echoes streams so the generated wrapper's
// per-message validation is actually exercised (PC-W-4): a handler that never
// pulls from the request stream would let invalid inbound messages pass
// unvalidated. A "bad-response" payload makes it emit an INVALID message so the
// egress validator trips.
struct StreamValidationHandler;

#[tonic::async_trait]
impl contractclient_model::s2sm::internal::grpc::servers::contract::contractclient::contract_api::ContractApiHandler for StreamValidationHandler {
    async fn get_contract(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        _req: ContractRequest,
    ) -> Result<ContractResponse, tonic::Status> {
        Ok(valid_response())
    }

    async fn get_public_contract(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        _req: ContractRequest,
    ) -> Result<ContractResponse, tonic::Status> {
        Ok(valid_response())
    }

    async fn list_events(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        _req: ContractRequest,
    ) -> Result<tonic::Response<EventStream>, tonic::Status> {
        Ok(tonic::Response::new(Box::pin(tokio_stream::iter(vec![Ok(valid_event())])) as EventStream))
    }

    async fn list_events_paged(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        _req: ContractPageRequest,
        _page: collection::PageParams,
    ) -> Result<collection::Page<ContractEvent>, tonic::Status> {
        Ok(collection::Page { items: vec![valid_event()], next_after: None, total: 1 })
    }

    async fn watch(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        _req: ContractWatchRequest,
    ) -> Result<collection::WatchSource<ContractEvent, ContractWatchChange>, tonic::Status> {
        Ok(watch_source(valid_event()))
    }

    // Client-streaming: drain the (validated) inbound stream — an invalid
    // message surfaces as Err(InvalidArgument) here and must propagate. A
    // "bad-response" trigger returns an INVALID unary response (missing required
    // result_id) so the wrapper's response validator trips (INTERNAL).
    async fn send_events(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        req: tonic::Request<EventStream>,
    ) -> Result<tonic::Response<ContractResponse>, tonic::Status> {
        let mut stream = req.into_inner();
        let mut bad = false;
        while let Some(item) = stream.next().await {
            let ev = item?;
            if ev.payload == "bad-response" {
                bad = true;
            }
        }
        if bad {
            Ok(tonic::Response::new(ContractResponse { accepted: true, ..Default::default() }))
        } else {
            Ok(tonic::Response::new(valid_response()))
        }
    }

    // Bidi: echo. Each inbound event maps to a reply, propagating inbound
    // validation errors; a "bad-response" trigger yields an INVALID reply so the
    // egress validator trips per item (INTERNAL).
    async fn assist_events(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        req: tonic::Request<EventStream>,
    ) -> Result<tonic::Response<EventStream>, tonic::Status> {
        let inbound = req.into_inner();
        let out = inbound.map(|item| {
            item.map(|ev| {
                if ev.payload == "bad-response" {
                    ContractEvent {
                        tenant_namespace: "tenant.alpha".to_string(),
                        payload: "oops".to_string(),
                        ..Default::default()
                    }
                } else {
                    valid_event()
                }
            })
        });
        Ok(tonic::Response::new(Box::pin(out) as EventStream))
    }
}

struct TestAuthorizer {
    authorize_calls: AtomicUsize,
    namespace_calls: AtomicUsize,
    reject_authorize: AtomicBool,
}

impl TestAuthorizer {
    fn new() -> Self {
        Self {
            authorize_calls: AtomicUsize::new(0),
            namespace_calls: AtomicUsize::new(0),
            reject_authorize: AtomicBool::new(true),
        }
    }

    fn set_reject_authorize(&self, reject: bool) {
        self.reject_authorize.store(reject, Ordering::SeqCst);
    }

    fn require_token(authorization: Option<&str>) -> Result<(), tonic::Status> {
        match authorization {
            Some("Bearer rust-token") => Ok(()),
            _ => Err(tonic::Status::unauthenticated("missing authorization header")),
        }
    }
}

#[tonic::async_trait]
impl contractclient_model::s2sm::internal::grpc::servers::contract::contractclient::contract_api::Authorizer for TestAuthorizer {
    async fn authorize(&self, _full_method: &str, authorization: Option<&str>) -> Result<HashMap<String, String>, tonic::Status> {
        self.authorize_calls.fetch_add(1, Ordering::SeqCst);
        Self::require_token(authorization)?;
        if self.reject_authorize.load(Ordering::SeqCst) {
            return Err(tonic::Status::unauthenticated("missing authorization header"));
        }
        Ok(HashMap::new())
    }

    async fn check_at_or_below(&self, authorization: Option<&str>, target_namespace: &str) -> Result<HashMap<String, String>, tonic::Status> {
        self.namespace_calls.fetch_add(1, Ordering::SeqCst);
        Self::require_token(authorization)?;
        if target_namespace != "/" && target_namespace != "" && target_namespace != "tenant.alpha" {
            return Err(tonic::Status::permission_denied("caller namespace is not at-or-above target"));
        }
        Ok(HashMap::new())
    }

    async fn authenticate(&self, authorization: Option<&str>) -> Result<HashMap<String, String>, tonic::Status> {
        Self::require_token(authorization)?;
        Ok(HashMap::new())
    }
}

async fn run_c2s_validation_smoke() {
    let read_calls = Arc::new(AtomicUsize::new(0));
    let public_read_calls = Arc::new(AtomicUsize::new(0));
    let list_events_calls = Arc::new(AtomicUsize::new(0));
    let watch_calls = Arc::new(AtomicUsize::new(0));
    let handler = Arc::new(C2SHandler {
        read_calls: read_calls.clone(),
        public_read_calls: public_read_calls.clone(),
        list_events_calls: list_events_calls.clone(),
        watch_calls: watch_calls.clone(),
    });
    let wrapper =
        contractclient_model::s2sm::internal::grpc::servers::contract::contractclient::contract_api::ValidationWrapper::new(handler);
    let wrapper_for_server = wrapper.clone();
    let listener = TcpListener::bind("127.0.0.1:0")
        .await
        .expect("bind rust C2S server");
    let addr = listener.local_addr().expect("rust C2S server addr");
    let incoming = TcpListenerStream::new(listener);
    let server = tokio::spawn(async move {
        tonic::transport::Server::builder()
            .add_service(
                contractclient_model::types::contract::contractclient::contract_api_server::ContractApiServer::new(wrapper_for_server),
            )
            .serve_with_incoming(incoming)
            .await
            .expect("serve rust C2S");
    });

    let channel = tonic::transport::Endpoint::from_shared(format!("http://{}", addr))
        .expect("rust C2S endpoint")
        .connect_timeout(Duration::from_secs(2))
        .connect()
        .await
        .expect("connect rust C2S");
    let c2s = contractclient_model::c2sm::ServiceManager::new(channel.clone(), None);
    let client = c2s.grpc().contractclient().contract_api_client();

    let err = client
        .get_contract(valid_request())
        .await
        .expect_err("protected Rust C2S read should fail without a token provider");
    assert_eq!(err.code(), tonic::Code::Unauthenticated);
    assert_eq!(read_calls.load(Ordering::SeqCst), 0);

    let auth = Arc::new(TestAuthorizer::new());
    wrapper.set_authorize_func(Some(auth.clone())).await;

    let err = client
        .get_contract(valid_request())
        .await
        .expect_err("protected Rust C2S read should fail before send without a token provider");
    assert_eq!(err.code(), tonic::Code::Unauthenticated);
    assert_eq!(auth.authorize_calls.load(Ordering::SeqCst), 0);
    assert_eq!(read_calls.load(Ordering::SeqCst), 0);

    auth.set_reject_authorize(false);
    let token_provider: contractclient_model::c2sm::internal::grpc::clients::grpc_clients::TokenProvider =
        Arc::new(|| Box::pin(async { Ok("rust-token".to_string()) }));
    let c2s_auth = contractclient_model::c2sm::ServiceManager::new(channel.clone(), Some(token_provider));
    let auth_client = c2s_auth.grpc().contractclient().contract_api_client();

    let resp = auth_client
        .get_contract(valid_request())
        .await
        .expect("rust C2S authorized read")
        .into_inner();
    assert_eq!(resp.result_id, "result-123");
    assert!(resp.accepted);
    assert_eq!(read_calls.load(Ordering::SeqCst), 1);
    assert_eq!(auth.namespace_calls.load(Ordering::SeqCst), 1);

    let err = auth_client
        .get_contract(forbidden_request())
        .await
        .expect_err("namespace-mismatched Rust C2S read should fail");
    assert_eq!(err.code(), tonic::Code::PermissionDenied);
    assert_eq!(read_calls.load(Ordering::SeqCst), 1);
    assert_eq!(auth.namespace_calls.load(Ordering::SeqCst), 2);

    let failing_provider: contractclient_model::c2sm::internal::grpc::clients::grpc_clients::TokenProvider =
        Arc::new(|| {
            Box::pin(async {
                Err(Box::new(std::io::Error::new(
                    std::io::ErrorKind::Other,
                    "identity unavailable",
                )) as Box<dyn std::error::Error + Send + Sync>)
            })
        });
    let c2s_failing = contractclient_model::c2sm::ServiceManager::new(channel.clone(), Some(failing_provider));
    let failing_client = c2s_failing.grpc().contractclient().contract_api_client();
    let err = failing_client
        .get_contract(valid_request())
        .await
        .expect_err("protected Rust C2S read with failing token provider should fail");
    assert_eq!(err.code(), tonic::Code::Unauthenticated);
    assert_eq!(read_calls.load(Ordering::SeqCst), 1);

    let resp = client
        .get_public_contract(valid_request())
        .await
        .expect("rust C2S public read")
        .into_inner();
    assert_eq!(resp.result_id, "result-123");
    assert!(resp.accepted);
    assert_eq!(public_read_calls.load(Ordering::SeqCst), 1);

    let resp = failing_client
        .get_public_contract(valid_request())
        .await
        .expect("rust C2S public read with failing token provider")
        .into_inner();
    assert_eq!(resp.result_id, "result-123");
    assert!(resp.accepted);
    assert_eq!(public_read_calls.load(Ordering::SeqCst), 2);

    let err = client
        .get_public_contract(invalid_request())
        .await
        .expect_err("invalid Rust C2S request should fail");
    assert_eq!(err.code(), tonic::Code::InvalidArgument);
    assert_eq!(public_read_calls.load(Ordering::SeqCst), 2);

    let mut bad_response = valid_request();
    bad_response.item_id = "bad-response".to_string();
    let err = client
        .get_public_contract(bad_response)
        .await
        .expect_err("invalid Rust C2S response should fail");
    assert_eq!(err.code(), tonic::Code::Internal);
    assert_eq!(public_read_calls.load(Ordering::SeqCst), 3);

    let err = client
        .watch(valid_watch_request())
        .await
        .expect_err("auth-any-user Rust C2S watch should fail before send without a token provider");
    assert_eq!(err.code(), tonic::Code::Unauthenticated);
    assert_eq!(watch_calls.load(Ordering::SeqCst), 0);

    let mut stream = auth_client
        .watch(valid_watch_request())
        .await
        .expect("rust C2S watch")
        .into_inner();
    let event = stream
        .message()
        .await
        .expect("read rust C2S watch event")
        .expect("one rust C2S watch event");
    match event.event {
        Some(contract_watch_event::Event::SnapshotItem(item)) => assert_eq!(item.event_id, "event-123"),
        other => panic!("unexpected rust C2S watch event: {:?}", other),
    }
    let done = stream
        .message()
        .await
        .expect("read rust C2S watch snapshot_done")
        .expect("one rust C2S watch snapshot_done event");
    match done.event {
        Some(contract_watch_event::Event::SnapshotDone(revision)) => assert_eq!(revision, 1),
        other => panic!("unexpected rust C2S watch snapshot_done event: {:?}", other),
    }
    assert!(stream.message().await.expect("read rust C2S watch eof").is_none());
    assert_eq!(watch_calls.load(Ordering::SeqCst), 1);

    let err = auth_client
        .watch(invalid_watch_request())
        .await
        .expect_err("invalid Rust C2S watch request should fail");
    assert_eq!(err.code(), tonic::Code::InvalidArgument);
    assert_eq!(watch_calls.load(Ordering::SeqCst), 1);

    let mut bad_watch = valid_watch_request();
    bad_watch.item_id = "bad-event".to_string();
    let mut stream = auth_client
        .watch(bad_watch)
        .await
        .expect("rust C2S watch bad-event")
        .into_inner();
    let err = stream
        .message()
        .await
        .expect_err("invalid Rust C2S watch response should fail");
    assert_eq!(err.code(), tonic::Code::Internal);
    assert_eq!(watch_calls.load(Ordering::SeqCst), 2);

    let err = client
        .list_events(valid_request())
        .await
        .expect_err("auth-any-user Rust C2S ListEvents should fail before send without a token provider");
    assert_eq!(err.code(), tonic::Code::Unauthenticated);
    assert_eq!(list_events_calls.load(Ordering::SeqCst), 0);

    let mut list_stream = auth_client
        .list_events(valid_request())
        .await
        .expect("rust C2S ListEvents")
        .into_inner();
    let event = list_stream
        .message()
        .await
        .expect("read rust C2S ListEvents event")
        .expect("one rust C2S ListEvents event");
    assert_eq!(event.event_id, "event-123");
    assert!(list_stream.message().await.expect("read rust C2S ListEvents eof").is_none());
    assert_eq!(list_events_calls.load(Ordering::SeqCst), 1);

    let err = auth_client
        .list_events(invalid_request())
        .await
        .expect_err("invalid Rust C2S ListEvents request should fail");
    assert_eq!(err.code(), tonic::Code::InvalidArgument);
    assert_eq!(list_events_calls.load(Ordering::SeqCst), 1);

    let mut bad_event = valid_request();
    bad_event.item_id = "bad-event".to_string();
    let mut list_stream = auth_client
        .list_events(bad_event)
        .await
        .expect("rust C2S ListEvents bad-event")
        .into_inner();
    let err = list_stream
        .message()
        .await
        .expect_err("invalid Rust C2S ListEvents response should fail");
    assert_eq!(err.code(), tonic::Code::Internal);
    assert_eq!(list_events_calls.load(Ordering::SeqCst), 2);

    server.abort();
}

fn read_http_request(stream: &mut std::net::TcpStream) -> String {
    let mut reader = BufReader::new(stream.try_clone().expect("clone stream"));
    let mut headers = String::new();
    let mut content_length = 0usize;
    loop {
        let mut line = String::new();
        reader.read_line(&mut line).expect("read request line");
        if line == "\r\n" || line.is_empty() {
            break;
        }
        if let Some(rest) = line.strip_prefix("Content-Length:").or_else(|| line.strip_prefix("content-length:")) {
            content_length = rest.trim().parse().expect("parse content length");
        }
        headers.push_str(&line);
    }
    let mut body = vec![0u8; content_length];
    reader.read_exact(&mut body).expect("read request body");
    format!("{}{}", headers, String::from_utf8(body).expect("utf8 body"))
}

fn start_http_validation_server() -> (String, Arc<AtomicUsize>, thread::JoinHandle<()>) {
    let listener = StdTcpListener::bind("127.0.0.1:0").expect("bind rust W2S HTTP server");
    let addr = listener.local_addr().expect("rust W2S HTTP server addr");
    let requests = Arc::new(AtomicUsize::new(0));
    let seen = requests.clone();
    let handle = thread::spawn(move || {
        for _ in 0..2 {
            let (mut stream, _) = listener.accept().expect("accept rust W2S HTTP request");
            stream
                .set_read_timeout(Some(Duration::from_secs(5)))
                .expect("set read timeout");
            let request = read_http_request(&mut stream);
            seen.fetch_add(1, Ordering::SeqCst);
            let body = if request.contains("bad-response") {
                r#"{"result":{"accepted":true}}"#
            } else {
                r#"{"result":{"resultId":"rust-result","accepted":true}}"#
            };
            let response = format!(
                "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\n\r\n{}",
                body.len(),
                body
            );
            stream.write_all(response.as_bytes()).expect("write rust W2S HTTP response");
        }
    });
    (format!("http://{}", addr), requests, handle)
}

async fn run_w2s_http_validation_smoke() {
    let (base_url, requests, server) = start_http_validation_server();
    let w2s = contractclient_model::w2sm::ServiceManager::with_token_provider(base_url.clone(), None);
    let client = w2s
        .w2s_clients()
        .contractclient()
        .contract_api_client()
        .clone()
        .with_connect_timeout(Duration::from_secs(2));

    let resp = client.get_public_contract(valid_request()).await.expect("rust W2S public read").into_inner();
    assert_eq!(resp.result_id, "rust-result");
    assert_eq!(requests.load(Ordering::SeqCst), 1);

    let err = client.get_public_contract(invalid_request()).await.expect_err("invalid Rust W2S request should fail");
    assert_eq!(err.code(), tonic::Code::InvalidArgument);
    assert_eq!(requests.load(Ordering::SeqCst), 1);

    let mut bad_response = valid_request();
    bad_response.item_id = "bad-response".to_string();
    let err = client.get_public_contract(bad_response).await.expect_err("invalid Rust W2S response should fail");
    assert_eq!(err.code(), tonic::Code::Internal);
    assert_eq!(requests.load(Ordering::SeqCst), 2);

    let failing_provider: contractclient_model::w2sm::internal::grpc_over_rest::w2s_clients::TokenProvider =
        Arc::new(|| {
            Box::pin(async {
                Err(Box::new(std::io::Error::new(
                    std::io::ErrorKind::Other,
                    "identity unavailable",
                )) as Box<dyn std::error::Error + Send + Sync>)
            })
        });
    let failing_w2s = contractclient_model::w2sm::ServiceManager::with_token_provider(
        base_url,
        Some(failing_provider),
    );
    let failing_client = failing_w2s
        .w2s_clients()
        .contractclient()
        .contract_api_client()
        .clone()
        .with_connect_timeout(Duration::from_secs(2));
    let err = failing_client.get_contract(valid_request()).await.expect_err("auth-required Rust W2S read with failing token should fail");
    assert_eq!(err.code(), tonic::Code::Unauthenticated);
    assert_eq!(requests.load(Ordering::SeqCst), 2);
    server.join().expect("join rust W2S HTTP server");
}

async fn next_ws_json<S>(ws: &mut S) -> Value
where
    S: futures::Stream<Item = Result<Message, tokio_tungstenite::tungstenite::Error>> + Unpin,
{
    match tokio::time::timeout(Duration::from_secs(5), ws.next()).await {
        Ok(Some(Ok(Message::Text(text)))) => serde_json::from_str(text.as_ref()).expect("valid JSON frame"),
        other => panic!("unexpected WebSocket frame: {other:?}"),
    }
}

async fn run_w2s_chat_smoke() {
    let listener = TcpListener::bind("127.0.0.1:0").await.expect("bind websocket smoke");
    let addr = listener.local_addr().expect("local addr");

    let server = tokio::spawn(async move {
        let (stream, _) = listener.accept().await.expect("accept websocket");
        let mut ws = accept_async(stream).await.expect("websocket handshake");

        let start = next_ws_json(&mut ws).await;
        let message = next_ws_json(&mut ws).await;
        ws.send(Message::Text(
            r#"{"type":"message","message":{"eventId":"assist_events-1","tenantNamespace":"tenant.alpha","payload":"reply"}}"#.into(),
        ))
        .await
        .expect("send websocket response");
        let complete = next_ws_json(&mut ws).await;
        (start, message, complete)
    });

    let token_provider: contractclient_model::w2sm::internal::grpc_over_rest::w2s_clients::TokenProvider =
        Arc::new(|| Box::pin(async { Ok("rust-token".to_string()) }));
    let w2s = contractclient_model::w2sm::ServiceManager::with_token_provider(
        format!("http://{}", addr),
        Some(token_provider),
    );
    let client = w2s
        .w2s_clients()
        .contractclient()
        .contract_api_client()
        .clone()
        .with_connect_timeout(Duration::from_secs(2));

    let (event_tx, event_rx) = std::sync::mpsc::channel();
    let on_message = Arc::new(move |event: ContractEvent| {
        event_tx.send(event).expect("record assist_events event");
    });
    let handle = client.assist_events(
        move |handle| async move {
            let err = handle
                .send(invalid_event())
                .await
                .expect_err("invalid Rust W2S assist_events send should fail");
            assert_eq!(err.code(), tonic::Code::InvalidArgument);
            handle.send(valid_event()).await.expect("send from on_connect");
        },
        on_message,
    );

    let received = tokio::task::spawn_blocking(move || {
        event_rx
            .recv_timeout(Duration::from_secs(5))
            .expect("assist_events response")
    })
    .await
    .expect("join event wait");
    assert_eq!(received.event_id, "assist_events-1");

    handle.finish().await.expect("finish assist_events stream");
    let (start, message, complete) = server.await.expect("join websocket server");
    assert_eq!(start["type"], "start");
    assert_eq!(start["authorization"], "Bearer rust-token");
    assert_eq!(message["type"], "message");
    assert_eq!(message["authorization"], "Bearer rust-token");
    assert_eq!(message["message"]["eventId"], "event-123");
    assert_eq!(complete["type"], "complete");

    let no_token = contractclient_model::w2sm::ServiceManager::with_token_provider(
        format!("http://{}", addr),
        None,
    );
    let no_token_client = no_token
        .w2s_clients()
        .contractclient()
        .contract_api_client()
        .clone()
        .with_connect_timeout(Duration::from_secs(1));
    let err = no_token_client.get_contract(valid_request()).await.expect_err("auth-required W2S read without token should fail");
    assert_eq!(err.code(), tonic::Code::Unauthenticated);
}
EOF
}

write_rust_c2d_consumer() {
  local main_rs="$1"
  cat > "$main_rs" <<'EOF'
use std::collections::HashMap;
use std::sync::Arc;
use std::time::Duration;

use tokio::net::TcpListener;
use tokio_stream::wrappers::TcpListenerStream;

use contractclient_model::s2sm::internal::grpc::servers::contract::contractclient::contract_daemon::{
    Authorizer as ContractDaemonAuthorizer, ContractDaemonHandler, ValidationWrapper,
};
use contractclient_model::types::contract::contractclient::contract_daemon_client::ContractDaemonClient;
use contractclient_model::types::contract::contractclient::contract_daemon_server::ContractDaemonServer;
use contractclient_model::types::contract::contractclient::{
    ContractMode, ContractRequest, ContractResponse,
};

fn main() {
    let rt = tokio::runtime::Runtime::new().expect("tokio runtime");
    rt.block_on(async {
        let channel = tonic::transport::Endpoint::from_static("http://127.0.0.1:1").connect_lazy();
        let sm = contractclient_model::c2dm::ServiceManager::new(channel);
        let _api = sm.grpc().contractclient().contract_daemon_client();
        let _socket_path = sm.socket_path();

        run_c2d_server_auth_skip_smoke().await;
    });
}

struct DaemonHandler;

#[tonic::async_trait]
impl ContractDaemonHandler for DaemonHandler {
    async fn get_local(
        &self,
        caller: Option<&servicemanager::auth::Caller>,
        req: ContractRequest,
    ) -> Result<ContractResponse, tonic::Status> {
        // C2D carries no token, so the wrapper has no caller identity to pass on.
        assert!(caller.is_none(), "C2D handler must not receive a caller");
        Ok(ContractResponse {
            result_id: format!("c2d-{}", req.item_id),
            accepted: true,
            ..Default::default()
        })
    }

    async fn generate_local_events(
        &self,
        _caller: Option<&servicemanager::auth::Caller>,
        _req: ContractRequest,
    ) -> Result<
        tonic::Response<
            std::pin::Pin<
                Box<
                    dyn tokio_stream::Stream<
                            Item = Result<
                                contractclient_model::types::contract::contractclient::ContractEvent,
                                tonic::Status,
                            >,
                        > + Send,
                >,
            >,
        >,
        tonic::Status,
    > {
        Err(tonic::Status::unimplemented("not exercised"))
    }
}

/// DenyAllAuthorizer rejects every authorization decision it is asked to make.
/// Wiring it proves the C2D wrapper never consults the authorizer: if it did,
/// the calls below would fail Unauthenticated instead of reaching the handler.
struct DenyAllAuthorizer;

#[tonic::async_trait]
impl ContractDaemonAuthorizer for DenyAllAuthorizer {
    async fn authorize(
        &self,
        _full_method: &str,
        _authorization: Option<&str>,
    ) -> Result<HashMap<String, String>, tonic::Status> {
        Err(tonic::Status::unauthenticated("deny-all authorizer"))
    }

    async fn check_at_or_below(
        &self,
        _authorization: Option<&str>,
        _target_namespace: &str,
    ) -> Result<HashMap<String, String>, tonic::Status> {
        Err(tonic::Status::unauthenticated("deny-all authorizer"))
    }

    async fn authenticate(
        &self,
        _authorization: Option<&str>,
    ) -> Result<HashMap<String, String>, tonic::Status> {
        Err(tonic::Status::unauthenticated("deny-all authorizer"))
    }
}

fn valid_local_request(item_id: &str) -> ContractRequest {
    ContractRequest {
        tenant_namespace: "tenant-a".to_string(),
        item_id: item_id.to_string(),
        mode: ContractMode::Read as i32,
        ..Default::default()
    }
}

// PC-W-3: the generated C2D server wrapper skips token authentication by design
// (the auth boundary is the socket permission, not a bearer token) but still runs
// the full validation pipeline — see docs/API.md "C2D server auth-skip scope".
// Rust is the only target that hosts a generated C2D server wrapper. This proves
// both halves live over the wire against a real generated ValidationWrapper.
async fn run_c2d_server_auth_skip_smoke() {
    let wrapper = ValidationWrapper::new(Arc::new(DaemonHandler));
    wrapper
        .set_authorize_func(Some(Arc::new(DenyAllAuthorizer)))
        .await;
    // Guard the negative: the deny-all authorizer really is installed, so the
    // successful calls below cannot be explained by an unwired authorizer slot.
    assert!(wrapper.has_authorizer().await, "deny-all authorizer must be wired");

    let listener = TcpListener::bind("127.0.0.1:0")
        .await
        .expect("bind rust c2d server");
    let addr = listener.local_addr().expect("rust c2d server addr");
    let incoming = TcpListenerStream::new(listener);
    let server = tokio::spawn(async move {
        tonic::transport::Server::builder()
            .add_service(ContractDaemonServer::new(wrapper))
            .serve_with_incoming(incoming)
            .await
            .expect("serve rust c2d");
    });

    let channel = tonic::transport::Endpoint::from_shared(format!("http://{}", addr))
        .expect("rust c2d endpoint")
        .connect_timeout(Duration::from_secs(2))
        .connect()
        .await
        .expect("connect rust c2d");
    let mut client = ContractDaemonClient::new(channel);

    // (a) No authorization metadata at all, and a deny-all authorizer wired:
    // the call must still reach the handler. Auth is genuinely bypassed.
    let response = client
        .get_local(valid_local_request("item-1"))
        .await
        .expect("C2D call without a token must succeed")
        .into_inner();
    assert_eq!(response.result_id, "c2d-item-1");
    assert!(response.accepted);

    // An invalid bearer token is likewise not consulted — no token lookup runs.
    let mut request = tonic::Request::new(valid_local_request("item-2"));
    request.metadata_mut().insert(
        "authorization",
        "Bearer not-a-real-token".parse().expect("metadata value"),
    );
    let response = client
        .get_local(request)
        .await
        .expect("C2D call with an invalid token must succeed")
        .into_inner();
    assert_eq!(response.result_id, "c2d-item-2");

    // (b) Validation still runs: item_id carries assert_min_size = 3, so a
    // two-character item_id must be rejected before the handler is reached.
    let err = client
        .get_local(valid_local_request("ab"))
        .await
        .expect_err("C2D request violating assert_min_size must be rejected");
    assert_eq!(err.code(), tonic::Code::InvalidArgument);
    assert!(
        err.message().contains("validation failed"),
        "C2D validation rejection message = {}",
        err.message()
    );

    server.abort();
}
EOF
}

compile_rust_handwritten_consumer() {
  local output="$1"
  local case_name="$2"
  local log_dir
  log_dir="$(dirname "$output")"

  local consumer_dir="$log_dir/rust-consumer"
  rm -rf "$consumer_dir"
  mkdir -p "$consumer_dir/src"
  write_rust_consumer_cargo "$consumer_dir/Cargo.toml" "$output"

  case "$case_name" in
    c2s)
      write_rust_c2s_consumer "$consumer_dir/src/main.rs"
      ;;
    w2s)
      write_rust_w2s_consumer "$consumer_dir/src/main.rs"
      ;;
    both)
      write_rust_both_consumer "$consumer_dir/src/main.rs"
      ;;
    c2d)
      write_rust_c2d_consumer "$consumer_dir/src/main.rs"
      ;;
    *)
      return
      ;;
  esac

  (cd "$consumer_dir" && CARGO_TARGET_DIR="$CROSS_TEST_CARGO_TARGET" cargo check) >"$log_dir/rust-consumer-check.log" 2>&1 || {
    cat "$log_dir/rust-consumer-check.log" >&2
    return 1
  }
  assert_clean_log "$log_dir/rust-consumer-check.log"

  (cd "$consumer_dir" && CARGO_TARGET_DIR="$CROSS_TEST_CARGO_TARGET" cargo run --quiet) >"$log_dir/rust-consumer-run.log" 2>&1 || {
    cat "$log_dir/rust-consumer-run.log" >&2
    return 1
  }
  assert_clean_log "$log_dir/rust-consumer-run.log"
}

python_runner() {
  if [[ -n "$PYTHON_BIN" ]]; then
    printf '%s\n' "$PYTHON_BIN"
  else
    printf '%s\n' python3
  fi
}

python_has_grpc_tools() {
  local python="$1"
  "$python" - <<'PY' >/dev/null 2>&1
import grpc_tools.protoc
PY
}

prepend_pythonpath() {
  local dir="$1"
  case ":${PYTHONPATH:-}:" in
    *":$dir:"*) ;;
    *) export PYTHONPATH="$dir${PYTHONPATH:+:$PYTHONPATH}" ;;
  esac
}

ensure_python_codegen_env() {
  local python
  python="$(python_runner)"
  if python_has_grpc_tools "$python"; then
    PYTHON_BIN="$python"
    return
  fi

  local log_dir="$WORK_ROOT/.python-codegen"
  local deps_dir="$WORK_ROOT/.python-codegen-deps"
  mkdir -p "$log_dir"
  mkdir -p "$deps_dir"
  if ! env PYTHONPATH="$deps_dir${PYTHONPATH:+:$PYTHONPATH}" "$python" - <<'PY' >/dev/null 2>&1
import grpc_tools.protoc
PY
  then
    "$python" -m pip install -q --disable-pip-version-check --upgrade --target "$deps_dir" grpcio-tools >"$log_dir/python-codegen-install.log" 2>&1 || {
      cat "$log_dir/python-codegen-install.log" >&2
      return 1
    }
  fi
  prepend_pythonpath "$deps_dir"
  PYTHON_BIN="$python"
  if ! python_has_grpc_tools "$PYTHON_BIN"; then
    die "Python codegen interpreter lacks grpc_tools.protoc: $PYTHON_BIN"
  fi
}

ensure_python_consumer_env() {
  local output="$1"
  local log_dir="$2"
  local python
  local deps_dir="$log_dir/python-consumer-deps"

  python="$(python_runner)"
  mkdir -p "$deps_dir"
  "$python" -m pip install -q --disable-pip-version-check --upgrade --target "$deps_dir" "$output" >"$log_dir/python-consumer-install.log" 2>&1 || {
    cat "$log_dir/python-consumer-install.log" >&2
    return 1
  }
  printf '%s\n' "$python"
}

write_python_c2s_consumer() {
  local script="$1"
  cat > "$script" <<'PY'
from contractclient_model.c2sm.service_manager import ServiceManager
from contractclient_model.types.contract.contractclient import contract_pb2, helpers


class FakeChannel:
    pass


req = contract_pb2.ContractRequest(
    tenant_namespace="tenant.alpha",
    item_id="item-123",
    mode=contract_pb2.CONTRACT_MODE_READ,
)
helpers.validate_contract_request(req)
sm = ServiceManager(FakeChannel(), token_provider=lambda: "token")
assert sm.conn is not None
assert sm.grpc_clients.contractclient.contract_api_client is not None
PY
}

write_python_w2s_consumer() {
  local script="$1"
  cat > "$script" <<'PY'
import asyncio

from contractclient_model.types.contract.contractclient import contract_pb2, helpers
from contractclient_model.w2sm.service_manager import ServiceManager


async def main() -> None:
    req = contract_pb2.ContractRequest(
        tenant_namespace="tenant.alpha",
        item_id="item-123",
        mode=contract_pb2.CONTRACT_MODE_READ,
    )
    helpers.validate_contract_request(req)
    sm = ServiceManager.with_token_provider("https://gateway.example", lambda: "token")
    assert sm.base_url == "https://gateway.example"
    assert sm.w2s_clients.contractclient.contract_api_client is not None
    await sm.close()


asyncio.run(main())
PY
}

write_python_both_consumer() {
  local script="$1"
  cat > "$script" <<'PY'
# Client-only combo: both C2S and W2S client surfaces coexist in one package.
# (The previous body hosted python s2sm servers for runtime smokes — dead under
# the full-client taxonomy: python emits no service runtime. The cross-language
# runtime smoke now lives in cross-test/runtime-smoke: a go fixture server
# hosts the contract and this package's generated clients drive it over the
# wire.)
import asyncio

from contractclient_model.c2sm.service_manager import ServiceManager as C2SServiceManager
from contractclient_model.types.contract.contractclient import contract_pb2, helpers
from contractclient_model.w2sm.service_manager import ServiceManager as W2SServiceManager


class FakeChannel:
    pass


async def main() -> None:
    req = contract_pb2.ContractRequest(
        tenant_namespace="tenant.alpha",
        item_id="item-123",
        mode=contract_pb2.CONTRACT_MODE_READ,
    )
    helpers.validate_contract_request(req)

    c2s = C2SServiceManager(FakeChannel(), token_provider=lambda: "token")
    assert c2s.conn is not None
    assert c2s.grpc_clients.contractclient.contract_api_client is not None

    w2s = W2SServiceManager.with_token_provider("https://gateway.example", lambda: "token")
    assert w2s.base_url == "https://gateway.example"
    assert w2s.w2s_clients.contractclient.contract_api_client is not None
    await w2s.close()


asyncio.run(main())
PY
}

write_python_c2d_consumer() {
  local script="$1"
  cat > "$script" <<'PY'
import asyncio

from contractclient_model.c2dm.service_manager import ServiceManager
from contractclient_model.types.contract.contractclient import contract_pb2, helpers


async def main() -> None:
    req = contract_pb2.ContractRequest(
        tenant_namespace="tenant.alpha",
        item_id="item-123",
        mode=contract_pb2.CONTRACT_MODE_READ,
    )
    helpers.validate_contract_request(req)
    sm = ServiceManager("/tmp/tacplus-consumer-contract.sock")
    assert sm.socket_path == "/tmp/tacplus-consumer-contract.sock"
    assert sm.grpc_clients.contractclient.contract_daemon_client is not None
    await sm.close()


asyncio.run(main())
PY
}

compile_python_handwritten_consumer() {
  local output="$1"
  local case_name="$2"
  local log_dir
  log_dir="$(dirname "$output")"

  local consumer_dir="$log_dir/python-consumer"
  rm -rf "$consumer_dir"
  mkdir -p "$consumer_dir"

  case "$case_name" in
    c2s)
      write_python_c2s_consumer "$consumer_dir/consumer.py"
      ;;
    w2s)
      write_python_w2s_consumer "$consumer_dir/consumer.py"
      ;;
    both)
      write_python_both_consumer "$consumer_dir/consumer.py"
      ;;
    c2d)
      write_python_c2d_consumer "$consumer_dir/consumer.py"
      ;;
    *)
      return
      ;;
  esac

  local consumer_python
  local consumer_deps="$log_dir/python-consumer-deps"
  consumer_python="$(ensure_python_consumer_env "$output" "$log_dir")" || return 1
  PYTHONPATH="$consumer_deps:$output${PYTHONPATH:+:$PYTHONPATH}" "$consumer_python" "$consumer_dir/consumer.py" >"$log_dir/python-consumer.log" 2>&1 || {
    cat "$log_dir/python-consumer.log" >&2
    return 1
  }
  assert_clean_log "$log_dir/python-consumer.log"
}

write_typescript_consumer_tsconfig() {
  local tsconfig="$1"
  cat > "$tsconfig" <<'EOF'
{
  "compilerOptions": {
    "target": "ES2022",
    "module": "ES2022",
    "lib": ["ES2022", "DOM", "DOM.Iterable"],
    "strict": true,
    "moduleResolution": "bundler",
    "esModuleInterop": true,
    "skipLibCheck": true,
    "allowSyntheticDefaultImports": true,
    "types": ["node"],
    "noEmit": true
  },
  "include": ["consumer.ts"]
}
EOF
}

write_typescript_c2s_consumer() {
  local script="$1"
  cat > "$script" <<'EOF'
import * as grpc from '@grpc/grpc-js';
import { create } from '@bufbuild/protobuf';

import { ServiceManager } from '../model/c2sm/index.js';
import {
  ContractMode,
  ContractRequestSchema,
  validateContractRequest,
} from '../model/types/contract/contract/index.js';

const req = create(ContractRequestSchema, {
  tenantNamespace: 'tenant.alpha',
  itemId: 'item-123',
  mode: ContractMode.READ,
});
validateContractRequest(req);

const sm = new ServiceManager('127.0.0.1:443', grpc.credentials.createInsecure(), async () => 'token');
const client = sm.grpc().contractclient().contract_api_client();
void client;
sm.close();
EOF
}

write_typescript_w2s_consumer() {
  local script="$1"
  cat > "$script" <<'EOF'
import { create } from '@bufbuild/protobuf';

import { ServiceManager } from '../model/w2sm/index.js';
import {
  ContractMode,
  ContractRequestSchema,
  validateContractRequest,
} from '../model/types/contract/contract/index.js';

const req = create(ContractRequestSchema, {
  tenantNamespace: 'tenant.alpha',
  itemId: 'item-123',
  mode: ContractMode.READ,
});
validateContractRequest(req);

const sm = new ServiceManager({ gatewayUrl: 'https://gateway.example' }, async () => 'token');
const transport = sm.transport();
const viaW2S = sm.w2sClients().contractclient().contract_api_client();
const viaGrpcAlias = sm.grpc().contractclient().contract_api_client();
void transport;
void viaW2S;
void viaGrpcAlias;
EOF
}

write_typescript_both_consumer() {
  local script="$1"
  cat > "$script" <<'EOF'
// Client-only combo: both C2S and W2S client surfaces coexist in one package.
// (The previous body imported s2sm server modules — dead under the full-client
// taxonomy: typescript emits no service runtime. The cross-language runtime
// smoke now lives in cross-test/runtime-smoke: a go fixture server hosts the
// contract and this package's generated clients drive it over the wire.)
import * as grpc from '@grpc/grpc-js';
import { create } from '@bufbuild/protobuf';

import { ServiceManager as C2SServiceManager } from '../model/c2sm/index.js';
import { ServiceManager as W2SServiceManager } from '../model/w2sm/index.js';
import {
  ContractMode,
  ContractRequestSchema,
  validateContractRequest,
} from '../model/types/contract/contract/index.js';

const req = create(ContractRequestSchema, {
  tenantNamespace: 'tenant.alpha',
  itemId: 'item-123',
  mode: ContractMode.READ,
});
validateContractRequest(req);

const c2s = new C2SServiceManager('127.0.0.1:443', grpc.credentials.createInsecure(), async () => 'token');
const viaC2S = c2s.grpc().contractclient().contract_api_client();
void viaC2S;
c2s.close();

const w2s = new W2SServiceManager({ gatewayUrl: 'https://gateway.example' }, async () => 'token');
const viaW2S = w2s.w2sClients().contractclient().contract_api_client();
const viaGrpcAlias = w2s.grpc().contractclient().contract_api_client();
void viaW2S;
void viaGrpcAlias;
EOF
}

write_typescript_c2d_consumer() {
  local script="$1"
  cat > "$script" <<'EOF'
import * as grpc from '@grpc/grpc-js';
import { create } from '@bufbuild/protobuf';

import { ServiceManager } from '../model/c2dm/index.js';
import {
  ContractMode,
  ContractRequestSchema,
  validateContractRequest,
} from '../model/types/contract/contract/index.js';

const req = create(ContractRequestSchema, {
  tenantNamespace: 'tenant.alpha',
  itemId: 'item-123',
  mode: ContractMode.READ,
});
validateContractRequest(req);

const sm = new ServiceManager('/tmp/tacplus-consumer-contract.sock', grpc.credentials.createInsecure());
const client = sm.grpc().contractclient().contract_daemon_client();
if (sm.socketPath() !== '/tmp/tacplus-consumer-contract.sock') {
  throw new Error('unexpected socket path');
}
void client;
sm.close();
EOF
}

# Client-only runtime smokes: execute the generated CLIENT surfaces with node
# against the model's compiled dist/ output (construction + wiring level; no
# live server). Mirrors the executed python consumers so TypeScript coverage is
# run-level, not compile-only.
write_typescript_client_runtime_smoke() {
  local script="$1"
  local case_name="$2"
  {
    cat <<'EOF'
import * as grpc from '@grpc/grpc-js';
import { create } from '@bufbuild/protobuf';
import {
  ContractMode,
  ContractRequestSchema,
  validateContractRequest,
} from '../model/dist/types/contract/contract/index.js';

const req = create(ContractRequestSchema, {
  tenantNamespace: 'tenant.alpha',
  itemId: 'item-123',
  mode: ContractMode.READ,
});
validateContractRequest(req);
EOF
    if [[ "$case_name" == c2s || "$case_name" == both ]]; then
      cat <<'EOF'
const { ServiceManager: C2SServiceManager } = await import('../model/dist/c2sm/index.js');
const c2s = new C2SServiceManager('127.0.0.1:443', grpc.credentials.createInsecure(), async () => 'token');
const c2sClient = c2s.grpc().contractclient().contract_api_client();
if (!c2sClient) throw new Error('missing c2s contract_api_client');
c2s.close();
EOF
    fi
    if [[ "$case_name" == w2s || "$case_name" == both ]]; then
      cat <<'EOF'
const { ServiceManager: W2SServiceManager } = await import('../model/dist/w2sm/index.js');
const w2s = new W2SServiceManager({ gatewayUrl: 'https://gateway.example' }, async () => 'token');
const viaW2S = w2s.w2sClients().contractclient().contract_api_client();
const viaAlias = w2s.grpc().contractclient().contract_api_client();
if (!viaW2S || !viaAlias) throw new Error('missing w2s contract_api_client');
EOF
    fi
    if [[ "$case_name" == c2d ]]; then
      cat <<'EOF'
const { ServiceManager: C2DServiceManager } = await import('../model/dist/c2dm/index.js');
const c2d = new C2DServiceManager('/tmp/tacplus-consumer-contract.sock', grpc.credentials.createInsecure());
const c2dClient = c2d.grpc().contractclient().contract_daemon_client();
if (!c2dClient) throw new Error('missing contract_daemon_client');
if (c2d.socketPath() !== '/tmp/tacplus-consumer-contract.sock') throw new Error('unexpected socket path');
c2d.close();
EOF
    fi
    echo "console.log('typescript $case_name client runtime ok');"
  } > "$script"
}

compile_typescript_handwritten_consumer() {
  local output="$1"
  local case_name="$2"
  local log_dir
  log_dir="$(dirname "$output")"

  local consumer_dir="$log_dir/typescript-consumer"
  rm -rf "$consumer_dir"
  mkdir -p "$consumer_dir"
  write_typescript_consumer_tsconfig "$consumer_dir/tsconfig.json"

  case "$case_name" in
    c2s)
      write_typescript_c2s_consumer "$consumer_dir/consumer.ts"
      ;;
    w2s)
      write_typescript_w2s_consumer "$consumer_dir/consumer.ts"
      ;;
    both)
      write_typescript_both_consumer "$consumer_dir/consumer.ts"
      ;;
    c2d)
      write_typescript_c2d_consumer "$consumer_dir/consumer.ts"
      ;;
    *)
      return
      ;;
  esac

  ln -s "$output/node_modules" "$consumer_dir/node_modules"
  (cd "$consumer_dir" && "$output/node_modules/.bin/tsc" -p tsconfig.json) >"$log_dir/typescript-consumer.log" 2>&1 || {
    cat "$log_dir/typescript-consumer.log" >&2
    return 1
  }
  assert_clean_log "$log_dir/typescript-consumer.log"

  # Execute the client surfaces with node (construction + wiring level; the
  # client-vs-live-server runtime checks belong to a go-fixture-server harness).
  write_typescript_client_runtime_smoke "$consumer_dir/runtime.mjs" "$case_name"
  (cd "$consumer_dir" && node runtime.mjs) >"$log_dir/typescript-consumer-runtime.log" 2>&1 || {
    cat "$log_dir/typescript-consumer-runtime.log" >&2
    return 1
  }
  assert_clean_log "$log_dir/typescript-consumer-runtime.log"
}

write_java_compile_pom() {
  local pom="$1"
  local source_dir="$2"
  cat > "$pom" <<EOF
<project xmlns="http://maven.apache.org/POM/4.0.0"
         xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"
         xsi:schemaLocation="http://maven.apache.org/POM/4.0.0 https://maven.apache.org/xsd/maven-4.0.0.xsd">
  <modelVersion>4.0.0</modelVersion>
  <groupId>com.gitlab.example.tacplus.consumer</groupId>
  <artifactId>contract-java-compile</artifactId>
  <version>0.0.0</version>

  <properties>
    <maven.compiler.release>17</maven.compiler.release>
    <project.build.sourceEncoding>UTF-8</project.build.sourceEncoding>
  </properties>

  <build>
    <sourceDirectory>${source_dir}/src/main/java</sourceDirectory>
    <plugins>
      <plugin>
        <groupId>org.apache.maven.plugins</groupId>
        <artifactId>maven-compiler-plugin</artifactId>
        <version>3.15.0</version>
        <configuration>
          <showWarnings>true</showWarnings>
          <failOnWarning>true</failOnWarning>
          <excludes>
            <exclude>**/Proof.java</exclude>
          </excludes>
        </configuration>
      </plugin>
    </plugins>
  </build>

  <dependencies>
    <dependency>
      <groupId>com.google.protobuf</groupId>
      <artifactId>protobuf-java</artifactId>
      <version>4.36.0</version>
    </dependency>
    <dependency>
      <groupId>com.google.protobuf</groupId>
      <artifactId>protobuf-java-util</artifactId>
      <version>4.36.0</version>
    </dependency>
    <dependency>
      <groupId>io.grpc</groupId>
      <artifactId>grpc-netty-shaded</artifactId>
      <version>1.79.0</version>
    </dependency>
    <dependency>
      <groupId>io.grpc</groupId>
      <artifactId>grpc-protobuf</artifactId>
      <version>1.79.0</version>
    </dependency>
    <dependency>
      <groupId>io.grpc</groupId>
      <artifactId>grpc-protobuf-lite</artifactId>
      <version>1.79.0</version>
    </dependency>
    <dependency>
      <groupId>io.grpc</groupId>
      <artifactId>grpc-stub</artifactId>
      <version>1.79.0</version>
    </dependency>
    <dependency>
      <groupId>io.grpc</groupId>
      <artifactId>grpc-services</artifactId>
      <version>1.79.0</version>
    </dependency>
    <dependency>
      <groupId>io.grpc</groupId>
      <artifactId>grpc-util</artifactId>
      <version>1.79.0</version>
    </dependency>
    <dependency>
      <groupId>io.grpc</groupId>
      <artifactId>grpc-inprocess</artifactId>
      <version>1.79.0</version>
    </dependency>
    <dependency>
      <groupId>io.perfmark</groupId>
      <artifactId>perfmark-api</artifactId>
      <version>0.27.0</version>
    </dependency>
    <dependency>
      <groupId>io.javalin</groupId>
      <artifactId>javalin</artifactId>
      <version>7.2.3</version>
    </dependency>
    <dependency>
      <groupId>io.jsonwebtoken</groupId>
      <artifactId>jjwt-api</artifactId>
      <version>0.13.0</version>
    </dependency>
    <dependency>
      <groupId>io.jsonwebtoken</groupId>
      <artifactId>jjwt-impl</artifactId>
      <version>0.13.0</version>
    </dependency>
    <dependency>
      <groupId>io.jsonwebtoken</groupId>
      <artifactId>jjwt-gson</artifactId>
      <version>0.13.0</version>
    </dependency>
    <dependency>
      <groupId>com.google.code.gson</groupId>
      <artifactId>gson</artifactId>
      <version>2.13.2</version>
    </dependency>
    <dependency>
      <groupId>com.moandjiezana.toml</groupId>
      <artifactId>toml4j</artifactId>
      <version>0.7.2</version>
    </dependency>
    <dependency>
      <groupId>org.yaml</groupId>
      <artifactId>snakeyaml</artifactId>
      <version>2.5</version>
    </dependency>
    <dependency>
      <groupId>org.bouncycastle</groupId>
      <artifactId>bcpkix-jdk18on</artifactId>
      <version>1.86</version>
    </dependency>
    <dependency>
      <groupId>org.bouncycastle</groupId>
      <artifactId>bcprov-jdk18on</artifactId>
      <version>1.86</version>
    </dependency>
    <dependency>
      <groupId>ch.qos.logback</groupId>
      <artifactId>logback-classic</artifactId>
      <version>1.5.34</version>
    </dependency>
    <dependency>
      <groupId>ch.qos.logback</groupId>
      <artifactId>logback-core</artifactId>
      <version>1.5.34</version>
    </dependency>
    <dependency>
      <groupId>org.apache.commons</groupId>
      <artifactId>commons-compress</artifactId>
      <version>1.28.0</version>
    </dependency>
    <dependency>
      <groupId>javax.annotation</groupId>
      <artifactId>javax.annotation-api</artifactId>
      <version>1.3.2</version>
    </dependency>
    <!-- Generated MCP dispatch (every c2s service with a user rpc) is written
         against the official MCP Java SDK, as cross-test/lib/java-compile.sh
         declares it; without it no Java leg of this fixture compiled. -->
    <dependency>
      <groupId>io.modelcontextprotocol.sdk</groupId>
      <artifactId>mcp</artifactId>
      <version>2.0.1</version>
    </dependency>
  </dependencies>
</project>
EOF
}

java_home_env() {
  if [[ -n "${JAVA_HOME:-}" && -x "$JAVA_HOME/bin/java" ]]; then
    return
  fi
  if [[ -x /usr/libexec/java_home ]]; then
    local resolved=""
    resolved="$(/usr/libexec/java_home -v 21 2>/dev/null || /usr/libexec/java_home -v 17 2>/dev/null || /usr/libexec/java_home 2>/dev/null || true)"
    if [[ -n "$resolved" ]]; then
      printf 'JAVA_HOME=%s\n' "$resolved"
    fi
  fi
}

run_java_generated_self_tests() {
  local output="$1"
  local compile_dir="$2"
  local log_dir="$3"
  local classpath_log="$log_dir/java-generated-selftests-classpath.log"
  local classpath_file="$log_dir/java-generated-selftests.classpath"
  local test_log="$log_dir/java-generated-selftests.log"
  local java_env
  java_env="$(java_home_env)"

  if [[ -n "$java_env" ]]; then
    (cd "$compile_dir" && env "$java_env" mvn -q dependency:build-classpath -Dmdep.outputFile="$classpath_file") >"$classpath_log" 2>&1 || {
      cat "$classpath_log" >&2
      return 1
    }
  else
    (cd "$compile_dir" && mvn -q dependency:build-classpath -Dmdep.outputFile="$classpath_file") >"$classpath_log" 2>&1 || {
      cat "$classpath_log" >&2
      return 1
    }
  fi
  assert_clean_log "$classpath_log"

  local java_home=""
  if [[ -n "$java_env" ]]; then
    java_home="${java_env#JAVA_HOME=}"
  elif [[ -n "${JAVA_HOME:-}" ]]; then
    java_home="$JAVA_HOME"
  fi
  local java_cmd="java"
  if [[ -n "$java_home" && -x "$java_home/bin/java" ]]; then
    java_cmd="$java_home/bin/java"
  fi

  local deps_cp=""
  [[ -f "$classpath_file" ]] && deps_cp="$(cat "$classpath_file")"
  local cp="$compile_dir/target/classes"
  [[ -n "$deps_cp" ]] && cp="$cp:$deps_cp"

  : >"$test_log"
  local pattern source_file package_name class_name
  for pattern in '*/chassis/spiffe/SpiffeVectorsTest.java' '*/chassis/x509/ImmutableCertChainTest.java' '*/chassis/grpcserver/AuthorizerBehaviorTest.java' '*/chassis/collection/CollectionRuntimeTest.java'; do
    source_file="$(find "$output/src/main/java" -type f -path "$pattern" -print -quit)"
    [[ -n "$source_file" ]] || die "missing generated Java self-test source: $pattern"
    package_name="$(awk '/^package /{gsub(";","",$2); print $2; exit}' "$source_file")"
    [[ -n "$package_name" ]] || die "could not determine Java package for $source_file"
    class_name="$package_name.$(basename "$source_file" .java)"
    SPIFFE_VECTOR_FIXTURE="$ROOT_DIR/cross-test/fixtures/spiffe_vectors.json" "$java_cmd" --sun-misc-unsafe-memory-access=allow -cp "$cp" "$class_name" >>"$test_log" 2>&1 || {
      cat "$test_log" >&2
      return 1
    }
  done
  # The java sys walk copies ALL lib templates unconditionally, so
  # ServiceConnectionTest is emitted for every java build (only GO gates
  # serviceconnection emission). Run its fail-closed/fail-fast self-test
  # (NEW-18/NEW-21). The `if` is defensive; in practice it is always present.
  local sc_test sc_class
  sc_test="$(find "$output/src/main/java" -type f -path '*/chassis/serviceconnection/ServiceConnectionTest.java' -print -quit)"
  if [[ -n "$sc_test" ]]; then
    sc_class="$(awk '/^package /{gsub(";","",$2); print $2; exit}' "$sc_test").$(basename "$sc_test" .java)"
    "$java_cmd" --sun-misc-unsafe-memory-access=allow -cp "$cp" "$sc_class" >>"$test_log" 2>&1 || {
      cat "$test_log" >&2
      return 1
    }
  fi
  assert_clean_log "$test_log"
}

run_rust_spiffe_self_tests() {
  local output="$1"
  local log_dir="$2"
  [[ -f "$output/chassis/spiffe/Cargo.toml" ]] || die "missing generated Rust spiffe crate"

  (cd "$output" && SPIFFE_VECTOR_FIXTURE="$ROOT_DIR/cross-test/fixtures/spiffe_vectors.json" cargo test -p spiffe) >"$log_dir/rust-spiffe-test.log" 2>&1 || {
      cat "$log_dir/rust-spiffe-test.log" >&2
      return 1
    }
  assert_clean_log "$log_dir/rust-spiffe-test.log"

  # PC-W-1: authorizer behavioral tests — real Ed25519-minted JWTs against the
  # REAL Rust Authorizer verify path.
  [[ -f "$output/chassis/grpcserver/Cargo.toml" ]] || die "missing generated Rust grpcserver crate"
  (cd "$output" && cargo test -p grpcserver) >"$log_dir/rust-grpcserver-test.log" 2>&1 || {
      cat "$log_dir/rust-grpcserver-test.log" >&2
      return 1
    }
  assert_clean_log "$log_dir/rust-grpcserver-test.log"

  # PC-C-2: page-token codec byte-identity vectors (shared with go/java).
  [[ -f "$output/chassis/collection/Cargo.toml" ]] || die "missing generated Rust collection crate"
  (cd "$output" && cargo test -p collection) >"$log_dir/rust-collection-test.log" 2>&1 || {
      cat "$log_dir/rust-collection-test.log" >&2
      return 1
    }
  assert_clean_log "$log_dir/rust-collection-test.log"
}

write_java_c2s_consumer() {
  local file="$1"
  cat > "$file" <<'EOF'
package consumer;

import com.gitlab.example.tacplus.consumer.contractclient.model.c2sm.ServiceManager;
import com.gitlab.example.tacplus.consumer.contractclient.model.types.contract.contractclient.Contractclient;
import com.gitlab.example.tacplus.consumer.contractclient.model.types.contract.contractclient.Helpers;
import io.grpc.ManagedChannel;
import io.grpc.ManagedChannelBuilder;

public final class Consumer {
  public static void main(String[] args) throws Exception {
    Contractclient.ContractRequest req = Contractclient.ContractRequest.newBuilder()
        .setTenantNamespace("tenant.alpha")
        .setItemId("item-123")
        .setMode(Contractclient.ContractMode.CONTRACT_MODE_READ)
        .build();
    Helpers.validateContractRequest(req);

    ManagedChannel channel = ManagedChannelBuilder.forTarget("127.0.0.1:443").usePlaintext().build();
    try {
      ServiceManager sm = new ServiceManager(channel, () -> "token");
      if (sm.grpc().contract().contractApi() == null) {
        throw new IllegalStateException("missing ContractApi client");
      }
    } finally {
      channel.shutdownNow();
    }
  }
}
EOF
}

write_java_w2s_consumer() {
  local file="$1"
  cat > "$file" <<'EOF'
package consumer;

import com.gitlab.example.tacplus.consumer.contractclient.model.w2sm.ServiceManager;
import com.gitlab.example.tacplus.consumer.contractclient.model.types.contract.contractclient.Contractclient;
import com.gitlab.example.tacplus.consumer.contractclient.model.types.contract.contractclient.Helpers;

public final class Consumer {
  public static void main(String[] args) throws Exception {
    Contractclient.ContractRequest req = Contractclient.ContractRequest.newBuilder()
        .setTenantNamespace("tenant.alpha")
        .setItemId("item-123")
        .setMode(Contractclient.ContractMode.CONTRACT_MODE_READ)
        .build();
    Helpers.validateContractRequest(req);

    ServiceManager sm = ServiceManager.withTokenProvider("https://gateway.example", () -> "token");
    if (sm.w2sClients().contractclient().contractApi() == null || sm.grpc().contractclient().contractApi() == null) {
      throw new IllegalStateException("missing ContractApi W2S client");
    }
  }
}
EOF
}

write_java_both_consumer() {
  local file="$1"
  cat > "$file" <<'EOF'
package consumer;

import com.gitlab.example.tacplus.consumer.contractclient.model.chassis.grpcserver.Authorizer;
import com.gitlab.example.tacplus.consumer.contractclient.model.chassis.am.AccountingManager;
import com.gitlab.example.tacplus.consumer.contractclient.model.chassis.collection.CollectionRuntime;
import com.gitlab.example.tacplus.consumer.contractclient.model.chassis.servicemanager.Caller;
import com.gitlab.example.tacplus.consumer.contractclient.model.chassis.token.TokenWrapper;
import com.gitlab.example.tacplus.consumer.contractclient.model.s2sm.internal.grpc.servers.contract.contractclient.ContractApi;
import com.gitlab.example.tacplus.consumer.contractclient.model.s2sm.internal.grpc.servers.services.contractclient.ContractAuditApi;
import com.gitlab.example.tacplus.consumer.contractclient.model.s2sm.subscribe.ContractfixturesFixtureStateEventsSubscriber;
import com.gitlab.example.tacplus.consumer.contractclient.model.types.contract.contractclient.Contractclient;
import com.gitlab.example.tacplus.consumer.contractclient.model.types.contract.contractclient.Helpers;
import com.gitlab.example.tacplus.consumer.contractclient.model.types.generated.services.Services;
import com.gitlab.example.tacplus.consumer.contractclient.model.types.sys.accounting.Accounting;
import com.gitlab.example.tacplus.consumer.contractclient.model.types.sys.errors.ProtoException;
import com.gitlab.example.tacplus.consumer.contractclient.model.types.sys.trace.Trace;
import com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.ContractAuditApiGrpc;
import com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractfixtures.Contractfixtures;
import com.google.protobuf.ByteString;
import com.google.protobuf.Timestamp;
import com.google.gson.JsonObject;
import com.google.gson.JsonParser;
import io.grpc.CallCredentials;
import io.grpc.CallOptions;
import io.grpc.Channel;
import io.grpc.ClientCall;
import io.grpc.ManagedChannel;
import io.grpc.ManagedChannelBuilder;
import io.grpc.Metadata;
import io.grpc.MethodDescriptor;
import io.grpc.Server;
import io.grpc.Status;
import io.grpc.inprocess.InProcessChannelBuilder;
import io.grpc.inprocess.InProcessServerBuilder;
import io.grpc.stub.StreamObserver;
import java.io.ByteArrayOutputStream;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.InetAddress;
import java.net.ServerSocket;
import java.net.Socket;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.time.Duration;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Base64;
import java.util.Collections;
import java.util.EnumMap;
import java.util.HashMap;
import java.util.Iterator;
import java.util.List;
import java.util.Map;
import java.util.concurrent.BlockingQueue;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.Executor;
import java.util.concurrent.LinkedBlockingQueue;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicReference;

public final class Consumer {
  private static Contractclient.ContractRequest validRequest() {
    return Contractclient.ContractRequest.newBuilder()
        .setTenantNamespace("tenant.alpha")
        .setItemId("item-123")
        .setMode(Contractclient.ContractMode.CONTRACT_MODE_READ)
        .build();
  }

  private static Contractclient.ContractRequest invalidRequest() {
    return Contractclient.ContractRequest.newBuilder()
        .setTenantNamespace("tenant.alpha")
        .setItemId("no")
        .setMode(Contractclient.ContractMode.CONTRACT_MODE_READ)
        .build();
  }

  private static Contractclient.ContractRequest forbiddenRequest() {
    return Contractclient.ContractRequest.newBuilder()
        .setTenantNamespace("tenant.beta")
        .setItemId("item-123")
        .setMode(Contractclient.ContractMode.CONTRACT_MODE_READ)
        .build();
  }

  private static Contractclient.ContractRequest badResponseRequest() {
    return Contractclient.ContractRequest.newBuilder()
        .setTenantNamespace("tenant.alpha")
        .setItemId("bad-response")
        .setMode(Contractclient.ContractMode.CONTRACT_MODE_READ)
        .build();
  }

  private static Contractclient.ContractRequest badEventRequest() {
    return Contractclient.ContractRequest.newBuilder()
        .setTenantNamespace("tenant.alpha")
        .setItemId("bad-event")
        .setMode(Contractclient.ContractMode.CONTRACT_MODE_READ)
        .build();
  }

  private static Contractclient.ContractWatchRequest validWatchRequest() {
    return Contractclient.ContractWatchRequest.newBuilder()
        .setTenantNamespace("tenant.alpha")
        .setItemId("item-123")
        .setMode(Contractclient.ContractMode.CONTRACT_MODE_READ)
        .setFromRevision(0)
        .build();
  }

  private static Contractclient.ContractWatchRequest invalidWatchRequest() {
    return Contractclient.ContractWatchRequest.newBuilder()
        .setTenantNamespace("tenant.alpha")
        .setItemId("no")
        .setMode(Contractclient.ContractMode.CONTRACT_MODE_READ)
        .build();
  }

  private static Contractclient.ContractWatchRequest badWatchRequest() {
    return Contractclient.ContractWatchRequest.newBuilder()
        .setTenantNamespace("tenant.alpha")
        .setItemId("bad-event")
        .setMode(Contractclient.ContractMode.CONTRACT_MODE_READ)
        .build();
  }

  private static Contractclient.ContractResponse validResponse() {
    return Contractclient.ContractResponse.newBuilder()
        .setResultId("result-123")
        .setAccepted(true)
        .build();
  }

  private static Contractclient.ContractEvent validEvent() {
    return Contractclient.ContractEvent.newBuilder()
        .setEventId("java-send")
        .setTenantNamespace("tenant.alpha")
        .setPayload("payload")
        .build();
  }

  private static Contractclient.ContractEvent invalidEvent() {
    return Contractclient.ContractEvent.newBuilder()
        .setTenantNamespace("tenant.alpha")
        .setPayload("missing-id")
        .build();
  }

  private static Contractclient.ContractWatchEvent watchSnapshot(Contractclient.ContractEvent event) {
    return Contractclient.ContractWatchEvent.newBuilder()
        .setSnapshotItem(event)
        .build();
  }

  private static Contractclient.ContractWatchChange validChange(long revision) {
    return Contractclient.ContractWatchChange.newBuilder()
        .setType(Contractclient.ContractChangeType.CONTRACT_CHANGE_TYPE_MODIFY)
        .setItem(Contractclient.ContractEvent.newBuilder()
            .setEventId("event-" + revision)
            .setTenantNamespace("tenant.alpha")
            .setPayload("ok")
            .build())
        .setRevision(revision)
        .build();
  }

  private static CollectionRuntime.WatchSource<Contractclient.ContractEvent, Contractclient.ContractWatchChange> watchSource(
      Contractclient.ContractEvent event) {
    return new CollectionRuntime.WatchSource<>(
        "tenant.alpha",
        Collections.singletonList(event).iterator(),
        1L,
        () -> Collections.<Contractclient.ContractWatchChange>emptyList().iterator());
  }

  private static final class ApiHandler implements ContractApi.ContractApiHandler {
    private final AtomicInteger readCalls = new AtomicInteger();
    private final AtomicInteger publicReadCalls = new AtomicInteger();
    private final AtomicInteger listEventsCalls = new AtomicInteger();
    private final AtomicInteger watchCalls = new AtomicInteger();

    int readCalls() {
      return readCalls.get();
    }

    int publicReadCalls() {
      return publicReadCalls.get();
    }

    int listEventsCalls() {
      return listEventsCalls.get();
    }

    int watchCalls() {
      return watchCalls.get();
    }

    @Override
    public Contractclient.ContractResponse getContract(Caller caller, Contractclient.ContractRequest request) throws ProtoException {
      readCalls.incrementAndGet();
      return validResponse();
    }

    @Override
    public Contractclient.ContractResponse getPublicContract(Caller caller, Contractclient.ContractRequest request) throws ProtoException {
      publicReadCalls.incrementAndGet();
      if ("bad-response".equals(request.getItemId())) {
        return Contractclient.ContractResponse.newBuilder()
            .setAccepted(true)
            .build();
      }
      return validResponse();
    }

    @Override
    public Iterator<Contractclient.ContractEvent> listEvents(Caller caller, Contractclient.ContractRequest request) throws ProtoException {
      listEventsCalls.incrementAndGet();
      if ("bad-event".equals(request.getItemId())) {
        return Collections.singletonList(invalidEvent()).iterator();
      }
      return Collections.singletonList(Contractclient.ContractEvent.newBuilder()
          .setEventId("event-1")
          .setTenantNamespace("tenant.alpha")
          .setPayload("ok")
          .build()).iterator();
    }

    @Override
    public CollectionRuntime.Page<Contractclient.ContractEvent> listEventsPaged(
        Caller caller,
        Contractclient.ContractPageRequest request,
        CollectionRuntime.PageParams page) throws ProtoException {
      // One full page, then exhausted (nextAfter=null -> empty next_page_token).
      java.util.List<Contractclient.ContractEvent> items = Collections.singletonList(
          Contractclient.ContractEvent.newBuilder()
              .setEventId("event-1")
              .setTenantNamespace("tenant.alpha")
              .setPayload("ok")
              .build());
      return new CollectionRuntime.Page<>(items, null, 1L);
    }

    @Override
    public CollectionRuntime.WatchSource<Contractclient.ContractEvent, Contractclient.ContractWatchChange> watch(
        Caller caller,
        Contractclient.ContractWatchRequest request) throws ProtoException {
      watchCalls.incrementAndGet();
      if ("bad-event".equals(request.getItemId())) {
        return watchSource(invalidEvent());
      }
      return watchSource(Contractclient.ContractEvent.newBuilder()
          .setEventId("event-1")
          .setTenantNamespace("tenant.alpha")
          .setPayload("ok")
          .build());
    }

    @Override
    public StreamObserver<Contractclient.ContractEvent> sendEvents(Caller caller, StreamObserver<Contractclient.ContractResponse> responseObserver) throws ProtoException {
      return new StreamObserver<>() {
        @Override public void onNext(Contractclient.ContractEvent value) {}
        @Override public void onError(Throwable t) {}
        @Override public void onCompleted() {
          responseObserver.onNext(validResponse());
          responseObserver.onCompleted();
        }
      };
    }

    @Override
    public StreamObserver<Contractclient.ContractEvent> assistEvents(Caller caller, StreamObserver<Contractclient.ContractEvent> responseObserver) throws ProtoException {
      return new StreamObserver<>() {
        @Override public void onNext(Contractclient.ContractEvent value) {}
        @Override public void onError(Throwable t) {}
        @Override public void onCompleted() { responseObserver.onCompleted(); }
      };
    }
  }

  private static final class TestAuthorizer extends Authorizer {
    private final AtomicInteger authorizeCalls = new AtomicInteger();
    private volatile boolean rejectAll = true;

    TestAuthorizer() {
      super(sink -> { });
    }

    void setRejectAll(boolean rejectAll) {
      this.rejectAll = rejectAll;
    }

    int authorizeCalls() {
      return authorizeCalls.get();
    }

    @Override
    public TokenWrapper authorize(String fullMethod, Object request) throws ProtoException {
      authorizeCalls.incrementAndGet();
      if (rejectAll) {
        throw ProtoException.newErrUnauthorized(null, "missing authorization header");
      }
      if (!(request instanceof Contractclient.ContractRequest)) {
        throw ProtoException.newErrGeneral(null, "authorize did not receive the request message");
      }
      Contractclient.ContractRequest typed = (Contractclient.ContractRequest) request;
      if (!"tenant.alpha".equals(typed.getTenantNamespace())) {
        throw ProtoException.newErrPermissionDenied(null, "caller namespace is not at-or-above target");
      }
      return TokenWrapper.create();
    }
  }

  private static com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient.AuditRequest validAuditRequest(
      String traceId) {
    return com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient.AuditRequest.newBuilder()
        .setItemId("audit-item")
        .setTraceId(traceId)
        .build();
  }

  private static com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient.AuditRequest failingAuditRequest(
      String traceId) {
    return com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient.AuditRequest.newBuilder()
        .setItemId("fail")
        .setTraceId(traceId)
        .build();
  }

  private static com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient.AuditEvent validAuditEvent() {
    return com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient.AuditEvent.newBuilder()
        .setEventId("audit-event")
        .build();
  }

  private static com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient.AuditEvent failingAuditEvent() {
    return com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient.AuditEvent.newBuilder()
        .setEventId("fail")
        .build();
  }

  private static com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient.AuditResponse validAuditResponse(
      String resultId) {
    return com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient.AuditResponse.newBuilder()
        .setResultId(resultId)
        .build();
  }

  private static final class AuditHandler implements ContractAuditApi.ContractAuditApiHandler {
    @Override
    public com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient.AuditResponse executeAudit(
        Caller caller,
        com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient.AuditRequest request) throws ProtoException {
      if ("fail".equals(request.getItemId())) {
        throw new ProtoException(ProtoException.INTERNAL, "audit unary failed");
      }
      return validAuditResponse("audit-unary");
    }

    @Override
    public void generateAuditEvents(
        Caller caller,
        com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient.AuditRequest request,
        StreamObserver<com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient.AuditEvent> responseObserver)
        throws ProtoException {
      if ("fail".equals(request.getItemId())) {
        throw new ProtoException(ProtoException.INTERNAL, "audit stream failed");
      }
      responseObserver.onNext(
          com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient.AuditEvent.newBuilder()
              .setEventId("audit-stream")
              .build());
      responseObserver.onCompleted();
    }

    @Override
    public StreamObserver<com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient.AuditEvent> sendAuditEvents(
        Caller caller,
        StreamObserver<com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient.AuditResponse> responseObserver)
        throws ProtoException {
      return new StreamObserver<>() {
        private boolean failed;

        @Override
        public void onNext(com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient.AuditEvent value) {
          if ("fail".equals(value.getEventId())) {
            failed = true;
            responseObserver.onError(Status.INTERNAL.withDescription("audit sendEvents failed").asRuntimeException());
          }
        }

        @Override public void onError(Throwable t) {}

        @Override
        public void onCompleted() {
          if (!failed) {
            responseObserver.onNext(validAuditResponse("audit-sendEvents"));
            responseObserver.onCompleted();
          }
        }
      };
    }

    @Override
    public StreamObserver<com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient.AuditEvent> assistAuditEvents(
        Caller caller,
        StreamObserver<com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient.AuditEvent> responseObserver)
        throws ProtoException {
      return new StreamObserver<>() {
        private boolean failed;

        @Override
        public void onNext(com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient.AuditEvent value) {
          if ("fail".equals(value.getEventId())) {
            failed = true;
            responseObserver.onError(Status.INTERNAL.withDescription("audit assistEvents failed").asRuntimeException());
            return;
          }
          responseObserver.onNext(
              com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient.AuditEvent.newBuilder()
                  .setEventId("audit-reply")
                  .build());
        }

        @Override public void onError(Throwable t) {}

        @Override
        public void onCompleted() {
          if (!failed) {
            responseObserver.onCompleted();
          }
        }
      };
    }
  }

  private static final class RecordingObserver<T> implements StreamObserver<T> {
    final List<T> values = Collections.synchronizedList(new ArrayList<>());
    final AtomicReference<Throwable> error = new AtomicReference<>();
    final CountDownLatch done = new CountDownLatch(1);

    @Override public void onNext(T value) { values.add(value); }
    @Override public void onError(Throwable t) { error.set(t); done.countDown(); }
    @Override public void onCompleted() { done.countDown(); }

    void await(String message) throws Exception {
      if (!done.await(5, TimeUnit.SECONDS)) {
        throw new IllegalStateException(message + " timed out");
      }
    }
  }

  private static void expectGrpcStatus(Throwable t, Status.Code code, String message) {
    if (t == null) {
      throw new IllegalStateException(message + ": missing error");
    }
    Status.Code got = Status.fromThrowable(t).getCode();
    if (got != code) {
      throw new IllegalStateException(message + ": expected " + code + ", got " + got, t);
    }
  }

  private static void countJavaAccountingEvents(
      List<Accounting.Event> events,
      Map<Services.ContractclientRPCs, Integer> completed,
      Map<Services.ContractclientRPCs, Integer> errors) {
    for (Accounting.Event event : events) {
      if (event.hasRpcCompleted()) {
        Services.ContractclientRPCs method = event.getRpcCompleted().getMethod().getContractclient();
        completed.merge(method, 1, Integer::sum);
      }
      if (event.hasRpcError()) {
        Services.ContractclientRPCs method = event.getRpcError().getMethod().getContractclient();
        errors.merge(method, 1, Integer::sum);
      }
    }
  }

  private static void assertJavaAuditCounts(
      Map<Services.ContractclientRPCs, Integer> completed,
      Map<Services.ContractclientRPCs, Integer> errors,
      Services.ContractclientRPCs method) {
    int done = completed.getOrDefault(method, 0);
    int failed = errors.getOrDefault(method, 0);
    if (done != 1 || failed != 1) {
      throw new IllegalStateException("Java audit accounting for " + method + " completed=" + done + " errors=" + failed);
    }
  }

  private static void runJavaAuditAccountingTraceSmoke() throws Exception {
    List<Accounting.Event> accountingEvents = Collections.synchronizedList(new ArrayList<>());
    List<Trace.TraceEvent> traceEvents = Collections.synchronizedList(new ArrayList<>());
    ContractAuditApi wrapper = new ContractAuditApi(new AuditHandler(), Authorizer.create());
    wrapper.setEmitter(new AccountingManager.Emitter("contractclient-test", accountingEvents::add));
    wrapper.setTracePublisher(traceEvents::add);

    String serverName = InProcessServerBuilder.generateName();
    Server server = InProcessServerBuilder
        .forName(serverName)
        .directExecutor()
        .addService(wrapper)
        .build()
        .start();
    ManagedChannel channel = InProcessChannelBuilder.forName(serverName).directExecutor().build();
    try {
      ContractAuditApiGrpc.ContractAuditApiBlockingStub blocking =
          ContractAuditApiGrpc.newBlockingStub(channel);
      ContractAuditApiGrpc.ContractAuditApiStub async =
          ContractAuditApiGrpc.newStub(channel);

      com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient.AuditResponse unary =
          blocking.executeAudit(validAuditRequest("trace-success"));
      if (!"audit-unary".equals(unary.getResultId())) {
        throw new IllegalStateException("unexpected Java audit unary response: " + unary);
      }
      try {
        blocking.executeAudit(failingAuditRequest("trace-error"));
        throw new IllegalStateException("failing Java audit unary was accepted");
      } catch (io.grpc.StatusRuntimeException e) {
        if (Status.fromThrowable(e).getCode() != Status.Code.INTERNAL) {
          throw e;
        }
      }

      Iterator<com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient.AuditEvent> stream =
          blocking.generateAuditEvents(validAuditRequest(""));
      if (!stream.hasNext() || !"audit-stream".equals(stream.next().getEventId()) || stream.hasNext()) {
        throw new IllegalStateException("unexpected Java audit stream result");
      }
      try {
        Iterator<com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient.AuditEvent> failing =
            blocking.generateAuditEvents(failingAuditRequest(""));
        while (failing.hasNext()) {
          failing.next();
        }
        throw new IllegalStateException("failing Java audit stream was accepted");
      } catch (io.grpc.StatusRuntimeException e) {
        if (Status.fromThrowable(e).getCode() != Status.Code.INTERNAL) {
          throw e;
        }
      }

      RecordingObserver<com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient.AuditResponse> uploadResponse =
          new RecordingObserver<>();
      StreamObserver<com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient.AuditEvent> sendEvents =
          async.sendAuditEvents(uploadResponse);
      sendEvents.onNext(validAuditEvent());
      sendEvents.onCompleted();
      uploadResponse.await("Java audit sendEvents");
      if (uploadResponse.error.get() != null || uploadResponse.values.size() != 1
          || !"audit-sendEvents".equals(uploadResponse.values.get(0).getResultId())) {
        throw new IllegalStateException("unexpected Java audit sendEvents result");
      }

      RecordingObserver<com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient.AuditResponse> failingUploadResponse =
          new RecordingObserver<>();
      StreamObserver<com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient.AuditEvent> failingUpload =
          async.sendAuditEvents(failingUploadResponse);
      failingUpload.onNext(failingAuditEvent());
      failingUploadResponse.await("failing Java audit sendEvents");
      expectGrpcStatus(failingUploadResponse.error.get(), Status.Code.INTERNAL, "failing Java audit sendEvents");

      RecordingObserver<com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient.AuditEvent> chatResponse =
          new RecordingObserver<>();
      StreamObserver<com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient.AuditEvent> assistEvents =
          async.assistAuditEvents(chatResponse);
      assistEvents.onNext(validAuditEvent());
      assistEvents.onCompleted();
      chatResponse.await("Java audit assistEvents");
      if (chatResponse.error.get() != null || chatResponse.values.size() != 1
          || !"audit-reply".equals(chatResponse.values.get(0).getEventId())) {
        throw new IllegalStateException("unexpected Java audit assistEvents result");
      }

      RecordingObserver<com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient.AuditEvent> failingChatResponse =
          new RecordingObserver<>();
      StreamObserver<com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient.AuditEvent> failingChat =
          async.assistAuditEvents(failingChatResponse);
      failingChat.onNext(failingAuditEvent());
      failingChatResponse.await("failing Java audit assistEvents");
      expectGrpcStatus(failingChatResponse.error.get(), Status.Code.INTERNAL, "failing Java audit assistEvents");
    } finally {
      channel.shutdownNow();
      channel.awaitTermination(5, TimeUnit.SECONDS);
      server.shutdownNow();
      server.awaitTermination(5, TimeUnit.SECONDS);
    }

    Map<Services.ContractclientRPCs, Integer> completed = new EnumMap<>(Services.ContractclientRPCs.class);
    Map<Services.ContractclientRPCs, Integer> errors = new EnumMap<>(Services.ContractclientRPCs.class);
    countJavaAccountingEvents(accountingEvents, completed, errors);
    assertJavaAuditCounts(completed, errors, Services.ContractclientRPCs.CONTRACTCLIENT_RPCS_EXECUTE_AUDIT);
    assertJavaAuditCounts(completed, errors, Services.ContractclientRPCs.CONTRACTCLIENT_RPCS_GENERATE_AUDIT_EVENTS);
    assertJavaAuditCounts(completed, errors, Services.ContractclientRPCs.CONTRACTCLIENT_RPCS_SEND_AUDIT_EVENTS);
    assertJavaAuditCounts(completed, errors, Services.ContractclientRPCs.CONTRACTCLIENT_RPCS_ASSIST_AUDIT_EVENTS);

    Map<String, Map<Trace.TraceEventKind, Trace.TraceEvent>> traceSeen = new HashMap<>();
    for (Trace.TraceEvent event : traceEvents) {
      traceSeen.computeIfAbsent(event.getTraceId(), ignored -> new EnumMap<>(Trace.TraceEventKind.class))
          .put(event.getKind(), event);
    }
    for (Map.Entry<String, String> entry : Map.of(
        "trace-success", "",
        "trace-error", "audit unary failed").entrySet()) {
      Map<Trace.TraceEventKind, Trace.TraceEvent> byKind = traceSeen.get(entry.getKey());
      if (byKind == null || !byKind.containsKey(Trace.TraceEventKind.TRACE_EVENT_KIND_INTERNAL_REQUEST)) {
        throw new IllegalStateException("missing Java request trace for " + entry.getKey());
      }
      Trace.TraceEvent response = byKind.get(Trace.TraceEventKind.TRACE_EVENT_KIND_INTERNAL_RESPONSE);
      if (response == null) {
        throw new IllegalStateException("missing Java response trace for " + entry.getKey());
      }
      if (!response.getError().contains(entry.getValue())) {
        throw new IllegalStateException("Java response trace error for " + entry.getKey() + " = " + response.getError());
      }
      if (response.getRpc().getContractclient() != Services.ContractclientRPCs.CONTRACTCLIENT_RPCS_EXECUTE_AUDIT) {
        throw new IllegalStateException("Java response trace rpc mismatch: " + response.getRpc());
      }
    }
  }

  @FunctionalInterface
  private interface ThrowingRunnable {
    void run() throws Exception;
  }

  private static void expectProtoCode(ThrowingRunnable runnable, int code, String message) throws Exception {
    try {
      runnable.run();
    } catch (ProtoException e) {
      if (e.getCode() != code) {
        throw new IllegalStateException(message + ": expected code " + code + ", got " + e.getCode(), e);
      }
      return;
    }
    throw new IllegalStateException(message);
  }

  private static List<Contractclient.ContractWatchChange> collectChanges(Iterator<Contractclient.ContractWatchChange> it) throws Exception {
    List<Contractclient.ContractWatchChange> changes = new ArrayList<>();
    try {
      while (it.hasNext()) {
        changes.add(it.next());
      }
    } finally {
      if (it instanceof AutoCloseable) {
        ((AutoCloseable) it).close();
      }
    }
    return changes;
  }

  private static void closeIterator(Iterator<?> it) throws Exception {
    if (it instanceof AutoCloseable) {
      ((AutoCloseable) it).close();
    }
  }

  private static List<Contractclient.ContractWatchEvent> collectWatch(
      com.gitlab.example.tacplus.consumer.contractclient.model.c2sm.internal.grpc.clients.contract.contractclient.ContractApiGrpcClient api,
      Contractclient.ContractWatchRequest req) throws Exception {
    List<Contractclient.ContractWatchEvent> events = new ArrayList<>();
    try (com.gitlab.example.tacplus.consumer.contractclient.model.c2sm.internal.grpc.clients.contract.contractclient.ContractApiGrpcClient.CloseableIterator<Contractclient.ContractWatchEvent> it = api.watch(req)) {
      while (it.hasNext()) {
        events.add(it.next());
      }
    }
    return events;
  }

  private static List<Contractclient.ContractEvent> collectListEvents(
      com.gitlab.example.tacplus.consumer.contractclient.model.c2sm.internal.grpc.clients.contract.contractclient.ContractApiGrpcClient api,
      Contractclient.ContractRequest req) throws Exception {
    List<Contractclient.ContractEvent> events = new ArrayList<>();
    try (com.gitlab.example.tacplus.consumer.contractclient.model.c2sm.internal.grpc.clients.contract.contractclient.ContractApiGrpcClient.CloseableIterator<Contractclient.ContractEvent> it = api.listEvents(req)) {
      while (it.hasNext()) {
        events.add(it.next());
      }
    }
    return events;
  }

  private static void expectWatchCode(
      com.gitlab.example.tacplus.consumer.contractclient.model.c2sm.internal.grpc.clients.contract.contractclient.ContractApiGrpcClient api,
      Contractclient.ContractWatchRequest req,
      int code,
      String message) throws Exception {
    try {
      try (com.gitlab.example.tacplus.consumer.contractclient.model.c2sm.internal.grpc.clients.contract.contractclient.ContractApiGrpcClient.CloseableIterator<Contractclient.ContractWatchEvent> it = api.watch(req)) {
        while (it.hasNext()) {
          it.next();
        }
      }
    } catch (ProtoException e) {
      if (e.getCode() != code) {
        throw new IllegalStateException(message + ": expected code " + code + ", got " + e.getCode(), e);
      }
      return;
    } catch (Throwable t) {
      int got = Status.fromThrowable(t).getCode().value();
      if (got != code) {
        throw new IllegalStateException(message + ": expected code " + code + ", got " + got, t);
      }
      return;
    }
    throw new IllegalStateException(message);
  }

  private static void expectListEventsCode(
      com.gitlab.example.tacplus.consumer.contractclient.model.c2sm.internal.grpc.clients.contract.contractclient.ContractApiGrpcClient api,
      Contractclient.ContractRequest req,
      int code,
      String message) throws Exception {
    try {
      try (com.gitlab.example.tacplus.consumer.contractclient.model.c2sm.internal.grpc.clients.contract.contractclient.ContractApiGrpcClient.CloseableIterator<Contractclient.ContractEvent> it = api.listEvents(req)) {
        while (it.hasNext()) {
          it.next();
        }
      }
    } catch (ProtoException e) {
      if (e.getCode() != code) {
        throw new IllegalStateException(message + ": expected code " + code + ", got " + e.getCode(), e);
      }
      return;
    } catch (Throwable t) {
      int got = Status.fromThrowable(t).getCode().value();
      if (got != code) {
        throw new IllegalStateException(message + ": expected code " + code + ", got " + got, t);
      }
      return;
    }
    throw new IllegalStateException(message);
  }

  private static void runJavaCollectionWatchRuntimeContract() throws Exception {
    java.util.function.ToLongFunction<Contractclient.ContractWatchChange> revisionOf =
        Contractclient.ContractWatchChange::getRevision;

    CollectionRuntime.WatchHub<Contractclient.ContractWatchChange> resumeHub =
        new CollectionRuntime.WatchHub<>(4, 0);
    List<Contractclient.ContractWatchChange> resumed = collectChanges(resumeHub.watch(
        "tenant.alpha",
        1L,
        () -> List.of(validChange(1), validChange(2), validChange(3)).iterator(),
        revisionOf));
    if (resumed.size() != 2 || resumed.get(0).getRevision() != 2L || resumed.get(1).getRevision() != 3L) {
      throw new IllegalStateException("unexpected Java watch resume replay: " + resumed);
    }

    CollectionRuntime.WatchHub<Contractclient.ContractWatchChange> relistHub =
        new CollectionRuntime.WatchHub<>(1, 0);
    Iterator<Contractclient.ContractWatchChange> primary = relistHub.watch(
        "tenant.alpha",
        0L,
        () -> List.of(validChange(2), validChange(3)).iterator(),
        revisionOf);
    Thread.sleep(50);
    try {
      collectChanges(relistHub.watch("tenant.alpha", 1L, Collections::emptyIterator, revisionOf));
      throw new IllegalStateException("evicted Java watch revision did not require relist");
    } catch (io.grpc.StatusRuntimeException e) {
      if (Status.fromThrowable(e).getCode() != Status.OUT_OF_RANGE.getCode()) {
        throw e;
      }
    } finally {
      closeIterator(primary);
    }

    CountDownLatch idleDone = new CountDownLatch(1);
    CollectionRuntime.WatchHub<Contractclient.ContractWatchChange> idleHub =
        new CollectionRuntime.WatchHub<>(2, 1);
    long started = System.nanoTime();
    try {
      collectChanges(idleHub.watch(
          "tenant.alpha",
          0L,
          () -> new Iterator<>() {
            @Override public boolean hasNext() {
              try {
                idleDone.await();
              } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
              }
              return false;
            }
            @Override public Contractclient.ContractWatchChange next() {
              throw new java.util.NoSuchElementException();
            }
          },
          revisionOf));
      throw new IllegalStateException("idle Java watch did not time out");
    } catch (io.grpc.StatusRuntimeException e) {
      if (Status.fromThrowable(e).getCode() != Status.DEADLINE_EXCEEDED.getCode()) {
        throw e;
      }
    } finally {
      idleDone.countDown();
    }
    if (TimeUnit.NANOSECONDS.toSeconds(System.nanoTime() - started) >= 3) {
      throw new IllegalStateException("Java watch idle timeout took too long");
    }

    CollectionRuntime.WatchHub<Contractclient.ContractWatchChange> teardownHub =
        new CollectionRuntime.WatchHub<>(2, 0);
    Iterator<Contractclient.ContractWatchChange> first = teardownHub.watch(
        "tenant.alpha",
        0L,
        () -> List.of(validChange(1)).iterator(),
        revisionOf);
    if (!first.hasNext() || first.next().getRevision() != 1L) {
      throw new IllegalStateException("unexpected Java first watcher revision");
    }
    closeIterator(first);

    AtomicReference<Object> freshResult = new AtomicReference<>();
    Thread freshThread = new Thread(() -> {
      try {
        Iterator<Contractclient.ContractWatchChange> fresh = teardownHub.watch(
            "tenant.alpha",
            0L,
            () -> List.of(validChange(2)).iterator(),
            revisionOf);
        try {
          freshResult.set(fresh.hasNext() ? fresh.next() : "ended");
        } finally {
          closeIterator(fresh);
        }
      } catch (Throwable t) {
        freshResult.set(t);
      }
    }, "java-watch-fresh-drain-test");
    freshThread.setDaemon(true);
    freshThread.start();
    freshThread.join(2000);
    if (freshThread.isAlive()) {
      throw new IllegalStateException("fresh Java watcher did not start a new drain after teardown");
    }
    Object result = freshResult.get();
    if (!(result instanceof Contractclient.ContractWatchChange)
        || ((Contractclient.ContractWatchChange) result).getRevision() != 2L) {
      throw new IllegalStateException("unexpected Java fresh watcher result: " + result);
    }
  }

  private static void runJavaC2SValidationSmoke(Contractclient.ContractRequest req) throws Exception {
    ApiHandler handler = new ApiHandler();
    try {
      new ContractApi(new ApiHandler(), null);
      throw new IllegalStateException("mandatory-auth Java wrapper accepted a null Authorizer");
    } catch (NullPointerException expected) {
      // Mandatory auth is a constructor dependency in Java; null must fail fast.
    }
    TestAuthorizer authorizer = new TestAuthorizer();
    AtomicInteger authenticateCalls = new AtomicInteger();
    ContractApi wrapper = new ContractApi(handler, authorizer);
    String serverName = InProcessServerBuilder.generateName();
    Server server = InProcessServerBuilder.forName(serverName)
        .directExecutor()
        .addService(wrapper)
        .build()
        .start();
    ManagedChannel channel = InProcessChannelBuilder.forName(serverName)
        .directExecutor()
        .build();
    try {
      com.gitlab.example.tacplus.consumer.contractclient.model.c2sm.ServiceManager c2s =
          new com.gitlab.example.tacplus.consumer.contractclient.model.c2sm.ServiceManager(channel, null);
      com.gitlab.example.tacplus.consumer.contractclient.model.c2sm.ServiceManager c2sAuth =
          new com.gitlab.example.tacplus.consumer.contractclient.model.c2sm.ServiceManager(channel, () -> "token");
      com.gitlab.example.tacplus.consumer.contractclient.model.c2sm.ServiceManager c2sFailingToken =
          new com.gitlab.example.tacplus.consumer.contractclient.model.c2sm.ServiceManager(
              channel,
              () -> { throw new IllegalStateException("identity unavailable"); });
      var api = c2s.grpc().contract().contractApi();
      var authApi = c2sAuth.grpc().contract().contractApi();
      var failingTokenApi = c2sFailingToken.grpc().contract().contractApi();
      expectProtoCode(
          () -> api.getContract(req),
          ProtoException.UNAUTHENTICATED,
          "protected Java C2S Read did not preserve unauthenticated status");
      if (handler.readCalls() != 0) {
        throw new IllegalStateException("unauthenticated Java C2S Read reached handler; calls=" + handler.readCalls());
      }
      if (authorizer.authorizeCalls() != 0) {
        throw new IllegalStateException("expected unauthenticated Java Read to fail before authorize, got " + authorizer.authorizeCalls());
      }

      authorizer.setRejectAll(false);
      Contractclient.ContractResponse protectedResp = authApi.getContract(req);
      if (!"result-123".equals(protectedResp.getResultId()) || !protectedResp.getAccepted()) {
        throw new IllegalStateException("unexpected Java C2S Read response: " + protectedResp);
      }
      if (handler.readCalls() != 1) {
        throw new IllegalStateException("expected authorized Java C2S Read to reach handler once, got " + handler.readCalls());
      }
      expectProtoCode(
          () -> authApi.getContract(forbiddenRequest()),
          ProtoException.PERMISSION_DENIED,
          "namespace-mismatched Java C2S Read was accepted");
      if (handler.readCalls() != 1) {
        throw new IllegalStateException("namespace-mismatched Java C2S Read reached handler; calls=" + handler.readCalls());
      }

      expectProtoCode(
          () -> failingTokenApi.getContract(req),
          ProtoException.UNAUTHENTICATED,
          "protected Java C2S Read with failing token provider was accepted");
      if (handler.readCalls() != 1) {
        throw new IllegalStateException("failing-token Java C2S Read reached handler; calls=" + handler.readCalls());
      }

      Contractclient.ContractResponse resp = api.getPublicContract(req);
      if (!"result-123".equals(resp.getResultId()) || !resp.getAccepted()) {
        throw new IllegalStateException("unexpected Java C2S GetPublicContract response: " + resp);
      }
      if (handler.publicReadCalls() != 1) {
        throw new IllegalStateException("expected valid Java C2S request to reach handler once, got " + handler.publicReadCalls());
      }
      Contractclient.ContractResponse failingTokenPublicResp = failingTokenApi.getPublicContract(req);
      if (!"result-123".equals(failingTokenPublicResp.getResultId()) || !failingTokenPublicResp.getAccepted()) {
        throw new IllegalStateException("unexpected failing-token Java C2S GetPublicContract response: " + failingTokenPublicResp);
      }
      if (handler.publicReadCalls() != 2) {
        throw new IllegalStateException("expected failing-token Java C2S request to reach handler once, got " + handler.publicReadCalls());
      }
      expectProtoCode(
          () -> api.getPublicContract(invalidRequest()),
          ProtoException.INVALID_ARGUMENT,
          "invalid Java C2S request was accepted");
      if (handler.publicReadCalls() != 2) {
        throw new IllegalStateException("invalid Java C2S request reached handler; calls=" + handler.publicReadCalls());
      }
      expectProtoCode(
          () -> api.getPublicContract(badResponseRequest()),
          ProtoException.INTERNAL,
          "invalid Java C2S response was accepted");
      if (handler.publicReadCalls() != 3) {
        throw new IllegalStateException("expected bad-response Java C2S request to reach handler once, calls=" + handler.publicReadCalls());
      }

      expectWatchCode(
          api,
          validWatchRequest(),
          ProtoException.UNAUTHENTICATED,
          "unwired Java auth-any-user Watch did not fail closed");
      if (handler.watchCalls() != 0) {
        throw new IllegalStateException("unwired Java auth-any-user Watch reached handler; calls=" + handler.watchCalls());
      }

      wrapper.setAuthenticateFunc((ContractApi.Authenticator) (targetNamespace) -> {
        authenticateCalls.incrementAndGet();
        if (targetNamespace != null && !targetNamespace.isEmpty() && !"/".equals(targetNamespace)) {
          throw ProtoException.newErrPermissionDenied(null, "unexpected target namespace " + targetNamespace);
        }
        return java.util.Map.of();
      });
      List<Contractclient.ContractWatchEvent> events = collectWatch(authApi, validWatchRequest());
      if (events.size() != 2
          || !"event-1".equals(events.get(0).getSnapshotItem().getEventId())
          || events.get(1).getSnapshotDone() != 1L) {
        throw new IllegalStateException("unexpected Java C2S Watch events: " + events);
      }
      if (handler.watchCalls() != 1) {
        throw new IllegalStateException("expected valid Java C2S Watch to reach handler once, got " + handler.watchCalls());
      }
      if (authenticateCalls.get() != 1) {
        throw new IllegalStateException("expected valid Java C2S Watch to authenticate once, got " + authenticateCalls.get());
      }
      expectWatchCode(
          authApi,
          invalidWatchRequest(),
          ProtoException.INVALID_ARGUMENT,
          "invalid Java C2S Watch request was accepted");
      if (handler.watchCalls() != 1) {
        throw new IllegalStateException("invalid Java C2S Watch reached handler; calls=" + handler.watchCalls());
      }
      expectWatchCode(
          authApi,
          badWatchRequest(),
          ProtoException.INTERNAL,
          "invalid Java C2S Watch response was accepted");
      if (handler.watchCalls() != 2) {
        throw new IllegalStateException("expected bad-event Java C2S Watch to reach handler once, calls=" + handler.watchCalls());
      }

      expectListEventsCode(
          api,
          req,
          ProtoException.UNAUTHENTICATED,
          "auth-any-user Java C2S ListEvents without token did not fail closed");
      if (handler.listEventsCalls() != 0) {
        throw new IllegalStateException("unauthenticated Java C2S ListEvents reached handler; calls=" + handler.listEventsCalls());
      }

      int authenticateCallsBeforeListEvents = authenticateCalls.get();
      List<Contractclient.ContractEvent> listed = collectListEvents(authApi, req);
      if (listed.size() != 1 || !"event-1".equals(listed.get(0).getEventId())) {
        throw new IllegalStateException("unexpected Java C2S ListEvents events: " + listed);
      }
      if (handler.listEventsCalls() != 1) {
        throw new IllegalStateException("expected valid Java C2S ListEvents to reach handler once, got " + handler.listEventsCalls());
      }
      if (authenticateCalls.get() != authenticateCallsBeforeListEvents + 1) {
        throw new IllegalStateException("expected valid Java C2S ListEvents to authenticate once, got before="
            + authenticateCallsBeforeListEvents + " after=" + authenticateCalls.get());
      }
      expectListEventsCode(
          authApi,
          invalidRequest(),
          ProtoException.INVALID_ARGUMENT,
          "invalid Java C2S ListEvents request was accepted");
      if (handler.listEventsCalls() != 1) {
        throw new IllegalStateException("invalid Java C2S ListEvents reached handler; calls=" + handler.listEventsCalls());
      }
      expectListEventsCode(
          authApi,
          badEventRequest(),
          ProtoException.INTERNAL,
          "invalid Java C2S ListEvents response was accepted");
      if (handler.listEventsCalls() != 2) {
        throw new IllegalStateException("expected bad-event Java C2S ListEvents to reach handler once, calls=" + handler.listEventsCalls());
      }
    } finally {
      channel.shutdownNow();
      channel.awaitTermination(5, TimeUnit.SECONDS);
      server.shutdownNow();
      server.awaitTermination(5, TimeUnit.SECONDS);
    }
  }

  private static void runJavaW2SChatSmoke() throws Exception {
    try (RawWsServer server = RawWsServer.start()) {
      com.gitlab.example.tacplus.consumer.contractclient.model.w2sm.ServiceManager liveW2S =
          com.gitlab.example.tacplus.consumer.contractclient.model.w2sm.ServiceManager.withTokenProvider(
              "http://127.0.0.1:" + server.port(), () -> "token");
      var client = liveW2S.w2sClients().contractclient().contractApi().withConnectTimeout(Duration.ofSeconds(2));
      BlockingQueue<Contractclient.ContractEvent> received = new LinkedBlockingQueue<>();
      try (var handle = client.assistEvents(received::offer)) {
        try {
          handle.send(invalidEvent());
          throw new IllegalStateException("invalid Java W2S assistEvents send was accepted");
        } catch (ProtoException e) {
          if (e.getCode() != ProtoException.INVALID_ARGUMENT) {
            throw new IllegalStateException("expected INVALID_ARGUMENT for invalid W2S assistEvents send, got " + e.getCode(), e);
          }
        }
        handle.send(validEvent()).get(5, TimeUnit.SECONDS);
        Contractclient.ContractEvent got = received.poll(5, TimeUnit.SECONDS);
        if (got == null) {
          throw new IllegalStateException("Java W2S assistEvents did not receive a response");
        }
        if (!"java-assistEvents".equals(got.getEventId()) || !"tenant.alpha".equals(got.getTenantNamespace())) {
          throw new IllegalStateException("Java W2S assistEvents decoded the wrong response: " + got);
        }
      }

      server.awaitDone();
      List<JsonObject> frames = server.jsonFrames();
      if (frames.size() < 3) {
        throw new IllegalStateException("expected start, message, and complete frames, got " + frames.size());
      }
      assertFrame(frames.get(0), "start", true);
      assertFrame(frames.get(1), "message", true);
      assertFrame(frames.get(2), "complete", true);
      JsonObject message = frames.get(1).getAsJsonObject("message");
      if (message == null || !"java-send".equals(message.get("eventId").getAsString())) {
        throw new IllegalStateException("Java W2S message frame did not include the outbound message");
      }
    }
  }

  private static void runJavaW2SFailClosedSmoke(Contractclient.ContractRequest req) {
    com.gitlab.example.tacplus.consumer.contractclient.model.w2sm.ServiceManager noToken =
        new com.gitlab.example.tacplus.consumer.contractclient.model.w2sm.ServiceManager(
            "http://127.0.0.1:9", target -> null);
    try {
      noToken.w2sClients().contractclient().contractApi().getContract(req);
      throw new IllegalStateException("auth-required Java W2S call without a token provider succeeded");
    } catch (ProtoException e) {
      if (e.getCode() != ProtoException.UNAUTHENTICATED) {
        throw new IllegalStateException("expected UNAUTHENTICATED, got " + e.getCode(), e);
      }
    }

    com.gitlab.example.tacplus.consumer.contractclient.model.w2sm.ServiceManager failingToken =
        com.gitlab.example.tacplus.consumer.contractclient.model.w2sm.ServiceManager.withTokenProvider(
            "http://127.0.0.1:9",
            () -> { throw new IllegalStateException("identity unavailable"); });
    try {
      failingToken.w2sClients().contractclient().contractApi().getContract(req);
      throw new IllegalStateException("auth-required Java W2S call with a failing token provider succeeded");
    } catch (ProtoException e) {
      if (e.getCode() != ProtoException.UNAUTHENTICATED) {
        throw new IllegalStateException("expected UNAUTHENTICATED for failing token provider, got " + e.getCode(), e);
      }
    }
  }

  // Executes the generated record dispatch the ServiceManager calls on every
  // subscribed FixtureEvent: handleFixtureEvent passes the FixtureEventRecord
  // projection, typed, to its insert before it returns (synchronous), writes
  // nothing on a trace-round noop event, and fails the delivery with the
  // insert's exception.
  private static void runJavaRecordDispatchSmoke() throws Exception {
    var event = com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractfixtures.Contractfixtures.FixtureEvent.newBuilder()
        .setEventId("evt-dispatch")
        .setTenantNamespace("tenant-a")
        .setTimestampMs(1_700_000_000_123L)
        .build();
    var rows = new java.util.ArrayList<com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractfixtures.Contractfixtures.FixtureEventRecord>();
    com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractfixtures.FixtureEventProjections.FixtureEventRecordInsert insert = rows::add;
    com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractfixtures.FixtureEventProjections.handleFixtureEvent(event, insert);
    if (rows.size() != 1
        || !"evt-dispatch".equals(rows.get(0).getEventId())
        || !"tenant-a".equals(rows.get(0).getTenantNamespace())
        || rows.get(0).getTimestampMs() != 1_700_000_000_123L) {
      throw new IllegalStateException("Java record dispatch rows = " + rows);
    }

    rows.clear();
    com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractfixtures.FixtureEventProjections.handleFixtureEvent(
        event.toBuilder().setNoop(true).build(), insert);
    if (!rows.isEmpty()) {
      throw new IllegalStateException("a trace-round noop event must write no row, got " + rows);
    }

    try {
      com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractfixtures.FixtureEventProjections.handleFixtureEvent(
          event, row -> { throw new IllegalStateException("store down"); });
      throw new AssertionError("Java record dispatch swallowed the insert's exception");
    } catch (IllegalStateException e) {
      if (!"store down".equals(e.getMessage())) {
        throw e;
      }
    }
  }

  private static void runJavaEventBusSubscriberMetadataSmoke() throws Exception {
    Contractfixtures.EventBusRecordMeta recordMeta = Contractfixtures.EventBusRecordMeta.newBuilder()
        .setKey(ByteString.copyFromUtf8("fixture-key"))
        .setTopic("contractfixtures.FixtureState.events")
        .setPartition(7)
        .setOffset(42)
        .setTimestamp(Timestamp.newBuilder().setSeconds(1_700_000_000L).setNanos(123_000_000).build())
        .build();
    Contractfixtures.FixtureEvent event = Contractfixtures.FixtureEvent.newBuilder()
        .setTraceId("fixture-subscribe-event")
        .build();
    FakeEventBusChannel channel = new FakeEventBusChannel(
        encodeJavaSubscribeEnvelope(recordMeta.toByteArray(), event.toByteArray()));
    CallCredentials creds = new CallCredentials() {
      @Override
      public void applyRequestMetadata(RequestInfo requestInfo, Executor appExecutor, MetadataApplier applier) {
        Metadata headers = new Metadata();
        headers.put(Metadata.Key.of("authorization", Metadata.ASCII_STRING_MARSHALLER), "Bearer subscriber-token");
        applier.apply(headers);
      }
    };
    ContractfixturesFixtureStateEventsSubscriber subscriber =
        new ContractfixturesFixtureStateEventsSubscriber(channel, () -> creds, "contractclient");

    AtomicReference<Contractfixtures.FixtureEvent> gotEvent = new AtomicReference<>();
    AtomicReference<ContractfixturesFixtureStateEventsSubscriber.RecordMeta> gotMeta = new AtomicReference<>();
    subscriber.subscribe((delivered, meta) -> {
      gotEvent.set(delivered);
      gotMeta.set(meta);
      Thread.currentThread().interrupt();
    });
    Thread.interrupted();

    // Per-topic subscribe name (eventbus.PerTopicSubscribeParts), not the flat
    // EventBusTypedService/Subscribe<Stem> this fixture asserted before 070d210a.
    // grpc-java full method names carry no leading slash.
    if (!"contractfixtures.FixtureStateStateService/SubscribeToEvents".equals(channel.fullMethod.get())) {
      throw new IllegalStateException("Java subscriber used unexpected EventBus method: " + channel.fullMethod.get());
    }
    // The subscriber must attach the supplied call credentials to every
    // (re)connect — a subscription that opens without them is rejected by
    // event-bus, and dropping the supplier would silently un-authenticate it.
    if (channel.credentials.get() != creds) {
      throw new IllegalStateException("Java subscriber did not attach the supplied call credentials: "
          + channel.credentials.get());
    }
    byte[] sentRequest = channel.sentRequest.get();
    if (sentRequest == null) {
      throw new IllegalStateException("Java subscriber did not send SubscribeRequest");
    }
    if (!new String(sentRequest, StandardCharsets.UTF_8).contains("contractclient")) {
      throw new IllegalStateException("Java SubscribeRequest missing consumer group");
    }
    if (gotEvent.get() == null || !"fixture-subscribe-event".equals(gotEvent.get().getTraceId())) {
      throw new IllegalStateException("Java subscriber delivered wrong event: " + gotEvent.get());
    }
    ContractfixturesFixtureStateEventsSubscriber.RecordMeta meta = gotMeta.get();
    if (meta == null) {
      throw new IllegalStateException("Java subscriber delivered null metadata");
    }
    if (!Arrays.equals(meta.key, recordMeta.getKey().toByteArray())
        || !recordMeta.getTopic().equals(meta.topic)
        || meta.partition != recordMeta.getPartition()
        || meta.offset != recordMeta.getOffset()
        || meta.timestamp != 1_700_000_000_123L) {
      throw new IllegalStateException("Java subscriber metadata mismatch: key="
          + Arrays.toString(meta.key)
          + " topic=" + meta.topic
          + " partition=" + meta.partition
          + " offset=" + meta.offset
          + " timestamp=" + meta.timestamp);
    }
  }

  private static byte[] encodeJavaSubscribeEnvelope(byte[] meta, byte[] payload) {
    ByteArrayOutputStream out = new ByteArrayOutputStream();
    writeLengthDelimitedField(out, 1, meta);
    writeLengthDelimitedField(out, 2, payload);
    return out.toByteArray();
  }

  private static void writeLengthDelimitedField(ByteArrayOutputStream out, int fieldNum, byte[] data) {
    writeJavaVarint(out, ((long) fieldNum << 3) | 2);
    writeJavaVarint(out, data.length);
    out.write(data, 0, data.length);
  }

  private static void writeJavaVarint(ByteArrayOutputStream out, long value) {
    while (value >= 0x80) {
      out.write((int) (value & 0x7F) | 0x80);
      value >>>= 7;
    }
    out.write((int) value);
  }

  private static final class FakeEventBusChannel extends Channel {
    private final byte[] frame;
    final AtomicReference<String> fullMethod = new AtomicReference<>();
    final AtomicReference<byte[]> sentRequest = new AtomicReference<>();
    final AtomicReference<CallCredentials> credentials = new AtomicReference<>();

    FakeEventBusChannel(byte[] frame) {
      this.frame = frame;
    }

    @Override
    @SuppressWarnings("unchecked")
    public <ReqT, RespT> ClientCall<ReqT, RespT> newCall(MethodDescriptor<ReqT, RespT> methodDescriptor, CallOptions callOptions) {
      fullMethod.set(methodDescriptor.getFullMethodName());
      credentials.set(callOptions.getCredentials());
      return (ClientCall<ReqT, RespT>) new FakeEventBusCall(this);
    }

    @Override
    public String authority() {
      return "eventbus-fixture";
    }
  }

  private static final class FakeEventBusCall extends ClientCall<byte[], byte[]> {
    private final FakeEventBusChannel channel;
    private Listener<byte[]> listener;
    private boolean delivered;

    FakeEventBusCall(FakeEventBusChannel channel) {
      this.channel = channel;
    }

    @Override
    public void start(Listener<byte[]> responseListener, Metadata headers) {
      listener = responseListener;
    }

    @Override
    public void request(int numMessages) {
      if (!delivered && numMessages > 0) {
        delivered = true;
        listener.onMessage(channel.frame);
        listener.onClose(Status.OK, new Metadata());
      }
    }

    @Override
    public void cancel(String message, Throwable cause) {}

    @Override
    public void halfClose() {}

    @Override
    public void sendMessage(byte[] message) {
      channel.sentRequest.set(message);
    }
  }

  private static void assertFrame(JsonObject frame, String type, boolean authRequired) {
    if (!frame.has("type") || !type.equals(frame.get("type").getAsString())) {
      throw new IllegalStateException("expected WebSocket frame type " + type + ", got " + frame);
    }
    if (authRequired && (!frame.has("authorization") || !"Bearer token".equals(frame.get("authorization").getAsString()))) {
      throw new IllegalStateException("missing bearer authorization on frame " + frame);
    }
  }

  private static final class RawWsServer implements AutoCloseable {
    private static final String WS_MAGIC = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11";

    private final ServerSocket server;
    private final List<String> frames = new ArrayList<>();
    private final CountDownLatch done = new CountDownLatch(1);
    private volatile Throwable error;
    private Thread thread;

    private RawWsServer(ServerSocket server) {
      this.server = server;
    }

    static RawWsServer start() throws Exception {
      ServerSocket socket = new ServerSocket(0, 1, InetAddress.getByName("127.0.0.1"));
      RawWsServer server = new RawWsServer(socket);
      server.thread = new Thread(server::run, "java-w2s-raw-websocket");
      server.thread.setDaemon(true);
      server.thread.start();
      return server;
    }

    int port() {
      return server.getLocalPort();
    }

    void awaitDone() throws Exception {
      if (!done.await(5, TimeUnit.SECONDS)) {
        throw new IllegalStateException("Java W2S raw WebSocket server did not finish");
      }
      if (error != null) {
        throw new IllegalStateException("Java W2S raw WebSocket server failed", error);
      }
    }

    List<JsonObject> jsonFrames() {
      List<JsonObject> parsed = new ArrayList<>();
      for (String frame : frames) {
        parsed.add(JsonParser.parseString(frame).getAsJsonObject());
      }
      return parsed;
    }

    @Override
    public void close() {
      try {
        server.close();
      } catch (Exception ignored) {
      }
      if (thread != null) {
        try {
          thread.join(1000);
        } catch (InterruptedException e) {
          Thread.currentThread().interrupt();
        }
      }
    }

    private void run() {
      try (Socket socket = server.accept()) {
        socket.setSoTimeout(5000);
        InputStream in = socket.getInputStream();
        OutputStream out = socket.getOutputStream();
        String header = readHttpHeader(in);
        if (!header.startsWith("GET /contractclient/ContractApi/AssistEvents ")) {
          throw new IllegalStateException("unexpected WebSocket path: " + header);
        }
        String key = headerValue(header, "Sec-WebSocket-Key");
        if (key == null || key.isBlank()) {
          throw new IllegalStateException("missing Sec-WebSocket-Key");
        }
        writeHandshake(out, key);

        frames.add(readTextFrame(in));
        frames.add(readTextFrame(in));
        sendTextFrame(out, "{\"type\":\"message\",\"message\":{\"eventId\":\"java-assistEvents\",\"tenantNamespace\":\"tenant.alpha\",\"payload\":\"reply\"}}");
        frames.add(readTextFrame(in));
      } catch (Throwable t) {
        error = t;
      } finally {
        done.countDown();
      }
    }

    private static String readHttpHeader(InputStream in) throws Exception {
      ByteArrayOutputStream bytes = new ByteArrayOutputStream();
      int matched = 0;
      byte[] end = new byte[] {'\r', '\n', '\r', '\n'};
      int value;
      while ((value = in.read()) != -1) {
        bytes.write(value);
        if (value == end[matched]) {
          matched++;
          if (matched == end.length) {
            return new String(bytes.toByteArray(), StandardCharsets.UTF_8);
          }
        } else {
          matched = value == end[0] ? 1 : 0;
        }
        if (bytes.size() > 8192) {
          throw new IllegalStateException("WebSocket handshake header is too large");
        }
      }
      throw new IllegalStateException("WebSocket handshake ended early");
    }

    private static String headerValue(String header, String name) {
      String[] lines = header.split("\\r?\\n");
      for (String line : lines) {
        int colon = line.indexOf(':');
        if (colon > 0 && name.equalsIgnoreCase(line.substring(0, colon).trim())) {
          return line.substring(colon + 1).trim();
        }
      }
      return null;
    }

    private static void writeHandshake(OutputStream out, String key) throws Exception {
      MessageDigest sha1 = MessageDigest.getInstance("SHA-1");
      byte[] digest = sha1.digest((key + WS_MAGIC).getBytes(StandardCharsets.US_ASCII));
      String accept = Base64.getEncoder().encodeToString(digest);
      String response = "HTTP/1.1 101 Switching Protocols\r\n"
          + "Upgrade: websocket\r\n"
          + "Connection: Upgrade\r\n"
          + "Sec-WebSocket-Accept: " + accept + "\r\n\r\n";
      out.write(response.getBytes(StandardCharsets.US_ASCII));
      out.flush();
    }

    private static String readTextFrame(InputStream in) throws Exception {
      int first = readByte(in);
      int second = readByte(in);
      int opcode = first & 0x0f;
      if (opcode == 8) {
        return "";
      }
      if (opcode != 1) {
        throw new IllegalStateException("expected text WebSocket frame, got opcode " + opcode);
      }
      boolean masked = (second & 0x80) != 0;
      long length = second & 0x7f;
      if (length == 126) {
        length = (readByte(in) << 8) | readByte(in);
      } else if (length == 127) {
        length = 0;
        for (int i = 0; i < 8; i++) {
          length = (length << 8) | readByte(in);
        }
      }
      if (length > Integer.MAX_VALUE) {
        throw new IllegalStateException("WebSocket frame is too large");
      }
      byte[] mask = masked ? in.readNBytes(4) : new byte[0];
      byte[] payload = in.readNBytes((int) length);
      if (payload.length != (int) length || mask.length != (masked ? 4 : 0)) {
        throw new IllegalStateException("WebSocket frame ended early");
      }
      if (masked) {
        for (int i = 0; i < payload.length; i++) {
          payload[i] = (byte) (payload[i] ^ mask[i % 4]);
        }
      }
      return new String(payload, StandardCharsets.UTF_8);
    }

    private static int readByte(InputStream in) throws Exception {
      int value = in.read();
      if (value < 0) {
        throw new IllegalStateException("WebSocket stream ended early");
      }
      return value;
    }

    private static void sendTextFrame(OutputStream out, String text) throws Exception {
      byte[] payload = text.getBytes(StandardCharsets.UTF_8);
      out.write(0x81);
      if (payload.length < 126) {
        out.write(payload.length);
      } else if (payload.length <= 0xffff) {
        out.write(126);
        out.write((payload.length >>> 8) & 0xff);
        out.write(payload.length & 0xff);
      } else {
        out.write(127);
        for (int i = 7; i >= 0; i--) {
          out.write((payload.length >>> (8 * i)) & 0xff);
        }
      }
      out.write(payload);
      out.flush();
    }
  }

  public static void main(String[] args) throws Exception {
    Contractclient.ContractRequest req = validRequest();
    Helpers.validateContractRequest(req);
    Helpers.validateContractResponse(validResponse());
    Helpers.validateContractEvent(validEvent());

    ManagedChannel channel = ManagedChannelBuilder.forTarget("127.0.0.1:443").usePlaintext().build();
    try {
      com.gitlab.example.tacplus.consumer.contractclient.model.c2sm.ServiceManager c2s =
          new com.gitlab.example.tacplus.consumer.contractclient.model.c2sm.ServiceManager(channel, () -> "token");
      if (c2s.grpc().contract().contractApi() == null) {
        throw new IllegalStateException("missing C2S client");
      }
    } finally {
      channel.shutdownNow();
      channel.awaitTermination(5, TimeUnit.SECONDS);
    }

    com.gitlab.example.tacplus.consumer.contractclient.model.w2sm.ServiceManager w2s =
        com.gitlab.example.tacplus.consumer.contractclient.model.w2sm.ServiceManager.withTokenProvider(
            "https://gateway.example", () -> "token");
    if (w2s.grpc().contractclient().contractApi() == null) {
      throw new IllegalStateException("missing W2S client");
    }
    runJavaAuditAccountingTraceSmoke();
    runJavaCollectionWatchRuntimeContract();
    runJavaC2SValidationSmoke(req);
    runJavaEventBusSubscriberMetadataSmoke();
    runJavaRecordDispatchSmoke();
    runJavaW2SChatSmoke();
    runJavaW2SFailClosedSmoke(req);
    runJavaRed1SanitizeVector();
    runJavaRed1WireEgressRedaction();
    runJavaDiskstoreFailClosedRead();

    ContractApi wrapper = new ContractApi(new ApiHandler(), Authorizer.create());
    wrapper.setAuthenticateFunc((ContractApi.Authenticator) (_targetNamespace) -> java.util.Map.of());
  }

  // RED-1 executed evidence (Java leg): the generated sanitize transform is
  // fail-closed, the format-preserving anonymize matches the cross-language
  // golden vector byte-for-byte, and the generated builder maps a token data
  // grant onto RAW (raw egress) — matching the executed Go and Rust legs.
  private static void runJavaRed1SanitizeVector() {
    var fx = com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractfixtures.Contractfixtures.FixtureEvent.newBuilder()
        .setEventId("evt-1")
        .setPayload("secret-payload")
        .setContactEmail("user@example.com")
        .setNotes("sensitive-notes")
        .build();
    byte[] key = "per-deployment-key".getBytes(java.nio.charset.StandardCharsets.UTF_8);

    // Fail-closed: null entitlement — hard floors apply AND MAY_* defaults protect.
    var out = com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractfixtures.Helpers.sanitizeFixtureEvent(fx, null, key);
    if (!out.getPayload().isEmpty() || !out.getPayloadRedacted()) {
      throw new IllegalStateException("DROP must clear payload, got " + out.getPayload());
    }
    if (!"u6e0907e2dcbb@h400973be.example".equals(out.getContactEmail()) || !out.getContactEmailRedacted()) {
      throw new IllegalStateException("format-preserving surrogate drifted: " + out.getContactEmail());
    }
    if (!out.getNotes().isEmpty() || !out.getNotesRedacted()) {
      throw new IllegalStateException("MAY_REDACT must default to REDACT (fail-closed), got " + out.getNotes());
    }

    // Loosening: a matching token data grant maps onto RAW via the builder.
    var ent = com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractfixtures.Helpers.buildFixtureEventEntitlement(
        java.util.Set.of("CONTRACTFIXTURES_FIXTURE_EVENT_NOTES"));
    var loosened = com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractfixtures.Helpers.sanitizeFixtureEvent(fx, ent, key);
    if (!"sensitive-notes".equals(loosened.getNotes()) || loosened.getNotesRedacted()) {
      throw new IllegalStateException("RAW grant must egress notes raw, got " + loosened.getNotes());
    }
  }

  private interface DiskstoreCall {
    Object run() throws Exception;
  }

  private static void mustThrowDiskstore(String name, DiskstoreCall call) {
    try {
      call.run();
      throw new IllegalStateException("PC-L-11: " + name + " must fail loud (fail-open read)");
    } catch (com.gitlab.example.tacplus.consumer.contractclient.model.chassis.diskstore.DiskStoreException expected) {
      // Typed disk-store failure — expected.
    } catch (IllegalStateException e) {
      throw e;
    } catch (Exception e) {
      throw new IllegalStateException("PC-L-11: " + name + " threw an untyped error: " + e);
    }
  }

  // PC-L-11 (Java leg): the generated TYPED store must never hand a record back
  // from a corrupt or constraint-violating payload — decode and validation
  // failures on the read path are LOUD DiskStoreExceptions, not a silent null
  // that reads as "empty store".
  private static void runJavaDiskstoreFailClosedRead() throws Exception {
    var stores = com.gitlab.example.tacplus.consumer.contractclient.model.chassis.diskstore.stores.DiskStores.class;
    if (stores == null) {
      throw new IllegalStateException("unreachable");
    }
    java.nio.file.Path base = java.nio.file.Files.createTempDirectory("tacplus-diskstore-test");
    try {
      var opts = com.gitlab.example.tacplus.consumer.contractclient.model.chassis.diskstore.Snapshot.Opts.defaults();
      var entry = com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractfixtures.Contractfixtures.FixtureStoreEntry.newBuilder()
          .setEntryId("e-1")
          .setValue("v")
          .build();
      com.gitlab.example.tacplus.consumer.contractclient.model.chassis.diskstore.stores.DiskStores.saveFixtureStoreEntry(base, "ns", entry, opts);
      var got = com.gitlab.example.tacplus.consumer.contractclient.model.chassis.diskstore.stores.DiskStores.loadFixtureStoreEntry(base, "ns");
      if (got == null || !"e-1".equals(got.getEntryId())) {
        throw new IllegalStateException("diskstore roundtrip mismatch: " + got);
      }

      java.nio.file.Path path = com.gitlab.example.tacplus.consumer.contractclient.model.chassis.diskstore.StorePath.storePath(base, "ns", "fixture_store_entry", ".snapshot");
      com.gitlab.example.tacplus.consumer.contractclient.model.chassis.diskstore.Snapshot.writeSnapshot(path, "{not json".getBytes(java.nio.charset.StandardCharsets.UTF_8), opts);
      mustThrowDiskstore("corrupt snapshot", () -> com.gitlab.example.tacplus.consumer.contractclient.model.chassis.diskstore.stores.DiskStores.loadFixtureStoreEntry(base, "ns"));

      com.gitlab.example.tacplus.consumer.contractclient.model.chassis.diskstore.Snapshot.writeSnapshot(path, "{\"entry_id\":\"\"}".getBytes(java.nio.charset.StandardCharsets.UTF_8), opts);
      mustThrowDiskstore("constraint-violating snapshot", () -> com.gitlab.example.tacplus.consumer.contractclient.model.chassis.diskstore.stores.DiskStores.loadFixtureStoreEntry(base, "ns"));

      mustThrowDiskstore("corrupt redo payload", () -> com.gitlab.example.tacplus.consumer.contractclient.model.chassis.diskstore.stores.DiskStores.decodeFixtureStoreEntry(
          com.gitlab.example.tacplus.consumer.contractclient.model.chassis.diskstore.RedoLog.Record.upsert("k", "{oops")));
      mustThrowDiskstore("constraint-violating redo payload", () -> com.gitlab.example.tacplus.consumer.contractclient.model.chassis.diskstore.stores.DiskStores.decodeFixtureStoreEntry(
          com.gitlab.example.tacplus.consumer.contractclient.model.chassis.diskstore.RedoLog.Record.upsert("k", "{\"entry_id\":\"\"}")));
      if (com.gitlab.example.tacplus.consumer.contractclient.model.chassis.diskstore.stores.DiskStores.decodeFixtureStoreEntry(
          com.gitlab.example.tacplus.consumer.contractclient.model.chassis.diskstore.RedoLog.Record.tombstone("k")) != null) {
        throw new IllegalStateException("tombstone must decode to null");
      }
    } finally {
      try (var walk = java.nio.file.Files.walk(base)) {
        walk.sorted(java.util.Comparator.reverseOrder()).forEach(p -> {
          try { java.nio.file.Files.deleteIfExists(p); } catch (Exception ignored) {}
        });
      }
    }
  }

  // RED-1 wire-level evidence (Java leg): the GENERATED wrapper — with zero
  // handler redaction code — ships redacted records to an unentitled caller
  // over an in-process gRPC server on every record-bearing egress path:
  // SNAPSHOT stream (listEvents), PAGE (listEventsPaged), and WATCH (snapshot
  // item). Fail-closed: the caller's token carries no data grants.
  private static void runJavaRed1WireEgressRedaction() throws Exception {
    ApiHandler handler = new ApiHandler();
    TestAuthorizer authorizer = new TestAuthorizer();
    authorizer.setRejectAll(false);
    ContractApi wrapper = new ContractApi(handler, authorizer);
    wrapper.setAuthenticateFunc((ContractApi.Authenticator) (targetNamespace) -> java.util.Map.of());
    wrapper.setAnonymizeHmacKey("per-deployment-key".getBytes(java.nio.charset.StandardCharsets.UTF_8));
    String serverName = InProcessServerBuilder.generateName();
    Server server = InProcessServerBuilder.forName(serverName)
        .directExecutor()
        .addService(wrapper)
        .build()
        .start();
    ManagedChannel channel = InProcessChannelBuilder.forName(serverName)
        .directExecutor()
        .build();
    try {
      var api = new com.gitlab.example.tacplus.consumer.contractclient.model.c2sm.ServiceManager(channel, () -> "token")
          .grpc().contract().contractApi();

      // SNAPSHOT stream (listEvents): every item redacted on the wire.
      List<Contractclient.ContractEvent> listed = collectListEvents(api, validRequest());
      if (listed.isEmpty()) {
        throw new IllegalStateException("red1: SNAPSHOT egress returned no items");
      }
      for (Contractclient.ContractEvent e : listed) {
        if (!e.getPayload().isEmpty() || !e.getPayloadRedacted()) {
          throw new IllegalStateException("red1: SNAPSHOT egress must redact payload fail-closed, got " + e.getPayload());
        }
        if (e.getEventId().isEmpty()) {
          throw new IllegalStateException("red1: unannotated fields must survive sanitization: " + e);
        }
      }

      // PAGE (listEventsPaged): every page item redacted on the wire.
      var page = api.listEventsPagedPage(Contractclient.ContractPageRequest.newBuilder()
          .setTenantNamespace("tenant.alpha")
          .setPageSize(10)
          .build());
      if (page.items.isEmpty()) {
        throw new IllegalStateException("red1: PAGE egress returned no items");
      }
      for (Contractclient.ContractEvent e : page.items) {
        if (!e.getPayload().isEmpty() || !e.getPayloadRedacted()) {
          throw new IllegalStateException("red1: PAGE egress must redact payload fail-closed, got " + e.getPayload());
        }
      }

      // WATCH: the snapshot item is redacted on the wire.
      List<Contractclient.ContractWatchEvent> watched = collectWatch(api, validWatchRequest());
      if (watched.isEmpty() || !watched.get(0).hasSnapshotItem()) {
        throw new IllegalStateException("red1: WATCH egress returned no snapshot item: " + watched);
      }
      Contractclient.ContractEvent snap = watched.get(0).getSnapshotItem();
      if (!snap.getPayload().isEmpty() || !snap.getPayloadRedacted()) {
        throw new IllegalStateException("red1: WATCH snapshot egress must redact payload fail-closed, got " + snap.getPayload());
      }
    } finally {
      channel.shutdownNow();
      channel.awaitTermination(5, TimeUnit.SECONDS);
      server.shutdownNow();
      server.awaitTermination(5, TimeUnit.SECONDS);
    }
  }
}
EOF
}

write_java_c2d_consumer() {
  local file="$1"
  cat > "$file" <<'EOF'
package consumer;

import com.gitlab.example.tacplus.consumer.contractclient.model.c2dm.ServiceManager;
import com.gitlab.example.tacplus.consumer.contractclient.model.types.contract.contractclient.Contractclient;
import com.gitlab.example.tacplus.consumer.contractclient.model.types.contract.contractclient.Helpers;
import io.grpc.ManagedChannel;
import io.grpc.ManagedChannelBuilder;

public final class Consumer {
  public static void main(String[] args) throws Exception {
    Contractclient.ContractRequest req = Contractclient.ContractRequest.newBuilder()
        .setTenantNamespace("tenant.alpha")
        .setItemId("item-123")
        .setMode(Contractclient.ContractMode.CONTRACT_MODE_READ)
        .build();
    Helpers.validateContractRequest(req);

    ManagedChannel channel = ManagedChannelBuilder.forTarget("unix:/tmp/tacplus-consumer-contract.sock").usePlaintext().build();
    try (ServiceManager sm = new ServiceManager(channel)) {
      if (sm.grpc().contract().contractDaemon() == null) {
        throw new IllegalStateException("missing ContractDaemon client");
      }
    }
  }
}
EOF
}

compile_java_handwritten_consumer() {
  local output="$1"
  local case_name="$2"
  local compile_dir="$3"
  local log_dir
  log_dir="$(dirname "$output")"

  local consumer_dir="$log_dir/java-consumer"
  rm -rf "$consumer_dir"
  mkdir -p "$consumer_dir/src/main/java/consumer" "$consumer_dir/classes"

  case "$case_name" in
    c2s)
      write_java_c2s_consumer "$consumer_dir/src/main/java/consumer/Consumer.java"
      ;;
    w2s)
      write_java_w2s_consumer "$consumer_dir/src/main/java/consumer/Consumer.java"
      ;;
    both)
      write_java_both_consumer "$consumer_dir/src/main/java/consumer/Consumer.java"
      ;;
    c2d)
      write_java_c2d_consumer "$consumer_dir/src/main/java/consumer/Consumer.java"
      ;;
    *)
      return
      ;;
  esac

  local java_env
  java_env="$(java_home_env)"
  if [[ -n "$java_env" ]]; then
    (cd "$compile_dir" && env "$java_env" mvn -q -DincludeScope=compile dependency:build-classpath -Dmdep.outputFile="$consumer_dir/classpath.txt") >"$log_dir/java-consumer-classpath.log" 2>&1 || {
      cat "$log_dir/java-consumer-classpath.log" >&2
      return 1
    }
  else
    (cd "$compile_dir" && mvn -q -DincludeScope=compile dependency:build-classpath -Dmdep.outputFile="$consumer_dir/classpath.txt") >"$log_dir/java-consumer-classpath.log" 2>&1 || {
      cat "$log_dir/java-consumer-classpath.log" >&2
      return 1
    }
  fi
  assert_clean_log "$log_dir/java-consumer-classpath.log"

  local cp
  local javac_cmd="javac"
  local java_cmd="java"
  if [[ -n "$java_env" ]]; then
    local java_home="${java_env#JAVA_HOME=}"
    javac_cmd="$java_home/bin/javac"
    java_cmd="$java_home/bin/java"
  fi
  cp="$compile_dir/target/classes:$(cat "$consumer_dir/classpath.txt")"
  "$javac_cmd" -Xlint:all -Werror --release 17 -cp "$cp" -d "$consumer_dir/classes" "$consumer_dir/src/main/java/consumer/Consumer.java" >"$log_dir/java-consumer-javac.log" 2>&1 || {
    cat "$log_dir/java-consumer-javac.log" >&2
    return 1
  }
  assert_clean_log "$log_dir/java-consumer-javac.log"

  if [[ "$case_name" != c2d ]]; then
    cat > "$consumer_dir/logback-quiet.xml" <<'EOF'
<configuration>
  <root level="ERROR"/>
</configuration>
EOF
    # --sun-misc-unsafe-memory-access=allow: protobuf-java's UnsafeUtil still uses
    # sun.misc.Unsafe; JDK 24+ warns on first use and the zero-warning gate would
    # trip on that environment noise.
    "$java_cmd" --sun-misc-unsafe-memory-access=allow -Dlogback.configurationFile="$consumer_dir/logback-quiet.xml" -cp "$consumer_dir/classes:$cp" consumer.Consumer >"$log_dir/java-consumer-run.log" 2>&1 || {
      cat "$log_dir/java-consumer-run.log" >&2
      return 1
    }
    assert_clean_log "$log_dir/java-consumer-run.log"
  fi
}

compile_generated_output() {
  local language="$1"
  local output="$2"
  local case_name="${3:-}"
  local log_dir
  log_dir="$(dirname "$output")"
  case "$language" in
    go)
      write_go_compile_mod "$log_dir/go.mod"
      if [[ "$case_name" == both && -f "$output/s2sm/s2sm.go" ]]; then
        write_go_s2sm_startup_invariant_test "$output/s2sm/startup_invariant_test.go"
        write_go_s2sm_eventbus_publisher_runtime_test "$output/s2sm/eventbus_publisher_runtime_test.go"
        write_go_s2sm_eventbus_subscriber_runtime_test "$output/s2sm/eventbus_subscriber_runtime_test.go"
        write_go_s2sm_diskstore_failclosed_test "$output/s2sm/diskstore_failclosed_test.go"
      fi
      # Race detector on the compiled generated output (see the consumer_test
      # note above); CGO_ENABLED=1 is required by -race.
      (cd "$log_dir" && go mod tidy && CGO_ENABLED=1 SPIFFE_VECTOR_FIXTURE="$ROOT_DIR/cross-test/fixtures/spiffe_vectors.json" go test -race -timeout 20m ./...) >"$log_dir/go-test.log" 2>&1 || {
        cat "$log_dir/go-test.log" >&2
        return 1
      }
      assert_clean_log "$log_dir/go-test.log"
      # Client-surface cases emit client_im wired to the C2S mint. `disabled` has
      # no client surface, so it emits no client_im.
      if [[ "$case_name" != disabled && -f "$output/chassis/client_im/manager.go" ]]; then
        assert_client_im_mint_path go "$output"
      fi
      compile_go_handwritten_consumer "$output" "$case_name"
      ;;
    rust)
      if [[ -f "$output/Cargo.toml" ]]; then
        (cd "$output" && cargo check) >"$log_dir/cargo-check.log" 2>&1 || {
          cat "$log_dir/cargo-check.log" >&2
          return 1
        }
        assert_clean_log "$log_dir/cargo-check.log"
        run_rust_spiffe_self_tests "$output" "$log_dir"
        compile_rust_handwritten_consumer "$output" "$case_name"
      fi
      ;;
    python)
      if [[ -n "$PYTHON_BIN" ]]; then
        "$PYTHON_BIN" -m compileall -q "$output" >"$log_dir/python-compileall.log" 2>&1 || {
          cat "$log_dir/python-compileall.log" >&2
          return 1
        }
      else
        python3 -m compileall -q "$output" >"$log_dir/python-compileall.log" 2>&1 || {
          cat "$log_dir/python-compileall.log" >&2
          return 1
        }
      fi
      assert_clean_log "$log_dir/python-compileall.log"
      assert_client_im_mint_path python "$output"
      compile_python_handwritten_consumer "$output" "$case_name"
      ;;
    typescript)
      if [[ -f "$output/package.json" ]]; then
        # --loglevel=error suppresses npm's environment-specific install-policy
        # notices (e.g. npm 11's "npm warn allow-scripts" about a dependency's
        # postinstall script) which are unrelated to generated code. Real install
        # failures still error out; `npm run build` (tsc) output is unaffected, so
        # genuine TS errors are still caught by assert_clean_log below.
        (cd "$output" && npm install --loglevel=error && npm run build) >"$log_dir/npm-build.log" 2>&1 || {
          cat "$log_dir/npm-build.log" >&2
          return 1
        }
        assert_clean_log "$log_dir/npm-build.log"
        assert_client_im_mint_path typescript "$output"
        assert_typescript_entrypoints_built "$output" "$log_dir"
        compile_typescript_handwritten_consumer "$output" "$case_name"
      fi
      ;;
    java)
      if command -v mvn >/dev/null; then
        local compile_dir="$log_dir/java-compile"
        rm -rf "$compile_dir"
        mkdir -p "$compile_dir"
        write_java_compile_pom "$compile_dir/pom.xml" "$output"
        local java_env
        java_env="$(java_home_env)"
        if [[ -n "$java_env" ]]; then
          (cd "$compile_dir" && env "$java_env" mvn -q -DskipTests compile) >"$log_dir/maven-compile.log" 2>&1 || {
            cat "$log_dir/maven-compile.log" >&2
            return 1
          }
        else
          (cd "$compile_dir" && mvn -q -DskipTests compile) >"$log_dir/maven-compile.log" 2>&1 || {
            cat "$log_dir/maven-compile.log" >&2
            return 1
          }
        fi
        assert_clean_log "$log_dir/maven-compile.log"
        # Client-surface cases emit client_im wired to the C2S mint. `disabled`
        # has no client surface, so it emits no client_im.
        if [[ "$case_name" != disabled ]] &&
           find "$output/src/main/java" -type f -path '*/chassis/client_im/ClientIdentityManager.java' -print -quit | rg -q .; then
          assert_client_im_mint_path java "$output"
        fi
        run_java_generated_self_tests "$output" "$compile_dir" "$log_dir"
        compile_java_handwritten_consumer "$output" "$case_name" "$compile_dir"
      else
        die "mvn is required to compile generated Java output"
      fi
      ;;
    swift)
      if [[ -f "$output/Package.swift" ]]; then
        if ! command -v swift >/dev/null; then
          case "${TACPLUS_ALLOW_MISSING_SWIFT:-}" in
            1|true|TRUE|yes|YES)
              echo "==> swift compiler not found; TACPLUS_ALLOW_MISSING_SWIFT set, skipping swift build"
              return
              ;;
            *)
              die "swift compiler is required to compile generated Swift output; set TACPLUS_ALLOW_MISSING_SWIFT=1 only in CI images that intentionally lack swift"
              ;;
          esac
        fi
        (cd "$output" && swift build) >"$log_dir/swift-build.log" 2>&1 || {
          cat "$log_dir/swift-build.log" >&2
          return 1
        }
        assert_clean_log "$log_dir/swift-build.log"
      fi
      ;;
    csharp)
      if find "$output" -type f -name '*.csproj' -print -quit | grep -q .; then
        if ! command -v dotnet >/dev/null; then
          case "${TACPLUS_ALLOW_MISSING_DOTNET:-}" in
            1|true|TRUE|yes|YES)
              echo "==> dotnet not found; TACPLUS_ALLOW_MISSING_DOTNET set, skipping dotnet build"
              return
              ;;
            *)
              die "dotnet is required to compile generated C# output; set TACPLUS_ALLOW_MISSING_DOTNET=1 only in CI images that intentionally lack dotnet"
              ;;
          esac
        fi
        (cd "$output" && dotnet build --nologo) >"$log_dir/dotnet-build.log" 2>&1 || {
          cat "$log_dir/dotnet-build.log" >&2
          return 1
        }
        assert_clean_log "$log_dir/dotnet-build.log"
      fi
      ;;
  esac
}

assert_clean_log() {
  local log="$1"
  if [[ ! -s "$log" ]]; then
    return
  fi
  if rg -n '(WARN|WARNING|warning:|npm warn|ERROR|panic:|fatal:|error TS[0-9]+|JsonWebKey)' "$log"; then
    die "warning/error output found in $log"
  fi
}

# Generic guard for the OF-29 class: for every generated workspace package,
# the file named by its package.json `main`/`types` must exist after the build.
# Generated code imports siblings by package name; node resolves those through
# each package's `main`, so a `main` that points at a file the build never
# emits is a runtime ERR_MODULE_NOT_FOUND waiting to happen. This catches the
# whole class categorically, regardless of which build layout the generator
# chooses.
assert_typescript_entrypoints_built() {
  local output="$1"
  local log_dir="$2"
  local checker="$log_dir/check-entrypoints.cjs"
  cat > "$checker" <<'EOF'
const fs = require('fs');
const path = require('path');
const root = process.argv[2];
const skip = new Set(['node_modules', 'dist', '.git']);
const failures = [];
function walk(dir) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    if (e.isDirectory() && !skip.has(e.name)) walk(path.join(dir, e.name));
  }
  const pj = path.join(dir, 'package.json');
  if (dir !== root && fs.existsSync(pj)) {
    let m;
    try { m = JSON.parse(fs.readFileSync(pj, 'utf8')); } catch (e) {
      failures.push(`${path.relative(root, pj)}: unparseable (${e.message})`);
      return;
    }
    for (const field of ['main', 'types']) {
      if (!m[field]) continue;
      const target = path.resolve(dir, m[field]);
      if (!fs.existsSync(target)) {
        failures.push(`${path.relative(root, pj)}: ${field}="${m[field]}" -> missing ${path.relative(root, target)}`);
      }
    }
  }
}
walk(root);
if (failures.length) {
  console.error('generated workspace package entrypoints missing after build:');
  for (const f of failures) console.error('  ' + f);
  process.exit(1);
}
console.log(`ok: all workspace package main/types entrypoints resolve to built files`);
EOF
  if ! (cd "$output" && node "$checker" "$output") >"$log_dir/entrypoints.log" 2>&1; then
    cat "$log_dir/entrypoints.log" >&2
    die "TypeScript workspace package entrypoints do not resolve to built files (OF-29 class)"
  fi
}

run_generation_case() {
  local language="$1"
  local case_name="$2"
  local dir="$WORK_ROOT/$language/$case_name"
  local output="$dir/model"
  local log="$dir/build.log"
  local proto_src="$PROTO_SRC"

  # Client surfaces are now proto-driven: each case gets a proto that declares
  # ONLY its expected surface(s). The obsolete clients.*.enabled YAML flags are
  # rejected by the runner, so per-case selection lives entirely in the proto.
  #
  # Full-client targets (python/typescript) generate from the synthetic
  # client-only proto for the positive cases; `disabled` keeps the FULL canonical
  # proto so the service-side rejection path stays covered (PAR-2/PAR-3).
  # Full-service targets (go/rust/java) and swift get a per-case copy of the rich
  # canonical fixture tree with contract.proto rewritten for the case, so
  # server-side fixture/eventbus coverage stays meaningful.
  if is_python_typescript_client_only "$language" && is_positive_client_case "$language" "$case_name"; then
    proto_src="$dir/client-only-proto"
    prepare_client_only_proto "$proto_src" "$case_name" "$language"
  elif is_full_service_language "$language" || [[ "$language" == swift || "$language" == csharp ]]; then
    proto_src="$dir/case-proto"
    prepare_full_service_case_proto "$proto_src" "$case_name"
  fi

  # Swift is a client-lite target and is now proto-driven: it emits C2D, C2S and
  # W2S client surfaces per the proto declarations. The per-case proto declares
  # the matching surface(s) for c2s/w2s/both/c2d, so swift generates + compiles
  # for those cases. The `disabled` case declares no client surface at all, so
  # swift must still reject (no C2D/C2S/W2S services resolved).
  if [[ "$language" == swift && "$case_name" == disabled ]]; then
    echo "==> $language/$case_name: expecting swift no-client-surface rejection"
    write_config "$dir" "$language" "$case_name" "$proto_src"
    if (cd "$dir" && TACPLUS_PYTHON="$PYTHON_BIN" "$TACPLUS_BIN" build --config tacplus.yaml --src "$proto_src" --tag latest >"$log" 2>&1); then
      die "swift unexpectedly accepted $case_name (no client surface declared)"
    fi
    rg -n 'swift lite found no service_c2d services' "$log" >/dev/null || {
      cat "$log" >&2
      die "swift rejection did not include the expected no-client-surface message"
    }
    return
  fi

  # C# is a client-lite target with the same proto-driven surface model as swift:
  # C2D, C2S and W2S per the proto declarations. The `disabled` case declares no
  # client surface at all, so csharp must reject (no service resolved).
  if [[ "$language" == csharp && "$case_name" == disabled ]]; then
    echo "==> $language/$case_name: expecting csharp no-client-surface rejection"
    write_config "$dir" "$language" "$case_name" "$proto_src"
    if (cd "$dir" && TACPLUS_PYTHON="$PYTHON_BIN" "$TACPLUS_BIN" build --config tacplus.yaml --src "$proto_src" --tag latest >"$log" 2>&1); then
      die "csharp unexpectedly accepted $case_name (no client surface declared)"
    fi
    rg -n 'csharp lite found no service_c2d services' "$log" >/dev/null || {
      cat "$log" >&2
      die "csharp rejection did not include the expected no-client-surface message"
    }
    return
  fi

  if is_python_typescript_client_only "$language" && ! is_positive_client_case "$language" "$case_name"; then
    echo "==> $language/$case_name: expecting client-only rejection"
    write_config "$dir" "$language" "$case_name" "$proto_src"
    if (cd "$dir" && TACPLUS_PYTHON="$PYTHON_BIN" "$TACPLUS_BIN" build --config tacplus.yaml --src "$proto_src" --tag latest >"$log" 2>&1); then
      die "$language unexpectedly accepted service-side case $case_name"
    fi
    # python and typescript are full-client targets: they reject service-side/runtime
    # generation (the full contract proto carries it) with the same message. The
    # positive client cases run above from the client-only proto.
    local expected_rejection="$language is a client-only target; only client generation is supported"
    rg -n "$expected_rejection" "$log" >/dev/null || {
      cat "$log" >&2
      die "$language rejection did not include the expected client-only message"
    }
    return
  fi

  echo "==> $language/$case_name: generating"
  write_config "$dir" "$language" "$case_name" "$proto_src"
  if ! (cd "$dir" && TACPLUS_PYTHON="$PYTHON_BIN" "$TACPLUS_BIN" build --config tacplus.yaml --src "$proto_src" --tag latest >"$log" 2>&1); then
    cat "$log" >&2
    die "generation failed for $language/$case_name"
  fi

  # EnforceVerbs fails generation for non-canonical RPC names, so any warning or
  # error output that reaches this point is a harness regression.
  if rg -n '(WARN|WARNING|warning:|ERROR|panic:|fatal:|error TS[0-9]+|JsonWebKey)' "$log"; then
    die "warning/error output found for $language/$case_name"
  fi

  if [[ "$language" == swift ]]; then
    [[ "$(case_enabled "$case_name" c2d)" == true ]] && assert_swift_c2d_surface "$output"
    [[ "$(case_enabled "$case_name" c2s)" == true ]] && assert_swift_c2s_surface "$output"
    [[ "$(case_enabled "$case_name" w2s)" == true ]] && assert_swift_w2s_surface "$output"
  elif [[ "$language" == csharp ]]; then
    [[ "$(case_enabled "$case_name" c2d)" == true ]] && assert_csharp_c2d_surface "$output"
    [[ "$(case_enabled "$case_name" c2s)" == true ]] && assert_csharp_c2s_surface "$output"
    [[ "$(case_enabled "$case_name" w2s)" == true ]] && assert_csharp_w2s_surface "$output"
  else
    assert_surface "$output" c2sm "$(case_enabled "$case_name" c2s)"
    assert_surface "$output" w2sm "$(case_enabled "$case_name" w2s)"
    assert_surface "$output" c2dm "$(case_enabled "$case_name" c2d)"
  fi
  assert_no_stale_generated_names "$output"
  # Full-service langs now build from a per-case copy of the canonical fixture
  # tree (proto_src = $dir/case-proto, not $PROTO_SRC), but the rich services/
  # vservices/ fixtures are copied verbatim, so fixture/eventbus coverage stays
  # meaningful. Gate on the language, not the proto path.
  if is_full_service_language "$language"; then
    assert_fixture_coverage "$output"
  fi
  if [[ "$(case_enabled "$case_name" w2s)" == true ]]; then
    assert_w2s_validation_wiring "$language" "$output"
  fi
  if [[ "$(case_enabled "$case_name" c2s)" == true ]]; then
    assert_service_wrapper_validation_wiring "$language" "$output"
  fi
  assert_service_manager_auth_wiring "$language" "$output"
  if is_full_service_language "$language"; then
    assert_eventbus_subscriber_meta_wiring "$language" "$output"
  fi
  assert_no_rust_legacy_publishers "$language" "$output"

  echo "==> $language/$case_name: compiling generated output when supported"
  compile_generated_output "$language" "$output" "$case_name"
}

run_missing_token_minter_case() {
  local language="$1"
  local dir="$WORK_ROOT/$language/missing-token-minter"
  local proto_src="$dir/proto"
  local log="$dir/build.log"

  echo "==> $language/missing-token-minter: expecting S2S generation rejection"
  prepare_missing_token_minter_proto "$proto_src"
  write_config "$dir" "$language" both "$proto_src"
  if (cd "$dir" && TACPLUS_PYTHON="$PYTHON_BIN" "$TACPLUS_BIN" build --config tacplus.yaml --src "$proto_src" --tag latest >"$log" 2>&1); then
    die "$language unexpectedly accepted S2S generation without an authoritative token minter"
  fi
  rg -n 'no authoritative \(spec\.token_minter\)' "$log" >/dev/null || {
    cat "$log" >&2
    die "$language missing-token-minter rejection did not mention the authoritative token minter"
  }
}

echo "==> Consumer contract workdir: $WORK_ROOT"
echo "==> Consumer contract proto: $PROTO_SRC"

for language in "${languages[@]}"; do
  if [[ "$language" == python ]]; then
    ensure_python_codegen_env
    break
  fi
done

for language in "${languages[@]}"; do
  if [[ "$language" == swift ]] && ! swift_toolchain_available; then
    echo "==> swift: SKIPPED (protoc-gen-swift / protoc-gen-grpc-swift-2 not found; CI runs this leg)"
    continue
  fi
  for case_name in "${cases[@]}"; do
    run_generation_case "$language" "$case_name"
  done
  if [[ -z "$CASE_FILTER" ]] && is_full_service_language "$language"; then
    run_missing_token_minter_case "$language"
  fi
done

echo "==> Consumer contract passed"
