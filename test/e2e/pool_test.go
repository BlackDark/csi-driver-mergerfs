//go:build e2e

package e2e

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

// TestPoolEmptyDirAndHostPath: an emptyDir RW branch over an allowed hostPath NC branch
// (the "local + remote" layout). Creates land on the emptyDir, reads see both, renames work.
func TestPoolEmptyDirAndHostPath(t *testing.T) {
	t.Parallel()
	c := ctx(t)
	remote := hostDir(t, "pool-remote", map[string]string{"remote.txt": "remote"})
	branches := `[{"type":"emptyDirRef","volumeName":"local"},{"type":"hostPath","path":"` + remote + `","mode":"NC"}]`
	createPod(c, t, "pool", podSpec(emptyDir("local"), poolVolume("media", branches, "category.create=ff,cache.files=off")))
	waitRunning(c, t, "pool")
	out := mustSh(c, t, "pool", `cat /media/remote.txt; echo new > /media/new.txt; mv /media/new.txt /media/moved.txt;
		ls /local; mount | grep ' /media ' | cut -d' ' -f5`)
	if out != "remotemoved.txt\nfuse.mergerfs\n" {
		t.Errorf("got %q", out)
	}
	if got := nodeExec(t, "ls "+remote); got != "remote.txt\n" {
		t.Errorf("NC branch got a create: %q", got)
	}
}

// TestBranchModes: RO refuses actions on its files; NC takes no creates but allows them.
func TestBranchModes(t *testing.T) {
	t.Parallel()
	c := ctx(t)
	ro := hostDir(t, "modes-ro", map[string]string{"ro.txt": "ro"})
	branches := `[{"type":"emptyDirRef","volumeName":"rw"},{"type":"hostPath","path":"` + ro + `","mode":"RO"},` +
		`{"type":"emptyDirRef","volumeName":"nc","mode":"NC"}]`
	createPod(c, t, "modes", podSpec(emptyDir("rw"), emptyDir("nc"), poolVolume("u", branches, "category.create=mfs")))
	waitRunning(c, t, "modes")
	if _, err := sh(c, t, "modes", "rm /u/ro.txt"); err == nil || !strings.Contains(err.Error(), "Read-only") {
		t.Errorf("unlink on RO branch: %v", err)
	}
	out := mustSh(c, t, "modes", "echo a > /u/a.txt; ls /rw; ls /nc | wc -l")
	if out != "a.txt\n0\n" {
		t.Errorf("creates: %q", out)
	}
}

func TestRejectOutsideAllowlist(t *testing.T) {
	t.Parallel()
	c := ctx(t)
	createPod(c, t, "reject-host", podSpec(poolVolume("u", `[{"type":"hostPath","path":"/etc"}]`, "")))
	waitEvent(c, t, "reject-host", "FailedMount", `hostPath "/etc" is not under an allowed host path`)
}

func TestRejectUnusedVolume(t *testing.T) {
	t.Parallel()
	c := ctx(t)
	spec := podSpec(poolVolume("u", `[{"type":"emptyDirRef","volumeName":"unused"}]`, ""))
	spec.Volumes = append(spec.Volumes, emptyDir("unused")) // in volumes, not in volumeMounts
	createPod(c, t, "reject-unused", spec)
	waitEvent(c, t, "reject-unused", "FailedMount", `volume "unused" is not in any container's volumeMounts`)
}

func TestRejectOption(t *testing.T) {
	t.Parallel()
	c := ctx(t)
	createPod(c, t, "reject-opt", podSpec(emptyDir("a"), poolVolume("u", `[{"type":"emptyDirRef","volumeName":"a"}]`, "log.file=/tmp/x")))
	waitEvent(c, t, "reject-opt", "FailedMount", `"log.file" is not allowed`)
}

// TestDynamicVolume: a StorageClass whose parameters become the pool's attributes.
func TestDynamicVolume(t *testing.T) {
	t.Parallel()
	c := ctx(t)
	dir := hostDir(t, "dynamic", map[string]string{"seed.txt": "seed"})
	sc := &storageClass{name: "e2e-mergerfs", branches: `[{"type":"hostPath","path":"` + dir + `"}]`}
	sc.create(c, t)
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "dynamic", Namespace: appNS},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: ptr.To(sc.name),
			Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: oneGi}},
		},
	}
	_ = cs.CoreV1().PersistentVolumeClaims(appNS).Delete(c, pvc.Name, metav1.DeleteOptions{})
	if _, err := cs.CoreV1().PersistentVolumeClaims(appNS).Create(c, pvc, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.CoreV1().PersistentVolumeClaims(appNS).Delete(c, pvc.Name, metav1.DeleteOptions{}) })
	vol := corev1.Volume{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "dynamic"}}}
	createPod(c, t, "dynamic", podSpec(vol))
	waitRunning(c, t, "dynamic")
	if out := mustSh(c, t, "dynamic", "cat /data/seed.txt; echo x > /data/w.txt"); out != "seed" {
		t.Errorf("got %q", out)
	}
	if got := nodeExec(t, "ls "+dir); got != "seed.txt\nw.txt\n" {
		t.Errorf("host dir: %q", got)
	}
}
