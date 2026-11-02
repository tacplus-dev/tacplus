#!/usr/bin/env bash
# Member port (tacplus#212) behaviour gate.
#
# java: generate a (spec.member_port) service, assert the ServiceManager owns
# its member port, and run the memberport-peer tests (peer <-> member port <->
# member port <-> peer: correctness, backpressure, faults, failure modes, the
# sender's checks of the receiver) and the Hazelcast integration test (two
# tenants' members through the port; Hazelcast 5.7.0, Stratum's version).
# MEMBERPORT_SOAK=1 adds the opt-in soak, which prints the overhead against a
# direct connection.
#
# go, rust: the member port is not generated there yet (tacplus#236, #237), so
# generation must refuse (spec.member_port) rather than emit a service whose
# member traffic nothing guards.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PROTO_SRC="$ROOT_DIR/cross-test/memberport/proto"
LANGUAGE="java"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --language) LANGUAGE="$2"; shift 2 ;;
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
  go|rust)
    echo "==> Generation must refuse (spec.member_port) for $LANGUAGE"
    if out="$(generate 2>&1)"; then
      die "$LANGUAGE generated a (spec.member_port) service it has no member port for"
    fi
    issue="tacplus#236"
    [[ "$LANGUAGE" == rust ]] && issue="tacplus#237"
    grep -q "declares (spec.member_port), which is generated for java only so far" <<<"$out" \
      || die "$LANGUAGE refused for another reason: $(tail -5 <<<"$out")"
    grep -q "$issue" <<<"$out" || die "$LANGUAGE refusal does not name $issue"
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

    echo "==> Running the member port behaviour and Hazelcast tests"
    java_env="$(java_home_env)"
    (cd "$OUT" && env ${java_env:+"$java_env"} MEMBERPORT_SOAK="${MEMBERPORT_SOAK:-}" \
      mvn -q -Dtest='MemberPortBehaviorTest,HazelcastMemberPortTest' test) \
      || die "member port tests failed"
    ;;
esac

echo "==> memberport check PASSED ($LANGUAGE)"
