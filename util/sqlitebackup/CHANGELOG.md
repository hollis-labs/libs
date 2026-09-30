# Changelog

All notable changes to go-sqlite-backup are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Write the entry for a release here BEFORE cutting its tag: the release workflow
refuses a tag whose CHANGELOG has no heading for it.

## Unreleased

### Added

- `Backup`, `Verify`, `Restore`: VACUUM INTO snapshot with read-only `integrity_check` verification, checksum taken after verification, atomic no-clobber publish, and a staged, rollback-safe restore with a `postCheck` seam. Options `WithMode` (`Online`/`Drained`), `WithVerify`, `WithDriverName`, `WithManifest`. `Rotate` is intentionally not included.
- Tests for the checksum-after-verify ordering, partial-file and no-clobber safety, corruption detection, restore atomicity, and a concurrent-writer backup; `FuzzVerify`.
