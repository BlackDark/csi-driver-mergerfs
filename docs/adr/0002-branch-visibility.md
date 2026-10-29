# Branch visibility in the Mount Pod

Status: accepted

A branch is either one of the consumer pod's volumes (`pvcRef`, `csiRef`, `emptyDirRef`) or a host path below an admin-allowed root (`hostPath`). The Mount Pod gets each of them as a hostPath volume with `mountPropagation: HostToContainer`, which an unprivileged container may use.

## Consumer volumes

kubelet mounts a pod's volumes below `<kubelet>/pods/<uid>/volumes/<plugin>/<name>[/mount]`, in parallel with our `NodePublishVolume`.

- Publish resolves the volume name through the pod spec (PVC → bound PV, which must be a CSI PV; generic ephemeral volume → PVC `<pod>-<volume>`; inline CSI; emptyDir). Before it starts the Mount Pod, it waits, bounded by the publish timeout, until each volume branch is a mount point in the plugin's mount table and answers `statfs`. Plain emptyDirs only have to exist. 0.1.x only waited for the dir, so mergerfs could start on the empty dir under a mount that was still coming.
- kubelet mounts only volumes that some container (or init container) lists in `volumeMounts`; checked on k3s 1.37: an unused PVC volume got no mount and no dir. Publish rejects such a branch with `InvalidArgument` instead of waiting.
- Each branch volume is a hostPath volume of its own at `/branches/<index>`, not the pod's whole `volumes` dir at its host path. `pods/<uid>` and `kubernetes.io~csi/<name>` are root `0750`, so a non-root mergerfs cannot get past them. The first in-cluster run, with the whole dir mounted, showed an empty rclone branch for that reason.
- `HostToContainer` makes a later remount of the branch volume (for example a replaced rclone Mount Pod) show up in the Mount Pod without a restart.

## hostPath branches

0.1.x let the plugin see only kubelet dirs, so `/srv/...` branches timed out. Now:

- `allowedHostPaths` (chart) / `--allowed-host-paths` (node) lists the roots. Any other host path is rejected with `InvalidArgument` that names the path. Paths with `:`, `=`, `,` or glob characters are rejected too: mergerfs gives them meaning in a branch list.
- Each root a pool uses is mounted into its Mount Pod at the same path (`HostToContainer`), so branch paths read the same on the host and in the pod. The node plugin mounts every root read-only to check that a branch exists.
- Anyone who may create a pod with an inline volume of this driver can read and write below these roots as `mountPod.uid`. Keep the roots narrow.

## Node plugin mounts

The plugin mounts `<kubeletDir>` once, `Bidirectional`. 0.1.x mounted `<kubeletDir>` and `<kubeletDir>/pods`, both `Bidirectional`, and every plugin restart stacked another copy of `/var/lib/kubelet/pods` on the host (seen growing from 3 to 7 copies). `mise run check` fails if the chart renders more than one `Bidirectional` mount.
