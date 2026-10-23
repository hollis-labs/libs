package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hollis-labs/libs/core/util/sqlitebackup"
)

// Backup errors. The last three are the underlying library's sentinels, so
// errors.Is works whether or not the caller imports it.
var (
	// ErrBackupUnsupported is returned by [Store.Backup] on Postgres, which is
	// backed up out of process (pg_dump or the platform's snapshots).
	ErrBackupUnsupported = errors.New("store: backup is SQLite only; back Postgres up with pg_dump")
	// ErrBackupExists means the destination is occupied; a backup never overwrites.
	ErrBackupExists = sqlitebackup.ErrDestinationExists
	// ErrBackupIntegrity means the copy failed verification.
	ErrBackupIntegrity = sqlitebackup.ErrIntegrity
	// ErrBackupDrainBusy means a Drained backup could not checkpoint the WAL.
	ErrBackupDrainBusy = sqlitebackup.ErrDrainBusy
)

// BackupOptions tunes [Store.Backup]. The zero value is an online, verified
// backup with a manifest next to it.
type BackupOptions struct {
	// Drained checkpoints and truncates the WAL first. The caller must be sure
	// nothing else holds the database; see core/util/sqlitebackup.
	Drained bool
	// SkipVerify leaves out the integrity check of the copy.
	SkipVerify bool
	// NoManifest skips the JSON sidecar; ManifestPath moves it (default
	// dest + ".manifest.json").
	NoManifest   bool
	ManifestPath string
}

// BackupResult describes a backup file.
type BackupResult struct {
	Path        string
	TakenAt     time.Time
	SizeBytes   int64
	SHA256      string
	IntegrityOK bool
	// Damage holds the reasons verification failed.
	Damage []string
}

func toResult(r sqlitebackup.Result) BackupResult {
	return BackupResult{Path: r.Path, TakenAt: r.TakenAt, SizeBytes: r.SizeBytes, SHA256: r.SHA256, IntegrityOK: r.IntegrityOK, Damage: r.Damage}
}

// Backup writes a consistent, verified copy of a SQLite database to dest, using
// core/util/sqlitebackup.
func (s *Store) Backup(ctx context.Context, dest string, o BackupOptions) (BackupResult, error) {
	if s.dialect.Name() != DriverSQLite {
		return BackupResult{}, ErrBackupUnsupported
	}
	opts := []sqlitebackup.Option{sqlitebackup.WithVerify(!o.SkipVerify)}
	if o.Drained {
		opts = append(opts, sqlitebackup.WithMode(sqlitebackup.Drained))
	}
	switch {
	case o.NoManifest:
		opts = append(opts, sqlitebackup.WithManifest(""))
	case o.ManifestPath != "":
		opts = append(opts, sqlitebackup.WithManifest(o.ManifestPath))
	}
	res, err := sqlitebackup.Backup(ctx, s.write, dest, opts...)
	if err != nil {
		return toResult(res), fmt.Errorf("store: backup: %w", err)
	}
	return toResult(res), nil
}

// VerifyBackup checks a SQLite backup file (integrity check) without opening it
// for writes.
func VerifyBackup(ctx context.Context, path string) (BackupResult, error) {
	res, err := sqlitebackup.Verify(ctx, path)
	return toResult(res), err
}
