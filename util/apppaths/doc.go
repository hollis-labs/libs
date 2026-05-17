// Package paths resolves the on-disk layout — data, state, cache, and config
// roots, the main database path, and the active workspace — for hollis-labs
// applications.
//
// Resolve once at startup and inject the returned Layout downward; there is
// no global singleton and no lazy resolution:
//
//	layout, err := paths.Resolve("torque")
//	if err != nil {
//		// handle
//	}
//	db := layout.MainDB()
//
// Base roots follow the XDG layout on every operating system —
// ~/.local/share, ~/.local/state, ~/.cache, ~/.config — honoring the
// $XDG_*_HOME environment variables when set. The macOS/Windows defaults
// adrg/xdg would otherwise pick are deliberately bypassed so a hollis-labs
// app keeps one portable layout everywhere.
//
// The main database path is resolved by precedence: an explicit
// WithDBOverride, then the <APP>_DB_PATH environment variable, then the
// active workspace's database, then the canonical "default" workspace. The
// <APP> prefix is derived from the app name passed to Resolve.
//
// apppaths resolves paths only — it never opens the database and never
// parses application config files.
package paths
