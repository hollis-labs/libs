package paths

import (
	"testing"
)

func TestWorkspacesListAndSelect(t *testing.T) {
	hermeticHome(t)

	l, err := Resolve("demo")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// Resolve materializes the active ("default") workspace.
	got, err := l.Workspaces()
	if err != nil {
		t.Fatalf("Workspaces: %v", err)
	}
	if len(got) != 1 || got[0].Name != "default" {
		t.Fatalf("Workspaces = %+v, want exactly [default]", got)
	}

	if err := l.SelectWorkspace("staging"); err != nil {
		t.Fatalf("SelectWorkspace: %v", err)
	}
	got, err = l.Workspaces()
	if err != nil {
		t.Fatalf("Workspaces after select: %v", err)
	}
	if len(got) != 2 || got[0].Name != "default" || got[1].Name != "staging" {
		t.Fatalf("Workspaces = %+v, want sorted [default staging]", got)
	}

	// The persisted pointer takes effect on the next Resolve.
	next, err := Resolve("demo")
	if err != nil {
		t.Fatalf("Resolve after select: %v", err)
	}
	if next.Workspace().Name != "staging" {
		t.Errorf("active workspace = %q, want staging", next.Workspace().Name)
	}
}

func TestWorkspacesMissingDirIsEmpty(t *testing.T) {
	hermeticHome(t)
	l, err := Resolve("demo", WithoutMaterialize())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got, err := l.Workspaces()
	if err != nil {
		t.Fatalf("Workspaces: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Workspaces = %+v, want empty (no workspaces dir)", got)
	}
}

func TestSelectWorkspaceValidation(t *testing.T) {
	hermeticHome(t)
	l, err := Resolve("demo")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for _, bad := range []string{"", "   ", "a/b", `a\b`} {
		if err := l.SelectWorkspace(bad); err == nil {
			t.Errorf("SelectWorkspace(%q) = nil error, want rejection", bad)
		}
	}
}

func TestResolveWorkspaceRecord(t *testing.T) {
	hermeticHome(t)
	l, err := Resolve("demo")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	ws := l.ResolveWorkspace("scratch")
	if ws.Name != "scratch" {
		t.Errorf("ResolveWorkspace name = %q, want scratch", ws.Name)
	}
	if ws.DBPath == "" || ws.Dir == "" {
		t.Errorf("ResolveWorkspace returned empty paths: %+v", ws)
	}
}
