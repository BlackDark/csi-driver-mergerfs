//go:build e2e

// Package e2e runs the driver on kind (hack/e2e/up.sh, `mise run e2e`). Tests that
// restart the node plugin or kill a Mount Pod are serial; the rest run in parallel,
// each with its own pods and host dirs.
package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	appNS      = "e2e-app"
	driverName = "mergerfs.csi.blackdark.github.io"
	busybox    = "busybox:1.37"
	// hostRoot is allowedHostPaths in hack/e2e/up.sh, inside the kind node.
	hostRoot = "/srv/e2e"
	poolUID  = 1000
)

func driverNS() string {
	if ns := os.Getenv("DRIVER_NS"); ns != "" {
		return ns
	}
	return "csi-mergerfs"
}

func kindNode() string {
	c := os.Getenv("CLUSTER")
	if c == "" {
		c = "csi-mergerfs-e2e"
	}
	return c + "-control-plane"
}

var cs kubernetes.Interface

func TestMain(m *testing.M) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		panic(err)
	}
	cs = kubernetes.NewForConfigOrDie(cfg)
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: appNS}}
	if _, err := cs.CoreV1().Namespaces().Create(context.Background(), ns, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		panic(err)
	}
	os.Exit(m.Run())
}

// nodeExec runs a shell command in the kind node, e.g. to prepare hostPath branches.
func nodeExec(t *testing.T, script string) string {
	t.Helper()
	out, err := exec.Command("docker", "exec", kindNode(), "sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("node exec %q: %v: %s", script, err, out)
	}
	return string(out)
}

// hostDir creates a fresh branch dir below hostRoot owned by the pool uid.
func hostDir(t *testing.T, name string, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(hostRoot, name)
	script := "rm -rf " + dir + " && mkdir -p " + dir
	for f, content := range files {
		script += " && printf %s '" + content + "' > " + filepath.Join(dir, f)
	}
	nodeExec(t, script+" && chown -R 1000:1000 "+dir)
	t.Cleanup(func() { _, _ = exec.Command("docker", "exec", kindNode(), "rm", "-rf", dir).CombinedOutput() })
	return dir
}
