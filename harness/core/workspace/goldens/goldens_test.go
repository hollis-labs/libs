package goldens

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Verify the comparison input catches the regressions the corpus protects:
// byte changes, chmod-only changes, and removed empty directories.
func TestSnapshotPreservesBytesModesAndEmptyDirectories(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "data.bin")
	empty := filepath.Join(root, "empty")
	if err := os.WriteFile(file, []byte{0, 255, 1}, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(empty, 0750); err != nil {
		t.Fatal(err)
	}
	before, err := Snapshot(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct {
		name  string
		apply func() error
		undo  func() error
	}{
		{"bytes", func() error { return os.WriteFile(file, []byte{0, 255, 2}, 0600) }, func() error { return os.WriteFile(file, []byte{0, 255, 1}, 0600) }},
		{"mode", func() error { return os.Chmod(file, 0700) }, func() error { return os.Chmod(file, 0600) }},
		{"empty directory", func() error { return os.Remove(empty) }, func() error { return os.Mkdir(empty, 0750) }},
		{"directory mode", func() error { return os.Chmod(empty, 0700) }, func() error { return os.Chmod(empty, 0750) }},
	} {
		t.Run(change.name, func(t *testing.T) {
			if err := change.apply(); err != nil {
				t.Fatal(err)
			}
			after, err := Snapshot(root, nil)
			if err != nil {
				t.Fatal(err)
			}
			if reflect.DeepEqual(before, after) {
				t.Error("snapshot lost a material tree change")
			}
			if err := change.undo(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestSnapshotRefusesSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink("missing", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := Snapshot(root, nil); err == nil {
		t.Error("snapshot accepted an unsupported symlink")
	}
}
