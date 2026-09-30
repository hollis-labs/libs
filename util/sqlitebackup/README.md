# go-sqlite-backup

Safe SQLite backup, verify and restore over database/sql: VACUUM INTO, integrity check, checksum, atomic rename.

Three functions and nothing else. `Backup` snapshots a live database with `VACUUM INTO` (never a raw file copy), reopens the snapshot read-only, runs `PRAGMA integrity_check`, takes a SHA-256 checksum, and only then atomically publishes it. `Verify` re-checks a backup file later. `Restore` verifies a backup, stages it, runs an optional caller-supplied check, and swaps it into place atomically while keeping the file it replaced.

## Status

**Pre-release.** This project is unreleased, not deployed, and has no outside consumers. It's being built in the open: the code, the docs, and this README describe what exists today, not a pitch for what's planned. Interfaces and behavior change without notice, and there are no compatibility guarantees yet.

## Install

```sh
go get github.com/hollis-labs/go-sqlite-backup
```

Requires Go 1.26.6 or newer. It depends on [`go-sqlite`](https://github.com/hollis-labs/go-sqlite) (`sqlitekit`) for the read-only opener and imports no SQLite driver itself: register one in your program (`modernc.org/sqlite`, driver name `sqlite`, is the default) or pass `WithDriverName`.

## Usage

```go
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"

	sqlitebackup "github.com/hollis-labs/go-sqlite-backup"
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
```

The same program lives in [`examples/hello`](./examples/hello/main.go).

The order inside `Backup` is load-bearing: **the checksum is taken after verification, never before.** Opening a `VACUUM INTO` output to verify it can rewrite its file header (Tangent hit this as CW-20260905-0014), so a checksum taken first describes a file that no longer exists. `TestBackupChecksumAfterVerification` fails if the order is reversed.

## Behavior

- `Backup` never overwrites: an occupied destination fails with `ErrDestinationExists`, and publication is a no-clobber hard link, so a destination that appears mid-backup is not replaced either. A failed backup leaves nothing at the destination (or beside it); a temporary `.<name>.tmp-*` file can remain only after a hard process kill.
- `WithMode(Drained)` checkpoints the WAL (`TRUNCATE`) first and fails with `ErrDrainBusy` if it cannot. The caller owns the lock that makes that safe; this package takes none.
- `Verify` reports a damaged file through both `Result.IntegrityOK == false` and an error wrapping `ErrIntegrity`. An empty file, a file under 512 bytes, or one without the SQLite header is damaged, not "a fresh database". It never modifies the file, and removes only the `-wal`/`-shm` sidecars its own open created.
- `Restore` always verifies the source, copies it to a staging file beside the target, checks the copy's digest, runs `postCheck` against the staged copy (read-only), then swaps. The replaced file and its `-wal`/`-shm` companions are kept beside the target as `<target>.superseded-<UTC stamp>`; a failure before or during the swap puts everything back and removes the staging file.
- The sidecar manifest is minimal: mode, time, size, checksum, integrity result. No schema or application fields.

## Provenance of this code

This is a fresh implementation of a primitive that Tesseract and Tangent each built independently, informed by reading `apps/tangent/internal/db/backup.go` and `integrity.go`, and `apps/tesseract/internal/contextstore/backup.go` (`verifyBackupDB`, `openBackupDB`) and `restore.go` (contrast only). Glyph's `internal/ledger/ledger.go` backup was read as the negative example. Nothing was copied wholesale, and nothing here claims behavioral equivalence with any of them: the original backup and restore tests of Tangent (`internal/db`) and Tesseract (`internal/contextstore`) were run on read-only copies and passed, which says nothing about this module. Deliberate differences: verification and checksum happen on a temporary file before publication (the originals write straight to the destination); `Restore` stages and runs `postCheck` before touching the target instead of swapping first and rolling back; the verifier refuses zero-byte files. No application uses this module yet.

## Compatibility

This module is pre-1.0 and unreleased: any release, including a minor one, may break the exported API, and there is no deprecation period. Pin an exact version and read [CHANGELOG.md](./CHANGELOG.md) before upgrading. The intent for v1 is that the three function signatures, the `Result`/`Manifest` JSON field names, and the sentinel errors are stable, with removals deprecated for at least one minor release first; nothing is promised until then.

## Known limitations

- The manifest is written but never read: `Verify` and `Restore` do not compare a file against its sidecar checksum. Compare `Result.SHA256` yourself if you need that.
- `Restore` checks that the staged copy is byte-identical to the verified source, but a source modified between `Verify` and the copy is detected only by that digest comparison, not prevented.
- The swap keeps the target present until the final rename (the old file is hard-linked aside first), but moving the `-wal`/`-shm` companions aside is not atomic with it; a hard crash in that window leaves the old database without its WAL beside `<target>.superseded-*`. Recovery is manual.
  - The crash window in detail: `Restore` moves the old `-wal`/`-shm` aside BEFORE it renames the staged file over the target. If the process dies between those two steps, the target is still the old main database file but without its WAL, so transactions that were only in the WAL are missing from the live target. They are not lost: the WAL sits beside the superseded copy as `<target>.superseded-<stamp>-wal` (and `-shm`). To recover, close everything using the database, move the `-wal`/`-shm` files back next to the target under the target's name so SQLite replays them, then verify with `Verify`. This order is deliberate; the opposite order could pair a new database with an old WAL.
- `Restore` requires that no other process or connection has the target open or is writing to it. The package takes no lock; the caller must quiesce writers first.
- On filesystems without hard links, `Backup` publishes with an `Lstat` followed by a rename, which leaves a small race against another process creating the same destination in between.
- Hard links are assumed; on filesystems without them the code falls back to rename-aside, which leaves a short window with no file at the target path.
- `Backup` in `Online` mode may miss writes committed while it runs; `Drained` only means something if the caller has stopped other writers.
- Backup files are created mode 0600; a restored file keeps the replaced file's permissions (0600 for a new target). Ownership is not preserved.
- Integrity is `PRAGMA integrity_check` only: no foreign-key check, no schema check. Use `postCheck` for anything application-specific.
- Windows is untested.

## Out of scope

- Rotation and retention. Intentionally not included: no application in the portfolio has ever rotated a backup file, so a `Rotate` function would be speculative surface. It is a follow-up candidate if a real adopter asks.
- Scheduling, daemon loops, remote upload.
- Application-specific manifest fields or fingerprints; use `Restore`'s `postCheck` seam and keep your own sidecar.
- Replacing Tesseract's or Tangent's own restore systems.
- Taking locks on the caller's behalf.
- Adoption by any application.

## Development

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
go test -run=^$ -fuzz=FuzzVerify -fuzztime=20s .
```

CI (`.github/workflows/check.yml`) is the full gate.

## License

MIT — see [LICENSE](./LICENSE).
