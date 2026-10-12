# syntax=docker/dockerfile:1

# Build mergerfs-csi from the Go module at ./mergerfs-csi
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY mergerfs-csi/go.mod mergerfs-csi/go.sum* ./
RUN go mod download 2>/dev/null || true
COPY mergerfs-csi/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mergerfs-csi ./cmd/mergerfs-csi

# Runtime: alpine + fuse3 + mergerfs static binary from GitHub releases.
FROM alpine:3.24
RUN apk add --no-cache ca-certificates fuse3 curl \
    && mkdir -p /var/lib/mergerfs-csi

# renovate: datasource=github-releases depName=trapexit/mergerfs
ARG MERGEFS_VERSION=2.42.0
ARG TARGETARCH
RUN case "${TARGETARCH}" in \
      amd64) ARCH=amd64 ;; \
      arm64) ARCH=arm64 ;; \
      *) echo "unsupported TARGETARCH=${TARGETARCH}" >&2; exit 1 ;; \
    esac \
 && curl -fsSL "https://github.com/trapexit/mergerfs/releases/download/${MERGEFS_VERSION}/mergerfs-${MERGEFS_VERSION}-static-linux_${ARCH}.tar.gz" \
      -o /tmp/mergerfs.tgz \
 && tar -xzf /tmp/mergerfs.tgz -C /tmp \
 && install -m 0755 /tmp/usr/local/bin/mergerfs /usr/local/bin/mergerfs \
 && rm -rf /tmp/mergerfs.tgz /tmp/usr /tmp/sbin \
 && mergerfs --version

COPY --from=build /out/mergerfs-csi /mergerfs-csi

# Binary reads CLI flags only (not ENV). Helm DaemonSet/Deployment pass:
#   --endpoint --node-id --mode --kubelet-pods-path --state-dir --mergerfs-path
# CMD defaults used for bare `docker run` / local smoke; Helm args replace CMD.
ENTRYPOINT ["/mergerfs-csi"]
CMD ["--mergerfs-path=/usr/local/bin/mergerfs"]
