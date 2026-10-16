# tacplus images: the binary for one commit or release, in two shapes.
#
#   tacplus        slim: everything `tacplus build` shells out to and nothing
#                  more: git, protoc and the plugins, Node (protoc-gen-es is
#                  JavaScript), gofmt (the Go generator runs it) and the
#                  binary. What a generate step pulls.
#   tacplus-build  full: the build image (runtimes plus tools) and the binary,
#                  for a build that generates and compiles in one container.
#
# Both are tagged by the version policy in docs/VERSIONING.md. The tools in
# the slim image are copied out of the build image, so there is one source of
# truth for their versions. Build with --target slim or --target full.
ARG BUILD_IMAGE=ghcr.io/tacplus-dev/tacplus/build:latest
ARG GO_VERSION=1.25

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-trixie AS tacplus
ARG VERSION=0.0.0
ARG VCS_REF=local
ARG PROTOC_VERSION=36.0
ARG PROTOC_GEN_GO_VERSION=1.36.11
ARG PROTOC_GEN_GO_GRPC_VERSION=1.6.0
ARG TARGETARCH
ARG BUILDARCH
# go generate for the spec package runs protoc with the Go plugins, natively.
RUN go install "google.golang.org/protobuf/cmd/protoc-gen-go@v${PROTOC_GEN_GO_VERSION}" \
    && go install "google.golang.org/grpc/cmd/protoc-gen-go-grpc@v${PROTOC_GEN_GO_GRPC_VERSION}"
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl unzip \
    && rm -rf /var/lib/apt/lists/* \
    && case "$BUILDARCH" in arm64) A=aarch_64 ;; *) A=x86_64 ;; esac \
    && curl -fsSL --retry 30 --retry-delay 5 -o /tmp/protoc.zip \
       "https://github.com/protocolbuffers/protobuf/releases/download/v${PROTOC_VERSION}/protoc-${PROTOC_VERSION}-linux-${A}.zip" \
    && unzip -q /tmp/protoc.zip -d /usr/local && rm /tmp/protoc.zip && chmod +x /usr/local/bin/protoc
WORKDIR /src
COPY . .
RUN go generate ./pkg/internal/proto/spec/... \
    && PKG=github.com/tacplus-dev/tacplus/pkg/buildinfo \
    && CGO_ENABLED=0 GOARCH=${TARGETARCH} go build -trimpath \
       -ldflags="-w -s -X ${PKG}.Version=${VERSION} -X ${PKG}.GitSha=${VCS_REF} -X ${PKG}.BuildID=${VCS_REF} -X ${PKG}.ProjectPath=tacplus-dev/tacplus" \
       -o /out/tacplus .

FROM ${BUILD_IMAGE} AS tools

FROM debian:trixie-slim AS slim
ARG VERSION=0.0.0
ARG VCS_REF=local
LABEL org.opencontainers.image.title="tacplus" \
      org.opencontainers.image.description="tacplus with exactly what generation needs: git, protoc and its plugins, Node, gofmt" \
      org.opencontainers.image.source="https://github.com/tacplus-dev/tacplus" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${VCS_REF}"
RUN apt-get update && apt-get install -y --no-install-recommends bash ca-certificates git \
    && rm -rf /var/lib/apt/lists/*
COPY --from=tools /usr/local/bin/protoc /usr/local/bin/protoc-gen-go /usr/local/bin/protoc-gen-go-grpc \
                  /usr/local/bin/protoc-gen-grpc-java /usr/local/bin/protoc-gen-prost /usr/local/bin/protoc-gen-tonic \
                  /usr/local/bin/protoc-gen-prost-serde /usr/local/bin/protoc-gen-prost-crate /usr/local/bin/
COPY --from=tools /usr/local/include /usr/local/include
COPY --from=tools /usr/local/bin/node /usr/local/bin/node
COPY --from=tools /usr/local/lib/node_modules/@bufbuild /usr/local/lib/node_modules/@bufbuild
COPY --from=tools /usr/local/go/bin/gofmt /usr/local/bin/gofmt
RUN ln -s ../lib/node_modules/@bufbuild/protoc-gen-es/bin/protoc-gen-es /usr/local/bin/protoc-gen-es
COPY --from=tacplus /out/tacplus /usr/local/bin/tacplus
COPY images/smoke.sh /usr/local/bin/tacplus-smoke
ENV TACPLUS_IMAGE=slim
WORKDIR /workspace

FROM ${BUILD_IMAGE} AS full
ARG VERSION=0.0.0
ARG VCS_REF=local
LABEL org.opencontainers.image.title="tacplus-build" \
      org.opencontainers.image.description="tacplus build image (runtimes and tools) plus the tacplus binary" \
      org.opencontainers.image.source="https://github.com/tacplus-dev/tacplus" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${VCS_REF}"
COPY --from=tacplus /out/tacplus /usr/local/bin/tacplus
COPY images/smoke.sh /usr/local/bin/tacplus-smoke
ENV TACPLUS_IMAGE=full
