# Changelog

## [0.2.0](https://github.com/BlackDark/csi-driver-mergerfs/compare/v0.1.1...v0.2.0) (2026-10-29)


### ⚠ BREAKING CHANGES

* Go module at repo root; chart renamed to csi-driver-mergerfs; hostPath branches need allowedHostPaths; branch field driverHint removed; mergerfs runs as mountPod.uid instead of root.

### Features

* rewrite around unprivileged Mount Pods and a fusermount shim ([#21](https://github.com/BlackDark/csi-driver-mergerfs/issues/21)) ([0912479](https://github.com/BlackDark/csi-driver-mergerfs/commit/0912479edd7fa67eb02012ee8e4b73d87c4c03c3))
