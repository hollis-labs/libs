//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package localdaemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// Spawn re-executes the running binary (os.Executable) with opts.Args as a
// detached background process and returns its PID as soon as it has started.
// It does not wait for the child to be ready; pair it with [WaitReady].
//
// ctx is honored only until the child starts. Canceling it afterwards does
// not stop the child: a daemon must outlive the call that started it.
//
// The child survives the parent's exit. While the parent lives, Spawn reaps
// the child in the background so it never lingers as a zombie (which
// [IsAlive] would report as alive).
func Spawn(ctx context.Context, opts SpawnOptions) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	exe, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("resolve executable: %w", err)
	}
	cmd := exec.Command(exe, opts.Args...) //nolint:gosec // G204: re-exec of the running binary
	cmd.Env = append(os.Environ(), opts.Env...)
	// A nil *os.File must stay a nil interface so exec uses the null device.
	if opts.Stdout != nil {
		cmd.Stdout = opts.Stdout
	}
	if opts.Stderr != nil {
		cmd.Stderr = opts.Stderr
	}
	switch opts.Detach {
	case Session:
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	case ProcessGroup:
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	default:
		return 0, fmt.Errorf("localdaemon: unknown Detach %d", opts.Detach)
	}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start %s: %w", exe, err)
	}
	pid := cmd.Process.Pid
	go func() { _ = cmd.Wait() }()
	return pid, nil
}
