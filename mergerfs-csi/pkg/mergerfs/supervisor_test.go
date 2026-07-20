package mergerfs

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

func TestSanitizeOptions(t *testing.T) {
	ok, err := SanitizeOptions("category.create=mfs,cache.files=partial")
	if err != nil || ok == "" {
		t.Fatalf("got %q err=%v", ok, err)
	}
	bad := []string{"$(rm -rf /)", "a;b", "x|y", "`id`", "a b", "a\nb"}
	for _, b := range bad {
		if _, err := SanitizeOptions(b); err == nil {
			t.Fatalf("expected reject %q", b)
		}
	}
	empty, err := SanitizeOptions("  ")
	if err != nil || empty != "" {
		t.Fatalf("empty: %q %v", empty, err)
	}
}

func TestBuildArgs(t *testing.T) {
	args, err := BuildArgs("category.create=mfs", []string{"/a", "/b"}, "/mnt/t")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-f", "-o", "category.create=mfs", "/a:/b", "/mnt/t"}
	if len(args) != len(want) {
		t.Fatalf("got %#v", args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("[%d] got %q want %q", i, args[i], want[i])
		}
	}
}

func TestBuildArgsRejectsUnsafe(t *testing.T) {
	_, err := BuildArgs("a;b", []string{"/a"}, "/t")
	if err == nil {
		t.Fatal("expected error")
	}
}

type fakeProc struct {
	pid  int
	dead bool
}

func (p *fakeProc) PID() int { return p.pid }
func (p *fakeProc) Wait() error {
	p.dead = true
	return nil
}
func (p *fakeProc) Kill() error {
	p.dead = true
	return nil
}

type fakeRunner struct {
	mu    sync.Mutex
	calls [][]string
	next  int
}

func (r *fakeRunner) Start(_ context.Context, bin string, args []string) (Process, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string{bin}, args...))
	r.next++
	return &fakeProc{pid: 1000 + r.next}, nil
}

type fakeFS struct {
	mu       sync.Mutex
	dirs     map[string]bool
	mounted  map[string]bool
	mkdirErr error
}

func newFakeFS() *fakeFS {
	return &fakeFS{dirs: map[string]bool{}, mounted: map[string]bool{}}
}

func (f *fakeFS) MkdirAll(path string, _ os.FileMode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mkdirErr != nil {
		return f.mkdirErr
	}
	f.dirs[path] = true
	return nil
}

func (f *fakeFS) IsMounted(target string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mounted[target], nil
}

type fakeUnmount struct {
	targets []string
	err     error
}

func (u *fakeUnmount) Unmount(target string) error {
	u.targets = append(u.targets, target)
	return u.err
}

func TestSupervisorMountIdempotent(t *testing.T) {
	state := t.TempDir()
	target := filepath.Join(t.TempDir(), "mnt")
	fs := newFakeFS()
	runner := &fakeRunner{}
	s := &Supervisor{
		Bin:      "mergerfs",
		StateDir: state,
		Runner:   runner,
		Unmount:  &fakeUnmount{},
		FS:       fs,
	}

	ctx := context.Background()
	if err := s.Mount(ctx, "vol1", target, "category.create=mfs", []string{"/a", "/b"}); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls=%d", len(runner.calls))
	}
	pidFile := filepath.Join(state, "vol1.pid")
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(string(b[:len(b)-1]))
	if pid != 1001 {
		t.Fatalf("pid=%d", pid)
	}

	fs.mounted[target] = true
	if err := s.Mount(ctx, "vol1", target, "category.create=mfs", []string{"/a", "/b"}); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("idempotent should not restart, calls=%d", len(runner.calls))
	}
}

func TestSupervisorUnmount(t *testing.T) {
	state := t.TempDir()
	target := filepath.Join(t.TempDir(), "mnt")
	fs := newFakeFS()
	um := &fakeUnmount{}
	var killed []int
	s := &Supervisor{
		Bin:      "mergerfs",
		StateDir: state,
		Runner:   &fakeRunner{},
		Unmount:  um,
		FS:       fs,
		Kill: func(pid int) error {
			killed = append(killed, pid)
			return nil
		},
	}
	ctx := context.Background()
	if err := s.Mount(ctx, "v", target, "", []string{"/a"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UnmountVolume("v", target); err != nil {
		t.Fatal(err)
	}
	if len(um.targets) != 1 || um.targets[0] != target {
		t.Fatalf("unmount targets=%v", um.targets)
	}
	if len(killed) != 1 || killed[0] != 1001 {
		t.Fatalf("killed=%v", killed)
	}
	if _, err := os.Stat(filepath.Join(state, "v.pid")); !os.IsNotExist(err) {
		t.Fatalf("pidfile should be gone: %v", err)
	}
}

func TestSanitizeVolumeID(t *testing.T) {
	got := sanitizeVolumeID("pvc-abc/../x")
	if got != "pvc-abc_.._x" {
		t.Fatalf("got %q", got)
	}
}
