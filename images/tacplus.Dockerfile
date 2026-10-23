# tacplus image: the tacplus binary and exactly what `tacplus build` shells
# out to: git, protoc and the plugin for every language that needs its own
# (Go, Java, TypeScript via Node, Rust, Swift, and Python's grpcio-tools),
# and gofmt. C# needs nothing here: the generated project runs Grpc.Tools
# from NuGet in the consumer's own dotnet build. The plugins are copied out
# of the build image, so their versions have one source. Tagged by the
# version policy in docs/VERSIONING.md (X.Y.Z, X.Y, X, latest, main-<sha>).
ARG BUILD_IMAGE=ghcr.io/tacplus-dev/tacplus/build:latest
ARG GO_VERSION=1.26

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

FROM debian:trixie-slim
ARG VERSION=0.0.0
ARG VCS_REF=local
LABEL org.opencontainers.image.title="tacplus" \
      org.opencontainers.image.description="tacplus and the protoc plugins for every target language" \
      org.opencontainers.image.source="https://github.com/tacplus-dev/tacplus" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${VCS_REF}"
RUN apt-get update && apt-get upgrade -y && apt-get install -y --no-install-recommends bash ca-certificates git libcurl4 libxml2 python3 \
    && rm -rf /var/lib/apt/lists/*
COPY --from=tools /usr/local/bin/protoc /usr/local/bin/protoc-gen-go /usr/local/bin/protoc-gen-go-grpc \
                  /usr/local/bin/protoc-gen-grpc-java /usr/local/bin/protoc-gen-prost /usr/local/bin/protoc-gen-tonic \
                  /usr/local/bin/protoc-gen-prost-serde /usr/local/bin/protoc-gen-prost-crate \
                  /usr/local/bin/protoc-gen-swift /usr/local/bin/protoc-gen-grpc-swift-2 /usr/local/bin/
COPY --from=tools /usr/local/include /usr/local/include
COPY --from=tools /usr/local/bin/node /usr/local/bin/node
COPY --from=tools /usr/local/lib/node_modules/@bufbuild /usr/local/lib/node_modules/@bufbuild
COPY --from=tools /usr/local/go/bin/gofmt /usr/local/bin/gofmt
COPY --from=tools /opt/swift-runtime /opt/swift-runtime
COPY --from=tools /opt/venv /opt/venv
RUN ln -s ../lib/node_modules/@bufbuild/protoc-gen-es/bin/protoc-gen-es /usr/local/bin/protoc-gen-es
COPY --from=tacplus /out/tacplus /usr/local/bin/tacplus
COPY images/smoke.sh /usr/local/bin/tacplus-smoke
ENV PATH="/opt/venv/bin:${PATH}" \
    LD_LIBRARY_PATH="/opt/swift-runtime/lib" \
    TACPLUS_IMAGE=slim
WORKDIR /workspace
