# Development

## Layout

```text
local-only/
├── README.md
├── LICENSE
├── docs/
│   ├── SPEC.md
│   ├── architecture.md
│   ├── volume-layering.md
│   ├── meta-fuse-validation.md
│   └── development.md
├── charts/mergerfs-csi/     # Helm (DaemonSet, optional controller, CSIDriver)
├── examples/                # static PV / sample pods
├── Dockerfile               # bakes mergerfs from GitHub releases (Renovate-tracked)
├── renovate.json
└── mergerfs-csi/            # Go module
    ├── go.mod               # github.com/local-only/mergerfs-csi
    ├── cmd/mergerfs-csi/
    └── pkg/
        ├── branches/        # JSON parse, path resolve
        ├── driver/          # CSI Identity + Node + Controller
        └── mergerfs/        # mount command, process supervise
```

Paths may land as code lands; treat SPEC as source of truth for behavior.

## Prerequisites

- Go (module version per `go.mod`)
- Docker / buildx for images (Linux/`linux/amd64` or `linux/arm64`)
- Helm 3, kubectl, Linux cluster with FUSE for e2e
- `mergerfs` baked into image from trapexit releases (`MERGEFS_VERSION`)

## Make targets

From repo root:

| Target | Purpose |
|--------|---------|
| `make build` | Compile `bin/mergerfs-csi` |
| `make test` | `go test ./...` |
| `make docker-build` | Build `linux/amd64` image |
| `make helm-lint` | `helm lint` chart |

Raw equivalents:

```bash
cd mergerfs-csi
go build -o ../bin/mergerfs-csi ./cmd/mergerfs-csi
go test ./...
```

## Testing

| Layer | What | Notes |
|-------|------|--------|
| Unit | Branch JSON parse, path resolve, option sanitize | No FUSE |
| Unit | Mount command builder | Mock exec/runner interface |
| Unit | CreateVolume param → VolumeContext | Controller mode |
| Fake FS | Tempdir branches + mock runner | No real mount |
| Linux mount | Real `mergerfs` | Build tags; **skip on Darwin** |

`go test ./...` must pass on Darwin: gate Linux-only mount tests with build tags (`//go:build linux`).

Manual cluster check:

1. `helm install` chart into `kube-system` (or test ns with privileged DS).
2. Optional: `--set controller.enabled=true --set storageClass.create=true`.
3. Apply example pod with `pvcRef` / `emptyDirRef` + mergerfs volume.
4. Verify files appear in union; rename within pool works; unpublish cleans mount/pidfile.

## Local run (node plugin)

CLI flags (see `cmd/mergerfs-csi/main.go`):

- `--endpoint` (CSI unix socket; default under kubelet plugins path)
- `--mode` (`node` default | `controller` | `all`)
- `--node-id` (or env `NODE_ID` / hostname)
- `--kubelet-pods-path` (default `/var/lib/kubelet/pods`)
- `--state-dir` (default `/var/lib/mergerfs-csi`)
- `--mergerfs-path` (path to mergerfs binary)

Driver name hardcoded as `mergerfs.csi.k8s.io`. Helm sets flags on DaemonSet/Deployment; Dockerfile `CMD` only defaults `--mergerfs-path`.

Needs access to kubelet pods dir and `/dev/fuse` for real publish (node mode).

## Conventions

- Small packages; interfaces for exec/mount
- Conventional commits
- Controller stays minimal (param echo only) unless SPEC expands
