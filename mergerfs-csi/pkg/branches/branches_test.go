package branches

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	raw := `[{"type":"hostPath","path":"/data/a"},{"type":"pvcRef","volumeName":"src"}]`
	bs, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(bs) != 2 {
		t.Fatalf("got %d branches", len(bs))
	}
	if bs[0].Type != TypeHostPath || bs[0].Path != "/data/a" {
		t.Fatalf("hostPath: %+v", bs[0])
	}
	if bs[1].Type != TypePVCRef || bs[1].VolumeName != "src" {
		t.Fatalf("pvcRef: %+v", bs[1])
	}
}

func TestParseRejects(t *testing.T) {
	cases := []string{
		"",
		`[]`,
		`[{"type":"hostPath","path":"rel"}]`,
		`[{"type":"pvcRef"}]`,
		`[{"type":"nope"}]`,
		`{not json}`,
	}
	for _, c := range cases {
		if _, err := Parse(c); err == nil {
			t.Fatalf("expected error for %q", c)
		}
	}
}

func TestResolve(t *testing.T) {
	r := Resolver{KubeletPodsPath: "/var/lib/kubelet/pods"}
	uid := "pod-uid-1"

	cases := []struct {
		b    Branch
		want string
	}{
		{Branch{Type: TypeHostPath, Path: "/data/a"}, "/data/a"},
		{Branch{Type: TypePVCRef, VolumeName: "src"}, "/var/lib/kubelet/pods/pod-uid-1/volumes/kubernetes.io~csi/src/mount"},
		{Branch{Type: TypeCSIRef, VolumeName: "vol"}, "/var/lib/kubelet/pods/pod-uid-1/volumes/kubernetes.io~csi/vol/mount"},
		{Branch{Type: TypeEmptyDirRef, VolumeName: "cache"}, "/var/lib/kubelet/pods/pod-uid-1/volumes/kubernetes.io~empty-dir/cache"},
	}
	for _, tc := range cases {
		got, err := r.Resolve(tc.b, uid)
		if err != nil {
			t.Fatalf("%+v: %v", tc.b, err)
		}
		if got != tc.want {
			t.Fatalf("%+v: got %q want %q", tc.b, got, tc.want)
		}
	}
}

func TestResolveEmptyDirVsCSI(t *testing.T) {
	r := Resolver{KubeletPodsPath: "/k"}
	uid := "u1"
	ed, err := r.Resolve(Branch{Type: TypeEmptyDirRef, VolumeName: "cache"}, uid)
	if err != nil {
		t.Fatal(err)
	}
	csi, err := r.Resolve(Branch{Type: TypeCSIRef, VolumeName: "cache"}, uid)
	if err != nil {
		t.Fatal(err)
	}
	if ed != "/k/u1/volumes/kubernetes.io~empty-dir/cache" {
		t.Fatalf("emptyDirRef want no /mount, got %q", ed)
	}
	if csi != "/k/u1/volumes/kubernetes.io~csi/cache/mount" {
		t.Fatalf("csiRef want /mount, got %q", csi)
	}
}

func TestResolveRejectsPathTraversal(t *testing.T) {
	r := Resolver{KubeletPodsPath: "/var/lib/kubelet/pods"}
	cases := []struct {
		name string
		b    Branch
		uid  string
	}{
		{"vol slash", Branch{Type: TypePVCRef, VolumeName: "a/b"}, "uid"},
		{"vol dotdot", Branch{Type: TypePVCRef, VolumeName: ".."}, "uid"},
		{"vol backslash", Branch{Type: TypeEmptyDirRef, VolumeName: `a\b`}, "uid"},
		{"uid slash", Branch{Type: TypeCSIRef, VolumeName: "v"}, "uid/../x"},
		{"uid empty", Branch{Type: TypePVCRef, VolumeName: "v"}, ""},
		{"vol empty via resolve", Branch{Type: TypePVCRef, VolumeName: "ok"}, ""},
	}
	for _, tc := range cases {
		if _, err := r.Resolve(tc.b, tc.uid); err == nil {
			t.Fatalf("%s: expected reject", tc.name)
		}
	}
	// Parse-time reject for volumeName traversal
	if _, err := Parse(`[{"type":"pvcRef","volumeName":"../escape"}]`); err == nil {
		t.Fatal("Parse should reject volumeName with ..")
	}
}

func TestResolveRejectsColonInPath(t *testing.T) {
	r := Resolver{KubeletPodsPath: "/var/lib/kubelet/pods"}
	_, err := r.Resolve(Branch{Type: TypeHostPath, Path: "/data/a:b"}, "")
	if err == nil {
		t.Fatal("expected reject colon in hostPath")
	}
}

func TestResolveRequiresPodUID(t *testing.T) {
	r := Resolver{KubeletPodsPath: "/var/lib/kubelet/pods"}
	_, err := r.Resolve(Branch{Type: TypePVCRef, VolumeName: "src"}, "")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestResolveAll(t *testing.T) {
	r := Resolver{KubeletPodsPath: "/k"}
	bs := []Branch{
		{Type: TypeHostPath, Path: "/a"},
		{Type: TypeEmptyDirRef, VolumeName: "e"},
	}
	got, err := r.ResolveAll(bs, "uid")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/a", "/k/uid/volumes/kubernetes.io~empty-dir/e"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("[%d] got %q want %q", i, got[i], want[i])
		}
	}
}

func TestWaitForDirs(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	if err := os.Mkdir(a, 0o755); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		done <- WaitForDirs(t.Context(), []string{a, b}, 2*time.Second)
	}()

	time.Sleep(50 * time.Millisecond)
	if err := os.Mkdir(b, 0o755); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}
}

func TestWaitForDirsTimeout(t *testing.T) {
	err := WaitForDirs(t.Context(), []string{"/no/such/dir/xyz"}, 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout")
	}
}
