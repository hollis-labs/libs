package paths

import (
	"os"
	"path/filepath"
	"runtime"
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
	if err := chmodOwned(root, DirMode); err != nil {
		t.Fatalf("chmodOwned on a non-owned path must degrade, got %v", err)
	}
	wantMode(t, root, before)
}

func TestChmodOwnedMissingPathIsAnError(t *testing.T) {
	skipNoModes(t)
	if err := chmodOwned(filepath.Join(t.TempDir(), "nope"), DirMode); err == nil {
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
