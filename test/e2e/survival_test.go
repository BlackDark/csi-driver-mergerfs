//go:build e2e

package e2e

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

var oneGi = resource.MustParse("1Gi")

type storageClass struct{ name, branches string }

func (s *storageClass) create(ctx context.Context, t *testing.T) {
	t.Helper()
	sc := &storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: s.name},
		Provisioner: driverName,
		Parameters:  map[string]string{"branches": s.branches},
	}
	_ = cs.StorageV1().StorageClasses().Delete(ctx, s.name, metav1.DeleteOptions{})
	if _, err := cs.StorageV1().StorageClasses().Create(ctx, sc, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cs.StorageV1().StorageClasses().Delete(context.Background(), s.name, metav1.DeleteOptions{})
	})
}

func mountPods(ctx context.Context, t *testing.T) []corev1.Pod {
	t.Helper()
	l, err := cs.CoreV1().Pods(driverNS()).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/component=pool"})
	if err != nil {
		t.Fatal(err)
	}
	return l.Items
}

// mountPodOf finds the Mount Pod serving the consumer pod.
func mountPodOf(ctx context.Context, t *testing.T, consumer string) string {
	t.Helper()
	var name string
	poll(ctx, t, "mount pod of "+consumer, func() (bool, error) {
		for _, p := range mountPods(ctx, t) {
			if strings.Contains(p.Annotations["mergerfs.csi.blackdark.github.io/workload"], `"name":"`+consumer+`"`) {
				name = p.Name
				return true, nil
			}
		}
		return false, nil
	})
	return name
}

// TestNodePluginRestartKeepsIO: deleting the node plugin pod (as a DaemonSet rollout
// does) leaves the pool serving, and the restarted plugin adopts it. Serial.
func TestNodePluginRestartKeepsIO(t *testing.T) {
	c := ctx(t)
	dir := hostDir(t, "restart", nil)
	createPod(c, t, "restart", podSpec(poolVolume("u", `[{"type":"hostPath","path":"`+dir+`"}]`, "")))
	waitRunning(c, t, "restart")
	mustSh(c, t, "restart", "nohup sh -c 'while :; do date +%s >> /u/beat || echo FAIL >> /tmp/fail; sleep 0.2; done' >/dev/null 2>&1 &")
	pods, err := cs.CoreV1().Pods(driverNS()).List(c, metav1.ListOptions{LabelSelector: "app.kubernetes.io/component=node"})
	if err != nil || len(pods.Items) != 1 {
		t.Fatalf("node pods: %v %v", pods, err)
	}
	old := pods.Items[0].Name
	if err := cs.CoreV1().Pods(driverNS()).Delete(c, old, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	poll(c, t, "node plugin back", func() (bool, error) {
		l, err := cs.CoreV1().Pods(driverNS()).List(c, metav1.ListOptions{LabelSelector: "app.kubernetes.io/component=node"})
		if err != nil || len(l.Items) != 1 || l.Items[0].Name == old {
			return false, err
		}
		for _, s := range l.Items[0].Status.ContainerStatuses {
			if !s.Ready {
				return false, nil
			}
		}
		return true, nil
	})
	before := lines(t, mustSh(c, t, "restart", "wc -l < /u/beat"))
	time.Sleep(3 * time.Second)
	after := lines(t, mustSh(c, t, "restart", "wc -l < /u/beat"))
	if after <= before {
		t.Errorf("writes stopped after the restart: %d -> %d", before, after)
	}
	if out, _ := sh(c, t, "restart", "cat /tmp/fail 2>/dev/null"); out != "" {
		t.Errorf("writes failed during the restart: %q", out)
	}
	logs, err := cs.CoreV1().Pods(driverNS()).GetLogs(nodePod(c, t), &corev1.PodLogOptions{Container: "mergerfs"}).DoRaw(c)
	if err != nil || !strings.Contains(string(logs), "adopted pool") {
		t.Errorf("no adoption logged: %v", err)
	}
}

func nodePod(ctx context.Context, t *testing.T) string {
	t.Helper()
	l, err := cs.CoreV1().Pods(driverNS()).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/component=node"})
	if err != nil || len(l.Items) == 0 {
		t.Fatalf("node pod: %v", err)
	}
	return l.Items[0].Name
}

func lines(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		t.Fatalf("not a count: %q", s)
	}
	return n
}

// TestMountPodLostHeals: killing a Mount Pod reports MountLost on the consumer, and
// healWorkloads evicts it so its Deployment brings up a pod with a fresh pool. Serial.
func TestMountPodLostHeals(t *testing.T) {
	c := ctx(t)
	dir := hostDir(t, "heal", map[string]string{"x.txt": "x"})
	labels := map[string]string{"app": "heal"}
	spec := podSpec(poolVolume("u", `[{"type":"hostPath","path":"`+dir+`"}]`, ""))
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "heal", Namespace: appNS},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: spec},
		},
	}
	_ = cs.AppsV1().Deployments(appNS).Delete(c, "heal", metav1.DeleteOptions{})
	if _, err := cs.AppsV1().Deployments(appNS).Create(c, dep, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cs.AppsV1().Deployments(appNS).Delete(context.Background(), "heal", metav1.DeleteOptions{})
	})
	first := runningPod(c, t, "app=heal", "")
	mp := mountPodOf(c, t, first)
	if err := cs.CoreV1().Pods(driverNS()).Delete(c, mp, metav1.DeleteOptions{GracePeriodSeconds: ptr.To(int64(0))}); err != nil {
		t.Fatal(err)
	}
	waitEvent(c, t, first, "MountLost", "is lost")
	waitEvent(c, t, first, "WorkloadHealed", "Evicted")
	second := runningPod(c, t, "app=heal", first)
	if out := mustSh(c, t, second, "cat /u/x.txt"); out != "x" {
		t.Errorf("replacement reads %q", out)
	}
}

// runningPod returns a running pod matching sel other than not.
func runningPod(ctx context.Context, t *testing.T, sel, not string) string {
	t.Helper()
	var name string
	poll(ctx, t, "running pod "+sel, func() (bool, error) {
		l, err := cs.CoreV1().Pods(appNS).List(ctx, metav1.ListOptions{LabelSelector: sel})
		if err != nil {
			return false, err
		}
		for _, p := range l.Items {
			if p.Name != not && p.Status.Phase == corev1.PodRunning && p.DeletionTimestamp == nil {
				name = p.Name
				return true, nil
			}
		}
		return false, nil
	})
	return name
}
