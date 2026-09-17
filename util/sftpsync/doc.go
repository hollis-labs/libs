// Package sftpsync recursively transfers a directory tree over an SFTP
// connection that the caller already owns.
//
// It exists because [github.com/pkg/sftp] gives you the primitives — Walk,
// ReadDir, MkdirAll, Chtimes — and leaves recursion, safety and options to
// you, and because every recursive alternative speaks the SCP protocol that
// OpenSSH has deprecated. The value here is not the wrapping; it is the
// opinions:
//
//   - Permission bits are carried across, so an uploaded script arrives
//     executable.
//   - A symlink whose target resolves outside the sync root is refused, not
//     followed. See [ErrSymlinkEscape].
//   - Every file is written to a sibling temp name and renamed into place, so
//     an interrupted transfer never replaces a good file with half of a new one.
//   - Cancellation is honoured between files and inside one. An io.Copy over
//     a dropped VPN stops when the context says so, not when TCP notices.
//   - The return value is a [Result] — what moved, what was skipped, how many
//     bytes, how long — because callers show this to operators.
//   - [WithDryRun] walks and reports without writing, so a caller can preview
//     an operation without reimplementing the walk.
//
// # Connections
//
// This package takes a connection; it does not make one. [Upload] and
// [Download] accept a live *sftp.Client, [UploadOverSSH] and
// [DownloadOverSSH] a live *ssh.Client. Nothing here reads ~/.ssh/config,
// accepts credentials, or dials. Authentication, host-key policy and
// connection lifetime belong to the caller, who already has opinions about
// all three; a library that dialled again would be a second transport with
// different failure modes from the one in use.
//
// # No delta transfer
//
// Every byte is copied every time. There is no rolling checksum, no
// size-and-mtime skip, and no deletion of files that exist only at the
// destination. That is the right trade for config files, env files, compose
// stacks and agent definitions, and the wrong one for a 600MB image. If you
// are moving something large and mostly unchanged, use rsync.
package sftpsync
