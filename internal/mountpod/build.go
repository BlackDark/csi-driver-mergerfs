package mountpod

import (
	"encoding/json"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/BlackDark/csi-driver-mergerfs/internal/branches"
	"github.com/BlackDark/csi-driver-mergerfs/internal/poolspec"
)

// MergerfsPath is the static mergerfs binary in the image. Its libfuse tries
// /usr/local/bin/mergerfs-fusermount first, which is where the shim is installed.
const MergerfsPath = "/usr/local/bin/mergerfs"

// Config is the node-wide part of every Mount Pod.
type Config struct {
	Namespace         string
	Image             string
	KubeletDir        string
	UID, GID          int64
	PriorityClassName string
	Tolerations       []corev1.Toleration
	Resources         corev1.ResourceRequirements
}

// Workload is the consumer pod of a pool.
type Workload struct {
	Namespace string `json:"ns"`
	Name      string `json:"name"`
	UID       string `json:"uid,omitempty"`
}

// Input is one Mount Pod to build.
type Input struct {
	Config   Config
	NodeName string
	ID       string
	VolumeID string
	Target   string
	Workload *Workload // nil without podInfoOnMount
	Spec     poolspec.Spec
	Branches []branches.Resolved
}

// Build returns the Mount Pod of one pool.
func Build(in Input) (corev1.Pod, error) {
	arg, err := poolspec.BranchArg(podPaths(in.Branches), in.Spec.Branches)
	if err != nil {
		return corev1.Pod{}, err
	}
	ann := map[string]string{AnnotationVolumeID: in.VolumeID, AnnotationTarget: in.Target}
	if in.Workload != nil {
		b, err := json.Marshal(in.Workload)
		if err != nil {
			return corev1.Pod{}, fmt.Errorf("mountpod: workload: %w", err)
		}
		ann[AnnotationWorkload] = string(b)
	}
	return corev1.Pod{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{
			Name: Name(in.ID), Namespace: in.Config.Namespace,
			Labels: Labels(in.NodeName, in.ID), Annotations: ann,
		},
		Spec: podSpec(in, arg),
	}, nil
}

// DecodeWorkload reads AnnotationWorkload; nil when missing or malformed.
func DecodeWorkload(pod *corev1.Pod) *Workload {
	var w Workload
	if json.Unmarshal([]byte(pod.Annotations[AnnotationWorkload]), &w) != nil || w.Name == "" {
		return nil
	}
	return &w
}

// podSpec pins the pod to its node. RestartPolicy Never: a restarted mergerfs would
// need a fresh FUSE connection the consumer's bind cannot follow (ADR 0001).
func podSpec(in Input, branchArg string) corev1.PodSpec {
	c := in.Config
	return corev1.PodSpec{
		NodeName:                      in.NodeName,
		RestartPolicy:                 corev1.RestartPolicyNever,
		PriorityClassName:             c.PriorityClassName,
		Tolerations:                   c.Tolerations,
		AutomountServiceAccountToken:  ptr.To(false),
		EnableServiceLinks:            ptr.To(false),
		TerminationGracePeriodSeconds: ptr.To(int64(10)),
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot:   ptr.To(true),
			RunAsUser:      ptr.To(c.UID),
			RunAsGroup:     ptr.To(c.GID),
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		Containers: []corev1.Container{{
			Name:    "mergerfs",
			Image:   c.Image,
			Command: []string{MergerfsPath},
			Args:    []string{"-f", "-o", in.Spec.MountOptions(), branchArg, MountPoint},
			Env: []corev1.EnvVar{
				{Name: SocketEnv, Value: SocketPath},
				{Name: "HOME", Value: RunPath},
			},
			Resources: c.Resources,
			SecurityContext: &corev1.SecurityContext{
				Privileged:               ptr.To(false),
				AllowPrivilegeEscalation: ptr.To(false),
				ReadOnlyRootFilesystem:   ptr.To(true),
				RunAsNonRoot:             ptr.To(true),
				Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			VolumeMounts: volumeMounts(in),
		}},
		Volumes: volumes(in),
	}
}

const (
	volRun    = "run"
	volBranch = "branch-"
	volHost   = "host-"
)

// BranchesPath holds the consumer-volume branches in the Mount Pod, one dir per index.
const BranchesPath = "/branches"

// podPaths are the branch paths mergerfs sees. Consumer volumes are mounted one by one
// below BranchesPath: kubelet's kubernetes.io~csi/<name> and pods/<uid> dirs are
// root 0750, so a non-root mergerfs could not reach them at their host paths.
// hostPath branches keep their host path below their mounted allowed root.
func podPaths(rs []branches.Resolved) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Path
		if r.HostRoot == "" {
			out[i] = BranchesPath + "/" + strconv.Itoa(i)
		}
	}
	return out
}

// volumeMounts use HostToContainer, so a branch volume kubelet remounts (or a mount
// below an allowed root) after the Mount Pod started still shows up (ADR 0002).
func volumeMounts(in Input) []corev1.VolumeMount {
	hc := ptr.To(corev1.MountPropagationHostToContainer)
	out := []corev1.VolumeMount{{Name: volRun, MountPath: RunPath, MountPropagation: ptr.To(corev1.MountPropagationNone)}}
	for i, r := range in.Branches {
		if r.HostRoot == "" {
			out = append(out, corev1.VolumeMount{Name: volBranch + strconv.Itoa(i), MountPath: BranchesPath + "/" + strconv.Itoa(i), MountPropagation: hc})
		}
	}
	for i, root := range branches.HostRoots(in.Branches) {
		out = append(out, corev1.VolumeMount{Name: volHost + strconv.Itoa(i), MountPath: root, MountPropagation: hc})
	}
	return out
}

func volumes(in Input) []corev1.Volume {
	dir := func(p string) corev1.VolumeSource {
		return corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: p, Type: ptr.To(corev1.HostPathDirectory)}}
	}
	out := []corev1.Volume{{Name: volRun, VolumeSource: dir(PoolDir(in.Config.KubeletDir, in.ID))}}
	for i, r := range in.Branches {
		if r.HostRoot == "" {
			out = append(out, corev1.Volume{Name: volBranch + strconv.Itoa(i), VolumeSource: dir(r.Path)})
		}
	}
	for i, root := range branches.HostRoots(in.Branches) {
		out = append(out, corev1.Volume{Name: volHost + strconv.Itoa(i), VolumeSource: dir(root)})
	}
	return out
}
