package driver

import (
	"context"
	"fmt"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CreateVolume copies StorageClass parameters into VolumeContext (no disk alloc).
func (d *Driver) CreateVolume(_ context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	if !d.controllerEnabled() {
		return nil, status.Error(codes.Unimplemented, "controller not enabled")
	}
	name := req.GetName()
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "name required")
	}
	if len(req.GetVolumeCapabilities()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "volume_capabilities required")
	}
	if req.GetVolumeContentSource() != nil {
		return nil, status.Error(codes.Unimplemented, "VolumeContentSource not supported")
	}

	var capacity int64
	if cr := req.GetCapacityRange(); cr != nil {
		capacity = cr.GetRequiredBytes()
		if capacity < 0 {
			return nil, status.Error(codes.InvalidArgument, "required_bytes must be >= 0")
		}
		if lim := cr.GetLimitBytes(); lim > 0 && capacity > lim {
			return nil, status.Error(codes.InvalidArgument, "required_bytes exceeds limit_bytes")
		}
	}

	volCtx := map[string]string{}
	for k, v := range req.GetParameters() {
		volCtx[k] = v
	}

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      name,
			CapacityBytes: capacity,
			VolumeContext: volCtx,
		},
	}, nil
}

// DeleteVolume is a no-op (volumes are virtual context only).
func (d *Driver) DeleteVolume(_ context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	if !d.controllerEnabled() {
		return nil, status.Error(codes.Unimplemented, "controller not enabled")
	}
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume_id required")
	}
	return &csi.DeleteVolumeResponse{}, nil
}

func (d *Driver) ControllerGetCapabilities(context.Context, *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	if !d.controllerEnabled() {
		return nil, status.Error(codes.Unimplemented, "controller not enabled")
	}
	return &csi.ControllerGetCapabilitiesResponse{
		Capabilities: []*csi.ControllerServiceCapability{
			{
				Type: &csi.ControllerServiceCapability_Rpc{
					Rpc: &csi.ControllerServiceCapability_RPC{
						Type: csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
					},
				},
			},
		},
	}, nil
}

func (d *Driver) controllerEnabled() bool {
	return d.cfg.Mode == ModeController || d.cfg.Mode == ModeAll
}

// Unused controller RPCs.

func (d *Driver) ControllerPublishVolume(context.Context, *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "unsupported")
}

func (d *Driver) ControllerUnpublishVolume(context.Context, *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "unsupported")
}

func (d *Driver) ValidateVolumeCapabilities(context.Context, *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	return nil, status.Error(codes.Unimplemented, "unsupported")
}

func (d *Driver) ListVolumes(context.Context, *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	return nil, status.Error(codes.Unimplemented, "unsupported")
}

func (d *Driver) GetCapacity(context.Context, *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	return nil, status.Error(codes.Unimplemented, "unsupported")
}

func (d *Driver) CreateSnapshot(context.Context, *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	return nil, status.Error(codes.Unimplemented, "unsupported")
}

func (d *Driver) DeleteSnapshot(context.Context, *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	return nil, status.Error(codes.Unimplemented, "unsupported")
}

func (d *Driver) ListSnapshots(context.Context, *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "unsupported")
}

func (d *Driver) ControllerExpandVolume(context.Context, *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "unsupported")
}

func (d *Driver) ControllerGetVolume(context.Context, *csi.ControllerGetVolumeRequest) (*csi.ControllerGetVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "unsupported")
}

// Mode helpers used by tests / main.
func ParseMode(s string) (Mode, error) {
	switch s {
	case "", string(ModeNode):
		return ModeNode, nil
	case string(ModeController):
		return ModeController, nil
	case string(ModeAll):
		return ModeAll, nil
	default:
		return "", fmt.Errorf("invalid mode %q (want node|controller|all)", s)
	}
}
