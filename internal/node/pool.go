package node

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/BlackDark/csi-driver-mergerfs/internal/fusemount"
	"github.com/BlackDark/csi-driver-mergerfs/internal/handoff"
	"github.com/BlackDark/csi-driver-mergerfs/internal/mountpod"
)

var errHandoffStopped = errors.New("handoff server stopped")

// pool is one mergerfs pool: its fuse/ mount, handoff server and Mount Pod.
type pool struct {
	id, name, target, volumeID string
	poolDir, fuseDir           string
	workload                   *corev1.ObjectReference // consumer pod; nil when unknown
	image                      string

	mounted     chan struct{} // the shim got the fd
	mountedOnce sync.Once
	lost        chan struct{} // the Pod watch saw the Mount Pod stop unasked
	lostOnce    sync.Once
	served      chan struct{} // closed when the handoff server returned
	stopServe   context.CancelFunc
	ready       atomic.Bool // mergerfs answered statfs on fuse/
	releasing   atomic.Bool // set while the plugin itself stops the Mount Pod
	podUID      atomic.Value
}

func (p *pool) uid() types.UID {
	v, _ := p.podUID.Load().(types.UID)
	return v
}

func (p *pool) signalLost() { p.lostOnce.Do(func() { close(p.lost) }) }

func (p *pool) isLost() bool { return closed(p.lost) }

func closed(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

func (s *Server) newPool(id, target, volumeID string, workload *corev1.ObjectReference) *pool {
	kd := s.cfg.Pod.KubeletDir
	return &pool{
		id: id, name: mountpod.Name(id), target: target, volumeID: volumeID, workload: workload,
		poolDir: mountpod.PoolDir(kd, id), fuseDir: mountpod.FuseDir(kd, id), image: s.cfg.Pod.Image,
		mounted: make(chan struct{}), lost: make(chan struct{}), served: make(chan struct{}),
	}
}

// serve runs p's handoff server until stopPool. The first mount request mounts
// fuse/ with the shim's options; already is set for adopted pools.
func (s *Server) serve(p *pool, already bool) {
	ctx, cancel := context.WithCancel(context.Background())
	p.stopServe = cancel
	events := make(chan handoff.Event, 8)
	uid, gid := int(s.cfg.Pod.UID), int(s.cfg.Pod.GID)
	cfg := handoff.ServerConfig{
		SockPath: mountpod.SockPath(s.cfg.Pod.KubeletDir, p.id), UID: uid, GID: gid, Mounted: already,
		Mount: func(opts string) (int, error) {
			o, err := fusemount.ParseOpts(opts, uid, gid)
			if err != nil {
				return -1, err
			}
			return s.deps.Fuse.Mount(p.fuseDir, o)
		},
	}
	go func() {
		defer close(p.served)
		if err := s.deps.Handoff.Serve(ctx, cfg, events); err != nil {
			s.deps.Log.Error("handoff server", "id", p.id, "err", err)
		}
	}()
	go func() {
		for {
			select {
			case <-p.served:
				return
			case e := <-events:
				if e.Kind == handoff.EventMounted {
					p.mountedOnce.Do(func() { close(p.mounted) })
				} else {
					s.deps.Log.Warn("handoff rejected", "id", p.id, "err", e.Err)
				}
			}
		}
	}()
}

// startPool prepares the pool dir, serves the handoff socket and creates the Mount
// Pod, then waits until mergerfs serves fuse/. Failures clean up after themselves.
func (s *Server) startPool(ctx context.Context, p *pool, pod *corev1.Pod) error {
	uid, gid := int(s.cfg.Pod.UID), int(s.cfg.Pod.GID)
	if err := s.deps.PrepareDir(p.poolDir, uid, gid); err != nil {
		return fmt.Errorf("prepare pool dir: %w", err)
	}
	// A mount left on fuse/ belongs to a dead Mount Pod (Adopt took the live ones).
	if err := s.deps.Fuse.Unmount(p.fuseDir, true); err == nil {
		s.deps.Log.Warn("unmounted stale pool mount", "id", p.id)
	}
	if err := s.deps.PrepareDir(p.fuseDir, uid, gid); err != nil {
		return fmt.Errorf("prepare fuse dir: %w", err)
	}
	s.serve(p, false)
	s.setPool(p.id, p)
	created, err := s.deps.Kube.CreatePod(ctx, pod)
	if err == nil && created.DeletionTimestamp != nil {
		err = fmt.Errorf("mount pod %s is still terminating", pod.Name)
	}
	if err == nil {
		p.podUID.Store(created.UID)
		err = s.waitServing(ctx, p)
	}
	if err != nil {
		s.stopPool(p)
		return err
	}
	return nil
}

// waitServing blocks until the shim got the fd and fuse/ answers statfs, which only
// succeeds once mergerfs finished FUSE init.
func (s *Server) waitServing(ctx context.Context, p *pool) error {
	select {
	case <-p.mounted:
	case <-p.lost:
		return errors.New("mount pod stopped before mounting")
	case <-p.served:
		return errHandoffStopped
	case <-ctx.Done():
		return fmt.Errorf("mount pod did not mount: %w", ctx.Err())
	}
	statted := make(chan error, 1)
	go func() { _, err := s.deps.Statfs(p.fuseDir); statted <- err }()
	select {
	case err := <-statted:
		if err != nil {
			return fmt.Errorf("pool not serving: %w", err)
		}
		p.ready.Store(true)
		return nil
	case <-p.lost:
		return errors.New("mount pod stopped while starting")
	case <-ctx.Done():
		return fmt.Errorf("mergerfs did not answer: %w", ctx.Err())
	}
}

// stopPool deletes the Mount Pod, lazily unmounts fuse/ and removes the pool dir.
func (s *Server) stopPool(p *pool) {
	p.releasing.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), kubeCallTimeout)
	defer cancel()
	if err := s.deps.Kube.DeletePod(ctx, p.name); err != nil {
		s.deps.Log.Warn("delete mount pod", "id", p.id, "err", err)
	}
	if err := s.deps.Fuse.Unmount(p.fuseDir, true); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.deps.Log.Debug("unmount pool", "id", p.id, "err", err)
	}
	if p.stopServe != nil {
		p.stopServe()
		<-p.served
	}
	_ = os.Remove(p.fuseDir)
	_ = os.Remove(mountpod.SockPath(s.cfg.Pod.KubeletDir, p.id))
	_ = os.Remove(p.poolDir)
	s.setPool(p.id, nil)
}

const kubeCallTimeout = 30 * time.Second
