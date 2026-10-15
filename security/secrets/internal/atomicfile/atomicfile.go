// Package atomicfile writes a private file so a reader never sees a partial
// value: the content goes to a temporary file in the same directory (created
// 0600 with O_EXCL), is fsynced, and is renamed over the target.
package atomicfile

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// DirMode and FileMode are the only modes this package creates.
const (
	DirMode  os.FileMode = 0o700
	FileMode os.FileMode = 0o600
)

// Write replaces path with data. Missing parent directories are created 0700.
// On any failure the temporary file is removed and the target is untouched.
func Write(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, DirMode); err != nil {
		return err
	}
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+filepath.Base(path)+"."+hex.EncodeToString(suffix[:])+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, FileMode) //nolint:gosec // G304: caller-validated path
	if err != nil {
		return err
	}
	cleanup := func() { _ = os.Remove(tmp) }
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		cleanup()
		return err
	}
	if err := f.Chmod(FileMode); err != nil { // umask cannot loosen it, but be explicit
		_ = f.Close()
		cleanup()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		cleanup()
		return err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		cleanup()
		return fmt.Errorf("rename: %w", err)
	}
	syncDir(dir)
	return nil
}

// syncDir makes the rename durable. Best effort: not every platform can fsync
// a directory.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil { //nolint:gosec // G304
		_ = d.Sync()
		_ = d.Close()
	}
}
