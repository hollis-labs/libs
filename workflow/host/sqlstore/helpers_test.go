package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// testDB is the test-side stand-in for Hadron's persistence.Store: an open
// SQLite database with the schema applied.
type testDB struct{ db *sql.DB }

func (d *testDB) DB() *sql.DB  { return d.db }
func (d *testDB) Close() error { return d.db.Close() }

// openTestDB opens (creating if needed) a SQLite database at path with the
// same connection setup Hadron uses (single connection, WAL, busy timeout) and
// applies the embedded schema.
func openTestDB(path string) (*testDB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("mkdir db dir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, `
		PRAGMA journal_mode=WAL;
		PRAGMA synchronous=NORMAL;
		PRAGMA busy_timeout=5000;
	`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}
	if err := Migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &testDB{db: db}, nil
}

func newTestStore(d *testDB) (*Store, error) { return New(d.db) }

func TestMigrateIsIdempotent(t *testing.T) {
	d, err := openTestDB(filepath.Join(t.TempDir(), "migrate.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	for i := 0; i < 2; i++ {
		if err := Migrate(context.Background(), d.db); err != nil {
			t.Fatalf("Migrate #%d: %v", i+2, err)
		}
	}
	var versions int
	if err := d.db.QueryRow(`SELECT COUNT(1) FROM ` + versionTable).Scan(&versions); err != nil || versions != 1 {
		t.Fatalf("recorded versions = %d, %v", versions, err)
	}
}

func TestMigrateOnDatabaseWithExistingTables(t *testing.T) {
	d, err := openTestDB(filepath.Join(t.TempDir(), "existing.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	// Simulate a host that applied Schema through its own runner: version
	// bookkeeping is gone but the tables remain.
	if _, err := d.db.Exec(`DROP TABLE ` + versionTable); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), d.db); err != nil {
		t.Fatalf("Migrate over existing tables: %v", err)
	}
}

// TestWriteTxConcurrentOnSingleConnection guards the DBTX contract: on a
// MaxOpenConns=1 database, concurrent WriteTx callbacks that read through the
// supplied DBTX must serialize instead of deadlocking.
func TestWriteTxConcurrentOnSingleConnection(t *testing.T) {
	d, err := openTestDB(filepath.Join(t.TempDir(), "writetx.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	store, err := New(d.db)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := d.db.ExecContext(ctx, `CREATE TABLE host_counter (id INTEGER PRIMARY KEY, n INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.ExecContext(ctx, `INSERT INTO host_counter (id, n) VALUES (1, 0)`); err != nil {
		t.Fatal(err)
	}
	const workers, perWorker = 2, 25
	errs := make(chan error, workers)
	for w := 0; w < workers; w++ {
		go func() {
			for i := 0; i < perWorker; i++ {
				err := store.WriteTx(ctx, "test.increment", func(tx DBTX) error {
					var n int
					if err := tx.QueryRowContext(ctx, `SELECT n FROM host_counter WHERE id = 1`).Scan(&n); err != nil {
						return err
					}
					_, err := tx.ExecContext(ctx, `UPDATE host_counter SET n = ? WHERE id = 1`, n+1)
					return err
				})
				if err != nil {
					errs <- err
					return
				}
			}
			errs <- nil
		}()
	}
	for w := 0; w < workers; w++ {
		if err := <-errs; err != nil {
			t.Fatalf("WriteTx worker: %v", err)
		}
	}
	var n int
	if err := d.db.QueryRowContext(ctx, `SELECT n FROM host_counter WHERE id = 1`).Scan(&n); err != nil || n != workers*perWorker {
		t.Fatalf("counter = %d, %v; want %d (lost update)", n, err, workers*perWorker)
	}
}

func TestWriteTxRollsBackOnCallbackError(t *testing.T) {
	d, err := openTestDB(filepath.Join(t.TempDir(), "rollback.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	store, _ := New(d.db)
	ctx := context.Background()
	if _, execErr := d.db.ExecContext(ctx, `CREATE TABLE host_note (v TEXT)`); execErr != nil {
		t.Fatal(execErr)
	}
	sentinel := fmt.Errorf("callback failed")
	err = store.WriteTx(ctx, "test.rollback", func(tx DBTX) error {
		if _, execErr := tx.ExecContext(ctx, `INSERT INTO host_note (v) VALUES ('x')`); execErr != nil {
			return execErr
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("WriteTx error = %v, want sentinel", err)
	}
	var count int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM host_note`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rows after rollback = %d, %v", count, err)
	}
}
