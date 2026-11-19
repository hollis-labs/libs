package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hollis-labs/libs/core/store"
)

func TestSQLiteSettingsReportTheConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.db")
	off := false
	s, err := store.Open(ctx, store.Config{Driver: store.DriverSQLite, SQLite: store.SQLiteConfig{
		Path: path, Synchronous: "normal", BusyTimeout: 250 * time.Millisecond, ForeignKeys: &off, ReadConns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	want := store.SQLiteSettings{Synchronous: "NORMAL", JournalMode: "wal", BusyTimeout: 250 * time.Millisecond, ForeignKeys: false, MainPath: resolved(t, path)}

	check := func(name string, got store.SQLiteSettings, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got.MainPath = resolved(t, got.MainPath)
		if got != want {
			t.Fatalf("%s: got %+v\nwant %+v", name, got, want)
		}
	}
	got, err := s.SQLiteSettings(ctx)
	check("writer", got, err)
	got, err = store.ReadSQLiteSettings(ctx, s.Reader())
	check("reader", got, err)
	err = s.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
		got, err := store.ReadSQLiteSettings(ctx, tx)
		check("inside WriteTx", got, err)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = s.ReadTx(ctx, func(ctx context.Context, q store.Querier) error {
		got, err := store.ReadSQLiteSettings(ctx, q)
		check("inside ReadTx", got, err)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// resolved is p made absolute with symlinks resolved: SQLite may report the
// main file either way (macOS's temp dir sits behind /var -> /private/var).
func resolved(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	r, err := filepath.EvalSymlinks(abs)
	if err != nil {
		t.Fatalf("resolve %s: %v", p, err)
	}
	return r
}

// The macOS case on any OS: the database is opened through a symlinked
// directory, and MainPath names the same file once both are resolved.
func TestSQLiteSettingsMainPathThroughASymlink(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	path := filepath.Join(link, "s.db")
	s, err := store.Open(ctx, store.Config{Driver: store.DriverSQLite, SQLite: store.SQLiteConfig{Path: path}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	got, err := s.SQLiteSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if resolved(t, got.MainPath) != resolved(t, path) {
		t.Fatalf("MainPath %q is not the file opened as %q", got.MainPath, path)
	}
}

func TestSQLiteSettingsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "defaults.db")
	s, err := store.Open(ctx, store.Config{Driver: store.DriverSQLite, SQLite: store.SQLiteConfig{Path: path}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	got, err := s.SQLiteSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Synchronous != "FULL" || got.JournalMode != "wal" || got.BusyTimeout != 5*time.Second || !got.ForeignKeys || got.MainPath == "" {
		t.Fatalf("defaults: %+v", got)
	}
}

func TestSQLiteSettingsInMemoryAndDeleteJournal(t *testing.T) {
	mem, err := store.Open(ctx, store.Config{Driver: store.DriverSQLite, SQLite: store.SQLiteConfig{Path: ":memory:"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	got, err := mem.SQLiteSettings(ctx)
	if err != nil || got.MainPath != "" || got.JournalMode != "memory" {
		t.Fatalf("in memory: %+v %v", got, err)
	}
	del, err := store.Open(ctx, store.Config{Driver: store.DriverSQLite, SQLite: store.SQLiteConfig{
		Path: filepath.Join(t.TempDir(), "d.db"), Journal: "delete", Synchronous: "EXTRA"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = del.Close() })
	got, err = del.SQLiteSettings(ctx)
	if err != nil || got.JournalMode != "delete" || got.Synchronous != "EXTRA" {
		t.Fatalf("delete journal: %+v %v", got, err)
	}
}

type pgQuerier struct{ store.Querier }

func (pgQuerier) Dialect() store.Dialect {
	d, _ := store.DialectFor(store.DriverPostgres)
	return d
}

func TestSQLiteSettingsRefusePostgres(t *testing.T) {
	if _, err := store.ReadSQLiteSettings(ctx, pgQuerier{}); !errors.Is(err, store.ErrSQLiteOnly) {
		t.Fatalf("postgres: %v, want ErrSQLiteOnly", err)
	}
}
