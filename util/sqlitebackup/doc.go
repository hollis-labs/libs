// Package sqlitebackup takes and verifies point-in-time SQLite backups over
// database/sql, and restores them safely. It knows nothing about any
// application's schema: the manifest it writes carries only mechanical facts
// (mode, size, checksum, integrity result), never domain content.
//
// The API is three functions:
//
//   - Backup writes a consistent snapshot with VACUUM INTO (never a raw file
//     copy), reopens it read-only, runs PRAGMA integrity_check, takes a
//     SHA-256 checksum, and only then atomically publishes it. The checksum
//     comes after verification on purpose; see Backup.
//   - Verify re-checks a backup file later without retaking it.
//   - Restore verifies a backup, stages it, runs an optional caller-supplied
//     check, and swaps it into place atomically, keeping the file it replaced.
//
// This package imports no SQLite driver. Register one (modernc.org/sqlite,
// driver name "sqlite", is the default) or pass WithDriverName. It does not
// schedule, rotate, or upload backups, and it takes no locks on the caller's
// behalf.
package sqlitebackup
