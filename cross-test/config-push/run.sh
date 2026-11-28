#!/usr/bin/env bash
# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# Config push wire gate (#927): a tenant's record store pushes its config
# slice to the slice's owner over configpush.ConfigPush, the two generated
# apart from one fleet (cross-test/proto-configpush) and meeting only on the
# wire.
#
# go: generates stratum (a tenant-owned (spec.record_store) with config_push,
# from its own proto alone: tenancy's proto is not in its run) and tenancy (a
# /system service owning the pushed slice TenancyTenantSettings), each its own
# Go module. testdata/go/store/configpush_wire.go goes into stratum's s2sm
# package, handing out the store's generated owners table, publisher and
# feed; testdata/go/owner/configpush_wire_test.go goes into tenancy's s2sm
# package and runs the owner's generated receiver on a TCP listener against
# them, with -race: the snapshot, each version at the record's revision, a
# delete, the owner's restart (memory only: the publisher sends its snapshot
# again), and the refusals of another caller and of an invalid value. The
# fleet's token is the test's own: the owner takes a stream's bearer as its
# subject.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GATE_DIR="$ROOT_DIR/cross-test/config-push"
PROTO_SRC="$ROOT_DIR/cross-test/proto-configpush"
TESTDATA="$GATE_DIR/testdata"
LANGUAGE="go"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --language) LANGUAGE="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

die() { echo "config-push check FAILED: $*" >&2; exit 1; }

[[ "$LANGUAGE" == "go" ]] || die "no $LANGUAGE leg (go only)"
command -v go >/dev/null || die "go is required"

WORK_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/config-push.XXXXXX")"
cleanup() { rm -rf "$WORK_ROOT"; }
trap cleanup EXIT

TACPLUS_BIN="${TACPLUS_BIN:-$WORK_ROOT/tacplus-local}"
if [[ ! -x "$TACPLUS_BIN" ]]; then
  echo "==> Building local tacplus binary"
  (cd "$ROOT_DIR" && go build -o "$TACPLUS_BIN" .)
fi

# generate SELF SIDE: one side of the fleet, built from its own proto tree
# (cross-test/proto-configpush/SIDE), the fleet's registry beside it.
generate() {
  local self="$1"
  local side="$2"
  local dir="$WORK_ROOT/$self"
  mkdir -p "$dir"
  cat > "$dir/tacplus.yaml" <<EOF
language: "go"
pkg: "com.gitlab.example.dummy.$self"
src: "$PROTO_SRC/$side"
output-dir: "tacplus"
EOF
  [[ "$self" == "stratum" ]] && echo 'records: "all"' >> "$dir/tacplus.yaml"
  echo "==> Generating go $self"
  (cd "$dir" && "$TACPLUS_BIN" build --config tacplus.yaml --src "$PROTO_SRC/$side") >"$dir/generate.log" 2>&1 || {
    cat "$dir/generate.log" >&2
    die "go generation failed ($self)"
  }
}

# write_go_mod FILE MODULE [REQUIRE...]: the pins of the consumer-contract go
# compile module (as cross-test/trace-carry's); the generation's tacplus/ is the
# module root, model/ a package tree inside it; each REQUIRE is a sibling
# side's module, replaced by its directory.
write_go_mod() {
  local file="$1" module="$2"
  shift 2
  cat > "$file" <<EOF
module $module

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
	golang.org/x/crypto v0.58.1-0.20261009181718-2f1f834330b2
	golang.org/x/net v0.62.0
	golang.org/x/sync v0.19.0
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa
	google.golang.org/grpc v1.83.2
	google.golang.org/protobuf v1.36.11
	gopkg.in/yaml.v3 v3.0.1
)
EOF
  local side
  for side in "$@"; do
    printf 'require gitlab.com/example/dummy/%s/tacplus v0.0.0\nreplace gitlab.com/example/dummy/%s/tacplus => ../../%s/tacplus\n' \
      "$side" "$side" "$side" >> "$file"
  done
}

generate stratum store
generate tenancy owner

store="$WORK_ROOT/stratum/tacplus"
owner="$WORK_ROOT/tenancy/tacplus"
# The store's run never holds the owner's proto: it knows tenancy only from
# the registry.
[[ -f "$store/model/s2sm/configpush_publisher.go" ]] || die "stratum holds no config push publisher"
grep -qF '1: "tenancy",' "$store/model/s2sm/configpush_publisher.go" || die "stratum's owners table does not give tenancy field 1"
! grep -rqs 'TenancyTenantSettings' "$store/model" || die "stratum's run holds tenancy's slice message"
[[ -f "$owner/model/s2sm/configpush.go" ]] || die "tenancy serves no config push receiver"

write_go_mod "$store/go.mod" "gitlab.com/example/dummy/stratum/tacplus"
write_go_mod "$owner/go.mod" "gitlab.com/example/dummy/tenancy/tacplus" stratum
cp "$TESTDATA/go/store/configpush_wire.go" "$store/model/s2sm/configpush_wire.go"
cp "$TESTDATA/go/owner/configpush_wire_test.go" "$owner/model/s2sm/configpush_wire_test.go"

echo "==> Running the store's publisher against the owner's receiver over TCP"
(cd "$store" && go mod tidy) >"$WORK_ROOT/store-tidy.log" 2>&1 || {
  cat "$WORK_ROOT/store-tidy.log" >&2
  die "go mod tidy (stratum)"
}
# Both generations register the same sys/ protos (sys/errors.proto, ...), which
# a process of the fleet never does: each side runs alone. One test binary
# holds both, so protobuf-go is told to warn of it, not panic; each side's
# messages keep their own descriptors.
(cd "$owner" && go mod tidy && GOLANG_PROTOBUF_REGISTRATION_CONFLICT=warn go test -count=1 -race -v -run '^TestConfigPushWire' ./model/s2sm) >"$WORK_ROOT/test.log" 2>&1 || {
  cat "$WORK_ROOT/test.log" >&2
  die "go wire test"
}
grep -E '^(=== RUN|--- |ok|PASS)' "$WORK_ROOT/test.log"
echo "config-push go: PASS"
