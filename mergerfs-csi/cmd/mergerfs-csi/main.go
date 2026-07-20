package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/local-only/mergerfs-csi/pkg/driver"
)

func main() {
	endpoint := flag.String("endpoint", "unix:///var/lib/kubelet/plugins/mergerfs.csi.k8s.io/csi.sock", "CSI endpoint")
	nodeID := flag.String("node-id", "", "Kubernetes node ID")
	mode := flag.String("mode", "node", "CSI role: node|controller|all")
	mergerfsPath := flag.String("mergerfs-path", "mergerfs", "Path to mergerfs binary")
	kubeletPods := flag.String("kubelet-pods-path", "/var/lib/kubelet/pods", "Kubelet pods directory")
	stateDir := flag.String("state-dir", "/var/lib/mergerfs-csi", "Directory for pidfiles")
	flag.Parse()

	m, err := driver.ParseMode(*mode)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	if *nodeID == "" {
		*nodeID = os.Getenv("NODE_ID")
	}
	if *nodeID == "" {
		h, err := os.Hostname()
		if err != nil {
			fmt.Fprintf(os.Stderr, "node-id required: %v\n", err)
			os.Exit(1)
		}
		*nodeID = h
	}

	d, err := driver.NewDefault(driver.Config{
		Endpoint:        *endpoint,
		NodeID:          *nodeID,
		Mode:            m,
		MergerfsPath:    *mergerfsPath,
		KubeletPodsPath: *kubeletPods,
		StateDir:        *stateDir,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "driver: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := d.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "run: %v\n", err)
		os.Exit(1)
	}
}
