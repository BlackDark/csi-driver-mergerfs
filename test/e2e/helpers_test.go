//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/utils/ptr"
)

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	return c
}

func poll(ctx context.Context, t *testing.T, what string, f func() (bool, error)) {
	t.Helper()
	err := wait.PollUntilContextTimeout(ctx, time.Second, 3*time.Minute, true, func(context.Context) (bool, error) {
		return f()
	})
	if err != nil {
		t.Fatalf("waiting for %s: %v", what, err)
	}
}

// poolVolume is an inline volume of this driver.
func poolVolume(name, branches, options string) corev1.Volume {
	attrs := map[string]string{"branches": branches}
	if options != "" {
		attrs["mergerfsOptions"] = options
	}
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{CSI: &corev1.CSIVolumeSource{
		Driver: driverName, VolumeAttributes: attrs,
	}}}
}

func emptyDir(name string) corev1.Volume {
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}
}

// podSpec runs busybox as the pool uid with every volume mounted at /<name>.
func podSpec(vols ...corev1.Volume) corev1.PodSpec {
	mounts := make([]corev1.VolumeMount, len(vols))
	for i, v := range vols {
		mounts[i] = corev1.VolumeMount{Name: v.Name, MountPath: "/" + v.Name}
	}
	return corev1.PodSpec{
		TerminationGracePeriodSeconds: ptr.To(int64(1)),
		SecurityContext:               &corev1.PodSecurityContext{RunAsUser: ptr.To(int64(poolUID)), RunAsGroup: ptr.To(int64(poolUID))},
		Containers: []corev1.Container{{
			Name: "app", Image: busybox, Command: []string{"sleep", "infinity"}, VolumeMounts: mounts,
		}},
		Volumes: vols,
	}
}

func createPod(ctx context.Context, t *testing.T, name string, spec corev1.PodSpec) {
	t.Helper()
	deletePod(ctx, t, name)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: appNS}, Spec: spec}
	if _, err := cs.CoreV1().Pods(appNS).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { deletePod(context.Background(), t, name) })
}

func deletePod(ctx context.Context, t *testing.T, name string) {
	t.Helper()
	_ = cs.CoreV1().Pods(appNS).Delete(ctx, name, metav1.DeleteOptions{})
	poll(ctx, t, "pod "+name+" deleted", func() (bool, error) {
		_, err := cs.CoreV1().Pods(appNS).Get(ctx, name, metav1.GetOptions{})
		return apierrors.IsNotFound(err), nil
	})
}

func waitRunning(ctx context.Context, t *testing.T, name string) {
	t.Helper()
	poll(ctx, t, "pod "+name+" running", func() (bool, error) {
		p, err := cs.CoreV1().Pods(appNS).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return p.Status.Phase == corev1.PodRunning, nil
	})
}

// waitEvent waits for an Event on the named object whose reason and message match.
func waitEvent(ctx context.Context, t *testing.T, object, reason, msgPart string) {
	t.Helper()
	poll(ctx, t, reason+" event on "+object, func() (bool, error) {
		l, err := cs.CoreV1().Events(appNS).List(ctx, metav1.ListOptions{FieldSelector: "involvedObject.name=" + object})
		if err != nil {
			return false, err
		}
		for _, e := range l.Items {
			if e.Reason == reason && strings.Contains(e.Message, msgPart) {
				return true, nil
			}
		}
		return false, nil
	})
}

// sh runs a script in the pod's app container and returns stdout; stderr is in the error.
func sh(ctx context.Context, t *testing.T, pod, script string) (string, error) {
	t.Helper()
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	req := cs.CoreV1().RESTClient().Post().Resource("pods").Namespace(appNS).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{Container: "app", Command: []string{"sh", "-c", script}, Stdout: true, Stderr: true}, scheme.ParameterCodec)
	exec, err := remotecommand.NewSPDYExecutor(cfg, "POST", req.URL())
	if err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: &out, Stderr: &errb})
	if err != nil {
		return out.String(), &execError{err: err, stderr: errb.String()}
	}
	return out.String(), nil
}

type execError struct {
	err    error
	stderr string
}

func (e *execError) Error() string { return e.err.Error() + ": " + e.stderr }

func mustSh(ctx context.Context, t *testing.T, pod, script string) string {
	t.Helper()
	out, err := sh(ctx, t, pod, script)
	if err != nil {
		t.Fatalf("%s: %v (stdout %q)", script, err, out)
	}
	return out
}

var _ = rest.Config{}
