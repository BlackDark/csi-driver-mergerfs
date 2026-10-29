//go:build fuse_integration && linux

// Needs root, /dev/fuse and a mergerfs binary (MERGERFS_BIN or PATH): run via
// `mise run test:fuse` or `mise run test:fuse:host`. mergerfs runs as an
// unprivileged uid with the shim as its only fusermount, like in a Mount Pod.
package fusemount_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/BlackDark/csi-driver-mergerfs/internal/fusemount"
	"github.com/BlackDark/csi-driver-mergerfs/internal/handoff"
)

const poolID = 1000

type fixture struct{ base, fuse, rw, nc, ro, sock, shimDir, mergerfs string }

func newFixture(t *testing.T) fixture {
	t.Helper()
	base, err := os.MkdirTemp("/tmp", "mfit")
	must(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	must(t, os.Chmod(base, 0o755))
	f := fixture{base: base, fuse: filepath.Join(base, "fuse"), rw: filepath.Join(base, "rw"),
		nc: filepath.Join(base, "nc"), ro: filepath.Join(base, "ro"), sock: filepath.Join(base, "handoff.sock"),
		shimDir: filepath.Join(base, "bin")}
	for _, d := range []string{f.fuse, f.rw, f.nc, f.ro, f.shimDir, filepath.Join(base, "pod-mnt")} {
		must(t, os.Mkdir(d, 0o755))
		must(t, os.Chown(d, poolID, poolID))
	}
	must(t, os.WriteFile(filepath.Join(f.nc, "remote.txt"), []byte("remote"), 0o644))
	must(t, os.WriteFile(filepath.Join(f.ro, "ro.txt"), []byte("ro"), 0o644))
	for _, p := range []string{filepath.Join(f.nc, "remote.txt"), filepath.Join(f.ro, "ro.txt")} {
		must(t, os.Chown(p, poolID, poolID))
	}
	f.mergerfs = os.Getenv("MERGERFS_BIN")
	if f.mergerfs == "" {
		f.mergerfs, err = exec.LookPath("mergerfs")
		must(t, err)
	}
	shim := filepath.Join(f.shimDir, "mergerfs-fusermount")
	if pre := os.Getenv("FUSE_IT_SHIM"); pre != "" { // prebuilt: root may lack go
		b, err := os.ReadFile(pre)
		must(t, err)
		must(t, os.WriteFile(shim, b, 0o755))
		return f
	}
	build := exec.Command("go", "build", "-buildvcs=false", "-o", shim, "../../cmd/mergerfs-fusermount")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	must(t, build.Run())
	return f
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// serve plays the node plugin: it mounts fuse/ when the shim asks.
func (f fixture) serve(t *testing.T) chan handoff.Event {
	t.Helper()
	events := make(chan handoff.Event, 4)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		err := handoff.Serve(ctx, handoff.ServerConfig{SockPath: f.sock, UID: poolID, GID: poolID,
			Mount: func(opts string) (int, error) {
				t.Logf("shim opts: %s", opts)
				o, err := fusemount.ParseOpts(opts, poolID, poolID)
				if err != nil {
					return -1, err
				}
				return fusemount.Mount(f.fuse, o)
			}}, events)
		if err != nil {
			t.Error(err)
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
	return events
}

func (f fixture) startMergerfs(t *testing.T) *exec.Cmd {
	t.Helper()
	branches := f.rw + "=RW:" + f.nc + "=NC:" + f.ro + "=RO"
	cmd := exec.Command(f.mergerfs, "-f", "-o", "auto_unmount,fsname=fit,category.create=ff,cache.files=off",
		branches, filepath.Join(f.base, "pod-mnt")) // the Mount Pod's mountpoint: must exist, never mounted
	cmd.Env = []string{"PATH=" + f.shimDir, "MERGERFS_CSI_HANDOFF=" + f.sock}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: poolID, Gid: poolID}}
	must(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = fusemount.Unmount(f.fuse, true)
	})
	return cmd
}

func waitServing(t *testing.T, dir string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var st unix.Statfs_t
		if err := unix.Statfs(dir, &st); err == nil && st.Type == unix.FUSE_SUPER_MAGIC {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("pool never served")
}

func TestFusePool(t *testing.T) {
	f := newFixture(t)
	events := f.serve(t)
	cmd := f.startMergerfs(t)
	select {
	case e := <-events:
		if e.Kind != handoff.EventMounted {
			t.Fatalf("handoff: %v %v", e.Kind, e.Err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("shim never asked for a mount")
	}
	waitServing(t, f.fuse)

	// Reads see every branch; the NC branch is readable.
	b, err := os.ReadFile(filepath.Join(f.fuse, "remote.txt"))
	if err != nil || string(b) != "remote" {
		t.Fatalf("read NC branch: %q %v", b, err)
	}
	// Creates land on the RW branch only.
	must(t, os.WriteFile(filepath.Join(f.fuse, "new.txt"), []byte("new"), 0o644))
	if _, err := os.Stat(filepath.Join(f.rw, "new.txt")); err != nil {
		t.Errorf("create not on RW branch: %v", err)
	}
	// Rename within the pool.
	must(t, os.Rename(filepath.Join(f.fuse, "new.txt"), filepath.Join(f.fuse, "moved.txt")))
	if _, err := os.Stat(filepath.Join(f.rw, "moved.txt")); err != nil {
		t.Errorf("rename: %v", err)
	}
	// RO branch: actions (unlink, chmod, rename) fail; mergerfs still opens files there for writing.
	if err := os.Remove(filepath.Join(f.fuse, "ro.txt")); err == nil {
		t.Error("unlink on RO branch succeeded")
	}

	// mergerfs gone: the pool reports ENOTCONN until the plugin unmounts it.
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	var st unix.Statfs_t
	if err := unix.Statfs(f.fuse, &st); !errors.Is(err, unix.ENOTCONN) {
		t.Errorf("statfs after kill: %v", err)
	}
	must(t, fusemount.Unmount(f.fuse, true))
}
