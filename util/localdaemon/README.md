# go-localdaemon

Local background-daemon lifecycle primitives: atomic PID file, flock single-instance lock, process-identity check, detached spawn, stop with escalation, poll-until-ready.

For a process that manages its own local daemon: the small OS-level pieces that Cerberus, Tether and Nanite each rebuilt by hand, in one place with tests. Six primitives (`PIDFile`, `Lock`/`TryAcquire`, `VerifyCommand`, `Spawn`, `Stop`, `WaitReady`) plus one optional, isolated `Listener` helper. Standard library only.

The design goal is that **the lock, not the PID file, decides who the single instance is**. A `flock(2)` lock held for the daemon's lifetime is released by the kernel even on `SIGKILL`, so a crashed daemon can't leave a stale lock and two daemons can't both believe they own the state. `Stop` will not signal a PID until a caller-supplied identity check (normally `VerifyCommand`) says the PID still belongs to the daemon.

## Status

**Pre-release.** This project is unreleased, not deployed, and has no outside consumers. It's being built in the open: the code, the docs, and this README describe what exists today, not a pitch for what's planned. Interfaces and behavior change without notice, and there are no compatibility guarantees yet.

## Install

```sh
go get github.com/hollis-labs/go-localdaemon
```

Requires Go 1.26.6 or newer. No dependencies outside the standard library.

## Usage

```go
package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"

	localdaemon "github.com/hollis-labs/go-localdaemon"
)

func main() {
	dir, err := os.MkdirTemp("", "localdaemon-example-")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	// The lock is the single-instance guard: hold it for the daemon's whole
	// life. The kernel drops it if the process dies, even on SIGKILL.
	lock, err := localdaemon.TryAcquire(filepath.Join(dir, "daemon.lock"))
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = lock.Release() }()

	// A second instance is refused.
	_, err = localdaemon.TryAcquire(lock.Path())
	var held *localdaemon.HeldError
	fmt.Println("second instance refused:", errors.As(err, &held))

	// The PID file is written atomically, so a reader never sees a torn file.
	pidFile := localdaemon.PIDFile{Path: filepath.Join(dir, "daemon.pid")}
	if err := pidFile.Write(os.Getpid()); err != nil {
		log.Fatal(err)
	}
	pid, err := pidFile.Read()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("pid recorded:", pid == os.Getpid(), "alive:", localdaemon.IsAlive(pid))
}
```

The same program lives in [`examples/hello`](./examples/hello/main.go). `Spawn`, `Stop`, `WaitReady`, `VerifyCommand` and `Listener` have runnable `Example*` functions in [`example_test.go`](./example_test.go) (`Spawn` re-executes the current binary, so its example is compiled but not run).

A daemon's startup order: `TryAcquire` the lock and keep it until exit; bind the socket (`Listener`); write the PID file. The launching CLI does `Spawn`, then `WaitReady` on a check that dials the daemon. To stop: read the PID file, then `Stop` with `Verify` built on `VerifyCommand`.

## Platform support

| Piece | darwin, linux, freebsd, netbsd, openbsd, dragonfly | windows and others |
|---|---|---|
| `PIDFile` | yes | yes (atomic rename; the live-PID guard in `Write` is a no-op because `IsAlive` is always false) |
| `IsAlive` | yes (signal 0) | always `false` |
| `TryAcquire`, `Acquire`, `Lock` | yes (flock) | `ErrUnsupported` |
| `VerifyCommand` | yes (needs `ps` on `PATH`) | `ErrUnsupported` |
| `Spawn` | yes | `ErrUnsupported` |
| `Stop` | yes | `ErrUnsupported` |
| `WaitReady` | yes | yes |
| `Listener` `tcp:` | yes | yes |
| `Listener` `unix:` | yes | `ErrUnsupported` |

Unix-only pieces are behind build tags; on other platforms the package compiles and returns `ErrUnsupported` rather than silently doing nothing. It is developed and tested on macOS; CI runs Linux. Windows is compile-checked (`GOOS=windows go vet`) only. There is no Windows implementation of any daemon primitive.

## Known limitations

- **`flock` on network filesystems.** Locks on NFS and similar are emulated or forwarded differently by kernel and server (on Linux NFS, `flock` becomes a whole-file byte-range lock; some servers or mounts do not honor it at all). Keep the lock file on a local filesystem. This is not tested here.
- **`IsAlive` is not identity.** PIDs are recycled, and a zombie child that its parent has not reaped still counts as alive. `Spawn` reaps its own child; if you start children another way, `Wait` on them.
- **The gap between `Verify` and the signal.** `Stop` checks identity, then signals, and checks again before `SIGKILL`; a PID recycled inside that instant cannot be excluded by any PID-based scheme. It is narrow, not closed.
- **`PIDFile.Write`'s live-PID refusal is advisory.** Two writers can both pass it, and a recycled PID makes it refuse wrongly. Use the lock for exclusion.
- **Keep the `*Lock` reachable.** The lock lives on the open file. If the `Lock` value is garbage collected, Go closes the file and the lock is lost. Hold it (e.g. `defer lock.Release()` in `main`) until exit.
- **The lock file is never deleted.** Deleting a lock file that another process has open splits the lock across two inodes and admits two holders; Cerberus's own implementation deletes it. The file stays on disk after `Release`. `TryAcquire` re-checks that the path still names the inode it locked, but that guard is not exercised by a test (the race is not reproducible on demand).
- **`ps` parsing.** `VerifyCommand` matches the full command line as `ps -p PID -o command=` prints it. An empty result or a non-zero `ps` exit means "not a match". If `ps` cannot be run at all it returns an error, and `Stop` then refuses to signal.
- **Holder PID in `HeldError` is a diagnostic.** It is read from the lock file after the holder wrote it; it can be `0` in the instant a holder is updating it.
- **`Acquire` polls** (25ms) instead of blocking in the kernel, because a blocking `flock` cannot be cancelled by a context. It is not FIFO-fair.
- **`Listener`'s dial check is not exclusion.** It only avoids deleting a socket that is live. Take the lock first.
- **`Spawn` re-executes `os.Executable()`.** It does not launch other binaries, and it ignores context cancellation after the child starts, by design (a daemon outlives its launcher).

## Compatibility

This module is pre-1.0: minor releases may break the exported API. Pin an exact version, and read [CHANGELOG.md](./CHANGELOG.md) before upgrading; every breaking change is listed there. The behavior of each primitive was designed from reading the Cerberus, Tether and Nanite implementations, and it deliberately differs from them in places (see [AGENTS.md](./AGENTS.md)); no behavioral equivalence with any of them is claimed.

## Out of scope

- **Log rotation.** Only one app (Tether) rotates a daemon log; that is a single-app pattern, not a shared one. `Spawn` takes the files to write to; rotating them is the caller's job.
- **launchd (or systemd) metadata.** No plist, `launchctl`, or supervisor-origin detection. That belongs in a separate library.
- **A CLI or `start/stop/status/restart` command set,** and any restart orchestration beyond `Stop` + `Spawn` + `WaitReady`.
- **A PID or socket path convention.** Every function takes an explicit path or address.
- **Process supervision** (restart on crash, health loops), **stray-process sweeps,** and **Windows.**

## Development

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

CI (`.github/workflows/check.yml`) is the full gate. The tests re-execute the test binary as short-lived helper processes; they use temp directories, start no system daemons, and signal only PIDs they started themselves.

## License

MIT — see [LICENSE](./LICENSE).
