// Package procuse reports process-use evidence without mutating the target.
// A check probes both current working directories and open references through
// lsof, using validated NUL-delimited field records rather than display rows.
// Before trusting empty results, every check proves that the same mechanisms
// can see the caller's cwd and a file the caller holds open in explicit scratch
// storage. Failed controls or uncertain observations return Unknown: retain.
//
// NotInUse means no cwd or open reference visible to lsof for this user at the
// time of the probes. It does not prove universal absence: permissions, other
// users, namespaces, symlinks and nested mounts limit visibility. The host must
// supply a canonical target within its supported visibility scope. No symlink
// or mount cross-over is requested. An independent generation pin proof and
// the cooperating launch guard remain required to close the observation race.
//
// This package does not decide ownership or retention, enumerate candidates,
// rename targets or delete them. Scratch control files are private, transient,
// and removed after each check. No model CLI or provider home is consulted.
package procuse
