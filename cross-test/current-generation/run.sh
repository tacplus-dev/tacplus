#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
PROTO_SRC="$ROOT_DIR/cross-test/proto"
HEALTHONLY_PROTO_SRC="$ROOT_DIR/cross-test/proto-healthonly"
C2SONLY_PROTO_SRC="$ROOT_DIR/cross-test/proto-c2sonly"
REQUIRESDEVICE_PROTO_SRC="$ROOT_DIR/cross-test/proto-requiresdevice"
# testdata/, not a plain directory: the go tool ignores directories named
# testdata, and this tree is Go source written against the GENERATED module
# (gitlab.com/example/dummy/vault/...), which `go vet ./...` in this repo cannot
# resolve. Anywhere else it breaks the repo vet gate.
SELFTESTS_SRC="$ROOT_DIR/cross-test/current-generation/testdata"
RECORDEGRESS_PROTO_SRC="$ROOT_DIR/cross-test/proto-recordegress"
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
  (cd "$ROOT_DIR" && go generate ./pkg/internal/proto/spec/... && go build -o "$TACPLUS_BIN" .)
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

write_c2sonly_config() {
  local dir="$1"
  local language="$2"

  mkdir -p "$dir"
  cat > "$dir/tacplus.yaml" <<EOF
language: "$language"
pkg: "com.gitlab.example.dummy.c2sonly"
src: "$C2SONLY_PROTO_SRC"
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

write_requiresdevice_config() {
  local dir="$1"
  local language="$2"

  mkdir -p "$dir"
  cat > "$dir/tacplus.yaml" <<EOF
language: "$language"
pkg: "com.gitlab.example.dummy.vault"
src: "$REQUIRESDEVICE_PROTO_SRC"
output-dir: "tacplus"
EOF
}

write_recordegress_config() {
  local dir="$1"
  local language="$2"

  mkdir -p "$dir"
  cat > "$dir/tacplus.yaml" <<EOF
language: "$language"
pkg: "com.gitlab.example.dummy.egressvault"
src: "$RECORDEGRESS_PROTO_SRC"
output-dir: "tacplus"
EOF
}

# The same fixture built as the record plane's HOST — the service declaring
# (spec.record_store), which the trait names in `host`. This is the arrangement
# every real declaration has: the trait lives in its own package, so the plane's
# two services are HOSTED VSERVICES and reach the host through the host tag, not
# through own-package discovery. Go resolved only the own-package half until
# 2026-09-22, so this build emitted clients to the plane and no server for it.
write_recordegress_host_config() {
  local dir="$1"
  local language="$2"

  mkdir -p "$dir"
  cat > "$dir/tacplus.yaml" <<EOF
language: "$language"
pkg: "com.gitlab.example.dummy.egressstratum"
src: "$RECORDEGRESS_PROTO_SRC"
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

# SPIFFE soundness (plan: Soundness Rules): no raw spiffe:// FORMATTING and no
# hand-rolled spiffe:// PARSING in generated code outside sys/spiffe and tests —
# printf verbs, format!/{} and ${} interpolation, f-strings, string concatenation
# onto a spiffe literal, prefix comparisons, and URI path splitting. All SPIFFE
# construction and parsing must go through the generated sys/spiffe helpers.
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

  # Formatting is only half the rule: hand-rolled PARSING (prefix comparison,
  # URI path splitting) is the other half, and the repo probe already owns those
  # patterns. Run that same rule set over the generated output rather than
  # keeping a second copy of the patterns here, so the two cannot drift.
  if ! (cd "$ROOT_DIR" && go run ./cmd/soundness -spiffe-scan "$output"); then
    die "hand-rolled SPIFFE parsing found in generated output — use the generated spiffe helpers"
  fi
}

dump_step_log() {
  local log="$1"
  if [[ -f "$log" ]]; then
    cat "$log" >&2
    return
  fi
  # The log is missing, which means the step never ran: bash could not create
  # the redirect target, failed the command before it started, and the `cat`
  # that used to sit here then died with "No such file or directory" — burying
  # the real cause (this is what killed the gate on 21 Aug). Name the actual
  # problem instead.
  local dir
  dir="$(dirname "$log")"
  echo "step failed before it could write $log — the command never ran" >&2
  if [[ ! -d "$dir" ]]; then
    echo "  cause: log directory does not exist: $dir" >&2
  elif [[ ! -w "$dir" ]]; then
    echo "  cause: log directory is not writable: $dir" >&2
  else
    echo "  log directory exists and is writable: $dir" >&2
    echo "  cause: the redirect failed for another reason (out of disk? see any bash redirection error above)" >&2
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
      dump_step_log "$classpath_log"
      return 1
    }
  else
    (cd "$compile_dir" && mvn -q dependency:build-classpath -Dmdep.outputFile="$classpath_file") >"$classpath_log" 2>&1 || {
      dump_step_log "$classpath_log"
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
  for pattern in '*/chassis/spiffe/SpiffeVectorsTest.java' '*/chassis/x509/ImmutableCertChainTest.java' '*/chassis/grpcserver/AuthorizerBehaviorTest.java' '*/chassis/collection/CollectionRuntimeTest.java' '*/chassis/approval/ApprovalCanonicalTest.java' '*/chassis/grpcconfig/ConfigDefaultsTest.java' '*/chassis/grpcconfig/RedactionInstalledTest.java' '*/chassis/logger/LogbackConfiguratorSecurityTest.java' '*/chassis/diskstore/DiskStoreTest.java'; do
    source_file="$(find "$output/src/main/java" -type f -path "$pattern" -print -quit)"
    [[ -n "$source_file" ]] || die "missing generated Java self-test source: $pattern"
    package_name="$(awk '/^package /{gsub(";","",$2); print $2; exit}' "$source_file")"
    [[ -n "$package_name" ]] || die "could not determine Java package for $source_file"
    class_name="$package_name.$(basename "$source_file" .java)"
    SPIFFE_VECTOR_FIXTURE="$ROOT_DIR/cross-test/fixtures/spiffe_vectors.json" "$java_cmd" --sun-misc-unsafe-memory-access=allow -cp "$cp" "$class_name" >>"$test_log" 2>&1 || {
      dump_step_log "$test_log"
      return 1
    }
  done
  # The java sys walk copies ALL lib templates unconditionally, so
  # ServiceConnectionTest is emitted for every java build (go emits its
  # serviceconnection chassis unconditionally too, since 53955c71; the go leg
  # asserts that separately). Run its fail-closed/fail-fast self-test
  # (NEW-18/NEW-21). The `if` is defensive; in practice it is always present.
  local sc_test sc_class
  sc_test="$(find "$output/src/main/java" -type f -path '*/chassis/serviceconnection/ServiceConnectionTest.java' -print -quit)"
  if [[ -n "$sc_test" ]]; then
    sc_class="$(awk '/^package /{gsub(";","",$2); print $2; exit}' "$sc_test").$(basename "$sc_test" .java)"
    "$java_cmd" --sun-misc-unsafe-memory-access=allow -cp "$cp" "$sc_class" >>"$test_log" 2>&1 || {
      dump_step_log "$test_log"
      return 1
    }
  fi
  # The record-egress fixture's user-and-listed-service self-test, copied in by
  # install_user_and_listed_service_selftest (which dies if it cannot), so it is
  # present exactly in that fixture's build.
  local uls_test uls_class
  uls_test="$(find "$output/src/main/java" -type f -path '*/chassis/grpcserver/UserAndListedServiceTest.java' -print -quit)"
  if [[ -n "$uls_test" ]]; then
    uls_class="$(awk '/^package /{gsub(";","",$2); print $2; exit}' "$uls_test").$(basename "$uls_test" .java)"
    "$java_cmd" --sun-misc-unsafe-memory-access=allow -cp "$cp" "$uls_class" >>"$test_log" 2>&1 || {
      dump_step_log "$test_log"
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
      dump_step_log "$log_dir/rust-spiffe-test.log"
      return 1
    }
  assert_clean_log "$log_dir/rust-spiffe-test.log"

  # SEC-1: the x509 crate's tests pin verify_service_name (the SPIFFE identity
  # pin behind ServiceNameServerVerifier) with certs minted by the crate's own
  # signer. Without this step a revert of the pin (or of the tests) is silent —
  # no other gate runs rust x509 tests.
  [[ -f "$output/chassis/x509/Cargo.toml" ]] || die "missing generated Rust x509 crate"
  (cd "$output" && cargo test -p x509) >"$log_dir/rust-x509-test.log" 2>&1 || {
      dump_step_log "$log_dir/rust-x509-test.log"
      return 1
    }
  assert_clean_log "$log_dir/rust-x509-test.log"

  # SEC-1 (verifier leg): SpiffeServerCertVerifier construction fail-loud (OF-12
  # malformed trust domain) + build_root_store parsing, minted via the x509
  # crate's signer (dev-dependency of the servicemanager crate).
  [[ -f "$output/chassis/servicemanager/Cargo.toml" ]] || die "missing generated Rust servicemanager crate"
  (cd "$output" && cargo test -p servicemanager) >"$log_dir/rust-servicemanager-test.log" 2>&1 || {
      dump_step_log "$log_dir/rust-servicemanager-test.log"
      return 1
    }
  assert_clean_log "$log_dir/rust-servicemanager-test.log"

  # SEC-2: the grpcconfig crate's loader test verifies that load_config() calls
  # logger::Logger::default().install(), catching the "generated but never wired"
  # defect where credential redaction exists but is never installed on the startup path.
  [[ -f "$output/chassis/grpcconfig/Cargo.toml" ]] || die "missing generated Rust grpcconfig crate"
  (cd "$output" && cargo test -p grpcconfig) >"$log_dir/rust-grpcconfig-test.log" 2>&1 || {
      dump_step_log "$log_dir/rust-grpcconfig-test.log"
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
      dump_step_log "$log_dir/rust-grpcserver-test.log"
      return 1
    }
  assert_clean_log "$log_dir/rust-grpcserver-test.log"

  # PC-C-2: the collection crate's page-token codec tests pin the
  # cross-language byte-identity vectors (shared verbatim with the go and java
  # codec tests). Never previously executed by any gate (cargo check only).
  [[ -f "$output/chassis/collection/Cargo.toml" ]] || die "missing generated Rust collection crate"
  (cd "$output" && cargo test -p collection) >"$log_dir/rust-collection-test.log" 2>&1 || {
      dump_step_log "$log_dir/rust-collection-test.log"
      return 1
    }
  assert_clean_log "$log_dir/rust-collection-test.log"

  # (spec.rpc).requires_device: the approval crate carries the cross-language
  # canonicalization vector — the GENERATOR's own emitted code, run against the
  # same pinned canonical bytes and SHA-256 that Go's and Java's tests assert.
  # Rust generates the canonicalization rather than reflecting over a descriptor,
  # so this is the only place its bytes are proven equal to the other two.
  [[ -f "$output/chassis/approval/Cargo.toml" ]] || die "missing generated Rust approval crate"
  (cd "$output" && cargo test -p approval) >"$log_dir/rust-approval-test.log" 2>&1 || {
      dump_step_log "$log_dir/rust-approval-test.log"
      return 1
    }
  assert_clean_log "$log_dir/rust-approval-test.log"

  # PC-L-7: the diskstore crate's tests (compress/path/snapshot/redolog/
  # schedule) existed in the templates but were never executed by any gate
  # (cargo check only).
  [[ -f "$output/chassis/diskstore/Cargo.toml" ]] || die "missing generated Rust diskstore crate"
  (cd "$output" && cargo test -p diskstore) >"$log_dir/rust-diskstore-test.log" 2>&1 || {
      dump_step_log "$log_dir/rust-diskstore-test.log"
      return 1
    }
  assert_clean_log "$log_dir/rust-diskstore-test.log"

  # The logger crate's credential-redaction tests. These existed in the templates
  # and were executed by NO gate (cargo check only), so rust's redaction had no
  # runtime coverage at all while go and java both did. That is how the JSON
  # structured-field gap survived in rust: redaction runs on already-encoded
  # output, every test used the `key: value` header shape, and nothing ran them.
  #
  # --lib restricts this to the unit tests. It is NOT a claim that the crate's
  # doc examples pass: six of them do not compile, because they import a bare
  # `types` crate that has never existed (the real dependency is
  # <crate-base>_types). They have been broken since they were written and were
  # invisible while no gate ran this crate. Fixing them is separate work; this
  # gate deliberately does not pretend to cover them.
  [[ -f "$output/chassis/logger/Cargo.toml" ]] || die "missing generated Rust logger crate"
  (cd "$output" && cargo test -p logger --lib) >"$log_dir/rust-logger-test.log" 2>&1 || {
      dump_step_log "$log_dir/rust-logger-test.log"
      return 1
    }
  assert_clean_log "$log_dir/rust-logger-test.log"

  # Behavioral config_defaults test: empty Config → health_probe_port == 8444.
  # Covers the state-authority (kernel) path where no typed-config fetch occurs.
  [[ -f "$output/chassis/grpcconfig/Cargo.toml" ]] || die "missing generated Rust grpcconfig crate"
  (cd "$output" && cargo test -p grpcconfig) >"$log_dir/rust-grpcconfig-test.log" 2>&1 || {
      dump_step_log "$log_dir/rust-grpcconfig-test.log"
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
    <!-- Generated MCP dispatch (every c2s service with a user rpc) is written
         against the official MCP Java SDK; see pkg/internal/java/types/mcp_dispatch_gen.go. -->
    <dependency>
      <groupId>io.modelcontextprotocol.sdk</groupId>
      <artifactId>mcp</artifactId>
      <version>2.0.1</version>
    </dependency>
  </dependencies>
</project>
EOF
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
      # NEW-22: 53955c71 made the go lib walk unconditional, so the
      # serviceconnection chassis is emitted for every go build. Nothing
      # asserted its PRESENCE — its absence still compiles, so a revert of that
      # change was silent. Java has the equivalent assert (ServiceConnectionTest
      # in run_java_generated_self_tests).
      local go_sc
      go_sc="$(find "$output" -type f -path '*/chassis/serviceconnection/serviceconnection.go' -print -quit)"
      [[ -n "$go_sc" ]] || die "missing generated Go serviceconnection chassis: chassis/serviceconnection/serviceconnection.go"

      write_go_compile_mod "$log_dir/go.mod" "$go_module"
      (cd "$log_dir" && go mod tidy && SPIFFE_VECTOR_FIXTURE="$ROOT_DIR/cross-test/fixtures/spiffe_vectors.json" go test ./...) >"$log_dir/go-test.log" 2>&1 || {
        dump_step_log "$log_dir/go-test.log"
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
          dump_step_log "$log_dir/maven-compile.log"
          return 1
        }
      else
        (cd "$compile_dir" && mvn -q -DskipTests compile) >"$log_dir/maven-compile.log" 2>&1 || {
          dump_step_log "$log_dir/maven-compile.log"
          return 1
        }
      fi
      assert_clean_log "$log_dir/maven-compile.log"
      run_java_generated_self_tests "$output" "$compile_dir" "$log_dir"
      ;;
    python)
      if [[ -n "$PYTHON_BIN" ]]; then
        "$PYTHON_BIN" -m compileall -q "$base" >"$log_dir/python-compileall.log" 2>&1 || {
          dump_step_log "$log_dir/python-compileall.log"
          return 1
        }
      else
        python3 -m compileall -q "$base" >"$log_dir/python-compileall.log" 2>&1 || {
          dump_step_log "$log_dir/python-compileall.log"
          return 1
        }
      fi
      assert_clean_log "$log_dir/python-compileall.log"
      ;;
    rust)
      if [[ -f "$output/Cargo.toml" ]]; then
        (cd "$output" && CARGO_TARGET_DIR="$output/target" cargo check) >"$log_dir/cargo-check.log" 2>&1 || {
          dump_step_log "$log_dir/cargo-check.log"
          return 1
        }
        assert_clean_log "$log_dir/cargo-check.log"
        run_rust_spiffe_self_tests "$output" "$log_dir"
      fi
      ;;
    typescript)
      if [[ -f "$output/package.json" ]]; then
        (cd "$output" && npm install && npm run build) >"$log_dir/npm-build.log" 2>&1 || {
          dump_step_log "$log_dir/npm-build.log"
          return 1
        }
        assert_clean_log "$log_dir/npm-build.log"
      fi
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

# assert_c2sonly_output checks that the C2S-only fixture actually selected the
# NO-token-exchanger chassis variants. Without this the leg silently degrades: if
# a future change makes this proto shape discover an exchanger, the tree still
# compiles and the variant this fixture exists to compile stops being built.
assert_c2sonly_output() {
  local output="$1"
  local language="$2"

  case "$language" in
    go)
      local im="$output/chassis/service_im/service_im.go"
      [[ -f "$im" ]] || die "c2s-only fixture: chassis/service_im/service_im.go not found under $output"
      grep -q 'errNoTokenSource' "$im" \
        || die "c2s-only fixture: $im is not the no-token-exchanger variant (no errNoTokenSource); the fixture no longer exercises the inline chassis sources"
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

# assert_singleton_fetch_defaults_before_validate reads the ONE generated
# singleton Fetch in the requires-device fixture and pins the order of the two
# calls inside it: apply this type's generated defaults, THEN validate. The
# reverse order is what crash-looped scottyagent (Rust) and stratum-2 (Java) on
# 2026-09-23 — a singleton field the seeded state omits because it carries a
# (spec.default_*) arrived zero and failed its own (spec.assert_*), and startup
# burned the whole config_fetch_timeout on a value that should have passed
# (fixed in 526feb6f / bba83f49). The ordering lives in five independent
# per-language templates, so a revert in one language is invisible to the others
# and to any single-language behavioural test; this is the leg that sees it.
#
# $1 file  $2 awk range start regex  $3 awk range end regex  $4 defaults regex  $5 validate regex
assert_singleton_fetch_defaults_before_validate() {
  local file="$1" start_re="$2" end_re="$3" defaults_re="$4" validate_re="$5"
  local body defaults_line validate_line

  body="$(awk "/$start_re/,/$end_re/" "$file")"
  [[ -n "$body" ]] || die "singleton fetch: could not locate the Fetch body in $file (start: $start_re)"

  defaults_line="$(printf '%s\n' "$body" | grep -nE "$defaults_re" | head -1 | cut -d: -f1)"
  validate_line="$(printf '%s\n' "$body" | grep -nE "$validate_re" | head -1 | cut -d: -f1)"

  [[ -n "$defaults_line" ]] \
    || die "singleton fetch: $file does not apply generated defaults in Fetch — this is the exact 2026-09-23 startup crash (526feb6f/bba83f49)"
  [[ -n "$validate_line" ]] \
    || die "singleton fetch: $file does not validate the fetched value in Fetch — defaults must not replace validation"
  [[ "$defaults_line" -lt "$validate_line" ]] \
    || die "singleton fetch: $file validates BEFORE applying defaults (defaults at body line $defaults_line, validate at $validate_line) — reverts 526feb6f/bba83f49"
}

# assert_requiresdevice_singleton_state checks that ServiceHealth.lock_settings —
# the only plain (non-repeated, non-topic) singleton boundary anywhere in
# cross-test/ — actually generated its state-singleton Fetch, and generated it
# with defaults applied before validation. Until this fixture carried the field,
# no generate-and-compile gate in the tree emitted a singleton Fetch at all, so
# the whole boundary kind was covered only by template render tests.
assert_requiresdevice_singleton_state() {
  local output="$1"
  local language="$2"
  local state=""

  case "$language" in
    go)
      state="$(find "$output" -type f -path '*/state/platform/service_health/state.go' -print -quit)"
      ;;
    java)
      state="$(find "$output" -type f -path '*/state/platform/*/servicehealth/StateClient.java' -print -quit)"
      ;;
    rust)
      state="$(find "$output" -type f -path '*/state/platform/service_health/state.rs' -print -quit)"
      ;;
    *)
      die "requires-device singleton state: unsupported language: $language"
      ;;
  esac

  [[ -n "$state" ]] \
    || die "requires-device fixture ($language): generated ServiceHealth state client not found under $output"

  case "$language" in
    go)
      grep -q 'func (w \*LockSettingsWrapper) Fetch(' "$state" \
        || die "requires-device fixture (go): the lock_settings singleton generated no Fetch in $state"
      assert_singleton_fetch_defaults_before_validate "$state" \
        'func \(w \*LockSettingsWrapper\) Fetch\(' '^}' \
        'LockSettingsDefaults\(value\)' 'ValidateLockSettings\(value\)'
      ;;
    rust)
      grep -q 'pub async fn fetch(&self) -> Result<LockSettings' "$state" \
        || die "requires-device fixture (rust): the lock_settings singleton generated no fetch in $state"
      assert_singleton_fetch_defaults_before_validate "$state" \
        'pub async fn fetch\(&self\) -> Result<LockSettings' '^    \}' \
        'lock_settings_defaults\(&mut value\)' 'value\.validate\(\)'
      ;;
    java)
      grep -q 'LockSettings fetchLockSettings()' "$state" \
        || die "requires-device fixture (java): the lock_settings singleton generated no fetch in $state"
      assert_singleton_fetch_defaults_before_validate "$state" \
        'public .*LockSettings fetchLockSettings\(' '^    \}' \
        'lockSettingsDefaults\(' 'validateLockSettings\('
      ;;
  esac
}

# install_go_state_singleton_selftest drops the fixture's hand-written Go
# self-test into the generated ServiceHealth state package. It has to live IN
# that package: Fetch's retry loop, its defaults call and the stub it calls are
# all package-internal, and nothing outside can reach them. `go test ./...` in
# compile_current_output then runs it as part of the gate.
install_go_state_singleton_selftest() {
  local output="$1"
  local src="$SELFTESTS_SRC/go/requiresdevice_state_singleton_fetch_test.go"
  local pkg_dir

  [[ -f "$src" ]] || die "missing requires-device Go state-singleton self-test: $src"
  pkg_dir="$(dirname "$(find "$output" -type f -path '*/state/platform/service_health/state.go' -print -quit)")"
  [[ -d "$pkg_dir" ]] || die "requires-device fixture (go): generated service_health package not found under $output"
  cp "$src" "$pkg_dir/"
}

# install_user_and_listed_service_selftest pins the user-AND-listed-service rpc
# (EgressVaultApi/CreateVaultNote: entitled users plus auth_services ["notary"]).
# It asserts the generated wrapper authorizes the rpc through the Authorizer and
# registers its auth_services, then drops the hand-written test that drives those
# same two calls into the generated grpcserver chassis, where it can reach the
# fixture's real grant catalog. compile_current_output then runs it as part of the
# gate: `go test ./...` (go), `cargo test -p grpcserver` (rust), and
# run_java_generated_self_tests (java).
install_user_and_listed_service_selftest() {
  local output="$1"
  local language="$2"
  local api dest

  case "$language" in
    go)
      api="$(find "$output" -type f -path '*/servers/services/egressvault/egress_vault_api.go' -print -quit)"
      [[ -n "$api" ]] || die "record-egress fixture (go): generated EgressVaultApi wrapper not found under $output"
      grep -qF 'p.authorize(ctx, "/services"+types.EgressVaultApi_CreateVaultNote_FullMethodName, req)' "$api" \
        || die "record-egress fixture (go): CreateVaultNote is not authorized through the Authorizer in $api"
      grep -qF 'auth.SetMethodAuth("egressvault.EgressVaultApi/CreateVaultNote", []string{"notary"})' "$api" \
        || die "record-egress fixture (go): CreateVaultNote's auth_services were not registered in $api"
      cp "$SELFTESTS_SRC/go/user_and_listed_service_test.go" "$output/chassis/grpcserver/"
      ;;
    java)
      api="$(find "$output" -type f -path '*/servers/services/egressvault/EgressVaultApi.java' -print -quit)"
      [[ -n "$api" ]] || die "record-egress fixture (java): generated EgressVaultApi wrapper not found under $output"
      grep -qF 'callerIdentity = checkAuth("EgressVaultApi/CreateVaultNote", request);' "$api" \
        || die "record-egress fixture (java): CreateVaultNote is not authorized through the Authorizer in $api"
      grep -qF 'authorizer.setMethodAuth("EgressVaultApi/CreateVaultNote", java.util.List.of("notary"));' "$api" \
        || die "record-egress fixture (java): CreateVaultNote's auth_services were not registered in $api"
      dest="$(dirname "$(find "$output/src/main/java" -type f -path '*/chassis/grpcserver/AuthorizerBehaviorTest.java' -print -quit)")"
      [[ -d "$dest" ]] || die "record-egress fixture (java): generated chassis/grpcserver package not found under $output"
      cp "$SELFTESTS_SRC/java/UserAndListedServiceTest.java" "$dest/"
      ;;
    rust)
      api="$(find "$output" -type f -path '*/servers/services/egressvault/egress_vault_api.rs' -print -quit)"
      [[ -n "$api" ]] || die "record-egress fixture (rust): generated EgressVaultApi wrapper not found under $output"
      grep -qF 'self.authorize_method("/services/egressvault.EgressVaultApi/CreateVaultNote", &request)' "$api" \
        || die "record-egress fixture (rust): CreateVaultNote is not authorized through the Authorizer in $api"
      grep -qF 'auth.set_method_auth("egressvault.EgressVaultApi/CreateVaultNote", &["notary"]);' "$api" \
        || die "record-egress fixture (rust): CreateVaultNote's auth_services were not registered in $api"
      mkdir -p "$output/chassis/grpcserver/tests"
      cp "$SELFTESTS_SRC/rust/user_and_listed_service_test.rs" "$output/chassis/grpcserver/tests/"
      ;;
  esac
}

# assert_requiresdevice_output checks that the (spec.rpc).requires_device gate was
# actually GENERATED, not merely that a tree with the annotation in it compiled.
# Every one of these has failed at least once during development in at least one
# language, and each failure compiled:
#   - the wrapper hashing the request AFTER consuming it,
#   - the broker call naming the unqualified method,
#   - the caller-side hash helper resolving to a function that was never emitted,
#   - the broker's S2S client not being auto-injected at all.
# The ungated sibling rpc (GetSecret) is here for the same reason: a gate spliced
# onto every unary method instead of the declared one also compiles.
assert_requiresdevice_output() {
  local output="$1"
  local language="$2"
  local wrapper="" client="" sm="" broker_client=""

  case "$language" in
    go)
      wrapper="$(find "$output" -type f -path '*/servers/services/vault/service.go' -print -quit)"
      client="$(find "$output" -type f -path '*/clients/services/ledger/service.go' -print -quit)"
      sm="$(find "$output" -type f -path '*/s2sm/s2sm.go' -print -quit)"
      broker_client="$(find "$output" -type f -path '*/clients/services/devicemint/*.go' -print -quit)"
      ;;
    java)
      wrapper="$(find "$output" -type f -path '*/servers/services/vault/VaultService.java' -print -quit)"
      client="$(find "$output" -type f -name 'LedgerServiceGrpcClient.java' -print -quit)"
      sm="$(find "$output" -type f -path '*/s2sm/ServiceManager.java' -print -quit)"
      broker_client="$(find "$output" -type f -name 'DeviceMintGrpcClient.java' -print -quit)"
      ;;
    rust)
      wrapper="$(find "$output" -type f -path '*/servers/services/vault/service.rs' -print -quit)"
      client="$(find "$output" -type f -path '*/clients/services/ledger/service.rs' -print -quit)"
      sm="$(find "$output" -type f -path '*/s2sm/mod.rs' -print -quit)"
      broker_client="$(find "$output" -type f -path '*/clients/services/devicemint/*.rs' -print -quit)"
      ;;
    *)
      die "requires-device fixture: unsupported language: $language"
      ;;
  esac

  [[ -n "$wrapper" ]] || die "requires-device fixture ($language): generated vault server wrapper not found under $output"
  [[ -n "$client" ]] || die "requires-device fixture ($language): generated ledger client not found under $output"
  [[ -n "$sm" ]] || die "requires-device fixture ($language): generated service manager not found under $output"
  # The broker's S2S client is DERIVED from the annotation — no
  # (spec.grpc_s2s_client) names devicemint anywhere in proto-requiresdevice/.
  [[ -n "$broker_client" ]] || die "requires-device fixture ($language): the approval broker's S2S client was not auto-injected (no generated devicemint client under $output)"

  # The gate is on the declared rpc, keyed by the WIRE-qualified method, and on
  # that rpc only. Match the broker CALL SITE, not the bare method key: the key
  # also appears in the per-method auth table for every rpc, gated or not.
  local gate_re='roker_?[Aa]pproval\(.*vault\.VaultService/'
  local gates
  gates="$(grep -cE "$gate_re" "$wrapper" || true)"
  [[ "$gates" == "2" ]] \
    || die "requires-device fixture ($language): expected exactly two approval gate call sites in $wrapper, found $gates (DeleteSecret's unconditional gate and UpdateSecret's conditional one; more means a gate was spliced onto ungated methods too, fewer means one was not generated)"
  grep -qE "${gate_re}DeleteSecret" "$wrapper" \
    || die "requires-device fixture ($language): the approval gate in $wrapper does not name the wire-qualified vault.VaultService/DeleteSecret"
  grep -qE "${gate_re}UpdateSecret" "$wrapper" \
    || die "requires-device fixture ($language): the requires_device_when gate in $wrapper does not name the wire-qualified vault.VaultService/UpdateSecret"

  # (spec.rpc).requires_device_when: the SAME gate body, reached only when the
  # named field holds the named value. The comparison is by enum NUMBER in every
  # language (go: req.Effect, java: request.getEffectValue(), rust:
  # request.get_ref().effect), resolved from the descriptor at generation time.
  grep -qE '[Ee]ffect(Value\(\))? == 2' "$wrapper" \
    || die "requires-device fixture ($language): UpdateSecret's gate is not wrapped in the declared condition (effect == UPDATE_EFFECT_DELEGATION) in $wrapper"
  local conds
  conds="$(grep -cE '[Ee]ffect(Value\(\))? == 2' "$wrapper" || true)"
  [[ "$conds" == "1" ]] \
    || die "requires-device fixture ($language): expected exactly one conditional gate in $wrapper, found $conds"

  # The service manager reaches the broker over the generated client.
  grep -q 'BrokerPendingApproval\|broker_pending_approval' "$sm" \
    || die "requires-device fixture ($language): the service manager does not call the approval broker ($sm)"

  # The caller-side half: the hash a proposer computes comes from the SAME
  # canonicalization the server gate recomputes.
  grep -qi 'DeleteEntryApprovalHash\|delete_entry_approval_hash' "$client" \
    || die "requires-device fixture ($language): the caller-side request-hash helper is missing from $client"
  grep -qi 'DeleteEntryWithApproval\|delete_entry_with_approval' "$client" \
    || die "requires-device fixture ($language): the caller-side approval-attach helper is missing from $client"

  assert_no_legacy_fixture_output "$output"
}


# assert_recordegress_output checks that the record EGRESS ROW FILTERS were
# actually GENERATED on the seams that carry pinned rows, and NOT on the seam
# that carries none. Until 04a24db3 the Go wrapper rendered none of them and
# refused generation outright wherever one was owed, so this fixture is the only
# place the three languages' filters are generated and COMPILED side by side.
#
# The counts are exact and identical in all three languages, which is the point:
# a language that drops one seam, or splices the filter onto every method instead
# of the row-bearing ones, changes a count. Both failures compile.
#
#   6 / 2 aborts          — the (spec.store_namespace)/(spec.record).private
#                            backstops. On the hand-written api: unary tenant
#                            row, unary private row, the PAGE window, and the
#                            WATCH seam's snapshot_item and delta frames. On the
#                            generated record reader: the namespace backstop on
#                            its two VaultRow seams.
#   4 / 2 linked-claim reads — the `linked` widening, once per namespace resolve.
#   1 / 0 delegated refusals — the private record's act-claim requirement, on the
#                            hand-written unary reader alone. The generated
#                            client-facing reader carries no private type: its
#                            data path is the plane's peer-facing read, which is
#                            not emitted for one (see the plane assertion below).
# assert_recordegress_host_output checks that the hosted VSERVICE half of server
# resolution actually produced SERVERS for the record query plane, and that the
# two seams the plane alone reaches rendered on them: the (spec.store_namespace)
# tenant backstop with its /system runtime gate on the peer half, and the
# (spec.store_principal) predicate pin on the client half.
#
# A language that resolves only own-package services emits the plane's CLIENTS
# and no server, which compiles — so presence of the server file is the whole
# assertion, and the compile step that follows is what proves it is real.
assert_recordegress_host_output() {
  local output="$1"
  local language="$2"
  local s2s="" c2s=""

  case "$language" in
    go)
      s2s="$(find "$output" -type f -path '*/servers/vservices/egressquery/egress_record_map.go' -print -quit)"
      c2s="$(find "$output" -type f -path '*/servers/vservices/egressquery/egress_record_query.go' -print -quit)"
      ;;
    java)
      s2s="$(find "$output" -type f -path '*/servers/vservices/egressquery/EgressRecordMap.java' -print -quit)"
      c2s="$(find "$output" -type f -path '*/servers/vservices/egressquery/EgressRecordQuery.java' -print -quit)"
      ;;
    rust)
      s2s="$(find "$output" -type f -path '*/servers/vservices/egressquery/egress_record_map.rs' -print -quit)"
      c2s="$(find "$output" -type f -path '*/servers/vservices/egressquery/egress_record_query.rs' -print -quit)"
      ;;
    *)
      die "record-egress host fixture: unsupported language: $language"
      ;;
  esac

  [[ -n "$s2s" ]] \
    || die "record-egress host fixture ($language): the record query plane's PEER half was not generated as a server under $output; the host tag half of server resolution is missing, so the plane's rpcs are unroutable from this language"
  [[ -n "$c2s" ]] \
    || die "record-egress host fixture ($language): the record query plane's CLIENT-FACING half was not generated as a server under $output"

  # The (spec.store_namespace) s2s seam with its /system runtime gate. It is
  # reached by no other surface in the fleet, so a language that never hosts the
  # plane never generates it at all.
  grep -q 'FATAL: (spec.store_namespace) backstop fired' "$s2s" \
    || die "record-egress host fixture ($language): the peer half carries no (spec.store_namespace) backstop"
  grep -q '/system' "$s2s" \
    || die "record-egress host fixture ($language): the peer half carries no /system runtime gate, so a fleet-wide peer would be pinned to its own tenant"

  # The (spec.store_principal) predicate pin on the client-facing half: the
  # plane is per-principal and the wrapper, not the handler, pins it.
  # Go and Java only. RUST RENDERS NO PREDICATE PIN AT ALL on this half — it
  # carries the egress row filters and nothing that pins the predicate before
  # the store is asked. That is a Rust gap, not a fixture one; asserting it for
  # Rust would make this leg fail rather than describe it, exactly as the unary
  # filter-ORDER divergence is described and not asserted. See
  # looper/workers/go-hosted-vservice-resolver-questions.md.
  if [[ "$language" != "rust" ]]; then
    grep -q 'the query plane is per-principal and the predicate is not the caller'"'"'s to choose' "$c2s" \
    || die "record-egress host fixture ($language): the client-facing half carries no (spec.store_principal) predicate pin"
  fi

  assert_no_legacy_fixture_output "$output"
}

assert_recordegress_output() {
  local output="$1"
  local language="$2"
  local api="" records="" abort_re="" linked_re=""

  case "$language" in
    go)
      api="$(find "$output" -type f -path '*/servers/services/egressvault/egress_vault_api.go' -print -quit)"
      records="$(find "$output" -type f -path '*/servers/services/egressvault/records.go' -print -quit)"
      abort_re='os\.Exit\(1\)'
      linked_re='caller\.Token\(\)\.GetLinked\(\)'
      ;;
    java)
      api="$(find "$output" -type f -name 'EgressVaultApi.java' -print -quit)"
      records="$(find "$output" -type f -name 'EgressvaultRecords.java' -print -quit)"
      abort_re='Runtime\.getRuntime\(\)\.halt\(1\)'
      linked_re='Auth\.callerLinkedFromContext\(\)'
      ;;
    rust)
      api="$(find "$output" -type f -path '*/servers/services/egressvault/egress_vault_api.rs' -print -quit)"
      records="$(find "$output" -type f -path '*/servers/services/egressvault/records.rs' -print -quit)"
      abort_re='std::process::abort\(\)'
      linked_re='caller\.as_ref\(\)\.map\(\|c\| c\.linked\(\)\)'
      ;;
    *)
      die "record-egress fixture: unsupported language: $language"
      ;;
  esac

  [[ -n "$api" ]] || die "record-egress fixture ($language): generated EgressVaultApi wrapper not found under $output"
  # The CLIENT-FACING record read plane. It is emitted only because egressvault
  # declares (spec.record_read).c2s, and that declaration was REFUSED for a Go
  # target until the Go egress filters landed — so its presence here is the
  # closed refusal, not decoration. It covers BOTH record types: the tenant-scoped
  # VaultRow, whose read is granted by s2s_read, and the PRIVATE VaultSecretRow,
  # which is forbidden to declare s2s_read and whose c2s read at
  # C2S_SCOPE_PRINCIPAL is therefore the only read it has.
  [[ -n "$records" ]] || die "record-egress fixture ($language): the generated client-facing record reader is missing under $output (the (spec.record_read).c2s plane was not emitted)"

  # THE PRIVATE RECORD HAS NO PEER-FACING READ. This is the assertion the whole
  # private/s2s split rests on, and it is made against the emitted PROTO rather
  # than against any one language's wrapper, because that is where the decision
  # is taken: record_map.go skips a (spec.record).private type when it renders
  # the plane's S2S half, so all three backends agree with no per-language check.
  # The C2S half, which carries the user token the owner pin binds to, must still
  # carry the same type's full lifecycle — the paired control, so a change that
  # suppressed the type everywhere fails here too.
  local plane
  plane="$(find "$output" -type f -name 'egressquery.proto' -print -quit)"
  [[ -n "$plane" ]] || die "record-egress fixture ($language): the generated record query plane proto was not found under $output"
  python3 - "$plane" "$language" <<'PLANE_PY' || die "record-egress fixture ($language): record query plane private/s2s split assertion failed"
import re, sys
path, language = sys.argv[1], sys.argv[2]
text = open(path).read()
bodies = {}
for m in re.finditer(r'^service (\w+) \{(.*?)^\}', text, re.S | re.M):
    bodies[m.group(1)] = m.group(2)
for want in ("EgressRecordMap", "EgressRecordQuery"):
    if want not in bodies:
        sys.exit("record-egress fixture (%s): service %s missing from %s" % (language, want, path))
s2s = re.findall(r'\brpc (\w+)\(', bodies["EgressRecordMap"])
c2s = re.findall(r'\brpc (\w+)\(', bodies["EgressRecordQuery"])
leaked = [r for r in s2s if "VaultSecretRow" in r]
if leaked:
    sys.exit("record-egress fixture (%s): the S2S half (EgressRecordMap) emits %d read rpc(s) for the PRIVATE "
             "record VaultSecretRow (%s); a peer calls that surface with its own service identity, so the owner "
             "pin has no caller principal and every named service could read every principal's rows"
             % (language, len(leaked), ", ".join(leaked)))
if not [r for r in s2s if "VaultRow" in r]:
    sys.exit("record-egress fixture (%s): the S2S half emits no read rpc for the TENANT-SCOPED record VaultRow; "
             "the private skip has over-reached" % language)
if len([r for r in c2s if "VaultSecretRow" in r]) != 8:
    sys.exit("record-egress fixture (%s): the C2S half (EgressRecordQuery) must keep the private record's full "
             "8-rpc query lifecycle (it carries a user token for the pin to bind to); found %d"
             % (language, len([r for r in c2s if "VaultSecretRow" in r])))
# THE PRIVATE RECORD'S KEY IS ON ITS FILTER. A record's key is folded into
# <T>Filter when it declares (spec.record).s2s_read - and a private record is
# forbidden to declare one, so the fold is gated on `private` as well. Without
# it the generated point read compiles against a criterion that is never
# emitted, which is the second of the two gaps that made this shape
# unimplementable. Asserted on the emitted proto, like the split above, because
# that is where the decision is taken.
filters = dict((m.group(1), m.group(2)) for m in
               re.finditer(r'^message (\w+Filter) \{(.*?)^\}', text, re.S | re.M))
for msg, key in (("VaultSecretRowFilter", "secret_id"), ("VaultRowFilter", "row_id")):
    if msg not in filters:
        sys.exit("record-egress fixture (%s): %s missing from %s" % (language, msg, path))
    if not re.search(r'\b%s\s*=' % key, filters[msg]):
        sys.exit("record-egress fixture (%s): %s carries no %s criterion, so the generated point read by key "
                 "has no filter field to set" % (language, msg, key))
PLANE_PY

  local n
  # api 6: two pinned seams over the private row (GetVaultSecretMaterial) plus
  # the namespace backstop on each of the four VaultRow seams. records 6: the
  # namespace backstop on each of the four generated seams (GetVaultRow,
  # GetVaultRowPage, GetVaultSecretRow, GetVaultSecretRowPage) plus the
  # private-owner backstop on the two that carry the PRIVATE row. A principal
  # MISMATCH drops the row and proceeds, so it contributes no abort — only the
  # namespace pin and the private-owner pin abort.
  #
  # Rust adds 4 more in the SAME file: EgressVaultApi is a service_c2s service,
  # so its user RPCs are auto-exposed as MCP tools, and Rust emits that tool
  # dispatch into the wrapper file rather than beside it. The tool call serves
  # the row itself, so each dispatch seam carries the same backstop its gRPC
  # twin does — call_get_vault_row_summary (namespace),
  # call_get_vault_secret_material (namespace + private owner) and
  # call_list_vault_rows_paged (namespace, over the page the tool returns).
  # GetVaultStatus is auto-exposed too and adds none, because its response
  # carries no pinned row; WatchVaultRows is not a tool at all (a server-
  # streaming read has no tool-call frame), which is why this is +4 and not +5.
  local api_aborts=6
  if [[ "$language" == "rust" ]]; then
    api_aborts=10
  fi
  for pair in "$api:$api_aborts" "$records:6"; do
    local f="${pair%:*}" want="${pair##*:}"
    n="$(grep -cE "$abort_re" "$f" || true)"
    [[ "$n" == "$want" ]] \
      || die "record-egress fixture ($language): expected $want egress backstop aborts in $f, found $n (fewer means a seam lost its filter; more means it was spliced onto a method whose response carries no pinned row)"
  done

  # Rust's +3 is the same auto-exposed MCP tool dispatch the abort count above
  # explains: the three seams that resolve a namespace
  # (call_get_vault_row_summary, call_get_vault_secret_material,
  # call_list_vault_rows_paged) resolve it with the same linked widening their
  # gRPC twins do.
  local api_linked=4
  if [[ "$language" == "rust" ]]; then
    api_linked=7
  fi
  for pair in "$api:$api_linked" "$records:4"; do
    local f="${pair%:*}" want="${pair##*:}"
    n="$(grep -cE "$linked_re" "$f" || true)"
    [[ "$n" == "$want" ]] \
      || die "record-egress fixture ($language): expected $want linked-claim reads in $f, found $n (a namespace resolve without the linked widening refuses a caller the other two languages admit)"
  done

  # The fourth caller shape: a SERVICE identity reading the private record with
  # no delegated (act) claim is refused, rather than pinned against an empty
  # owner that would match a row by accident. It lands on the hand-written c2s
  # rpc AND on the two generated seams over the private row, which is what the
  # private record's own (spec.record_read).c2s buys: the same refusal, emitted
  # rather than written. Rust's +1 on the api is the auto-exposed MCP tool
  # dispatch again: the tool that serves the private record refuses a delegated
  # caller with no act claim exactly as its gRPC twin does, so a tool call cannot
  # reach the row by a path the rpc would have closed.
  local want_act api_act=1
  if [[ "$language" == "rust" ]]; then
    api_act=2
  fi
  for pair in "$api:$api_act" "$records:2"; do
    local f="${pair%:*}"
    want_act="${pair##*:}"
    n="$(grep -c 'reads a private record and requires a delegated caller identity (act claim)' "$f" || true)"
    [[ "$n" == "$want_act" ]] \
      || die "record-egress fixture ($language): expected $want_act delegated-identity refusals in $f, found $n"
  done

  # The matching and mismatched caller shapes, spelled out: the row is kept when
  # the caller's namespace matches and the process aborts when it does not. The
  # operator-facing diagnostic is one contract stated once in three generators —
  # a language that reworded it has drifted on the only surface observable from
  # outside the process.
  grep -q 'FATAL: (spec.store_namespace) backstop fired' "$api" \
    || die "record-egress fixture ($language): the namespace backstop diagnostic is missing from $api"
  grep -q 'FATAL: (spec.record).private backstop fired' "$api" \
    || die "record-egress fixture ($language): the private-owner backstop diagnostic is missing from $api"

  # THE PRIVATE RECORD'S READER DIALS THE OTHER HALF OF THE PLANE. This is the
  # first of the two gaps that made a private record's c2s read unimplementable:
  # the typed reader's client was the plane's S2S half for every record, and
  # record_map.go emits no rpcs there for a private type, so the reader compiled
  # against methods that do not exist. A private record reads the C2S half, which
  # keeps the type's full lifecycle because it carries the user token the owner
  # pin binds to; a tenant-scoped record still reads the S2S half. Asserted in
  # Rust alone because the typed reader is a Rust surface: Go and Java generate
  # the wrapper and the Handler interface for this plane, not its data path.
  # The typed record readers over the two records' stored rows, one per
  # (spec.record_read), each borrowing the plane half its record needs: the
  # S2S half for the tenant-scoped VaultRow, the C2S half for the private
  # VaultSecretRow. Go and Java emit the same readers as Rust (2026-10-14).
  if [[ "$language" == "go" ]]; then
    local go_readers
    go_readers="$(find "$output" -type f -path '*/s2sm/record_readers.go' -print -quit)"
    [[ -n "$go_readers" ]] || die "record-egress fixture (go): the typed record readers file was not generated under $output"
    grep -q 'type VaultRowRecordReader struct {' "$go_readers" && grep -A1 'type VaultRowRecordReader struct {' "$go_readers" | grep -q 'EgressRecordMapClient' \
      || die "record-egress fixture (go): VaultRowRecordReader must read the plane's S2S half (EgressRecordMapClient)"
    grep -A1 'type VaultSecretRowRecordReader struct {' "$go_readers" | grep -q 'EgressRecordQueryClient' \
      || die "record-egress fixture (go): VaultSecretRowRecordReader must read the plane's C2S half (EgressRecordQueryClient)"
  fi
  if [[ "$language" == "java" ]]; then
    local java_reader
    java_reader="$(find "$output" -type f -path '*/s2sm/VaultRowRecordReader.java' -print -quit)"
    [[ -n "$java_reader" ]] || die "record-egress fixture (java): VaultRowRecordReader.java was not generated under $output"
    grep -q 'EgressRecordMapGrpcClient client;' "$java_reader" \
      || die "record-egress fixture (java): VaultRowRecordReader must read the plane's S2S half (EgressRecordMapGrpcClient)"
    java_reader="$(find "$output" -type f -path '*/s2sm/VaultSecretRowRecordReader.java' -print -quit)"
    [[ -n "$java_reader" ]] || die "record-egress fixture (java): VaultSecretRowRecordReader.java was not generated under $output"
    grep -q 'EgressRecordQueryGrpcClient client;' "$java_reader" \
      || die "record-egress fixture (java): VaultSecretRowRecordReader must read the plane's C2S half (EgressRecordQueryGrpcClient)"
  fi
  if [[ "$language" == "rust" ]]; then
    local readers
    readers="$(find "$output" -type f -path '*/s2sm/record_readers.rs' -print -quit)"
    [[ -n "$readers" ]] || die "record-egress fixture (rust): the typed record readers file was not generated under $output"
    python3 - "$readers" <<'READERS_PY' || die "record-egress fixture (rust): record reader plane-half assertion failed"
import re, sys
path = sys.argv[1]
text = open(path).read()
want = {"VaultRowRecordReader": "EgressRecordMapGrpcClient",
        "VaultSecretRowRecordReader": "EgressRecordQueryGrpcClient"}
for struct, client in want.items():
    m = re.search(r"pub struct %s<'a> \{\s*client: &'a (\w+)," % struct, text)
    if not m:
        sys.exit("record-egress fixture (rust): %s was not generated in %s" % (struct, path))
    if m.group(1) != client:
        sys.exit("record-egress fixture (rust): %s borrows %s, expected %s "
                 "(a private record reads the plane's C2S half; every other record reads its S2S half)"
                 % (struct, m.group(1), client))
for client in set(want.values()):
    if ("::%s;" % client) not in text:
        sys.exit("record-egress fixture (rust): %s is used but never imported in %s" % (client, path))
READERS_PY
  fi

  # PAIRED CONTROL: GetVaultStatus returns no record rows, so it must carry no
  # filter. Asserted structurally by the exact counts above; asserted by name
  # here so the failure says which method went wrong.
  grep -q 'GetVaultStatus\|getVaultStatus\|get_vault_status' "$api" \
    || die "record-egress fixture ($language): the ungated control method is missing from $api"

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

# C2S-only fixture: the whole server surface is non-S2S, so no authoritative
# token minter is required and the NO-token-exchanger variants of
# chassis/service_im and chassis/servicemanager are generated (inline sources in
# pkg/internal/go/types/stateclient_gen.go, not templates). Nothing else in any
# gate generates that variant, and it shipped un-compilable twice — the NEW-21
# classifier sentinels and the NEW-47 credential-degradation helpers were each an
# unconditional chassis reference the inline source did not declare. Go only:
# java and rust emit ONE class/crate with {{else}} branches inside it, so they
# have no second variant to drift from.
if [[ -d "$C2SONLY_PROTO_SRC" ]]; then
  for language in "${languages[@]}"; do
    [[ "$language" == "go" ]] || continue
    dir="$WORK_ROOT/c2sonly-$language"
    log="$WORK_ROOT/c2sonly-$language.log"
    rm -rf "$dir"
    write_c2sonly_config "$dir" "$language"

    echo "==> Generating $language c2s-only (no-token-exchanger) fixture"
    if ! (cd "$dir" && TACPLUS_PYTHON="$PYTHON_BIN" "$TACPLUS_BIN" build --config tacplus.yaml --src "$C2SONLY_PROTO_SRC" >"$log" 2>&1); then
      tail -120 "$log" >&2 || true
      die "$language c2s-only generation failed; log: $log"
    fi
    output_root="$(resolve_output_root "$dir/tacplus")"
    assert_spiffe_soundness "$dir/tacplus"
    assert_c2sonly_output "$output_root" "$language"
    echo "==> Compiling $language c2s-only fixture"
    compile_current_output "$output_root" "$language" "$dir/tacplus" "gitlab.com/example/dummy/c2sonly"
    echo "    PASS $language c2s-only"
  done
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

# requires-device fixture: the ONLY place (spec.rpc).requires_device is generated
# and compiled by a standing gate. cross-test/proto declares no (spec.service), so
# it is a types-only build and emits none of the three surfaces the gate lands on
# (server wrapper, service manager, S2S clients) — the annotation would generate
# nothing there. This set is a full service build instead: vault is the self
# package and hosts the guarded rpc, devicemint is the authoritative
# (spec.token_minter) hosting the broker in a DIFFERENT package (all three
# backends refuse a self-hosting minter), and ledger is a third package vault
# holds a client to, so the caller-side helpers are compiled too. All three
# languages, because all three implement the gate independently.
if [[ -d "$REQUIRESDEVICE_PROTO_SRC" ]]; then
  for language in "${languages[@]}"; do
    dir="$WORK_ROOT/requiresdevice-$language"
    log="$WORK_ROOT/requiresdevice-$language.log"
    rm -rf "$dir"
    write_requiresdevice_config "$dir" "$language"

    echo "==> Generating $language requires-device fixture"
    if ! (cd "$dir" && TACPLUS_PYTHON="$PYTHON_BIN" "$TACPLUS_BIN" build --config tacplus.yaml --src "$REQUIRESDEVICE_PROTO_SRC" >"$log" 2>&1); then
      tail -120 "$log" >&2 || true
      die "$language requires-device generation failed; log: $log"
    fi
    output_root="$(resolve_output_root "$dir/tacplus")"
    assert_spiffe_soundness "$dir/tacplus"
    assert_requiresdevice_output "$output_root" "$language"
    assert_requiresdevice_singleton_state "$output_root" "$language"
    if [[ "$language" == "go" ]]; then
      install_go_state_singleton_selftest "$output_root"
    fi
    echo "==> Compiling $language requires-device fixture"
    compile_current_output "$output_root" "$language" "$dir/tacplus" "gitlab.com/example/dummy/vault"
    echo "    PASS $language requires-device"
  done
fi


# record-egress fixture: the ONLY place the (spec.store_namespace) /
# (spec.store_principal) / (spec.record).private egress ROW FILTERS are generated
# AND COMPILED by a standing gate. cross-test/proto is types-only, so it emits no
# server wrapper — the one surface the filters land on; cross-test/consumer-
# contract carries records with these annotations but no service that discloses
# them to users, so it never emits a filtered read either. This set is a full
# service build: egressvault owns a tenant-scoped record and a private one and
# reads both on the three c2s seams that have a per-item hook (unary, PAGE,
# WATCH), egressstratum is the (spec.record_store) the trait names as its host,
# and egressquery is the (spec.trait) whose plane makes (spec.record).s2s_read —
# and with it (spec.record_read) — legal at all.
#
# NOT here, deliberately: a pinned row on a Tier-2/Tier-3 SNAPSHOT or a plain
# server stream. goStreamEgressRefusal and its Rust twin refuse those by design
# (no per-item hook to ride), so a fixture that carried one would assert the
# refusal is a bug.
if [[ -d "$RECORDEGRESS_PROTO_SRC" ]]; then
  for language in "${languages[@]}"; do
    dir="$WORK_ROOT/recordegress-$language"
    log="$WORK_ROOT/recordegress-$language.log"
    rm -rf "$dir"
    write_recordegress_config "$dir" "$language"

    echo "==> Generating $language record-egress fixture"
    if ! (cd "$dir" && TACPLUS_PYTHON="$PYTHON_BIN" "$TACPLUS_BIN" build --config tacplus.yaml --src "$RECORDEGRESS_PROTO_SRC" >"$log" 2>&1); then
      tail -120 "$log" >&2 || true
      die "$language record-egress generation failed; log: $log"
    fi
    output_root="$(resolve_output_root "$dir/tacplus")"
    assert_spiffe_soundness "$dir/tacplus"
    assert_recordegress_output "$output_root" "$language"
    install_user_and_listed_service_selftest "$output_root" "$language"
    echo "==> Compiling $language record-egress fixture"
    compile_current_output "$output_root" "$language" "$dir/tacplus" "gitlab.com/example/dummy/egressvault"
    echo "    PASS $language record-egress"
  done

  # The HOST leg: the same protos built as egressstratum, the (spec.record_store)
  # the trait names. The plane's two services are defined in the trait's own
  # package and labeled onto this one, so they are hosted VSERVICES — the half of
  # server resolution Go did not implement. Generating them as SERVERS is the
  # assertion; compiling the result is what proves the wrapper is real, and is
  # how the three latent defects that half had never exercised (a WATCH seam
  # reading from_revision off a handle that declares none, an `optional string`
  # predicate pin assigned a bare string, and a Handle<T> projection dispatch
  # emitted for a source that has none) were found in the first place.
  for language in "${languages[@]}"; do
    dir="$WORK_ROOT/recordegress-host-$language"
    log="$WORK_ROOT/recordegress-host-$language.log"
    rm -rf "$dir"
    write_recordegress_host_config "$dir" "$language"

    echo "==> Generating $language record-egress HOST fixture"
    if ! (cd "$dir" && TACPLUS_PYTHON="$PYTHON_BIN" "$TACPLUS_BIN" build --config tacplus.yaml --src "$RECORDEGRESS_PROTO_SRC" >"$log" 2>&1); then
      tail -120 "$log" >&2 || true
      die "$language record-egress host generation failed; log: $log"
    fi
    output_root="$(resolve_output_root "$dir/tacplus")"
    assert_spiffe_soundness "$dir/tacplus"
    assert_recordegress_host_output "$output_root" "$language"
    echo "==> Compiling $language record-egress HOST fixture"
    compile_current_output "$output_root" "$language" "$dir/tacplus" "gitlab.com/example/dummy/egressstratum"
    echo "    PASS $language record-egress host"
  done
fi

if [[ "$KEEP" != true ]]; then
  rm -rf "$WORK_ROOT"
fi

echo "==> Current cross-test generation and compile passed"
