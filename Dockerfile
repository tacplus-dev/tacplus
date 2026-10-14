# TACPlus Release Image
# Layers the tacplus binary on top of the pre-built base image.
# Base image contains all build tools — this just adds the binary.

# Build the base first: docker build -f Dockerfile.base -t tacplus-base .
ARG BASE_IMAGE=tacplus-base:latest
FROM ${BASE_IMAGE}

ARG BUILD_DATE
ARG VCS_REF
ARG VERSION

LABEL org.opencontainers.image.created="${BUILD_DATE}" \
      org.opencontainers.image.authors="Seabeam LLC" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${VCS_REF}" \
      org.opencontainers.image.vendor="Seabeam LLC" \
      org.opencontainers.image.title="TACPlus Compiler System" \
      org.opencontainers.image.description="Multi-language Protocol Buffer code generator for Go, TypeScript, Python, Rust, Java, Swift, and C# lite clients"

ARG TARGETARCH
COPY bin/tacplus-linux-${TARGETARCH} /usr/local/bin/tacplus
RUN chmod +x /usr/local/bin/tacplus \
    && tacplus --help > /dev/null

CMD ["tacplus", "--help"]
