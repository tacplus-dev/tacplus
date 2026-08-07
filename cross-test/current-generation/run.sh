#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
PROTO_SRC="$ROOT_DIR/cross-test/proto"
HEALTHONLY_PROTO_SRC="$ROOT_DIR/cross-test/proto-healthonly"
WORK_ROOT="${TACPLUS_CURRENT_GENERATION_WORKDIR:-$ROOT_DIR/target/cross-test/current-generation}"
TACPLUS_BIN="${TACPLUS_BIN:-}"
PYTHON_BIN="${TACPLUS_PYTHON:-}"
LANG_FILTER=""
KEEP=false

usage() {
  cat <<'USAGE'
Usage:
  cross-test/current-generation/run.sh [options]

Options:
  --language LANG   Run one service language: go, java, rust
  --workdir DIR     Use a specific generated-output work directory
  --tacplus BIN     Use an existing tacplus binary
  --keep            Keep generated outputs after success
  -h, --help        Show this help

Environment:
  TACPLUS_BIN                         Existing tacplus binary to use
  TACPLUS_CURRENT_GENERATION_WORKDIR  Work directory for generated outputs
  TACPLUS_PYTHON                      Python interpreter for grpc_tools.protoc
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
      KEEP=true
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

[[ -d "$PROTO_SRC" ]] || die "cross-test proto fixture not found: $PROTO_SRC"

all_languages=(go java rust)
case "$LANG_FILTER" in
  "" ) languages=("${all_languages[@]}") ;;
  go|java|rust ) languages=("$LANG_FILTER") ;;
  * ) die "unsupported language: $LANG_FILTER" ;;
esac

if [[ -n "$PYTHON_BIN" && "$PYTHON_BIN" == */* && ! -x "$PYTHON_BIN" ]]; then
  die "TACPLUS_PYTHON is not executable: $PYTHON_BIN"
fi

mkdir -p "$WORK_ROOT"
WORK_ROOT="$(cd "$WORK_ROOT" && pwd)"

if [[ -z "$TACPLUS_BIN" ]]; then
  TACPLUS_BIN="$ROOT_DIR/target/tacplus-current-generation"
  echo "==> Building local tacplus binary"
  (cd "$ROOT_DIR" && go build -o "$TACPLUS_BIN" .)
fi

if [[ "$TACPLUS_BIN" == */* ]]; then
  TACPLUS_BIN="$(cd "$(dirname "$TACPLUS_BIN")" && pwd)/$(basename "$TACPLUS_BIN")"
fi

write_config() {
  local dir="$1"
  local language="$2"

  mkdir -p "$dir"
  cat > "$dir/tacplus.yaml" <<EOF
language: "$language"
pkg: "com.gitlab.example.dummy.proto"
src: "$PROTO_SRC"
output-dir: "tacplus"
EOF
}

write_healthonly_config() {
  local dir="$1"
  local language="$2"

  mkdir -p "$dir"
  cat > "$dir/tacplus.yaml" <<EOF
language: "$language"
pkg: "com.gitlab.example.dummy.healthonly"
src: "$HEALTHONLY_PROTO_SRC"
output-dir: "tacplus"
EOF
}

assert_no_legacy_fixture_output() {
  local output="$1"

  if find "$output" -type f \( \
    -path '*/proto/special/*' -o \
    -path '*/types/special/*' -o \
    -name 'special_pb2.py' -o \
    -name 'special_pb.ts' -o \
    -name 'special.pb.go' -o \
    -name 'special.rs' \
  \) -print -quit | grep -q .; then
    find "$output" -type f \( \
      -path '*/proto/special/*' -o \
      -path '*/types/special/*' -o \
      -name 'special_pb2.py' -o \
      -name 'special_pb.ts' -o \
      -name 'special.pb.go' -o \
      -name 'special.rs' \
    \) | sed 's/^/legacy fixture output: /' >&2
    die "generated output still contains legacy special fixture files"
  fi

  if rg -n 'special/special\.proto|special/errors\.proto|from special|import special|special_pb2|GrpcS2SClients\.java|class S2SClients|struct S2SClients|export class S2SClients|contract_s2_s|types\.services\.UNKNOWN|UNKNOWN_pb2_grpc|UNKNOWN\.Mint|TOKEN_EXCHANGER_PACKAGE = '\''UNKNOWN'\''|DEFAULT_AUDIENCE = '\''UNKNOWN'\''' "$output"; then
    die "generated output still contains stale generated symbols"
  fi

  if rg -n '%![A-Za-z]' "$output"; then
    die "generated output contains fmt formatter artifacts"
  fi

  if rg -n '^import org\.apache\.commons\.compress' "$output"; then
    die "Java generated source must not require commons-compress at compile time"
  fi
}

# SPIFFE soundness (plan: Soundness Rules): no raw spiffe:// FORMATTING in
# generated code outside sys/spiffe and tests — printf verbs, format!/{} and
# ${} interpolation, f-strings, and string concatenation onto a spiffe literal.
# All SPIFFE construction must go through the generated sys/spiffe helpers.
# Plain mentions in doc comments, protos, and registry metadata are fine.
assert_spiffe_soundness() {
  local output="$1"
  local hits
  hits="$(rg -n 'f["'\'']spiffe:|spiffe://%|spiffe://\{|spiffe://\$|spiffe:[^"'\''\x60]*["'\''\x60][[:space:]]*\+|\+[[:space:]]*["'\''\x60]spiffe:' "$output" \
    --glob '!**/sys/spiffe/**' \
    --glob '!**/chassis/spiffe/**' \
    --glob '!**/*.proto' \
    --glob '!**/*.json' \
    --glob '!**/*_test.go' \
    --glob '!**/test_*.py' \
    --glob '!**/*.test.ts' \
    --glob '!**/*Test.java' \
    --glob '!**/*.md' \
    || true)"
  if [[ -n "$hits" ]]; then
    printf '%s\n' "$hits" >&2
    die "raw spiffe:// formatting found outside sys/spiffe — use the generated spiffe helpers"
  fi
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
  for pattern in '*/chassis/spiffe/SpiffeVectorsTest.java' '*/chassis/x509/ImmutableCertChainTest.java' '*/chassis/grpcserver/AuthorizerBehaviorTest.java' '*/chassis/collection/CollectionRuntimeTest.java' '*/chassis/grpcconfig/ConfigDefaultsTest.java' '*/chassis/grpcconfig/RedactionInstalledTest.java' '*/chassis/logger/LogbackConfiguratorSecurityTest.java' '*/chassis/diskstore/DiskStoreTest.java'; do
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

  # SEC-1: the x509 crate's tests pin verify_service_name (the SPIFFE identity
  # pin behind ServiceNameServerVerifier) with certs minted by the crate's own
  # signer. Without this step a revert of the pin (or of the tests) is silent —
  # no other gate runs rust x509 tests.
  [[ -f "$output/chassis/x509/Cargo.toml" ]] || die "missing generated Rust x509 crate"
  (cd "$output" && cargo test -p x509) >"$log_dir/rust-x509-test.log" 2>&1 || {
      cat "$log_dir/rust-x509-test.log" >&2
      return 1
    }
  assert_clean_log "$log_dir/rust-x509-test.log"

  # SEC-1 (verifier leg): SpiffeServerCertVerifier construction fail-loud (OF-12
  # malformed trust domain) + build_root_store parsing, minted via the x509
  # crate's signer (dev-dependency of the servicemanager crate).
  [[ -f "$output/chassis/servicemanager/Cargo.toml" ]] || die "missing generated Rust servicemanager crate"
  (cd "$output" && cargo test -p servicemanager) >"$log_dir/rust-servicemanager-test.log" 2>&1 || {
      cat "$log_dir/rust-servicemanager-test.log" >&2
      return 1
    }
  assert_clean_log "$log_dir/rust-servicemanager-test.log"

  # SEC-2: the grpcconfig crate's loader test verifies that load_config() calls
  # logger::Logger::default().install(), catching the "generated but never wired"
  # defect where credential redaction exists but is never installed on the startup path.
  [[ -f "$output/chassis/grpcconfig/Cargo.toml" ]] || die "missing generated Rust grpcconfig crate"
  (cd "$output" && cargo test -p grpcconfig) >"$log_dir/rust-grpcconfig-test.log" 2>&1 || {
      cat "$log_dir/rust-grpcconfig-test.log" >&2
      return 1
    }
  assert_clean_log "$log_dir/rust-grpcconfig-test.log"

  # PC-W-1: the grpcserver crate's authorizer behavioral tests mint real
  # Ed25519 JWTs against the REAL Authorizer verify path (signature, audience,
  # expiry, unknown-kid, tamper, auth_services, entitlements presence,
  # namespace rule). Without this step a decision-logic regression in the Rust
  # Authorizer is silent — no other gate runs rust grpcserver tests.
  [[ -f "$output/chassis/grpcserver/Cargo.toml" ]] || die "missing generated Rust grpcserver crate"
  (cd "$output" && cargo test -p grpcserver) >"$log_dir/rust-grpcserver-test.log" 2>&1 || {
      cat "$log_dir/rust-grpcserver-test.log" >&2
      return 1
    }
  assert_clean_log "$log_dir/rust-grpcserver-test.log"

  # PC-C-2: the collection crate's page-token codec tests pin the
  # cross-language byte-identity vectors (shared verbatim with the go and java
  # codec tests). Never previously executed by any gate (cargo check only).
  [[ -f "$output/chassis/collection/Cargo.toml" ]] || die "missing generated Rust collection crate"
  (cd "$output" && cargo test -p collection) >"$log_dir/rust-collection-test.log" 2>&1 || {
      cat "$log_dir/rust-collection-test.log" >&2
      return 1
    }
  assert_clean_log "$log_dir/rust-collection-test.log"

  # PC-L-7: the diskstore crate's tests (compress/path/snapshot/redolog/
  # schedule) existed in the templates but were never executed by any gate
  # (cargo check only).
  [[ -f "$output/chassis/diskstore/Cargo.toml" ]] || die "missing generated Rust diskstore crate"
  (cd "$output" && cargo test -p diskstore) >"$log_dir/rust-diskstore-test.log" 2>&1 || {
      cat "$log_dir/rust-diskstore-test.log" >&2
      return 1
    }
  assert_clean_log "$log_dir/rust-diskstore-test.log"

  # Behavioral config_defaults test: empty Config → health_probe_port == 8444.
  # Covers the state-authority (kernel) path where no typed-config fetch occurs.
  [[ -f "$output/chassis/grpcconfig/Cargo.toml" ]] || die "missing generated Rust grpcconfig crate"
  (cd "$output" && cargo test -p grpcconfig) >"$log_dir/rust-grpcconfig-test.log" 2>&1 || {
      cat "$log_dir/rust-grpcconfig-test.log" >&2
      return 1
    }
  assert_clean_log "$log_dir/rust-grpcconfig-test.log"
}

write_go_compile_mod() {
  local mod="$1"
  local module_path="${2:-gitlab.com/example/dummy/proto}"
  cat > "$mod" <<EOF
module $module_path

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
	golang.org/x/net v0.49.0
	golang.org/x/sync v0.19.0
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260209200024-4cfbd4190f57
	google.golang.org/grpc v1.78.0
	google.golang.org/protobuf v1.36.11
	gopkg.in/yaml.v3 v3.0.1
)
EOF
}

write_java_compile_pom() {
  local pom="$1"
  local source_dir="$2"
  cat > "$pom" <<EOF
<project xmlns="http://maven.apache.org/POM/4.0.0"
         xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"
         xsi:schemaLocation="http://maven.apache.org/POM/4.0.0 https://maven.apache.org/xsd/maven-4.0.0.xsd">
  <modelVersion>4.0.0</modelVersion>
  <groupId>com.gitlab.example.dummy</groupId>
  <artifactId>current-generation-java-compile</artifactId>
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
  </dependencies>
</project>
EOF
}

write_saml_runtime_fixture() {
  local dir="$1"

  command -v openssl >/dev/null || die "openssl is required for generated SAML runtime probes"
  mkdir -p "$dir"
  if ! openssl ecparam -name prime256v1 -genkey -noout -out "$dir/idp-key.pem" >"$dir/openssl-key.log" 2>&1; then
    cat "$dir/openssl-key.log" >&2
    return 1
  fi
  if ! openssl req -new -x509 -key "$dir/idp-key.pem" -out "$dir/idp-cert.pem" -days 2 -subj "/CN=tacplus-test-idp" >"$dir/openssl-cert.log" 2>&1; then
    cat "$dir/openssl-cert.log" >&2
    return 1
  fi
}

run_java_saml_runtime_probe() {
  local output="$1"
  local log_dir="$2"
  local compile_dir="$log_dir/java-compile"
  local saml_java
  saml_java="$(find "$output/src/main/java" -type f -path '*/chassis/saml/SamlAssertion.java' -print -quit)"
  [[ -n "$saml_java" ]] || die "missing generated Java SamlAssertion.java"

  local saml_pkg
  saml_pkg="$(awk '/^package /{gsub(";","",$2); print $2; exit}' "$saml_java")"
  [[ -n "$saml_pkg" ]] || die "could not determine generated Java SAML package"

  local pkg_base="${saml_pkg%.chassis.saml}"
  local chassis_base="$pkg_base.chassis"
  local types_base="$pkg_base.types"
  local fixture_dir="$log_dir/saml-runtime-fixture"
  write_saml_runtime_fixture "$fixture_dir"

  local probe_dir="$log_dir/java-saml-runtime"
  rm -rf "$probe_dir"
  mkdir -p "$probe_dir/classes"
  cat > "$probe_dir/SamlRuntimeProbe.java" <<EOF
import ${chassis_base}.key.ImmutableKey;
import ${chassis_base}.saml.SamlAssertion;
import ${chassis_base}.x509.ImmutableCert;
import ${types_base}.sys.key.KeyOuterClass;
import ${types_base}.sys.x509.X509;

import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Instant;
import java.util.Base64;

public final class SamlRuntimeProbe {
    private static final String SUCCESS = "urn:oasis:names:tc:SAML:2.0:status:Success";
    private static final String RESPONDER = "urn:oasis:names:tc:SAML:2.0:status:Responder";

    public static void main(String[] args) throws Exception {
        ImmutableKey key = ImmutableKey.from(KeyOuterClass.Key.newBuilder()
                .setId("idp-key")
                .setBase64(base64Url(Files.readAllBytes(Path.of(args[0]))))
                .setCreated(Instant.now().getEpochSecond())
                .build());
        ImmutableCert cert = ImmutableCert.from(X509.X509Cert.newBuilder()
                .setId("idp-cert")
                .setBase64(base64Url(Files.readAllBytes(Path.of(args[1]))))
                .build());

        SamlAssertion assertion = SamlAssertion.builder()
                .issuer("https://idp.example.test")
                .subjectNameId("user@example.test")
                .subjectRecipient("https://sp.example.test/acs")
                .audience("https://sp.example.test")
                .attribute("role", "tester")
                .sign(key, cert);

        SamlAssertion parsed = SamlAssertion.parse(assertion.toBase64());
        parsed.verify(cert, new SamlAssertion.VerifyOptions()
                .expectedIssuer("https://idp.example.test")
                .expectedAudience("https://sp.example.test")
                .expectedRecipient("https://sp.example.test/acs"));

        expectFailure("non-success status", () ->
                SamlAssertion.parse(withNonSuccessStatus(assertion.getXml())).verify(cert), "not Success");
        SamlAssertion missingExpiry = SamlAssertion.parse(withMissingAssertionExpiry(assertion.getXml()));
        if (missingExpiry.isTimeValid()) {
            throw new AssertionError("missing assertion NotOnOrAfter should not be time-valid");
        }
        expectFailure("missing assertion NotOnOrAfter", () ->
                missingExpiry.verify(cert), "mismatch");
        expectFailure("multiple assertions", () ->
                SamlAssertion.parse(withDuplicateAssertion(assertion.getXml())), "multiple Assertion");
    }

    private static String base64Url(byte[] bytes) {
        return Base64.getUrlEncoder().withoutPadding().encodeToString(bytes);
    }

    private static String withNonSuccessStatus(String xml) {
        return xml.replace(SUCCESS, RESPONDER);
    }

    private static String withMissingAssertionExpiry(String xml) {
        return xml.replaceFirst(" NotOnOrAfter=\"[^\"]*\"><saml:AudienceRestriction", "><saml:AudienceRestriction");
    }

    private static String withDuplicateAssertion(String xml) {
        String open = "<saml:Assertion ";
        String close = "</saml:Assertion>";
        int start = xml.indexOf(open);
        int end = xml.indexOf(close, start);
        if (start < 0 || end < 0) throw new IllegalArgumentException("missing assertion");
        end += close.length();
        String assertion = xml.substring(start, end);
        return xml.replace("</samlp:Response>", assertion + "</samlp:Response>");
    }

    private static void expectFailure(String name, CheckedRunnable runnable, String expectedMessage) throws Exception {
        try {
            runnable.run();
        } catch (Exception e) {
            if (e.getMessage() != null && e.getMessage().contains(expectedMessage)) {
                return;
            }
            throw new AssertionError(name + " failed with unexpected error: " + e.getMessage(), e);
        }
        throw new AssertionError(name + " unexpectedly succeeded");
    }

    @FunctionalInterface
    private interface CheckedRunnable {
        void run() throws Exception;
    }
}
EOF

  local java_env
  java_env="$(java_home_env)"
  local java_home=""
  if [[ -n "$java_env" ]]; then
    java_home="${java_env#JAVA_HOME=}"
  elif [[ -n "${JAVA_HOME:-}" ]]; then
    java_home="$JAVA_HOME"
  fi

  if [[ -n "$java_env" ]]; then
    (cd "$compile_dir" && env "$java_env" mvn -q dependency:build-classpath -Dmdep.outputFile=classpath.txt) >"$log_dir/maven-runtime-classpath.log" 2>&1 || {
      cat "$log_dir/maven-runtime-classpath.log" >&2
      return 1
    }
  else
    (cd "$compile_dir" && mvn -q dependency:build-classpath -Dmdep.outputFile=classpath.txt) >"$log_dir/maven-runtime-classpath.log" 2>&1 || {
      cat "$log_dir/maven-runtime-classpath.log" >&2
      return 1
    }
  fi

  local deps_cp=""
  [[ -f "$compile_dir/classpath.txt" ]] && deps_cp="$(cat "$compile_dir/classpath.txt")"
  local cp="$compile_dir/target/classes"
  [[ -n "$deps_cp" ]] && cp="$cp:$deps_cp"

  local javac_bin="javac"
  local java_bin="java"
  if [[ -n "$java_home" && -x "$java_home/bin/javac" && -x "$java_home/bin/java" ]]; then
    javac_bin="$java_home/bin/javac"
    java_bin="$java_home/bin/java"
  fi

  "$javac_bin" -cp "$cp" -d "$probe_dir/classes" "$probe_dir/SamlRuntimeProbe.java" >"$log_dir/java-saml-runtime.log" 2>&1 || {
    cat "$log_dir/java-saml-runtime.log" >&2
    return 1
  }
  "$java_bin" -cp "$probe_dir/classes:$cp" SamlRuntimeProbe "$fixture_dir/idp-key.pem" "$fixture_dir/idp-cert.pem" >>"$log_dir/java-saml-runtime.log" 2>&1 || {
    cat "$log_dir/java-saml-runtime.log" >&2
    return 1
  }
  assert_clean_log "$log_dir/java-saml-runtime.log"
}

ensure_python_saml_runtime_deps() {
  local python_cmd="$1"
  local deps_dir="$2"
  local log="$3"

  mkdir -p "$deps_dir"
  if PYTHONPATH="$deps_dir${PYTHONPATH:+:$PYTHONPATH}" "$python_cmd" -c 'import cryptography, google.protobuf' >/dev/null 2>&1; then
    return
  fi

  "$python_cmd" -m pip install -q --disable-pip-version-check --target "$deps_dir" \
    'cryptography>=42.0.0' 'protobuf>=6.33.6' >"$log" 2>&1 || {
    cat "$log" >&2
    return 1
  }
}

run_python_saml_runtime_probe() {
  local base="$1"
  local log_dir="$2"
  local python_cmd="${PYTHON_BIN:-python3}"
  local saml_py
  saml_py="$(find "$base" -type f -path '*/chassis/saml/saml_assertion.py' -print -quit)"
  [[ -n "$saml_py" ]] || die "missing generated Python saml_assertion.py"

  local pkg_dir
  pkg_dir="$(cd "$(dirname "$saml_py")/../.." && pwd)"
  local py_pkg
  py_pkg="$(basename "$pkg_dir")"
  # Import root is the directory CONTAINING the generated package (registry.json
  # sits next to it at the project root), not the workdir base.
  local py_root
  py_root="$(dirname "$pkg_dir")"
  local fixture_dir="$log_dir/saml-runtime-fixture"
  write_saml_runtime_fixture "$fixture_dir"

  local deps_dir="$log_dir/python-runtime-deps"
  ensure_python_saml_runtime_deps "$python_cmd" "$deps_dir" "$log_dir/python-saml-runtime-pip.log"

  local probe="$log_dir/python-saml-runtime-probe.py"
  cat > "$probe" <<'PY'
import base64
import importlib
import pathlib
import sys
import time


def b64url(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).decode("ascii").rstrip("=")


def duplicate_assertion(xml: str) -> str:
    start = xml.find("<saml:Assertion ")
    close = "</saml:Assertion>"
    end = xml.find(close, start)
    if start < 0 or end < 0:
        raise AssertionError("missing assertion")
    end += len(close)
    return xml.replace("</samlp:Response>", xml[start:end] + "</samlp:Response>")


def missing_assertion_expiry(xml: str) -> str:
    return xml.replace(
        f' NotOnOrAfter="{assertion.not_on_or_after}"><saml:AudienceRestriction',
        '><saml:AudienceRestriction',
    )


def expect_failure(name, fn, expected: str) -> None:
    try:
        fn()
    except Exception as exc:
        if expected in str(exc):
            return
        raise AssertionError(f"{name} failed with unexpected error: {exc}") from exc
    raise AssertionError(f"{name} unexpectedly succeeded")


pkg, key_path, cert_path = sys.argv[1:4]
key_pb2 = importlib.import_module(f"{pkg}.types.sys.key.key_pb2")
x509_pb2 = importlib.import_module(f"{pkg}.types.sys.x509.x509_pb2")
key_mod = importlib.import_module(f"{pkg}.chassis.key.key")
x509_mod = importlib.import_module(f"{pkg}.chassis.x509.x509_cert")
saml_mod = importlib.import_module(f"{pkg}.chassis.saml.saml_assertion")

key_proto = key_pb2.Key()
key_proto.id = "idp-key"
key_proto.base64 = b64url(pathlib.Path(key_path).read_bytes())
key_proto.created = int(time.time())
key = key_mod.ImmutableKey(key_proto)

cert_proto = x509_pb2.X509Cert()
cert_proto.id = "idp-cert"
cert_proto.base64 = b64url(pathlib.Path(cert_path).read_bytes())
cert = x509_mod.ImmutableCert(cert_proto)

assertion = (
    saml_mod.SamlAssertionBuilder()
    .set_issuer("https://idp.example.test")
    .set_subject_name_id("user@example.test")
    .set_subject_recipient("https://sp.example.test/acs")
    .add_audience("https://sp.example.test")
    .set_attribute("role", "tester")
    .sign(key, cert)
)

parsed = saml_mod.SamlAssertion.parse(assertion.to_base64())
parsed.verify(
    cert,
    saml_mod.SamlVerifyOptions(
        expected_issuer="https://idp.example.test",
        expected_audience="https://sp.example.test",
        expected_recipient="https://sp.example.test/acs",
    ),
)

expect_failure(
    "non-success status",
    lambda: saml_mod.SamlAssertion.parse(
        assertion.xml.replace(
            "urn:oasis:names:tc:SAML:2.0:status:Success",
            "urn:oasis:names:tc:SAML:2.0:status:Responder",
        )
    ).verify(cert),
    "not Success",
)
missing_expiry = saml_mod.SamlAssertion.parse(missing_assertion_expiry(assertion.xml))
if missing_expiry.is_time_valid():
    raise AssertionError("missing assertion NotOnOrAfter should not be time-valid")
expect_failure(
    "missing assertion NotOnOrAfter",
    lambda: missing_expiry.verify(cert),
    "mismatch",
)
expect_failure(
    "multiple assertions",
    lambda: saml_mod.SamlAssertion.parse(duplicate_assertion(assertion.xml)),
    "multiple Assertion",
)
PY

  PYTHONPATH="$deps_dir:$py_root${PYTHONPATH:+:$PYTHONPATH}" "$python_cmd" "$probe" "$py_pkg" "$fixture_dir/idp-key.pem" "$fixture_dir/idp-cert.pem" >"$log_dir/python-saml-runtime.log" 2>&1 || {
    cat "$log_dir/python-saml-runtime.log" >&2
    return 1
  }
  assert_clean_log "$log_dir/python-saml-runtime.log"
}

run_typescript_saml_runtime_probe() {
  local output="$1"
  local log_dir="$2"
  local fixture_dir="$log_dir/saml-runtime-fixture"
  write_saml_runtime_fixture "$fixture_dir"

  local probe="$log_dir/typescript-saml-runtime-probe.mjs"
  cat > "$probe" <<'JS'
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

function b64url(bytes) {
  return Buffer.from(bytes).toString('base64url');
}

function duplicateAssertion(xml) {
  const start = xml.indexOf('<saml:Assertion ');
  const close = '</saml:Assertion>';
  const end = xml.indexOf(close, start);
  if (start < 0 || end < 0) throw new Error('missing assertion');
  const assertion = xml.slice(start, end + close.length);
  return xml.replace('</samlp:Response>', assertion + '</samlp:Response>');
}

function missingAssertionExpiry(xml) {
  return xml.replace(/ NotOnOrAfter="[^"]*"><saml:AudienceRestriction/, '><saml:AudienceRestriction');
}

function expectFailure(name, fn, expected) {
  try {
    fn();
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    if (message.includes(expected)) return;
    throw new Error(name + ' failed with unexpected error: ' + message);
  }
  throw new Error(name + ' unexpectedly succeeded');
}

const [output, keyPath, certPath] = process.argv.slice(2);
const saml = await import(pathToFileURL(path.join(output, 'dist/sys/saml/index.js')).href);
const key = await import(pathToFileURL(path.join(output, 'dist/sys/key/index.js')).href);
const x509 = await import(pathToFileURL(path.join(output, 'dist/sys/x509/index.js')).href);

const immutableKey = await key.ImmutableKey.create({
  id: 'idp-key',
  base64: b64url(readFileSync(keyPath)),
});
const immutableCert = x509.ImmutableCert.create({
  id: 'idp-cert',
  base64: b64url(readFileSync(certPath)),
});

const assertion = new saml.SamlAssertionBuilder()
  .setIssuer('https://idp.example.test')
  .setSubjectNameId('user@example.test')
  .setSubjectRecipient('https://sp.example.test/acs')
  .addAudience('https://sp.example.test')
  .setAttribute('role', 'tester')
  .sign(immutableKey, immutableCert);

const parsed = saml.SamlAssertion.parse(assertion.toBase64());
parsed.verify(immutableCert, {
  expectedIssuer: 'https://idp.example.test',
  expectedAudience: 'https://sp.example.test',
  expectedRecipient: 'https://sp.example.test/acs',
});

expectFailure(
  'non-success status',
  () => saml.SamlAssertion
    .parse(assertion.xml.replace(
      'urn:oasis:names:tc:SAML:2.0:status:Success',
      'urn:oasis:names:tc:SAML:2.0:status:Responder',
    ))
    .verify(immutableCert),
  'not Success',
);
const missingExpiry = saml.SamlAssertion.parse(missingAssertionExpiry(assertion.xml));
if (missingExpiry.isTimeValid()) {
  throw new Error('missing assertion NotOnOrAfter should not be time-valid');
}
expectFailure(
  'missing assertion NotOnOrAfter',
  () => missingExpiry.verify(immutableCert),
  'mismatch',
);
expectFailure(
  'multiple assertions',
  () => saml.SamlAssertion.parse(duplicateAssertion(assertion.xml)),
  'multiple Assertion',
);
JS

  (cd "$output" && node "$probe" "$output" "$fixture_dir/idp-key.pem" "$fixture_dir/idp-cert.pem") >"$log_dir/typescript-saml-runtime.log" 2>&1 || {
    cat "$log_dir/typescript-saml-runtime.log" >&2
    return 1
  }
  assert_clean_log "$log_dir/typescript-saml-runtime.log"
}

run_saml_runtime_probe() {
  local output="$1"
  local language="$2"
  local base="${3:-$1}"
  local log_dir
  log_dir="$(dirname "$base")"

  case "$language" in
    java)
      run_java_saml_runtime_probe "$output" "$log_dir"
      ;;
    python)
      run_python_saml_runtime_probe "$base" "$log_dir"
      ;;
    typescript)
      run_typescript_saml_runtime_probe "$output" "$log_dir"
      ;;
  esac
}

compile_current_output() {
  local output="$1"
  local language="$2"
  local base="${3:-$1}"
  local go_module="${4:-}"
  local log_dir
  log_dir="$(dirname "$base")"

  case "$language" in
    go)
      write_go_compile_mod "$log_dir/go.mod" "$go_module"
      (cd "$log_dir" && go mod tidy && SPIFFE_VECTOR_FIXTURE="$ROOT_DIR/cross-test/fixtures/spiffe_vectors.json" go test ./...) >"$log_dir/go-test.log" 2>&1 || {
        cat "$log_dir/go-test.log" >&2
        return 1
      }
      assert_clean_log "$log_dir/go-test.log"
      ;;
    java)
      command -v mvn >/dev/null || die "mvn is required to compile generated Java output"
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
      run_java_generated_self_tests "$output" "$compile_dir" "$log_dir"
      ;;
    python)
      if [[ -n "$PYTHON_BIN" ]]; then
        "$PYTHON_BIN" -m compileall -q "$base" >"$log_dir/python-compileall.log" 2>&1 || {
          cat "$log_dir/python-compileall.log" >&2
          return 1
        }
      else
        python3 -m compileall -q "$base" >"$log_dir/python-compileall.log" 2>&1 || {
          cat "$log_dir/python-compileall.log" >&2
          return 1
        }
      fi
      assert_clean_log "$log_dir/python-compileall.log"
      ;;
    rust)
      if [[ -f "$output/Cargo.toml" ]]; then
        (cd "$output" && CARGO_TARGET_DIR="$output/target" cargo check) >"$log_dir/cargo-check.log" 2>&1 || {
          cat "$log_dir/cargo-check.log" >&2
          return 1
        }
        assert_clean_log "$log_dir/cargo-check.log"
        run_rust_spiffe_self_tests "$output" "$log_dir"
      fi
      ;;
    typescript)
      if [[ -f "$output/package.json" ]]; then
        (cd "$output" && npm install && npm run build) >"$log_dir/npm-build.log" 2>&1 || {
          cat "$log_dir/npm-build.log" >&2
          return 1
        }
        assert_clean_log "$log_dir/npm-build.log"
      fi
      ;;
  esac

  case "$language" in
    java|python|typescript)
      echo "==> Running $language SAML runtime probe"
      run_saml_runtime_probe "$output" "$language" "$base"
      ;;
  esac
}

resolve_output_root() {
  local base="$1"

  if [[ -f "$base/registry.json" ]]; then
    printf '%s\n' "$base"
    return
  fi

  local found=()
  while IFS= read -r registry; do
    found+=("$(dirname "$registry")")
  done < <(find "$base" -maxdepth 3 -type f -name registry.json | sort)

  if [[ ${#found[@]} -ne 1 ]]; then
    find "$base" -maxdepth 3 -type f -name registry.json | sed 's/^/registry candidate: /' >&2
    die "could not determine generated project root under $base"
  fi
  printf '%s\n' "${found[0]}"
}

assert_current_output() {
	local output="$1"
	local language="$2"
  local base="${3:-$1}"
  local proto_root="$output/proto"

  if [[ ! -f "$proto_root/sys/spec.proto" && -f "$base/proto/sys/spec.proto" ]]; then
    proto_root="$base/proto"
  fi

  [[ -f "$output/registry.json" ]] || die "missing registry.json in $output"
  [[ -f "$proto_root/sys/spec.proto" ]] || die "missing embedded sys/spec.proto in $proto_root"
  [[ -f "$proto_root/services2/inventory.proto" ]] || die "missing migrated inventory proto in $proto_root"
  [[ -f "$proto_root/services2/analytics.proto" ]] || die "missing migrated analytics proto in $proto_root"

  case "$language" in
    go)
      [[ -f "$output/types/services2/inventory/inventory.pb.go" ]] || die "missing Go inventory protobuf output"
      [[ -f "$output/types/services2/analytics/analytics.pb.go" ]] || die "missing Go analytics protobuf output"
      ;;
    java)
      find "$output" -type f -name '*Inventory*.java' -print -quit | grep -q . || die "missing Java inventory output"
      find "$output" -type f -name '*Analytics*.java' -print -quit | grep -q . || die "missing Java analytics output"
      ;;
    python)
      find "$output" -type f -name 'inventory_pb2.py' -print -quit | grep -q . || die "missing Python inventory output"
      find "$output" -type f -name 'analytics_pb2.py' -print -quit | grep -q . || die "missing Python analytics output"
      ;;
    rust)
      find "$output" -type f -name 'inventory.rs' -print -quit | grep -q . || die "missing Rust inventory output"
      find "$output" -type f -name 'analytics.rs' -print -quit | grep -q . || die "missing Rust analytics output"
      ;;
    typescript)
      find "$output" -type f -name 'inventory_pb.ts' -print -quit | grep -q . || die "missing TypeScript inventory output"
      find "$output" -type f -name 'analytics_pb.ts' -print -quit | grep -q . || die "missing TypeScript analytics output"
      ;;
  esac

  assert_no_legacy_fixture_output "$output"
}

# assert_healthonly_output checks fixture-specific invariants for the health-only
# proto source (HasTlsGrpc=false, HasC2DGrpc=false, HasAutoTls=true).
# For Rust, this verifies that the health-only branch was actually emitted —
# the whole point of the fixture is to guarantee the else-branch in
# service_manager.rs.tmpl is compiled and not just silently skipped.
assert_healthonly_output() {
  local output="$1"
  local language="$2"

  case "$language" in
    rust)
      # The service manager is emitted as s2sm/mod.rs (GetSmPrefix() == "s2sm").
      local sm="$output/s2sm/mod.rs"
      [[ -f "$sm" ]] || die "health-only fixture: s2sm/mod.rs not found under $output"
      grep -q 'health-only gRPC server listening' "$sm" \
        || die "health-only fixture: 'health-only gRPC server listening' not found in $sm (wrong template branch?)"
      ;;
  esac

  assert_no_legacy_fixture_output "$output"
}

for language in "${languages[@]}"; do
  dir="$WORK_ROOT/$language"
  log="$WORK_ROOT/$language.log"
  rm -rf "$dir"
  write_config "$dir" "$language"

  echo "==> Generating $language cross-test fixture"
  if ! (cd "$dir" && TACPLUS_PYTHON="$PYTHON_BIN" "$TACPLUS_BIN" build --config tacplus.yaml --src "$PROTO_SRC" >"$log" 2>&1); then
    tail -120 "$log" >&2 || true
    die "$language generation failed; log: $log"
  fi
  output_root="$(resolve_output_root "$dir/tacplus")"
  assert_no_legacy_fixture_output "$dir/tacplus"
  assert_spiffe_soundness "$dir/tacplus"
  assert_current_output "$output_root" "$language" "$dir/tacplus"
  echo "==> Compiling $language cross-test fixture"
  compile_current_output "$output_root" "$language" "$dir/tacplus"
  echo "    PASS $language"
done

# Config-defaults cross-language parity: for every proto message that has
# spec.default_int / spec.default_string / etc. annotations, verify that
# ALL three full-service languages generated the exact same set of DEFAULT_*
# constants. A language that silently omits a default (the health_probe_port
# incident: go defaulted 8444, java/rust did not) is a build-failing gap.
# This check runs only when all three languages were generated.
assert_config_defaults_parity() {
  local work_root="$1"

  echo "==> Checking config defaults parity across go, java, rust"

  # Extract DEFAULT constant names from each language's generated grpcconfig helpers,
  # normalized to UPPER_SNAKE_CASE for comparison.
  extract_go_defaults() {
    local helpers
    helpers="$(find "$work_root/go" -type f -name 'helpers.go' -path '*/grpcconfig/*' -print -quit)"
    [[ -n "$helpers" ]] || die "parity: missing Go grpcconfig helpers.go"
    grep -oE 'Default[A-Za-z]+' "$helpers" | grep -v 'func\|Defaults' | sort -u | while read -r name; do
      # PascalCase -> UPPER_SNAKE_CASE: DefaultHealthProbePort -> DEFAULT_HEALTH_PROBE_PORT
      echo "$name" | sed -E 's/([a-z])([A-Z])/\1_\2/g; s/([A-Z]+)([A-Z][a-z])/\1_\2/g' | tr '[:lower:]' '[:upper:]'
    done | sort -u
  }
  extract_java_defaults() {
    local helpers
    helpers="$(find "$work_root/java" -type f -name 'Helpers.java' -path '*/grpcconfig/*' -print -quit)"
    [[ -n "$helpers" ]] || die "parity: missing Java grpcconfig Helpers.java"
    grep -oE 'DEFAULT_[A-Z_]+' "$helpers" | sort -u
  }
  extract_rust_defaults() {
    local helpers
    helpers="$(find "$work_root/rust" -type f -name 'helpers.rs' -path '*/grpcconfig/*' -print -quit)"
    [[ -n "$helpers" ]] || die "parity: missing Rust grpcconfig helpers.rs"
    grep -oE 'DEFAULT_[A-Z_]+' "$helpers" | sort -u
  }

  local go_defaults java_defaults rust_defaults
  go_defaults="$(extract_go_defaults)"
  java_defaults="$(extract_java_defaults)"
  rust_defaults="$(extract_rust_defaults)"

  local go_file java_file rust_file
  go_file="$(mktemp)"
  java_file="$(mktemp)"
  rust_file="$(mktemp)"
  printf '%s\n' "$go_defaults" > "$go_file"
  printf '%s\n' "$java_defaults" > "$java_file"
  printf '%s\n' "$rust_defaults" > "$rust_file"

  local failed=false

  # Compare go vs java
  local go_only_vs_java java_only_vs_go
  go_only_vs_java="$(comm -23 "$go_file" "$java_file")"
  java_only_vs_go="$(comm -13 "$go_file" "$java_file")"
  if [[ -n "$go_only_vs_java" ]]; then
    echo "PARITY FAILURE: defaults in Go but not Java:" >&2
    echo "$go_only_vs_java" | sed 's/^/  /' >&2
    failed=true
  fi
  if [[ -n "$java_only_vs_go" ]]; then
    echo "PARITY FAILURE: defaults in Java but not Go:" >&2
    echo "$java_only_vs_go" | sed 's/^/  /' >&2
    failed=true
  fi

  # Compare go vs rust
  local go_only_vs_rust rust_only_vs_go
  go_only_vs_rust="$(comm -23 "$go_file" "$rust_file")"
  rust_only_vs_go="$(comm -13 "$go_file" "$rust_file")"
  if [[ -n "$go_only_vs_rust" ]]; then
    echo "PARITY FAILURE: defaults in Go but not Rust:" >&2
    echo "$go_only_vs_rust" | sed 's/^/  /' >&2
    failed=true
  fi
  if [[ -n "$rust_only_vs_go" ]]; then
    echo "PARITY FAILURE: defaults in Rust but not Go:" >&2
    echo "$rust_only_vs_go" | sed 's/^/  /' >&2
    failed=true
  fi

  rm -f "$go_file" "$java_file" "$rust_file"

  if [[ "$failed" == true ]]; then
    die "config defaults parity check failed — a language is missing defaults that another has"
  fi

  echo "    PASS config defaults parity ($(echo "$go_defaults" | wc -l | tr -d ' ') constants matched across go/java/rust)"
}

# Config-defaults BEHAVIORAL parity: verify that each language's generated
# defaults function actually SETS every field that has a DEFAULT_* constant.
# The constant-name parity above catches "constant missing"; this check catches
# "constant generated but defaults function never applies it" — the exact
# dead-code class that left health_probe_port at 0 on the live fleet.
assert_config_defaults_behavioral() {
  local work_root="$1"

  echo "==> Checking config defaults BEHAVIORAL parity across go, java, rust"

  # For each language, extract field names that are SET inside the defaults
  # function (not just declared as constants). This proves the generated code
  # would actually apply the default at runtime.

  # Go: ConfigDefaults function body contains "v.FieldName = ..."
  extract_go_defaults_applied() {
    local helpers
    helpers="$(find "$work_root/go" -type f -name 'helpers.go' -path '*/grpcconfig/*' -print -quit)"
    [[ -n "$helpers" ]] || die "behavioral: missing Go grpcconfig helpers.go"
    # Extract field assignments inside ConfigDefaults: "v.SomeName = ..."
    sed -n '/^func ConfigDefaults/,/^func /p' "$helpers" \
      | grep -oE 'v\.[A-Z][A-Za-z]+ =' \
      | sed 's/v\.//; s/ =//' \
      | sort -u
  }

  # Java: configDefaults function body contains "builder.setSomeName(...)"
  extract_java_defaults_applied() {
    local helpers
    helpers="$(find "$work_root/java" -type f -name 'Helpers.java' -path '*/grpcconfig/*' -print -quit)"
    [[ -n "$helpers" ]] || die "behavioral: missing Java grpcconfig Helpers.java"
    # Extract setter calls inside configDefaults: "builder.setSomeName(...)"
    sed -n '/public static.*configDefaults/,/^[[:space:]]*public static/p' "$helpers" \
      | grep -oE 'builder\.set[A-Z][A-Za-z]+' \
      | sed 's/builder\.set//' \
      | sort -u
  }

  # Rust: config_defaults function body contains "msg.some_name = ..."
  extract_rust_defaults_applied() {
    local helpers
    helpers="$(find "$work_root/rust" -type f -name 'helpers.rs' -path '*/grpcconfig/*' -print -quit)"
    [[ -n "$helpers" ]] || die "behavioral: missing Rust grpcconfig helpers.rs"
    # Extract field assignments inside config_defaults: "msg.some_name = ..."
    sed -n '/pub fn config_defaults/,/^pub fn /p' "$helpers" \
      | grep -oE 'msg\.[a-z_]+ =' \
      | sed 's/msg\.//; s/ =//' \
      | sort -u
  }

  # Normalize field names to UPPER_SNAKE_CASE for cross-language comparison.
  pascal_to_upper_snake() {
    sed -E 's/([a-z])([A-Z])/\1_\2/g; s/([A-Z]+)([A-Z][a-z])/\1_\2/g' | tr '[:lower:]' '[:upper:]'
  }
  snake_to_upper() {
    tr '[:lower:]' '[:upper:]'
  }

  local go_applied java_applied rust_applied
  go_applied="$(extract_go_defaults_applied | pascal_to_upper_snake | sort -u)"
  java_applied="$(extract_java_defaults_applied | pascal_to_upper_snake | sort -u)"
  rust_applied="$(extract_rust_defaults_applied | snake_to_upper | sort -u)"

  local go_file java_file rust_file
  go_file="$(mktemp)"
  java_file="$(mktemp)"
  rust_file="$(mktemp)"
  printf '%s\n' "$go_applied" > "$go_file"
  printf '%s\n' "$java_applied" > "$java_file"
  printf '%s\n' "$rust_applied" > "$rust_file"

  local failed=false

  # Go vs Java behavioral parity
  local go_only java_only
  go_only="$(comm -23 "$go_file" "$java_file")"
  java_only="$(comm -13 "$go_file" "$java_file")"
  if [[ -n "$go_only" ]]; then
    echo "BEHAVIORAL PARITY FAILURE: defaults APPLIED in Go but not Java:" >&2
    echo "$go_only" | sed 's/^/  /' >&2
    failed=true
  fi
  if [[ -n "$java_only" ]]; then
    echo "BEHAVIORAL PARITY FAILURE: defaults APPLIED in Java but not Go:" >&2
    echo "$java_only" | sed 's/^/  /' >&2
    failed=true
  fi

  # Go vs Rust behavioral parity
  go_only="$(comm -23 "$go_file" "$rust_file")"
  local rust_only
  rust_only="$(comm -13 "$go_file" "$rust_file")"
  if [[ -n "$go_only" ]]; then
    echo "BEHAVIORAL PARITY FAILURE: defaults APPLIED in Go but not Rust:" >&2
    echo "$go_only" | sed 's/^/  /' >&2
    failed=true
  fi
  if [[ -n "$rust_only" ]]; then
    echo "BEHAVIORAL PARITY FAILURE: defaults APPLIED in Rust but not Go:" >&2
    echo "$rust_only" | sed 's/^/  /' >&2
    failed=true
  fi

  # Sanity: the behavioral set must be non-empty (the grpcconfig.Config has
  # many defaults) and must include health_probe_port specifically.
  if ! echo "$go_applied" | grep -q 'HEALTH_PROBE_PORT'; then
    echo "BEHAVIORAL PARITY FAILURE: Go ConfigDefaults does not apply HEALTH_PROBE_PORT" >&2
    failed=true
  fi
  if ! echo "$java_applied" | grep -q 'HEALTH_PROBE_PORT'; then
    echo "BEHAVIORAL PARITY FAILURE: Java configDefaults does not apply HEALTH_PROBE_PORT" >&2
    failed=true
  fi
  if ! echo "$rust_applied" | grep -q 'HEALTH_PROBE_PORT'; then
    echo "BEHAVIORAL PARITY FAILURE: Rust config_defaults does not apply HEALTH_PROBE_PORT" >&2
    failed=true
  fi

  rm -f "$go_file" "$java_file" "$rust_file"

  if [[ "$failed" == true ]]; then
    die "config defaults behavioral parity check failed — a defaults function does not apply a default that should be set"
  fi

  echo "    PASS config defaults behavioral parity (go: $(echo "$go_applied" | wc -l | tr -d ' ') fields, java: $(echo "$java_applied" | wc -l | tr -d ' ') fields, rust: $(echo "$rust_applied" | wc -l | tr -d ' ') fields)"
}

# TLS-degrade seam parity (PC-L-1): every full-service language must emit
# the TLS-degrade seam (fields + report method) so that TLS renewal failures
# surface in the health plane. The seam is unconditional in the template.
assert_tls_degrade_parity() {
  local work_root="$1"

  echo "==> Checking TLS-degrade seam parity across go, java, rust"

  local go_sm java_sm rust_sm
  # NOTE: these patterns must match the REAL generated layout, which is s2sm/:
  #   go   -> model/s2sm/s2sm.go
  #   java -> model/s2sm/ServiceManager.java     (path contains /s2sm/, NOT /sm/)
  #   rust -> model/s2sm/mod.rs                  (NOT service_manager.rs)
  # The first version of this gate looked for 'service_manager.rs' and '*/sm/*' and so
  # could never match anything — it reported SKIP on every run. A parity gate that
  # cannot fire is worse than no gate, because it reads as coverage.
  go_sm="$(find "$work_root/go" -type f -name 's2sm.go' -print -quit)"
  java_sm="$(find "$work_root/java" -type f -name 'ServiceManager.java' -path '*s2sm*' -print -quit)"
  rust_sm="$(find "$work_root/rust" -type f -name 'mod.rs' -path '*s2sm*' -print -quit)"

  # Skip if the fixture does not generate SM files (no spec.service in proto).
  if [[ -z "$go_sm" && -z "$java_sm" && -z "$rust_sm" ]]; then
    # Be explicit that this is NOT coverage. The main current-generation fixture has
    # no (spec.service), so no ServiceManager is generated and this gate cannot run.
    # Real coverage for the TLS-degrade seam is the template test
    # pkg/internal/java/types/tls_degrade_template_test.go, which pins the methods AND
    # both call sites. This gate only becomes meaningful if a multi-language fixture
    # with a service is added.
    echo "    SKIP TLS-degrade seam parity — NOT COVERAGE (fixture generates no ServiceManager;"
    echo "         real coverage is pkg/internal/java/types/tls_degrade_template_test.go)"
    return
  fi

  [[ -n "$go_sm" ]] || die "parity: missing Go s2sm.go (java/rust have SM but go does not)"
  [[ -n "$java_sm" ]] || die "parity: missing Java ServiceManager.java (go/rust have SM but java does not)"
  [[ -n "$rust_sm" ]] || die "parity: missing Rust service_manager.rs (go/java have SM but rust does not)"

  local fail=false
  grep -q 'reportTLSDegraded' "$go_sm" || { echo "FAIL: Go SM missing reportTLSDegraded" >&2; fail=true; }
  grep -q 'reportTLSDegraded' "$java_sm" || { echo "FAIL: Java SM missing reportTLSDegraded" >&2; fail=true; }
  grep -q 'report_tls_degraded' "$rust_sm" || { echo "FAIL: Rust SM missing report_tls_degraded" >&2; fail=true; }

  grep -q 'onTLSDegraded' "$go_sm" || { echo "FAIL: Go SM missing onTLSDegraded" >&2; fail=true; }
  grep -q 'onTLSDegraded' "$java_sm" || { echo "FAIL: Java SM missing onTLSDegraded" >&2; fail=true; }
  grep -q 'on_tls_degraded' "$rust_sm" || { echo "FAIL: Rust SM missing on_tls_degraded" >&2; fail=true; }

  if [[ "$fail" == true ]]; then
    die "TLS-degrade seam parity check failed — a language is missing the health-degrade seam"
  fi

  echo "    PASS TLS-degrade seam parity (all three languages)"
}

# Run parity check only when all three full-service languages were generated.
if [[ ${#languages[@]} -eq 3 ]]; then
  assert_config_defaults_parity "$WORK_ROOT"
  assert_config_defaults_behavioral "$WORK_ROOT"
  assert_tls_degrade_parity "$WORK_ROOT"
fi

# Health-only fixture: exercises the {{else}} server branch in
# service_manager.rs.tmpl (HasTlsGrpc=false, HasC2DGrpc=false).
# The proto-healthonly/ source has a state root (auto_tls=TLS_KEY_TYPE_ED25519)
# but no gRPC services, so the generated service_manager starts a health-only
# TLS listener. pkg uses "healthonly" as SelfPackage so own-states are detected.
# This fixture tests a Rust-specific branch; other generators reject a proto with
# no token_minter, so we only run it for Rust.
if [[ -d "$HEALTHONLY_PROTO_SRC" ]]; then
  for language in "${languages[@]}"; do
    [[ "$language" == "rust" ]] || continue
    dir="$WORK_ROOT/healthonly-$language"
    log="$WORK_ROOT/healthonly-$language.log"
    rm -rf "$dir"
    write_healthonly_config "$dir" "$language"

    echo "==> Generating $language health-only fixture"
    if ! (cd "$dir" && TACPLUS_PYTHON="$PYTHON_BIN" "$TACPLUS_BIN" build --config tacplus.yaml --src "$HEALTHONLY_PROTO_SRC" >"$log" 2>&1); then
      tail -120 "$log" >&2 || true
      die "$language health-only generation failed; log: $log"
    fi
    output_root="$(resolve_output_root "$dir/tacplus")"
    assert_spiffe_soundness "$dir/tacplus"
    assert_healthonly_output "$output_root" "$language"
    echo "==> Compiling $language health-only fixture"
    compile_current_output "$output_root" "$language" "$dir/tacplus" "gitlab.com/example/dummy/healthonly"
    echo "    PASS $language health-only"
  done
fi

if [[ "$KEEP" != true ]]; then
  rm -rf "$WORK_ROOT"
fi

echo "==> Current cross-test generation and compile passed"
