#!/usr/bin/env bash
# Brings up kind + the chart for test/e2e. Idempotent.
set -euo pipefail

CLUSTER="${CLUSTER:-csi-mergerfs-e2e}"
DRIVER_NS="${DRIVER_NS:-csi-mergerfs}"
IMAGE="csi-driver-mergerfs:e2e"
# hostPath branch root inside the kind node; test/e2e uses subdirs of it.
HOST_ROOT=/srv/e2e
here="$(cd "$(dirname "$0")" && pwd)"
root="$here/../.."
chart="$root/charts/csi-driver-mergerfs"
node="$CLUSTER-control-plane"

if ! kind get clusters | grep -qx "$CLUSTER"; then
  kind create cluster --name "$CLUSTER" --config "$here/kind.yaml" --wait 120s
fi
kubectl config use-context "kind-$CLUSTER" >/dev/null
# Owned by the Mount Pod uid (mountPod.uid below), like a real media disk.
docker exec "$node" sh -c "mkdir -p $HOST_ROOT && chown 1000:1000 $HOST_ROOT"

# The chart's sidecar images and busybox pull inside kind while the driver builds.
pulls=()
for img in busybox:1.37 $(helm template t "$chart" | sed -n 's/^ *image: *"\{0,1\}\([^"]*\)"\{0,1\}$/\1/p' | sort -u); do
  case "$img" in csi-driver-mergerfs:* | */csi-driver-mergerfs:*) continue ;; esac
  docker exec "$node" crictl pull "$img" >/dev/null &
  pulls+=($!)
done

# Host build reuses the local Go build cache; the image only copies the binaries in.
arch="$(docker exec "$node" dpkg --print-architecture)" # the node's, not the daemon's
for cmd in csi-driver-mergerfs mergerfs-fusermount; do
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -C "$root" -trimpath \
    -ldflags "-s -w -X main.version=e2e" -o "bin/$cmd" "./cmd/$cmd"
done
docker build -t "$IMAGE" --build-arg BIN=local --build-arg VERSION=e2e "$root"
kind load docker-image --name "$CLUSTER" "$IMAGE"

for p in "${pulls[@]}"; do wait "$p"; done

kubectl create namespace "$DRIVER_NS" --dry-run=client -o yaml | kubectl apply -f -
kubectl label namespace "$DRIVER_NS" pod-security.kubernetes.io/enforce=privileged --overwrite
# Rolls the DaemonSet and controller even when only the locally rebuilt image changed.
helm upgrade --install csi-mergerfs "$chart" -n "$DRIVER_NS" \
  --set image.repository=csi-driver-mergerfs --set image.tag=e2e --set image.pullPolicy=Never \
  --set logLevel=debug --set "allowedHostPaths={$HOST_ROOT}" \
  --set mountPod.uid=1000 --set mountPod.gid=1000 --set mountPod.healWorkloads.enabled=true \
  --set-string "node.podAnnotations.e2e/build=$(date +%s)" \
  --set-string "controller.podAnnotations.e2e/build=$(date +%s)" \
  --wait --timeout 3m
