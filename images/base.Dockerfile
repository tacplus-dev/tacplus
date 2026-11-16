# tacplus base image: the OS and every language SDK tacplus is built and
# tested against: Go, Java (JDK + Maven), Rust, Python, Node (TypeScript),
# Swift and .NET. Rebuilt on a schedule or when a runtime pin changes. See
# docs/VERSIONING.md, Images.
#
# Debian trixie, because it is the release that packages jbmc (the build
# image's Java verifier); Swift comes from swift.org's Debian 12 tarball,
# which runs on trixie. Pins in one place:
ARG GO_VERSION=1.26
ARG RUST_VERSION=1
ARG JDK_VERSION=25
ARG MAVEN_VERSION=3.9
ARG MAVEN_GUAVA_VERSION=33.7.2-jre
ARG MAVEN_GUAVA_SHA256=b530942257fb935f8b2cfaa5f8eb5bd59c555fd8e8d01b8ce98912e077ea606c
ARG NODE_VERSION=24
ARG NPM_VERSION=11.21.1
ARG NPM_UNDICI_VERSION=6.29.1
ARG SWIFT_VERSION=6.1.3
ARG DOTNET_CHANNEL=10.0

FROM golang:${GO_VERSION}-trixie AS go
FROM eclipse-temurin:${JDK_VERSION}-jdk AS jdk
FROM maven:${MAVEN_VERSION}-eclipse-temurin-${JDK_VERSION} AS maven
FROM node:${NODE_VERSION}-trixie-slim AS node

# rust's slim image, not rust:X-trixie: that one is buildpack-deps, whose
# ImageMagick -dev packages brought in OpenEXR (CVE-2026-42216/-42217) and
# some two hundred other packages nothing here uses (#554).
FROM rust:${RUST_VERSION}-slim-trixie
ARG MAVEN_GUAVA_VERSION
ARG MAVEN_GUAVA_SHA256
ARG TARGETARCH
ARG NPM_VERSION
ARG NPM_UNDICI_VERSION
ARG SWIFT_VERSION
ARG DOTNET_CHANNEL
LABEL org.opencontainers.image.title="tacplus base" \
      org.opencontainers.image.description="OS and language SDKs (Go, Java, Rust, Python, Node, Swift, .NET) for building and testing tacplus" \
      org.opencontainers.image.source="https://github.com/tacplus-dev/tacplus"
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
# The Debian packages are images/base.packages, and nothing else is installed
# (tacplus-packages-check, the last step). APT_REFRESH is unique to each
# images.yml run, so the registry cache never hands back an older run's
# upgrade (#369; build.Dockerfile says why).
COPY images/base.packages /usr/local/share/tacplus/packages/base
COPY images/packages-check.sh /usr/local/bin/tacplus-packages-check
ARG APT_REFRESH=
RUN : "${APT_REFRESH}" && apt-get update && apt-get upgrade -y \
    && sed 's/#.*//' /usr/local/share/tacplus/packages/base | xargs apt-get install -y --no-install-recommends \
    && rm -rf /var/lib/apt/lists/*
COPY --from=go /usr/local/go /usr/local/go
COPY --from=jdk /opt/java/openjdk /opt/java/openjdk
COPY --from=maven /usr/share/maven /usr/share/maven
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/include/node /usr/local/include/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -s /usr/share/maven/bin/mvn /usr/local/bin/mvn \
    && ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx
# Maven's own lib/ carries a guava that lags its advisories (3.9.16 ships
# 33.6.0, CVE-2026-102554, #222). No Maven release yet ships 33.7.2 or later,
# so MAVEN_GUAVA_VERSION (same failureaccess 1.0.3 Maven ships) replaces it,
# checksum-verified; Maven must then still start and build a project. Drop it
# once a Maven release bundles it.
RUN curl -fsSL --retry 30 --retry-delay 5 --retry-all-errors -o /tmp/guava.jar \
       "https://repo.maven.apache.org/maven2/com/google/guava/guava/${MAVEN_GUAVA_VERSION}/guava-${MAVEN_GUAVA_VERSION}.jar" \
    && echo "${MAVEN_GUAVA_SHA256}  /tmp/guava.jar" | sha256sum -c - \
    && rm /usr/share/maven/lib/guava-*.jar \
    && mv /tmp/guava.jar "/usr/share/maven/lib/guava-${MAVEN_GUAVA_VERSION}.jar" \
    && JAVA_HOME=/opt/java/openjdk mvn --version \
    && d=$(mktemp -d) && mkdir -p "$d/src/main/java" \
    && printf '%s\n' '<project xmlns="http://maven.apache.org/POM/4.0.0"><modelVersion>4.0.0</modelVersion>' \
       '<groupId>smoke</groupId><artifactId>smoke</artifactId><version>1</version>' \
       '<properties><maven.compiler.release>17</maven.compiler.release></properties></project>' > "$d/pom.xml" \
    && echo 'public class Smoke {}' > "$d/src/main/java/Smoke.java" \
    && JAVA_HOME=/opt/java/openjdk mvn -B -q -f "$d/pom.xml" package \
    && test -f "$d/target/smoke-1.jar" \
    && rm -rf "$d" /root/.m2
# The npm bundled in the Node tarball carries its own node_modules (brace-expansion,
# undici) at versions that lag their advisories; installing a pinned npm over it
# replaces that whole tree. NPM_VERSION is the first release whose bundle Trivy
# does not flag for brace-expansion. No npm release yet bundles an undici past
# the latest advisory, so NPM_UNDICI_VERSION (same 6.x line npm requires) is
# unpacked over npm's copy; drop it once an npm release bundles it.
RUN npm install -g --no-fund --no-audit "npm@${NPM_VERSION}" \
    && d=$(mktemp -d) && (cd "$d" && npm pack --silent "undici@${NPM_UNDICI_VERSION}") \
    && rm -rf /usr/local/lib/node_modules/npm/node_modules/undici \
    && mkdir /usr/local/lib/node_modules/npm/node_modules/undici \
    && tar -xz --strip-components=1 -C /usr/local/lib/node_modules/npm/node_modules/undici -f "$d"/undici-*.tgz \
    && rm -rf "$d" && npm cache clean --force && npm --version \
    && npm view undici version >/dev/null
# Swift SDK into /usr, where swift.org's tarball layout puts it.
RUN case "$TARGETARCH" in \
      amd64) P=debian12; F="swift-${SWIFT_VERSION}-RELEASE-debian12" ;; \
      arm64) P=debian12-aarch64; F="swift-${SWIFT_VERSION}-RELEASE-debian12-aarch64" ;; \
      *) echo "unsupported TARGETARCH '$TARGETARCH'" >&2; exit 1 ;; \
    esac \
    && curl -fsSL --retry 30 --retry-delay 5 --retry-all-errors -o /tmp/swift.tar.gz \
       "https://download.swift.org/swift-${SWIFT_VERSION}-release/${P}/swift-${SWIFT_VERSION}-RELEASE/${F}.tar.gz" \
    && tar -xz --strip-components=1 -C / -f /tmp/swift.tar.gz && rm /tmp/swift.tar.gz \
    && swift --version
# .NET SDK. The generated C# clients target net8.0, which the current SDK
# builds against the targeting pack from NuGet; the 8.0 SDK itself ships
# assemblies with fixed-only HIGH findings and no patched 8.0 SDK exists.
RUN curl -fsSL --retry 30 --retry-delay 5 -o /tmp/dotnet-install.sh https://dot.net/v1/dotnet-install.sh \
    && bash /tmp/dotnet-install.sh --channel "${DOTNET_CHANNEL}" --install-dir /usr/share/dotnet \
    && ln -s /usr/share/dotnet/dotnet /usr/local/bin/dotnet && rm /tmp/dotnet-install.sh \
    && dotnet --version
ENV JAVA_HOME=/opt/java/openjdk \
    MAVEN_HOME=/usr/share/maven \
    MAVEN_OPTS=--sun-misc-unsafe-memory-access=allow \
    DOTNET_ROOT=/usr/share/dotnet \
    DOTNET_CLI_TELEMETRY_OPTOUT=1 \
    DOTNET_NOLOGO=1 \
    PATH="/usr/local/go/bin:/opt/java/openjdk/bin:${PATH}"
# No Debian package beyond images/base.packages, Debian's base system and
# their dependencies (#554).
RUN tacplus-packages-check /usr/local/share/tacplus/packages/base
WORKDIR /workspace
