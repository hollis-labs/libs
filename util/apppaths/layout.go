package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Layout is the immutable result of Resolve: the four XDG base roots for an
// app, its resolved main database path, and its active workspace. Resolve it
// once at startup and inject it downward — there is no global singleton and
// no lazy resolution.
type Layout struct {
	app         string
	dataDir     string
	stateDir    string
	cacheDir    string
	configDir   string
	mainDB      string
	workspace   Workspace
	projectMode bool
	// warn is a pointer, not a func, so Layout stays comparable with ==.
	warn *warner
}

// App returns the application name the Layout was resolved for.
func (l Layout) App() string { return l.app }

// DataDir returns the data root (~/.local/share/<app> by default).
func (l Layout) DataDir() string { return l.dataDir }

// StateDir returns the state root (~/.local/state/<app> by default).
func (l Layout) StateDir() string { return l.stateDir }

// CacheDir returns the cache root (~/.cache/<app> by default).
func (l Layout) CacheDir() string { return l.cacheDir }

// ConfigDir returns the config root (~/.config/<app> by default).
func (l Layout) ConfigDir() string { return l.configDir }

// MainDB returns the resolved path to the application's main database file.
// apppaths resolves the path only; opening the database is the caller's job.
func (l Layout) MainDB() string { return l.mainDB }

// Workspace returns the active workspace.
func (l Layout) Workspace() Workspace { return l.workspace }

// ProjectMode reports whether the Layout uses the CWD-local project layout.
func (l Layout) ProjectMode() bool { return l.projectMode }

// Entry is one labeled value in a Layout's Describe output.
type Entry struct {
	Label string
	Value string
}

// Describe returns the Layout's resolved values as an ordered, printable
// list — the data backing an `<app> path` introspection subcommand.
func (l Layout) Describe() []Entry {
	return []Entry{
		{"app", l.app},
		{"project-mode", strconv.FormatBool(l.projectMode)},
		{"data", l.dataDir},
		{"state", l.stateDir},
		{"cache", l.cacheDir},
		{"config", l.configDir},
		{"workspace", l.workspace.Name},
		{"workspace-dir", l.workspace.Dir},
		{"main-db", l.mainDB},
	}
}

// materialize creates the Layout's app-owned directories and forces DirMode
// (0o700) onto each of them, including ones that already exist at a looser
// mode — an install created by an earlier release retightens on the next
// Resolve. It is idempotent.
//
// The app-owned set is the four base roots, the workspace directory and the
// workspace database's directory. filepath.Dir(mainDB) is deliberately not
// forced to 0o700 when it differs from that set: WithDBOverride and
// <APP>_DB_PATH can point it at a path the operator chose and may share with
// other tools, so it is only created (0o755 before umask), never chmodded.
func (l Layout) materialize() error {
	owned := []string{
		l.dataDir, l.stateDir, l.cacheDir, l.configDir,
		l.workspace.Dir, filepath.Dir(l.workspace.DBPath),
	}
	for _, dir := range owned {
		if err := ensureOwnedDir(dir, l.warn); err != nil {
			return fmt.Errorf("apppaths: materialize %q: %w", dir, err)
		}
	}
	if dbDir := filepath.Dir(l.mainDB); dbDir != filepath.Dir(l.workspace.DBPath) {
		if err := os.MkdirAll(dbDir, 0o755); err != nil {
			return fmt.Errorf("apppaths: materialize %q: %w", dbDir, err)
		}
	}
	return nil
}
