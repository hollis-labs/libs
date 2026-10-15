package sqlstore

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed schema/*.sql
var schemaFS embed.FS

// versionTable records which embedded schema files Migrate has applied. It is
// namespaced so it never collides with a host's own migration bookkeeping.
const versionTable = "sqlstore_schema_versions"

// Schema returns the embedded final-shape SQL (tables, indexes and triggers)
// as a file system of numbered .sql files, for hosts that apply schema through
// their own migration runner. Every statement is create-if-missing. The files
// are the same ones Migrate applies.
func Schema() fs.FS {
	sub, err := fs.Sub(schemaFS, "schema")
	if err != nil {
		panic(err) // the embedded directory is fixed at build time
	}
	return sub
}

// Migrate creates the store's tables, indexes and triggers when they are
// missing and records the applied schema version in its own table. It is safe
// to call repeatedly and on a database that already contains the tables. It
// does not detect or repair drift in existing tables; hosts with their own
// migration history should apply Schema through their runner instead.
func Migrate(ctx context.Context, db *sql.DB) error {
	if err := checkWorkflowContext(ctx); err != nil {
		return err
	}
	if db == nil {
		return workflowInvalid(fmt.Errorf("migrate requires an open database"))
	}
	entries, err := fs.ReadDir(Schema(), ".")
	if err != nil {
		return fmt.Errorf("read embedded schema: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)

	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("migrate: acquire sqlite connection: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS `+versionTable+` (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("migrate: ensure %s: %w", versionTable, err)
	}
	for _, name := range names {
		version, err := parseSchemaVersion(name)
		if err != nil {
			return err
		}
		var applied int
		if scanErr := conn.QueryRowContext(ctx, `SELECT COUNT(1) FROM `+versionTable+` WHERE version = ?`, version).Scan(&applied); scanErr != nil {
			return fmt.Errorf("migrate: check version %d: %w", version, scanErr)
		}
		if applied > 0 {
			continue
		}
		body, err := fs.ReadFile(Schema(), name)
		if err != nil {
			return fmt.Errorf("migrate: read %s: %w", name, err)
		}
		if err := applySchemaFile(ctx, conn, version, name, string(body)); err != nil {
			return err
		}
	}
	return nil
}

func applySchemaFile(ctx context.Context, conn *sql.Conn, version int, name, body string) (err error) {
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("migrate %s: begin sqlite transaction: %w", name, err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(rollbackCtx, "ROLLBACK")
	}()
	if _, err := conn.ExecContext(ctx, body); err != nil {
		return fmt.Errorf("migrate %s: %w", name, err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO `+versionTable+` (version, name, applied_at) VALUES (?, ?, ?)`,
		version, name, workflowTime(time.Now())); err != nil {
		return fmt.Errorf("migrate %s: record version: %w", name, err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("migrate %s: commit sqlite transaction: %w", name, err)
	}
	committed = true
	return nil
}

func parseSchemaVersion(name string) (int, error) {
	prefix, _, ok := strings.Cut(path.Base(name), "_")
	if !ok {
		return 0, fmt.Errorf("schema file %q: want NNNN_name.sql", name)
	}
	version, err := strconv.Atoi(prefix)
	if err != nil || version <= 0 {
		return 0, fmt.Errorf("schema file %q: invalid version prefix", name)
	}
	return version, nil
}
