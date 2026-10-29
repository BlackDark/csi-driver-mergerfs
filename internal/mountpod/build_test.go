package mountpod

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/BlackDark/csi-driver-mergerfs/internal/branches"
	"github.com/BlackDark/csi-driver-mergerfs/internal/poolspec"
)

func input() Input {
	return Input{
		Config:   Config{Namespace: "drv", Image: "img", KubeletDir: "/var/lib/kubelet", UID: 1000, GID: 1000},
		NodeName: "n1", ID: "abc", VolumeID: "vol", Target: "/t",
		Workload: &Workload{Namespace: "media", Name: "jellyfin-0", UID: "u1"},
		Spec: poolspec.Spec{
			Branches: []poolspec.Branch{{Type: poolspec.TypeHostPath, Path: "/srv/indigo/media-local", Mode: "RW"},
				{Type: poolspec.TypePVCRef, VolumeName: "remote", Mode: "NC"}},
			Options: []string{"category.create=pfrd"},
		},
		Branches: []branches.Resolved{
			{Path: "/srv/indigo/media-local", HostRoot: "/srv/indigo"},
			{Path: "/var/lib/kubelet/pods/u1/volumes/kubernetes.io~csi/pv/mount", Mounted: true},
		},
	}
}

func TestBuild(t *testing.T) {
	pod, err := Build(input())
	if err != nil {
		t.Fatal(err)
	}
	if pod.Name != "mergerfs-pool-abc" || pod.Spec.NodeName != "n1" || pod.Labels[LabelID] != "abc" {
		t.Errorf("meta: %+v", pod.ObjectMeta)
	}
	c := pod.Spec.Containers[0]
	wantArgs := []string{"-f", "-o", "auto_unmount,category.create=pfrd",
		"/srv/indigo/media-local=RW:/branches/1=NC", MountPoint}
	if !reflect.DeepEqual(c.Args, wantArgs) || c.Command[0] != MergerfsPath {
		t.Errorf("args: %q", c.Args)
	}
	sc := c.SecurityContext
	if *sc.Privileged || !*sc.ReadOnlyRootFilesystem || *sc.AllowPrivilegeEscalation || sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("security context: %+v", sc)
	}
	mounts := map[string]corev1.VolumeMount{}
	for _, m := range c.VolumeMounts {
		mounts[m.MountPath] = m
	}
	if m := mounts["/branches/1"]; m.Name == "" || *m.MountPropagation != corev1.MountPropagationHostToContainer {
		t.Errorf("volume branch: %+v", m)
	}
	if m := mounts["/srv/indigo"]; m.Name == "" || *m.MountPropagation != corev1.MountPropagationHostToContainer {
		t.Errorf("host root: %+v", m)
	}
	vols := map[string]string{}
	for _, v := range pod.Spec.Volumes {
		vols[v.Name] = v.HostPath.Path
	}
	want := map[string]string{"run": "/var/lib/kubelet/plugins/mergerfs.csi.blackdark.github.io/pools/abc",
		"branch-1": "/var/lib/kubelet/pods/u1/volumes/kubernetes.io~csi/pv/mount", "host-0": "/srv/indigo"}
	if !reflect.DeepEqual(vols, want) {
		t.Errorf("volumes: %v", vols)
	}
	for _, v := range pod.Spec.Containers[0].Env {
		if v.Name == SocketEnv && v.Value != SocketPath {
			t.Errorf("socket env %q", v.Value)
		}
	}
	if w := DecodeWorkload(&pod); w == nil || *w != *input().Workload {
		t.Errorf("workload: %+v", w)
	}
}

func TestBuildHostOnly(t *testing.T) {
	in := input()
	in.Spec.Branches, in.Branches = in.Spec.Branches[:1], in.Branches[:1]
	pod, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(pod.Spec.Volumes) != 2 {
		t.Errorf("host-only pool should not mount pod volumes: %+v", pod.Spec.Volumes)
	}
}

func TestIDPerTarget(t *testing.T) {
	if ID("v", "/a") == ID("v", "/b") || len(ID("v", "/a")) != 16 {
		t.Error("ids")
	}
}
