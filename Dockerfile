# syntax=docker/dockerfile:1

# Build mergerfs-csi from the Go module at ./mergerfs-csi
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY mergerfs-csi/go.mod mergerfs-csi/go.sum* ./
RUN go mod download 2>/dev/null || true
COPY mergerfs-csi/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mergerfs-csi ./cmd/mergerfs-csi

# Runtime: alpine + fuse3. mergerfs is NOT in Alpine apk (as of 3.21).
# Set MERGEFS_URL to a static/linux binary URL, or COPY a pre-built binary into the image.
FROM alpine:3.21
RUN apk add --no-cache ca-certificates fuse3 curl \
    && mkdir -p /var/lib/mergerfs-csi

# Prefer baking mergerfs from upstream release binary.
# Example:
#   docker build --build-arg MERGEFS_URL=https://github.com/trapexit/mergerfs/releases/download/2.40.2/mergerfs-static-linux_amd64.tar.gz ...
ARG MERGEFS_URL=""
RUN if [ -n "$MERGEFS_URL" ]; then \
      curl -fsSL "$MERGEFS_URL" -o /tmp/mergerfs.tgz \
      && tar -xzf /tmp/mergerfs.tgz -C /usr/local/bin --strip-components=1 2>/dev/null \
         || tar -xzf /tmp/mergerfs.tgz -C /tmp && find /tmp -name mergerfs -type f -exec install -m 0755 {} /usr/local/bin/mergerfs \; \
      && rm -rf /tmp/mergerfs.tgz /tmp/mergerfs* \
      && mergerfs --version || true; \
    else \
      echo "WARN: MERGEFS_URL unset; place mergerfs at /usr/local/bin/mergerfs before runtime" >&2; \
    fi

COPY --from=build /out/mergerfs-csi /mergerfs-csi

# Non-secret defaults (override via env / Helm)
ENV CSI_ENDPOINT=unix:///csi/csi.sock \
    DRIVER_NAME=mergerfs.csi.k8s.io \
    KUBELET_PODS_PATH=/var/lib/kubelet/pods \
    STATE_DIR=/var/lib/mergerfs-csi \
    MERGERFS_BIN=/usr/local/bin/mergerfs

ENTRYPOINT ["/mergerfs-csi"]
