package paths

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// adoptLegacy migrates each legacy app's base roots onto the current app's
// roots. For every legacy name and every base root the rule is identical and
// idempotent:
//
//   - legacy directory absent          → do nothing;
//   - legacy present, target absent    → move legacy onto target;
//   - legacy present, target present   → warn and leave both untouched.
//
// A run never clobbers existing data; adoption is safe to call on every
// startup.
func adoptLegacy(w io.Writer, projectMode bool, cwd string, bases xdgBaseSet, app string, legacyNames []string) {
	appData, appState, appCache, appConfig := rootsFor(projectMode, cwd, bases, app)
	appRoots := [4]string{appData, appState, appCache, appConfig}
	for _, legacy := range legacyNames {
		if legacy == "" || legacy == app {
			continue
		}
		legData, legState, legCache, legConfig := rootsFor(projectMode, cwd, bases, legacy)
		legacyRoots := [4]string{legData, legState, legCache, legConfig}
		for i := range appRoots {
			adoptRoot(w, legacyRoots[i], appRoots[i])
		}
	}
}

// adoptRoot performs the move-if-target-absent migration for a single
// directory pair.
func adoptRoot(w io.Writer, legacy, target string) {
	info, err := os.Stat(legacy)
	if err != nil || !info.IsDir() {
		return // no legacy directory — nothing to adopt
	}
	if _, statErr := os.Stat(target); statErr == nil {
		warnf(w, "apppaths: WARNING: legacy %q and current %q both exist; "+
			"not migrating — resolve manually", legacy, target)
		return
	}
	if mkErr := os.MkdirAll(filepath.Dir(target), 0o755); mkErr != nil {
		warnf(w, "apppaths: WARNING: cannot prepare %q for legacy migration: %v", target, mkErr)
		return
	}
	if mvErr := os.Rename(legacy, target); mvErr != nil {
		warnf(w, "apppaths: WARNING: failed migrating %q -> %q: %v", legacy, target, mvErr)
		return
	}
	warnf(w, "apppaths: migrated legacy directory %q -> %q", legacy, target)
}

// warnf writes a single newline-terminated line to w, discarding the write
// result — adoption diagnostics are best-effort.
func warnf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format+"\n", args...)
}
