# go-localdaemon

Local background-daemon lifecycle primitives: atomic PID file, flock single-instance lock, process-identity check, detached spawn, stop with escalation, poll-until-ready.

It is not a daemon framework, a CLI, a supervisor, or a launchd/systemd integration. Do not add log rotation, launchd metadata, a PID/socket path convention, or `start/stop/status` commands.

## Start Here

- `localdaemon` package — the importable API; its `doc.go` is the package documentation.
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
- The lock decides single instance, not the PID file. It is flock, held for the daemon's life, and must be released by the kernel when the holder dies by SIGKILL. Never replace it with a PID-file or socket check, and never add a "break stale lock" path that deletes the lock file (`TestLockReleasedOnSIGKILLOfHolder`, `TestTryAcquireRefusesDoubleAcquire`, `TestTryAcquireRefusedWhileHeldByAnotherProcess`, `TestTryAcquireConcurrentExactlyOneWins`). The lock file is left on disk on `Release`: unlinking it splits the lock across inodes. The lock fd must stay close-on-exec so spawned children never inherit it (`TestLockNotInheritedByChild`).
- `Stop` never signals a PID it cannot identify. `Verify` is required (`TestStopRequiresVerify`); a mismatch or a probe error signals nothing (`TestStopRefusesOnIdentityMismatch`, `TestStopVerifyErrorRefusesToSignal`); identity is checked again before SIGKILL because the PID can be recycled during the grace period (`TestStopReverifiesBeforeKill`). `pid <= 0` (kill(2) group semantics) and the caller's own PID are always refused (`TestStopNeverSignalsInvalidPIDs`). Unlike Cerberus, a failed identity probe refuses rather than proceeding.
- `Stop` escalates TERM then KILL with bounded waits, gives up with `ErrStopTimeout`, and does not escalate on context cancel (`TestStopTermExit`, `TestStopEscalatesToKillWhenTermIgnored`, `TestStopTimesOutWhenProcessWillNotDie`, `TestStopContextCancelDoesNotEscalate`).
- PID-file writes are always atomic: unique temp file in the same directory, fsync, rename. No `os.WriteFile` fast path, no shared `.tmp` name (`TestWriteAtomicNoTornRead`, `TestPIDFileWriteAtomicUnderConcurrentWriters`). The PID-file guard in `Write` is advisory; exclusion belongs to the lock (`TestPIDFileStaleDetection`, `TestPIDFileWriteRefusesLiveOtherPID`).
- `VerifyCommand` reports a missing or non-matching process as `(false, nil)` and a probe failure or ended context as an error, never as a mismatch (`TestVerifyCommandWithFakeRunner`, `TestVerifyCommandContextEndIsAnErrorNotAMismatch`, `TestVerifyCommandRealPS`).
- `Spawn` detaches, reaps its child in the background (an unreaped zombie reads as alive to `IsAlive`), and does not tie the child to the caller's context (`TestSpawnDetachesAndSurvivesParent`, `TestSpawnProcessGroupDetach`, `TestSpawnContextCancelAfterStartDoesNotKillChild`).
- `Listener` never deletes a live socket or a non-socket file (`TestListenerUnixDoesNotStealLiveSocket`, `TestListenerUnixRefusesNonSocketFile`). It stays in `listener*.go` and imports nothing from the other primitives beyond `ErrAlreadyRunning`.
- Unix-only code is behind the build tag `darwin || dragonfly || freebsd || linux || netbsd || openbsd` with an `_other.go` stub returning `ErrUnsupported`. After touching any of it run `GOOS=linux`, `GOOS=windows`, `GOOS=freebsd` `go vet ./...`.
- Standard library only. No log rotation, no launchd metadata, no path convention, no CLI.
- Tests use temp directories and re-executed helper processes (`helpers_test.go`, selected by `LOCALDAEMON_HELPER`), never real system daemons. A test may signal only a PID it started itself; helpers exit on their own after 60s. `WaitReady` and `Stop` tests use real timers, so keep margins generous.
- Behavioral equivalence with Cerberus's, Tether's or Nanite's code is not claimed. Their code was read; only their own daemon and lock tests were run, on copies.
