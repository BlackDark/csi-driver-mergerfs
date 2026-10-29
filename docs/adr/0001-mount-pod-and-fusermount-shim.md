# Unprivileged Mount Pod per pool, FUSE fd through a fusermount shim

Status: accepted

Each published pool runs the stock static mergerfs in its own Mount Pod in the driver namespace. The Mount Pod is unprivileged: non-root, all capabilities dropped, read-only root filesystem, seccomp `RuntimeDefault`, no `/dev/fuse`. The privileged node plugin does the FUSE mount and passes the fd to mergerfs through a fusermount helper we ship.

## Why

Up to 0.1.x mergerfs ran inside the node plugin. Restarting or upgrading the plugin killed every mergerfs process, and every pool returned `Transport endpoint is not connected` until its pods restarted. A FUSE connection cannot move to a new process, so the process that serves it has to outlive the plugin.

## How mergerfs gets the fd

mergerfs bundles its own libfuse. Its `fuse_kern_mount` (`vendored/libfuse/lib/mount_generic.h`, 2.42.0) first tries `mount(2)` itself (`fuse_mount_sys`), and calls the fusermount helper only when that returns -2. It does not accept a `/dev/fd/N` mountpoint: it resolves it and mounts elsewhere.

- Without `/dev/fuse`, `fuse_mount_sys` returns -1 (`open` fails) and mergerfs gives up. It never reaches the helper.
- With `-o auto_unmount`, `fuse_mount_sys` returns -2 before it opens `/dev/fuse`. mergerfs then forks the helper with `_FUSE_COMMFD=<socketpair fd>` and `-o <mount options> -- <mountpoint>`, and waits for one byte with the fd as `SCM_RIGHTS`.
- The helper is found by `exec_fusermount`: `$FUSERMOUNT_DIR/mergerfs-fusermount` first, then `mergerfs-fusermount` on `PATH`, then `fusermount`/`fusermount3`. The upstream static build has `FUSERMOUNT_DIR=/usr/local/bin` (`strings` on the binary).

So the image installs our shim (`cmd/mergerfs-fusermount`) at `/usr/local/bin/mergerfs-fusermount` and leaves out upstream's setuid helper, and the Mount Pod always passes `auto_unmount`. The shim sends the options to the node plugin over the pool's handoff socket. The plugin checks them against an allowlist, opens `/dev/fuse`, mounts `fuse.mergerfs` on the pool's `fuse/` dir with `allow_other` and the Mount Pod's uid/gid, and returns the fd. The shim hands it to mergerfs over `_FUSE_COMMFD`. mergerfs's `fusermount -u` on exit only gets an acknowledgement: the plugin owns unmounting. Protocol: [Mount Pod contract](../design/mount-pod-contract.md#handoff-protocol).

## Spike evidence

oci-test, arm64, Ubuntu 26.04, kernel 7.0, mergerfs 2.42.0 (distro build and upstream `static-linux_arm64` tarball). `internal/fusemount/fuse_integration_test.go` (`mise run test:fuse:host`): a root test process plays the plugin. mergerfs runs as uid 1000 with only the shim on `PATH` and branches `rw=RW:nc=NC:ro=RO`.

- The shim was called with `rw,nosuid,nodev,default_permissions,auto_unmount,fsname=fit,subtype=mergerfs`; the plugin-side mount served it.
- Reads from the NC branch, creates on the RW branch, rename, `EROFS` on unlink in the RO branch: all passed.
- Killing mergerfs gives `ENOTCONN` on the pool until the plugin unmounts it.
- In-cluster (k3s 1.37): a Mount Pod with `privileged: false`, `capabilities.drop: [ALL]`, non-root and a read-only root filesystem served a pool over a hostPath branch and an rclone CSI PVC (Box). A node plugin restart adopted it while a writer in the consumer kept writing every second without an error.

## Considered: privileged Mount Pod running mergerfs as root with `/dev/fuse`

It needs no shim, but every pool would be a privileged, root container, which is what the old in-plugin design was. Rejected: the shim is small, and the privilege stays in the node plugin.

## Considered: FUSE in the node plugin

That is the old design, and it fails on every restart. Rejected.

## Consequences

- mergerfs is not root, so it cannot switch to the caller's uid per request. Every branch access runs as `mountPod.uid`/`gid` (the "not running as root" warning in its log). Set them to the owner of the branch data. Per-file ownership by different callers is not possible.
- One Mount Pod per published pool (per volume and target). Its branches are the consumer pod's volumes, so a pool cannot be shared across pods.
- A crashed mergerfs cannot be resumed (`RestartPolicy: Never`). The consumer gets `MountLost` and must restart. Opt-in healing evicts controller-owned consumers.
- mergerfs options that need root (`readahead`, `scheduling-priority`, `passthrough.*`) are not allowlisted.
