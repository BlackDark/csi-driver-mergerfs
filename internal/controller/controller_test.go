package controller

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newServer() *Server { return &Server{Log: slog.New(slog.NewTextHandler(io.Discard, nil))} }

func mountCap() *csi.VolumeCapability {
	return &csi.VolumeCapability{
		AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
		AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
	}
}

const branches = `[{"type":"hostPath","path":"/srv/media"},{"type":"pvcRef","volumeName":"remote","mode":"NC"}]`

func createReq(p map[string]string) *csi.CreateVolumeRequest {
	return &csi.CreateVolumeRequest{Name: "pvc-1", CapacityRange: &csi.CapacityRange{RequiredBytes: 1 << 30},
		VolumeCapabilities: []*csi.VolumeCapability{mountCap()}, Parameters: p}
}

func TestCreateVolume(t *testing.T) {
	resp, err := newServer().CreateVolume(context.Background(), createReq(map[string]string{
		"branches": branches, "mergerfsOptions": "category.create=pfrd", "csi.storage.k8s.io/pvc/name": "x",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"branches": branches, "mergerfsOptions": "category.create=pfrd"}
	if v := resp.Volume; v.VolumeId != "pvc-1" || v.CapacityBytes != 1<<30 || !reflect.DeepEqual(v.VolumeContext, want) {
		t.Errorf("volume %+v", v)
	}
}

func TestCreateVolumeRejects(t *testing.T) {
	s := newServer()
	for name, req := range map[string]*csi.CreateVolumeRequest{
		"no name":     {VolumeCapabilities: []*csi.VolumeCapability{mountCap()}, Parameters: map[string]string{"branches": branches}},
		"no branches": createReq(nil),
		"bad option":  createReq(map[string]string{"branches": branches, "mergerfsOptions": "log.file=/x"}),
		"block": {Name: "x", Parameters: map[string]string{"branches": branches}, VolumeCapabilities: []*csi.VolumeCapability{{
			AccessType: &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}},
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER}}}},
	} {
		if _, err := s.CreateVolume(context.Background(), req); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

func TestDeleteAndValidate(t *testing.T) {
	s := newServer()
	if _, err := s.DeleteVolume(context.Background(), &csi.DeleteVolumeRequest{VolumeId: "pvc-1"}); err != nil {
		t.Error(err)
	}
	r, err := s.ValidateVolumeCapabilities(context.Background(), &csi.ValidateVolumeCapabilitiesRequest{
		VolumeId: "pvc-1", VolumeCapabilities: []*csi.VolumeCapability{mountCap()}})
	if err != nil || r.Confirmed == nil {
		t.Errorf("validate %+v %v", r, err)
	}
}
