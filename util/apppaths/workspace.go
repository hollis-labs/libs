package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// activeWorkspaceFile is the basename, under StateDir, of the small text
// file that persists the active-workspace pointer.
const activeWorkspaceFile = "active_workspace"

// defaultWorkspace is the canonical workspace used when nothing else selects
// one.
const defaultWorkspace = "default"

// Workspace is a named, isolated data area within an app's DataDir. Every
// app has at least the "default" workspace.
type Workspace struct {
	// Name is the workspace identifier.
	Name string
	// Dir is the workspace's directory (<DataDir>/workspaces/<name>).
	Dir string
	// DBPath is the workspace's main database file path.
	DBPath string
}

// workspaceFor builds the Workspace record for name under dataDir.
func workspaceFor(dataDir, name string) Workspace {
	dir := filepath.Join(dataDir, "workspaces", name)
	return Workspace{
		Name:   name,
		Dir:    dir,
		DBPath: filepath.Join(dir, "main.db"),
	}
}

// Workspaces lists every workspace registered under the Layout's DataDir,
// ordered by name. A missing workspaces directory yields an empty list.
func (l Layout) Workspaces() ([]Workspace, error) {
	root := filepath.Join(l.dataDir, "workspaces")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("apppaths: list workspaces: %w", err)
	}
	out := make([]Workspace, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, workspaceFor(l.dataDir, e.Name()))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ResolveWorkspace returns the Workspace record for name without changing
// the active-workspace pointer. The directory need not exist yet.
func (l Layout) ResolveWorkspace(name string) Workspace {
	return workspaceFor(l.dataDir, strings.TrimSpace(name))
}

// SelectWorkspace persists name as the active-workspace pointer (a text file
// under StateDir) and ensures the workspace directory exists. The change
// takes effect on the next Resolve. WithWorkspace and the <APP>_WORKSPACE
// environment variable still take precedence over the persisted pointer.
func (l Layout) SelectWorkspace(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("apppaths: workspace name must not be empty")
	}
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("apppaths: workspace name %q must not contain a path separator", name)
	}
	if err := os.MkdirAll(workspaceFor(l.dataDir, name).Dir, 0o755); err != nil {
		return fmt.Errorf("apppaths: create workspace %q: %w", name, err)
	}
	if err := os.MkdirAll(l.stateDir, 0o755); err != nil {
		return fmt.Errorf("apppaths: prepare state dir: %w", err)
	}
	file := filepath.Join(l.stateDir, activeWorkspaceFile)
	if err := os.WriteFile(file, []byte(name+"\n"), 0o644); err != nil {
		return fmt.Errorf("apppaths: write active-workspace pointer: %w", err)
	}
	return nil
}

// readActiveWorkspace returns the persisted active-workspace name, or "" if
// the pointer file is absent or unreadable.
func readActiveWorkspace(stateDir string) string {
	raw, err := os.ReadFile(filepath.Join(stateDir, activeWorkspaceFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}
