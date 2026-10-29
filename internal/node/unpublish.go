package node

import (
	"context"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/mount-utils"

	"github.com/BlackDark/csi-driver-mergerfs/internal/mountpod"
)

// NodeUnpublishVolume unmounts the target and stops its pool. The pool id follows
// from volume id and target, so a pool this process does not know (a failed adopt)
// is still cleaned up.
func (s *Server) NodeUnpublishVolume(_ context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	target := req.GetTargetPath()
	if req.GetVolumeId() == "" || target == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id and target path are required")
	}
	id := mountpod.ID(req.GetVolumeId(), target)
	unlock := s.locks.lock(id)
	defer unlock()
	if err := mount.CleanupMountPoint(target, s.deps.Mounter, true); err != nil {
		return nil, status.Errorf(codes.Internal, "unmount %s: %v", target, err)
	}
	s.forgetTarget(target)
	p := s.getPool(id)
	if p == nil {
		p = s.newPool(id, target, req.GetVolumeId(), nil)
	}
	s.stopPool(p)
	return &csi.NodeUnpublishVolumeResponse{}, nil
}
