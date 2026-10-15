package paths

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// legacyDataDir returns the data root a legacy app named `name` would have
// used under the hermetic home, and creates it with a marker file.
func seedLegacyData(t *testing.T, home, name, marker string) string {
	t.Helper()
	dir := filepath.Join(home, ".local", "share", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("seed legacy %q: %v", name, err)
	}
	if err := os.WriteFile(filepath.Join(dir, marker), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed marker in %q: %v", name, err)
	}
	return dir
}

func TestAdoptionMovesLegacy(t *testing.T) {
	home := hermeticHome(t)
	legacyData := seedLegacyData(t, home, "oldapp", "legacy-marker")

	var warn bytes.Buffer
	l, err := Resolve("newapp", WithLegacyNames("oldapp"), WithWarnWriter(&warn))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if _, err := os.Stat(filepath.Join(l.DataDir(), "legacy-marker")); err != nil {
		t.Errorf("legacy marker not migrated into %q: %v", l.DataDir(), err)
	}
	if _, err := os.Stat(legacyData); !os.IsNotExist(err) {
		t.Errorf("legacy directory %q should be gone after adoption", legacyData)
	}
	if !strings.Contains(warn.String(), "migrated legacy directory") {
		t.Errorf("expected a migration notice, got %q", warn.String())
	}
}

func TestAdoptionConflictWarnsAndPreservesBoth(t *testing.T) {
	home := hermeticHome(t)
	legacyData := seedLegacyData(t, home, "oldapp", "legacy-marker")

	// Pre-create the target so legacy and target collide.
	targetData := filepath.Join(home, ".local", "share", "newapp")
	if err := os.MkdirAll(targetData, 0o755); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetData, "target-marker"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed target marker: %v", err)
	}

	var warn bytes.Buffer
	if _, err := Resolve("newapp", WithLegacyNames("oldapp"), WithWarnWriter(&warn)); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// Never clobber: both directories keep their own marker.
	if _, err := os.Stat(filepath.Join(legacyData, "legacy-marker")); err != nil {
		t.Errorf("legacy marker lost — adoption clobbered the source: %v", err)
	}
	if _, err := os.Stat(filepath.Join(targetData, "target-marker")); err != nil {
		t.Errorf("target marker lost — adoption clobbered the target: %v", err)
	}
	if !strings.Contains(warn.String(), "both exist") {
		t.Errorf("expected a conflict warning, got %q", warn.String())
	}
}

func TestAdoptionNoLegacyIsSilentNoop(t *testing.T) {
	hermeticHome(t)
	var warn bytes.Buffer
	if _, err := Resolve("newapp", WithLegacyNames("ghost"), WithWarnWriter(&warn)); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if warn.Len() != 0 {
		t.Errorf("expected no warnings when no legacy directory exists, got %q", warn.String())
	}
}
