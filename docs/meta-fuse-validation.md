# Track B — meta-fuse validation (not implemented)

Parallel **validation track only**. mergerfs-csi v1 does **not** integrate [meta-fuse-csi-plugin](https://github.com/kubernetes-sigs/meta-fuse-csi-plugin). Do not ship product dependency on it.

## Intent

meta-fuse nests FUSE mounts under a CSI volume with tighter unprivileged / mount-propagation stories. Track B asks: can mergerfs run usefully under meta-fuse for the same volume-layering scenarios Track A solves with a privileged node plugin?

## Status

| Item | State |
|------|--------|
| Code in this repo | **None** |
| Helm / examples for meta-fuse | **None** |
| Support commitment | **None** — validation checklist only |

## Validation checklist (future)

Use when experimenting on a lab cluster; results are advisory.

1. **Baseline Track A.** Confirm privileged DaemonSet + `pvcRef` / `emptyDirRef` pool works (publish/unpublish, create policies, no EXDEV on rename within pool).
2. **Deploy meta-fuse-csi-plugin** per upstream docs: https://github.com/kubernetes-sigs/meta-fuse-csi-plugin
3. **Nest mergerfs** as the inner FUSE under a meta-fuse volume (exact wiring TBD; not specified here).
4. **Privilege delta.** Record whether meta-fuse removes need for privileged/`SYS_ADMIN` on the mergerfs worker vs Track A.
5. **Branch visibility.** Confirm inner mergerfs still sees **kubelet host paths** for PVC/emptyDir branches (or document an alternate resolve path). Failure mode: in-container-only paths → broken branches.
6. **Lifecycle.** Publish/unpublish, pod delete, node reboot: no stale FUSE, no leaked mounts.
7. **EXDEV.** Same anti-patterns as Track A — mount pool root only; no subPath; no separate branch binds.
8. **Perf / correctness.** Spot-check create policies (`category.create`), directory listing, concurrent writers.
9. **Decision.** Keep Track A as default; adopt meta-fuse only if privilege or operability gains outweigh complexity.

## Out of scope for Track B docs

- Implementing a meta-fuse backend in this repository
- Dual-stack production support
- Changing Track A security model based on incomplete experiments

## Links

- meta-fuse-csi-plugin: https://github.com/kubernetes-sigs/meta-fuse-csi-plugin
- Track A design: [architecture.md](architecture.md)
- Volume rules: [volume-layering.md](volume-layering.md)
