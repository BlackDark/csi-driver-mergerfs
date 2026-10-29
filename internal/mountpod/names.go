// Package mountpod builds Mount Pod manifests and derives their names and node paths
// (docs/design/mount-pod-contract.md). It is pure: no I/O.
package mountpod

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"

	"github.com/BlackDark/csi-driver-mergerfs/internal/driver"
)

// Labels and annotations on Mount Pods.
const (
	LabelName      = "app.kubernetes.io/name"
	LabelComponent = "app.kubernetes.io/component"
	LabelNode      = driver.Name + "/node"
	LabelID        = driver.Name + "/id"

	AnnotationVolumeID = driver.Name + "/volume-id"
	AnnotationTarget   = driver.Name + "/target"
	// AnnotationWorkload is the consumer pod as JSON {"ns","name","uid"}, so a
	// restarted node plugin can still send it Events and heal it.
	AnnotationWorkload = driver.Name + "/workload"

	AppName   = "csi-driver-mergerfs"
	Component = "pool"
)

// In-pod paths.
const (
	RunPath    = "/run/mergerfs-pool"
	SocketPath = RunPath + "/handoff.sock"
	// MountPoint is what mergerfs is told to mount on. The shim mounts nothing there:
	// the node plugin mounts the pool on the host's fuse/ dir.
	MountPoint = RunPath + "/fuse"
	// SocketEnv is read by the fusermount shim (cmd/mergerfs-fusermount).
	SocketEnv = "MERGERFS_CSI_HANDOFF"
)

const idLen = 16

// ID is the pool id of one (volume, target): every publish gets its own pool,
// because branches are the consumer pod's volumes.
func ID(volumeID, target string) string {
	sum := sha256.Sum256([]byte(volumeID + "\x00" + target))
	return hex.EncodeToString(sum[:])[:idLen]
}

// Name is the Mount Pod name for id.
func Name(id string) string { return "mergerfs-pool-" + id }

// PoolDir is the per-pool host dir holding fuse/ and handoff.sock.
func PoolDir(kubeletDir, id string) string {
	return filepath.Join(kubeletDir, "plugins", driver.Name, "pools", id)
}

// FuseDir is the pool's FUSE mountpoint inside PoolDir.
func FuseDir(kubeletDir, id string) string { return filepath.Join(PoolDir(kubeletDir, id), "fuse") }

// SockPath is the handoff socket inside PoolDir.
func SockPath(kubeletDir, id string) string {
	return filepath.Join(PoolDir(kubeletDir, id), "handoff.sock")
}

// Labels of a Mount Pod.
func Labels(nodeName, id string) map[string]string {
	return map[string]string{LabelName: AppName, LabelComponent: Component, LabelNode: nodeName, LabelID: id}
}
