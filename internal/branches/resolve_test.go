package branches

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/BlackDark/csi-driver-mergerfs/internal/poolspec"
)

const kd = "/var/lib/kubelet"

func pod() *corev1.Pod {
	mounts := []corev1.VolumeMount{{Name: "remote"}, {Name: "scratch"}, {Name: "inline"}, {Name: "local"}, {Name: "eph"}, {Name: "ram"}}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app", UID: "u1"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "c", VolumeMounts: mounts}},
			Volumes: []corev1.Volume{
				{Name: "remote", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "media-rclone"}}},
				{Name: "local", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "local"}}},
				{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				{Name: "ram", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory}}},
				{Name: "inline", VolumeSource: corev1.VolumeSource{CSI: &corev1.CSIVolumeSource{Driver: "x"}}},
				{Name: "eph", VolumeSource: corev1.VolumeSource{Ephemeral: &corev1.EphemeralVolumeSource{}}},
				{Name: "unused", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			},
		},
	}
}

func pvs(claim string) (*corev1.PersistentVolume, error) {
	switch claim {
	case "media-rclone", "app-eph":
		return &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-" + claim},
			Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{}}}}, nil
	case "local":
		return &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-local"},
			Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{HostPath: &corev1.HostPathVolumeSource{}}}}, nil
	}
	return nil, errors.New("not bound")
}

func TestResolve(t *testing.T) {
	bs := []poolspec.Branch{
		{Type: poolspec.TypeHostPath, Path: "/srv/media/local"},
		{Type: poolspec.TypePVCRef, VolumeName: "remote"},
		{Type: poolspec.TypeEmptyDirRef, VolumeName: "scratch"},
		{Type: poolspec.TypeEmptyDirRef, VolumeName: "ram"},
		{Type: poolspec.TypeCSIRef, VolumeName: "inline"},
		{Type: poolspec.TypePVCRef, VolumeName: "eph"},
	}
	got, err := Resolve(Input{KubeletDir: kd, Pod: pod(), Branches: bs, Allowed: []string{"/srv/media"}, PV: pvs})
	if err != nil {
		t.Fatal(err)
	}
	v := kd + "/pods/u1/volumes/"
	want := []Resolved{
		{Path: "/srv/media/local", HostRoot: "/srv/media"},
		{Path: v + "kubernetes.io~csi/pv-media-rclone/mount", Mounted: true},
		{Path: v + "kubernetes.io~empty-dir/scratch"},
		{Path: v + "kubernetes.io~empty-dir/ram", Mounted: true},
		{Path: v + "kubernetes.io~csi/inline/mount", Mounted: true},
		{Path: v + "kubernetes.io~csi/pv-app-eph/mount", Mounted: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
	if r := HostRoots(got); !reflect.DeepEqual(r, []string{"/srv/media"}) {
		t.Errorf("roots %v", r)
	}
}

func TestResolveErrors(t *testing.T) {
	cases := map[string]struct {
		b       poolspec.Branch
		invalid bool
		want    string
	}{
		"missing":    {poolspec.Branch{Type: poolspec.TypePVCRef, VolumeName: "nope"}, true, `no volume "nope"`},
		"unused":     {poolspec.Branch{Type: poolspec.TypeEmptyDirRef, VolumeName: "unused"}, true, "volumeMounts"},
		"wrong kind": {poolspec.Branch{Type: poolspec.TypeCSIRef, VolumeName: "scratch"}, true, "not an inline CSI"},
		"not pvc":    {poolspec.Branch{Type: poolspec.TypePVCRef, VolumeName: "scratch"}, true, "not a persistentVolumeClaim"},
		"local pv":   {poolspec.Branch{Type: poolspec.TypePVCRef, VolumeName: "local"}, true, "not a CSI volume"},
	}
	for name, c := range cases {
		_, err := Resolve(Input{KubeletDir: kd, Pod: pod(), Branches: []poolspec.Branch{c.b}, PV: pvs})
		if errors.Is(err, poolspec.ErrInvalid) != c.invalid || err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	p := pod()
	p.Spec.Volumes[0].PersistentVolumeClaim.ClaimName = "unbound"
	_, err := Resolve(Input{KubeletDir: kd, Pod: p, Branches: []poolspec.Branch{{Type: poolspec.TypePVCRef, VolumeName: "remote"}}, PV: pvs})
	if !errors.Is(err, ErrNotReady) {
		t.Errorf("unbound: got %v", err)
	}
	_, err = Resolve(Input{KubeletDir: kd, Branches: []poolspec.Branch{{Type: poolspec.TypePVCRef, VolumeName: "remote"}}})
	if !errors.Is(err, poolspec.ErrInvalid) {
		t.Errorf("no pod: got %v", err)
	}
}
