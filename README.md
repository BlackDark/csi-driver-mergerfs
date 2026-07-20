# mergerfs-csi

Node-local Kubernetes CSI driver that runs [`mergerfs`](https://github.com/trapexit/mergerfs) (FUSE) to union branch directories into a single volume mount for a pod.

**Driver name:** `mergerfs.csi.k8s.io`  
**Status:** Phase 1+2 node driver (in progress). No dynamic provisioner.  
**Platform:** Linux only (FUSE / kubelet host paths).

## Docs

| Doc | Topic |
|-----|--------|
| [docs/SPEC.md](docs/SPEC.md) | Spec / scope |
| [docs/architecture.md](docs/architecture.md) | CSI node design, lifecycle, privileges |
| [docs/volume-layering.md](docs/volume-layering.md) | `pvcRef` / `emptyDirRef`, EXDEV anti-patterns |
| [docs/meta-fuse-validation.md](docs/meta-fuse-validation.md) | Track B validation (not implemented) |
| [docs/development.md](docs/development.md) | Build, test, layout |

## Quickstart

### Build

```bash
cd mergerfs-csi
make build          # or: go build -o bin/mergerfs-csi ./cmd/mergerfs-csi
make image          # container image (Linux)
```

### Helm install

```bash
helm install mergerfs-csi ./charts/mergerfs-csi \
  --namespace kube-system \
  --set image.repository=<your-registry>/mergerfs-csi \
  --set image.tag=<tag>
```

Chart deploys a DaemonSet (node plugin + `node-driver-registrar`) and a `CSIDriver` with `podInfoOnMount: true`.

### Example usage

1. Mount branch volumes on the **same pod** (PVC, emptyDir, or hostPath).
2. Mount a mergerfs CSI volume whose `volumeAttributes.branches` JSON references those volumes by name (or host paths).
3. App uses the mergerfs mount only — do **not** remount branch subdirs separately into the app.

Static PV / example manifests: `examples/` (when present).

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
- **PVC-as-branch = kubelet host paths.** Branch resolution uses paths under `/var/lib/kubelet/pods/<uid>/...`, not in-container mount paths.
- **Linux only.** Unit tests may run on Darwin; real mount tests are Linux/build-tagged.
- **Node-local.** No cross-node RWX without shared branch storage. No snapshots/clones in v1.

## License

Apache-2.0 — see [LICENSE](LICENSE).
