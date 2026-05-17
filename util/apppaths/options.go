package paths

import (
	"io"
	"strings"
)

// Option customizes how Resolve builds a Layout. Options are applied in the
// order passed to Resolve.
type Option func(*config)

// config is the mutable accumulator the Option set writes into; Resolve
// consumes it once and produces an immutable Layout.
type config struct {
	workspace       string
	dbOverride      string
	projectMode     bool
	skipMaterialize bool
	legacyNames     []string
	warnWriter      io.Writer
}

// WithWorkspace pins the active workspace by name. It overrides both the
// <APP>_WORKSPACE environment variable and the persisted active-workspace
// pointer.
func WithWorkspace(name string) Option {
	return func(c *config) { c.workspace = strings.TrimSpace(name) }
}

// WithDBOverride pins an explicit path for the main database, taking
// precedence over the <APP>_DB_PATH environment variable and the workspace
// default.
func WithDBOverride(path string) Option {
	return func(c *config) { c.dbOverride = strings.TrimSpace(path) }
}

// WithProjectMode switches the four base roots to a CWD-local layout
// (<cwd>/.<app>/{data,state,cache,config}) instead of the XDG layout. It is
// opt-in — handy for self-contained, project-scoped runs.
func WithProjectMode() Option {
	return func(c *config) { c.projectMode = true }
}

// WithLegacyNames registers prior app names whose directories should be
// adopted on Resolve. For each legacy name, every base root is migrated with
// an idempotent move-if-target-absent; a legacy/target collision is reported
// and left untouched. See the package's adoption rules for detail.
func WithLegacyNames(names ...string) Option {
	return func(c *config) { c.legacyNames = append(c.legacyNames, names...) }
}

// WithWarnWriter redirects adoption warnings (legacy/target collisions and
// migration failures) to w. The default is os.Stderr.
func WithWarnWriter(w io.Writer) Option {
	return func(c *config) { c.warnWriter = w }
}

// WithoutMaterialize skips directory creation. By default Resolve MkdirAll's
// the resolved roots; an introspection-only caller (e.g. an `<app> path`
// subcommand) can opt out.
func WithoutMaterialize() Option {
	return func(c *config) { c.skipMaterialize = true }
}
