# tacplus base image: the OS and the language runtimes every generated service
# builds with. Rebuilt on a schedule or when a runtime pin changes; nothing
# tacplus-specific lives here, so it is the layer that is pulled once and
# kept. See docs/VERSIONING.md, Images.
#
# Pins, in one place. The Go version follows go.mod; the JDK is what the
# gates run; protoc itself is in the build image.
ARG GO_VERSION=1.26
ARG RUST_VERSION=1
ARG JDK_VERSION=25
ARG MAVEN_VERSION=3.9
ARG NODE_VERSION=24
ARG NPM_VERSION=11.21.1
ARG NPM_UNDICI_VERSION=6.29.1

FROM golang:${GO_VERSION}-trixie AS go
FROM eclipse-temurin:${JDK_VERSION}-jdk-noble AS jdk
FROM maven:${MAVEN_VERSION}-eclipse-temurin-25 AS maven
FROM node:${NODE_VERSION}-trixie-slim AS node

FROM rust:${RUST_VERSION}-trixie
ARG NPM_VERSION
ARG NPM_UNDICI_VERSION
LABEL org.opencontainers.image.title="tacplus base" \
      org.opencontainers.image.description="OS and language runtimes (Go, JDK, Maven, Node, Rust, Python) for tacplus builds" \
      org.opencontainers.image.source="https://github.com/tacplus-dev/tacplus"
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
RUN apt-get update && apt-get upgrade -y && apt-get install -y --no-install-recommends \
    bash ca-certificates curl file git jq libssl-dev pkg-config python3 unzip \
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
ENV JAVA_HOME=/opt/java/openjdk \
    MAVEN_HOME=/usr/share/maven \
    MAVEN_OPTS=--sun-misc-unsafe-memory-access=allow \
    PATH="/usr/local/go/bin:/opt/java/openjdk/bin:${PATH}"
WORKDIR /workspace
