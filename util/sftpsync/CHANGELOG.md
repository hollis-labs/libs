# Changelog

All notable changes to `go-sftpsync` are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## v0.1.1 — 2026-09-17

Documentation only. No code changed, no API moved, and the transferred bytes
are identical — upgrading from `v0.1.0` is free and skipping it costs nothing.
It exists because pkg.go.dev renders the docs for a tagged version, so the
status notice below could not otherwise reach the place most readers meet
this package.

### Changed

- `README.md` and the package doc now open with a status notice: pre-1.0, in
  development, safety properties tested but not yet used in anger, API churn
  expected in minor versions, and a pointer to the issue tracker.
- The `## Status` section was folded into that notice rather than repeating
  it. Its one substantive claim — that the documented opinions are the
  contract, so changing one is a breaking change even where the function
  signature does not move — moved down to those opinions, where a reader
  meets them.

## v0.1.0 — 2026-09-17

First release.

### Added

- `Upload` / `Download` — recursive directory transfer over an `*sftp.Client`
  the caller owns, in either direction, sharing one walk implementation.
- `UploadOverSSH` / `DownloadOverSSH` — the same, over an `*ssh.Client` the
  caller owns. The subsystem is opened and closed; the connection is not.
- Options: `WithPreserveMode` (default **on**), `WithPreserveModTime`,
  `WithSymlinkPolicy`, `WithDryRun`, `WithFileMode`, `WithDirMode`,
  `WithMaxDepth`.
- `SymlinkPolicy`: `SymlinkReplicate` (default),  `SymlinkSkip`, and
  `SymlinkDereferenceIncludingOutsideRoot`.
- `Result` and `Entry` — what moved, what was skipped and why, byte count and
  duration, plus a `String()` one-liner. Returned even when the transfer fails.
- `PathError` carrying `Op`, `Side` and `Path`, and unwrapping to the cause.
- Sentinels `ErrSymlinkEscape`, `ErrNotDirectory`, `ErrMaxDepthExceeded`.
- `examples/preview` (runs with no host, against an in-process SFTP server) and
  `examples/overssh` (runs against a real host, with the caller doing its own
  dialling and `known_hosts` verification).
- `LICENSE` (MIT, Hollis Labs), `README.md`, `AGENTS.md`, root `doc.go`.

### Notes

The behaviours below are the point of the package rather than incidental, and
changing any of them is a breaking change even where the signature does not
move:

- **`WithPreserveMode` defaults to true.** A script that arrives non-executable
  fails an hour after the transfer that caused it.
- **Directory modes are applied after contents, not at creation.** A `0500`
  source directory arrives as `0500` with its files inside it.
- **A symlink whose target leaves the sync root is refused**, and an absolute
  target is treated as leaving it. Following is available only under
  `SymlinkDereferenceIncludingOutsideRoot`.
- **Every entry is `lstat`ed** rather than trusting `SSH_FXP_READDIR`
  attributes, which are server-defined. One extra round trip per entry is the
  cost of the escape check meaning anything.
- **An existing destination root keeps its own mode.** Only directories this
  package creates take the source's.
- **There is no delta transfer** and no deletion of extraneous destination
  files. Every byte is copied every time.
- **Ownership is not carried.** No `chown`, no uid/gid mapping.
