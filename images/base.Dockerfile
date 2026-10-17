# tacplus base image: the OS and every language SDK tacplus is built and
# tested against: Go, Java (JDK + Maven), Rust, Python, Node (TypeScript),
# Swift and .NET. Rebuilt on a schedule or when a runtime pin changes. See
# docs/VERSIONING.md, Images.
#
# Debian trixie, because it is the release that packages jbmc (the build
# image's Java verifier); Swift comes from swift.org's Debian 12 tarball,
# which runs on trixie. Pins in one place:
ARG GO_VERSION=1.25
ARG RUST_VERSION=1
ARG JDK_VERSION=25
ARG MAVEN_VERSION=3.9
ARG NODE_VERSION=24
ARG SWIFT_VERSION=6.1.3
ARG DOTNET_CHANNEL=8.0

FROM golang:${GO_VERSION}-trixie AS go
FROM eclipse-temurin:${JDK_VERSION}-jdk AS jdk
FROM maven:${MAVEN_VERSION}-eclipse-temurin-${JDK_VERSION} AS maven
FROM node:${NODE_VERSION}-trixie-slim AS node

FROM rust:${RUST_VERSION}-trixie
ARG TARGETARCH
ARG SWIFT_VERSION
ARG DOTNET_CHANNEL
LABEL org.opencontainers.image.title="tacplus base" \
      org.opencontainers.image.description="OS and language SDKs (Go, Java, Rust, Python, Node, Swift, .NET) for building and testing tacplus" \
      org.opencontainers.image.source="https://github.com/tacplus-dev/tacplus"
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
# Swift's documented runtime dependencies (Debian 12 list, trixie names),
# .NET's (libicu), and the common build basics.
RUN apt-get update && apt-get install -y --no-install-recommends \
    bash binutils build-essential ca-certificates cmake curl file git gnupg jq \
    libc6-dev libcurl4-openssl-dev libedit2 libgcc-14-dev libicu76 libncurses-dev \
    libpython3-dev libsqlite3-0 libssl-dev libstdc++-14-dev libxml2-dev libz3-dev \
    pkg-config procps python3 python3-pip python3-venv tzdata unzip xz-utils zlib1g-dev \
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
# Swift SDK into /usr, where swift.org's tarball layout puts it.
RUN case "$TARGETARCH" in \
      amd64) P=debian12; F="swift-${SWIFT_VERSION}-RELEASE-debian12" ;; \
      arm64) P=debian12-aarch64; F="swift-${SWIFT_VERSION}-RELEASE-debian12-aarch64" ;; \
      *) echo "unsupported TARGETARCH '$TARGETARCH'" >&2; exit 1 ;; \
    esac \
    && curl -fsSL --retry 30 --retry-delay 5 \
       "https://download.swift.org/swift-${SWIFT_VERSION}-release/${P}/swift-${SWIFT_VERSION}-RELEASE/${F}.tar.gz" \
       | tar -xz --strip-components=1 -C / \
    && swift --version
# .NET SDK (the generated C# clients target net8.0).
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
WORKDIR /workspace
