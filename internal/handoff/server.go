package handoff

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// MountFunc mounts the pool with the shim's fusermount options and returns the FUSE
// fd. The server closes its copy of the fd after sending it.
type MountFunc func(opts string) (fd int, err error)

// EventKind classifies what happened on a connection.
type EventKind int

// Event kinds.
const (
	EventMounted EventKind = iota // fd sent to the shim
	EventRejected
)

func (k EventKind) String() string { return [...]string{"mounted", "rejected"}[k] }

// Event is reported for every mount request.
type Event struct {
	Kind EventKind
	Err  error
}

// ServerConfig configures Serve.
type ServerConfig struct {
	SockPath string
	UID, GID int  // socket owner (the Mount Pod's uid/gid); -1 leaves it unchanged
	Mounted  bool // the pool is already mounted (plugin restart): every mount is rejected
	Mount    MountFunc
}

// SocketPerm lets owner and group connect, nobody else.
const SocketPerm = 0o660

// Serve listens on cfg.SockPath until ctx is done, reporting to events. It removes a
// stale socket first and the socket on return.
func Serve(ctx context.Context, cfg ServerConfig, events chan<- Event) error {
	l, err := listen(cfg)
	if err != nil {
		return err
	}
	s := &server{mounted: cfg.Mounted, mount: cfg.Mount, events: events}
	go func() {
		<-ctx.Done()
		_ = l.Close()
	}()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		c, err := l.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("handoff: accept: %w", err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { _ = c.Close() }()
			s.handle(ctx, c)
		}()
	}
}

func listen(cfg ServerConfig) (*net.UnixListener, error) {
	if err := os.Remove(cfg.SockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("handoff: remove stale socket: %w", err)
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: cfg.SockPath, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("handoff: listen: %w", err)
	}
	l.SetUnlinkOnClose(true)
	if err := os.Chmod(cfg.SockPath, SocketPerm); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("handoff: chmod socket: %w", err)
	}
	if err := os.Lchown(cfg.SockPath, cfg.UID, cfg.GID); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("handoff: chown socket: %w", err)
	}
	return l, nil
}

type server struct {
	mu      sync.Mutex
	mounted bool
	mount   MountFunc
	events  chan<- Event
}

func (s *server) emit(ctx context.Context, e Event) {
	select {
	case s.events <- e:
	case <-ctx.Done():
	}
}

func (s *server) handle(ctx context.Context, c *net.UnixConn) {
	line, err := bufio.NewReaderSize(c, maxLine).ReadString('\n')
	if err != nil {
		return
	}
	m, arg, err := parseLine(line)
	switch {
	case err != nil:
		s.reject(ctx, c, ReasonProtocol, err)
	case m == MsgUnmount:
		_, _ = c.Write(encode(MsgOK, ""))
	case m == MsgMount:
		s.handleMount(ctx, c, arg)
	default:
		s.reject(ctx, c, ReasonProtocol, fmt.Errorf("handoff: unexpected %q", m))
	}
}

func (s *server) handleMount(ctx context.Context, c *net.UnixConn, arg string) {
	opts, err := decodeOpts(arg)
	if err != nil {
		s.reject(ctx, c, ReasonProtocol, err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mounted {
		s.reject(ctx, c, ReasonMountExists, errors.New("handoff: pool already mounted"))
		return
	}
	fd, err := s.mount(opts)
	if err != nil {
		s.reject(ctx, c, ReasonMountFailed+" "+oneLine(err.Error()), err)
		return
	}
	defer func() { _ = unix.Close(fd) }()
	if _, _, err := c.WriteMsgUnix(encode(MsgFD, ""), unix.UnixRights(fd), nil); err != nil {
		s.emit(ctx, Event{Kind: EventRejected, Err: fmt.Errorf("handoff: send fd: %w", err)})
		return
	}
	s.mounted = true
	s.emit(ctx, Event{Kind: EventMounted})
}

func (s *server) reject(ctx context.Context, c *net.UnixConn, reason string, err error) {
	_, _ = c.Write(encode(MsgReject, reason))
	s.emit(ctx, Event{Kind: EventRejected, Err: err})
}

func oneLine(s string) string { return strings.ReplaceAll(s, "\n", " ") }
