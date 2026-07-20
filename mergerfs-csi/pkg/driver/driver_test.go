package driver

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"

	"github.com/local-only/mergerfs-csi/pkg/mergerfs"
)

type fakeProc struct{ pid int }

func (p *fakeProc) PID() int   { return p.pid }
func (p *fakeProc) Wait() error { return nil }
func (p *fakeProc) Kill() error { return nil }

type fakeRunner struct {
	mu    sync.Mutex
	calls int
}

func (r *fakeRunner) Start(_ context.Context, _ string, _ []string) (mergerfs.Process, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return &fakeProc{pid: 42}, nil
}

type fakeFS struct {
	mounted map[string]bool
}

func (f *fakeFS) MkdirAll(string, os.FileMode) error { return nil }
func (f *fakeFS) IsMounted(t string) (bool, error)   { return f.mounted[t], nil }

type fakeUnmount struct{}

func (fakeUnmount) Unmount(string) error { return nil }

func testDriver(t *testing.T, kubelet string, wait time.Duration) (*Driver, *fakeRunner) {
	t.Helper()
	runner := &fakeRunner{}
	sup := &mergerfs.Supervisor{
		Bin:      "mergerfs",
		StateDir: t.TempDir(),
		Runner:   runner,
		Unmount:  fakeUnmount{},
		FS:       &fakeFS{mounted: map[string]bool{}},
		Kill:     func(int) error { return nil },
	}
	d, err := New(Config{
		NodeID:          "node-1",
		Endpoint:        "unix:///tmp/test.sock",
		KubeletPodsPath: kubelet,
		BranchWait:      wait,
		Supervisor:      sup,
	})
	if err != nil {
		t.Fatal(err)
	}
	return d, runner
}

func TestIdentity(t *testing.T) {
	d, _ := testDriver(t, "/k", time.Second)
	info, err := d.GetPluginInfo(context.Background(), &csi.GetPluginInfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != DriverName {
		t.Fatalf("name=%q", info.Name)
	}
	ni, err := d.NodeGetInfo(context.Background(), &csi.NodeGetInfoRequest{})
	if err != nil || ni.NodeId != "node-1" {
		t.Fatalf("%+v %v", ni, err)
	}
}

func TestNodePublishHostPath(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	d, runner := testDriver(t, "/k", time.Second)
	target := filepath.Join(t.TempDir(), "tgt")

	_, err := d.NodePublishVolume(context.Background(), &csi.NodePublishVolumeRequest{
		VolumeId:   "vol-1",
		TargetPath: target,
		VolumeContext: map[string]string{
			"branches": `[{"type":"hostPath","path":"` + a + `"},{"type":"hostPath","path":"` + b + `"}]`,
		},
		VolumeCapability: &csi.VolumeCapability{
			AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if runner.calls != 1 {
		t.Fatalf("calls=%d", runner.calls)
	}
}

func TestNodePublishPVCRef(t *testing.T) {
	kubelet := t.TempDir()
	uid := "pod-uid"
	volPath := filepath.Join(kubelet, uid, "volumes", "kubernetes.io~csi", "src", "mount")
	if err := os.MkdirAll(volPath, 0o755); err != nil {
		t.Fatal(err)
	}
	host := t.TempDir()

	d, runner := testDriver(t, kubelet, time.Second)
	target := filepath.Join(t.TempDir(), "tgt")

	_, err := d.NodePublishVolume(context.Background(), &csi.NodePublishVolumeRequest{
		VolumeId:   "vol-2",
		TargetPath: target,
		VolumeContext: map[string]string{
			"branches":                 `[{"type":"hostPath","path":"` + host + `"},{"type":"pvcRef","volumeName":"src"}]`,
			"csi.storage.k8s.io/pod.uid": uid,
			"mergerfsOptions":          "category.create=mfs",
		},
		VolumeCapability: &csi.VolumeCapability{
			AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if runner.calls != 1 {
		t.Fatalf("calls=%d", runner.calls)
	}
}

func TestNodePublishRejectsBadOptions(t *testing.T) {
	a := t.TempDir()
	d, _ := testDriver(t, "/k", time.Second)
	_, err := d.NodePublishVolume(context.Background(), &csi.NodePublishVolumeRequest{
		VolumeId:   "vol-bad",
		TargetPath: filepath.Join(t.TempDir(), "t"),
		VolumeContext: map[string]string{
			"branches":        `[{"type":"hostPath","path":"` + a + `"}]`,
			"mergerfsOptions": "a;rm -rf /",
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestNodeUnpublish(t *testing.T) {
	d, _ := testDriver(t, "/k", time.Second)
	_, err := d.NodeUnpublishVolume(context.Background(), &csi.NodeUnpublishVolumeRequest{
		VolumeId:   "vol-1",
		TargetPath: "/mnt/x",
	})
	if err != nil {
		t.Fatal(err)
	}
}
