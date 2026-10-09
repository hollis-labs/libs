//go:build !windows

package runner_test

import (
	"context"
	"errors"
	"github.com/hollis-labs/substrate/harness/runner"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestResourceLimits_CPUTime functionally verifies CPUTime enforcement.
// Uses a sh busy-loop (not a Go binary) because Go's runtime swallows
// SIGXCPU on at least darwin — the runtime catches the signal and
// lets the program continue. Native C-based binaries (sh, yes, dd)
// honor SIGXCPU normally and terminate at the soft limit.
func TestResourceLimits_CPUTime(t *testing.T) {
	workspace := t.TempDir()

	// sh script: emit one delta line then busy-loop. RLIMIT_CPU=1s
	// fires SIGXCPU which sh's default handler honors (terminates).
	script := filepath.Join(workspace, "burn.sh")
	body := `#!/bin/sh
echo '{"type":"delta","content":"start"}'
while :; do : $((1+1)); done
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	cfg := runner.Config{
		Provider:  &stubAdapter{binPath: script},
		Workspace: workspace,
		Args:      []string{},
		ResourceLimits: runner.ResourceLimits{
			CPUTime: 1 * time.Second,
		},
		OnEvent: func(runner.Event) {},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	start := time.Now()
	err := runner.Run(ctx, cfg)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected non-nil error after CPU time limit")
	}
	var xe *runner.ExitError
	if !errors.As(err, &xe) {
		t.Fatalf("error is not *ExitError: %T %v", err, err)
	}
	if xe.Signal != int(syscall.SIGXCPU) && xe.Signal != int(syscall.SIGKILL) {
		t.Errorf("ExitError.Signal = %d, want SIGXCPU(%d) or SIGKILL(%d)",
			xe.Signal, syscall.SIGXCPU, syscall.SIGKILL)
	}
	if elapsed > 8*time.Second {
		t.Errorf("Run took %v, expected <8s with CPUTime=1s", elapsed)
	}
}
