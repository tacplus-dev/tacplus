# tacplus build image: the base image plus every build-time tool a service
# build needs: protoc and its plugins for each target language, just, the
# docker CLI with buildx, and gcloud. Rebuilt when a tool pin changes.
# See docs/VERSIONING.md, Images.
#
# protoc is pinned at the ceiling the justfile allows (protoc-check); the
# Java runtime the generated models pin (protobuf-java 4.36.0) must be at
# least the protoc gencode version, so protoc stays at 36.0 until that pin
# moves.
ARG BASE_IMAGE=ghcr.io/tacplus-dev/tacplus/base:latest
ARG GO_VERSION=1.26
ARG RUST_VERSION=1
ARG NODE_VERSION=24
ARG DOCKER_VERSION=29
ARG PROTOC_VERSION=36.0
ARG PROTOC_GEN_GO_VERSION=1.36.11
ARG PROTOC_GEN_GO_GRPC_VERSION=1.6.0
ARG GRPC_JAVA_VERSION=1.79.0
ARG PROTOC_GEN_ES_VERSION=2.11.0
ARG PROTOC_GEN_PROST_VERSION=0.5.0
ARG PROTOC_GEN_TONIC_VERSION=0.5.0
ARG PROTOC_GEN_PROST_SERDE_VERSION=0.4.0
ARG PROTOC_GEN_PROST_CRATE_VERSION=0.5.0
ARG JUST_VERSION=1.58.0
ARG GCLOUD_VERSION=549.0.0

FROM debian:trixie-slim AS fetch
ARG TARGETARCH
ARG PROTOC_VERSION
ARG GRPC_JAVA_VERSION
ARG JUST_VERSION
ARG GCLOUD_VERSION
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl unzip \
    && rm -rf /var/lib/apt/lists/*
RUN case "$TARGETARCH" in \
      arm64) PROTOC_ARCH=aarch_64; JUST_ARCH=aarch64; GCLOUD_ARCH=arm ;; \
      amd64) PROTOC_ARCH=x86_64; JUST_ARCH=x86_64; GCLOUD_ARCH=x86_64 ;; \
      *) echo "unsupported TARGETARCH '$TARGETARCH'" >&2; exit 1 ;; \
    esac \
    && curl -fsSL --retry 30 --retry-delay 5 -o /tmp/protoc.zip \
       "https://github.com/protocolbuffers/protobuf/releases/download/v${PROTOC_VERSION}/protoc-${PROTOC_VERSION}-linux-${PROTOC_ARCH}.zip" \
    && unzip -q /tmp/protoc.zip -d /opt/protoc && rm /tmp/protoc.zip \
    && curl -fsSL --retry 30 --retry-delay 5 -o /opt/protoc/bin/protoc-gen-grpc-java \
       "https://repo1.maven.org/maven2/io/grpc/protoc-gen-grpc-java/${GRPC_JAVA_VERSION}/protoc-gen-grpc-java-${GRPC_JAVA_VERSION}-linux-${PROTOC_ARCH}.exe" \
    && chmod +x /opt/protoc/bin/protoc /opt/protoc/bin/protoc-gen-grpc-java \
    && mkdir -p /opt/just \
    && curl -fsSL --retry 30 --retry-delay 5 --retry-all-errors -o /tmp/just.tar.gz \
       "https://github.com/casey/just/releases/download/${JUST_VERSION}/just-${JUST_VERSION}-${JUST_ARCH}-unknown-linux-musl.tar.gz" \
    && tar -xz -C /opt/just -f /tmp/just.tar.gz just && rm /tmp/just.tar.gz \
    && curl -fsSL --retry 30 --retry-delay 5 --retry-all-errors -o /tmp/gcloud.tar.gz \
       "https://dl.google.com/dl/cloudsdk/channels/rapid/downloads/google-cloud-cli-${GCLOUD_VERSION}-linux-${GCLOUD_ARCH}.tar.gz" \
    && tar -xz -C /opt -f /tmp/gcloud.tar.gz && rm /tmp/gcloud.tar.gz \
    && rm -rf /opt/google-cloud-sdk/platform/bundledpythonunix /opt/google-cloud-sdk/bin/gcloud-crc32c

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-trixie AS go-tools
ARG TARGETARCH
ARG PROTOC_GEN_GO_VERSION
ARG PROTOC_GEN_GO_GRPC_VERSION
RUN GOARCH=${TARGETARCH} go install "google.golang.org/protobuf/cmd/protoc-gen-go@v${PROTOC_GEN_GO_VERSION}" \
    && GOARCH=${TARGETARCH} go install "google.golang.org/grpc/cmd/protoc-gen-go-grpc@v${PROTOC_GEN_GO_GRPC_VERSION}"

FROM rust:${RUST_VERSION}-trixie AS rust-tools
ARG PROTOC_GEN_PROST_VERSION
ARG PROTOC_GEN_TONIC_VERSION
ARG PROTOC_GEN_PROST_SERDE_VERSION
ARG PROTOC_GEN_PROST_CRATE_VERSION
RUN cargo install --locked --root /out \
    "protoc-gen-prost@${PROTOC_GEN_PROST_VERSION}" \
    "protoc-gen-tonic@${PROTOC_GEN_TONIC_VERSION}" \
    "protoc-gen-prost-serde@${PROTOC_GEN_PROST_SERDE_VERSION}" \
    "protoc-gen-prost-crate@${PROTOC_GEN_PROST_CRATE_VERSION}"

FROM node:${NODE_VERSION}-trixie-slim AS node-tools
ARG PROTOC_GEN_ES_VERSION
RUN npm install -g "@bufbuild/protoc-gen-es@${PROTOC_GEN_ES_VERSION}" "@bufbuild/protobuf@${PROTOC_GEN_ES_VERSION}"

FROM docker:${DOCKER_VERSION}-cli AS docker-cli

FROM ${BASE_IMAGE}
LABEL org.opencontainers.image.title="tacplus build" \
      org.opencontainers.image.description="tacplus base plus protoc and its plugins, just, the docker CLI and gcloud" \
      org.opencontainers.image.source="https://github.com/tacplus-dev/tacplus"
COPY --from=fetch /opt/protoc/bin/protoc /opt/protoc/bin/protoc-gen-grpc-java /usr/local/bin/
COPY --from=fetch /opt/protoc/include /usr/local/include
COPY --from=fetch /opt/just/just /usr/local/bin/just
COPY --from=fetch /opt/google-cloud-sdk /opt/google-cloud-sdk
# go install puts a cross-compiled binary under /go/bin/<GOOS>_<GOARCH>; a
# native one under /go/bin. Collect whichever exists.
COPY --from=go-tools /go/bin/ /tmp/go-bin/
RUN set -e; for t in protoc-gen-go protoc-gen-go-grpc; do \
      f=$(find /tmp/go-bin -type f -name "$t" | head -1); cp "$f" /usr/local/bin/$t; chmod +x /usr/local/bin/$t; done; rm -rf /tmp/go-bin
COPY --from=rust-tools /out/bin/ /usr/local/bin/
COPY --from=node-tools /usr/local/lib/node_modules/@bufbuild /usr/local/lib/node_modules/@bufbuild
COPY --from=docker-cli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=docker-cli /usr/local/libexec/docker/cli-plugins/docker-buildx /usr/local/libexec/docker/cli-plugins/docker-buildx
RUN ln -s ../lib/node_modules/@bufbuild/protoc-gen-es/bin/protoc-gen-es /usr/local/bin/protoc-gen-es
# gcloud runs on the image's system Python: the SDK's bundled Python and its
# gcloud-crc32c binary (a Go program) are removed in the fetch stage because
# they pin cryptography, pyOpenSSL and a Go stdlib that Trivy flags at
# HIGH/CRITICAL, and no gate uses them.
ENV CLOUDSDK_PYTHON=/usr/bin/python3 \
    PATH="/opt/google-cloud-sdk/bin:${PATH}"
