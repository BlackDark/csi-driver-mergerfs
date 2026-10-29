// Package branches maps a Pool Spec's branches onto host paths, using the consumer
// pod's volumes (ADR 0002). Pure: the caller passes the pod and a PV lookup.
package branches

import (
	"errors"
	"fmt"
	"path/filepath"

	corev1 "k8s.io/api/core/v1"

	"github.com/BlackDark/csi-driver-mergerfs/internal/poolspec"
)

// ErrNotReady marks lookups worth retrying (unbound PVC, API errors).
var ErrNotReady = errors.New("branch not ready")

// Resolved is one branch on the host. The Mount Pod sees it at the same path.
type Resolved struct {
	Path string
	// Mounted: the path is a volume mount kubelet makes; publish waits for it to be a
	// mount point that answers statfs. Otherwise it only has to exist.
	Mounted bool
	// HostRoot is the allowed host path root of a hostPath branch, else "".
	HostRoot string
}

// PVLookup returns the PV bound to claim in the pod's namespace.
type PVLookup func(claim string) (*corev1.PersistentVolume, error)

// Input is what Resolve needs.
type Input struct {
	KubeletDir string
	Pod        *corev1.Pod // nil when the pool has only hostPath branches
	Branches   []poolspec.Branch
	Allowed    []string
	PV         PVLookup
}

func invalid(i int, format string, a ...any) error {
	return fmt.Errorf("%w: branches[%d]: %s", poolspec.ErrInvalid, i, fmt.Sprintf(format, a...))
}

// Resolve returns one Resolved per branch, in order.
func Resolve(in Input) ([]Resolved, error) {
	out := make([]Resolved, len(in.Branches))
	for i, b := range in.Branches {
		if b.Type == poolspec.TypeHostPath {
			out[i] = Resolved{Path: b.Path, HostRoot: poolspec.AllowedRoot(b.Path, in.Allowed)}
			continue
		}
		r, err := resolveRef(in, i, b)
		if err != nil {
			return nil, err
		}
		out[i] = r
	}
	return out, nil
}

// VolumesDir is where kubelet mounts the volumes of the pod with uid.
func VolumesDir(kubeletDir, uid string) string {
	return filepath.Join(kubeletDir, "pods", uid, "volumes")
}

func resolveRef(in Input, i int, b poolspec.Branch) (Resolved, error) {
	if in.Pod == nil {
		return Resolved{}, invalid(i, "%s needs the consumer pod: set podInfoOnMount on the CSIDriver", b.Type)
	}
	v := findVolume(in.Pod, b.VolumeName)
	if v == nil {
		return Resolved{}, invalid(i, "pod has no volume %q", b.VolumeName)
	}
	if !mountedByContainer(in.Pod, b.VolumeName) {
		return Resolved{}, invalid(i, "volume %q is not in any container's volumeMounts; kubelet only mounts volumes a container uses", b.VolumeName)
	}
	vols := VolumesDir(in.KubeletDir, string(in.Pod.UID))
	csiDir := func(name string) string { return filepath.Join(vols, "kubernetes.io~csi", name, "mount") }
	switch b.Type {
	case poolspec.TypeEmptyDirRef:
		if v.EmptyDir == nil {
			return Resolved{}, invalid(i, "volume %q is not an emptyDir", b.VolumeName)
		}
		return Resolved{Path: filepath.Join(vols, "kubernetes.io~empty-dir", b.VolumeName),
			Mounted: v.EmptyDir.Medium == corev1.StorageMediumMemory}, nil
	case poolspec.TypeCSIRef:
		if v.CSI == nil {
			return Resolved{}, invalid(i, "volume %q is not an inline CSI volume", b.VolumeName)
		}
		return Resolved{Path: csiDir(b.VolumeName), Mounted: true}, nil
	}
	claim := claimName(in.Pod, v)
	if claim == "" {
		return Resolved{}, invalid(i, "volume %q is not a persistentVolumeClaim or ephemeral volume", b.VolumeName)
	}
	pv, err := in.PV(claim)
	if err != nil {
		return Resolved{}, fmt.Errorf("%w: branches[%d]: claim %s: %w", ErrNotReady, i, claim, err)
	}
	if pv.Spec.CSI == nil {
		return Resolved{}, invalid(i, "claim %s is bound to PV %s, which is not a CSI volume; use a hostPath branch for local PVs", claim, pv.Name)
	}
	return Resolved{Path: csiDir(pv.Name), Mounted: true}, nil
}

func findVolume(pod *corev1.Pod, name string) *corev1.Volume {
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Name == name {
			return &pod.Spec.Volumes[i]
		}
	}
	return nil
}

// claimName is the PVC behind v; generic ephemeral volumes get <pod>-<volume>.
func claimName(pod *corev1.Pod, v *corev1.Volume) string {
	switch {
	case v.PersistentVolumeClaim != nil:
		return v.PersistentVolumeClaim.ClaimName
	case v.Ephemeral != nil:
		return pod.Name + "-" + v.Name
	}
	return ""
}

func mountedByContainer(pod *corev1.Pod, name string) bool {
	for _, cs := range [][]corev1.Container{pod.Spec.InitContainers, pod.Spec.Containers} {
		for _, c := range cs {
			for _, m := range c.VolumeMounts {
				if m.Name == name {
					return true
				}
			}
		}
	}
	return false
}

// HostRoots lists the distinct allowed roots the branches use, in order.
func HostRoots(rs []Resolved) []string {
	var out []string
	seen := map[string]bool{}
	for _, r := range rs {
		if r.HostRoot != "" && !seen[r.HostRoot] {
			seen[r.HostRoot] = true
			out = append(out, r.HostRoot)
		}
	}
	return out
}
