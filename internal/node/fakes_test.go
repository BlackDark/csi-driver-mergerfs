package node

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/mount-utils"

	"github.com/BlackDark/csi-driver-mergerfs/internal/fusemount"
	"github.com/BlackDark/csi-driver-mergerfs/internal/handoff"
	"github.com/BlackDark/csi-driver-mergerfs/internal/mountpod"
	"github.com/BlackDark/csi-driver-mergerfs/internal/poolspec"
)

type fakeFuse struct {
	mu        sync.Mutex
	mounted   map[string]fusemount.Opts
	unmounted []string
}

func (f *fakeFuse) Mount(target string, o fusemount.Opts) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mounted[target] = o
	return -1, nil
}

func (f *fakeFuse) Unmount(target string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.mounted[target]; !ok {
		return syscall.EINVAL
	}
	delete(f.mounted, target)
	f.unmounted = append(f.unmounted, target)
	return nil
}

func (f *fakeFuse) opts(target string) (fusemount.Opts, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.mounted[target]
	return o, ok
}

// fakeHandoff plays the shim: on a fresh server it asks for a mount at once, unless
// hang is set (a Mount Pod that never starts mergerfs).
type fakeHandoff struct {
	mu      sync.Mutex
	hang    bool
	serves  int
	adopted int
}

func (h *fakeHandoff) Serve(ctx context.Context, cfg handoff.ServerConfig, ev chan<- handoff.Event) error {
	h.mu.Lock()
	h.serves++
	if cfg.Mounted {
		h.adopted++
	}
	hang := h.hang
	h.mu.Unlock()
	if !cfg.Mounted && !hang {
		if _, err := cfg.Mount("rw,nosuid,nodev,default_permissions,auto_unmount,fsname=pool,subtype=mergerfs"); err != nil {
			ev <- handoff.Event{Kind: handoff.EventRejected, Err: err}
		} else {
			ev <- handoff.Event{Kind: handoff.EventMounted}
		}
	}
	<-ctx.Done()
	return nil
}

type harness struct {
	t         *testing.T
	srv       *Server
	cs        *fake.Clientset
	mounter   *mount.FakeMounter
	fuse      *fakeFuse
	ho        *fakeHandoff
	kd        string
	mu        sync.Mutex
	infos     []mount.MountInfo
	statErr   map[string]error
	evictions []*policyv1.Eviction
}

func newHarness(t *testing.T, mutate ...func(*Config)) *harness {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir()) // darwin: /var -> /private/var
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{
		t: t, cs: fake.NewClientset(), mounter: mount.NewFakeMounter(nil),
		fuse: &fakeFuse{mounted: map[string]fusemount.Opts{}}, ho: &fakeHandoff{},
		kd: dir, statErr: map[string]error{},
	}
	h.cs.PrependReactor("create", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		p, ok := a.(k8stesting.CreateAction).GetObject().(*corev1.Pod)
		if !ok {
			return false, nil, nil
		}
		p.UID = types.UID("uid-" + p.Name)
		return false, nil, nil
	})
	var evN int
	h.cs.PrependReactor("create", "events", func(a k8stesting.Action) (bool, runtime.Object, error) {
		e := a.(k8stesting.CreateAction).GetObject().(*corev1.Event)
		evN++ // the fake does not apply GenerateName
		e.Name = fmt.Sprintf("%s%d", e.GenerateName, evN)
		return false, nil, nil
	})
	h.cs.PrependReactor("create", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.GetSubresource() != "eviction" {
			return false, nil, nil
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		h.evictions = append(h.evictions, a.(k8stesting.CreateAction).GetObject().(*policyv1.Eviction))
		return true, nil, nil
	})
	cfg := Config{
		NodeID:           "node-a",
		Pod:              mountpod.Config{Namespace: "drv", Image: "img", KubeletDir: dir, UID: 1000, GID: 1000},
		PublishTimeout:   2 * time.Second,
		AllowedHostPaths: []string{filepath.Join(dir, "srv")},
	}
	for _, m := range mutate {
		m(&cfg)
	}
	h.srv = New(cfg, Deps{
		Kube: NewKube(h.cs, "drv"), Mounter: h.mounter, Fuse: h.fuse, Handoff: h.ho,
		PrepareDir: func(p string, _, _ int) error { return os.MkdirAll(p, 0o750) },
		MountInfo: func() ([]mount.MountInfo, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return append([]mount.MountInfo(nil), h.infos...), nil
		},
		Statfs: func(p string) (Usage, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			if err := h.statErr[p]; err != nil {
				return Usage{}, err
			}
			return Usage{TotalBytes: 100, AvailableBytes: 60, UsedBytes: 40}, nil
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(h.srv.Wait)
	return h
}

func (h *harness) addMount(path string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.infos = append(h.infos, mount.MountInfo{MountPoint: path})
}

func (h *harness) setStatErr(path string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.statErr[path] = err
}

// consumer creates the publishing pod with an rclone-like PVC branch and an emptyDir.
func (h *harness) consumer() {
	h.t.Helper()
	yes := true
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "jellyfin-0", UID: "u1",
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: "jellyfin", UID: "ss", Controller: &yes}}},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app", VolumeMounts: []corev1.VolumeMount{{Name: "remote"}, {Name: "scratch"}}}},
			Volumes: []corev1.Volume{
				{Name: "remote", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "media-rclone"}}},
				{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			},
		},
	}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "media-rclone"},
		Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "pv-rclone"}}
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-rclone"},
		Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: "rclone"}}}}
	for _, o := range []runtime.Object{pod, pvc, pv} {
		if err := h.cs.Tracker().Add(o); err != nil {
			h.t.Fatal(err)
		}
	}
}

func (h *harness) volumesDir() string { return filepath.Join(h.kd, "pods", "u1", "volumes") }

// target is the CSI target path of the pool in the consumer pod.
func (h *harness) target() string {
	return filepath.Join(h.volumesDir(), "kubernetes.io~csi", "pool", "mount")
}

func (h *harness) remoteBranch() string {
	return filepath.Join(h.volumesDir(), "kubernetes.io~csi", "pv-rclone", "mount")
}

func (h *harness) localBranch() string { return filepath.Join(h.kd, "srv", "media-local") }

// mediaLayout: local hostPath RW + rclone PVC NC, like the README example.
func (h *harness) mediaLayout() map[string]string {
	return map[string]string{
		poolspec.KeyBranches: `[{"type":"hostPath","path":"` + h.localBranch() + `"},{"type":"pvcRef","volumeName":"remote","mode":"NC"}]`,
		poolspec.KeyOptions:  "category.create=pfrd,func.getattr=newest,cache.files=off,dropcacheonclose=false",
		keyPodName:           "jellyfin-0", keyPodNamespace: "media", keyPodUID: "u1",
	}
}

func publishReq(target string, vc map[string]string) *csi.NodePublishVolumeRequest {
	return &csi.NodePublishVolumeRequest{
		VolumeId:   "vol-1",
		TargetPath: target,
		VolumeCapability: &csi.VolumeCapability{
			AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
		},
		VolumeContext: vc,
	}
}

// readyBranches makes the media layout's branches usable.
func (h *harness) readyBranches() {
	h.t.Helper()
	for _, d := range []string{h.localBranch(), h.remoteBranch()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			h.t.Fatal(err)
		}
	}
	h.addMount(h.remoteBranch())
}

func (h *harness) mountPods() []corev1.Pod {
	h.t.Helper()
	l, err := h.cs.CoreV1().Pods("drv").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		h.t.Fatal(err)
	}
	return l.Items
}

func (h *harness) events() []corev1.Event {
	h.t.Helper()
	var out []corev1.Event
	for _, ns := range []string{"drv", "media"} {
		l, err := h.cs.CoreV1().Events(ns).List(context.Background(), metav1.ListOptions{})
		if err != nil {
			h.t.Fatal(err)
		}
		out = append(out, l.Items...)
	}
	return out
}

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
