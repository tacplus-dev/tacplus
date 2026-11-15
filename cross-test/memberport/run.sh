#!/usr/bin/env bash
# Member port (tacplus#212, on gRPC since #321) behaviour gate.
#
# java: generate a (spec.member_port) service, assert the ServiceManager owns
# its member port, and run the memberport-peer tests (peer <-> member port <->
# member port <-> peer: correctness, session rotation across token lifetimes,
# backpressure, refusals and their statuses, faults, failure modes, the
# sender's checks of the receiver), the member port's stream-expiry leg and the
# Hazelcast integration test (two tenants' members through the port; Hazelcast
# 5.7.0, Stratum's version). MEMBERPORT_SOAK=1 adds the opt-in soak, which
# prints the overhead against a direct connection. --test <classes> runs only
# those (cross-test/stream-expiry runs MemberPortStreamExpiryTest this way).
#
# go (tacplus#322): generate the same service, assert the ServiceManager owns
# its member port the same way, and run testdata/go (the Go twins of
# MemberPortBehaviorTest and MemberPortStreamExpiryTest, Hazelcast aside) with
# -race inside the generated module. --test <regexp> is `go test -run` there
# (default ^TestMemberPort; cross-test/stream-expiry passes
# ^TestMemberPortStreamExpiry). MEMBERPORT_SOAK=1 adds the soak as for java.
# --interop (needs mvn) adds the Go <-> Java leg: the java fixture is generated
# too, and memberport_interop_test.go and MemberPortInteropTest.java run at
# once over one fake fleet (written by the Go side to a shared directory), a
# Go and a Java member port tunnelling to each other both ways across several
# token rotations. It then runs the Hazelcast leg: HazelcastMemberPortTest's
# cases in HazelcastMemberPortInteropTest.java (Java Hazelcast members, as they
# must be) beside memberport_hazelcast_test.go, which owns every member port in
# the leg (Go member ports) and serves the Java side's command files in a
# shared directory.
#
# rust (tacplus#322): generate the same service, assert the ServiceManager owns
# its member port the same way, and run testdata/rust (the Rust twins of
# MemberPortBehaviorTest and MemberPortStreamExpiryTest, Hazelcast aside) as
# integration tests of the generated memberport crate, one at a time. --test
# names the targets (memberport_behavior, memberport_stream_expiry, or their
# Java class names; cross-test/stream-expiry can pass MemberPortStreamExpiryTest).
# The build shares cross-test/lib/cargo-target.sh's target directory
# (TACPLUS_CROSS_TEST_CARGO_TARGET moves it). No soak leg. --interop (needs
# mvn) adds the Rust <-> Java leg the way go's does: memberport_interop.rs
# writes the fleet and plays the Go side's marker files for the unchanged
# MemberPortInteropTest.java. It then runs the Hazelcast leg the way go's does,
# memberport_hazelcast.rs owning every member port (Rust member ports).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PROTO_SRC="$ROOT_DIR/cross-test/memberport/proto"
LANGUAGE="java"
TESTS="MemberPortBehaviorTest,MemberPortStreamExpiryTest,HazelcastMemberPortTest"
TESTS_SET=""
INTEROP=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --language) LANGUAGE="$2"; shift 2 ;;
    --test) TESTS="$2"; TESTS_SET=1; shift 2 ;;
    --interop) INTEROP=1; shift ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

die() { echo "memberport check FAILED: $*" >&2; exit 1; }

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

write_java_memberport_pom() {
  local pom="$1"
  local source_dir="$2"
  cat > "$pom" <<EOF
<project xmlns="http://maven.apache.org/POM/4.0.0"
         xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"
         xsi:schemaLocation="http://maven.apache.org/POM/4.0.0 https://maven.apache.org/xsd/maven-4.0.0.xsd">
  <modelVersion>4.0.0</modelVersion>
  <groupId>com.example.grid</groupId>
  <artifactId>memberport-java-fixture</artifactId>
  <version>0.0.0</version>

  <properties>
    <maven.compiler.release>17</maven.compiler.release>
    <project.build.sourceEncoding>UTF-8</project.build.sourceEncoding>
  </properties>

  <build>
    <sourceDirectory>${source_dir}/src/main/java</sourceDirectory>
    <testSourceDirectory>${source_dir}/src/test/java</testSourceDirectory>
    <plugins>
      <plugin>
        <groupId>org.apache.maven.plugins</groupId>
        <artifactId>maven-compiler-plugin</artifactId>
        <version>3.15.0</version>
        <configuration>
          <excludes>
            <exclude>**/Proof.java</exclude>
          </excludes>
        </configuration>
      </plugin>
      <plugin>
        <groupId>org.apache.maven.plugins</groupId>
        <artifactId>maven-surefire-plugin</artifactId>
        <version>3.5.4</version>
        <configuration>
          <useModulePath>false</useModulePath>
        </configuration>
      </plugin>
    </plugins>
  </build>

  <dependencyManagement>
    <dependencies>
      <!-- Pinned past advisories in transitive dependencies (#200);
           scripts/java_dependency_floor_test.go holds the floors.
           guava (GHSA-xxph-c9ww-hj94) comes in through grpc-java. -->
      <dependency>
        <groupId>com.google.guava</groupId>
        <artifactId>guava</artifactId>
        <version>33.7.2-android</version>
      </dependency>
    </dependencies>
  </dependencyManagement>

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
    <dependency>
      <groupId>com.hazelcast</groupId>
      <artifactId>hazelcast</artifactId>
      <version>5.7.0</version>
      <scope>test</scope>
    </dependency>
    <dependency>
      <groupId>org.junit.jupiter</groupId>
      <artifactId>junit-jupiter</artifactId>
      <version>6.0.2</version>
      <scope>test</scope>
    </dependency>
  </dependencies>
</project>
EOF
}

# The Hazelcast legs: runs the Java test class $1 in the java fixture ($JOUT)
# in the background with $2=$3 (a fresh shared directory, which keeps its
# log), and the $5 side's command (the rest) in the foreground; both must
# pass, and the surefire report must show $4 tests run.
run_beside_java() {
  local test="$1" var="$2" dir="$3" runs="$4" side="$5"
  shift 5
  mkdir -p "$dir"
  (cd "$JOUT" && exec env ${java_env:+"$java_env"} "$var=$dir" \
    mvn -q -Dtest="$test" -Dsurefire.failIfNoSpecifiedTests=false test) >"$dir/java.log" 2>&1 &
  local java_pid=$!
  if ! "$@"; then
    kill "$java_pid" 2>/dev/null || true
    wait "$java_pid" 2>/dev/null || true
    tail -60 "$dir/java.log" >&2
    die "the $side side of $test failed"
  fi
  if ! wait "$java_pid"; then
    tail -60 "$dir/java.log" >&2
    die "the Java side of $test failed"
  fi
  grep -q "Tests run: $runs, Failures: 0, Errors: 0, Skipped: 0" "$JOUT"/target/surefire-reports/*"$test".txt \
    || die "the Java side of $test did not run"
}

case "$LANGUAGE" in
  go|rust|java) ;;
  *) die "the member port fixture supports --language java, go or rust" ;;
esac

TACPLUS_BIN="${TACPLUS_BIN:-$ROOT_DIR/target/tacplus-memberport-check}"
if [[ ! -x "$TACPLUS_BIN" || -z "${TACPLUS_BIN_PREBUILT:-}" ]]; then
  echo "==> Building tacplus binary"
  (cd "$ROOT_DIR" && go generate ./pkg/internal/proto/spec/... && go build -o "$TACPLUS_BIN" .)
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
cat > "$WORK/tacplus.yaml" <<EOF
language: "$LANGUAGE"
pkg: "com.example.grid"
src: "$PROTO_SRC"
output-dir: "tacplus"
EOF

generate() {
  (cd "$WORK" && TACPLUS_PYTHON="${TACPLUS_PYTHON:-python3}" "$TACPLUS_BIN" build --config tacplus.yaml --src "$PROTO_SRC")
}

case "$LANGUAGE" in
  go)
    command -v go >/dev/null || die "go is required to test generated Go output"
    echo "==> Generating the member port fixture (go)"
    generate || die "generation failed"
    OUT="$WORK/tacplus/model"
    SM="$OUT/s2sm/s2sm.go"
    MP="$OUT/chassis/memberport/memberport.go"
    [[ -f "$MP" ]] || die "chassis memberport missing"
    grep -q '"member_port": *true' "$OUT/registry.json" || die "registry.json missing \"member_port\": true"
    grep -q 'func (sm \*ServiceManager) MemberPort() \*memberport.MemberPort' "$SM" \
      || die "ServiceManager does not expose its member port"
    grep -q 'memberport.AudienceFor("grid")' "$SM" || die "ServiceManager does not mint the member audience"
    grep -q 'sm.identity.GetTokenFor(memberAudience)' "$SM" || die "member tokens do not come from the identity manager"
    grep -q 'sm.auth.JwkCache().Get' "$SM" || die "the member port does not verify against the watched JWKS"
    grep -q 'if _, _, err := memberPortAddresses(cfg); err != nil' "$SM" \
      || die "member port config is not checked before startup"
    grep -q 'sm.memberPort.Close()' "$SM" || die "the member port is not closed on shutdown"
    grep -q 'FullMethod = "/" + ServiceName + "/Open"' "$MP" \
      || die "the member port does not serve the member tunnel over gRPC"
    [[ -f "$OUT/types/sys/memberport/memberport.pb.go" ]] \
      || die "sys/memberport.proto (MemberChunk) was not generated"

    echo "==> Generation must refuse (spec.member_port) without auto_tls"
    NO_TLS="$WORK/proto-no-tls"
    cp -R "$PROTO_SRC" "$NO_TLS"
    sed -i.bak '/auto_tls:/d' "$NO_TLS/svc/grid.proto"
    rm -f "$NO_TLS/svc/grid.proto.bak"
    mkdir -p "$WORK/no-tls"
    sed "s#src: .*#src: \"$NO_TLS\"#" "$WORK/tacplus.yaml" > "$WORK/no-tls/tacplus.yaml"
    if out="$(cd "$WORK/no-tls" && "$TACPLUS_BIN" build --config tacplus.yaml --src "$NO_TLS" 2>&1)"; then
      die "a (spec.member_port) service without auto_tls was generated"
    fi
    grep -q "declare auto_tls on its (spec.service_state)" <<<"$out" \
      || die "refused without auto_tls for another reason: $(tail -5 <<<"$out")"

    cp "$ROOT_DIR"/cross-test/memberport/testdata/go/*_test.go "$OUT/chassis/memberport/"
    # The fixture's own dependency pins (proto/dependencies.yaml), not a copy.
    {
      printf 'module example.com/grid/tacplus/model\n\ngo 1.23\n\nrequire (\n'
      awk '/^    go:$/{f=1;next} /^    [a-z]+:$/{f=0} f && /^            [^ ]+: [^ ]+$/{sub(/^ +/,""); sub(/:/,""); gsub(/"/,""); print "\t"$0}' \
        "$PROTO_SRC/dependencies.yaml"
      printf ')\n'
    } > "$OUT/go.mod"
    GO_RUN='^TestMemberPort'
    if [[ -n "$TESTS_SET" ]]; then
      GO_RUN="$TESTS"
    fi

    echo "==> Running the member port tests: $GO_RUN"
    (cd "$OUT" && go mod tidy && go vet ./chassis/memberport ./s2sm \
      && MEMBERPORT_SOAK="${MEMBERPORT_SOAK:-}" go test -race -count=1 -timeout 600s ./chassis/memberport -run "$GO_RUN" -v) \
      || die "member port tests failed"

    if [[ -n "$INTEROP" ]]; then
      command -v mvn >/dev/null || die "--interop needs mvn to run the Java member port"
      echo "==> Generating the member port fixture (java) for the interop leg"
      mkdir -p "$WORK/java"
      sed 's/^language: .*/language: "java"/' "$WORK/tacplus.yaml" > "$WORK/java/tacplus.yaml"
      (cd "$WORK/java" && "$TACPLUS_BIN" build --config tacplus.yaml --src "$PROTO_SRC") >"$WORK/java/gen.log" 2>&1 \
        || die "java generation failed: $(tail -5 "$WORK/java/gen.log")"
      JOUT="$WORK/java/tacplus/model"
      JTEST_DIR="$JOUT/src/test/java/com/example/grid/model/chassis/memberport"
      mkdir -p "$JTEST_DIR"
      cp "$ROOT_DIR"/cross-test/memberport/testdata/*.java "$JTEST_DIR/"
      write_java_memberport_pom "$JOUT/pom.xml" "$JOUT"
      java_env="$(java_home_env)"
      (cd "$JOUT" && env ${java_env:+"$java_env"} mvn -q -DskipTests test-compile) || die "the java fixture does not compile"

      echo "==> Running the Go <-> Java member port interop leg"
      IDIR="$WORK/interop"
      mkdir -p "$IDIR"
      (cd "$JOUT" && env ${java_env:+"$java_env"} MEMBERPORT_INTEROP_DIR="$IDIR" \
        mvn -q -Dtest=MemberPortInteropTest -Dsurefire.failIfNoSpecifiedTests=false test) >"$IDIR/java.log" 2>&1 &
      JAVA_PID=$!
      go_ok=1
      (cd "$OUT" && MEMBERPORT_INTEROP_DIR="$IDIR" \
        go test -race -count=1 -timeout 900s ./chassis/memberport -run '^TestMemberPortInteropWithJava$' -v) || go_ok=""
      if [[ -z "$go_ok" ]]; then
        kill "$JAVA_PID" 2>/dev/null || true
        wait "$JAVA_PID" 2>/dev/null || true
        die "the Go side of the interop leg failed"
      fi
      java_ok=1
      wait "$JAVA_PID" || java_ok=""
      if [[ -z "$java_ok" ]]; then
        tail -60 "$IDIR/java.log" >&2
        die "the Java side of the interop leg failed"
      fi
      grep -q "Tests run: 1, Failures: 0, Errors: 0, Skipped: 0" "$JOUT"/target/surefire-reports/*MemberPortInteropTest.txt \
        || die "the Java side of the interop leg did not run"

      echo "==> Running the Hazelcast leg on Go member ports"
      go_hazelcast_side() {
        (cd "$OUT" && MEMBERPORT_HAZELCAST_DIR="$WORK/hazelcast" \
          go test -race -count=1 -timeout 900s ./chassis/memberport -run '^TestMemberPortHazelcastWithJava$' -v)
      }
      run_beside_java HazelcastMemberPortInteropTest MEMBERPORT_HAZELCAST_DIR "$WORK/hazelcast" 3 Go go_hazelcast_side
    fi
    ;;
  rust)
    command -v cargo >/dev/null || die "cargo is required to test generated Rust output"
    # shellcheck source=../lib/cargo-target.sh
    source "$ROOT_DIR/cross-test/lib/cargo-target.sh"
    echo "==> Generating the member port fixture (rust)"
    generate || die "generation failed"
    OUT="$WORK/tacplus/model"
    SM="$OUT/s2sm/mod.rs"
    MP="$OUT/chassis/memberport"
    [[ -f "$MP/src/lib.rs" ]] || die "chassis memberport crate missing"
    grep -q '"chassis/memberport"' "$OUT/Cargo.toml" || die "the memberport crate is not a workspace member"
    grep -q '"member_port": *true' "$OUT/registry.json" || die "registry.json missing \"member_port\": true"
    grep -q 'pub fn member_port(&self) -> &memberport::MemberPort' "$SM" \
      || die "ServiceManager does not expose its member port"
    grep -q 'memberport::audience_for("grid")' "$SM" || die "ServiceManager does not mint the member audience"
    grep -q 'im.get_token_for(&aud)' "$SM" || die "member tokens do not come from the identity manager"
    grep -q 'authorizer.jwk_cache().clone()' "$SM" || die "the member port does not verify against the watched JWKS"
    grep -q 'member_port_addresses(&cfg).await?;' "$SM" || die "member port config is not checked before startup"
    grep -q 'self.member_port.close();' "$SM" || die "the member port is not closed on shutdown"
    grep -q 'OPEN_PATH' "$MP/src/tunnel.rs" || die "the member port does not serve the member tunnel over gRPC"
    [[ -f "$OUT/types/sys/memberport/memberport.rs" ]] \
      || die "sys/memberport.proto (MemberChunk) was not generated"

    echo "==> Generation must refuse (spec.member_port) without auto_tls"
    NO_TLS="$WORK/proto-no-tls"
    cp -R "$PROTO_SRC" "$NO_TLS"
    sed -i.bak '/auto_tls:/d' "$NO_TLS/svc/grid.proto"
    rm -f "$NO_TLS/svc/grid.proto.bak"
    mkdir -p "$WORK/no-tls"
    sed "s#src: .*#src: \"$NO_TLS\"#" "$WORK/tacplus.yaml" > "$WORK/no-tls/tacplus.yaml"
    if out="$(cd "$WORK/no-tls" && "$TACPLUS_BIN" build --config tacplus.yaml --src "$NO_TLS" 2>&1)"; then
      die "a (spec.member_port) service without auto_tls was generated"
    fi
    grep -q "declare auto_tls on its (spec.service_state)" <<<"$out" \
      || die "refused without auto_tls for another reason: $(tail -5 <<<"$out")"

    mkdir -p "$MP/tests/support"
    cp "$ROOT_DIR"/cross-test/memberport/testdata/rust/*.rs "$MP/tests/"
    cp "$ROOT_DIR"/cross-test/memberport/testdata/rust/support/*.rs "$MP/tests/support/"
    # --test names test targets; the Java class names map to their Rust twins
    # (the Rust Hazelcast leg runs under --interop).
    RUST_TESTS=()
    IFS=',' read -r -a requested <<<"$TESTS"
    for t in "${requested[@]}"; do
      case "$t" in
        MemberPortBehaviorTest|memberport_behavior) RUST_TESTS+=(--test memberport_behavior) ;;
        MemberPortStreamExpiryTest|memberport_stream_expiry) RUST_TESTS+=(--test memberport_stream_expiry) ;;
        HazelcastMemberPortTest) [[ -z "$TESTS_SET" ]] || die "the Rust Hazelcast leg runs under --interop, not --test" ;;
        *) die "unknown rust member port test: $t" ;;
      esac
    done

    echo "==> Running the member port tests: ${RUST_TESTS[*]}"
    # One test at a time, like the Java and Go legs: several assert timings
    # (a stream closed within 1.5 s of exp, a reset under a second).
    (cd "$OUT" && cargo test -p memberport "${RUST_TESTS[@]}" -- --test-threads=1) \
      || die "member port tests failed"

    if [[ -n "$INTEROP" ]]; then
      command -v mvn >/dev/null || die "--interop needs mvn to run the Java member port"
      echo "==> Generating the member port fixture (java) for the interop leg"
      mkdir -p "$WORK/java"
      sed 's/^language: .*/language: "java"/' "$WORK/tacplus.yaml" > "$WORK/java/tacplus.yaml"
      (cd "$WORK/java" && "$TACPLUS_BIN" build --config tacplus.yaml --src "$PROTO_SRC") >"$WORK/java/gen.log" 2>&1 \
        || die "java generation failed: $(tail -5 "$WORK/java/gen.log")"
      JOUT="$WORK/java/tacplus/model"
      JTEST_DIR="$JOUT/src/test/java/com/example/grid/model/chassis/memberport"
      mkdir -p "$JTEST_DIR"
      cp "$ROOT_DIR"/cross-test/memberport/testdata/*.java "$JTEST_DIR/"
      write_java_memberport_pom "$JOUT/pom.xml" "$JOUT"
      java_env="$(java_home_env)"
      (cd "$JOUT" && env ${java_env:+"$java_env"} mvn -q -DskipTests test-compile) || die "the java fixture does not compile"
      # Built before the Java side starts waiting, so its wait is not a build.
      (cd "$OUT" && cargo test -p memberport --test memberport_interop --test memberport_hazelcast --no-run) \
        || die "the Rust side of the interop leg does not build"

      # MemberPortInteropTest.java is the Go leg's Java half, unchanged: the
      # Rust side writes the same fleet files and plays its "go" markers.
      echo "==> Running the Rust <-> Java member port interop leg"
      IDIR="$WORK/interop"
      mkdir -p "$IDIR"
      (cd "$JOUT" && env ${java_env:+"$java_env"} MEMBERPORT_INTEROP_DIR="$IDIR" \
        mvn -q -Dtest=MemberPortInteropTest -Dsurefire.failIfNoSpecifiedTests=false test) >"$IDIR/java.log" 2>&1 &
      JAVA_PID=$!
      rust_ok=1
      (cd "$OUT" && MEMBERPORT_INTEROP_DIR="$IDIR" \
        cargo test -p memberport --test memberport_interop -- --nocapture) || rust_ok=""
      if [[ -z "$rust_ok" ]]; then
        kill "$JAVA_PID" 2>/dev/null || true
        wait "$JAVA_PID" 2>/dev/null || true
        die "the Rust side of the interop leg failed"
      fi
      java_ok=1
      wait "$JAVA_PID" || java_ok=""
      if [[ -z "$java_ok" ]]; then
        tail -60 "$IDIR/java.log" >&2
        die "the Java side of the interop leg failed"
      fi
      grep -q "Tests run: 1, Failures: 0, Errors: 0, Skipped: 0" "$JOUT"/target/surefire-reports/*MemberPortInteropTest.txt \
        || die "the Java side of the interop leg did not run"

      echo "==> Running the Hazelcast leg on Rust member ports"
      rust_hazelcast_side() {
        (cd "$OUT" && MEMBERPORT_HAZELCAST_DIR="$WORK/hazelcast" \
          cargo test -p memberport --test memberport_hazelcast -- --nocapture)
      }
      run_beside_java HazelcastMemberPortInteropTest MEMBERPORT_HAZELCAST_DIR "$WORK/hazelcast" 3 Rust rust_hazelcast_side
    fi
    prune_cargo_workspace "$OUT"
    ;;
  java)
    command -v mvn >/dev/null || die "mvn is required to test generated Java output"
    echo "==> Generating the member port fixture (java)"
    generate || die "generation failed"
    OUT="$WORK/tacplus/model"
    SM="$OUT/src/main/java/com/example/grid/model/s2sm/ServiceManager.java"
    MP="$OUT/src/main/java/com/example/grid/model/chassis/memberport/MemberPort.java"
    [[ -f "$MP" ]] || die "chassis MemberPort missing"
    grep -q '"member_port": *true' "$OUT/registry.json" || die "registry.json missing \"member_port\": true"
    grep -q 'public MemberPort getMemberPort()' "$SM" || die "ServiceManager does not expose its member port"
    grep -q 'MemberPort.audienceFor("grid")' "$SM" || die "ServiceManager does not mint the member audience"
    grep -q 'identity.getTokenFor(memberAudience)' "$SM" || die "member tokens do not come from the identity manager"
    grep -q 'memberPortAddresses(cfg);' "$SM" || die "member port config is not checked before startup"
    grep -q 'OPEN_METHOD' "$MP" || die "the member port does not serve the member tunnel over gRPC"
    [[ -f "$OUT/src/main/java/com/example/grid/model/types/sys/memberport/Memberport.java" ]] \
      || die "sys/memberport.proto (MemberChunk) was not generated"

    echo "==> Generation must refuse (spec.member_port) without auto_tls"
    NO_TLS="$WORK/proto-no-tls"
    cp -R "$PROTO_SRC" "$NO_TLS"
    sed -i.bak '/auto_tls:/d' "$NO_TLS/svc/grid.proto"
    rm -f "$NO_TLS/svc/grid.proto.bak"
    mkdir -p "$WORK/no-tls"
    sed "s#src: .*#src: \"$NO_TLS\"#" "$WORK/tacplus.yaml" > "$WORK/no-tls/tacplus.yaml"
    if out="$(cd "$WORK/no-tls" && "$TACPLUS_BIN" build --config tacplus.yaml --src "$NO_TLS" 2>&1)"; then
      die "a (spec.member_port) service without auto_tls was generated"
    fi
    grep -q "declare auto_tls on its (spec.service_state)" <<<"$out" \
      || die "refused without auto_tls for another reason: $(tail -5 <<<"$out")"

    TEST_DIR="$OUT/src/test/java/com/example/grid/model/chassis/memberport"
    mkdir -p "$TEST_DIR"
    cp "$ROOT_DIR"/cross-test/memberport/testdata/*.java "$TEST_DIR/"
    write_java_memberport_pom "$OUT/pom.xml" "$OUT"

    echo "==> Running the member port tests: $TESTS"
    java_env="$(java_home_env)"
    (cd "$OUT" && env ${java_env:+"$java_env"} MEMBERPORT_SOAK="${MEMBERPORT_SOAK:-}" \
      mvn -q -Dtest="$TESTS" test) \
      || die "member port tests failed"
    ;;
esac

echo "==> memberport check PASSED ($LANGUAGE)"
