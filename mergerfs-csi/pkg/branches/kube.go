package branches

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// VolumeDirNamer maps a pod volume name to the unique directory name under
// kubernetes.io~csi/ on the kubelet host path.
type VolumeDirNamer interface {
	CSIVolumeDirName(ctx context.Context, podNamespace, podName, podVolumeName string) (string, error)
}

// KubeVolumeDir resolves CSI mount dir names via the Kubernetes API.
type KubeVolumeDir struct {
	Client kubernetes.Interface
}

// CSIVolumeDirName returns the unique kubernetes.io~csi/<name>/ directory for a
// pod volume: PVC → bound PV name; inline CSI ephemeral → pod volume name.
func (k *KubeVolumeDir) CSIVolumeDirName(ctx context.Context, podNamespace, podName, podVolumeName string) (string, error) {
	if k == nil || k.Client == nil {
		return "", fmt.Errorf("kube client required")
	}
	if err := sanitizePathElement(podVolumeName, "volumeName"); err != nil {
		return "", err
	}
	pod, err := k.Client.CoreV1().Pods(podNamespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get pod %s/%s: %w", podNamespace, podName, err)
	}
	for i := range pod.Spec.Volumes {
		v := &pod.Spec.Volumes[i]
		if v.Name != podVolumeName {
			continue
		}
		if v.PersistentVolumeClaim != nil {
			claim := v.PersistentVolumeClaim.ClaimName
			pvc, err := k.Client.CoreV1().PersistentVolumeClaims(podNamespace).Get(ctx, claim, metav1.GetOptions{})
			if err != nil {
				return "", fmt.Errorf("get pvc %s/%s: %w", podNamespace, claim, err)
			}
			pvName := pvc.Spec.VolumeName
			if pvName == "" {
				return "", fmt.Errorf("pvc %s/%s not bound", podNamespace, claim)
			}
			if err := sanitizePathElement(pvName, "pvName"); err != nil {
				return "", err
			}
			return pvName, nil
		}
		if v.CSI != nil {
			// Ephemeral inline CSI uses the pod volume name as the kubelet dir.
			return podVolumeName, nil
		}
		return "", fmt.Errorf("pod volume %q is not PVC or CSI (type not supported for csi dir resolve)", podVolumeName)
	}
	return "", fmt.Errorf("pod %s/%s has no volume named %q", podNamespace, podName, podVolumeName)
}
