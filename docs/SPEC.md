# mergerfs-csi — Spec (v1)

**Date:** 2026-07-20  
**Scope:** Track A Phase 1–3 (node driver, hostPath + pod volume refs, minimal controller).  
**Status:** Implemented: Phase 1–3 (controller minimal — CreateVolume/DeleteVolume echo params)  
**Plan source:** Obsidian `plans/2026-07-12-mergerfs-csi-k8s`

## Goal

Node-local Kubernetes CSI driver that runs `mergerfs -f` to union branch directories into a volume mount for a pod. Optional lightweight controller copies StorageClass parameters into VolumeContext for dynamic PVCs.

## Non-goals (v1)

- Real disk allocation / capacity enforcement beyond RequiredBytes validation
- VolumeContentSource (clones/snapshots)
- Cross-node RWX without shared branches
- Snapshots / clones
- meta-fuse-csi integration (documented as future validation only)

## Driver identity

- Name: `mergerfs.csi.k8s.io`
- Language: Go
- Module: `github.com/BlackDark/csi-driver-mergerfs/mergerfs-csi`
- Modes: `--mode=node|controller|all` (default `node`)

## CSI capabilities

| RPC | Behavior |
|-----|----------|
| NodeGetInfo | Return node ID from env/`NODE_ID` or hostname |
| NodeGetCapabilities | STAGE_UNSTAGE_VOLUME optional; implement STAGE as no-op or skip |
| NodePublishVolume | Resolve branches → start mergerfs → mount at target |
| NodeUnpublishVolume | fusermount/umount target; stop supervised mergerfs |
| Identity / Probe | Standard healthy probe; advertise CONTROLLER_SERVICE when mode=controller\|all |
| CreateVolume | Validate capacity; copy `parameters` → Volume.VolumeContext; no disk alloc |
| DeleteVolume | No-op success |
| ControllerGetCapabilities | CREATE_DELETE_VOLUME only |

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
| `pvcRef` | `volumeName` | kubelet CSI mount: PVC → PV name via API (`…/kubernetes.io~csi/<pvName>/mount`); ephemeral CSI uses pod volume name |
| `emptyDirRef` | `volumeName` | kubelet emptyDir path |
| `csiRef` | `volumeName` | kubelet CSI volume mount (`driverHint` reserved, unused in v1) |

Require `podInfoOnMount: true`. Use pod UID / name / namespace from CSI volume context (`csi.storage.k8s.io/pod.*`).

Kubelet path patterns (Linux):

```
/var/lib/kubelet/pods/<podUID>/volumes/kubernetes.io~csi/<pvName-or-ephemeralVolName>/mount
/var/lib/kubelet/pods/<podUID>/volumes/kubernetes.io~empty-dir/<volName>
```

For `pvcRef`, the CSI directory is the **bound PV name** (e.g. `pvc-edd9e34a-…`), not the pod volume name. The node plugin resolves Pod → PVC → `pvc.Spec.VolumeName` via the API (fast path: if `…/csi/<volumeName>/mount` already exists, use that — typical for ephemeral CSI).

Configurable via `--kubelet-pods-path` (default `/var/lib/kubelet/pods`).

## Process model

1. `NodePublishVolume`: mkdir target; wait for branches (timeout); exec `mergerfs -f -o <opts> <b0:b1:...> <target>` as supervised child
2. Track PID per volume ID in memory + pidfile under `/var/lib/mergerfs-csi/<volID>.pid`
3. `NodeUnpublishVolume`: umount (fusermount3 -u or umount); kill process; remove pidfile

If target already mounted, treat as idempotent success (mount presence only in v1).

## Security

DaemonSet needs: privileged or `SYS_ADMIN` + `/dev/fuse`, hostPath for kubelet pods dir (read), hostPath for optional branch roots.

## Testing

- Unit: branch JSON parse, path resolve, option sanitize
- Unit: mount command builder (no real FUSE required)
- Unit: CreateVolume copies parameters to VolumeContext
- Fake FS: tempfile branches + mock runner interface
- `go test ./...` must pass on Darwin (skip Linux-only mount tests with build tags)

## Helm

Chart `charts/mergerfs-csi`:

- DaemonSet node plugin + node-driver-registrar sidecar (`--mode=node`)
- Optional controller Deployment + `csi-provisioner` sidecar (`controller.enabled=true`, `--mode=controller`)
- CSIDriver object
- optional StorageClass / static PV templates under `examples/`

## Docs

- README: install, examples, EXDEV / no-subPath warnings, Renovate `MERGEFS_VERSION`
- `docs/architecture.md`, `docs/volume-layering.md`
- Track B note: `docs/meta-fuse-validation.md` (not implemented)

## Quality bar

KISS, small packages, interfaces for exec/mount, no premature abstraction, conventional commits, MIT or Apache-2.0 LICENSE.
