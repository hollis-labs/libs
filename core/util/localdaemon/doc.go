// Package localdaemon provides the mechanical, OS-level primitives a process
// needs to manage its own local background-daemon lifecycle. Every function
// takes an explicit path or address; the package has no configuration, no
// logger, no path convention and no dependencies beyond the standard library.
//
// # Primitives
//
//   - [PIDFile] and [IsAlive]: an atomically written PID file and a liveness
//     probe. A PID file is a hint, never a lock.
//   - [TryAcquire] / [Acquire] and [Lock]: a flock(2) single-instance lock,
//     held for the daemon's whole life and released by the kernel however the
//     process dies, SIGKILL included. This is the authoritative
//     "am I the only one" guard: with it held, no PID file or socket check can
//     be raced into running two daemons.
//   - [VerifyCommand]: confirms via ps that a PID still names your program,
//     so a recycled PID is never mistaken for your daemon.
//   - [Spawn]: re-executes the running binary as a detached background
//     process.
//   - [Stop]: SIGTERM, wait, SIGKILL, wait, and only after identity has been
//     verified through a required hook.
//   - [WaitReady]: polls a caller-defined readiness check.
//   - [Listener]: optional, independent helper for scheme-prefixed
//     unix:/tcp: control sockets with safe stale-socket handling.
//
// # Typical daemon startup
//
// In the daemon process: [TryAcquire] the lock first and keep it until exit;
// then bind the control socket ([Listener]); then [PIDFile.Write]. In the
// launching CLI: [Spawn], then [WaitReady] on a check that dials the daemon.
// To stop: read the PID from the [PIDFile], then [Stop] with a Verify hook
// built on [VerifyCommand].
//
// # Platform support
//
// Lock, VerifyCommand, Spawn, Stop and unix: listener addresses are
// implemented for darwin, linux, freebsd, netbsd, openbsd and dragonfly.
// Elsewhere they return [ErrUnsupported] (build tags keep the package
// compiling everywhere). [PIDFile] and tcp: listeners are portable, but
// [IsAlive] reports false on unsupported platforms. See the README.
package localdaemon
