# Volume layering

mergerfs unions **directories on the node** into one FUSE mount. CSI resolves branch paths on the **host** (kubelet volume trees), then mounts the union at the CSI target for the pod.

## How-to: pvcRef / emptyDirRef

1. Declare branch volumes on the **same pod** as the mergerfs volume.
2. Point mergerfs `branches` at those volumes by **pod volume name** (`volumeName`), not PVC claim name alone.
3. Mount **only the mergerfs root** into the app container (no `subPath`).

### Example

```yaml
volumes:
  - name: src
    persistentVolumeClaim:
      claimName: data-a
  - name: scratch
    emptyDir: {}
  - name: pool
    csi:
      driver: mergerfs.csi.k8s.io
      volumeAttributes:
        branches: >-
          [{"type":"pvcRef","volumeName":"src"},
           {"type":"emptyDirRef","volumeName":"scratch"}]
        mergerfsOptions: "category.create=mfs,cache.files=partial"
containers:
  - name: app
    volumeMounts:
      - name: pool
        mountPath: /data
      # Do not also mount src/scratch into the app unless you accept EXDEV risks
```

### Branch types

| type | fields | host resolve |
|------|--------|----------------|
| `hostPath` | `path` | absolute path on node |
| `pvcRef` | `volumeName` | `…/kubernetes.io~csi/<pvName>/mount` — API maps pod volume → PVC → bound PV name; ephemeral CSI keeps pod volume name |
| `emptyDirRef` | `volumeName` | `.../volumes/kubernetes.io~empty-dir/<volName>` |
| `csiRef` | `volumeName` | CSI kubelet mount for that volume |

`volumeName` = key under `spec.volumes[]`.name of the **consumer pod**.

### Path rule

PVC-as-branch works **only** via kubelet host paths. The driver does not look inside the app container’s mount namespace for branch roots.

For PVC volumes, kubelet names the CSI dir after the **PV** (`pvc-…`), not the pod volume name. Node plugin uses the kube API (Pod → PVC → `VolumeName`) to map; ephemeral CSI volumes still use the pod volume name as the dir.

Default base: `/var/lib/kubelet/pods/<podUID>/volumes/...`  
Override: `--kubelet-pods-path`.

## Correct pattern

```text
[branch A] ─┐
[branch B] ─┼─► mergerfs (one FUSE mount) ─► /data in container
[branch C] ─┘
```

- One CSI publish per pool.
- App sees a single tree.
- Creates go to branches per `category.create` / mergerfs policy.

## Anti-patterns (EXDEV / broken semantics)

### 1. `subPath` on the mergerfs volume

```yaml
# BAD
volumeMounts:
  - name: pool
    mountPath: /data/subdir
    subPath: some/branch/path
```

Mount the **pool root** only. Subpaths fight FUSE and branch policies; avoid.

### 2. Separate bind mounts of branch subdirs into the same container

```yaml
# BAD — two mount points that look like one tree but are not
volumeMounts:
  - name: src
    mountPath: /data/a
  - name: scratch
    mountPath: /data/b
```

rename/link across those mounts → **`EXDEV`** (cross-device). mergerfs exists so the kernel sees one FS for the union; bypassing it reintroduces device boundaries.

### 3. Resolving PVC paths inside the container

Do not pass `/data/src`-style **container** paths as `hostPath` branches. Those paths do not exist (or differ) on the node where mergerfs runs. Use `pvcRef` / `emptyDirRef` / `csiRef`.

### 4. Cross-node pools without shared branches

Each node unions **local** branch paths. Without shared underlying storage, RWX across nodes is undefined / wrong.

## Checklist

- [ ] Branch volumes present on same pod before publish (driver waits with timeout).
- [ ] `podInfoOnMount` enabled on CSIDriver.
- [ ] App mounts pool root only; no `subPath`.
- [ ] No parallel binds of the same branch trees into the app “as the pool”.
- [ ] Linux nodes with FUSE available to the DaemonSet.
