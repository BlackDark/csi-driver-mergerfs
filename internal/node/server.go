// Package node implements the CSI Node service: one unprivileged Mount Pod runs
// mergerfs per published pool, and the plugin bind-mounts the pool into the target
// (docs/design/mount-pod-contract.md).
package node

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"k8s.io/mount-utils"
	"k8s.io/utils/clock"

	"github.com/BlackDark/csi-driver-mergerfs/internal/fusemount"
	"github.com/BlackDark/csi-driver-mergerfs/internal/handoff"
	"github.com/BlackDark/csi-driver-mergerfs/internal/mountpod"
)

// Config is the node plugin's settings.
type Config struct {
	NodeID string
	Pod    mountpod.Config
	// PublishTimeout bounds waiting for branches and for mergerfs to serve.
	PublishTimeout time.Duration
	// HealWorkloads evicts controller-owned consumer pods whose Mount Pod was lost.
	HealWorkloads bool
	// AllowedHostPaths are the roots hostPath branches may use; none disables them.
	AllowedHostPaths []string
}

// FuseMounter performs the kernel FUSE mount (package fusemount).
type FuseMounter interface {
	Mount(target string, o fusemount.Opts) (int, error)
	Unmount(target string, lazy bool) error
}

// HandoffServer serves one handoff socket (package handoff).
type HandoffServer interface {
	Serve(ctx context.Context, cfg handoff.ServerConfig, events chan<- handoff.Event) error
}

// RealFuse is the Linux fusemount implementation.
type RealFuse struct{}

// Mount opens /dev/fuse and mounts it.
func (RealFuse) Mount(target string, o fusemount.Opts) (int, error) {
	return fusemount.Mount(target, o)
}

// Unmount unmounts a FUSE mount.
func (RealFuse) Unmount(target string, lazy bool) error { return fusemount.Unmount(target, lazy) }

// RealHandoff is the handoff package server.
type RealHandoff struct{}

// Serve runs handoff.Serve.
func (RealHandoff) Serve(ctx context.Context, cfg handoff.ServerConfig, ev chan<- handoff.Event) error {
	return handoff.Serve(ctx, cfg, ev)
}

// Deps are the side effects, injected so tests can fake them.
type Deps struct {
	Kube    Kube
	Mounter mount.Interface
	Fuse    FuseMounter
	Handoff HandoffServer
	// PrepareDir creates a dir owned by the Mount Pod uid/gid.
	PrepareDir func(path string, uid, gid int) error
	// MountInfo reads this process's mount table; Statfs reports a path's usage.
	MountInfo func() ([]mount.MountInfo, error)
	Statfs    func(path string) (Usage, error)
	// Exists reports whether a dir exists (hostPath and emptyDir branches).
	Exists func(path string) bool
	Clock  clock.WithDelayedExecution
	Log    *slog.Logger
	// Metrics are the event counters; nil gets an unregistered set.
	Metrics *Metrics
}

// Server is the CSI Node service.
type Server struct {
	csi.UnimplementedNodeServer
	cfg  Config
	deps Deps

	locks keyedLock
	mu    sync.Mutex
	// pools are the live pools by id; Adopt rebuilds them after a restart.
	pools map[string]*pool
	// dead are targets whose pool lost its Mount Pod, with the reason. Guarded by mu.
	dead map[string]string
	bg   sync.WaitGroup
}

// New returns a Server; nil optional deps get the defaults.
func New(cfg Config, deps Deps) *Server {
	if deps.PrepareDir == nil {
		deps.PrepareDir = prepareDir
	}
	if deps.MountInfo == nil {
		deps.MountInfo = func() ([]mount.MountInfo, error) { return mount.ParseMountInfo("/proc/self/mountinfo") }
	}
	if deps.Statfs == nil {
		deps.Statfs = Statfs
	}
	if deps.Exists == nil {
		deps.Exists = func(p string) bool { fi, err := os.Stat(p); return err == nil && fi.IsDir() }
	}
	if deps.Clock == nil {
		deps.Clock = clock.RealClock{}
	}
	if deps.Metrics == nil {
		deps.Metrics = NewMetrics(nil)
	}
	return &Server{cfg: cfg, deps: deps, pools: map[string]*pool{}, dead: map[string]string{}}
}

func prepareDir(path string, uid, gid int) error {
	if err := os.MkdirAll(path, 0o750); err != nil {
		return err
	}
	return os.Chown(path, uid, gid)
}

// Wait blocks until background work (loss handling, healing) has finished.
func (s *Server) Wait() { s.bg.Wait() }

// NodeGetCapabilities lists volume stats and volume health.
func (s *Server) NodeGetCapabilities(context.Context, *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	caps := []csi.NodeServiceCapability_RPC_Type{
		csi.NodeServiceCapability_RPC_GET_VOLUME_STATS,
		csi.NodeServiceCapability_RPC_GET_VOLUME_HEALTH,
	}
	out := make([]*csi.NodeServiceCapability, 0, len(caps))
	for _, c := range caps {
		out = append(out, &csi.NodeServiceCapability{Type: &csi.NodeServiceCapability_Rpc{
			Rpc: &csi.NodeServiceCapability_RPC{Type: c},
		}})
	}
	return &csi.NodeGetCapabilitiesResponse{Capabilities: out}, nil
}

// NodeGetInfo returns the node id; there is no topology or volume limit.
func (s *Server) NodeGetInfo(context.Context, *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return &csi.NodeGetInfoResponse{NodeId: s.cfg.NodeID}, nil
}

func (s *Server) getPool(id string) *pool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pools[id]
}

func (s *Server) setPool(id string, p *pool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p == nil {
		delete(s.pools, id)
		return
	}
	s.pools[id] = p
}

func (s *Server) markDead(target, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dead[target] = reason
}

func (s *Server) forgetTarget(target string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.dead, target)
}

func (s *Server) deadReason(target string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.dead[target]
	return r, ok
}

// keyedLock serializes work per key.
type keyedLock struct {
	mu    sync.Mutex
	locks map[string]*refMutex
}

type refMutex struct {
	sync.Mutex
	refs int
}

// lock returns the unlock func.
func (k *keyedLock) lock(key string) func() {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = map[string]*refMutex{}
	}
	m := k.locks[key]
	if m == nil {
		m = &refMutex{}
		k.locks[key] = m
	}
	m.refs++
	k.mu.Unlock()
	m.Lock()
	return func() {
		m.Unlock()
		k.mu.Lock()
		m.refs--
		if m.refs == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}
