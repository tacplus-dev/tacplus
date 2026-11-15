#!/usr/bin/env bash
# The cache determinism test (#315), run inside the build image (`just
# images`): a cache only speeds a build up.
# The same sources built with a cold cache and then with a warm one give the
# same verdict and the same artifacts, byte for byte:
#   compilers  a Go module, a Rust crate (sccache) and a Maven project, built
#              and tested cold (nothing restored), their caches saved by
#              tacplus-cache; then built and tested again from a fresh local
#              directory restored from that bucket. The warm run must have
#              used the cache: Go's test results come from it, sccache hits,
#              and Maven builds offline.
#   images     a multi-arch image built by tacplus-image-build (rootless
#              BuildKit) cold, its cache exported; the daemon's own cache
#              pruned; then built again from the exported cache. The two
#              image digests must be one, and the warm build must have
#              reused the cached steps.
# The "bucket" is a local directory (file://): the test proves the cache
# cannot change a result, not that a cloud bucket is reachable.
#
#   bash cache-determinism.sh [compilers|images|all]   (default all)
#
# images needs rootless BuildKit to start: the container runs with
# --security-opt seccomp=unconfined --security-opt apparmor=unconfined.
set -euo pipefail

what=${1:-all}
case "$what" in compilers | images | all) ;; *) echo "usage: $0 [compilers|images|all]" >&2; exit 2 ;; esac

work=$(mktemp -d /tmp/cache-determinism.XXXXXX)
trap 'chmod -R u+w "$work" 2>/dev/null; rm -rf "$work"' EXIT
fail() { echo "cache-determinism: FAIL: $*" >&2; exit 1; }
say() { echo "=== cache-determinism: $*"; }

write_fixtures() {
  local src=$1
  mkdir -p "$src/go" "$src/rust/src" "$src/maven/src/main/java/fixture" "$src/maven/src/test/java/fixture"
  cat >"$src/go/go.mod" <<'EOF'
module example.com/fixture

go 1.22
EOF
  : >"$src/go/go.sum"
  cat >"$src/go/main.go" <<'EOF'
package main

import "fmt"

func Sum(xs ...int) int {
	t := 0
	for _, x := range xs {
		t += x
	}
	return t
}

func main() { fmt.Println(Sum(1, 2, 3)) }
EOF
  cat >"$src/go/main_test.go" <<'EOF'
package main

import "testing"

func TestSum(t *testing.T) {
	if Sum(1, 2, 3) != 6 {
		t.Fatal("Sum(1, 2, 3) != 6")
	}
}
EOF
  cat >"$src/rust/Cargo.toml" <<'EOF'
[package]
name = "fixture"
version = "0.1.0"
edition = "2021"

[dependencies]
EOF
  # A library and a binary: sccache caches the library's compilation, never
  # a crate the linker runs for (a binary, a test harness).
  cat >"$src/rust/src/main.rs" <<'EOF'
fn main() {
    println!("{}", fixture::sum(&[1, 2, 3]));
}
EOF
  cat >"$src/rust/src/lib.rs" <<'EOF'
pub fn sum(xs: &[i64]) -> i64 {
    xs.iter().sum()
}

#[cfg(test)]
mod tests {
    #[test]
    fn sums() {
        assert_eq!(super::sum(&[1, 2, 3]), 6);
    }
}
EOF
  cat >"$src/maven/pom.xml" <<'EOF'
<project xmlns="http://maven.apache.org/POM/4.0.0">
  <modelVersion>4.0.0</modelVersion>
  <groupId>fixture</groupId>
  <artifactId>fixture</artifactId>
  <version>1</version>
  <properties>
    <maven.compiler.release>17</maven.compiler.release>
    <project.build.sourceEncoding>UTF-8</project.build.sourceEncoding>
    <project.build.outputTimestamp>2026-01-01T00:00:00Z</project.build.outputTimestamp>
  </properties>
  <dependencies>
    <dependency>
      <groupId>junit</groupId>
      <artifactId>junit</artifactId>
      <version>4.13.2</version>
      <scope>test</scope>
    </dependency>
  </dependencies>
</project>
EOF
  cat >"$src/maven/src/main/java/fixture/Sum.java" <<'EOF'
package fixture;

public final class Sum {
  public static int of(int... xs) {
    int t = 0;
    for (int x : xs) t += x;
    return t;
  }
}
EOF
  cat >"$src/maven/src/test/java/fixture/SumTest.java" <<'EOF'
package fixture;

public class SumTest {
  @org.junit.Test
  public void sums() {
    org.junit.Assert.assertEquals(6, Sum.of(1, 2, 3));
  }
}
EOF
  git -C "$src" init -q && git -C "$src" add -A
}

# build_compilers SRC OUT: builds and tests the three fixtures with the cache
# settings tacplus-cache gives, writing the verdict and the artifacts' digests
# to OUT/verdict and OUT/digests, and the evidence of cache use to OUT/*.log.
build_compilers() {
  local src=$1 out=$2 mvn_offline=$3
  mkdir -p "$out"
  (
    set -euo pipefail
    unset CARGO_TARGET_DIR
    eval "$(tacplus-cache env)"
    tacplus-cache restore "$src"
    sccache --stop-server >/dev/null 2>&1 || true
    sccache --start-server >/dev/null
    sccache --zero-stats >/dev/null
    v=()
    (cd "$src/go" && go test ./... >"$out/go-test.log" 2>&1) && v+=("go test: pass") || v+=("go test: fail")
    (cd "$src/go" && go build -trimpath -o "$out/fixture-go" .) && v+=("go build: pass") || v+=("go build: fail")
    (cd "$src/rust" && cargo test --release --quiet >"$out/rust-test.log" 2>&1) && v+=("cargo test: pass") || v+=("cargo test: fail")
    (cd "$src/rust" && cargo build --release --quiet) && v+=("cargo build: pass") || v+=("cargo build: fail")
    sccache --show-stats >"$out/sccache.log" 2>&1
    sccache --stop-server >/dev/null 2>&1 || true
    (cd "$src/maven" && mvn -B -q ${mvn_offline:+"$mvn_offline"} package >"$out/maven.log" 2>&1) && v+=("mvn package: pass") || v+=("mvn package: fail")
    printf '%s\n' "${v[@]}" >"$out/verdict"
    sha256sum "$out/fixture-go" "$src/rust/target/release/fixture" "$src/maven/target/fixture-1.jar" 2>&1 \
      | sed "s|$out/||; s|$src/||" >"$out/digests" || true
    tacplus-cache save "$src"
  )
}

compilers() {
  local src="$work/src" bucket="$work/bucket"
  write_fixtures "$src"
  export TACPLUS_CACHE_BUCKET="file://$bucket"

  say "compilers: cold"
  TACPLUS_CACHE_DIR="$work/cache-cold" build_compilers "$src" "$work/cold" ""
  # The warm build starts from the same sources and a fresh local directory:
  # everything it finds came from the bucket.
  rm -rf "$src/go/fixture" "$src/rust/target" "$src/maven/target"
  say "compilers: warm"
  TACPLUS_CACHE_DIR="$work/cache-warm" build_compilers "$src" "$work/warm" "-o"

  cat "$work/cold/verdict"
  grep -q fail "$work/cold/verdict" && fail "the cold build failed: $(cat "$work"/cold/*.log)"
  diff -u "$work/cold/verdict" "$work/warm/verdict" || fail "cold and warm verdicts differ: $(cat "$work"/warm/*.log)"
  [[ $(wc -l <"$work/cold/digests") -eq 3 ]] || fail "missing artifacts: $(cat "$work/cold/digests")"
  cat "$work/cold/digests"
  diff -u "$work/cold/digests" "$work/warm/digests" || fail "cold and warm artifacts differ"
  # The warm build used the cache: Go's test result came from it, and sccache hit.
  grep -q '(cached)' "$work/warm/go-test.log" || fail "the warm go test did not use the restored GOCACHE: $(cat "$work/warm/go-test.log")"
  hits=$(awk '/^Cache hits  *[0-9]/ && !/rate/ { print $NF; exit }' "$work/warm/sccache.log")
  [[ "${hits:-0}" -gt 0 ]] || fail "the warm cargo build had no sccache hits: $(cat "$work/warm/sccache.log")"
  say "compilers: cold and warm identical (go test cached, $hits sccache hits, mvn offline)"
}

images() {
  local ctx="$work/image" cache="$work/image-cache" platforms
  case "$(uname -m)" in
    x86_64) platforms=linux/amd64,linux/arm64 ;;
    *) platforms=linux/arm64,linux/amd64 ;;
  esac
  mkdir -p "$ctx"
  cat >"$ctx/Dockerfile" <<'EOF'
FROM busybox:1.37.0@sha256:a7dbc319b1438115a207e11930dc4bb3e643dcb3ec0bdd694bb36cd8fad289ec
ARG TARGETARCH
COPY greeting /greeting
RUN mkdir -p /out && echo "$TARGETARCH" > /out/arch && cat /greeting /out/arch > /out/both
EOF
  echo hello >"$ctx/greeting"
  addr=$(tacplus-image-build start)
  export SOURCE_DATE_EPOCH=1767225600

  say "images: cold ($platforms)"
  buildctl --addr "$addr" prune --all >/dev/null
  TACPLUS_CACHE_DIR="$cache" tacplus-image-build --platform "$platforms" --cache fixture \
    --output "type=oci,dest=$work/cold.tar" --metadata-file "$work/cold.json" "$ctx" 2>"$work/cold.log" \
    || fail "the cold image build failed: $(tail -n 30 "$work/cold.log")"
  say "images: warm"
  buildctl --addr "$addr" prune --all >/dev/null
  TACPLUS_CACHE_DIR="$cache" tacplus-image-build --platform "$platforms" --cache fixture \
    --output "type=oci,dest=$work/warm.tar" --metadata-file "$work/warm.json" "$ctx" 2>"$work/warm.log" \
    || fail "the warm image build failed: $(tail -n 30 "$work/warm.log")"

  cold=$(jq -r '."containerimage.digest"' "$work/cold.json")
  warm=$(jq -r '."containerimage.digest"' "$work/warm.json")
  [[ "$cold" == sha256:* ]] || fail "no digest from the cold build: $(cat "$work/cold.json")"
  [[ "$cold" == "$warm" ]] || fail "cold and warm image digests differ: $cold vs $warm"
  grep -q 'CACHED' "$work/warm.log" || fail "the warm image build reused nothing: $(tail -n 30 "$work/warm.log")"
  say "images: cold and warm identical ($cold)"
}

[[ "$what" == images ]] || compilers
[[ "$what" == compilers ]] || images
echo "cache-determinism: OK ($what)"
