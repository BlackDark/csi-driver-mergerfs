package mergerfs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
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

func TestBuildArgsRejectsColonInPath(t *testing.T) {
	_, err := BuildArgs("", []string{"/data/a:b"}, "/t")
	if err == nil {
		t.Fatal("expected reject colon in branch path")
	}
}

type fakeProc struct {
	pid   int
	dead  bool
	mu    sync.Mutex
	kills int
}

func (p *fakeProc) PID() int { return p.pid }
func (p *fakeProc) Wait() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dead = true
	return nil
}
func (p *fakeProc) Kill() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dead = true
	p.kills++
	return nil
}
func (p *fakeProc) Alive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.dead
}

type fakeRunner struct {
	mu    sync.Mutex
	calls [][]string
	next  int
	proc  *fakeProc // optional shared proc
}

func (r *fakeRunner) Start(_ context.Context, bin string, args []string) (Process, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string{bin}, args...))
	r.next++
	if r.proc != nil {
		return r.proc, nil
	}
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
	pidFile := s.pidPath("vol1")
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
	if _, err := os.Stat(s.pidPath("v")); !os.IsNotExist(err) {
		t.Fatalf("pidfile should be gone: %v", err)
	}
}

func TestSupervisorUnmountSurfacesError(t *testing.T) {
	state := t.TempDir()
	target := filepath.Join(t.TempDir(), "mnt")
	um := &fakeUnmount{err: fmt.Errorf("umount failed: busy")}
	var killed []int
	s := &Supervisor{
		Bin:      "mergerfs",
		StateDir: state,
		Runner:   &fakeRunner{},
		Unmount:  um,
		FS:       newFakeFS(),
		Kill: func(pid int) error {
			killed = append(killed, pid)
			return nil
		},
	}
	if err := s.Mount(context.Background(), "v", target, "", []string{"/a"}); err != nil {
		t.Fatal(err)
	}
	err := s.UnmountVolume("v", target)
	if err == nil {
		t.Fatal("expected unmount error")
	}
	if len(killed) != 1 {
		t.Fatalf("kill still attempted, killed=%v", killed)
	}
	if _, err := os.Stat(s.pidPath("v")); !os.IsNotExist(err) {
		t.Fatalf("pidfile should be gone: %v", err)
	}
}

func TestSanitizeVolumeID(t *testing.T) {
	a := sanitizeVolumeID("pvc-abc")
	b := sanitizeVolumeID("pvc-abc/../x")
	if a == "" || len(a) != 32 {
		t.Fatalf("expected 32 hex chars, got %q", a)
	}
	if a == b {
		t.Fatal("different IDs should hash differently")
	}
	if stringsContainSlash(a) {
		t.Fatalf("hash must not contain slash: %q", a)
	}
}

func stringsContainSlash(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' || s[i] == '\\' {
			return true
		}
	}
	return false
}

func TestRejectVolumeIDDotDot(t *testing.T) {
	s := &Supervisor{
		Bin:      "mergerfs",
		StateDir: t.TempDir(),
		Runner:   &fakeRunner{},
		Unmount:  &fakeUnmount{},
		FS:       newFakeFS(),
	}
	err := s.Mount(context.Background(), "vol/../escape", filepath.Join(t.TempDir(), "t"), "", []string{"/a"})
	if err == nil {
		t.Fatal("expected reject .. in volumeID")
	}
}

func TestExecRunnerIgnoresCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := ExecRunner{}
	proc, err := r.Start(ctx, "sleep", []string{"30"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = proc.Kill() }()

	cancel()
	time.Sleep(150 * time.Millisecond)
	if !proc.Alive() {
		t.Fatal("ctx cancel must not kill process started via ExecRunner")
	}
}

func TestSettleKillsDeadProcess(t *testing.T) {
	state := t.TempDir()
	target := filepath.Join(t.TempDir(), "mnt")
	proc := &fakeProc{pid: 99, dead: true}
	runner := &fakeRunner{proc: proc}
	s := &Supervisor{
		Bin:      "mergerfs",
		StateDir: state,
		Runner:   runner,
		Unmount:  &fakeUnmount{},
		FS:       newFakeFS(),
	}
	err := s.Mount(context.Background(), "dead", target, "", []string{"/a"})
	if err == nil {
		t.Fatal("expected error when process dead")
	}
}
