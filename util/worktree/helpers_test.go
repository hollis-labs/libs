package worktree_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	worktree "github.com/hollis-labs/go-worktree"
)

// TestMain isolates every git invocation in this test binary from the
// developer's machine: no global or system config, a private HOME, a fixed
// identity, and no ambient GIT_DIR-style variables (set, for example, when
// tests run from inside a git hook).
func TestMain(m *testing.M) {
	if _, err := exec.LookPath("git"); err != nil {
		os.Stderr.WriteString("git not found on PATH; skipping tests\n")
		os.Exit(0)
	}
	home, err := os.MkdirTemp("", "go-worktree-home-")
	if err != nil {
		panic(err)
	}
	for _, k := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_PREFIX"} {
		os.Unsetenv(k)
	}
	for k, v := range map[string]string{
		"HOME":                home,
		"XDG_CONFIG_HOME":     home,
		"GIT_CONFIG_GLOBAL":   os.DevNull,
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME":     "Test",
		"GIT_AUTHOR_EMAIL":    "test@example.com",
		"GIT_COMMITTER_NAME":  "Test",
		"GIT_COMMITTER_EMAIL": "test@example.com",
		"GIT_TERMINAL_PROMPT": "0",
	} {
		os.Setenv(k, v)
	}
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// git runs git in dir and returns trimmed stdout, failing the test on error.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // test helper with fixed arguments
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// commitFile writes name in dir, commits it, and returns the new commit id.
func commitFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	write(t, filepath.Join(dir, name), content)
	git(t, dir, "add", name)
	git(t, dir, "commit", "-q", "-m", "add "+name)
	return git(t, dir, "rev-parse", "HEAD")
}

// newRepo makes a local-only repository (no remotes) with one commit. It sits
// in a private temp dir, so sibling worktrees are cleaned up with the test.
func newRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o750); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-q", "-b", "main")
	commitFile(t, repo, "README.md", "# seed\n")
	return repo
}

// newOriginClone makes a bare origin with one commit on main and returns a
// clone of it, so origin/main is a real remote-tracking ref.
func newOriginClone(t *testing.T) string {
	t.Helper()
	seed := newRepo(t)
	origin := filepath.Join(t.TempDir(), "origin.git")
	git(t, filepath.Dir(origin), "clone", "-q", "--bare", seed, origin)
	work := filepath.Join(t.TempDir(), "work")
	git(t, filepath.Dir(work), "clone", "-q", origin, work)
	return work
}

func mustNew(t *testing.T, repo string, opts ...worktree.Option) *worktree.Manager {
	t.Helper()
	m, err := worktree.New(repo, opts...)
	if err != nil {
		t.Fatalf("New(%s): %v", repo, err)
	}
	return m
}

func mustCreate(t *testing.T, m *worktree.Manager, spec worktree.Spec) worktree.Worktree {
	t.Helper()
	wt, err := m.Create(context.Background(), spec)
	if err != nil {
		t.Fatalf("Create(%+v): %v", spec, err)
	}
	return wt
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// hasBranch reports whether repo has a local branch of that name.
func hasBranch(t *testing.T, repo, name string) bool {
	t.Helper()
	return git(t, repo, "branch", "--list", name) != ""
}

// same reports whether two paths are the same location after symlink
// resolution (t.TempDir is under a symlink on macOS).
func same(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

func mkdirAndSymlink(target, link string) error {
	if err := os.MkdirAll(target, 0o750); err != nil {
		return err
	}
	return os.Symlink(target, link)
}

func timeAgo(hours int) time.Time { return time.Now().Add(-time.Duration(hours) * time.Hour) }

func removeAll(p string) error { return os.RemoveAll(p) }
