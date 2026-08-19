#!/usr/bin/env bash
# Behavioral cert-stream fixture for generated service-manager loops.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PROTO_SRC="$ROOT_DIR/cross-test/cert-stream/proto"
LANGUAGE="go"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --language) LANGUAGE="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

die() { echo "cert-stream check FAILED: $*" >&2; exit 1; }

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

write_java_cert_stream_pom() {
  local pom="$1"
  local source_dir="$2"
  cat > "$pom" <<EOF
<project xmlns="http://maven.apache.org/POM/4.0.0"
         xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"
         xsi:schemaLocation="http://maven.apache.org/POM/4.0.0 https://maven.apache.org/xsd/maven-4.0.0.xsd">
  <modelVersion>4.0.0</modelVersion>
  <groupId>com.example.certstream</groupId>
  <artifactId>cert-stream-java-fixture</artifactId>
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

  <dependencies>
    <dependency>
      <groupId>com.google.protobuf</groupId>
      <artifactId>protobuf-java</artifactId>
      <version>4.35.1</version>
    </dependency>
    <dependency>
      <groupId>com.google.protobuf</groupId>
      <artifactId>protobuf-java-util</artifactId>
      <version>4.35.1</version>
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
      <version>6.7.0</version>
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
      <version>1.80</version>
    </dependency>
    <dependency>
      <groupId>org.bouncycastle</groupId>
      <artifactId>bcprov-jdk18on</artifactId>
      <version>1.80</version>
    </dependency>
    <dependency>
      <groupId>ch.qos.logback</groupId>
      <artifactId>logback-classic</artifactId>
      <version>1.5.29</version>
    </dependency>
    <dependency>
      <groupId>ch.qos.logback</groupId>
      <artifactId>logback-core</artifactId>
      <version>1.5.29</version>
    </dependency>
    <dependency>
      <groupId>org.apache.commons</groupId>
      <artifactId>commons-compress</artifactId>
      <version>1.27.1</version>
    </dependency>
    <dependency>
      <groupId>javax.annotation</groupId>
      <artifactId>javax.annotation-api</artifactId>
      <version>1.3.2</version>
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
  *) die "behavioral cert-stream fixture supports --language go, rust, or java" ;;
esac

TACPLUS_BIN="${TACPLUS_BIN:-$ROOT_DIR/target/tacplus-cert-stream-check}"
if [[ ! -x "$TACPLUS_BIN" || -z "${TACPLUS_BIN_PREBUILT:-}" ]]; then
  echo "==> Building tacplus binary"
  (cd "$ROOT_DIR" && go build -o "$TACPLUS_BIN" .)
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
cat > "$WORK/tacplus.yaml" <<EOF
language: "$LANGUAGE"
pkg: "com.example.certstream"
src: "$PROTO_SRC"
output-dir: "tacplus"
EOF

echo "==> Generating cert-stream fixture ($LANGUAGE)"
(cd "$WORK" && TACPLUS_PYTHON="${TACPLUS_PYTHON:-python3}" "$TACPLUS_BIN" build --config tacplus.yaml --src "$PROTO_SRC") \
  || die "generation failed for $LANGUAGE"

OUT="$WORK/tacplus/model"
[[ -d "$OUT" ]] || die "no generated output at $OUT"

case "$LANGUAGE" in
  go)
    echo "==> Asserting generated Go cert-stream seams"
    grep -rq "tlsCertStreamLoop" "$OUT/s2sm"      || die "go TLS cert stream loop missing"
    grep -rq "smbCertStreamLoop" "$OUT/s2sm"      || die "go SMB cert stream loop missing"
    grep -rq "certLeafMatchesPublicKey" "$OUT/s2sm" || die "public-key validation missing"
    # The retry budget is a parameter so exhaustion is testable (PC-L-3), but the
    # production call site must still spend the unchanged default of 30.
    grep -rq "const certStreamMaxInitRetries = 30" "$OUT/s2sm" || die "go cert stream default retry budget changed"
    grep -rq "tlsReadyCh, certStreamMaxInitRetries)" "$OUT/s2sm" || die "go TLS cert stream does not pass the default retry budget"
    grep -q '"smb_authority": *true' "$OUT/registry.json" || die "registry missing SMB authority"

    cp "$ROOT_DIR/cross-test/cert-stream/testdata/cert_stream_behavior_test.go" "$OUT/s2sm/cert_stream_behavior_test.go"

    cat > "$OUT/go.mod" <<'EOF'
module example.com/certstream/tacplus/model

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

    echo "==> Running generated Go cert-stream behavior tests"
    (cd "$OUT" && go mod tidy && go test -count=1 -timeout 300s ./s2sm -run '^TestGo') \
      || die "generated Go cert-stream behavior tests failed"
    ;;
  rust)
    echo "==> Asserting generated Rust cert-stream seams"
    grep -rq "tls_cert_stream_loop" "$OUT/s2sm"      || die "rust TLS cert stream loop missing"
    grep -rq "smb_cert_stream_loop" "$OUT/s2sm"      || die "rust SMB cert stream loop missing"
    grep -rq "cert_leaf_matches_public_key" "$OUT/s2sm" || die "rust public-key validation missing"
    grep -q '"smb_authority": *true' "$OUT/registry.json" || die "registry missing SMB authority"

    cp "$ROOT_DIR/cross-test/cert-stream/testdata/cert_stream_behavior_test.rs" "$OUT/s2sm/cert_stream_behavior_test.rs"
    printf '\n#[cfg(test)]\nmod cert_stream_behavior_test;\n' >> "$OUT/s2sm/mod.rs"

    echo "==> Running generated Rust cert-stream behavior tests"
    (cd "$OUT" && CARGO_TARGET_DIR="$OUT/target" cargo test -p certstream-model rust_) \
      || die "generated Rust cert-stream behavior tests failed"
    ;;
  java)
    command -v mvn >/dev/null || die "mvn is required to test generated Java output"

    echo "==> Asserting generated Java cert-stream seams"
    grep -rq "tlsCertStreamLoop" "$OUT/src/main/java"      || die "java TLS cert stream loop missing"
    grep -rq "smbCertStreamLoop" "$OUT/src/main/java"      || die "java SMB cert stream loop missing"
    grep -rq "verifyLeafMatchesPublicKey" "$OUT/src/main/java" || die "java public-key validation missing"
    grep -rq "no active connection for ManageCert" "$OUT/src/main/java" || die "java TLS missing-connection error missing"
    grep -rq "no active connection for ManageSmbCert" "$OUT/src/main/java" || die "java SMB missing-connection error missing"
    grep -q '"smb_authority": *true' "$OUT/registry.json" || die "registry missing SMB authority"

    mkdir -p "$OUT/src/test/java/com/example/certstream/model/s2sm"
    cp "$ROOT_DIR/cross-test/cert-stream/testdata/CertStreamBehaviorTest.java" \
      "$OUT/src/test/java/com/example/certstream/model/s2sm/CertStreamBehaviorTest.java"
    write_java_cert_stream_pom "$OUT/pom.xml" "$OUT"

    echo "==> Running generated Java cert-stream behavior tests"
    java_env="$(java_home_env)"
    if [[ -n "$java_env" ]]; then
      (cd "$OUT" && env "$java_env" mvn -q -Dtest=CertStreamBehaviorTest test) \
        || die "generated Java cert-stream behavior tests failed"
    else
      (cd "$OUT" && mvn -q -Dtest=CertStreamBehaviorTest test) \
        || die "generated Java cert-stream behavior tests failed"
    fi
    ;;
esac

echo "==> cert-stream check PASSED ($LANGUAGE)"
