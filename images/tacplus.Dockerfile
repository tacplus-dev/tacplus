# tacplus image: the build image plus the tacplus binary for one commit or
# release. Rebuilt on every push to main and every release; tagged by the
# version policy in docs/VERSIONING.md (X.Y.Z, X.Y, X, latest, main-<sha>).
ARG BUILD_IMAGE=ghcr.io/tacplus-dev/tacplus/build:latest
ARG GO_VERSION=1.25

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-trixie AS tacplus
ARG VERSION=0.0.0
ARG VCS_REF=local
ARG PROTOC_VERSION=36.0
ARG TARGETARCH
ARG BUILDARCH
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

FROM ${BUILD_IMAGE}
ARG VERSION=0.0.0
ARG VCS_REF=local
LABEL org.opencontainers.image.title="tacplus" \
      org.opencontainers.image.description="tacplus build image plus the tacplus binary" \
      org.opencontainers.image.source="https://github.com/tacplus-dev/tacplus" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${VCS_REF}"
COPY --from=tacplus /out/tacplus /usr/local/bin/tacplus
