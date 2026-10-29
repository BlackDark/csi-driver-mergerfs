# Volume parameters

Volume attributes of an inline volume (`csi.volumeAttributes`), a PV (`csi.volumeAttributes`) or a StorageClass (`parameters`). Invalid ones fail the publish with `InvalidArgument` naming the key and branch index; kubelet shows the message in a `FailedMount` Event.

| Key | Required | Value |
|---|---|---|
| `branches` | yes | JSON list of branches, in mergerfs order |
| `mergerfsOptions` | no | comma-separated mergerfs `-o` options, allowlisted |
| `allowAllOptions` | no | `true` skips the option allowlist; PV and StorageClass only, not inline volumes |

## Branches

```json
[{"type":"hostPath","path":"/srv/media","mode":"RW"},
 {"type":"pvcRef","volumeName":"remote","mode":"NC"}]
```

| `type` | Field | Resolves to |
|---|---|---|
| `hostPath` | `path`: absolute host path below one of `allowedHostPaths` | the host path |
| `pvcRef` | `volumeName`: the consumer pod's volume name (not the claim name) | the pod's PVC or generic ephemeral volume; its PV must be a CSI PV |
| `csiRef` | `volumeName` | the pod's inline CSI volume |
| `emptyDirRef` | `volumeName` | the pod's emptyDir |

`mode` is mergerfs' per-branch mode, default `RW`:
- `RW`: reads, creates and actions (unlink, rename, chmod …).
- `NC`: no create. Reads and actions, but new files never land here. Use it for a remote branch that should only be read or replaced through the local branch.
- `RO`: no create and no actions (`EROFS`). mergerfs still opens existing files there for writing.

Rules:
- Every `volumeName` must be in some container's `volumeMounts`: kubelet does not mount unused volumes. Mounting it in an init container is enough.
- `*Ref` branches need the consumer pod (`podInfoOnMount`, set by the chart). A pool cannot be shared between pods: each publish gets its own Mount Pod.
- A PVC bound to a non-CSI PV (local, hostPath) is rejected: use a `hostPath` branch for local disks.
- `hostPath` paths may not contain `: = , * ? [ ] \`.

## mergerfsOptions

Allowed (mergerfs 2.42 names, `_` matches `-`): `category.action`, `category.create`, `category.search`, `func.<function>` (`access`, `chmod`, `chown`, `create`, `getattr`, `getxattr`, `link`, `listxattr`, `mkdir`, `mknod`, `open`, `readdir`, `readlink`, `removexattr`, `rename`, `rmdir`, `setxattr`, `symlink`, `truncate`, `unlink`, `utimens`), `cache.attr`, `cache.entry`, `cache.files`, `cache.files.process-names`, `cache.negative-entry`, `cache.readdir`, `cache.statfs`, `cache.symlinks`, `cache.writeback`, `async-read`, `branches-mount-timeout`, `branches-mount-timeout-fail`, `direct-io-allow-mmap`, `dropcacheonclose`, `export-support`, `flush-on-close`, `follow-symlinks`, `fsname`, `fuse-msg-size`, `handle-killpriv`, `handle-killpriv-v2`, `ignorepponrename`, `inodecalc`, `kernel-permissions-check`, `link-cow`, `link-exdev`, `minfreespace`, `moveonenospc`, `never-forget-nodes`, `nfsopenhack`, `noforget`, `nullrw`, `parallel-direct-writes`, `posix-acl`, `process-thread-count`, `process-thread-queue-depth`, `read-thread-count`, `remember-nodes`, `rename-exdev`, `security-capability`, `statfs`, `statfs-ignore`, `symlinkify`, `symlinkify-timeout`, `threads`, `xattr`.

Not allowed: options that move the pool or its branches (`branches`, `srcmounts`, `mount`, `mountpoint`, `config`), write files (`log.file`, `debug`, `pid`), change identities (`uid`, `gid`, `umask`, `allow-idmap`), or need root (`readahead`, `scheduling-priority`, `proxy-ioprio`, `passthrough.*`). `allowAllOptions: "true"` on a PV or StorageClass lifts the list and emits an `UnrestrictedOptions` Warning Event on each consumer. `auto_unmount`, `allow_other`, `default_permissions` and the branch and mount options stay reserved even then. Values may not contain whitespace, quotes, backslashes or commas.

## Inline, Static and Dynamic Volumes

- Inline (`csi:` in the pod spec): the common case, since branches are the pod's own volumes.
- Static PV: the same attributes in `csi.volumeAttributes`; `volumeHandle` is free-form.
- Dynamic (StorageClass `provisioner: mergerfs.csi.blackdark.github.io`): the parameters are copied into the PV. The controller checks the syntax; the `hostPath` allowlist is checked by the node at publish. `*Ref` branches then name volumes every consuming pod must have. Deleting the PVC removes only the PV object.
