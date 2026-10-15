package localdaemon

import (
	"fmt"
	"os"
	"sync"
)

// Lock is an exclusive advisory lock on a file, held for as long as the
// process keeps it. On supported platforms it is a flock(2) lock, so the
// kernel drops it when the holder exits by any means, SIGKILL included: a
// crashed daemon can never leave a lock behind, and a lock can never be
// "stale".
//
// The lock lives on the open file, not on the *Lock value: keep the *Lock
// reachable for the daemon's whole lifetime (a deferred Release counts). If
// it is garbage collected unreferenced, its file is closed and the lock is
// silently lost.
//
// The lock file is never deleted by this package. Deleting a lock file that
// another process has open or is about to open splits the lock across two
// inodes and lets two holders in.
type Lock struct {
	mu   sync.Mutex
	file *os.File
	path string
}

// HeldError reports that the lock is held by another open file description,
// usually another process. HolderPID and Info come from the lock file's
// content, which the holder wrote after acquiring: they are diagnostics, not
// proof (HolderPID is 0 when unreadable or mid-update).
type HeldError struct {
	Path      string
	HolderPID int
	// Info is the holder's opaque [Lock.SetInfo] payload, possibly empty.
	Info []byte
}

func (e *HeldError) Error() string {
	return fmt.Sprintf("localdaemon: lock held at %s (pid %d)", e.Path, e.HolderPID)
}

// Is makes errors.Is(err, ErrAlreadyRunning) true for a *HeldError.
func (e *HeldError) Is(target error) bool { return target == ErrAlreadyRunning }

// parseHolder splits lock-file content into the holder PID (first line) and
// the caller's opaque info (everything after it).
func parseHolder(b []byte) (pid int, info []byte) {
	for i, c := range b {
		if c == '\n' {
			info = append([]byte(nil), b[i+1:]...)
			b = b[:i]
			break
		}
	}
	n := 0
	for _, c := range b {
		if c < '0' || c > '9' || n > 1<<30 {
			return 0, info
		}
		n = n*10 + int(c-'0')
	}
	return n, info
}
