//go:build linux || darwin

package workspace_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/libs/harness/core/workspace"
)

func TestCanonicalRootRefResolvesSymlinkedSpelling(t *testing.T) {
	physical, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(physical, "store", "agent"), 0o700); err != nil {
		t.Fatal(err)
	}
	logical := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(filepath.Join(physical, "store"), logical); err != nil {
		t.Fatal(err)
	}
	ref := workspace.RootRef{ID: "r", Owner: "o", Provenance: "p", AllowedBase: logical, Path: filepath.Join(logical, "agent")}

	var refusal *workspace.Refusal
	if _, err := workspace.InspectRoot(ref); !errors.As(err, &refusal) || refusal.Code != workspace.CodeNoncanonicalRoot {
		t.Fatalf("InspectRoot must keep refusing the symlink spelling, got %v", err)
	}
	got, err := workspace.CanonicalRootRef(ref)
	if err != nil {
		t.Fatal(err)
	}
	if got.AllowedBase != filepath.Join(physical, "store") || got.Path != filepath.Join(physical, "store", "agent") {
		t.Fatalf("not physical: %+v", got)
	}
	if got.ID != ref.ID || got.Owner != ref.Owner || got.Provenance != ref.Provenance {
		t.Fatalf("identity fields changed: %+v", got)
	}
	if _, err := workspace.InspectRoot(got); err != nil {
		t.Fatalf("canonicalized root refused: %v", err)
	}

	// A root that does not exist yet keeps its missing suffix.
	missing := ref
	missing.Path = filepath.Join(logical, "new", "root")
	got, err = workspace.CanonicalRootRef(missing)
	if err != nil || got.Path != filepath.Join(physical, "store", "new", "root") {
		t.Fatalf("missing root: %+v %v", got, err)
	}
}

func TestCanonicalRootRefRefusesEscapeAndUnavailableBase(t *testing.T) {
	physical, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(physical, "base")
	outside := filepath.Join(physical, "outside")
	for _, d := range []string{base, outside} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(base, "escape")); err != nil {
		t.Fatal(err)
	}
	var refusal *workspace.Refusal
	_, err = workspace.CanonicalRootRef(workspace.RootRef{ID: "r", Owner: "o", Provenance: "p", AllowedBase: base, Path: filepath.Join(base, "escape")})
	if !errors.As(err, &refusal) || refusal.Code != workspace.CodeNoncanonicalRoot {
		t.Fatalf("escape through symlink must be refused, got %v", err)
	}
	_, err = workspace.CanonicalRootRef(workspace.RootRef{ID: "r", Owner: "o", Provenance: "p", AllowedBase: filepath.Join(physical, "absent"), Path: filepath.Join(physical, "absent", "x")})
	if !errors.As(err, &refusal) || refusal.Code != workspace.CodeCanonicalBaseUnavailable {
		t.Fatalf("missing base must be refused, got %v", err)
	}
	if _, err = workspace.CanonicalRootRef(workspace.RootRef{AllowedBase: "rel", Path: "rel/x"}); err == nil {
		t.Fatal("relative paths must be refused")
	}
}
