package cmdhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	hooks "github.com/hollis-labs/go-hooks"
)

// BlockExitCode is the exit code that signals an intentional block.
const BlockExitCode = 2

const (
	maxStderrInError = 4 << 10
	defaultWaitDelay = 500 * time.Millisecond
)

// Runner executes command-kind hooks.
type Runner struct {
	// Start builds the command. The default is exec.CommandContext. Tests
	// override it to substitute a helper process.
	Start func(ctx context.Context, name string, args ...string) *exec.Cmd
}

// Run writes input (JSON-encoded) to the hook's stdin, enforces h.Timeout via
// a context deadline (applied even when ctx carries none), and decodes the
// outcome per the package conventions.
//
// A timeout returns an error wrapping context.DeadlineExceeded. A canceled
// ctx returns an error wrapping ctx.Err(). Neither is a Decision.
func (r Runner) Run(ctx context.Context, h hooks.Hook, input any) (hooks.Output, error) {
	if h.Kind != hooks.KindCommand {
		return hooks.Output{}, fmt.Errorf("cmdhook: hook %q has kind %q, want %q", h.Name, h.Kind, hooks.KindCommand)
	}
	if h.Command == "" {
		return hooks.Output{}, fmt.Errorf("cmdhook: hook %q has no Command", h.Name)
	}
	if h.Timeout <= 0 {
		return hooks.Output{}, fmt.Errorf("cmdhook: hook %q has no Timeout; a positive Timeout is required", h.Name)
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return hooks.Output{}, fmt.Errorf("cmdhook: hook %q: encode input: %w", h.Name, err)
	}

	tctx, cancel := context.WithTimeout(ctx, h.Timeout)
	defer cancel()

	start := r.Start
	if start == nil {
		start = exec.CommandContext
	}
	cmd := start(tctx, h.Command, h.CommandArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if cmd.WaitDelay == 0 {
		// Bound the wait for pipe-holding grandchildren after the kill.
		cmd.WaitDelay = defaultWaitDelay
	}

	runErr := cmd.Run()
	if tctx.Err() != nil {
		// Timeout or cancellation wins over whatever the killed process
		// reported, including a coincidental exit code 2.
		if cerr := ctx.Err(); cerr != nil {
			return hooks.Output{}, fmt.Errorf("cmdhook: hook %q canceled: %w", h.Name, cerr)
		}
		return hooks.Output{}, fmt.Errorf("cmdhook: hook %q timed out after %s: %w", h.Name, h.Timeout, context.DeadlineExceeded)
	}

	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) && ee.ExitCode() == BlockExitCode {
			return hooks.Output{
				Decision: hooks.DecisionDeny,
				Reason:   strings.TrimSpace(stderr.String()),
			}, nil
		}
		return hooks.Output{}, fmt.Errorf("cmdhook: hook %q failed: %w%s", h.Name, runErr, stderrSuffix(stderr.Bytes()))
	}

	raw := bytes.TrimSpace(stdout.Bytes())
	if len(raw) == 0 {
		return hooks.Output{}, nil
	}
	var out hooks.Output
	if err := json.Unmarshal(raw, &out); err != nil {
		return hooks.Output{}, fmt.Errorf("cmdhook: hook %q printed invalid output: %w", h.Name, err)
	}
	if err := out.Validate(); err != nil {
		return hooks.Output{}, fmt.Errorf("cmdhook: hook %q: %w", h.Name, err)
	}
	return out, nil
}

func stderrSuffix(b []byte) string {
	s := strings.TrimSpace(string(b))
	if s == "" {
		return ""
	}
	if len(s) > maxStderrInError {
		s = s[:maxStderrInError] + "..."
	}
	return ": " + s
}
