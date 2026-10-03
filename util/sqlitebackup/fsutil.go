package sqlitebackup

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// reserveTemp creates a uniquely-named empty file in dir and returns its path.
func reserveTemp(dir, base string) (string, error) {
	f, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("sqlitebackup: create temp file: %w", err)
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		removeAll(name)
		return "", fmt.Errorf("sqlitebackup: close temp file: %w", err)
	}
	return name, nil
}

func removeAll(paths ...string) {
	for _, p := range paths {
		_ = os.Remove(p)
	}
}

func statDigest(path string) (size int64, sum string, err error) {
	f, err := os.Open(path) //nolint:gosec // caller-named path
	if err != nil {
		return 0, "", fmt.Errorf("sqlitebackup: open %q for digest: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return 0, "", fmt.Errorf("sqlitebackup: digest %q: %w", path, err)
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0) //nolint:gosec // caller-named path
	if err != nil {
		return fmt.Errorf("sqlitebackup: open %q to sync: %w", path, err)
	}
	syncErr := f.Sync()
	if err := errors.Join(syncErr, f.Close()); err != nil {
		return fmt.Errorf("sqlitebackup: sync %q: %w", path, err)
	}
	return nil
}

// syncDir makes a rename or link durable. Best effort: some platforms cannot
// fsync a directory.
func syncDir(dir string) {
	d, err := os.Open(dir) //nolint:gosec // caller-named path
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// publishNoClobber moves the finished file src to dst without ever replacing
// something already at dst. A hard link is atomic and fails if dst exists; on
// filesystems without hard links it falls back to a check followed by rename.
func publishNoClobber(src, dst string) error {
	err := os.Link(src, dst)
	switch {
	case err == nil:
		_ = os.Remove(src)
		return nil
	case errors.Is(err, os.ErrExist):
		return fmt.Errorf("%w: %q", ErrDestinationExists, dst)
	}
	if _, statErr := os.Lstat(dst); statErr == nil {
		return fmt.Errorf("%w: %q", ErrDestinationExists, dst)
	}
	if rnErr := os.Rename(src, dst); rnErr != nil {
		return fmt.Errorf("sqlitebackup: publish backup: %w", rnErr)
	}
	return nil
}

var sidecarSuffixes = []string{"-wal", "-shm"}

// noteSidecars records which -wal/-shm files exist beside path and returns a
// function removing only the ones created after the call. Opening a WAL-mode
// database, even read-only, can materialize a -shm; a stray sidecar would
// otherwise make a later check of the same file behave differently.
func noteSidecars(path string) (cleanup func()) {
	pre := map[string]bool{}
	for _, s := range sidecarSuffixes {
		if _, err := os.Lstat(path + s); err == nil {
			pre[s] = true
		}
	}
	return func() {
		for _, s := range sidecarSuffixes {
			if !pre[s] {
				_ = os.Remove(path + s)
			}
		}
	}
}

func resolve(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("sqlitebackup: resolve %q: %w", path, err)
	}
	return abs, nil
}
