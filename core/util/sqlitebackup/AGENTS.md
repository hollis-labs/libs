# go-sqlite-backup

Safe SQLite backup, verify and restore over database/sql: VACUUM INTO, integrity check, checksum, atomic rename.

It is not a scheduler, a rotation or retention tool, an uploader, or a replacement for an application's own restore system. Do not add `Rotate`, timers, remote targets, or application-specific manifest fields.

## Start Here

- `sqlitebackup` package — the importable API; its `doc.go` is the package documentation.
- `examples/hello/main.go` — the runnable example; the README `## Usage` fence must stay identical to it.
- `.github/workflows/check.yml` — the full CI gate; `release.yml` refuses a tag with no CHANGELOG heading.

## Commands

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

CI (`.github/workflows/check.yml`) is the full gate.

## Boundaries

- No `replace` directive in `go.mod` and no committed `go.work`: consumers cannot resolve either.
- Exactly `Backup`, `Verify`, `Restore`. No `Rotate`: no app has ever rotated a backup, so it is speculative surface. Adding it is a design decision, not a convenience.
- Never copy a live SQLite file raw. Snapshots come from `VACUUM INTO` only (`TestBackupOnlineProducesVerifiedCopy`, `TestBackupWhileConcurrentWriterIsSnapshotConsistent`).
- Order in `Backup`: `VACUUM INTO` a temp file, verify read-only, then checksum, then publish. The checksum must come after verification because verification can rewrite the header (Tangent CW-20260905-0014). Do not "tidy" this order (`TestBackupChecksumAfterVerification`, `TestBackupResultChecksumMatchesFileOnDisk`).
- A failed backup leaves nothing at or beside the destination, and an existing destination is never overwritten, including one that appears mid-flight: publication is a no-clobber link (`TestBackupFailureLeavesNoPartialFile`, `TestBackupRefusesExistingDestinationWithoutClobbering`, `TestBackupDestinationAppearingMidFlightIsNotClobbered`, `TestBackupManifestPublishFailureRemovesDatabase`).
- `Verify` must never report a damaged file as ok, must never modify it, and must remove only sidecars it created (`TestVerifyDetectsCorruption`, `TestVerifyDoesNotModifyTheFile`, `TestVerifySidecarHygiene`, `FuzzVerify`).
- `Restore` verifies the source first, never deletes the file it replaces, moves the `-wal`/`-shm` companions with it, and leaves the target as the complete old or complete new file on every failure path (`TestRestoreRefusesDamagedSourceBeforeTouchingTarget`, `TestRestoreAtomicityRollsBackOnFailureAfterMoveAside`, `TestRestorePostCheckSeesRestoredDataAndCanVeto`, `TestRestoreMovesWALCompanionsWithReplacedFile`).
- The manifest stays mechanical (five fields); no extension mechanism (`TestBackupWritesMinimalManifest`).
- This package imports no SQLite driver; only tests import `modernc.org/sqlite`. The read-only DSN comes from `go-sqlite`'s `sqlitekit.OpenReadOnly`; do not hand-roll a DSN.
- Tests use temp directories and temp or in-memory databases only, never real application data. The test hook (`withHook`) is unexported test scaffolding, not API.
- Behavioral equivalence with Tesseract's or Tangent's code is not claimed. Their code was read; only their own tests were run, on copies.
