# go-sftpsync

A recursive directory transfer over an SFTP connection the caller already
owns. One walk serves both directions; the value is in the opinions — mode
preservation, symlink-escape refusal, temp-and-rename, real cancellation, a
report instead of an `error`, and a dry run that previews the same walk the
transfer performs.

It is a library, not a tool: nothing here dials, authenticates, reads
`~/.ssh/config`, or holds a credential.

## Start Here

- `README.md` — the opinions, the limits, and an honest Alternatives table.
- `sftpsync.go` — the entire public entry surface: four functions.
- `walk.go` — the single recursive engine, plus `escapesRoot`, which is the
  security check.
- `copy.go` — temp-and-rename, and `ctxReader`, which is what makes
  cancellation real.
- `fs.go` — the `fileSystem` seam that makes upload and download one
  implementation. Two impls: `localFS` and `remoteFS`.
- `options.go`, `result.go`, `errors.go` — the rest of the public surface.
- `server_test.go` — the in-process SFTP server every test runs against.
- `examples/preview` is runnable with no host at all.

## Commands

```bash
gofmt -l .
go vet ./...
golangci-lint run ./...
go test -race -count=1 ./...
go run ./examples/preview
```

There is no CI workflow or Makefile in this repo.

## Boundaries

**Take a connection, do not make one.** The public functions accept a live
`*sftp.Client` or `*ssh.Client` and nothing else. Do not add a constructor that
takes a host, a user, a key path, an `ssh.ClientConfig`, or a
`HostKeyCallback`. The first consumer already owns authentication, host-key
policy and connection lifetime; a second transport inside this library would
have different failure modes from the one the caller is already using, and
would be discovered only when the two disagreed. `UploadOverSSH` opens an SFTP
subsystem and closes that subsystem — it must never close the `*ssh.Client`.

**Upload and download stay one implementation.** Both directions go through
`session` in `walk.go` with `src` and `dst` swapped. If you find yourself
writing a download-specific branch, that is the moment the two safety
properties start to diverge, and the download side is where "it is only
reading" becomes "it wrote outside the destination". Put the difference behind
`fileSystem` instead.

**`escapesRoot` is lexical, and that is not a shortcut.** It needs no I/O, so
it cannot be raced by a target that changes between the check and the copy; it
gives the same answer on the remote side, where there is no `EvalSymlinks` to
call; and it is *sound* only because nothing is ever read through a replicated
link. If you ever make `SymlinkReplicate` follow a link, that soundness
argument is gone and the check has to be rebuilt. `TestEscapesRoot` and
`TestEscapesRootIsExactAtTheBoundary` pin the semantics, including that
`a/link -> ".."` resolves to the root and is *inside* it while `link -> ".."`
is not.

**Every entry is `lstat`ed, deliberately.** `walkEntry` does not trust the
attributes that come back with `SSH_FXP_READDIR`: which stat they derive from
is server-defined, and a server reporting a symlink's target instead of the
link would let a link present itself as a regular file and bypass
`escapesRoot` entirely. This costs one round trip per entry. Do not "optimise"
it away without replacing the guarantee.

**Directory modes are applied on the way out of the walk**
(`enterDir` → `applyDirAttrs`), not at creation. `TestReadOnlyDirectoryReceivesItsContents`
is the case: a `0500` source directory cannot accept the files that belong in
it if its mode lands first. Both SCP implementations in the prior art apply the
mode at creation because their protocol requires it; over SFTP we are not
obliged to.

**Cancellation is gated on the reader, not in a hand-rolled copy loop.**
`ctxReader` deliberately does not implement `io.WriterTo`, so `io.Copy` cannot
bypass it, while still finding `(*sftp.File).ReadFrom` on the write side and
keeping pipelined writes. Replacing it with a manual loop would check the
context just as often and give up most of the throughput.
`TestCancelDuringOneFileStopsPromptly` and
`TestCancelBetweenFilesStopsPromptly` both fail if cancellation is only
checked once at the start.

**Temp files must always be cleaned up on the failure path.** Every error after
`createExcl` in `transfer` goes through `discard`, which removes the temp file
before returning. A failing sync that litters the destination with
`.sftpsync-tmp` debris looks like data to whatever reads that directory next.
The cancellation tests assert `tempDebris` is empty.

**`Result` is returned even when the transfer fails.** That is the difference,
for an operator, between "nothing moved" and "half the tree moved". A change
that returns `nil, err` on the error path breaks the contract the callers of
this library were written against.

**Dry run and transfer must report the same set.** `WithDryRun` is an option on
the same functions rather than a separate `Plan` entry point precisely so the
two cannot drift. `TestDryRunReportsTheSameSetItWouldTransfer` compares
`Result.Entries` with `reflect.DeepEqual`; directory listings are sorted in
`fs.go` so that comparison is meaningful.

**Two dependencies.** `pkg/sftp` and `golang.org/x/crypto`. Adding a third
needs a reason in the commit message. There is no logging library here and
should not be; callers log the `Result`.

**Scope that has already been declined.** Delta transfer, compression, resume,
bandwidth limiting, `--delete` semantics, `chown`, and anything requiring a
binary on the remote host. If one of these turns out to be needed it is a later
minor version with the decision recorded in `CHANGELOG.md`, not a quiet
addition. A tar-over-exec strategy, if it ever lands, is a *second* strategy
beside this one, not a replacement for it.

## Tests

Tests run against `pkg/sftp`'s own server implementation over a `net.Pipe`
(`server_test.go`), so the "remote" side is a real temp directory reached over
the real protocol. No network, no ssh daemon, no external binary. Keep it that
way: a mock would only assert what this package already believes about SFTP.

`newSlowTestClient` inserts a per-packet delay so a transfer can be interrupted
at a known point. Tests that need to cancel mid-transfer poll for the temp file
to appear rather than sleeping, so they are deterministic rather than merely
usually right.
