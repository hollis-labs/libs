//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package localdaemon

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// runPS returns the command line of pid as reported by ps. It is a variable
// so tests can inject output without spawning processes.
var runPS = func(ctx context.Context, pid int) ([]byte, error) {
	// #nosec G204 -- literal arguments; pid is formatted from an int.
	return exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
}

// VerifyCommand reports whether the process with this PID is running a
// command line that pattern matches, using "ps -p PID -o command=" (the same
// on BSD/macOS and procps ps). Use it to confirm that a PID read from a PID
// file still names your daemon before you signal it.
//
// It returns (false, nil) when the process does not exist or does not match.
// It returns an error only when the check itself could not be made (ps
// missing, ctx ended, nil pattern); callers must treat an error as "not
// verified" and not signal.
//
// The command line is read at one instant: a PID recycled between this call
// and a following signal is a residual race that no PID-based scheme closes.
func VerifyCommand(ctx context.Context, pid int, pattern Matcher) (bool, error) {
	if pattern == nil {
		return false, errors.New("localdaemon: VerifyCommand needs a pattern")
	}
	if pid <= 0 {
		return false, nil
	}
	out, err := runPS(ctx, pid)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return false, fmt.Errorf("ps probe pid %d: %w", pid, cerr)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// ps exits non-zero when the PID does not exist.
			return false, nil
		}
		return false, fmt.Errorf("ps probe pid %d: %w", pid, err)
	}
	cmd := strings.TrimSpace(string(out))
	if cmd == "" {
		return false, nil
	}
	return pattern.MatchString(cmd), nil
}
