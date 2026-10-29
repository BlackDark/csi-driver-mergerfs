package node

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/mount-utils"

	"github.com/BlackDark/csi-driver-mergerfs/internal/branches"
	"github.com/BlackDark/csi-driver-mergerfs/internal/mountpod"
	"github.com/BlackDark/csi-driver-mergerfs/internal/poolspec"
)

// Volume context keys kubelet adds with podInfoOnMount.
const (
	keyPodName      = "csi.storage.k8s.io/pod.name"
	keyPodNamespace = "csi.storage.k8s.io/pod.namespace"
	keyPodUID       = "csi.storage.k8s.io/pod.uid"
)

// NodePublishVolume resolves the branches, starts the pool's Mount Pod and
// bind-mounts the pool on the target.
func (s *Server) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	readOnly, err := validatePublish(req)
	if err != nil {
		return nil, err
	}
	vc := req.GetVolumeContext()
	spec, err := poolspec.Parse(poolspec.Input{Attributes: vc, AllowedHostPaths: s.cfg.AllowedHostPaths})
	if err != nil {
		return nil, specError(err)
	}
	target := req.GetTargetPath()
	id := mountpod.ID(req.GetVolumeId(), target)
	unlock := s.locks.lock(id)
	defer unlock()

	mounted, err := s.prepareTarget(target)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "target %s: %v", target, err)
	}
	if p := s.getPool(id); p != nil && mounted && !p.isLost() {
		return &csi.NodePublishVolumeResponse{}, nil
	}
	if mounted { // a bind of a dead pool
		if err := s.deps.Mounter.Unmount(target); err != nil {
			return nil, status.Errorf(codes.Internal, "unmount stale target %s: %v", target, err)
		}
	}
	if p := s.getPool(id); p != nil {
		s.stopPool(p)
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.PublishTimeout)
	defer cancel()
	p, err := s.preparePool(ctx, id, req.GetVolumeId(), target, vc, spec)
	if err != nil {
		return nil, err
	}
	opts := []string{"bind"}
	if readOnly {
		opts = append(opts, "ro")
	}
	if err := s.deps.Mounter.Mount(p.fuseDir, target, "", opts); err != nil {
		s.stopPool(p)
		return nil, status.Errorf(codes.Internal, "bind mount %s: %v", target, err)
	}
	s.forgetTarget(target)
	go s.monitor(p)
	if spec.Unrestricted && p.workload != nil {
		s.emit([]corev1.Event{s.unrestrictedEvent(*p.workload, req.GetVolumeId())})
	}
	return &csi.NodePublishVolumeResponse{}, nil
}

func validatePublish(req *csi.NodePublishVolumeRequest) (readOnly bool, err error) {
	switch {
	case req.GetVolumeId() == "":
		return false, status.Error(codes.InvalidArgument, "volume id missing")
	case req.GetTargetPath() == "":
		return false, status.Error(codes.InvalidArgument, "target path missing")
	case req.GetVolumeCapability() == nil:
		return false, status.Error(codes.InvalidArgument, "volume capability missing")
	case req.GetVolumeCapability().GetMount() == nil:
		return false, status.Error(codes.InvalidArgument, "only mount access type is supported")
	}
	mode := req.GetVolumeCapability().GetAccessMode().GetMode()
	readerOnly := mode == csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY ||
		mode == csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY
	return req.GetReadonly() || readerOnly, nil
}

// specError keeps user-facing messages: kubelet shows them in FailedMount Events.
func specError(err error) error {
	switch {
	case errors.Is(err, poolspec.ErrInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, branches.ErrNotReady), errors.Is(err, errBranchesNotReady):
		return status.Error(codes.Unavailable, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	}
	return status.Error(codes.Unavailable, err.Error())
}

// prepareTarget creates the target dir and reports whether it is a mount already. A
// corrupted (dead FUSE) mount is reported as mounted so the caller replaces it.
func (s *Server) prepareTarget(target string) (bool, error) {
	notMnt, err := s.deps.Mounter.IsLikelyNotMountPoint(target)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return false, os.MkdirAll(target, 0o750)
	case mount.IsCorruptedMnt(err):
		return true, nil
	case err != nil:
		return false, err
	}
	return !notMnt, nil
}

// preparePool resolves and waits for the branches, then starts the Mount Pod.
func (s *Server) preparePool(ctx context.Context, id, volumeID, target string, vc map[string]string, spec poolspec.Spec) (*pool, error) {
	ref := workloadRef(vc)
	consumer, err := s.consumerPod(ctx, ref, spec)
	if err != nil {
		return nil, specError(err)
	}
	ns := vc[keyPodNamespace]
	resolved, err := branches.Resolve(branches.Input{
		KubeletDir: s.cfg.Pod.KubeletDir, Pod: consumer, Branches: spec.Branches, Allowed: s.cfg.AllowedHostPaths,
		PV: func(claim string) (*corev1.PersistentVolume, error) { return s.deps.Kube.GetClaimPV(ctx, ns, claim) },
	})
	if err != nil {
		return nil, specError(err)
	}
	if err := s.waitBranches(ctx, resolved); err != nil {
		return nil, specError(err)
	}
	var w *mountpod.Workload
	if consumer != nil {
		w = &mountpod.Workload{Namespace: consumer.Namespace, Name: consumer.Name, UID: string(consumer.UID)}
		ref.UID = consumer.UID
	}
	pod, err := mountpod.Build(mountpod.Input{Config: s.cfg.Pod, NodeName: s.cfg.NodeID, ID: id, VolumeID: volumeID,
		Target: target, Workload: w, Spec: spec, Branches: resolved})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "build mount pod: %v", err)
	}
	p := s.newPool(id, target, volumeID, ref)
	if err := s.startPool(ctx, p, &pod); err != nil {
		return nil, specError(err)
	}
	return p, nil
}

// consumerPod reads the publishing pod when a branch refers to its volumes.
func (s *Server) consumerPod(ctx context.Context, ref *corev1.ObjectReference, spec poolspec.Spec) (*corev1.Pod, error) {
	needed := false
	for _, b := range spec.Branches {
		needed = needed || b.Type != poolspec.TypeHostPath
	}
	if !needed || ref == nil {
		return nil, nil // Resolve explains a missing pod for ref branches
	}
	pod, err := s.deps.Kube.GetWorkloadPod(ctx, ref.Namespace, ref.Name)
	if err != nil {
		return nil, fmt.Errorf("%w: consumer pod: %w", branches.ErrNotReady, err)
	}
	if pod == nil || (ref.UID != "" && pod.UID != ref.UID) {
		return nil, fmt.Errorf("%w: consumer pod %s/%s not found", branches.ErrNotReady, ref.Namespace, ref.Name)
	}
	return pod, nil
}

// workloadRef is the publishing pod from podInfoOnMount, or nil without it.
func workloadRef(vc map[string]string) *corev1.ObjectReference {
	name, ns := vc[keyPodName], vc[keyPodNamespace]
	if name == "" || ns == "" {
		return nil
	}
	return &corev1.ObjectReference{Kind: "Pod", APIVersion: "v1", Namespace: ns, Name: name, UID: types.UID(vc[keyPodUID])}
}
