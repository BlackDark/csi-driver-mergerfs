# Architecture

Node CSI driver + optional minimal controller. Static PVs / ephemeral volumes work without controller; enable controller for dynamic PVCs that echo StorageClass params into VolumeContext.

## Components

```
Pod ──► kubelet ──► CSI NodePublish ──► mergerfs-csi (DaemonSet, --mode=node)
                                           │
                                           ├─ resolve branches (hostPath / kubelet vol paths)
                                           ├─ exec mergerfs -f -o <opts> <b0:b1:...> <target>
                                           └─ pidfile /var/lib/mergerfs-csi/<volID>.pid

PVC ──► csi-provisioner ──► CreateVolume ──► mergerfs-csi (Deployment, --mode=controller)
                                              └─ copy SC parameters → VolumeContext (no disk)
```

| Piece | Role |
|-------|------|
| DaemonSet node plugin | gRPC CSI Identity + Node (`--mode=node`) |
| Deployment controller | Identity + Controller Create/Delete (`--mode=controller`) |
| `node-driver-registrar` | Registers driver with kubelet |
| `csi-provisioner` | Watches PVCs; calls CreateVolume |
| `mergerfs` binary | FUSE union FS (baked from GitHub releases) |
| `CSIDriver` | `mergerfs.csi.k8s.io`, `podInfoOnMount: true` |

## CSI RPCs

| RPC | Behavior |
|-----|----------|
| Identity / Probe | Healthy if process up; CONTROLLER_SERVICE when mode=controller\|all |
| NodeGetInfo | Node ID from `NODE_ID` env or hostname |
| NodeGetCapabilities | STAGE optional; STAGE may be no-op / skipped |
| NodePublishVolume | Resolve branches → start supervised `mergerfs -f` → mount at target |
| NodeUnpublishVolume | `fusermount3 -u` / `umount` → kill process → remove pidfile |
| CreateVolume | Validate capacity; copy `parameters` → VolumeContext |
| DeleteVolume | No-op |
| ControllerGetCapabilities | CREATE_DELETE_VOLUME |

Idempotent publish: if target already mounted (`IsMounted`), succeed (best-effort; no source/branch re-check in v1).

## Process lifecycle

1. **Publish:** `mkdir` target; wait for branch paths (timeout); spawn:

   ```text
   mergerfs -f -o <mergerfsOptions> <branch0:branch1:...> <target>
   ```

2. **Track:** pidfile under `/var/lib/mergerfs-csi/<sha256(volID)>.pid` (hashed name).

3. **Unpublish:** unmount target; kill supervised PID; delete pidfile.

Supervisor owns the FUSE process for the volume lifetime. Crash of mergerfs → mount gone; kubelet may republish.

## Branch resolution

Requires `podInfoOnMount: true`. Pod UID from CSI context (`csi.storage.k8s.io/pod.uid`).

Kubelet Linux patterns (default `--kubelet-pods-path=/var/lib/kubelet/pods`):

```text
.../pods/<podUID>/volumes/kubernetes.io~csi/<pvName-or-ephemeralVolName>/mount
.../pods/<podUID>/volumes/kubernetes.io~empty-dir/<volName>
```

| Branch type | Resolves via |
|-------------|--------------|
| `hostPath` | Absolute host path |
| `pvcRef` / `csiRef` | Kubelet CSI mount: try pod `volumeName` dir first (ephemeral); else kube API maps PVC → bound PV name (`pvc-…`) as the CSI dir |
| `emptyDirRef` | Kubelet emptyDir path for that pod volume name |

**Important:** refs use **host** paths under kubelet, never container-local paths. Driver runs on the node and sees the same filesystem namespace as kubelet volume plugins (via privileged/host mounts). Node SA needs get/list on pods, PVCs, and PVs for `pvcRef` resolution.

## Privilege model

DaemonSet typically needs:

- Privileged **or** `SYS_ADMIN` + device `/dev/fuse`
- hostPath (read) to kubelet pods dir for `pvcRef` / `emptyDirRef` / `csiRef`
- hostPath to optional fixed branch roots (`hostPath` branches)
- Bidirectional/propagation as required for FUSE mounts into kubelet targets

Controller Deployment does not need FUSE privileges (no mounts).

## Non-goals (v1)

- Real capacity / disk allocation (CreateVolume is param echo only)
- VolumeContentSource / snapshots / clones
- Cross-node RWX without shared branches
- meta-fuse nesting (see [meta-fuse-validation.md](meta-fuse-validation.md))

## Related

- [volume-layering.md](volume-layering.md) — how pods should compose volumes
- [SPEC.md](SPEC.md) — normative v1 scope
