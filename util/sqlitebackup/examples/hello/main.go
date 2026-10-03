package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"

	sqlitebackup "github.com/hollis-labs/libs/util/sqlitebackup"
	_ "modernc.org/sqlite"
)

func main() {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "sqlitebackup-example-")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	db, err := sql.Open("sqlite", filepath.Join(dir, "app.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err = db.Exec(`CREATE TABLE notes (body TEXT); INSERT INTO notes VALUES ('hello')`); err != nil {
		log.Fatal(err)
	}

	// Snapshot with VACUUM INTO, verify it read-only, checksum it, publish it.
	backupPath := filepath.Join(dir, "backups", "app.db")
	res, err := sqlitebackup.Backup(ctx, db, backupPath)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("backup intact:", res.IntegrityOK)

	// Re-check it later, without retaking it.
	again, err := sqlitebackup.Verify(ctx, backupPath)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("same checksum:", again.SHA256 == res.SHA256)

	// Restore into a new location; the optional check runs before the swap.
	restored := filepath.Join(dir, "restored.db")
	_, err = sqlitebackup.Restore(ctx, restored, backupPath, func(ctx context.Context, db *sql.DB) error {
		var n int
		return db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notes`).Scan(&n)
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("restored")
}
