# csi-driver-mergerfs

Kubernetes CSI driver for mergerfs pools over a pod's volumes and allowed host paths, for cluster operators. Pre-alpha. Baseline: Linux, Kubernetes ≥1.34 (external-provisioner v6 needs `storage/v1` VolumeAttributesClass; chart `kubeVersion`). Sibling of [csi-driver-rclone](https://github.com/BlackDark/csi-driver-rclone): same patterns, keep them aligned.

## Verify

`mise run check` green before every commit. Workflow changes: `mise exec -- zizmor .github/workflows` and `mise exec -- actionlint` clean. FUSE changes (`internal/fusemount`, `internal/handoff`, `cmd/mergerfs-fusermount`): `mise run test:fuse` (Docker) or `test:fuse:host` (Linux + sudo).

## Stack

Follows https://github.com/BlackDark/agent-presets/blob/main/stack/core.md.
Deviations:
- Go, not TypeScript: CSI drivers, client-go and mount-utils are Go.
- Helm is the only install method (OCI chart on GHCR); no kustomize or raw manifests.
- Images: distroless static, non-root, multi-stage; amd64 + arm64 from one job, Go cross-compiled on the build platform (no QEMU). mergerfs is the upstream static release, pinned by version only in the `Dockerfile` (no sums), so Renovate bumps it on its own.
- golangci-lint in `mise run check` beside gofmt and go vet.
- CI: `check` gates `mise run check` (includes csi-sanity, `test/sanity`), zizmor + actionlint, `test:fuse:host`, and e2e when it runs. e2e is opt-in: weekly schedule on main, manual `workflow_dispatch` (input `e2e`, any branch) and every release. Go caches restore everywhere except release runs and save only on main. Docs-only PRs (`docs/**`, `*.md`) skip `test:fuse`. Release = merge the release-please PR (`release-please.yml`, Conventional Commits → version, `CHANGELOG.md`, Chart.yaml); it tags, creates the GitHub release and calls `release.yml` with the tag, which calls `ci.yml` with `e2e: true`; `hack/release/check-main-ci.sh` requires the tag commit on main with a passing CI `check` there, and `hack/release/check-version.sh` ties the tag to Chart.yaml. No manual tags.

## Map

- `cmd/csi-driver-mergerfs`: the driver binary; modes `node`, `controller`. `cmd/mergerfs-fusermount`: the fusermount shim mergerfs runs in the Mount Pod (ADR 0001).
- `internal/poolspec` (attributes, option allowlist), `internal/branches` (branch → host path), `internal/mountpod` (Mount Pod manifest), `internal/handoff` + `internal/fusemount` (fd handoff, plugin-side mount), `internal/node` (CSI node service, adoption, loss, healing).
- `charts/csi-driver-mergerfs`: the Helm chart. `hack/e2e` + `test/e2e`: kind e2e (`mise run e2e`); the binaries build on the host (`Dockerfile` `BIN=local`). e2e tests that restart the node plugin or kill a Mount Pod stay serial; the rest `t.Parallel()`, with their own pods and host dirs.
- Domain terms: `GLOSSARY.md`. Volume keys: `docs/reference/volume-parameters.md`. Decisions: `docs/adr/`. Scope: `docs/scope.md`.
- `refs/`: gitignored reference checkouts (mergerfs source); read only, never edit or import.

## Every change

Same PR, only what the change touches:
- tests for the changed behaviour (unit, csi-sanity, fuse, e2e)
- docs in `docs/` that describe it; new terms into `GLOSSARY.md`
- README when a user-visible feature changes

## Code style

- Data in, data out: pure functions, side effects (I/O, time, randomness) at the edges and passed in.
- Expected failures are Go `error` values; panic for bugs only.
- Composition over inheritance.
- Small units: split a file past ~400 lines, a function past ~60.
- Names explain what, comments explain why.
- Refactors keep behaviour and land in their own commit.

## Stewardship

Leave every touched area cleaner than you found it.
- Small fixes beside the change (dead code, stale comment or doc, missing test, unclear name): own commit, same PR.
- Whatever your change makes obsolete (code, flags, config, docs, deps) goes in the same PR.
- Larger problems: report to the user with `file:line` and a proposal; the PR stays on topic.

## Git

- Conventional Commits. One topic per PR.
- Merge: rebase when the commits are logically separate, squash otherwise.
- PR already merged → branch anew from `origin/main`, open a new PR.
- Force-push: `--force-with-lease` on own branches only.

## Boundaries

- `sudo`, system packages, global installs: the user runs them; ask with what and why.
- Running user processes stay up; test on other ports.
- Language: code, comments, docs, commits in English.

## Gotchas

- mise task scripts are Tera templates: wrap `{{ }}` (e.g. `go list -f`) in `{% raw %}`.
- `gofmt -l .` would walk `refs/`; the check formats only `go list ./...` package dirs. `rg` over the repo root walks `refs/` too: scope searches to `cmd internal test charts docs`.
- mergerfs only calls the shim with `-o auto_unmount`; without it, it tries `/dev/fuse` itself and fails in the Mount Pod.
- kubelet's `pods/<uid>` and `kubernetes.io~csi/<name>` dirs are root `0750`: mount branch volumes into the Mount Pod one by one, never via a parent dir.
- Release PRs get no CI by themselves: no `RELEASE_PLEASE_TOKEN` secret is set (by choice, for now), and PRs opened with `GITHUB_TOKEN` trigger no workflows. Before merging one, run CI by hand: `gh workflow run ci.yml --ref <release-please branch> -f e2e=true`, or close and reopen the PR. Adding the secret (fine-grained PAT, Contents + Pull requests read/write, or a GitHub App token) removes this step.
