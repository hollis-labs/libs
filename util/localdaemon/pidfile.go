package localdaemon

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// PIDFile is a file holding the PID of a running daemon. The zero value with
// an empty Path is not usable.
//
// A PID file is a hint, not a lock: it can be deleted, corrupted, or left
// behind by a crash, and the PID it names can be recycled by an unrelated
// process. Use [TryAcquire] as the authoritative single-instance guard and
// [VerifyCommand] before signaling a PID read from a file.
type PIDFile struct {
	Path string
}

// Write records pid atomically: readers see the complete previous content or
// the complete new content, never a partial file. Parent directories are
// created as needed and the file is mode 0600.
//
// Write refuses with [ErrAlreadyRunning] when the file names a different PID
// that [IsAlive] reports live. That check is advisory and racy (two writers
// can both pass it) and cannot tell a live daemon from a recycled PID; the
// [Lock] is what makes single instance true. Writing the PID that is already
// recorded is always allowed.
func (f PIDFile) Write(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("%w: %d", ErrInvalidPID, pid)
	}
	if existing, err := f.Read(); err == nil && existing != pid && IsAlive(existing) {
		return fmt.Errorf("%w (pid %d, pidfile %s)", ErrAlreadyRunning, existing, f.Path)
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o750); err != nil {
		return fmt.Errorf("create pidfile dir: %w", err)
	}
	if err := writeFileAtomic(f.Path, []byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
		return fmt.Errorf("write pidfile: %w", err)
	}
	return nil
}

// Read returns the recorded PID. A missing file yields an error matching
// [fs.ErrNotExist]; a file that does not hold one positive integer yields
// [ErrCorruptPIDFile].
func (f PIDFile) Read() (int, error) {
	b, err := os.ReadFile(f.Path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("%w: %s", ErrCorruptPIDFile, f.Path)
	}
	return pid, nil
}

// Remove deletes the PID file. A missing file is not an error.
func (f PIDFile) Remove() error {
	if err := os.Remove(f.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// writeFileAtomic writes data to a uniquely named temporary file in the
// target's directory, flushes it, and renames it over path. A crash between
// create and rename can leave a ".tmp-*" sibling; it never leaves a partial
// path. The unique name keeps concurrent writers from sharing a temp file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		return fail(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}
