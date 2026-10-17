# tacplus build image: the base image plus every tool that building and
# testing tacplus uses: protoc and the plugin for every target language,
# grpcio-tools and the Python packaging tools, the Java verifiers (JBMC,
# OpenJML), just, the gh CLI, the docker CLI and gcloud. This is the one image the gates
# run in (CI, the nightly gates, `just gates-container`). Rebuilt when a tool
# pin changes. See docs/VERSIONING.md, Images.
#
# protoc is pinned at the ceiling the justfile allows (protoc-check); the
# Java runtime the generated models pin (protobuf-java 4.36.0) must be at
# least the protoc gencode version.
ARG BASE_IMAGE=ghcr.io/tacplus-dev/tacplus/base:latest
ARG GO_VERSION=1.25
ARG RUST_VERSION=1
ARG NODE_VERSION=24
ARG SWIFT_VERSION=6.1.3
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
ARG SWIFT_PROTOBUF_VERSION=1.38.0
ARG GRPC_SWIFT_PROTOBUF_VERSION=2.4.0
ARG GRPCIO_TOOLS_VERSION=1.76.0
ARG JUST_VERSION=1.58.0
ARG GH_VERSION=2.102.0
ARG GCLOUD_VERSION=549.0.0
ARG OPENJML_VERSION=21-0.23

FROM debian:trixie-slim AS fetch
ARG TARGETARCH
ARG PROTOC_VERSION
ARG GRPC_JAVA_VERSION
ARG JUST_VERSION
ARG GH_VERSION
ARG GCLOUD_VERSION
ARG OPENJML_VERSION
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
    && curl -fsSL --retry 30 --retry-delay 5 \
       "https://github.com/casey/just/releases/download/${JUST_VERSION}/just-${JUST_VERSION}-${JUST_ARCH}-unknown-linux-musl.tar.gz" \
       | tar -xz -C /opt/just just \
    && mkdir -p /opt/gh \
    && curl -fsSL --retry 30 --retry-delay 5 \
       "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_${TARGETARCH}.tar.gz" \
       | tar -xz --strip-components=2 -C /opt/gh "gh_${GH_VERSION}_linux_${TARGETARCH}/bin/gh" \
    && curl -fsSL --retry 30 --retry-delay 5 \
       "https://dl.google.com/dl/cloudsdk/channels/rapid/downloads/google-cloud-cli-${GCLOUD_VERSION}-linux-${GCLOUD_ARCH}.tar.gz" \
       | tar -xz -C /opt \
    && mkdir -p /opt/openjml \
    && curl -fsSL --retry 30 --retry-delay 5 -o /tmp/openjml.zip \
       "https://github.com/OpenJML/OpenJML/releases/download/${OPENJML_VERSION}/openjml-ubuntu-latest-${OPENJML_VERSION}.zip" \
    && unzip -q /tmp/openjml.zip -d /opt/openjml && rm /tmp/openjml.zip && chmod -R +x /opt/openjml

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-trixie AS go-tools
ARG TARGETARCH
ARG PROTOC_GEN_GO_VERSION
ARG PROTOC_GEN_GO_GRPC_VERSION
RUN GOARCH=${TARGETARCH} go install "google.golang.org/protobuf/cmd/protoc-gen-go@v${PROTOC_GEN_GO_VERSION}" \
    && GOARCH=${TARGETARCH} go install "google.golang.org/grpc/cmd/protoc-gen-go-grpc@v${PROTOC_GEN_GO_GRPC_VERSION}" \
    && mkdir -p /out && find /go/bin -type f \( -name protoc-gen-go -o -name protoc-gen-go-grpc \) -exec cp {} /out/ \;

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

# The Swift plugins, built with the same Swift release the base image
# carries. /opt/swift-runtime holds the shared libraries they need, so the
# slim tacplus image can run them without the Swift SDK.
FROM swift:${SWIFT_VERSION}-bookworm AS swift-tools
ARG SWIFT_PROTOBUF_VERSION
ARG GRPC_SWIFT_PROTOBUF_VERSION
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git && rm -rf /var/lib/apt/lists/*
RUN git clone --depth 1 --branch "${SWIFT_PROTOBUF_VERSION}" https://github.com/apple/swift-protobuf /tmp/swift-protobuf \
    && swift build --package-path /tmp/swift-protobuf -c release --product protoc-gen-swift \
    && git clone --depth 1 --branch "${GRPC_SWIFT_PROTOBUF_VERSION}" https://github.com/grpc/grpc-swift-protobuf /tmp/grpc-swift-protobuf \
    && swift build --package-path /tmp/grpc-swift-protobuf -c release --product protoc-gen-grpc-swift-2 \
    && mkdir -p /out/bin /out/swift-runtime/lib \
    && cp /tmp/swift-protobuf/.build/release/protoc-gen-swift /tmp/grpc-swift-protobuf/.build/release/protoc-gen-grpc-swift-2 /out/bin/ \
    && cp -a /usr/lib/swift/linux/. /out/swift-runtime/lib/ \
    && find /usr/lib -type f -name 'libicu*.so*' -exec cp -L -t /out/swift-runtime/lib {} +

FROM docker:${DOCKER_VERSION}-cli AS docker-cli

FROM ${BASE_IMAGE}
ARG GRPCIO_TOOLS_VERSION
LABEL org.opencontainers.image.title="tacplus build" \
      org.opencontainers.image.description="tacplus base plus protoc and every plugin, grpcio-tools, JBMC/OpenJML, just, the docker CLI and gcloud: the image the tacplus gates run in" \
      org.opencontainers.image.source="https://github.com/tacplus-dev/tacplus"
RUN apt-get update && apt-get install -y --no-install-recommends dnsutils iproute2 jbmc patch ripgrep \
    && rm -rf /var/lib/apt/lists/*
COPY --from=fetch /opt/protoc/bin/protoc /opt/protoc/bin/protoc-gen-grpc-java /usr/local/bin/
COPY --from=fetch /opt/protoc/include /usr/local/include
COPY --from=fetch /opt/just/just /usr/local/bin/just
COPY --from=fetch /opt/gh/gh /usr/local/bin/gh
COPY --from=fetch /opt/google-cloud-sdk /opt/google-cloud-sdk
COPY --from=fetch /opt/openjml /root/.cache/tacplus/tools/openjml
COPY --from=go-tools /out/ /usr/local/bin/
COPY --from=rust-tools /out/bin/ /usr/local/bin/
COPY --from=node-tools /usr/local/lib/node_modules/@bufbuild /usr/local/lib/node_modules/@bufbuild
COPY --from=swift-tools /out/bin/ /usr/local/bin/
COPY --from=swift-tools /out/swift-runtime /opt/swift-runtime
COPY --from=docker-cli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=docker-cli /usr/local/libexec/docker/cli-plugins/docker-buildx /usr/local/libexec/docker/cli-plugins/docker-buildx
COPY images/smoke.sh /usr/local/bin/tacplus-smoke
RUN ln -s ../lib/node_modules/@bufbuild/protoc-gen-es/bin/protoc-gen-es /usr/local/bin/protoc-gen-es
# Python codegen runs `python -m grpc_tools.protoc`; black/twine/build are
# what the Python client gates use. One venv, first on PATH.
RUN python3 -m venv /opt/venv \
    && /opt/venv/bin/pip install --no-cache-dir "grpcio-tools==${GRPCIO_TOOLS_VERSION}" black build twine
ENV PATH="/opt/venv/bin:/opt/google-cloud-sdk/bin:/root/.cache/tacplus/tools/openjml:${PATH}" \
    TACPLUS_CONTAINER=1 \
    TACPLUS_IMAGE=build
