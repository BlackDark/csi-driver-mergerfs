package node

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/BlackDark/csi-driver-mergerfs/internal/mountpod"
)

// Event reasons.
const (
	// ReasonMountLost: a Mount Pod stopped without the plugin asking.
	ReasonMountLost = "MountLost"
	// ReasonUnrestrictedOptions: a pool runs with allowAllOptions.
	ReasonUnrestrictedOptions = "UnrestrictedOptions"
)

// MountPodChanged is fed by the Mount Pod watch. A Mount Pod that fails, finishes or
// is deleted while the plugin did not stop it counts as lost.
func (s *Server) MountPodChanged(pod *corev1.Pod, deleted bool) {
	p := s.getPool(pod.Labels[mountpod.LabelID])
	if p == nil || p.uid() != pod.UID || p.releasing.Load() {
		return
	}
	ph := pod.Status.Phase
	if deleted || pod.DeletionTimestamp != nil || ph == corev1.PodFailed || ph == corev1.PodSucceeded {
		p.signalLost()
	}
}

// monitor waits for a published pool to lose its Mount Pod.
func (s *Server) monitor(p *pool) {
	select {
	case <-p.lost:
	case <-p.served:
		return
	}
	if p.releasing.Load() {
		return
	}
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		s.poolLost(p)
	}()
}

// poolLost reports the dead target and removes the Mount Pod. A FUSE connection
// cannot be resumed, and the consumer's containers hold the old bind, so the pool is
// not recreated: the consumer pod must restart, which --heal-workloads automates.
func (s *Server) poolLost(p *pool) {
	unlock := s.locks.lock(p.id)
	defer unlock()
	if s.getPool(p.id) != p || p.releasing.Load() {
		return
	}
	msg := fmt.Sprintf("Mount Pod %s of volume %s is lost. The volume is unreachable in this pod "+
		"(transport endpoint is not connected); restart the pod to remount it", p.name, p.volumeID)
	s.deps.Log.Warn("mount pod lost", "id", p.id, "pod", p.name, "target", p.target)
	s.markDead(p.target, msg)
	s.deps.Metrics.lost.Inc()
	s.emit(s.lostEvents(p, msg))
	p.releasing.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), kubeCallTimeout)
	defer cancel()
	if err := s.deps.Kube.DeletePod(ctx, p.name); err != nil {
		s.deps.Log.Warn("delete lost mount pod", "id", p.id, "err", err)
	}
	if p.workload != nil {
		s.startHeal([]corev1.ObjectReference{*p.workload})
	}
}

func (s *Server) lostEvents(p *pool, msg string) []corev1.Event {
	refs := []corev1.ObjectReference{{Kind: "Pod", APIVersion: "v1", Namespace: s.cfg.Pod.Namespace, Name: p.name, UID: p.uid()}}
	if p.workload != nil {
		refs = append(refs, *p.workload)
	}
	now := metav1.NewTime(s.deps.Clock.Now())
	out := make([]corev1.Event, 0, len(refs))
	for _, r := range refs {
		out = append(out, warningEvent(r, ReasonMountLost, msg, s.cfg.NodeID, now))
	}
	return out
}

func (s *Server) unrestrictedEvent(ref corev1.ObjectReference, volumeID string) corev1.Event {
	msg := fmt.Sprintf("Volume %s runs mergerfs with allowAllOptions: its mergerfsOptions skip the driver's allowlist", volumeID)
	return warningEvent(ref, ReasonUnrestrictedOptions, msg, s.cfg.NodeID, metav1.NewTime(s.deps.Clock.Now()))
}

func warningEvent(ref corev1.ObjectReference, reason, msg, node string, now metav1.Time) corev1.Event {
	return newEvent(ref, corev1.EventTypeWarning, reason, msg, node, now)
}

func newEvent(ref corev1.ObjectReference, typ, reason, msg, node string, now metav1.Time) corev1.Event {
	return corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{GenerateName: ref.Name + ".", Namespace: ref.Namespace},
		InvolvedObject: ref,
		Reason:         reason,
		Message:        msg,
		Type:           typ,
		Source:         corev1.EventSource{Component: "csi-driver-mergerfs-node", Host: node},
		FirstTimestamp: now,
		LastTimestamp:  now,
		Count:          1,
	}
}

func (s *Server) emit(events []corev1.Event) {
	ctx, cancel := context.WithTimeout(context.Background(), kubeCallTimeout)
	defer cancel()
	for _, e := range events {
		if err := s.deps.Kube.CreateEvent(ctx, &e); err != nil {
			s.deps.Log.Warn("emit event", "reason", e.Reason, "object", e.InvolvedObject.Name, "err", err)
		}
	}
}
