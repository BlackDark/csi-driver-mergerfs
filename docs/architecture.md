# Architecture

Node-only CSI driver. No controller provisioner in v1. Static PVs (or inline volume attributes) are enough.

## Components

```
Pod ──► kubelet ──► CSI NodePublish ──► mergerfs-csi (DaemonSet)
                                           │
                                           ├─ resolve branches (hostPath / kubelet vol paths)
                                           ├─ exec mergerfs -f -o <opts> <b0:b1:...> <target>
                                           └─ pidfile /var/lib/mergerfs-csi/<volID>.pid
```

| Piece | Role |
|-------|------|
| DaemonSet node plugin | gRPC CSI Identity + Node services |
| `node-driver-registrar` | Registers driver with kubelet |
| `mergerfs` binary | FUSE union FS (in image or on host) |
| `CSIDriver` | `mergerfs.csi.k8s.io`, `podInfoOnMount: true` |

Controller RPCs: omit or Identity stub only. Dynamic `CreateVolume` is out of scope.

## CSI RPCs (v1)

| RPC | Behavior |
|-----|----------|
| Identity / Probe | Healthy if process up |
| NodeGetInfo | Node ID from `NODE_ID` env or hostname |
| NodeGetCapabilities | STAGE optional; STAGE may be no-op / skipped |
| NodePublishVolume | Resolve branches → start supervised `mergerfs -f` → mount at target |
| NodeUnpublishVolume | `fusermount3 -u` / `umount` → kill process → remove pidfile |

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
.../pods/<podUID>/volumes/kubernetes.io~csi/<volName>/mount
.../pods/<podUID>/volumes/kubernetes.io~empty-dir/<volName>
```

| Branch type | Resolves via |
|-------------|--------------|
| `hostPath` | Absolute host path |
| `pvcRef` / `csiRef` | Kubelet CSI PVC mount path for that pod volume name |
| `emptyDirRef` | Kubelet emptyDir path for that pod volume name |

**Important:** refs use **host** paths under kubelet, never container-local paths. Driver runs on the node and sees the same filesystem namespace as kubelet volume plugins (via privileged/host mounts).

## Privilege model

DaemonSet typically needs:

- Privileged **or** `SYS_ADMIN` + device `/dev/fuse`
- hostPath (read) to kubelet pods dir for `pvcRef` / `emptyDirRef` / `csiRef`
- hostPath to optional fixed branch roots (`hostPath` branches)
- Bidirectional/propagation as required for FUSE mounts into kubelet targets

Without FUSE capability, `NodePublishVolume` fails. Treat the node plugin as a privileged storage agent.

## Non-goals (v1)

- Dynamic provisioning / CreateVolume staging
- Cross-node RWX without shared branches
- Snapshots / clones
- meta-fuse nesting (see [meta-fuse-validation.md](meta-fuse-validation.md))

## Related

- [volume-layering.md](volume-layering.md) — how pods should compose volumes
- [SPEC.md](SPEC.md) — normative v1 scope
