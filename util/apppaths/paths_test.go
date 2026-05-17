package paths

import (
	"os"
	"path/filepath"
	"testing"
)

// hermeticHome points $HOME at a fresh temp dir and clears the XDG and
// per-app environment variables the tests rely on, so resolution is
// deterministic regardless of the host environment.
func hermeticHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, v := range []string{
		"XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_CONFIG_HOME",
		"DEMO_DB_PATH", "DEMO_WORKSPACE",
	} {
		t.Setenv(v, "")
	}
	return home
}

func TestResolveDefaultRoots(t *testing.T) {
	home := hermeticHome(t)

	l, err := Resolve("demo")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	want := []struct {
		name, got, expect string
	}{
		{"data", l.DataDir(), filepath.Join(home, ".local", "share", "demo")},
		{"state", l.StateDir(), filepath.Join(home, ".local", "state", "demo")},
		{"cache", l.CacheDir(), filepath.Join(home, ".cache", "demo")},
		{"config", l.ConfigDir(), filepath.Join(home, ".config", "demo")},
	}
	for _, w := range want {
		if w.got != w.expect {
			t.Errorf("%sDir = %q, want %q", w.name, w.got, w.expect)
		}
		if fi, statErr := os.Stat(w.got); statErr != nil || !fi.IsDir() {
			t.Errorf("%s root not materialized: %q", w.name, w.got)
		}
	}

	wantDB := filepath.Join(home, ".local", "share", "demo", "workspaces", "default", "main.db")
	if l.MainDB() != wantDB {
		t.Errorf("MainDB = %q, want %q", l.MainDB(), wantDB)
	}
	if l.Workspace().Name != "default" {
		t.Errorf("Workspace = %q, want default", l.Workspace().Name)
	}
}

func TestResolveXDGOverride(t *testing.T) {
	hermeticHome(t)
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)

	l, err := Resolve("demo")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if want := filepath.Join(dataHome, "demo"); l.DataDir() != want {
		t.Errorf("DataDir = %q, want %q (honoring XDG_DATA_HOME)", l.DataDir(), want)
	}
	// The roots whose XDG var is unset still use the Linux-style default.
	if filepath.Base(filepath.Dir(l.CacheDir())) != ".cache" {
		t.Errorf("CacheDir = %q, want a ~/.cache/<app> default", l.CacheDir())
	}
}

func TestResolveProjectMode(t *testing.T) {
	hermeticHome(t)
	t.Chdir(t.TempDir())
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	l, err := Resolve("demo", WithProjectMode())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !l.ProjectMode() {
		t.Error("ProjectMode() = false, want true")
	}
	base := filepath.Join(cwd, ".demo")
	if want := filepath.Join(base, "data"); l.DataDir() != want {
		t.Errorf("DataDir = %q, want %q (CWD-local project layout)", l.DataDir(), want)
	}
	if want := filepath.Join(base, "config"); l.ConfigDir() != want {
		t.Errorf("ConfigDir = %q, want %q", l.ConfigDir(), want)
	}
}

func TestResolveDBPrecedence(t *testing.T) {
	t.Run("workspace default", func(t *testing.T) {
		home := hermeticHome(t)
		l, err := Resolve("demo")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		want := filepath.Join(home, ".local", "share", "demo", "workspaces", "default", "main.db")
		if l.MainDB() != want {
			t.Errorf("MainDB = %q, want %q", l.MainDB(), want)
		}
	})

	t.Run("selected workspace", func(t *testing.T) {
		home := hermeticHome(t)
		l, err := Resolve("demo", WithWorkspace("staging"))
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		want := filepath.Join(home, ".local", "share", "demo", "workspaces", "staging", "main.db")
		if l.MainDB() != want {
			t.Errorf("MainDB = %q, want %q", l.MainDB(), want)
		}
	})

	t.Run("env var beats workspace", func(t *testing.T) {
		hermeticHome(t)
		t.Setenv("DEMO_DB_PATH", "/tmp/demo-env.db")
		l, err := Resolve("demo", WithWorkspace("staging"))
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if l.MainDB() != "/tmp/demo-env.db" {
			t.Errorf("MainDB = %q, want the DEMO_DB_PATH value", l.MainDB())
		}
	})

	t.Run("override beats env var", func(t *testing.T) {
		hermeticHome(t)
		t.Setenv("DEMO_DB_PATH", "/tmp/demo-env.db")
		l, err := Resolve("demo", WithDBOverride("/tmp/demo-explicit.db"))
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if l.MainDB() != "/tmp/demo-explicit.db" {
			t.Errorf("MainDB = %q, want the WithDBOverride value", l.MainDB())
		}
	})
}

func TestResolveWorkspacePrecedence(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		hermeticHome(t)
		l, err := Resolve("demo")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if l.Workspace().Name != "default" {
			t.Errorf("Workspace = %q, want default", l.Workspace().Name)
		}
	})

	t.Run("persisted pointer", func(t *testing.T) {
		hermeticHome(t)
		seed, err := Resolve("demo")
		if err != nil {
			t.Fatalf("Resolve seed: %v", err)
		}
		if err := seed.SelectWorkspace("persisted"); err != nil {
			t.Fatalf("SelectWorkspace: %v", err)
		}
		l, err := Resolve("demo")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if l.Workspace().Name != "persisted" {
			t.Errorf("Workspace = %q, want persisted (from state file)", l.Workspace().Name)
		}
	})

	t.Run("env var beats pointer", func(t *testing.T) {
		hermeticHome(t)
		seed, err := Resolve("demo")
		if err != nil {
			t.Fatalf("Resolve seed: %v", err)
		}
		if err := seed.SelectWorkspace("persisted"); err != nil {
			t.Fatalf("SelectWorkspace: %v", err)
		}
		t.Setenv("DEMO_WORKSPACE", "fromenv")
		l, err := Resolve("demo")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if l.Workspace().Name != "fromenv" {
			t.Errorf("Workspace = %q, want fromenv", l.Workspace().Name)
		}
	})

	t.Run("option beats env var", func(t *testing.T) {
		hermeticHome(t)
		t.Setenv("DEMO_WORKSPACE", "fromenv")
		l, err := Resolve("demo", WithWorkspace("explicit"))
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if l.Workspace().Name != "explicit" {
			t.Errorf("Workspace = %q, want explicit", l.Workspace().Name)
		}
	})
}

func TestResolveValidation(t *testing.T) {
	for _, bad := range []string{"", "   ", "with/slash", `with\backslash`} {
		if _, err := Resolve(bad); err == nil {
			t.Errorf("Resolve(%q) = nil error, want rejection", bad)
		}
	}
}

func TestWithoutMaterialize(t *testing.T) {
	hermeticHome(t)
	l, err := Resolve("demo", WithoutMaterialize())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, err := os.Stat(l.DataDir()); !os.IsNotExist(err) {
		t.Errorf("DataDir %q should not exist with WithoutMaterialize", l.DataDir())
	}
}

func TestEnvPrefix(t *testing.T) {
	cases := map[string]string{
		"demo":        "DEMO",
		"go-apppaths": "GO_APPPATHS",
		"My.App":      "MY_APP",
	}
	for in, want := range cases {
		if got := envPrefix(in); got != want {
			t.Errorf("envPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDescribe(t *testing.T) {
	hermeticHome(t)
	l, err := Resolve("demo")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got := map[string]string{}
	for _, e := range l.Describe() {
		got[e.Label] = e.Value
	}
	if got["app"] != "demo" {
		t.Errorf("Describe app = %q, want demo", got["app"])
	}
	if got["main-db"] != l.MainDB() {
		t.Errorf("Describe main-db = %q, want %q", got["main-db"], l.MainDB())
	}
	if got["data"] != l.DataDir() {
		t.Errorf("Describe data = %q, want %q", got["data"], l.DataDir())
	}
}
