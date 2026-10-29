package handoff

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"

	"golang.org/x/sys/unix"
)

// RequestMount asks the node plugin to mount the pool with opts (the -o string
// libfuse passed to fusermount) and returns the FUSE fd. The caller owns it.
func RequestMount(ctx context.Context, sockPath, opts string) (int, error) {
	c, err := dial(ctx, sockPath)
	if err != nil {
		return -1, err
	}
	defer func() { _ = c.Close() }()
	if _, err := c.Write(encode(MsgMount, encodeOpts(opts))); err != nil {
		return -1, fmt.Errorf("handoff: send mount: %w", err)
	}
	m, arg, fds, err := readReply(c)
	if err != nil {
		return -1, err
	}
	if m != MsgFD || len(fds) != 1 {
		closeFDs(fds)
		return -1, replyErr(m, arg)
	}
	return fds[0], nil
}

// RequestUnmount tells the plugin mergerfs wants the pool unmounted. The plugin owns
// the mount and only acknowledges.
func RequestUnmount(ctx context.Context, sockPath string) error {
	c, err := dial(ctx, sockPath)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	if _, err := c.Write(encode(MsgUnmount, "")); err != nil {
		return fmt.Errorf("handoff: send unmount: %w", err)
	}
	m, arg, fds, err := readReply(c)
	closeFDs(fds)
	if err != nil {
		return err
	}
	if m != MsgOK {
		return replyErr(m, arg)
	}
	return nil
}

func dial(ctx context.Context, sockPath string) (*net.UnixConn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("handoff: dial: %w", err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	return c.(*net.UnixConn), nil
}

func replyErr(m Message, arg string) error {
	if m == MsgReject {
		return &RejectError{Reason: arg}
	}
	return fmt.Errorf("handoff: unexpected reply %q", m)
}

// readReply reads exactly one line with any passed fds. ReadMsgUnix, not a buffered
// reader, so the SCM_RIGHTS control message attached to the reply is not lost.
func readReply(c *net.UnixConn) (Message, string, []int, error) {
	var line []byte
	var fds []int
	buf := make([]byte, maxLine)
	oob := make([]byte, unix.CmsgSpace(4*4))
	for !bytes.HasSuffix(line, []byte("\n")) {
		n, oobn, _, _, err := c.ReadMsgUnix(buf, oob)
		got, perr := parseRights(oob[:oobn])
		fds = append(fds, got...)
		if perr != nil {
			closeFDs(fds)
			return "", "", nil, perr
		}
		line = append(line, buf[:n]...)
		if err != nil || n == 0 || len(line) > maxLine {
			closeFDs(fds)
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return "", "", nil, fmt.Errorf("handoff: read reply: %w", err)
		}
	}
	m, arg, err := parseLine(string(line))
	if err != nil {
		closeFDs(fds)
		return "", "", nil, err
	}
	return m, arg, fds, nil
}

func parseRights(oob []byte) ([]int, error) {
	if len(oob) == 0 {
		return nil, nil
	}
	msgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return nil, fmt.Errorf("handoff: control message: %w", err)
	}
	var fds []int
	for i := range msgs {
		got, err := unix.ParseUnixRights(&msgs[i])
		if err != nil {
			continue
		}
		fds = append(fds, got...)
	}
	return fds, nil
}

func closeFDs(fds []int) {
	for _, fd := range fds {
		_ = unix.Close(fd)
	}
}

// SendFD passes fd over the libfuse _FUSE_COMMFD socket, as fusermount does: one
// zero byte with the fd as SCM_RIGHTS.
func SendFD(commFD, fd int) error {
	if err := unix.Sendmsg(commFD, []byte{0}, unix.UnixRights(fd), nil, 0); err != nil {
		return fmt.Errorf("handoff: send fd to libfuse: %w", err)
	}
	return nil
}
