package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func skipNoModes(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission modes are not enforced on windows")
	}
}

func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

func wantMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	if got := modeOf(t, path); got != want {
		t.Errorf("%s mode = %#o, want %#o", path, got, want)
	}
}

// ownedDirs lists every directory materialize is required to make owner-only.
func ownedDirs(l Layout) []string {
	return []string{
		l.DataDir(), l.StateDir(), l.CacheDir(), l.ConfigDir(),
		l.Workspace().Dir, filepath.Dir(l.Workspace().DBPath),
	}
}

func TestMaterializeDirModeIsOwnerOnly(t *testing.T) {
	skipNoModes(t)
	hermeticHome(t)
	l, err := Resolve("demo")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for _, d := range ownedDirs(l) {
		wantMode(t, d, DirMode)
	}
}

func TestMaterializeRetightensExistingLooseDir(t *testing.T) {
	skipNoModes(t)
	home := hermeticHome(t)
	roots := []string{
		filepath.Join(home, ".local", "share", "demo"),
		filepath.Join(home, ".local", "state", "demo"),
		filepath.Join(home, ".cache", "demo"),
		filepath.Join(home, ".config", "demo"),
		filepath.Join(home, ".local", "share", "demo", "workspaces", "default"),
	}
	for _, d := range roots {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	l, err := Resolve("demo")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for _, d := range ownedDirs(l) {
		wantMode(t, d, DirMode)
	}
	// Idempotent: a second Resolve changes nothing and does not fail.
	l2, err := Resolve("demo")
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	for _, d := range ownedDirs(l2) {
		wantMode(t, d, DirMode)
	}
}

func TestSelectWorkspaceActiveWorkspaceFileIsOwnerOnly(t *testing.T) {
	skipNoModes(t)
	hermeticHome(t)
	l, err := Resolve("demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := l.SelectWorkspace("staging"); err != nil {
		t.Fatalf("SelectWorkspace: %v", err)
	}
	wantMode(t, filepath.Join(l.StateDir(), activeWorkspaceFile), FileMode)
	wantMode(t, l.ResolveWorkspace("staging").Dir, DirMode)
	wantMode(t, l.StateDir(), DirMode)
}

func TestSelectWorkspaceRetightensExistingLooseFile(t *testing.T) {
	skipNoModes(t)
	hermeticHome(t)
	l, err := Resolve("demo")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(l.StateDir(), activeWorkspaceFile)
	if err := os.WriteFile(file, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(l.StateDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := l.SelectWorkspace("staging"); err != nil {
		t.Fatal(err)
	}
	wantMode(t, file, FileMode)
	wantMode(t, l.StateDir(), DirMode)
}

func TestMaterializeDoesNotTightenDBOverrideDirectory(t *testing.T) {
	skipNoModes(t)
	hermeticHome(t)
	shared := filepath.Join(t.TempDir(), "shared")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve("demo", WithDBOverride(filepath.Join(shared, "x.db"))); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	wantMode(t, shared, 0o755)

	// Same boundary through the environment variable.
	t.Setenv("DEMO_DB_PATH", filepath.Join(shared, "env.db"))
	if _, err := Resolve("demo"); err != nil {
		t.Fatal(err)
	}
	wantMode(t, shared, 0o755)

	// A not-yet-existing override directory is still created.
	fresh := filepath.Join(shared, "fresh")
	if _, err := Resolve("demo", WithDBOverride(filepath.Join(fresh, "x.db"))); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("override dir not created: %v", err)
	}
}

func TestMaterializeDoesNotFollowSymlinkedRoot(t *testing.T) {
	skipNoModes(t)
	home := hermeticHome(t)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(home, ".cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(cache, "demo")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	l, err := Resolve("demo")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	wantMode(t, outside, 0o755) // symlink target untouched
	wantMode(t, l.DataDir(), DirMode)
}

func TestSelectWorkspaceDoesNotFollowSymlinkedPointer(t *testing.T) {
	skipNoModes(t)
	hermeticHome(t)
	l, err := Resolve("demo")
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(l.StateDir(), activeWorkspaceFile)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := l.SelectWorkspace("staging"); err != nil {
		t.Fatalf("SelectWorkspace: %v", err)
	}
	wantMode(t, target, 0o644)
}

func TestChmodOwnedDegradesOnNonOwnedPath(t *testing.T) {
	skipNoModes(t)
	if os.Geteuid() == 0 {
		t.Skip("root may chmod anything")
	}
	root := "/usr"
	info, err := os.Stat(root)
	if err != nil {
		t.Skip("no /usr")
	}
	before := info.Mode().Perm()
	if err := chmodOwned(root, DirMode, nil); err != nil {
		t.Fatalf("chmodOwned on a non-owned path must degrade, got %v", err)
	}
	wantMode(t, root, before)
}

func TestChmodOwnedMissingPathIsAnError(t *testing.T) {
	skipNoModes(t)
	if err := chmodOwned(filepath.Join(t.TempDir(), "nope"), DirMode, nil); err == nil {
		t.Error("expected error for a missing path")
	}
}

func TestWithoutMaterializeCreatesNothing(t *testing.T) {
	home := hermeticHome(t)
	if _, err := Resolve("demo", WithoutMaterialize()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".local")); !os.IsNotExist(err) {
		t.Errorf("WithoutMaterialize created directories: %v", err)
	}
}

// nonOwnedRoot returns a directory the current user cannot chmod, or skips.
func nonOwnedRoot(t *testing.T) string {
	t.Helper()
	skipNoModes(t)
	if os.Geteuid() == 0 {
		t.Skip("root may chmod anything")
	}
	if _, err := os.Stat("/usr"); err != nil {
		t.Skip("no /usr")
	}
	return "/usr"
}

func TestChmodOwnedWarnsWhenPermissionDenied(t *testing.T) {
	root := nonOwnedRoot(t)
	var msgs []string
	w := &warner{logf: func(format string, args ...any) {
		msgs = append(msgs, fmt.Sprintf(format, args...))
	}}
	if err := chmodOwned(root, DirMode, w); err != nil {
		t.Fatalf("chmodOwned: %v", err)
	}
	if len(msgs) != 1 || !strings.Contains(msgs[0], root) || !strings.Contains(msgs[0], "0700") {
		t.Errorf("warnings = %q, want one naming %s and the mode", msgs, root)
	}
}

func TestChmodOwnedDoesNotWarnOnSuccessOrCorrectMode(t *testing.T) {
	skipNoModes(t)
	calls := 0
	w := &warner{logf: func(string, ...any) { calls++ }}
	d := filepath.Join(t.TempDir(), "d")
	if err := os.Mkdir(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := chmodOwned(d, DirMode, w); err != nil { // tightening succeeds
		t.Fatal(err)
	}
	if err := chmodOwned(d, DirMode, w); err != nil { // already right
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(d, link); err == nil { // skipped symlink
		if err := chmodOwned(link, DirMode, w); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 0 {
		t.Errorf("warn called %d times, want 0", calls)
	}
}

func TestEnsureOwnedDirWarnsOnPermissionDenied(t *testing.T) {
	root := nonOwnedRoot(t)
	var got []string
	w := &warner{logf: func(f string, a ...any) { got = append(got, fmt.Sprintf(f, a...)) }}
	if err := ensureOwnedDir(root, w); err != nil {
		t.Fatalf("ensureOwnedDir: %v", err)
	}
	if len(got) != 1 || !strings.Contains(got[0], root) {
		t.Errorf("warnings = %q", got)
	}
}

func TestWithWarnIsCarriedByLayout(t *testing.T) {
	hermeticHome(t)
	l, err := Resolve("demo", WithWarn(func(string, ...any) {}))
	if err != nil {
		t.Fatal(err)
	}
	if l.warn == nil {
		t.Error("Layout does not carry the WithWarn callback, SelectWorkspace could not report")
	}
	if l2, _ := Resolve("demo"); l2.warn != nil {
		t.Error("default Layout must have no warner")
	}
}

func TestResolveWithWarnHealthyInstallIsSilent(t *testing.T) {
	hermeticHome(t)
	calls := 0
	l, err := Resolve("demo", WithWarn(func(string, ...any) { calls++ }))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.SelectWorkspace("staging"); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Errorf("warn called %d times on a healthy install", calls)
	}
}

func TestWarnDefaultsAndNilAndPanicAreSafe(t *testing.T) {
	hermeticHome(t)
	if _, err := Resolve("demo"); err != nil { // no option
		t.Fatal(err)
	}
	if _, err := Resolve("demo", WithWarn(nil)); err != nil {
		t.Fatal(err)
	}
	var zero Layout // zero Layout has a nil warner
	zero.warn.warnf("x %d", 1)
	boom := &warner{logf: func(string, ...any) { panic("boom") }}
	boom.warnf("x") // must not propagate
	if os.Geteuid() != 0 && runtime.GOOS != "windows" {
		if err := chmodOwned("/usr", DirMode, boom); err != nil {
			t.Fatalf("panicking warn broke chmodOwned: %v", err)
		}
	}
}
