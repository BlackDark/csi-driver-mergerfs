package node

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// Kube is the node plugin's API surface: Mount Pods in the driver namespace,
// consumer pods, PVCs and PVs for branch resolution, Events anywhere.
type Kube interface {
	// CreatePod returns the existing pod when one with the same name exists.
	CreatePod(ctx context.Context, pod *corev1.Pod) (*corev1.Pod, error)
	// DeletePod treats NotFound as success.
	DeletePod(ctx context.Context, name string) error
	// ListPods lists Mount Pods matching the label selector.
	ListPods(ctx context.Context, selector string) ([]corev1.Pod, error)
	// GetPod returns nil, nil when the Mount Pod does not exist.
	GetPod(ctx context.Context, name string) (*corev1.Pod, error)
	// CreateEvent records e in e.Namespace, which may be a workload namespace.
	CreateEvent(ctx context.Context, e *corev1.Event) error
	// GetWorkloadPod reads a pod in any namespace; nil, nil when it does not exist.
	GetWorkloadPod(ctx context.Context, ns, name string) (*corev1.Pod, error)
	// GetClaimPV returns the PV bound to PVC ns/claim; an error while unbound.
	GetClaimPV(ctx context.Context, ns, claim string) (*corev1.PersistentVolume, error)
	// EvictPod evicts the pod with uid through the Eviction API, honouring PDBs.
	// ErrEvictionBlocked when a PDB refuses; ErrPodReplaced when gone or uid differs.
	EvictPod(ctx context.Context, ns, name string, uid types.UID) error
}

type clientKube struct {
	cs kubernetes.Interface
	ns string
}

// NewKube wraps a clientset; Mount Pods live in namespace ns.
func NewKube(cs kubernetes.Interface, ns string) Kube { return &clientKube{cs: cs, ns: ns} }

func (k *clientKube) CreatePod(ctx context.Context, pod *corev1.Pod) (*corev1.Pod, error) {
	out, err := k.cs.CoreV1().Pods(k.ns).Create(ctx, pod, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		out, err = k.cs.CoreV1().Pods(k.ns).Get(ctx, pod.Name, metav1.GetOptions{})
	}
	if err != nil {
		return nil, fmt.Errorf("create pod %s: %w", pod.Name, err)
	}
	return out, nil
}

func (k *clientKube) DeletePod(ctx context.Context, name string) error {
	err := k.cs.CoreV1().Pods(k.ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete pod %s: %w", name, err)
	}
	return nil
}

func (k *clientKube) ListPods(ctx context.Context, selector string) ([]corev1.Pod, error) {
	l, err := k.cs.CoreV1().Pods(k.ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	return l.Items, nil
}

func (k *clientKube) GetPod(ctx context.Context, name string) (*corev1.Pod, error) {
	return k.GetWorkloadPod(ctx, k.ns, name)
}

func (k *clientKube) CreateEvent(ctx context.Context, e *corev1.Event) error {
	if _, err := k.cs.CoreV1().Events(e.Namespace).Create(ctx, e, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create event %s: %w", e.Reason, err)
	}
	return nil
}

func (k *clientKube) GetWorkloadPod(ctx context.Context, ns, name string) (*corev1.Pod, error) {
	p, err := k.cs.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get pod %s/%s: %w", ns, name, err)
	}
	return p, nil
}

func (k *clientKube) GetClaimPV(ctx context.Context, ns, claim string) (*corev1.PersistentVolume, error) {
	pvc, err := k.cs.CoreV1().PersistentVolumeClaims(ns).Get(ctx, claim, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get pvc: %w", err)
	}
	if pvc.Spec.VolumeName == "" {
		return nil, errors.New("pvc not bound")
	}
	pv, err := k.cs.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get pv: %w", err)
	}
	return pv, nil
}

func (k *clientKube) EvictPod(ctx context.Context, ns, name string, uid types.UID) error {
	ev := &policyv1.Eviction{
		ObjectMeta:    metav1.ObjectMeta{Namespace: ns, Name: name},
		DeleteOptions: &metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}},
	}
	err := k.cs.PolicyV1().Evictions(ns).Evict(ctx, ev)
	switch {
	case err == nil:
		return nil
	case apierrors.IsTooManyRequests(err):
		return fmt.Errorf("evict pod %s/%s: %w: %w", ns, name, ErrEvictionBlocked, err)
	case apierrors.IsNotFound(err) || apierrors.IsConflict(err):
		return fmt.Errorf("evict pod %s/%s: %w", ns, name, ErrPodReplaced)
	}
	return fmt.Errorf("evict pod %s/%s: %w", ns, name, err)
}
