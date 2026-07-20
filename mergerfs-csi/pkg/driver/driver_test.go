package driver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"

	"github.com/local-only/mergerfs-csi/pkg/mergerfs"
)

type fakeProc struct{ pid int }

func (p *fakeProc) PID() int    { return p.pid }
func (p *fakeProc) Wait() error { return nil }
func (p *fakeProc) Kill() error { return nil }
func (p *fakeProc) Alive() bool { return true }

type fakeRunner struct {
	mu    sync.Mutex
	calls int
	fs    *fakeFS
}

func (r *fakeRunner) Start(_ context.Context, _ string, args []string) (mergerfs.Process, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.fs != nil && len(args) > 0 {
		r.fs.mounted[args[len(args)-1]] = true
	}
	return &fakeProc{pid: 42}, nil
}

type fakeFS struct {
	mounted map[string]bool
}

func (f *fakeFS) MkdirAll(string, os.FileMode) error { return nil }
func (f *fakeFS) IsMounted(t string) (bool, error)   { return f.mounted[t], nil }

type fakeUnmount struct {
	err error
}

func (u fakeUnmount) Unmount(string) error { return u.err }

func testDriver(t *testing.T, kubelet string, wait time.Duration) (*Driver, *fakeRunner) {
	t.Helper()
	fs := &fakeFS{mounted: map[string]bool{}}
	runner := &fakeRunner{fs: fs}
	sup := &mergerfs.Supervisor{
		Bin:      "mergerfs",
		StateDir: t.TempDir(),
		Runner:   runner,
		Unmount:  fakeUnmount{},
		FS:       fs,
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

func TestNodeUnpublishSurfacesUnmountError(t *testing.T) {
	runner := &fakeRunner{}
	sup := &mergerfs.Supervisor{
		Bin:      "mergerfs",
		StateDir: t.TempDir(),
		Runner:   runner,
		Unmount:  fakeUnmount{err: fmt.Errorf("umount: device busy")},
		FS:       &fakeFS{mounted: map[string]bool{}},
		Kill:     func(int) error { return nil },
	}
	d, err := New(Config{
		NodeID:          "node-1",
		Endpoint:        "unix:///tmp/test.sock",
		KubeletPodsPath: "/k",
		BranchWait:      time.Second,
		Supervisor:      sup,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.NodeUnpublishVolume(context.Background(), &csi.NodeUnpublishVolumeRequest{
		VolumeId:   "vol-1",
		TargetPath: "/mnt/x",
	})
	if err == nil {
		t.Fatal("expected unmount error surfaced")
	}
}

func TestCreateVolumeCopiesParameters(t *testing.T) {
	d, _ := testDriver(t, "/k", time.Second)
	d.cfg.Mode = ModeController

	branches := `[{"type":"hostPath","path":"/data/a"}]`
	opts := "category.create=mfs"
	resp, err := d.CreateVolume(context.Background(), &csi.CreateVolumeRequest{
		Name: "pvc-xyz",
		CapacityRange: &csi.CapacityRange{
			RequiredBytes: 1 << 30,
		},
		VolumeCapabilities: []*csi.VolumeCapability{
			{
				AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
				AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER},
			},
		},
		Parameters: map[string]string{
			"branches":         branches,
			"mergerfsOptions":  opts,
			"csi.storage.k8s.io/fstype": "fuse",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Volume.VolumeId != "pvc-xyz" {
		t.Fatalf("id=%q", resp.Volume.VolumeId)
	}
	if resp.Volume.CapacityBytes != 1<<30 {
		t.Fatalf("cap=%d", resp.Volume.CapacityBytes)
	}
	if resp.Volume.VolumeContext["branches"] != branches {
		t.Fatalf("branches=%q", resp.Volume.VolumeContext["branches"])
	}
	if resp.Volume.VolumeContext["mergerfsOptions"] != opts {
		t.Fatalf("opts=%q", resp.Volume.VolumeContext["mergerfsOptions"])
	}
}

func TestGetPluginCapabilitiesController(t *testing.T) {
	d, _ := testDriver(t, "/k", time.Second)
	caps, err := d.GetPluginCapabilities(context.Background(), &csi.GetPluginCapabilitiesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(caps.Capabilities) != 0 {
		t.Fatalf("node mode should have no controller cap, got %+v", caps.Capabilities)
	}

	d.cfg.Mode = ModeController
	caps, err = d.GetPluginCapabilities(context.Background(), &csi.GetPluginCapabilitiesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(caps.Capabilities) != 1 {
		t.Fatalf("want 1 cap, got %+v", caps.Capabilities)
	}
}
