package node

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/mount-utils"

	"github.com/BlackDark/csi-driver-mergerfs/internal/mountpod"
)

// Adoption results in csi_mergerfs_adoptions_total.
const (
	AdoptAdopted = "adopted"
	AdoptCleaned = "cleaned"
)

// nodePool is what the node shows for one pool id after a plugin restart.
type nodePool struct {
	id   string
	pod  *corev1.Pod // nil without a Mount Pod
	fuse bool        // fuse/ is mounted
}

func (n nodePool) podAlive() bool {
	return n.pod != nil && n.pod.DeletionTimestamp == nil &&
		n.pod.Status.Phase != corev1.PodFailed && n.pod.Status.Phase != corev1.PodSucceeded
}

// MountPodSelector selects the Mount Pods of node.
func MountPodSelector(node string) string {
	return labels.SelectorFromSet(labels.Set{
		mountpod.LabelName: mountpod.AppName, mountpod.LabelComponent: mountpod.Component, mountpod.LabelNode: node,
	}).String()
}

// scanNode joins Mount Pods with the fuse/ mounts under pools/<id>.
func scanNode(pods []corev1.Pod, infos []mount.MountInfo, kubeletDir string) []nodePool {
	byID := map[string]*nodePool{}
	get := func(id string) *nodePool {
		if byID[id] == nil {
			byID[id] = &nodePool{id: id}
		}
		return byID[id]
	}
	for i := range pods {
		if id := pods[i].Labels[mountpod.LabelID]; id != "" {
			get(id).pod = &pods[i]
		}
	}
	root := filepath.Dir(mountpod.PoolDir(kubeletDir, "x"))
	for _, mi := range infos {
		dir, base := filepath.Split(mi.MountPoint)
		if base == "fuse" && filepath.Dir(filepath.Clean(dir)) == root {
			get(filepath.Base(dir)).fuse = true
		}
	}
	out := make([]nodePool, 0, len(byID))
	for _, n := range byID {
		out = append(out, *n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// Adopt rebuilds the plugin's state after a restart, before it serves CSI calls: a
// pool whose fuse/ answers and whose Mount Pod is alive keeps running untouched.
// Everything else (dead fuse/, Pods without a mount) is removed and reported.
func (s *Server) Adopt(ctx context.Context) error {
	pods, err := s.deps.Kube.ListPods(ctx, MountPodSelector(s.cfg.NodeID))
	if err != nil {
		return fmt.Errorf("adopt: %w", err)
	}
	infos, err := s.deps.MountInfo()
	if err != nil {
		return fmt.Errorf("adopt: mount table: %w", err)
	}
	for _, n := range scanNode(pods, infos, s.cfg.Pod.KubeletDir) {
		if n.fuse && n.podAlive() && s.serving(n.id) {
			s.adoptPool(n)
			continue
		}
		s.cleanupPool(ctx, n)
	}
	return nil
}

func (s *Server) serving(id string) bool {
	_, err := s.deps.Statfs(mountpod.FuseDir(s.cfg.Pod.KubeletDir, id))
	return err == nil
}

func (s *Server) adoptPool(n nodePool) {
	p := s.newPool(n.id, n.pod.Annotations[mountpod.AnnotationTarget], n.pod.Annotations[mountpod.AnnotationVolumeID], workloadOf(n.pod))
	p.podUID.Store(n.pod.UID)
	if len(n.pod.Spec.Containers) > 0 {
		p.image = n.pod.Spec.Containers[0].Image
	}
	p.mountedOnce.Do(func() { close(p.mounted) })
	p.ready.Store(true)
	s.serve(p, true)
	s.setPool(n.id, p)
	go s.monitor(p)
	s.deps.Metrics.adoptions.WithLabelValues(AdoptAdopted).Inc()
	s.deps.Log.Info("adopted pool", "id", n.id, "pod", p.name, "target", p.target)
}

func workloadOf(pod *corev1.Pod) *corev1.ObjectReference {
	w := mountpod.DecodeWorkload(pod)
	if w == nil {
		return nil
	}
	return &corev1.ObjectReference{Kind: "Pod", APIVersion: "v1", Namespace: w.Namespace, Name: w.Name, UID: types.UID(w.UID)}
}

// cleanupPool removes what is left of a pool that cannot be adopted. Its target is
// dead until kubelet unpublishes it; its consumer gets MountLost (and is healed).
func (s *Server) cleanupPool(ctx context.Context, n nodePool) {
	kd := s.cfg.Pod.KubeletDir
	s.deps.Metrics.adoptions.WithLabelValues(AdoptCleaned).Inc()
	s.deps.Log.Warn("cleaning up pool left by a plugin restart", "id", n.id, "fuseMounted", n.fuse, "pod", n.pod != nil)
	if n.fuse {
		if err := s.deps.Fuse.Unmount(mountpod.FuseDir(kd, n.id), true); err != nil {
			s.deps.Log.Warn("unmount pool", "id", n.id, "err", err)
		}
	}
	if n.pod != nil {
		if err := s.deps.Kube.DeletePod(ctx, n.pod.Name); err != nil {
			s.deps.Log.Warn("delete mount pod", "id", n.id, "err", err)
		}
	}
	_ = os.Remove(mountpod.FuseDir(kd, n.id))
	_ = os.Remove(mountpod.SockPath(kd, n.id))
	_ = os.Remove(mountpod.PoolDir(kd, n.id))
	if !n.fuse || n.pod == nil {
		return // never served (publish interrupted) or no record of its consumer
	}
	s.deps.Metrics.lost.Inc()
	target := n.pod.Annotations[mountpod.AnnotationTarget]
	msg := fmt.Sprintf("Mount Pod %s was lost while the node plugin restarted; restart the pod to remount the volume", n.pod.Name)
	if target != "" {
		s.markDead(target, msg)
	}
	ref := workloadOf(n.pod)
	if ref == nil {
		return
	}
	s.emit([]corev1.Event{warningEvent(*ref, ReasonMountLost, msg, s.cfg.NodeID, metav1.NewTime(s.deps.Clock.Now()))})
	s.startHeal([]corev1.ObjectReference{*ref})
}
