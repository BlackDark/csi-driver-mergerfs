package node

import (
	"context"
	"errors"
	"os"
	"syscall"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ReasonMountPodLost is the CSI health reason for a target whose Mount Pod is gone.
const ReasonMountPodLost = "MountPodLost"

const notConnected = "the Mount Pod is gone (transport endpoint is not connected); restart the pod to remount"

// NodeGetVolumeHealth reports a target Inaccessible when its Mount Pod was lost or the
// pool answers ENOTCONN; kubelet writes it to pod.status.volumeHealth.
func (s *Server) NodeGetVolumeHealth(_ context.Context, req *csi.NodeGetVolumeHealthRequest) (*csi.NodeGetVolumeHealthResponse, error) {
	path := req.GetVolumePublishPath()
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id is required")
	}
	health := &csi.VolumeHealth{VolumeId: req.GetVolumeId()}
	if path == "" {
		return &csi.NodeGetVolumeHealthResponse{VolumeHealth: health}, nil
	}
	msg, dead := s.deadReason(path)
	if !dead {
		_, err := s.deps.Statfs(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			return nil, status.Errorf(codes.NotFound, "volume path %s not found", path)
		case errors.Is(err, syscall.ENOTCONN):
			msg, dead = notConnected, true
		case err != nil:
			return nil, status.Errorf(codes.Internal, "statfs %s: %v", path, err)
		}
	}
	if dead {
		health.HealthStatuses = []*csi.VolumeHealth_VolumeHealthEntry{{
			Status: csi.VolumeHealthErrorType_INACCESSIBLE, Reason: ReasonMountPodLost, Message: msg,
		}}
	}
	return &csi.NodeGetVolumeHealthResponse{VolumeHealth: health}, nil
}

// NodeGetVolumeStats reports statfs usage of a target; a dead target is Unavailable.
func (s *Server) NodeGetVolumeStats(_ context.Context, req *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	path := req.GetVolumePath()
	if req.GetVolumeId() == "" || path == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id and volume path are required")
	}
	u, err := s.deps.Statfs(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, status.Errorf(codes.NotFound, "volume path %s not found", path)
	case errors.Is(err, syscall.ENOTCONN):
		return nil, status.Error(codes.Unavailable, notConnected)
	case err != nil:
		return nil, status.Errorf(codes.Internal, "statfs %s: %v", path, err)
	}
	return &csi.NodeGetVolumeStatsResponse{Usage: []*csi.VolumeUsage{
		{Unit: csi.VolumeUsage_BYTES, Total: u.TotalBytes, Available: u.AvailableBytes, Used: u.UsedBytes},
		{Unit: csi.VolumeUsage_INODES, Total: u.TotalInodes, Available: u.FreeInodes, Used: u.UsedInodes},
	}}, nil
}
