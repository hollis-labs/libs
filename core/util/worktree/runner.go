package worktree

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Runner executes one git command. Implementations receive the arguments
// after the `git` binary name and the directory to run in, and return the
// command's stdout. A non-zero exit must be reported as a non-nil error.
type Runner interface {
	Run(ctx context.Context, dir string, args ...string) (stdout string, err error)
}

// ExecRunner returns the default Runner: it executes the `git` binary found on
// PATH with cmd.Dir set to dir. It sets GIT_TERMINAL_PROMPT=0, LC_ALL=C and
// GIT_OPTIONAL_LOCKS=0, and strips GIT_DIR, GIT_WORK_TREE and GIT_INDEX_FILE
// from the inherited environment so an ambient hook environment cannot
// redirect a command at a different repository. Errors carry the arguments
// and the trimmed stderr, and wrap the underlying exec error.
func ExecRunner() Runner { return execRunner{} }

type execRunner struct{}

var _ Runner = execRunner{}

func (execRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // args are built by this package from validated inputs
	cmd.Dir = dir
	cmd.Env = gitEnv(os.Environ())
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(stderr.String()), err)
	}
	return stdout.String(), nil
}

func gitEnv(base []string) []string {
	env := make([]string, 0, len(base)+3)
	for _, kv := range base {
		switch {
		case strings.HasPrefix(kv, "GIT_DIR="),
			strings.HasPrefix(kv, "GIT_WORK_TREE="),
			strings.HasPrefix(kv, "GIT_INDEX_FILE="),
			strings.HasPrefix(kv, "GIT_TERMINAL_PROMPT="),
			strings.HasPrefix(kv, "GIT_OPTIONAL_LOCKS="),
			strings.HasPrefix(kv, "LC_ALL="):
		default:
			env = append(env, kv)
		}
	}
	return append(env, "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
}
