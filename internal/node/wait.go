package node

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/BlackDark/csi-driver-mergerfs/internal/branches"
)

var errBranchesNotReady = errors.New("branches not ready")

const branchPoll = 250 * time.Millisecond

// waitBranches blocks until every branch is usable: volume branches are mount points
// that answer statfs (kubelet mounts the consumer's volumes in parallel with this
// publish), other branches exist. Bounded by ctx (the publish timeout).
func (s *Server) waitBranches(ctx context.Context, rs []branches.Resolved) error {
	for {
		pending, err := s.pendingBranches(rs)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: waiting for %s: %w", errBranchesNotReady, strings.Join(pending, ", "), ctx.Err())
		case <-s.deps.Clock.After(branchPoll):
		}
	}
}

// pendingBranches describes the branches not usable yet.
func (s *Server) pendingBranches(rs []branches.Resolved) ([]string, error) {
	var mountPoints map[string]bool
	var pending []string
	for i, r := range rs {
		if !r.Mounted {
			if !s.deps.Exists(r.Path) {
				pending = append(pending, fmt.Sprintf("branches[%d] %s to exist", i, r.Path))
			}
			continue
		}
		if mountPoints == nil {
			infos, err := s.deps.MountInfo()
			if err != nil {
				return nil, fmt.Errorf("mount table: %w", err)
			}
			mountPoints = map[string]bool{}
			for _, mi := range infos {
				mountPoints[mi.MountPoint] = true
			}
		}
		if !mountPoints[r.Path] {
			pending = append(pending, fmt.Sprintf("branches[%d] %s to be mounted", i, r.Path))
			continue
		}
		if _, err := s.deps.Statfs(r.Path); err != nil {
			pending = append(pending, fmt.Sprintf("branches[%d] %s to answer statfs (%v)", i, r.Path, err))
		}
	}
	return pending, nil
}
