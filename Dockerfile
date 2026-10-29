# syntax=docker/dockerfile:1
# build: compile in the image (release); local: hack/e2e/up.sh prebuilt bin/.
ARG BIN=build
FROM --platform=$BUILDPLATFORM golang:1.27.3-bookworm@sha256:df4bb0305771186a85d0fd3a40f5c5da9af97ac8053d391ffce2c070817a20c6 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG TARGETOS TARGETARCH
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/csi-driver-mergerfs ./cmd/csi-driver-mergerfs && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w" -o /out/mergerfs-fusermount ./cmd/mergerfs-fusermount

FROM scratch AS bin-local
COPY bin/csi-driver-mergerfs bin/mergerfs-fusermount /out/

FROM build AS bin-build
FROM bin-${BIN} AS bin

# The upstream static (static-pie) build: runs on distroless/static. Only the mergerfs
# binary is taken, not its setuid mergerfs-fusermount: the shim replaces it.
# The pinned release tag over HTTPS is the trust anchor (no per-arch sums), so Renovate
# bumps the version alone; the published image is signed and carries provenance.
FROM --platform=$BUILDPLATFORM golang:1.27.3-bookworm@sha256:df4bb0305771186a85d0fd3a40f5c5da9af97ac8053d391ffce2c070817a20c6 AS mergerfs
# renovate: datasource=github-releases depName=trapexit/mergerfs
ARG MERGERFS_VERSION=2.42.0
ARG TARGETARCH
RUN set -eu; \
    case "$TARGETARCH" in amd64|arm64) ;; *) exit 1 ;; esac; \
    f="mergerfs-${MERGERFS_VERSION}-static-linux_${TARGETARCH}.tar.gz"; \
    curl -fsSL -o "/tmp/$f" "https://github.com/trapexit/mergerfs/releases/download/${MERGERFS_VERSION}/$f"; \
    mkdir /out && tar -xzf "/tmp/$f" -C /out --strip-components=3 usr/local/bin/mergerfs

# `mise run test:fuse` exports just the binary.
FROM scratch AS mergerfs-bin
COPY --from=mergerfs /out/mergerfs /mergerfs

# The node plugin overrides to root + privileged in the chart; Mount Pods keep nonroot.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=bin /out/csi-driver-mergerfs /csi-driver-mergerfs
# libfuse in mergerfs execs /usr/local/bin/mergerfs-fusermount first (ADR 0001).
COPY --from=bin /out/mergerfs-fusermount /usr/local/bin/mergerfs-fusermount
COPY --from=mergerfs /out/mergerfs /usr/local/bin/mergerfs
ARG VERSION=dev
LABEL org.opencontainers.image.source="https://github.com/BlackDark/csi-driver-mergerfs" \
      org.opencontainers.image.description="Kubernetes CSI driver for mergerfs union volumes" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="Apache-2.0"
ENTRYPOINT ["/csi-driver-mergerfs"]
