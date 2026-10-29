<div align="center">

# csi-driver-mergerfs

**Kubernetes CSI driver for mergerfs: union a pod's volumes and allowed host paths into one volume.**

[![CI](https://github.com/BlackDark/csi-driver-mergerfs/actions/workflows/ci.yml/badge.svg)](https://github.com/BlackDark/csi-driver-mergerfs/actions/workflows/ci.yml)

</div>

Status: pre-alpha. Linux nodes, Kubernetes ≥1.34.

## Why

It replaces a host mergerfs mount plus a hostPath PV with a volume Kubernetes manages, for example a local disk in front of an rclone remote.

- Each pool's mergerfs runs in its own unprivileged Mount Pod, so pools survive node plugin restarts and `helm upgrade` ([ADR 0001](docs/adr/0001-mount-pod-and-fusermount-shim.md)).
- Branches are the pod's own volumes (any CSI PVC, inline CSI, emptyDir) or host paths below admin-allowed roots, each `RW`, `RO` or `NC` ([ADR 0002](docs/adr/0002-branch-visibility.md)).
- The pool starts only once every volume branch is mounted and answers.

## What works, what does not

Full list with reasons: [docs/scope.md](docs/scope.md).

Works:
- Inline, Static and Dynamic Volumes; per-branch modes; allowlisted mergerfs options.
- Pools keep serving through node plugin restarts and upgrades.
- A crashed Mount Pod is reported (`MountLost` Event, volume health, metrics); with `mountPod.healWorkloads.enabled` its consumer is evicted and comes back with a fresh pool.

Does not work, by design:
- Sharing one pool across pods: every publish gets its own Mount Pod.
- Per-caller file ownership: mergerfs runs as `mountPod.uid`, not root ([operations](docs/operations.md#mount-pod-uid)).
- Branch volumes that no container mounts (kubelet does not mount them), non-CSI PVCs as branches, host paths outside `allowedHostPaths`.
- Windows nodes, Kubernetes < 1.34.

## Install

The node plugin is privileged and Mount Pods use hostPath volumes, so the namespace needs the `privileged` Pod Security level.

```bash
kubectl create namespace csi-mergerfs
kubectl label namespace csi-mergerfs pod-security.kubernetes.io/enforce=privileged
helm install csi-mergerfs oci://ghcr.io/blackdark/csi-driver-mergerfs/charts/csi-driver-mergerfs --version <version> -n csi-mergerfs \
  --set 'allowedHostPaths={/srv/indigo}' --set mountPod.uid=1000 --set mountPod.gid=1000
```

The image (`ghcr.io/blackdark/csi-driver-mergerfs:<appVersion>`) holds the driver, the fusermount shim and the upstream static mergerfs. Chart values: [values.yaml](charts/csi-driver-mergerfs/values.yaml). Verify signatures with:

```bash
cosign verify ghcr.io/blackdark/csi-driver-mergerfs:<version> \
  --certificate-identity-regexp '^https://github.com/BlackDark/csi-driver-mergerfs/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## Quickstart

A media library: new files go to the local disk, older ones live on an rclone remote ([csi-driver-rclone](https://github.com/BlackDark/csi-driver-rclone) PVC `media-rclone`, mounted with `uid`/`gid` 1000). mergerfs runs as uid 1000 (`mountPod.uid` above), the owner of both.

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: jellyfin
  namespace: media
spec:
  replicas: 1
  selector: {matchLabels: {app: jellyfin}}
  template:
    metadata:
      labels: {app: jellyfin}
    spec:
      securityContext: {runAsUser: 1000, runAsGroup: 1000}
      containers:
        - name: app
          image: jellyfin/jellyfin
          volumeMounts:
            - {name: media, mountPath: /media}
            # Branch volumes must be mounted by some container, or kubelet never mounts them.
            - {name: remote, mountPath: /mnt/remote, readOnly: true}
      volumes:
        - name: remote
          persistentVolumeClaim: {claimName: media-rclone}
        - name: media
          csi:
            driver: mergerfs.csi.k8s.io
            volumeAttributes:
              branches: >-
                [{"type":"hostPath","path":"/srv/indigo/media-local","mode":"RW"},
                 {"type":"pvcRef","volumeName":"remote","mode":"NC"}]
              mergerfsOptions: category.create=pfrd,func.getattr=newest,cache.files=off,dropcacheonclose=false
```

`pvcRef` takes the pod's volume name, not the claim name. `NC` keeps creates off the remote; reads, renames and deletes still reach it. All keys: [volume parameters](docs/reference/volume-parameters.md).

## Docs

[docs/README.md](docs/README.md): scope, volume parameters, operations, metrics, design and ADRs.

## Development

`mise run check` (gofmt, vet, golangci-lint, unit tests, csi-sanity, shellcheck, helm lint) gates every commit. `mise run test:fuse` runs the shim + mergerfs FUSE test in Docker (`test:fuse:host` on a Linux host with sudo). `mise run e2e` brings up kind and runs `test/e2e`.

## License

Apache-2.0. The image ships [mergerfs](https://github.com/trapexit/mergerfs) (ISC).
