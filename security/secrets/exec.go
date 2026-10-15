package secrets

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CommandRunner runs one external command. It returns the child's stdout and
// exit code. A non-nil err means the child could not be started or was killed
// (context). The child's stderr is never captured: it cannot leak into an
// error because this package never sees it.
type CommandRunner func(ctx context.Context, path string, args []string, env []string) (stdout []byte, exit int, err error)

// maxHelperOutput bounds a helper's stdout. A secret is a few hundred bytes.
const maxHelperOutput = 64 << 10

type execConfig struct {
	run      CommandRunner
	lookPath func(string) (string, error)
	dirs     []string
}

func (e execConfig) withDefaults() execConfig {
	if e.run == nil {
		e.run = runCommand
	}
	if e.lookPath == nil {
		e.lookPath = exec.LookPath
	}
	if e.dirs == nil {
		e.dirs = defaultHelperDirs()
	}
	return e
}

func defaultHelperDirs() []string {
	dirs := []string{}
	if v := os.Getenv("GOBIN"); v != "" {
		dirs = append(dirs, v)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs, filepath.Join(home, "go", "bin"), filepath.Join(home, ".local", "bin"))
	}
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	return append(dirs, "/opt/homebrew/bin", "/usr/local/bin")
}

// find locates an executable by name: PATH first, then the extra directories.
func (e execConfig) find(name string) (string, bool) {
	if p, err := e.lookPath(name); err == nil && p != "" {
		return p, true
	}
	for _, d := range e.dirs {
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
			return p, true
		}
	}
	return "", false
}

// sanitizedEnviron returns the process environment without any variable whose
// value is a secret reference. A helper that prefers a conventional variable
// (OPENAI_API_KEY) would otherwise read "keychain://..." and echo it back as
// the secret.
func sanitizedEnviron(schemes []string) []string {
	var out []string
	for _, kv := range environ() {
		_, v, ok := strings.Cut(kv, "=")
		if ok && hasKnownScheme(strings.TrimSpace(v), schemes) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func runCommand(ctx context.Context, path string, args []string, env []string) ([]byte, int, error) {
	cmd := exec.CommandContext(ctx, path, args...) //nolint:gosec // G204: the helper path is operator-configured and validated by the caller
	cmd.Env = env
	out := &cappedBuffer{limit: maxHelperOutput}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	if out.over {
		return nil, 0, errTooLarge
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ctx.Err() == nil {
		return out.buf.Bytes(), ee.ExitCode(), nil
	}
	if err != nil {
		return nil, 0, err
	}
	return out.buf.Bytes(), 0, nil
}

var errTooLarge = errors.New("output too large")

type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
	over  bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.buf.Len()+len(p) > c.limit {
		c.over = true
		return len(p), nil // keep draining so the child is not blocked
	}
	return c.buf.Write(p)
}

// runHelper runs path with args and maps the outcome onto the error taxonomy.
// Exit codes: 0 success (stdout is the value); 2 not found; 3 denied; anything
// else unavailable. A helper that only ever exits 1 therefore reads as
// unavailable, which is honest: its caller cannot tell more.
func runHelper(ctx context.Context, ex execConfig, schemes []string, ref Ref, source, path string, args []string) (Resolved, error) {
	stdout, exit, err := ex.run(ctx, path, args, sanitizedEnviron(schemes))
	switch {
	case errors.Is(err, errTooLarge):
		return Resolved{}, newErr(KindTooLarge, ref, source, "the helper's output is too large", nil)
	case err != nil:
		reason := "the helper could not be run"
		if ctx.Err() != nil {
			reason = "the helper did not finish in time"
		}
		return Resolved{}, newErr(KindUnavailable, ref, source, reason, err)
	}
	switch exit {
	case 0:
		return Resolved{Secret: FromBytes(bytes.TrimSpace(stdout)), Source: source}, nil
	case 2:
		return Resolved{}, newErr(KindNotFound, ref, source, "the helper reports no such entry", nil)
	case 3:
		return Resolved{}, newErr(KindDenied, ref, source, "the helper was refused access", nil)
	}
	return Resolved{}, newErr(KindUnavailable, ref, source, "the helper failed", nil)
}
