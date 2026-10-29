package node

import (
	"context"
	"strings"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/BlackDark/csi-driver-mergerfs/internal/mountpod"
	"github.com/BlackDark/csi-driver-mergerfs/internal/poolspec"
)

func code(err error) codes.Code { return status.Code(err) }

func TestPublishMediaLayout(t *testing.T) {
	h := newHarness(t)
	h.consumer()
	h.readyBranches()
	ctx := context.Background()
	if _, err := h.srv.NodePublishVolume(ctx, publishReq(h.target(), h.mediaLayout())); err != nil {
		t.Fatal(err)
	}
	id := mountpod.ID("vol-1", h.target())
	pods := h.mountPods()
	if len(pods) != 1 || pods[0].Name != mountpod.Name(id) {
		t.Fatalf("mount pods: %+v", pods)
	}
	args := pods[0].Spec.Containers[0].Args
	wantBranches := h.localBranch() + "=RW:/branches/1=NC"
	if args[2] != "auto_unmount,category.create=pfrd,func.getattr=newest,cache.files=off,dropcacheonclose=false" || args[3] != wantBranches {
		t.Errorf("args %q", args)
	}
	o, ok := h.fuse.opts(mountpod.FuseDir(h.kd, id))
	if !ok || o.UID != 1000 || o.Source != "pool" || !o.DefaultPermissions {
		t.Errorf("fuse opts %+v %v", o, ok)
	}
	if mps, _ := h.mounter.List(); len(mps) != 1 || mps[0].Path != h.target() || mps[0].Device != mountpod.FuseDir(h.kd, id) {
		t.Errorf("binds %+v", mps)
	}
	// Idempotent: a second publish of the healthy target does nothing new.
	if _, err := h.srv.NodePublishVolume(ctx, publishReq(h.target(), h.mediaLayout())); err != nil {
		t.Fatal(err)
	}
	if h.ho.serves != 1 {
		t.Errorf("serves %d", h.ho.serves)
	}
}

func TestPublishReadOnly(t *testing.T) {
	h := newHarness(t)
	h.consumer()
	h.readyBranches()
	req := publishReq(h.target(), h.mediaLayout())
	req.Readonly = true
	if _, err := h.srv.NodePublishVolume(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	mps, _ := h.mounter.List()
	if len(mps) != 1 || !strings.Contains(strings.Join(mps[0].Opts, ","), "ro") {
		t.Errorf("binds %+v", mps)
	}
}

func TestPublishRejects(t *testing.T) {
	h := newHarness(t)
	h.consumer()
	cases := map[string]struct {
		vc   map[string]string
		want string
	}{
		"outside allowlist": {map[string]string{poolspec.KeyBranches: `[{"type":"hostPath","path":"/etc"}]`}, `hostPath "/etc" is not under`},
		"unknown volume": {map[string]string{poolspec.KeyBranches: `[{"type":"pvcRef","volumeName":"nope"}]`,
			keyPodName: "jellyfin-0", keyPodNamespace: "media", keyPodUID: "u1"}, `no volume "nope"`},
		"no pod info": {map[string]string{poolspec.KeyBranches: `[{"type":"pvcRef","volumeName":"remote"}]`}, "podInfoOnMount"},
		"option":      {map[string]string{poolspec.KeyBranches: `[{"type":"emptyDirRef","volumeName":"scratch"}]`, poolspec.KeyOptions: "log.file=/x"}, "log.file"},
	}
	for name, c := range cases {
		_, err := h.srv.NodePublishVolume(context.Background(), publishReq(h.target(), c.vc))
		if code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	if len(h.mountPods()) != 0 {
		t.Error("rejected publish created a Mount Pod")
	}
}

func TestPublishWaitsForBranchMount(t *testing.T) {
	h := newHarness(t)
	h.consumer()
	h.readyBranches()
	h.mu.Lock()
	h.infos = nil // the rclone PVC dir exists but kubelet has not mounted it yet
	h.mu.Unlock()
	_, err := h.srv.NodePublishVolume(context.Background(), publishReq(h.target(), h.mediaLayout()))
	if code(err) != codes.Unavailable || !strings.Contains(err.Error(), "to be mounted") {
		t.Fatalf("got %v", err)
	}
	if len(h.mountPods()) != 0 {
		t.Error("Mount Pod created before its branches were ready")
	}
	h.addMount(h.remoteBranch())
	if _, err := h.srv.NodePublishVolume(context.Background(), publishReq(h.target(), h.mediaLayout())); err != nil {
		t.Fatal(err)
	}
}

func TestPublishMountPodNeverMounts(t *testing.T) {
	h := newHarness(t)
	h.consumer()
	h.readyBranches()
	h.ho.hang = true
	_, err := h.srv.NodePublishVolume(context.Background(), publishReq(h.target(), h.mediaLayout()))
	if code(err) != codes.DeadlineExceeded {
		t.Fatalf("got %v", err)
	}
	if len(h.mountPods()) != 0 || h.srv.getPool(mountpod.ID("vol-1", h.target())) != nil {
		t.Error("failed publish left its pool behind")
	}
}

func TestUnpublish(t *testing.T) {
	h := newHarness(t)
	h.consumer()
	h.readyBranches()
	ctx := context.Background()
	if _, err := h.srv.NodePublishVolume(ctx, publishReq(h.target(), h.mediaLayout())); err != nil {
		t.Fatal(err)
	}
	if _, err := h.srv.NodeUnpublishVolume(ctx, &csi.NodeUnpublishVolumeRequest{VolumeId: "vol-1", TargetPath: h.target()}); err != nil {
		t.Fatal(err)
	}
	id := mountpod.ID("vol-1", h.target())
	if len(h.mountPods()) != 0 || h.srv.getPool(id) != nil {
		t.Error("pool not stopped")
	}
	if _, ok := h.fuse.opts(mountpod.FuseDir(h.kd, id)); ok {
		t.Error("fuse still mounted")
	}
	// Unknown targets are fine.
	if _, err := h.srv.NodeUnpublishVolume(ctx, &csi.NodeUnpublishVolumeRequest{VolumeId: "vol-1", TargetPath: h.kd + "/gone"}); err != nil {
		t.Error(err)
	}
}

func TestPublishUnrestrictedEvent(t *testing.T) {
	h := newHarness(t)
	h.consumer()
	h.readyBranches()
	vc := h.mediaLayout()
	vc[poolspec.KeyAllowAllOptions] = "true"
	vc[poolspec.KeyOptions] = "log.file=/tmp/x"
	if _, err := h.srv.NodePublishVolume(context.Background(), publishReq(h.target(), vc)); err != nil {
		t.Fatal(err)
	}
	ev := h.events()
	if len(ev) != 1 || ev[0].Reason != ReasonUnrestrictedOptions || ev[0].InvolvedObject.Name != "jellyfin-0" {
		t.Errorf("events %+v", ev)
	}
}
