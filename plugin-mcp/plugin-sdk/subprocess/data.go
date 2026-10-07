package subprocess

import (
	"os"
	"path/filepath"
)

// DataHelper exposes the plugin's writable data directory. Data written
// here survives plugin updates and reinstalls — it is the "user owns
// this" tier. The SDK deliberately does not expose a SQLite or KV
// helper; plugins that need durable structured storage should open
// their own database file under DataDir().
type DataHelper interface {
	// DataDir returns the absolute path of the plugin's data directory.
	// Plugins may create subdirectories under it.
	DataDir() string

	// DataPath returns a path under DataDir, joined with filepath.Join.
	DataPath(elem ...string) string

	// EnsureDataDir creates the plugin's data directory if it does not
	// exist, with 0o755 permissions.
	EnsureDataDir() error
}

// CacheHelper exposes the plugin's writable cache directory. The cache
// may be wiped by the host at any time; plugins must not rely on cache
// contents surviving a restart. Cache lives on the same filesystem as
// DataDir so atomic renames work for both.
type CacheHelper interface {
	// CacheDir returns the absolute path of the plugin's cache directory.
	CacheDir() string

	// CachePath returns a path under CacheDir, joined with filepath.Join.
	CachePath(elem ...string) string

	// EnsureCacheDir creates the plugin's cache directory if it does
	// not exist, with 0o755 permissions.
	EnsureCacheDir() error
}

type dirHelper struct {
	root string
}

func (d *dirHelper) DataDir() string  { return d.root }
func (d *dirHelper) CacheDir() string { return d.root }
func (d *dirHelper) DataPath(elem ...string) string {
	return filepath.Join(append([]string{d.root}, elem...)...)
}
func (d *dirHelper) CachePath(elem ...string) string {
	return filepath.Join(append([]string{d.root}, elem...)...)
}
func (d *dirHelper) EnsureDataDir() error  { return ensureDir(d.root) }
func (d *dirHelper) EnsureCacheDir() error { return ensureDir(d.root) }

func ensureDir(path string) error {
	return os.MkdirAll(path, 0o755)
}
