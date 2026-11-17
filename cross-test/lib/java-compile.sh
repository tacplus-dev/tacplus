#!/usr/bin/env bash
# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# Shared by the cross-test gates that compile a generated Java model with
# Maven: the pom a generated model compiles under, and the JAVA_HOME the
# host's java_home resolves. Source this file; it defines functions only.

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

# GCS_ARCHIVE_SDK_VERSION is the com.google.cloud:google-cloud-storage pin of
# cross-test/proto-quorumwrite/dependencies.yaml. A build that pins the SDK emits
# GcsArchiveSink, which compiles only with the SDK on the classpath. The pom
# carries it only for such a build, so no other gate downloads it.
# scripts/java_dependency_floor_test.go holds its floors (#263).
GCS_ARCHIVE_SDK_VERSION="2.76.0"

# The SDK still declares guava 33.6.0-jre (GHSA-xxph-c9ww-hj94) and
# opentelemetry-api 1.62.0 (GHSA-ccrj-gw8r-854m): pinned under the pom's
# <dependencyManagement>, as #200 pins the harness's. The SDK needs guava's
# -jre flavour, which stands in for the -android pin grpc-java resolves.
GCS_ARCHIVE_GUAVA_VERSION="33.7.2-jre"
GCS_ARCHIVE_OPENTELEMETRY_VERSION="1.67.0"

add_java_gcs_archive_dependency() {
  local pom="$1"
  python3 -c '
import re, sys
pom, version, guava, otel = sys.argv[1:5]
text = open(pom).read()
# The project-level </dependencies>, at two spaces: the <dependencyManagement>
# block closes its own at four.
end = "\n  </dependencies>\n"
if text.count(end) != 1:
    sys.exit("expected exactly one project-level </dependencies> in " + pom)
dep = ("\n    <dependency>\n      <groupId>com.google.cloud</groupId>\n"
       "      <artifactId>google-cloud-storage</artifactId>\n"
       "      <version>" + version + "</version>\n    </dependency>")
text = text.replace(end, dep + end)
guava_pin = re.compile(r"(<groupId>com\.google\.guava</groupId>\s*<artifactId>guava</artifactId>\s*<version>)[^<]+(</version>)")
text, pins = guava_pin.subn(r"\g<1>" + guava + r"\g<2>", text)
if pins != 1:
    sys.exit("expected exactly one managed guava pin in " + pom)
managed = "\n  <dependencyManagement>\n    <dependencies>\n"
if text.count(managed) != 1:
    sys.exit("expected exactly one <dependencyManagement> block in " + pom)
bom = ("      <dependency>\n        <groupId>io.opentelemetry</groupId>\n"
       "        <artifactId>opentelemetry-bom</artifactId>\n"
       "        <version>" + otel + "</version>\n"
       "        <type>pom</type>\n        <scope>import</scope>\n      </dependency>\n")
open(pom, "w").write(text.replace(managed, managed + bom))
' "$pom" "$GCS_ARCHIVE_SDK_VERSION" "$GCS_ARCHIVE_GUAVA_VERSION" "$GCS_ARCHIVE_OPENTELEMETRY_VERSION"
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
      <!-- jackson 3.1.4, which the MCP Java SDK declares, carries nine
           parser and databind advisories: the family at 3.1.8. -->
      <dependency>
        <groupId>tools.jackson.core</groupId>
        <artifactId>jackson-core</artifactId>
        <version>3.1.8</version>
      </dependency>
      <dependency>
        <groupId>tools.jackson.core</groupId>
        <artifactId>jackson-databind</artifactId>
        <version>3.1.8</version>
      </dependency>
      <dependency>
        <groupId>tools.jackson.dataformat</groupId>
        <artifactId>jackson-dataformat-yaml</artifactId>
        <version>3.1.8</version>
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
