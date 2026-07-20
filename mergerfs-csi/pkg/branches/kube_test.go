package branches

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestKubeVolumeDir_PVCToPV(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "ns"},
			Spec: corev1.PodSpec{
				Volumes: []corev1.Volume{
					{
						Name: "remote",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: "data",
							},
						},
					},
				},
			},
		},
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "ns"},
			Spec: corev1.PersistentVolumeClaimSpec{
				VolumeName: "pvc-edd9e34a-5dfd-48c9-9d60-00ff9c9853b2",
			},
		},
	)
	k := &KubeVolumeDir{Client: client}
	got, err := k.CSIVolumeDirName(t.Context(), "ns", "app", "remote")
	if err != nil {
		t.Fatal(err)
	}
	want := "pvc-edd9e34a-5dfd-48c9-9d60-00ff9c9853b2"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestKubeVolumeDir_CSIEphemeral(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "ns"},
			Spec: corev1.PodSpec{
				Volumes: []corev1.Volume{
					{
						Name: "union",
						VolumeSource: corev1.VolumeSource{
							CSI: &corev1.CSIVolumeSource{Driver: "mergerfs.csi.k8s.io"},
						},
					},
				},
			},
		},
	)
	k := &KubeVolumeDir{Client: client}
	got, err := k.CSIVolumeDirName(t.Context(), "ns", "app", "union")
	if err != nil {
		t.Fatal(err)
	}
	if got != "union" {
		t.Fatalf("got %q want union", got)
	}
}

func TestKubeVolumeDir_UnboundPVC(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "ns"},
			Spec: corev1.PodSpec{
				Volumes: []corev1.Volume{
					{
						Name: "remote",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: "data",
							},
						},
					},
				},
			},
		},
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "ns"},
			Spec:       corev1.PersistentVolumeClaimSpec{},
		},
	)
	k := &KubeVolumeDir{Client: client}
	if _, err := k.CSIVolumeDirName(t.Context(), "ns", "app", "remote"); err == nil {
		t.Fatal("expected unbound PVC error")
	}
}

func TestKubeVolumeDir_MissingVolume(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "ns"},
			Spec:       corev1.PodSpec{},
		},
	)
	k := &KubeVolumeDir{Client: client}
	if _, err := k.CSIVolumeDirName(t.Context(), "ns", "app", "nope"); err == nil {
		t.Fatal("expected missing volume error")
	}
}

func TestResolvePVCRefViaKube(t *testing.T) {
	pvName := "pvc-edd9e34a-5dfd-48c9-9d60-00ff9c9853b2"
	client := fake.NewSimpleClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "ns"},
			Spec: corev1.PodSpec{
				Volumes: []corev1.Volume{
					{
						Name: "remote",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: "data",
							},
						},
					},
				},
			},
		},
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "ns"},
			Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: pvName},
		},
	)
	kubelet := t.TempDir()
	uid := "pod-uid"
	r := Resolver{
		KubeletPodsPath: kubelet,
		PodNamespace:    "ns",
		PodName:         "app",
		DirNamer:        &KubeVolumeDir{Client: client},
	}
	got, err := r.Resolve(t.Context(), Branch{Type: TypePVCRef, VolumeName: "remote"}, uid)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(kubelet, uid, "volumes", "kubernetes.io~csi", pvName, "mount")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

type failingDirNamer struct{}

func (failingDirNamer) CSIVolumeDirName(context.Context, string, string, string) (string, error) {
	return "", errors.New("should not call kube")
}

func TestResolveCSIRefFastPathSkipsKube(t *testing.T) {
	kubelet := t.TempDir()
	uid := "pod-uid"
	volPath := filepath.Join(kubelet, uid, "volumes", "kubernetes.io~csi", "union", "mount")
	if err := os.MkdirAll(volPath, 0o755); err != nil {
		t.Fatal(err)
	}
	r := Resolver{
		KubeletPodsPath: kubelet,
		PodNamespace:    "ns",
		PodName:         "app",
		DirNamer:        failingDirNamer{},
	}
	got, err := r.Resolve(t.Context(), Branch{Type: TypeCSIRef, VolumeName: "union"}, uid)
	if err != nil {
		t.Fatal(err)
	}
	if got != volPath {
		t.Fatalf("got %q want %q", got, volPath)
	}
}
