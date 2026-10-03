package sqlitebackup

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/hollis-labs/go-sqlite/sqlitekit"
	_ "modernc.org/sqlite"
)

// newSourceDB opens a WAL-mode database in a temp dir with two tables that a
// writer always updates together, so a torn snapshot is detectable.
func newSourceDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "src.db")
	db, err := sqlitekit.OpenWriter(context.Background(), path, sqlitekit.OpenOptions{CreateParentDir: true})
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mustExec(t, db, `CREATE TABLE a (id INTEGER PRIMARY KEY, v TEXT)`)
	mustExec(t, db, `CREATE TABLE b (id INTEGER PRIMARY KEY, v TEXT)`)
	seed(t, db, 0, 200)
	return db, path
}

func seed(t testing.TB, db *sql.DB, from, n int) {
	t.Helper()
	for i := from; i < from+n; i++ {
		if err := insertPair(context.Background(), db, i); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

func insertPair(ctx context.Context, db *sql.DB, i int) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	pad := fmt.Sprintf("row-%d-%0100d", i, i)
	if _, err := tx.ExecContext(ctx, `INSERT INTO a(id, v) VALUES (?, ?)`, i, pad); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO b(id, v) VALUES (?, ?)`, i, pad); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func mustExec(t testing.TB, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

// openRO opens a database file for reading in tests.
func openRO(t testing.TB, path string) *sql.DB {
	t.Helper()
	db, err := sqlitekit.OpenReadOnly(context.Background(), path, sqlitekit.OpenOptions{MaxOpenConns: 1})
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func count(t testing.TB, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func listDir(t testing.TB, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func readFile(t testing.TB, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

// makeBackup produces a verified backup file in a fresh temp dir.
func makeBackup(t *testing.T, db *sql.DB) string {
	t.Helper()
	dest := filepath.Join(t.TempDir(), "backup.db")
	if _, err := Backup(context.Background(), db, dest); err != nil {
		t.Fatalf("backup: %v", err)
	}
	return dest
}

func withHook(h func(step, path string) error) Option {
	return func(o *options) { o.hook = h }
}
