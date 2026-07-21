# Track B — meta-fuse live validation

**Date:** 2026-07-21  
**Cluster:** `informaten` (k3s v1.36.1, node `debian-01` amd64)  
**Verdict:** **Failed — keep Track A as default**

## Goal

Can mergerfs run under [meta-fuse-csi-plugin](https://github.com/pfnet-research/meta-fuse-csi-plugin) fusermount3-proxy with unprivileged sidecar initContainers, for the same volume-layering scenarios Track A solves with a privileged node CSI driver?

## What we deployed

| Component | Image / source |
|-----------|----------------|
| meta-fuse CSI DaemonSet | `meta-fuse-csi-plugin:local` (built from upstream), ns `mfcp-system` |
| mergerfs sidecar | `ghcr.io/blackdark/mergerfs-mfcp:0.1.0-test3` — mergerfs 2.42.0 + fusermount3-proxy symlinks |
| Examples | `examples/meta-fuse/deploy-basic.yaml`, `deploy-layered.yaml` |

## Test results (informaten)

| Test | Expected | Actual |
|------|----------|--------|
| meta-fuse CSI DaemonSet | Running | **OK** (2/2) |
| Upstream sshfs proxy example (local build) | `MFCP_SSHFS_OK` | **FAIL** — `fuse: mount failed: Permission denied` |
| mergerfs 2× emptyDir (`deploy-basic`) | `MFCP_BASIC_OK` | **FAIL** — `mergerfs: mount failed: Permission denied` |
| mergerfs PVC + emptyDir (`deploy-layered`) | `MFCP_LAYERED_OK` | **Not reached** (blocked by basic) |
| Privilege delta (starter `privileged: false`) | N/A | Configured; never reached working mount |
| Track A baseline (same cluster, prior session) | `SMOKE_OK`, `LAYERED_OK` | **OK** |

## Root cause analysis

1. **CSI fd-passing socket is created** — logs show `start to accept connections to the listener`.
2. **Sidecar never connects** — no CSI log lines for `opening the device /dev/fuse` or successful fd SendMsg.
3. **libfuse fails before fusermount3-proxy** — starter logs show `Permission denied` (EACCES), not EPERM fallback to fusermount.
4. **Not mergerfs-specific** — locally built upstream sshfs example fails identically on the same cluster.
5. **Likely k3s / k8s 1.36 interaction** — pods receive a real `/dev/fuse` char device; meta-fuse docs assume `touch /dev/fuse` fake file to force EPERM→fusermount path. `rm + touch` and `hostUsers: true` did not fix on this node.

## Architecture notes (still valid if meta-fuse works elsewhere)

- FUSE impl must mount to **`/tmp`** (ignored mountpoint); CSI publishes at **`/data`** via fd-passing + mount propagation.
- Do **not** mount mergerfs directly at `/data`.
- Do **not** drop all capabilities on starter if libfuse needs EPERM fallback (upstream examples omit cap drop).
- `fuse-starter` + `/dev/fd/3` path unlikely for mergerfs (no libfuse fd mountpoint support).
- Track B branches use **in-pod paths** — no kubelet PVC→PV resolver (contrast Track A `pvcRef` fix).

## Decision matrix

| Criterion | Track A (mergerfs CSI) | Track B (meta-fuse) |
|-----------|------------------------|---------------------|
| Works on informaten | Yes | **No** |
| Privileged node DaemonSet | Yes | CSI only; sidecar unprivileged in theory |
| PVC branch resolution | kubelet path via API | in-pod mounts only |
| Operational complexity | Medium | High (2 CSI stacks + sidecar) |
| Upstream maintenance | Self | pfnet-research / k8s-sigs |

**Recommendation:** Stay on Track A. Revisit Track B on a cluster where upstream sshfs proxy example passes, or after meta-fuse documents k3s/k8s 1.36+ compatibility.

## Artifacts

```
examples/meta-fuse/
  Dockerfile.mergerfs-mfcp
  entrypoint.sh
  deploy-basic.yaml
  deploy-layered.yaml
```

## Cleanup (manual)

```bash
kubectl delete ns mfcp-validation mfcp-system --ignore-not-found
kubectl delete csidriver meta-fuse-csi-plugin.csi.storage.pfn.io --ignore-not-found
```

## Links

- meta-fuse-csi-plugin: https://github.com/pfnet-research/meta-fuse-csi-plugin
- Track A: [architecture.md](architecture.md)
