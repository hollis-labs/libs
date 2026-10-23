package store

import (
	"errors"

	"github.com/hollis-labs/libs/core/util/sqlite/txutil"
	"github.com/jackc/pgx/v5/pgconn"
	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// ErrClosed is returned by a Store used after Close.
var ErrClosed = errors.New("store: closed")

// IsUniqueViolation reports a UNIQUE or PRIMARY KEY violation on either engine.
func IsUniqueViolation(err error) bool {
	var se *sqlite.Error
	if errors.As(err, &se) {
		c := se.Code()
		return c == sqlite3.SQLITE_CONSTRAINT_UNIQUE || c == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
	}
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

// IsForeignKeyViolation reports a FOREIGN KEY violation on either engine.
func IsForeignKeyViolation(err error) bool {
	var se *sqlite.Error
	if errors.As(err, &se) {
		return se.Code() == sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY
	}
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23503"
}

// IsRetryable reports a transient contention error worth retrying the whole
// transaction for: SQLITE_BUSY/LOCKED, or Postgres serialization failure and
// deadlock.
func IsRetryable(err error) bool {
	if txutil.IsRetryableLock(err) {
		return true
	}
	var pe *pgconn.PgError
	return errors.As(err, &pe) && (pe.Code == "40001" || pe.Code == "40P01")
}
