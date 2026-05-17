package paths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/adrg/xdg"
)

// Resolve builds an immutable Layout for appName. It is the single entry
// point of the package: call it once at startup and inject the result
// downward.
//
// Resolution runs in a fixed order: base roots, then legacy adoption (before
// any directory is created), then the active workspace, then the main
// database path, and finally directory materialization.
//
// The main database path follows the precedence: WithDBOverride >
// <APP>_DB_PATH env var > active workspace's database > the "default"
// workspace. The active workspace follows: WithWorkspace > <APP>_WORKSPACE
// env var > persisted active-workspace pointer > "default".
func Resolve(appName string, opts ...Option) (Layout, error) {
	app := strings.TrimSpace(appName)
	if app == "" {
		return Layout{}, errors.New("apppaths: appName must not be empty")
	}
	if strings.ContainsAny(app, `/\`) {
		return Layout{}, fmt.Errorf("apppaths: appName %q must not contain a path separator", app)
	}

	cfg := config{warnWriter: os.Stderr}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.warnWriter == nil {
		cfg.warnWriter = os.Stderr
	}

	prefix := envPrefix(app)

	cwd := ""
	if cfg.projectMode {
		wd, err := os.Getwd()
		if err != nil {
			return Layout{}, fmt.Errorf("apppaths: resolve project-mode working dir: %w", err)
		}
		cwd = wd
	}
	bases := xdgBases()

	// Legacy adoption runs before materialization so the move-if-target-absent
	// check observes the real on-disk state rather than freshly-created roots.
	if len(cfg.legacyNames) > 0 {
		adoptLegacy(cfg.warnWriter, cfg.projectMode, cwd, bases, app, cfg.legacyNames)
	}
	dataDir, stateDir, cacheDir, configDir := rootsFor(cfg.projectMode, cwd, bases, app)

	// Active workspace: explicit option > env var > persisted pointer > default.
	ws := cfg.workspace
	if ws == "" {
		ws = strings.TrimSpace(os.Getenv(prefix + "_WORKSPACE"))
	}
	if ws == "" {
		ws = readActiveWorkspace(stateDir)
	}
	if ws == "" {
		ws = defaultWorkspace
	}
	workspace := workspaceFor(dataDir, ws)

	// Main database: explicit override > env var > active workspace's DB.
	mainDB := cfg.dbOverride
	if mainDB == "" {
		mainDB = strings.TrimSpace(os.Getenv(prefix + "_DB_PATH"))
	}
	if mainDB == "" {
		mainDB = workspace.DBPath
	}

	layout := Layout{
		app:         app,
		dataDir:     dataDir,
		stateDir:    stateDir,
		cacheDir:    cacheDir,
		configDir:   configDir,
		mainDB:      mainDB,
		workspace:   workspace,
		projectMode: cfg.projectMode,
	}
	if !cfg.skipMaterialize {
		if err := layout.materialize(); err != nil {
			return Layout{}, err
		}
	}
	return layout, nil
}

// xdgBaseSet holds the four resolved XDG base directories (the parents of the
// per-app roots).
type xdgBaseSet struct {
	data, state, cache, config string
}

// xdgBases resolves the four XDG base directories. Each honors its
// $XDG_*_HOME environment variable when set and otherwise falls back to the
// Linux-style default on every GOOS. adrg/xdg's own per-OS defaults (e.g.
// ~/Library on macOS) are deliberately bypassed so a hollis-labs app keeps
// one portable layout everywhere.
func xdgBases() xdgBaseSet {
	return xdgBaseSet{
		data:   xdgBase("XDG_DATA_HOME", filepath.Join(".local", "share")),
		state:  xdgBase("XDG_STATE_HOME", filepath.Join(".local", "state")),
		cache:  xdgBase("XDG_CACHE_HOME", ".cache"),
		config: xdgBase("XDG_CONFIG_HOME", ".config"),
	}
}

// xdgBase returns the directory named by envVar when set, else home/linuxRel.
func xdgBase(envVar, linuxRel string) string {
	if v := strings.TrimSpace(os.Getenv(envVar)); v != "" {
		return v
	}
	return filepath.Join(homeDir(), linuxRel)
}

// homeDir resolves the user home directory. os.UserHomeDir honors $HOME, so
// the result tracks the environment; adrg/xdg's xdg.Home is the fallback
// when the environment yields nothing.
func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil && strings.TrimSpace(h) != "" {
		return h
	}
	return xdg.Home
}

// rootsFor returns the four base roots for an app (or legacy) name under
// either the CWD-local project layout or the XDG layout.
func rootsFor(projectMode bool, cwd string, bases xdgBaseSet, name string) (data, state, cache, config string) {
	if projectMode {
		base := filepath.Join(cwd, "."+name)
		return filepath.Join(base, "data"),
			filepath.Join(base, "state"),
			filepath.Join(base, "cache"),
			filepath.Join(base, "config")
	}
	return filepath.Join(bases.data, name),
		filepath.Join(bases.state, name),
		filepath.Join(bases.cache, name),
		filepath.Join(bases.config, name)
}

// envPrefix derives the per-app environment-variable prefix from appName:
// upper-cased, with every non-alphanumeric rune replaced by '_'. For example
// "go-apppaths" yields "GO_APPPATHS", giving GO_APPPATHS_DB_PATH and
// GO_APPPATHS_WORKSPACE.
func envPrefix(appName string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(appName) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
