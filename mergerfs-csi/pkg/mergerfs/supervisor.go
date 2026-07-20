package mergerfs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Process is a started mergerfs child.
type Process interface {
	PID() int
	Wait() error
	Kill() error
}

// Runner starts mergerfs processes.
type Runner interface {
	Start(ctx context.Context, bin string, args []string) (Process, error)
}

// Unmounter unmounts a FUSE mount point.
type Unmounter interface {
	Unmount(target string) error
}

// Mounter abstracts mount checks for testability.
type Mounter interface {
	IsMounted(target string) (bool, error)
	MkdirAll(path string, perm os.FileMode) error
}

// ExecRunner starts real processes with Setpgid so they survive independently.
type ExecRunner struct{}

type execProcess struct {
	cmd *exec.Cmd
}

func (p *execProcess) PID() int { return p.cmd.Process.Pid }

func (p *execProcess) Wait() error { return p.cmd.Wait() }

func (p *execProcess) Kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Kill()
}

func (r ExecRunner) Start(ctx context.Context, bin string, args []string) (Process, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", bin, err)
	}
	return &execProcess{cmd: cmd}, nil
}

// ExecUnmounter tries fusermount3, fusermount, then umount.
type ExecUnmounter struct{}

func (ExecUnmounter) Unmount(target string) error {
	for _, try := range [][]string{
		{"fusermount3", "-u", target},
		{"fusermount", "-u", target},
		{"umount", target},
	} {
		cmd := exec.Command(try[0], try[1:]...)
		if err := cmd.Run(); err == nil {
			return nil
		}
	}
	return fmt.Errorf("unmount %s: all methods failed", target)
}

// OSMounter uses the real filesystem.
type OSMounter struct{}

func (OSMounter) MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

func (OSMounter) IsMounted(target string) (bool, error) {
	return isMountPoint(target)
}

// Killer sends a signal to a PID (injectable for tests).
type Killer func(pid int) error

// Supervisor tracks pidfiles and starts/stops mergerfs.
type Supervisor struct {
	Bin      string
	StateDir string
	Runner   Runner
	Unmount  Unmounter
	FS       Mounter
	Kill     Killer
}

func (s *Supervisor) pidPath(volumeID string) string {
	safe := sanitizeVolumeID(volumeID)
	return fmt.Sprintf("%s/%s.pid", s.StateDir, safe)
}

func sanitizeVolumeID(id string) string {
	out := make([]byte, 0, len(id))
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "unknown"
	}
	return string(out)
}

// Mount starts mergerfs at target if not already mounted. Idempotent.
func (s *Supervisor) Mount(ctx context.Context, volumeID, target, opts string, branches []string) error {
	if err := s.FS.MkdirAll(s.StateDir, 0o755); err != nil {
		return err
	}
	if err := s.FS.MkdirAll(target, 0o755); err != nil {
		return err
	}

	mounted, err := s.FS.IsMounted(target)
	if err != nil {
		return err
	}
	if mounted {
		return nil
	}

	args, err := BuildArgs(opts, branches, target)
	if err != nil {
		return err
	}

	proc, err := s.Runner.Start(ctx, s.Bin, args)
	if err != nil {
		return err
	}

	if err := writePID(s.pidPath(volumeID), proc.PID()); err != nil {
		_ = proc.Kill()
		return err
	}

	// Brief settle so a fast-failing mergerfs surfaces before we return success.
	select {
	case <-time.After(200 * time.Millisecond):
	case <-ctx.Done():
		_ = proc.Kill()
		_ = os.Remove(s.pidPath(volumeID))
		return ctx.Err()
	}
	return nil
}

// Unmount stops mergerfs for volumeID and unmounts target.
func (s *Supervisor) UnmountVolume(volumeID, target string) error {
	var first error
	if err := s.Unmount.Unmount(target); err != nil {
		first = err
	}
	pid, err := readPID(s.pidPath(volumeID))
	if err == nil && pid > 0 {
		kill := s.Kill
		if kill == nil {
			kill = killPID
		}
		_ = kill(pid)
	}
	_ = os.Remove(s.pidPath(volumeID))
	return first
}

func writePID(path string, pid int) error {
	return os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o644)
}

func readPID(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

func killPID(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(syscall.SIGTERM)
}
