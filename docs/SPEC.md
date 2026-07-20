# mergerfs-csi — Spec (v1)

**Date:** 2026-07-20  
**Scope:** Track A Phase 1 + Phase 2 (node driver, hostPath + pod volume refs). No dynamic provisioner yet.  
**Plan source:** Obsidian `plans/2026-07-12-mergerfs-csi-k8s`

## Goal

Node-local Kubernetes CSI driver that runs `mergerfs -f` to union branch directories into a volume mount for a pod.

## Non-goals (v1)

- Controller provisioner / CreateVolume dynamic staging
- Cross-node RWX without shared branches
- Snapshots / clones
- meta-fuse-csi integration (documented as future validation only)

## Driver identity

- Name: `mergerfs.csi.k8s.io`
- Language: Go
- Module: `github.com/local-only/mergerfs-csi` (local; no remote required)

## CSI capabilities

| RPC | Behavior |
|-----|----------|
| NodeGetInfo | Return node ID from env/`NODE_ID` or hostname |
| NodeGetCapabilities | STAGE_UNSTAGE_VOLUME optional; implement STAGE as no-op or skip |
| NodePublishVolume | Resolve branches → start mergerfs → mount at target |
| NodeUnpublishVolume | fusermount/umount target; stop supervised mergerfs |
| Identity / Probe | Standard healthy probe |
| Controller | Identity only stub OR omit controller; node-only is enough for static PV |

## Volume context

```json
{
  "branches": "[{\"type\":\"hostPath\",\"path\":\"/data/a\"},{\"type\":\"pvcRef\",\"volumeName\":\"src\"}]",
  "mergerfsOptions": "category.create=mfs,cache.files=partial"
}
```

Branch types:

| type | fields | resolve to |
|------|--------|------------|
| `hostPath` | `path` | absolute host path |
| `pvcRef` | `volumeName` | kubelet CSI PVC mount under pod |
| `emptyDirRef` | `volumeName` | kubelet emptyDir path |
| `csiRef` | `volumeName`, optional `driverHint` | kubelet CSI volume mount |

Require `podInfoOnMount: true`. Use pod UID from CSI volume context (`csi.storage.k8s.io/pod.uid`).

Kubelet path patterns (Linux):

```
/var/lib/kubelet/pods/<podUID>/volumes/kubernetes.io~csi/<volName>/mount
/var/lib/kubelet/pods/<podUID>/volumes/kubernetes.io~empty-dir/<volName>
```

Configurable via `--kubelet-pods-path` (default `/var/lib/kubelet/pods`).

## Process model

1. `NodePublishVolume`: mkdir target; wait for branches (timeout); exec `mergerfs -f -o <opts> <b0:b1:...> <target>` as supervised child
2. Track PID per volume ID in memory + pidfile under `/var/lib/mergerfs-csi/<volID>.pid`
3. `NodeUnpublishVolume`: umount (fusermount3 -u or umount); kill process; remove pidfile

If target already mounted with expected source, treat as idempotent success.

## Security

DaemonSet needs: privileged or `SYS_ADMIN` + `/dev/fuse`, hostPath for kubelet pods dir (read), hostPath for optional branch roots.

## Testing

- Unit: branch JSON parse, path resolve, option sanitize
- Unit: mount command builder (no real FUSE required)
- Fake FS: tempfile branches + mock runner interface
- `go test ./...` must pass on Darwin (skip Linux-only mount tests with build tags)

## Helm

Chart `charts/mergerfs-csi`:

- DaemonSet node plugin + node-driver-registrar sidecar
- CSIDriver object
- optional example StorageClass / static PV templates under `examples/`

## Docs

- README: install, examples, EXDEV / no-subPath warnings
- `docs/architecture.md`, `docs/volume-layering.md`
- Track B note: `docs/meta-fuse-validation.md` (not implemented)

## Quality bar

KISS, small packages, interfaces for exec/mount, no premature abstraction, conventional commits, MIT or Apache-2.0 LICENSE.
