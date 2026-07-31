#!/usr/bin/env bash
# SMB (Service Mesh Blackbox, SPIFFE plan D7) end-to-end generation check.
#
# Why this is separate from cross-test/current-generation: that harness builds
# with a dummy pkg ("...dummy.proto") which resolves SelfPackage="proto", matching
# no real service package — so SelfPackage-gated ServiceManager features (the SMB
# member-cert seam, like auto_tls's SM side) are never exercised there. This
# fixture builds with a REAL pkg so HasSmb fires for the (spec.smb) service.
#
# Default verifies Go fully (generate -> assert seam -> compile), which needs only
# Go + protoc. Other languages use the same recipe via --language <lang>; their
# generated output is asserted for the seam and compiled with that language's
# toolchain (rust: cargo, java: mvn, typescript: tsc, python: grpc_tools venv).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PROTO_SRC="$ROOT_DIR/cross-test/smb/proto"
LANGUAGE="go"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --language) LANGUAGE="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

die() { echo "SMB check FAILED: $*" >&2; exit 1; }

TACPLUS_BIN="${TACPLUS_BIN:-$ROOT_DIR/target/tacplus-smb-check}"
if [[ ! -x "$TACPLUS_BIN" || -z "${TACPLUS_BIN_PREBUILT:-}" ]]; then
  echo "==> Building tacplus binary"
  (cd "$ROOT_DIR" && go build -o "$TACPLUS_BIN" .)
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
cat > "$WORK/tacplus.yaml" <<EOF
language: "$LANGUAGE"
pkg: "com.example.mysmb"
src: "$PROTO_SRC"
output-dir: "tacplus"
EOF

echo "==> Generating SMB fixture ($LANGUAGE)"
(cd "$WORK" && TACPLUS_PYTHON="${TACPLUS_PYTHON:-python3}" "$TACPLUS_BIN" build --config tacplus.yaml --src "$PROTO_SRC") \
  || die "generation failed for $LANGUAGE"

OUT="$WORK/tacplus/model"
[[ -d "$OUT" ]] || die "no generated output at $OUT"

echo "==> Asserting language-neutral SMB markers"
grep -q '"smb": *true' "$OUT/registry.json"                         || die "registry.json missing \"smb\": true"
grep -q '"smb_root": *"SMB_ROOT_ISOLATED"' "$OUT/registry.json"      || die "registry.json missing isolated smb_root"
grep -q '"smb_root": *"SMB_ROOT_CLUSTER"' "$OUT/registry.json"       || die "registry.json missing cluster smb_root"
grep -q '"smb_authority": *true' "$OUT/registry.json"               || die "registry.json missing \"smb_authority\": true"
find "$OUT" -path '*sys/smbcert*' | grep -q .                      || die "generated SmbCertService client (sys/smbcert) missing"

echo "==> Asserting $LANGUAGE SMB seam"
case "$LANGUAGE" in
  go)
    grep -rq "smbCertStreamLoop" "$OUT"      || die "go SM seam (smbCertStreamLoop) missing"
    grep -rq "smbAuthorityConn.Conn" "$OUT"  || die "go SMB stream does not use declared authority connection"
    grep -rq "NewSmbCert" "$OUT"             || die "go handler NewSmbCert missing"
    grep -rq "ManageSmbCert" "$OUT"          || die "go SmbCertService client (ManageSmbCert) missing"
    ;;
  rust)
    grep -rq "run_smb_cert_stream\|new_smb_cert" "$OUT" || die "rust SMB seam missing"
    grep -rq "smb_authority_conn.clone()" "$OUT"        || die "rust SMB stream does not use declared authority connection"
    ;;
  java)
    grep -rq "runSmbCertStream\|newSmbCert" "$OUT"      || die "java SMB seam missing"
    grep -rq "smbCertStreamLoop(this, smbAuthorityConn" "$OUT" || die "java SMB stream does not use declared authority connection"
    ;;
  typescript) grep -rq "runSmbCertStream\|newSmbCert" "$OUT"      || die "typescript SMB seam missing" ;;
  python)     grep -rq "_run_smb_cert_stream\|new_smb_cert" "$OUT" || die "python SMB seam missing" ;;
  *) die "unsupported --language $LANGUAGE" ;;
esac

# The black-box config must never carry the true root: the only trust anchor in
# the seam comes from the smbcert delivery's per-SMB `ca`. (Contract invariant is
# also pinned by pkg/internal/common/parser/smb_contract_test.go.)
echo "==> SMB seam generated and markers present for $LANGUAGE"

if [[ "$LANGUAGE" == "go" ]]; then
  echo "==> Compiling generated Go"
  # Module path matches the generated ModuleBasePath (pkg com.example.mysmb ->
  # example.com/mysmb, output under tacplus/model).
  cat > "$OUT/go.mod" <<'EOF'
module example.com/mysmb/tacplus/model

go 1.23

require (
	github.com/dsnet/compress v0.0.2-0.20230904184137-39efe44ab707
	github.com/gin-gonic/gin v1.11.0
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/pelletier/go-toml/v2 v2.2.4
	github.com/soheilhy/cmux v0.1.5
	go.uber.org/zap v1.27.1
	golang.org/x/sync v0.19.0
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260209200024-4cfbd4190f57
	google.golang.org/grpc v1.78.0
	google.golang.org/protobuf v1.36.11
	gopkg.in/yaml.v3 v3.0.1
)
EOF
  (cd "$OUT" && go mod tidy && go build ./...) || die "generated Go does not compile"
  echo "==> Generated Go compiles"
fi

echo "==> SMB check PASSED ($LANGUAGE)"
