package mergerfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	settleTimeout = 2 * time.Second
	settlePoll    = 50 * time.Millisecond
	settleMin     = 200 * time.Millisecond // non-Linux: brief alive check
)

// Process is a started mergerfs child.
type Process interface {
	PID() int
	Wait() error
	Kill() error
	Alive() bool
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

func (p *execProcess) Alive() bool {
	if p.cmd.ProcessState != nil && p.cmd.ProcessState.Exited() {
		return false
	}
	if p.cmd.Process == nil {
		return false
	}
	err := p.cmd.Process.Signal(syscall.Signal(0))
	return err == nil
}

func (r ExecRunner) Start(ctx context.Context, bin string, args []string) (Process, error) {
	// Intentionally ignore request ctx: CommandContext would SIGKILL mergerfs
	// when the NodePublishVolume RPC returns and cancels ctx.
	_ = ctx
	cmd := exec.Command(bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", bin, err)
	}
	go func() { _ = cmd.Wait() }()
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
	if _, err := os.Stat(target); os.IsNotExist(err) {
		return nil
	}
	if ok, _ := isMountPoint(target); !ok {
		return nil
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

// sanitizeVolumeID hex-encodes a truncated sha256 of volumeID so path
// separators cannot escape StateDir. Rejects ".." for defense in depth.
func sanitizeVolumeID(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:16])
}

func rejectVolumeID(id string) error {
	if id == "" {
		return fmt.Errorf("volumeID empty")
	}
	if strings.Contains(id, "..") {
		return fmt.Errorf("volumeID must not contain parent path segments")
	}
	return nil
}

// Mount starts mergerfs at target if not already mounted. Idempotent.
// If already mounted, succeeds without restart (source check is best-effort / not performed).
func (s *Supervisor) Mount(ctx context.Context, volumeID, target, opts string, branches []string) error {
	if err := rejectVolumeID(volumeID); err != nil {
		return err
	}
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

	pidFile := s.pidPath(volumeID)
	if err := writePID(pidFile, proc.PID()); err != nil {
		_ = proc.Kill()
		return err
	}

	if err := s.settle(ctx, proc, target, pidFile); err != nil {
		return err
	}
	return nil
}

func (s *Supervisor) settleCleanup(proc Process, pidFile string) {
	_ = proc.Kill()
	_ = os.Remove(pidFile)
}

// settle polls until mergerfs is alive and (on Linux) target is mounted.
func (s *Supervisor) settle(ctx context.Context, proc Process, target, pidFile string) error {
	requireMount := runtime.GOOS == "linux"
	start := time.Now()
	deadline := start.Add(settleTimeout)

	for {
		select {
		case <-ctx.Done():
			s.settleCleanup(proc, pidFile)
			return ctx.Err()
		default:
		}

		if !proc.Alive() {
			_ = os.Remove(pidFile)
			return fmt.Errorf("mergerfs exited before mount settled")
		}

		mounted, err := s.FS.IsMounted(target)
		if err != nil {
			s.settleCleanup(proc, pidFile)
			return fmt.Errorf("check mount: %w", err)
		}
		if mounted {
			return nil
		}

		elapsed := time.Since(start)
		if !requireMount && elapsed >= settleMin {
			// Darwin cannot reliably detect FUSE mounts; alive after brief settle is enough.
			return nil
		}
		if time.Now().After(deadline) {
			s.settleCleanup(proc, pidFile)
			return fmt.Errorf("mergerfs started but %s not mounted", target)
		}

		select {
		case <-ctx.Done():
			s.settleCleanup(proc, pidFile)
			return ctx.Err()
		case <-time.After(settlePoll):
		}
	}
}

// UnmountVolume stops mergerfs for volumeID and unmounts target.
// Umount errors are returned unless target is not mounted / ENOENT.
// Kill + pidfile cleanup always attempted.
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
	if err := p.Signal(syscall.Signal(0)); err != nil {
		return nil // already gone
	}
	return p.Signal(syscall.SIGTERM)
}
