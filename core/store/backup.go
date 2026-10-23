package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/hollis-labs/libs/core/util/sqlitebackup"
)

// ErrBackupUnsupported is returned by [Store.Backup] on Postgres, which is
// backed up out of process (pg_dump or the platform's snapshots).
var ErrBackupUnsupported = errors.New("store: backup is SQLite only; back Postgres up with pg_dump")

// Backup writes a consistent, verified copy of a SQLite database to dest, using
// core/util/sqlitebackup. Options pass through to it.
func (s *Store) Backup(ctx context.Context, dest string, opts ...sqlitebackup.Option) (sqlitebackup.Result, error) {
	if s.dialect.Name() != DriverSQLite {
		return sqlitebackup.Result{}, ErrBackupUnsupported
	}
	res, err := sqlitebackup.Backup(ctx, s.write, dest, opts...)
	if err != nil {
		return res, fmt.Errorf("store: backup: %w", err)
	}
	return res, nil
}

// VerifyBackup checks a SQLite backup file (integrity check) without opening it
// for writes.
func VerifyBackup(ctx context.Context, path string, opts ...sqlitebackup.Option) (sqlitebackup.Result, error) {
	return sqlitebackup.Verify(ctx, path, opts...)
}
