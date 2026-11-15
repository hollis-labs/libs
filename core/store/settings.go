package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrSQLiteOnly is returned by the SQLite settings readers on Postgres.
var ErrSQLiteOnly = errors.New("store: SQLite only")

// SQLiteSettings are a SQLite connection's settings as the connection itself
// reports them, for code that must check them before it relies on them (a
// durable audit record needs synchronous FULL or EXTRA; a proof must be
// computed on the named database file, not a copy).
type SQLiteSettings struct {
	// Synchronous is OFF, NORMAL, FULL or EXTRA.
	Synchronous string
	// JournalMode is lower case: wal, delete, memory, truncate, persist or off.
	JournalMode string
	// BusyTimeout is how long the connection waits for another's lock.
	BusyTimeout time.Duration
	// ForeignKeys reports whether foreign keys are enforced.
	ForeignKeys bool
	// MainPath is the file behind the "main" schema, as SQLite reports it
	// (absolute), or "" for an in-memory database.
	MainPath string
}

// ReadSQLiteSettings reads the settings of the connection q runs on: inside a
// WriteTx callback pass its tx (the writer connection), inside a ReadTx its
// querier (one read connection). It only reads pragmas; it changes nothing.
// On Postgres it returns ErrSQLiteOnly.
func ReadSQLiteSettings(ctx context.Context, q Querier) (SQLiteSettings, error) {
	var st SQLiteSettings
	if q.Dialect().Name() != DriverSQLite {
		return st, ErrSQLiteOnly
	}
	var sync, busyMS, fk int
	if err := q.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&sync); err != nil {
		return st, fmt.Errorf("store: read synchronous: %w", err)
	}
	names := []string{"OFF", "NORMAL", "FULL", "EXTRA"}
	if sync < 0 || sync >= len(names) {
		return st, fmt.Errorf("store: synchronous %d is not a known level", sync)
	}
	st.Synchronous = names[sync]
	if err := q.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&st.JournalMode); err != nil {
		return st, fmt.Errorf("store: read journal_mode: %w", err)
	}
	st.JournalMode = strings.ToLower(st.JournalMode)
	if err := q.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyMS); err != nil {
		return st, fmt.Errorf("store: read busy_timeout: %w", err)
	}
	st.BusyTimeout = time.Duration(busyMS) * time.Millisecond
	if err := q.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil {
		return st, fmt.Errorf("store: read foreign_keys: %w", err)
	}
	st.ForeignKeys = fk != 0
	rows, err := q.QueryContext(ctx, `PRAGMA database_list`)
	if err != nil {
		return st, fmt.Errorf("store: read database_list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	found := false
	for rows.Next() {
		var seq int
		var name, file string
		if err := rows.Scan(&seq, &name, &file); err != nil {
			return st, fmt.Errorf("store: read database_list: %w", err)
		}
		if name == "main" {
			st.MainPath, found = file, true
		}
	}
	if err := rows.Err(); err != nil {
		return st, fmt.Errorf("store: read database_list: %w", err)
	}
	if !found {
		return st, errors.New("store: database_list has no main schema")
	}
	return st, nil
}

// SQLiteSettings reads the writer connection's settings (see
// ReadSQLiteSettings). It waits for the single writer connection, so do not
// call it inside a WriteTx callback: pass the callback's tx to
// ReadSQLiteSettings instead. The read pool's settings are read with
// ReadSQLiteSettings(ctx, s.Reader()). On Postgres it returns ErrSQLiteOnly.
func (s *Store) SQLiteSettings(ctx context.Context) (SQLiteSettings, error) {
	return ReadSQLiteSettings(ctx, newBound(s.dialect, s.write, s.cache))
}
