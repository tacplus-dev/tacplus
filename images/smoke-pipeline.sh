#!/usr/bin/env bash
# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# The build image's pipeline smoke (#193): generate, test, verify and fuzz, the
# way a repository's build runs them, through the image's tacplus launcher.
#
# tacplus is built from the mounted checkout and published as a local release
# (file:// URLs, binary plus checksums.txt) of the image's line, so the smoke
# needs no published release; a pinned repository (tacplus.lock) of the base
# fixture (cross-test/proto) is then, per language:
#   go:   generated, tested (go test), verified (`tacplus verify` reports Go as
#         skipped, by design) and fuzzed (go test -fuzz);
#   rust: generated, tested (cargo test) and fuzzed (cargo fuzz), and
#         `tacplus verify` runs Kani on a harness of the repository's own.
#
# Usage, from the checkout:
#   docker run --rm -v "$PWD:/src:ro" <build image> bash /src/images/smoke-pipeline.sh [go|rust]...
set -euo pipefail

SRC="${TACPLUS_SMOKE_SRC:-/src}"
FUZZ_SECONDS="${TACPLUS_SMOKE_FUZZ_SECONDS:-10}"
languages=("$@")
[[ ${#languages[@]} -gt 0 ]] || languages=(go rust)
W="$(mktemp -d)"
trap 'rm -rf "$W"' EXIT
step() { echo "==> $*"; }
die() {
  echo "smoke-pipeline: $*" >&2
  exit 1
}

[[ "$(head -n 1 "$(command -v tacplus)")" == '#!/bin/sh' ]] || die "tacplus on PATH is not the launcher"
version="${TACPLUS_LINE:-0.0}.0"
case "$(uname -m)" in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; *) die "unsupported $(uname -m)" ;; esac

step "building tacplus $version from $SRC as a local release"
mkdir -p "$W/tacplus-src" "$W/forge/download/v$version"
tar -C "$SRC" --exclude=./target --exclude=./.git -cf - . | tar -C "$W/tacplus-src" -xf -
asset="tacplus-${version//./-}-linux-$arch"
(cd "$W/tacplus-src" && go generate ./pkg/internal/proto/spec/... &&
  CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/tacplus-dev/tacplus/pkg/buildinfo.Version=$version" \
    -o "$W/forge/download/v$version/$asset" .)
(cd "$W/forge/download/v$version" && sha256sum "$asset" >checksums.txt)
printf '[{"tag_name": "v%s", "draft": false}]\n' "$version" >"$W/forge/releases.json"
export TACPLUS_RELEASES_URL="file://$W/forge/releases.json"
export TACPLUS_DOWNLOAD_URL="file://$W/forge/download"
export TACPLUS_CACHE="$W/cache"

# repository LANGUAGE: a pinned repository of the base fixture; prints its path.
repository() {
  local dir="$W/$1"
  mkdir -p "$dir"
  cp -R "$SRC/cross-test/proto" "$dir/proto"
  printf 'language: %s\npkg: com.gitlab.example.dummy.proto\nsrc: proto\noutput-dir: model\n' "$1" >"$dir/tacplus.yaml"
  printf 'tacplus: %s\nbuild_image: ghcr.io/tacplus-dev/tacplus/build:%s@sha256:%s\n' \
    "$version" "$version" "$(printf '0%.0s' {1..64})" >"$dir/tacplus.lock"
  echo "$dir"
}

smoke_go() {
  local dir
  dir="$(repository go)"
  cd "$dir"
  step "go: generate"
  tacplus build --config tacplus.yaml --src proto --tag latest >generate.log 2>&1 || {
    tail -50 generate.log
    die "go generation failed"
  }
  grep -q "tacplus version $version" <(tacplus version) || die "the launcher did not run the pinned $version"
  printf 'module gitlab.com/example/dummy/proto\n\ngo 1.23\n' >go.mod
  step "go: test"
  go mod tidy >/dev/null 2>&1 || die "go mod tidy failed"
  go vet ./model/... || die "go vet failed"
  go test -count=1 ./model/... >test.log 2>&1 || {
    tail -50 test.log
    die "go test failed"
  }
  step "go: verify"
  tacplus verify | tee verify.log
  grep -q 'go: skipped' verify.log || die "verify did not report Go as skipped"
  step "go: fuzz (${FUZZ_SECONDS}s)"
  cat >model/types/sys/ip/smoke_fuzz_test.go <<'EOF'
package ip

import (
	"testing"

	"google.golang.org/protobuf/proto"
)

func FuzzIPPortRoundTrip(f *testing.F) {
	f.Add([]byte{0x08, 0x50})
	f.Fuzz(func(t *testing.T, data []byte) {
		var m IPPort
		if proto.Unmarshal(data, &m) != nil {
			return
		}
		out, err := proto.Marshal(&m)
		if err != nil {
			t.Fatal(err)
		}
		var back IPPort
		if err := proto.Unmarshal(out, &back); err != nil || !proto.Equal(&m, &back) {
			t.Fatalf("round trip: %v", err)
		}
	})
}
EOF
  go test -run '^$' -fuzz '^FuzzIPPortRoundTrip$' -fuzztime "${FUZZ_SECONDS}s" ./model/types/sys/ip/ || die "go fuzz failed"
}

smoke_rust() {
  local dir
  dir="$(repository rust)"
  cd "$dir"
  step "rust: generate"
  tacplus build --config tacplus.yaml --src proto --tag latest >generate.log 2>&1 || {
    tail -50 generate.log
    die "rust generation failed"
  }
  step "rust: test"
  (cd model && cargo test --quiet -p proto-model-types) >test.log 2>&1 || {
    tail -50 test.log
    die "cargo test failed"
  }
  step "rust: verify (Kani)"
  # tacplus verify runs Kani on every crate with a #[kani::proof] harness. The
  # harnesses tacplus generates into the model do not verify yet (#211), so the
  # verifier is driven through tacplus verify on a pinned repository with one
  # harness of its own.
  local proj="$W/rust-verify"
  mkdir -p "$proj/proof/src"
  printf 'language: rust\noutput-dir: model\n' >"$proj/tacplus.yaml"
  cp tacplus.lock "$proj/tacplus.lock"
  printf '[package]\nname = "smoke-proof"\nversion = "0.0.0"\nedition = "2021"\npublish = false\n\n[workspace]\n' >"$proj/proof/Cargo.toml"
  cat >"$proj/proof/src/lib.rs" <<'EOF'
/// A port is valid when it fits in 16 bits.
pub fn valid_port(port: u32) -> bool {
    port <= u16::MAX as u32
}

#[cfg(kani)]
mod proof {
    #[kani::proof]
    fn every_u16_is_a_valid_port() {
        let port: u16 = kani::any();
        assert!(super::valid_port(port as u32));
    }
}
EOF
  (cd "$proj" && tacplus verify) >verify.log 2>&1 || {
    tail -80 verify.log
    die "tacplus verify (Kani) failed"
  }
  grep -q 'verification passed' verify.log || {
    tail -40 verify.log
    die "tacplus verify ran no Kani harness"
  }
  step "rust: fuzz (${FUZZ_SECONDS}s)"
  mkdir -p model/types/fuzz/fuzz_targets
  cat >model/types/fuzz/Cargo.toml <<'EOF'
[package]
name = "smoke-fuzz"
version = "0.0.0"
edition = "2021"
publish = false

[package.metadata]
cargo-fuzz = true

[dependencies]
libfuzzer-sys = "0.4"
prost = "0.14"
proto-model-types = { path = ".." }

[[bin]]
name = "ip_port"
path = "fuzz_targets/ip_port.rs"
test = false
doc = false

[workspace]
EOF
  cat >model/types/fuzz/fuzz_targets/ip_port.rs <<'EOF'
#![no_main]
use libfuzzer_sys::fuzz_target;
use prost::Message;
use proto_model_types::sys::ip::IpPort;

fuzz_target!(|data: &[u8]| {
    if let Ok(m) = IpPort::decode(data) {
        let back = IpPort::decode(m.encode_to_vec().as_slice()).expect("re-decode");
        assert_eq!(m, back);
    }
});
EOF
  (cd model/types && cargo +"$RUST_NIGHTLY" fuzz run ip_port -- -max_total_time="$FUZZ_SECONDS") >fuzz.log 2>&1 || {
    tail -50 fuzz.log
    die "cargo fuzz failed"
  }
  tail -3 fuzz.log
}

for l in "${languages[@]}"; do
  case "$l" in
    go) smoke_go ;;
    rust) smoke_rust ;;
    *) die "unknown language $l (go, rust)" ;;
  esac
  cd "$W"
done
echo "smoke-pipeline: OK (${languages[*]}; tacplus $version through the launcher)"
