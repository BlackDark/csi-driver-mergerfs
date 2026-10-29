// Package controller implements the CSI Controller service for Dynamic Volumes: a
// StorageClass's parameters become the volume context. It is stateless and stores
// nothing; a pool is only its branches, so DeleteVolume has nothing to remove.
package controller

import (
	"context"
	"log/slog"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/BlackDark/csi-driver-mergerfs/internal/poolspec"
)

// k8sParamPrefix marks parameters Kubernetes adds; they never reach the volume context.
const k8sParamPrefix = "csi.storage.k8s.io/"

// Server answers Controller RPCs.
type Server struct {
	csi.UnimplementedControllerServer
	Log *slog.Logger
}

// CreateVolume turns StorageClass parameters into a volume ID and context.
func (s *Server) CreateVolume(_ context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if err := checkCapabilities(req.GetVolumeCapabilities()); err != nil {
		return nil, err
	}
	if req.GetVolumeContentSource() != nil {
		return nil, status.Error(codes.InvalidArgument, "volume content sources are not supported")
	}
	vc := map[string]string{}
	for k, v := range req.GetParameters() {
		if !strings.HasPrefix(k, k8sParamPrefix) {
			vc[k] = v
		}
	}
	// The hostPath allowlist is a node setting: the node checks it at publish.
	if _, err := poolspec.Parse(poolspec.Input{Attributes: vc, AllowedHostPaths: []string{"/"}}); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	s.Log.Info("volume created", "volumeID", req.GetName())
	return &csi.CreateVolumeResponse{Volume: &csi.Volume{
		VolumeId:      req.GetName(),
		CapacityBytes: capacity(req.GetCapacityRange()),
		VolumeContext: vc,
	}}, nil
}

// capacity echoes the request: a pool's size is that of its branches.
func capacity(r *csi.CapacityRange) int64 {
	if b := r.GetRequiredBytes(); b > 0 {
		return b
	}
	return r.GetLimitBytes()
}

func checkCapabilities(caps []*csi.VolumeCapability) error {
	if len(caps) == 0 {
		return status.Error(codes.InvalidArgument, "volume capabilities are required")
	}
	for _, c := range caps {
		if c.GetMount() == nil {
			return status.Error(codes.InvalidArgument, "only mount access type is supported")
		}
		if c.GetAccessMode() == nil {
			return status.Error(codes.InvalidArgument, "access mode is required")
		}
	}
	return nil
}

// DeleteVolume succeeds without touching branch data.
func (s *Server) DeleteVolume(_ context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	s.Log.Info("volume deleted, branch data kept", "volumeID", req.GetVolumeId())
	return &csi.DeleteVolumeResponse{}, nil
}

// ValidateVolumeCapabilities confirms mount access in every access mode.
func (s *Server) ValidateVolumeCapabilities(_ context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	if len(req.GetVolumeCapabilities()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "volume capabilities are required")
	}
	if err := checkCapabilities(req.GetVolumeCapabilities()); err != nil {
		return &csi.ValidateVolumeCapabilitiesResponse{Message: status.Convert(err).Message()}, nil
	}
	return &csi.ValidateVolumeCapabilitiesResponse{Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{
		VolumeContext:      req.GetVolumeContext(),
		VolumeCapabilities: req.GetVolumeCapabilities(),
		Parameters:         req.GetParameters(),
	}}, nil
}

// ControllerGetCapabilities lists CREATE_DELETE_VOLUME only.
func (s *Server) ControllerGetCapabilities(context.Context, *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	return &csi.ControllerGetCapabilitiesResponse{Capabilities: []*csi.ControllerServiceCapability{{
		Type: &csi.ControllerServiceCapability_Rpc{Rpc: &csi.ControllerServiceCapability_RPC{
			Type: csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
		}},
	}}}, nil
}
