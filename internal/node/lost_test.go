package node

import (
	"context"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/mount-utils"

	"github.com/BlackDark/csi-driver-mergerfs/internal/mountpod"
)

func (h *harness) publish() *pool {
	h.t.Helper()
	h.consumer()
	h.readyBranches()
	if _, err := h.srv.NodePublishVolume(context.Background(), publishReq(h.target(), h.mediaLayout())); err != nil {
		h.t.Fatal(err)
	}
	return h.srv.getPool(mountpod.ID("vol-1", h.target()))
}

func reasons(evs []corev1.Event) string {
	var out []string
	for _, e := range evs {
		out = append(out, e.Reason+"@"+e.InvolvedObject.Name)
	}
	return strings.Join(out, " ")
}

func TestMountPodLost(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.HealWorkloads = true })
	p := h.publish()
	pod := h.mountPods()[0]
	pod.Status.Phase = corev1.PodFailed
	h.srv.MountPodChanged(&pod, false)
	eventually(t, "mount pod deleted", func() bool { return len(h.mountPods()) == 0 })
	h.srv.Wait()
	got := reasons(h.events())
	for _, want := range []string{ReasonMountLost + "@" + p.name, ReasonMountLost + "@jellyfin-0", ReasonWorkloadHealed + "@jellyfin-0"} {
		if !strings.Contains(got, want) {
			t.Errorf("events %q lack %q", got, want)
		}
	}
	resp, err := h.srv.NodeGetVolumeHealth(context.Background(), &csi.NodeGetVolumeHealthRequest{VolumeId: "vol-1", VolumePublishPath: h.target()})
	if err != nil || len(resp.VolumeHealth.HealthStatuses) != 1 || resp.VolumeHealth.HealthStatuses[0].Reason != ReasonMountPodLost {
		t.Errorf("health %+v %v", resp, err)
	}
	if v := testutil.ToFloat64(h.srv.deps.Metrics.lost); v != 1 {
		t.Errorf("lost %v", v)
	}
	// Republishing the dead target replaces the bind and the pool.
	h.mu.Lock()
	h.statErr = map[string]error{}
	h.mu.Unlock()
	if _, err := h.srv.NodePublishVolume(context.Background(), publishReq(h.target(), h.mediaLayout())); err != nil {
		t.Fatal(err)
	}
	if len(h.mountPods()) != 1 {
		t.Error("pool not recreated")
	}
}

func TestReleaseIsNotLost(t *testing.T) {
	h := newHarness(t)
	h.publish()
	pod := h.mountPods()[0]
	if _, err := h.srv.NodeUnpublishVolume(context.Background(), &csi.NodeUnpublishVolumeRequest{VolumeId: "vol-1", TargetPath: h.target()}); err != nil {
		t.Fatal(err)
	}
	h.srv.MountPodChanged(&pod, true)
	h.srv.Wait()
	if len(h.events()) != 0 {
		t.Errorf("events %s", reasons(h.events()))
	}
}

func TestHealthENOTCONN(t *testing.T) {
	h := newHarness(t)
	h.publish()
	h.setStatErr(h.target(), syscall.ENOTCONN)
	resp, err := h.srv.NodeGetVolumeHealth(context.Background(), &csi.NodeGetVolumeHealthRequest{VolumeId: "vol-1", VolumePublishPath: h.target()})
	if err != nil || len(resp.VolumeHealth.HealthStatuses) != 1 {
		t.Errorf("health %+v %v", resp, err)
	}
	if _, err := h.srv.NodeGetVolumeStats(context.Background(), &csi.NodeGetVolumeStatsRequest{VolumeId: "vol-1", VolumePath: h.target()}); code(err) != 14 {
		t.Errorf("stats: %v", err)
	}
}

func TestAdopt(t *testing.T) {
	h := newHarness(t)
	h.publish()
	live := h.mountPods()[0]
	id := live.Labels[mountpod.LabelID]
	// A second Mount Pod whose mergerfs died while the plugin was down.
	dead := live.DeepCopy()
	dead.Name, dead.UID = mountpod.Name("deadbeefdeadbeef"), "uid-dead"
	dead.Labels[mountpod.LabelID] = "deadbeefdeadbeef"
	dead.Annotations[mountpod.AnnotationTarget] = "/t/dead"
	dead.Status.Phase = corev1.PodFailed
	if _, err := h.cs.CoreV1().Pods("drv").Create(context.Background(), dead, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	h.addMount(mountpod.FuseDir(h.kd, id))
	h.addMount(mountpod.FuseDir(h.kd, "deadbeefdeadbeef"))
	h.fuse.mounted[mountpod.FuseDir(h.kd, "deadbeefdeadbeef")] = h.fuse.mounted[mountpod.FuseDir(h.kd, id)]

	restarted := New(h.srv.cfg, h.srv.deps)
	if err := restarted.Adopt(context.Background()); err != nil {
		t.Fatal(err)
	}
	restarted.Wait()
	p := restarted.getPool(id)
	if p == nil || p.target != h.target() || p.workload == nil || p.workload.Name != "jellyfin-0" || !p.ready.Load() {
		t.Fatalf("adopted %+v", p)
	}
	if restarted.getPool("deadbeefdeadbeef") != nil {
		t.Error("dead pool adopted")
	}
	if _, ok := restarted.deadReason("/t/dead"); !ok {
		t.Error("dead target not recorded")
	}
	if names := podNames(h.mountPods()); names != live.Name {
		t.Errorf("mount pods after adopt: %s", names)
	}
	if !strings.Contains(reasons(h.events()), ReasonMountLost+"@jellyfin-0") {
		t.Errorf("events %s", reasons(h.events()))
	}
	// The adopted pool is still watched.
	pod := h.mountPods()[0]
	pod.Status.Phase = corev1.PodSucceeded
	restarted.MountPodChanged(&pod, false)
	eventually(t, "adopted pool lost", func() bool { return len(h.mountPods()) == 0 })
	restarted.Wait()
}

func podNames(ps []corev1.Pod) string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Name)
	}
	return strings.Join(out, ",")
}

func TestScanNodeIgnoresOtherMounts(t *testing.T) {
	infos := []mount.MountInfo{{MountPoint: "/k/plugins/mergerfs.csi.k8s.io/pools/a/fuse"}, {MountPoint: "/k/pods/u/volumes/x/fuse"}}
	got := scanNode(nil, infos, "/k")
	if len(got) != 1 || got[0].id != "a" || !got[0].fuse {
		t.Errorf("got %+v", got)
	}
}

func TestCollector(t *testing.T) {
	h := newHarness(t)
	h.publish()
	reg := prometheus.NewRegistry()
	reg.MustRegister(h.srv.Collector())
	want := `
# HELP csi_mergerfs_pools Pools on this node by state.
# TYPE csi_mergerfs_pools gauge
csi_mergerfs_pools{state="lost"} 0
csi_mergerfs_pools{state="ready"} 1
csi_mergerfs_pools{state="starting"} 0
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "csi_mergerfs_pools"); err != nil {
		t.Error(err)
	}
}

func TestHealDecision(t *testing.T) {
	yes := true
	owned := func(uid string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "web-0", UID: types.UID(uid),
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "web", Controller: &yes}}}}
	}
	ref := corev1.ObjectReference{Namespace: "app", Name: "web-0", UID: "u1"}
	bare := owned("u1")
	bare.OwnerReferences = nil
	deleting := owned("u1")
	deleting.DeletionTimestamp = &metav1.Time{}
	for name, c := range map[string]struct {
		pod  *corev1.Pod
		want healAction
	}{
		"owned": {owned("u1"), healEvict}, "gone": {nil, healGone}, "deleting": {deleting, healGone},
		"recreated": {owned("u2"), healGone}, "bare": {bare, healBare},
	} {
		if got := healDecision(ref, c.pod); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
	if nextBackoff(time.Second) != 2*time.Second || nextBackoff(20*time.Second) != healMaxBackoff {
		t.Error("backoff")
	}
}

func TestHealEvictsWithUIDPrecondition(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.HealWorkloads = true })
	h.publish()
	pod := h.mountPods()[0]
	h.srv.MountPodChanged(&pod, true)
	eventually(t, "eviction", func() bool { h.mu.Lock(); defer h.mu.Unlock(); return len(h.evictions) == 1 })
	h.srv.Wait()
	h.mu.Lock()
	defer h.mu.Unlock()
	if e := h.evictions[0]; e.Name != "jellyfin-0" || *e.DeleteOptions.Preconditions.UID != "u1" {
		t.Errorf("eviction %+v", e)
	}
}
