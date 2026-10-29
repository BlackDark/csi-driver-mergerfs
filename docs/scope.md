# Scope

What the driver does, what it does not do, and why. Terms: [GLOSSARY.md](../GLOSSARY.md).

## Possible

- Pools over the consumer pod's own volumes (any CSI PVC, generic ephemeral volumes, inline CSI volumes, emptyDirs) and over host paths below admin-allowed roots, in any order and mix.
- Per-branch mode `RW`, `RO` or `NC`, so a local disk can take every write while a remote branch (for example an rclone volume) is only read.
- Inline, Static and Dynamic Volumes. Dynamic Volumes copy the StorageClass parameters into the PV; there is nothing to provision.
- Allowlisted mergerfs options; `allowAllOptions` on PVs and StorageClasses for anything else.
- Pools survive node plugin restarts and `helm upgrade`: the pool's mergerfs runs in its own Mount Pod, and the restarted plugin adopts it (ADR 0001).
- Publish waits until every volume branch is mounted and answers `statfs`, so mergerfs never starts on the empty dir below a mount that is still coming (ADR 0002).
- A crashed Mount Pod is reported: `MountLost` Events, `Inaccessible` in `pod.status.volumeHealth` (feature gate `CSIVolumeHealth`), metrics. Opt-in healing evicts controller-owned consumers so they come back with a fresh pool.
- Read-only per publish (`readOnly` volume or a reader-only access mode).
- Prometheus metrics for the node plugin and controller.

## Security posture

- The node plugin is the only privileged component: it opens `/dev/fuse`, mounts and bind-mounts with one `Bidirectional` mount of the kubelet dir. No hostNetwork.
- Mount Pods are unprivileged: non-root, no capabilities, read-only root filesystem, seccomp `RuntimeDefault`, no `/dev/fuse`, no service account token. They get only their pool dir and their branches as hostPath volumes. Their namespace needs the `privileged` Pod Security level for those hostPath volumes.
- hostPath branches are off until an admin sets `allowedHostPaths`; anything outside is rejected. Whoever may create pods with an inline volume of this driver can read and write below the allowed roots as `mountPod.uid`.
- mergerfs kernel options from the Mount Pod are allowlisted by the plugin; `suid` and `dev` are never set.
- RBAC: pods `get`, `list`, `watch`, `create`, `delete` in the driver namespace; cluster-wide `get` on pods, PVCs and PVs (branch resolution) and Events `create`/`patch`; `pods/eviction` `create` only with healing. No Secrets. The controller has only what external-provisioner needs.
- Metrics endpoints are unauthenticated plain HTTP on the pod network.

## Not possible, by design

- Sharing one pool across pods: branches are one pod's volumes, so every publish gets its own Mount Pod.
- Per-caller file ownership: mergerfs runs as `mountPod.uid`, not as root, so it cannot act as each caller (ADR 0001).
- Resuming a pool after its mergerfs died: the consumer must restart. A FUSE connection cannot move to a new process.
- Branches from non-CSI PVs (local, hostPath PVs): use a `hostPath` branch.
- Volumes no container mounts: kubelet does not mount them, and publish rejects them.
- mergerfs options that need root, write files or change the branches.
- Windows nodes; Kubernetes < 1.34.
