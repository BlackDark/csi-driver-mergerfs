package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"

	"github.com/BlackDark/csi-driver-mergerfs/internal/csiserver"
	"github.com/BlackDark/csi-driver-mergerfs/internal/identity"
	"github.com/BlackDark/csi-driver-mergerfs/internal/metrics"
	"github.com/BlackDark/csi-driver-mergerfs/internal/node"
)

// version is set with -ldflags "-X main.version=...".
var version = "dev"

type nodeFlags struct {
	endpoint     string
	cfg          node.Config
	uid, gid     int64
	tolerations  string
	resources    string
	allowedHosts string
	metricsAddr  string
	logLevel     slog.Level
}

func parseNodeFlags(args []string, stderr io.Writer) (nodeFlags, error) {
	var f nodeFlags
	p := &f.cfg.Pod
	fs := flag.NewFlagSet("node", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&f.endpoint, "endpoint", "unix:///csi/csi.sock", "CSI endpoint")
	fs.StringVar(&f.cfg.NodeID, "node-id", os.Getenv("NODE_ID"), "node name (env NODE_ID)")
	fs.StringVar(&p.Namespace, "namespace", os.Getenv("POD_NAMESPACE"), "driver namespace for Mount Pods (env POD_NAMESPACE)")
	fs.StringVar(&p.KubeletDir, "kubelet-dir", "/var/lib/kubelet", "kubelet root dir")
	fs.StringVar(&p.Image, "mount-image", "", "Mount Pod image")
	fs.Int64Var(&f.uid, "mount-uid", 65532, "Mount Pod uid: mergerfs runs, reads and writes branches as this uid")
	fs.Int64Var(&f.gid, "mount-gid", 65532, "Mount Pod gid")
	fs.StringVar(&p.PriorityClassName, "mount-priority-class", "system-node-critical", "Mount Pod priorityClassName")
	fs.StringVar(&f.tolerations, "mount-tolerations", "", "Mount Pod tolerations as a JSON list")
	fs.StringVar(&f.resources, "mount-resources", `{"requests":{"memory":"32Mi"}}`, "Mount Pod resources as JSON ResourceRequirements")
	fs.StringVar(&f.allowedHosts, "allowed-host-paths", "", "comma-separated host path roots hostPath branches may use; empty disables hostPath branches")
	fs.DurationVar(&f.cfg.PublishTimeout, "publish-timeout", 90*time.Second, "bound on waiting for branches and the Mount Pod")
	fs.BoolVar(&f.cfg.HealWorkloads, "heal-workloads", false, "evict controller-owned consumer pods whose Mount Pod was lost, so they remount")
	fs.StringVar(&f.metricsAddr, "metrics-addr", ":9809", "address for /metrics; empty disables")
	fs.TextVar(&f.logLevel, "log-level", slog.LevelInfo, "log level")
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	return f, f.finish(fs)
}

func (f *nodeFlags) finish(fs *flag.FlagSet) error {
	p := &f.cfg.Pod
	switch {
	case fs.NArg() > 0:
		return fmt.Errorf("%w: node: unexpected arguments %v", errUsage, fs.Args())
	case f.cfg.NodeID == "" || p.Namespace == "" || p.Image == "":
		return fmt.Errorf("%w: node: --node-id, --namespace and --mount-image are required", errUsage)
	case f.uid <= 0 || f.gid <= 0:
		return fmt.Errorf("%w: node: --mount-uid and --mount-gid must be non-root", errUsage)
	}
	p.UID, p.GID = f.uid, f.gid
	if f.tolerations != "" {
		var t []corev1.Toleration
		if err := json.Unmarshal([]byte(f.tolerations), &t); err != nil {
			return fmt.Errorf("%w: node: --mount-tolerations: %w", errUsage, err)
		}
		p.Tolerations = t
	}
	if err := json.Unmarshal([]byte(f.resources), &p.Resources); err != nil {
		return fmt.Errorf("%w: node: --mount-resources: %w", errUsage, err)
	}
	roots, err := parseRoots(f.allowedHosts)
	if err != nil {
		return fmt.Errorf("%w: node: --allowed-host-paths: %w", errUsage, err)
	}
	f.cfg.AllowedHostPaths = roots
	return nil
}

// parseRoots splits and validates the allowed host path roots.
func parseRoots(s string) ([]string, error) {
	var out []string
	for _, r := range strings.Split(s, ",") {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if !filepath.IsAbs(r) || strings.ContainsAny(r, ":=,*?[]") {
			return nil, fmt.Errorf("%q must be an absolute path without : = * ? [ ]", r)
		}
		out = append(out, filepath.Clean(r))
	}
	return out, nil
}

func runNode(args []string, stderr io.Writer) error {
	f, err := parseNodeFlags(args, stderr)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: f.logLevel}))
	rc, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("node: in-cluster config: %w", err)
	}
	cs, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return fmt.Errorf("node: kube client: %w", err)
	}
	reg := metrics.NewRegistry()
	srv := node.New(f.cfg, node.Deps{
		Kube:    node.NewKube(cs, f.cfg.Pod.Namespace),
		Mounter: node.NewBindMounter(),
		Fuse:    node.RealFuse{},
		Handoff: node.RealHandoff{},
		Log:     log,
		Metrics: node.NewMetrics(reg),
	})
	reg.MustRegister(srv.Collector())
	rpc := metrics.NewGRPC(reg)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	metrics.ServeBackground(ctx, f.metricsAddr, metrics.Handler(reg, nil), log)
	if err := srv.Adopt(ctx); err != nil {
		return fmt.Errorf("node: %w", err)
	}
	watchMountPods(ctx, cs, f.cfg, srv)
	return csiserver.Serve(ctx, f.endpoint, log, func(g *grpc.Server) {
		csi.RegisterIdentityServer(g, &identity.Server{Version: version})
		csi.RegisterNodeServer(g, srv)
	}, rpc.Interceptor(time.Now))
}

// watchMountPods feeds this node's Mount Pod changes to srv until ctx is done.
func watchMountPods(ctx context.Context, cs kubernetes.Interface, cfg node.Config, srv *node.Server) {
	factory := informers.NewSharedInformerFactoryWithOptions(cs, 0,
		informers.WithNamespace(cfg.Pod.Namespace),
		informers.WithTweakListOptions(func(o *metav1.ListOptions) { o.LabelSelector = node.MountPodSelector(cfg.NodeID) }))
	inf := factory.Core().V1().Pods().Informer()
	_, _ = inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(o any) { podChanged(srv, o, false) },
		UpdateFunc: func(_, o any) { podChanged(srv, o, false) },
		DeleteFunc: func(o any) { podChanged(srv, o, true) },
	})
	factory.Start(ctx.Done())
}

func podChanged(srv *node.Server, o any, deleted bool) {
	if t, ok := o.(cache.DeletedFinalStateUnknown); ok {
		o = t.Obj
	}
	if p, ok := o.(*corev1.Pod); ok {
		srv.MountPodChanged(p, deleted)
	}
}
