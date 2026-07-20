# mergerfs-csi

Node-local Kubernetes CSI driver that runs [`mergerfs`](https://github.com/trapexit/mergerfs) (FUSE) to union branch directories into a single volume mount for a pod. Optional minimal controller for dynamic PVCs (params → VolumeContext).

**Driver name:** `mergerfs.csi.k8s.io`  
**Status:** Phase 1–3 implemented (controller minimal).  
**Platform:** Linux only (FUSE / kubelet host paths).

## Docs

| Doc | Topic |
|-----|--------|
| [docs/SPEC.md](docs/SPEC.md) | Spec / scope |
| [docs/architecture.md](docs/architecture.md) | CSI node + controller design |
| [docs/volume-layering.md](docs/volume-layering.md) | `pvcRef` / `emptyDirRef`, EXDEV anti-patterns |
| [docs/meta-fuse-validation.md](docs/meta-fuse-validation.md) | Track B validation (not implemented) |
| [docs/development.md](docs/development.md) | Build, test, layout |

## Quickstart

### Build

```bash
make build          # bin/mergerfs-csi
make test           # go test ./...
make docker-build   # linux/amd64 image (bakes mergerfs from GitHub releases)
```

Image always downloads a static mergerfs binary. Version pinned in `Dockerfile`:

```dockerfile
# renovate: datasource=github-releases depName=trapexit/mergerfs
ARG MERGEFS_VERSION=2.42.0
```

Renovate (`renovate.json`) bumps `MERGEFS_VERSION` via regex customManager. Override at build time: `--build-arg MERGEFS_VERSION=...`.

### Helm install

```bash
helm install mergerfs-csi ./charts/mergerfs-csi \
  --namespace kube-system \
  --set image.repository=<your-registry>/mergerfs-csi \
  --set image.tag=<tag>
```

Chart deploys a DaemonSet (node plugin + `node-driver-registrar`, `--mode=node`) and a `CSIDriver` with `podInfoOnMount: true`.

Optional controller + StorageClass:

```bash
helm upgrade --install mergerfs-csi ./charts/mergerfs-csi \
  --namespace kube-system \
  --set controller.enabled=true \
  --set storageClass.create=true
```

### Example usage

1. Mount branch volumes on the **same pod** (PVC, emptyDir, or hostPath).
2. Mount a mergerfs CSI volume whose `volumeAttributes.branches` JSON references those volumes by name (or host paths).
3. App uses the mergerfs mount only — do **not** remount branch subdirs separately into the app.

Examples: `examples/static-pv-hostpath.yaml`, `examples/layered-pvc-emptydir.yaml`, `examples/layered-rclone.yaml`.

Volume context sketch:

```yaml
volumeAttributes:
  branches: '[{"type":"pvcRef","volumeName":"src"},{"type":"emptyDirRef","volumeName":"scratch"}]'
  mergerfsOptions: "category.create=mfs,cache.files=partial"
```

See [volume layering](docs/volume-layering.md) for full branch types and path rules.

## Warnings

- **FUSE privilege.** Node plugin needs privileged (or `SYS_ADMIN` + `/dev/fuse`) and hostPath access to the kubelet pods directory.
- **EXDEV.** Mount the **pool root** only. No `subPath` into mergerfs. Do not bind-mount branch subdirs as separate volumes into the same container — rename/move across branches fails with `EXDEV`.
- **PVC-as-branch = kubelet host paths.** Branch resolution uses paths under `/var/lib/kubelet/pods/<uid>/...`, not in-container mount paths. `pvcRef` maps pod volume name → bound PV name via the API (`…/kubernetes.io~csi/<pvName>/mount`).
- **Linux only.** Unit tests may run on Darwin; real mount tests are Linux/build-tagged.
- **Node-local.** No cross-node RWX without shared branch storage. Controller CreateVolume does not allocate disk — only echoes SC parameters.

## License

Apache-2.0 — see [LICENSE](LICENSE).
