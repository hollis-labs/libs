package sqlitebackup_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	sqlitebackup "github.com/hollis-labs/go-sqlite-backup"
	_ "modernc.org/sqlite"
)

func exampleDB() (db *sql.DB, dir string) {
	dir, err := os.MkdirTemp("", "sqlitebackup-example-")
	if err != nil {
		panic(err)
	}
	db, err = sql.Open("sqlite", filepath.Join(dir, "app.db"))
	if err != nil {
		panic(err)
	}
	if _, err := db.Exec(`CREATE TABLE notes (body TEXT); INSERT INTO notes VALUES ('hello')`); err != nil {
		panic(err)
	}
	return db, dir
}

func ExampleBackup() {
	db, dir := exampleDB()
	defer func() { _ = os.RemoveAll(dir) }()
	defer func() { _ = db.Close() }()

	res, err := sqlitebackup.Backup(context.Background(), db, filepath.Join(dir, "backups", "app.db"))
	if err != nil {
		fmt.Println("backup failed:", err)
		return
	}
	fmt.Println(res.Mode, res.IntegrityOK, len(res.SHA256))
	// Output: online true 64
}

func ExampleVerify() {
	db, dir := exampleDB()
	defer func() { _ = os.RemoveAll(dir) }()
	defer func() { _ = db.Close() }()

	path := filepath.Join(dir, "app.bak")
	if _, err := sqlitebackup.Backup(context.Background(), db, path); err != nil {
		fmt.Println(err)
		return
	}
	res, err := sqlitebackup.Verify(context.Background(), path)
	fmt.Println(res.IntegrityOK, err)

	// A damaged file is reported by both the Result and the error.
	if err = os.WriteFile(path, []byte("not a database"), 0o600); err != nil {
		fmt.Println(err)
		return
	}
	res, err = sqlitebackup.Verify(context.Background(), path)
	fmt.Println(res.IntegrityOK, errors.Is(err, sqlitebackup.ErrIntegrity))
	// Output:
	// true <nil>
	// false true
}

func ExampleRestore() {
	db, dir := exampleDB()
	defer func() { _ = os.RemoveAll(dir) }()
	defer func() { _ = db.Close() }()

	backup := filepath.Join(dir, "app.bak")
	if _, err := sqlitebackup.Backup(context.Background(), db, backup); err != nil {
		fmt.Println(err)
		return
	}
	target := filepath.Join(dir, "restored.db")
	res, err := sqlitebackup.Restore(context.Background(), target, backup,
		func(ctx context.Context, restored *sql.DB) error {
			var n int
			if err := restored.QueryRowContext(ctx, `SELECT COUNT(*) FROM notes`).Scan(&n); err != nil {
				return err
			}
			if n != 1 {
				return fmt.Errorf("expected 1 note, found %d", n)
			}
			return nil
		})
	fmt.Println(err, res.SupersededPath == "")
	// Output: <nil> true
}
