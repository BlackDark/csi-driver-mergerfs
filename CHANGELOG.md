# Changelog

## [0.3.0](https://github.com/BlackDark/csi-driver-mergerfs/compare/v0.2.0...v0.3.0) (2026-10-29)


### ⚠ BREAKING CHANGES

* k8s.io names belong to the Kubernetes project. PVs, StorageClasses and inline volumes must use driver mergerfs.csi.blackdark.github.io; see README Upgrading.

### Features

* rename the driver to mergerfs.csi.blackdark.github.io ([dfc25f8](https://github.com/BlackDark/csi-driver-mergerfs/commit/dfc25f817fd0cdefbd00e9626c55f81a14f0326d))


### Documentation

* migration steps for the driver rename ([b1b8268](https://github.com/BlackDark/csi-driver-mergerfs/commit/b1b8268fb7f02d70b61c36f83552d9b6c418fe0d))

## [0.2.0](https://github.com/BlackDark/csi-driver-mergerfs/compare/v0.1.1...v0.2.0) (2026-10-29)


### ⚠ BREAKING CHANGES

* Go module at repo root; chart renamed to csi-driver-mergerfs; hostPath branches need allowedHostPaths; branch field driverHint removed; mergerfs runs as mountPod.uid instead of root.

### Features

* rewrite around unprivileged Mount Pods and a fusermount shim ([#21](https://github.com/BlackDark/csi-driver-mergerfs/issues/21)) ([0912479](https://github.com/BlackDark/csi-driver-mergerfs/commit/0912479edd7fa67eb02012ee8e4b73d87c4c03c3))
