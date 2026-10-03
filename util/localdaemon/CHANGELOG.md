# Changelog

All notable changes to go-localdaemon are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Write the entry for a release here BEFORE cutting its tag: the release workflow
refuses a tag whose CHANGELOG has no heading for it.

## v0.1.0 — 2026-09-30

### Added

- Initial extraction of the local-daemon lifecycle primitives:
  - `PIDFile` (`Write`, `Read`, `Remove`) with always-atomic writes, and `IsAlive`.
  - `TryAcquire`, `Acquire`, `Lock` (`Release`, `SetInfo`, `Path`) and `HeldError`: a flock single-instance lock held for the daemon's lifetime and released by the kernel on `SIGKILL`.
  - `VerifyCommand`: `ps`-based process-identity check.
  - `Spawn` (`SpawnOptions`, `Detach`): detached re-exec of the running binary.
  - `Stop` (`StopOptions`, `DefaultStopOptions`): SIGTERM then SIGKILL, with a required identity `Verify` hook.
  - `WaitReady`: poll-until-ready.
  - `Listener`: optional `unix:`/`tcp:` listener with safe stale-socket removal.
- Build tags: the Lock, VerifyCommand, Spawn, Stop and unix-socket pieces are unix-only and return `ErrUnsupported` elsewhere.
