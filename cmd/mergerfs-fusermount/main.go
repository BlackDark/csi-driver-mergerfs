// Command mergerfs-fusermount is the fusermount helper mergerfs runs inside a Mount
// Pod. Instead of mounting (the Mount Pod has no /dev/fuse and no CAP_SYS_ADMIN), it
// asks the node plugin over the handoff socket and passes the FUSE fd it receives to
// mergerfs over libfuse's _FUSE_COMMFD socket (ADR 0001).
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"golang.org/x/sys/unix"

	"github.com/BlackDark/csi-driver-mergerfs/internal/handoff"
)

// SocketEnv names the handoff socket; the Mount Pod sets it.
const SocketEnv = "MERGERFS_CSI_HANDOFF"

const defaultSocket = "/run/mergerfs-pool/handoff.sock"

const commFDEnv = "_FUSE_COMMFD"

// request is a parsed fusermount command line.
type request struct {
	unmount    bool
	opts       string
	mountpoint string
}

// parseArgs accepts the forms libfuse uses: [-o opts] [--] mountpoint and
// -u [-q] [-z] [--] mountpoint.
func parseArgs(args []string) (request, error) {
	var r request
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "-u":
			r.unmount = true
		case "-q", "-z":
		case "-o":
			if i+1 >= len(args) {
				return request{}, errors.New("-o needs an argument")
			}
			i++
			r.opts = args[i]
		case "--":
			if i+1 != len(args)-1 {
				return request{}, errors.New("expected one mountpoint after --")
			}
			r.mountpoint = args[i+1]
			return r, nil
		default:
			if len(a) > 0 && a[0] == '-' {
				return request{}, fmt.Errorf("unsupported flag %q", a)
			}
			if i != len(args)-1 {
				return request{}, errors.New("expected one mountpoint")
			}
			r.mountpoint = a
		}
	}
	if r.mountpoint == "" {
		return request{}, errors.New("missing mountpoint")
	}
	return r, nil
}

func main() {
	if err := run(os.Args[1:], os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, "mergerfs-fusermount:", err)
		os.Exit(1)
	}
}

func run(args []string, getenv func(string) string) error {
	r, err := parseArgs(args)
	if err != nil {
		return err
	}
	sock := getenv(SocketEnv)
	if sock == "" {
		sock = defaultSocket
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if r.unmount {
		return handoff.RequestUnmount(ctx, sock)
	}
	commFD, err := strconv.Atoi(getenv(commFDEnv))
	if err != nil {
		return fmt.Errorf("%s not set: not started by libfuse", commFDEnv)
	}
	fd, err := handoff.RequestMount(ctx, sock, r.opts)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	return handoff.SendFD(commFD, fd)
}
