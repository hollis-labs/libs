// Package procuse reports process-use evidence without mutating the target.
// A check probes cwd and open references with lsof and validates structured
// field records. Every check first confirms visibility of this process's cwd
// with a bounded own-PID selection (no recursive cwd scan), and of a file this
// process holds open in private scratch storage. Both controls use the same
// command builder and parser as target probes. Failed controls or uncertain
// observations return Unknown: retain. A hand-built Result is never proof;
// callers must check Result.SafeToClean rather than Outcome alone.
//
// Target and scratch must be canonical directories on the same device, with
// scratch outside the target. Known unscannable virtual filesystems are refused
// on Linux and macOS. Other platforms return Unknown. Symlink paths refuse
// before control creation. The host must keep these roots stable during Check;
// observations do not protect against an unrelated actor replacing an ancestor.
//
// NotInUse means no reference visible to lsof for this user at scan time, not
// universal absence. Same-user PR_SET_DUMPABLE=0 processes and threads with a
// private cwd can be invisible. Other users, namespaces and containers can hide
// references. +D takes a directory snapshot: files opened after scanning starts
// can be missed. Nested mounts and symlinks are not crossed. FUSE, NFS and overlay
// filesystems add visibility uncertainty even on the same device. Independent
// pin-lock proof, ownership/retention checks and coordination with new launches
// remain required; no negative result alone authorizes cleanup.
//
// This package does not enumerate candidates, rename or delete targets. Its two
// os.Remove calls clean up only its own private control file and directory.
// Direct commands run with a minimal C locale environment, bounded output and
// process-group cancellation on Unix. Four invocations can take about
// 4 x (5 seconds + 1 second pipe shutdown), excluding filesystem preflight and
// control cleanup; use the parent context to request a shorter command budget.
package procuse
