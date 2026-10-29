# Mount Pod contract

How the node plugin and a Mount Pod work together. Background: [ADR 0001](../adr/0001-mount-pod-and-fusermount-shim.md), [ADR 0002](../adr/0002-branch-visibility.md).

## Names and paths

- `<id>`: the first 16 hex characters of sha256(volume ID, NUL, target path). One pool per publish.
- Mount Pod `mergerfs-pool-<id>` in the driver namespace. Labels `app.kubernetes.io/name=csi-driver-mergerfs`, `app.kubernetes.io/component=pool`, `mergerfs.csi.blackdark.github.io/node=<node>`, `mergerfs.csi.blackdark.github.io/id=<id>`. Annotations `mergerfs.csi.blackdark.github.io/volume-id`, `…/target` and `…/workload` (`{"ns","name","uid"}` of the consumer pod, from `podInfoOnMount`).
- Pool dir on the host: `<kubeletDir>/plugins/mergerfs.csi.blackdark.github.io/pools/<id>/`, owned by the Mount Pod uid/gid, mode `0750`:
  - `fuse/`: the pool's FUSE mountpoint. Only the node plugin mounts it.
  - `handoff.sock`: unix socket served by the node plugin, mode `0660`.
- The Mount Pod mounts the pool dir at `/run/mergerfs-pool` with propagation `None`, so the plugin's later mount on `fuse/` stays invisible to it. mergerfs is told to mount on `/run/mergerfs-pool/fuse`; nothing is ever mounted there.
- Branches: consumer volume `i` at `/branches/<i>`, allowed hostPath roots at their host path, all `HostToContainer` (ADR 0002).
- Pod: `restartPolicy: Never`, `automountServiceAccountToken: false`, `enableServiceLinks: false`, non-root uid/gid from `mountPod.uid`/`gid`, no capabilities, read-only root filesystem, `terminationGracePeriodSeconds: 10`, priority `system-node-critical`. The container runs `/usr/local/bin/mergerfs -f -o auto_unmount,<mergerfsOptions> <branches> /run/mergerfs-pool/fuse` with env `MERGERFS_CSI_HANDOFF=/run/mergerfs-pool/handoff.sock`.

## Handoff protocol

Package `internal/handoff`. One ASCII line per message, one request per connection.

| Shim sends | Plugin replies |
|---|---|
| `mount <base64 of the fusermount -o string>` | `fd` with the FUSE fd as `SCM_RIGHTS`; at most once per pool |
| `mount …` again, or after adoption | `reject mount-exists` |
| `mount …` with an option outside the allowlist | `reject mount-failed <error>` |
| `unmount` | `ok` (the plugin owns unmounting) |
| anything else | `reject protocol` |

Kernel option allowlist (`fusemount.ParseOpts`): `rw`, `ro`, `nosuid`, `nodev`, `exec`, `noexec`, `atime`, `noatime` and the other atime flags, `async`, `default_permissions`, `fsname=`, `subtype=`, `max_read=`, `blksize=`; `allow_other`, `auto_unmount` and `large_read` are accepted and ignored. The plugin always sets `nosuid`, `nodev`, `allow_other`, `rootmode`, `user_id`/`group_id` (the Mount Pod's) and fstype `fuse.mergerfs`. `suid`, `dev`, `blkdev`, SELinux contexts and anything else fail the mount.

## Sequence

1. `NodePublishVolume`: parse the volume attributes ([volume parameters](../reference/volume-parameters.md)); errors are `InvalidArgument` and show in kubelet's `FailedMount` Event. Take the `<id>` lock. A healthy target returns OK; a dead bind is unmounted and the pool replaced.
2. Resolve the branches against the consumer pod and wait until they are usable (ADR 0002), bounded by `--publish-timeout` (default 90s); a timeout is `Unavailable` naming the branches still pending, and kubelet retries.
3. Create the pool dir and `fuse/`, serve `handoff.sock`, create the Mount Pod. A Pod still terminating fails the publish with `Unavailable`.
4. mergerfs runs the shim; the plugin mounts `fuse/` and sends the fd. The plugin then waits for `statfs` on `fuse/`, which returns once mergerfs has answered FUSE init.
5. Bind-mount `fuse/` onto the target (`ro` remount when the publish is read-only). With `allowAllOptions`, emit a Warning Event `UnrestrictedOptions` on the consumer pod. Any failure in steps 3 to 5 deletes the Mount Pod and unmounts `fuse/`.
6. `NodeUnpublishVolume`: unmount the target, delete the Mount Pod, lazily unmount `fuse/`, remove the pool dir. The id follows from volume ID and target, so this also works for a pool the plugin lost track of.

## Restart and failure

- Node plugin restart: before serving CSI calls, the plugin lists its Mount Pods (label `…/node`) and reads `/proc/self/mountinfo`. A pool is adopted when `fuse/` is mounted and answers `statfs` and its Mount Pod is alive. Target and consumer come from the annotations. Anything else is cleaned up: `fuse/` lazily unmounted, Pod deleted, dirs removed. A pool that had served gets its target marked dead, a `MountLost` Event on the consumer, and healing if enabled.
- Mount Pod lost (Failed, Succeeded or deleted while the plugin did not stop it, seen by the Pod watch): the plugin marks the target dead, emits Warning `MountLost` on the Mount Pod and the consumer, deletes the Pod, and with `--heal-workloads` evicts the consumer. The pool is not recreated: the consumer's containers hold the old bind, which returns `ENOTCONN` until the pod restarts. The next publish of the target replaces the dead bind.
- Health: `NodeGetVolumeHealth` reports a dead target, or one whose `statfs` returns `ENOTCONN`, as `INACCESSIBLE` with reason `MountPodLost` (`pod.status.volumeHealth` with feature gate `CSIVolumeHealth`). `NodeGetVolumeStats` returns statfs usage, or `Unavailable` for `ENOTCONN`.

## Workload healing

Opt-in with `--heal-workloads` (chart `mountPod.healWorkloads.enabled`), copied from csi-driver-rclone. For the consumer of a lost pool, in the background:

1. get the pod; skip it when gone, terminating, or its UID differs from the recorded one,
2. no controller ownerReference: Warning `HealBlocked`, not evicted,
3. else a `policy/v1` Eviction with a UID precondition. A PodDisruptionBudget refusal (429) is retried with backoff from 1s, doubling to 30s, for up to 5 minutes, then Warning `HealBlocked`. Success: Normal `WorkloadHealed`.

Each outcome counts in `csi_mergerfs_workload_heals_total{result=evicted|skipped|blocked}`.
