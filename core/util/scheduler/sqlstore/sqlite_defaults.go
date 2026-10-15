package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// DefaultBusyTimeout lets contending SQLite writers wait instead of failing immediately.
const DefaultBusyTimeout = 5 * time.Second

// Option configures the SQLite reference store.
type Option func(*Store)

// WithBusyTimeout overrides the connection busy timeout. Zero disables waiting;
// negative durations are rejected by New. The setting is reapplied on every
// connection acquired for a write, including connections added to a caller's pool.
func WithBusyTimeout(timeout time.Duration) Option {
	return func(s *Store) { s.busyTimeout = timeout }
}

func (s *Store) connection(ctx context.Context) (*sql.Conn, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	ms := s.busyTimeout.Milliseconds()
	if ms == 0 && s.busyTimeout > 0 {
		ms = 1
	}
	if ms > 2147483647 {
		_ = conn.Close()
		return nil, errors.New("sqlstore: busy timeout exceeds SQLite range")
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", ms)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func (s *Store) begin(ctx context.Context) (*sql.Tx, func(), error) {
	conn, err := s.connection(ctx)
	if err != nil {
		return nil, nil, err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	return tx, func() { _ = tx.Rollback(); _ = conn.Close() }, nil
}

func (s *Store) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	conn, err := s.connection(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	return conn.ExecContext(ctx, query, args...)
}
