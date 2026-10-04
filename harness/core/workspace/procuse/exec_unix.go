//go:build unix

package procuse

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const outputLimit = 1 << 20

var errOutputLimit = errors.New("procuse: output limit exceeded")

type boundedOutput struct {
	buffer   bytes.Buffer
	exceeded bool
}

func (b *boundedOutput) Write(data []byte) (int, error) {
	remaining := outputLimit - b.buffer.Len()
	if len(data) > remaining {
		b.exceeded = true
	}
	if remaining > len(data) {
		remaining = len(data)
	}
	_, _ = b.buffer.Write(data[:remaining])
	return len(data), nil
}

// ExecRunner runs lsof without a shell, with LC_ALL=C and no inherited home or
// locale settings. Empty path uses the caller's PATH to resolve lsof before
// starting; the child gets only LC_ALL. An explicit path also forwards PATH,
// allowing a caller-authorized wrapper to find its helpers. Each stream is
// capped at one MiB; excess output is discarded and returns an error.
// Cancellation kills the owned process group and waits for the direct child;
// pipe shutdown has a one-second bound after cancellation or child exit.
func ExecRunner(path string) Runner {
	explicit := path != ""
	if path == "" {
		path = "lsof"
	}
	return func(ctx context.Context, args ...string) ([]byte, []byte, error) {
		if ctx == nil {
			return nil, nil, errors.New("procuse: context is required")
		}
		cmd := exec.CommandContext(ctx, path, args...)
		cmd.Env = []string{"LC_ALL=C"}
		if explicit {
			if value, ok := os.LookupEnv("PATH"); ok {
				cmd.Env = append(cmd.Env, "PATH="+value)
			}
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		cmd.WaitDelay = time.Second
		var stdout, stderr boundedOutput
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		if cmd.Process != nil && (ctx.Err() != nil || errors.Is(err, exec.ErrWaitDelay)) {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		if stdout.exceeded || stderr.exceeded {
			err = errOutputLimit
		}
		return stdout.buffer.Bytes(), stderr.buffer.Bytes(), err
	}
}

func platformRoots(target, scratch string) ReasonCode {
	if !scannableFilesystem(target) {
		return ReasonUnsupportedFilesystem
	}
	targetInfo, err := os.Stat(target)
	if err != nil {
		return ReasonUnsupportedFilesystem
	}
	scratchInfo, err := os.Stat(scratch)
	if err != nil {
		return ReasonUnsupportedFilesystem
	}
	targetStat, ok := targetInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return ReasonUnsupportedFilesystem
	}
	scratchStat, ok := scratchInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return ReasonUnsupportedFilesystem
	}
	if targetStat.Dev != scratchStat.Dev {
		return ReasonDeviceMismatch
	}
	return ""
}
