package driver

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"

	"github.com/local-only/mergerfs-csi/pkg/branches"
	"github.com/local-only/mergerfs-csi/pkg/mergerfs"
)

const DriverName = "mergerfs.csi.k8s.io"

// Mode selects which CSI services this process exposes.
type Mode string

const (
	ModeNode       Mode = "node"
	ModeController Mode = "controller"
	ModeAll        Mode = "all"
)

const (
	ctxBranches        = "branches"
	ctxMergerfsOptions = "mergerfsOptions"
	ctxPodUID          = "csi.storage.k8s.io/pod.uid"
	defaultBranchWait  = 30 * time.Second
)

// Config holds driver runtime settings.
type Config struct {
	Name            string
	NodeID          string
	Endpoint        string
	Mode            Mode
	MergerfsPath    string
	KubeletPodsPath string
	StateDir        string
	BranchWait      time.Duration
	Supervisor      *mergerfs.Supervisor
}

// Driver is the CSI node and/or controller plugin.
type Driver struct {
	csi.UnimplementedIdentityServer
	csi.UnimplementedControllerServer
	csi.UnimplementedNodeServer

	cfg Config
	srv *grpc.Server
	mu  sync.Mutex
}

// New creates a driver. Supervisor must be set (or defaults built by NewDefault).
func New(cfg Config) (*Driver, error) {
	if cfg.Name == "" {
		cfg.Name = DriverName
	}
	if cfg.Mode == "" {
		cfg.Mode = ModeNode
	}
	switch cfg.Mode {
	case ModeNode, ModeController, ModeAll:
	default:
		return nil, fmt.Errorf("invalid mode %q", cfg.Mode)
	}
	if cfg.NodeID == "" {
		return nil, fmt.Errorf("node-id required")
	}
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("endpoint required")
	}
	if cfg.BranchWait == 0 {
		cfg.BranchWait = defaultBranchWait
	}
	if cfg.Supervisor == nil {
		return nil, fmt.Errorf("supervisor required")
	}
	return &Driver{cfg: cfg}, nil
}

// NewDefault wires real exec runner/unmounter/FS.
func NewDefault(cfg Config) (*Driver, error) {
	if cfg.MergerfsPath == "" {
		cfg.MergerfsPath = "mergerfs"
	}
	if cfg.StateDir == "" {
		cfg.StateDir = "/var/lib/mergerfs-csi"
	}
	cfg.Supervisor = &mergerfs.Supervisor{
		Bin:      cfg.MergerfsPath,
		StateDir: cfg.StateDir,
		Runner:   mergerfs.ExecRunner{},
		Unmount:  mergerfs.ExecUnmounter{},
		FS:       mergerfs.OSMounter{},
	}
	return New(cfg)
}

// Run starts the gRPC server and blocks until ctx cancel or Serve error.
func (d *Driver) Run(ctx context.Context) error {
	u, err := parseEndpoint(d.cfg.Endpoint)
	if err != nil {
		return err
	}
	if u.Scheme == "unix" {
		_ = os.Remove(u.Path)
	}
	lis, err := net.Listen(u.Scheme, u.Host+u.Path)
	if err != nil {
		return err
	}
	d.srv = grpc.NewServer()
	csi.RegisterIdentityServer(d.srv, d)
	if d.cfg.Mode == ModeNode || d.cfg.Mode == ModeAll {
		csi.RegisterNodeServer(d.srv, d)
	}
	if d.cfg.Mode == ModeController || d.cfg.Mode == ModeAll {
		csi.RegisterControllerServer(d.srv, d)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- d.srv.Serve(lis)
	}()

	select {
	case <-ctx.Done():
		d.srv.GracefulStop()
		return nil
	case err := <-errCh:
		return err
	}
}

func parseEndpoint(ep string) (*url.URL, error) {
	u, err := url.Parse(ep)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "" {
		return nil, fmt.Errorf("endpoint scheme required (unix:// or tcp://)")
	}
	if u.Scheme == "unix" {
		// url.Parse("unix:///path") -> Path=/path, Host empty
		if u.Path == "" && u.Opaque != "" {
			u.Path = "/" + u.Opaque
			u.Opaque = ""
		}
	}
	return u, nil
}

// --- Identity ---

func (d *Driver) GetPluginInfo(context.Context, *csi.GetPluginInfoRequest) (*csi.GetPluginInfoResponse, error) {
	return &csi.GetPluginInfoResponse{
		Name:          d.cfg.Name,
		VendorVersion: "0.1.0",
	}, nil
}

func (d *Driver) GetPluginCapabilities(context.Context, *csi.GetPluginCapabilitiesRequest) (*csi.GetPluginCapabilitiesResponse, error) {
	caps := []*csi.PluginCapability{}
	if d.controllerEnabled() {
		caps = append(caps, &csi.PluginCapability{
			Type: &csi.PluginCapability_Service_{
				Service: &csi.PluginCapability_Service{
					Type: csi.PluginCapability_Service_CONTROLLER_SERVICE,
				},
			},
		})
	}
	return &csi.GetPluginCapabilitiesResponse{Capabilities: caps}, nil
}

func (d *Driver) Probe(context.Context, *csi.ProbeRequest) (*csi.ProbeResponse, error) {
	return &csi.ProbeResponse{}, nil
}

// --- Node ---

func (d *Driver) NodeGetInfo(context.Context, *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return &csi.NodeGetInfoResponse{NodeId: d.cfg.NodeID}, nil
}

func (d *Driver) NodeGetCapabilities(context.Context, *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: []*csi.NodeServiceCapability{},
	}, nil
}

func (d *Driver) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, fmt.Errorf("volume_id required")
	}
	target := req.GetTargetPath()
	if target == "" {
		return nil, fmt.Errorf("target_path required")
	}
	vc := req.GetVolumeContext()
	if vc == nil {
		return nil, fmt.Errorf("volume_context required")
	}
	rawBranches, ok := vc[ctxBranches]
	if !ok || strings.TrimSpace(rawBranches) == "" {
		return nil, fmt.Errorf("volume_context %q required", ctxBranches)
	}
	bs, err := branches.Parse(rawBranches)
	if err != nil {
		return nil, err
	}
	opts := vc[ctxMergerfsOptions]
	podUID := vc[ctxPodUID]

	resolver := branches.Resolver{KubeletPodsPath: d.cfg.KubeletPodsPath}
	paths, err := resolver.ResolveAll(bs, podUID)
	if err != nil {
		return nil, err
	}
	if err := branches.WaitForDirs(ctx, paths, d.cfg.BranchWait); err != nil {
		return nil, err
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.cfg.Supervisor.Mount(ctx, req.GetVolumeId(), target, opts, paths); err != nil {
		return nil, err
	}
	return &csi.NodePublishVolumeResponse{}, nil
}

func (d *Driver) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, fmt.Errorf("volume_id required")
	}
	target := req.GetTargetPath()
	if target == "" {
		return nil, fmt.Errorf("target_path required")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	// Surface umount errors (ExecUnmounter treats not-mounted / ENOENT as nil).
	// Kill + pidfile cleanup still runs inside UnmountVolume.
	if err := d.cfg.Supervisor.UnmountVolume(req.GetVolumeId(), target); err != nil {
		return nil, err
	}
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

// Unused node RPCs — return empty / unimplemented as appropriate.

func (d *Driver) NodeStageVolume(context.Context, *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	return &csi.NodeStageVolumeResponse{}, nil
}

func (d *Driver) NodeUnstageVolume(context.Context, *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	return &csi.NodeUnstageVolumeResponse{}, nil
}

func (d *Driver) NodeGetVolumeStats(context.Context, *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	return nil, fmt.Errorf("unsupported")
}

func (d *Driver) NodeExpandVolume(context.Context, *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	return nil, fmt.Errorf("unsupported")
}
