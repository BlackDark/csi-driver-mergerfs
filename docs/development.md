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
├── charts/mergerfs-csi/     # Helm (DaemonSet, CSIDriver)
├── examples/                # static PV / sample pods
└── mergerfs-csi/            # Go module
    ├── go.mod               # github.com/local-only/mergerfs-csi
    ├── cmd/mergerfs-csi/
    └── pkg/
        ├── branches/        # JSON parse, path resolve
        ├── driver/          # CSI Identity + Node
        └── mergerfs/        # mount command, process supervise
```

Paths may land as code lands; treat SPEC as source of truth for behavior.

## Prerequisites

- Go (module version per `go.mod`)
- Docker / buildx for images (Linux/`linux/amd64` or `linux/arm64`)
- Helm 3, kubectl, Linux cluster with FUSE for e2e
- `mergerfs` in image or on node for real mounts

## Make targets

From `mergerfs-csi/` (or repo root if Makefile is hoisted):

| Target | Purpose |
|--------|---------|
| `make build` | Compile `bin/mergerfs-csi` |
| `make test` | `go test ./...` |
| `make image` | Build node plugin image |
| `make lint` | golangci-lint (if configured) |

Raw equivalents:

```bash
cd mergerfs-csi
go build -o bin/mergerfs-csi ./cmd/mergerfs-csi
go test ./...
```

## Testing

| Layer | What | Notes |
|-------|------|--------|
| Unit | Branch JSON parse, path resolve, option sanitize | No FUSE |
| Unit | Mount command builder | Mock exec/runner interface |
| Fake FS | Tempdir branches + mock runner | No real mount |
| Linux mount | Real `mergerfs` | Build tags; **skip on Darwin** |

`go test ./...` must pass on Darwin: gate Linux-only mount tests with build tags (`//go:build linux`).

Manual cluster check:

1. `helm install` chart into `kube-system` (or test ns with privileged DS).
2. Apply example pod with `pvcRef` / `emptyDirRef` + mergerfs volume.
3. Verify files appear in union; rename within pool works; unpublish cleans mount/pidfile.

## Local run (node plugin)

Typical flags (exact names follow binary `--help`):

- CSI endpoint (unix socket for kubelet)
- `--kubelet-pods-path` (default `/var/lib/kubelet/pods`)
- Node ID / `NODE_ID`

Needs access to kubelet pods dir and `/dev/fuse` for real publish.

## Conventions

- Small packages; interfaces for exec/mount
- Conventional commits
- Do not expand into controller/provisioner without SPEC change
