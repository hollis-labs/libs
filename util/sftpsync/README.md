# go-sftpsync

Recursive directory transfer over an SFTP connection you already have. `go-sftpsync` walks a
directory tree in either direction — local to remote or remote to local — carrying permission
bits, refusing symlinks that point outside the sync root, writing every file to a temp name and
renaming it into place, honouring `context.Context` between files and inside one, and returning a
report of what happened rather than a bare `error`. It takes a live `*sftp.Client` or `*ssh.Client`
and never dials, authenticates, or reads `~/.ssh/config`: the connection is the caller's, and so
are the decisions that made it trustworthy.

## Status

Pre-1.0 (`v0.1.x`). The public API is small and intended to stay that way — four transfer
functions, seven options, one result type — but minor breaks may still happen between `v0.x`
releases. See [`CHANGELOG.md`](CHANGELOG.md) and pin a version in your `go.mod`.

Documentation: [pkg.go.dev/github.com/hollis-labs/go-sftpsync](https://pkg.go.dev/github.com/hollis-labs/go-sftpsync).

## Install

```bash
go get github.com/hollis-labs/go-sftpsync
```

## Usage

```go
package main

import (
	"context"
	"fmt"
	"log"

	sftpsync "github.com/hollis-labs/go-sftpsync"
	"golang.org/x/crypto/ssh"
)

func deploy(ctx context.Context, client *ssh.Client) error {
	// Preview first. The dry run walks exactly what the transfer walks, so
	// this is the file set, not a second opinion about it.
	plan, err := sftpsync.UploadOverSSH(ctx, client, "./build", "/srv/app",
		sftpsync.WithDryRun(true))
	if err != nil {
		return err
	}
	fmt.Println(plan) // upload ./build -> /srv/app (dry run): 12 files, 4 dirs, 84213 bytes in 31ms
	for _, e := range plan.Entries {
		fmt.Printf("  %-8s %s\n", e.Action, e.Path)
	}

	result, err := sftpsync.UploadOverSSH(ctx, client, "./build", "/srv/app")
	if err != nil {
		// Errors name the side and the path: "sftpsync: remote create
		// /srv/app/.bin.3f2a.sftpsync-tmp: permission denied"
		return err
	}
	log.Println(result)
	return nil
}
```

For a version that runs with no host at all, against an in-process SFTP server:

```bash
go run ./examples/preview
```

`examples/overssh` is the same thing against a real host, and shows the caller doing its own
dialling, key loading and `known_hosts` verification.

## API

| Symbol | Purpose |
|---|---|
| `Upload(ctx, *sftp.Client, localDir, remoteDir, ...Option) (*Result, error)` | Copy a local tree to the far end. |
| `Download(ctx, *sftp.Client, remoteDir, localDir, ...Option) (*Result, error)` | The same walk, reversed. |
| `UploadOverSSH(ctx, *ssh.Client, ...)` / `DownloadOverSSH(ctx, *ssh.Client, ...)` | Open an SFTP subsystem on a connection you own, transfer, close the subsystem. The `*ssh.Client` is left open. |
| `WithPreserveMode(bool)` | Carry source permission bits. Default **true**. |
| `WithPreserveModTime(bool)` | Carry source modification times. Default false. |
| `WithSymlinkPolicy(SymlinkPolicy)` | `SymlinkReplicate` (default), `SymlinkSkip`, `SymlinkDereferenceIncludingOutsideRoot`. |
| `WithDryRun(bool)` | Walk and report, write nothing. |
| `WithFileMode(os.FileMode)` / `WithDirMode(os.FileMode)` | Modes used when `WithPreserveMode` is off. Default 0644 / 0755. |
| `WithMaxDepth(int)` | Bound the descent. Default 64. Exceeding it is an error, never a silent truncation. |
| `Result` | `Direction`, `Source`, `Destination`, `DryRun`, `Entries`, `Files`, `Dirs`, `Symlinks`, `Skipped`, `Bytes`, `Duration`, and a `String()` one-liner for operators. |
| `Entry` | One thing the walk found: `Path` (relative, slash-separated), `Action`, `Mode`, `Size`, `Target`, `Reason`. |
| `PathError` | Every error. Carries `Op`, `Side` (`Local` / `Remote`), `Path`, and unwraps to the cause. |
| `ErrSymlinkEscape`, `ErrNotDirectory`, `ErrMaxDepthExceeded` | Sentinels for conditions a caller acts on. |

## The opinions

A general-purpose "copy a directory" helper usually lacks these. They are what makes it safe to
point at a real host.

**Permission bits are carried across.** An uploaded script that arrives `0644` does not fail at
upload time; it fails an hour later, far from the transfer that caused it. Directory modes are
applied *after* the directory's contents are written, so a source directory of `0500` arrives as
`0500` instead of refusing its own files — a refinement over the SCP prior art, which has to send a
directory's mode at creation because the protocol requires it.

**Symlinks that leave the sync root are refused.** A link inside the tree pointing at
`/etc/shadow` does not exfiltrate it. The check is lexical, so it costs no I/O, cannot be raced by
a target that changes after the check, and gives the same answer on the remote side where there is
no `EvalSymlinks` to call. It is sound because nothing is ever read *through* a replicated link.
Refusal is `ErrSymlinkEscape`, wrapped in a `PathError` that names the link and its target.
Following links outside the root is possible, and the option is named
`SymlinkDereferenceIncludingOutsideRoot` so that choosing it reads as a decision.

An absolute target counts as an escape even when it points inside the source root: the same
absolute path on the other machine is a different place, and this library will not guess which one
you meant.

**Every file is written to a sibling temp name and renamed into place.** An interrupted sync
leaves the previous file intact rather than half of the new one. Mode and mtime are applied to the
temp file *before* the rename, so the file arrives with its final attributes in the same instant it
arrives with its contents — never complete but not yet executable. Where the server offers
`posix-rename@openssh.com` the replacement is atomic; where it does not, the destination is removed
first, which is the one window this technique cannot close.

**Cancellation is honoured between files and inside one.** `io.Copy` has no context, so a sync of
ten thousand small files over a dropped VPN would otherwise run to completion long after the caller
gave up. The reader is wrapped rather than the copy loop hand-rolled, which keeps `pkg/sftp`'s
pipelined writes — most of its throughput over a real link — while still checking on every chunk.

**Transfers return a report.** `*Result` says what moved, what was skipped and why, how many bytes
and how long. It is returned *even when the transfer fails*, so a caller can tell an operator the
difference between "nothing moved" and "half the tree moved".

**Dry run first.** `WithDryRun(true)` walks and reports without creating, writing or renaming
anything. It is an option on the transfer functions rather than a separate `Plan` call on purpose:
a preview is only worth showing an operator if it came from the same walk as the transfer.

**Upload and download are one implementation.** The two directions differ only in which end is the
source. That is deliberate: the download path is where "it is only reading" quietly becomes "it
wrote outside the destination", and one code path cannot drift into two sets of safety properties.

## Limits

**There is no delta transfer.** Every byte is copied every time. There is no rolling checksum, no
size-and-mtime skip, and no deletion of files that exist only at the destination. That is the right
trade for config files, env files, compose stacks and agent definitions, and the wrong one for a
600MB image that changed in one place. If you are moving something large and mostly unchanged, use
rsync. Should a tar-over-exec strategy ever land here it will be a second strategy, not a
replacement for this one.

Also out of scope, and deliberately: compression, resume, bandwidth limiting, and anything that
requires a binary on the remote host.

**Every entry is `lstat`ed.** SFTP's `SSH_FXP_READDIR` carries attributes, but which stat they come
from is server-defined, and a server that reports a symlink's target instead of the link would let
a link present itself as a regular file and walk straight past the escape check. One extra round
trip per entry is the price of that check meaning anything. For a tree of thousands of tiny files
over a high-latency link, this is the dominant cost.

**Ownership is not carried.** No `chown`, no uid/gid mapping. The far end's files belong to
whoever the SFTP connection authenticated as.

## Alternatives

A reader deciding between libraries is better served by an honest list than by a claim that this
one is best.

| Library | When to prefer it |
|---|---|
| [`github.com/pkg/sftp`](https://pkg.go.dev/github.com/pkg/sftp) | **Whenever it is enough.** It is the foundation this package is built on, and its `Walk`, `ReadDir`, `MkdirAll` and `Chtimes` will serve you directly if you are moving a handful of files, do not need recursion, or want to make the safety decisions yourself. Depending on this package only pays for itself if you want *these particular* opinions. |
| [`github.com/bramvdbogaerde/go-scp`](https://github.com/bramvdbogaerde/go-scp) | A maintained, tagged, single-file SCP client with a good `PassThru` progress hook and a `NewClientBySSH` constructor that takes an existing connection. Prefer it if you need progress reporting on one large file and are content with SCP — which OpenSSH has deprecated and now implements over SFTP anyway. It does not do recursion. |
| [`github.com/povsister/scp`](https://github.com/povsister/scp) | The most complete recursive SCP implementation in Go, with careful directory traversal ordering and mode handling worth reading. Prefer it if you must speak SCP specifically. Its caveats are that it has no tagged release (`v0.0.0` pseudo-version only) and that it, too, is SCP. |
| [`github.com/melbahja/goph`](https://github.com/melbahja/goph) | A convenience wrapper over `golang.org/x/crypto/ssh` covering connection, commands and single-file transfer. Prefer it if you want one package to handle *dialling* as well — which is exactly the thing this package refuses to do. It does not solve recursion. |
| `rsync` over `ssh.Session` | Prefer it for anything large, anything mostly unchanged, anything needing `--delete`, and anything where resuming matters. It requires rsync on both ends; this package requires only an SFTP subsystem. |

## Prior art

`povsister/scp` and `bramvdbogaerde/go-scp` were both read before this was written, and where an
approach is borrowed from either, the comment at that line says so.

The clearest debts are `povsister/scp`'s directory-creation ordering and mode handling — reading
how it has to send a directory's mode at creation is what made applying the mode *after* contents
the obvious improvement, and the comment in `enterDir` says as much — and `pkg/sftp`'s own
delayed-writer test trick, which is what lets the cancellation tests interrupt a transfer at a
known point instead of sleeping and hoping.

## Dependencies

Direct:

- [`github.com/pkg/sftp`](https://pkg.go.dev/github.com/pkg/sftp) — the SFTP client, and the
  in-process server the tests run against.
- [`golang.org/x/crypto`](https://pkg.go.dev/golang.org/x/crypto) — `ssh.Client`, for the
  `*OverSSH` entry points.

That is the whole list, and it is meant to stay that way.

## Testing

```bash
go test -race ./...
```

Tests run against a **real SFTP server in-process** — the one that ships with `pkg/sftp`, wired to
a client over a `net.Pipe` — rather than a mock, so they exercise `SSH_FXP_LSTAT`,
`SSH_FXP_SYMLINK`, `SSH_FXP_SETSTAT` and `posix-rename@openssh.com` as the protocol actually
implements them. No network, no ssh daemon, no external `sftp-server` binary, no environment
variables and no fixtures.

## License

[MIT](LICENSE) © Hollis Labs.
