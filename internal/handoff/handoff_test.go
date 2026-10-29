package handoff

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type fixture struct {
	sock   string
	events chan Event
	opts   chan string
}

func serve(t *testing.T, mounted bool, mount MountFunc) fixture {
	t.Helper()
	dir, err := os.MkdirTemp("", "ho") // short: unix socket paths are limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	f := fixture{sock: filepath.Join(dir, "s"), events: make(chan Event, 8), opts: make(chan string, 8)}
	if mount == nil {
		mount = func(opts string) (int, error) {
			f.opts <- opts
			r, w, err := os.Pipe()
			if err != nil {
				return -1, err
			}
			_ = w.Close()
			return unix.Dup(int(r.Fd()))
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, ServerConfig{SockPath: f.sock, UID: -1, GID: -1, Mounted: mounted, Mount: mount}, f.events)
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	for range 100 {
		if _, err := os.Stat(f.sock); err == nil {
			return f
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("socket never appeared")
	return f
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return c
}

func next(t *testing.T, f fixture) Event {
	t.Helper()
	select {
	case e := <-f.events:
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}
	return Event{}
}

func TestMountOnce(t *testing.T) {
	f := serve(t, false, nil)
	const opts = `rw,nosuid,fsname=a\,b`
	fd, err := RequestMount(ctx(t), f.sock, opts)
	if err != nil {
		t.Fatal(err)
	}
	_ = unix.Close(fd)
	if got := <-f.opts; got != opts {
		t.Errorf("opts: got %q", got)
	}
	if e := next(t, f); e.Kind != EventMounted {
		t.Errorf("got %v %v", e.Kind, e.Err)
	}
	_, err = RequestMount(ctx(t), f.sock, opts)
	var re *RejectError
	if !errors.As(err, &re) || re.Reason != ReasonMountExists {
		t.Errorf("second mount: got %v", err)
	}
	if e := next(t, f); e.Kind != EventRejected {
		t.Errorf("got %v", e.Kind)
	}
}

func TestAdoptedRejectsMount(t *testing.T) {
	f := serve(t, true, nil)
	if _, err := RequestMount(ctx(t), f.sock, "rw"); !errors.Is(err, ErrRejected) {
		t.Errorf("got %v", err)
	}
}

func TestMountFailed(t *testing.T) {
	f := serve(t, false, func(string) (int, error) { return -1, errors.New("option \"suid\"\nnot allowed") })
	_, err := RequestMount(ctx(t), f.sock, "suid")
	var re *RejectError
	if !errors.As(err, &re) || !strings.HasPrefix(re.Reason, ReasonMountFailed+" option") || strings.Contains(re.Reason, "\n") {
		t.Errorf("got %v", err)
	}
	// A failed mount does not use up the pool: the next request may mount.
	f2 := serve(t, false, nil)
	if fd, err := RequestMount(ctx(t), f2.sock, "rw"); err != nil {
		t.Error(err)
	} else {
		_ = unix.Close(fd)
	}
}

func TestUnmountIsAcknowledged(t *testing.T) {
	f := serve(t, true, nil)
	if err := RequestUnmount(ctx(t), f.sock); err != nil {
		t.Error(err)
	}
}

func TestProtocolError(t *testing.T) {
	f := serve(t, false, nil)
	c, err := net.Dial("unix", f.sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_, _ = c.Write([]byte("hello\n"))
	m, arg, _, err := readReply(c.(*net.UnixConn))
	if err != nil || m != MsgReject || arg != ReasonProtocol {
		t.Errorf("got %q %q %v", m, arg, err)
	}
}

func TestSendFD(t *testing.T) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(pair[0]); _ = unix.Close(pair[1]) }()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	if err := SendFD(pair[0], int(w.Fd())); err != nil {
		t.Fatal(err)
	}
	buf, oob := make([]byte, 1), make([]byte, unix.CmsgSpace(4))
	n, oobn, _, _, err := unix.Recvmsg(pair[1], buf, oob, 0)
	if err != nil || n != 1 {
		t.Fatalf("recv: %d %v", n, err)
	}
	fds, err := parseRights(oob[:oobn])
	if err != nil || len(fds) != 1 {
		t.Fatalf("rights: %v %v", fds, err)
	}
	got := os.NewFile(uintptr(fds[0]), "w")
	defer func() { _ = got.Close() }()
	if _, err := got.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 1)
	if _, err := r.Read(b); err != nil || b[0] != 'x' {
		t.Errorf("read %q %v", b, err)
	}
}
