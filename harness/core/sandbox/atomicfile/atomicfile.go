package atomicfile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

// Injection seams. Production always uses the defaults, which are exactly the
// calls the code made before the seams existed. Tests substitute them (and
// restore them) to observe or fail the durability steps, which an ordinary
// filesystem never lets a test fail on demand. They are package-level, so a test
// that replaces one must not run in parallel with other atomicfile tests.
var (
	// writeTemp writes WriteFile's data to its temp file.
	writeTemp = func(f *os.File, p []byte) (int, error) { return f.Write(p) }
	// syncFile fsyncs a temp file before it is published.
	syncFile = func(f *os.File) error { return f.Sync() }
	// syncDir fsyncs the parent directory after the rename.
	syncDir = func(d *os.File) error { return d.Sync() }
)

// syncParentDir fsyncs the parent directory of path so the rename is
// durable across power loss on POSIX. On Windows os.Open on a directory
// returns an error and directory fsync is not a meaningful operation, so
// this is a no-op there.
func syncParentDir(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("atomicfile: open parent dir: %w", err)
	}
	syncErr := syncDir(dir)
	closeErr := dir.Close()
	if syncErr != nil {
		return fmt.Errorf("atomicfile: parent dir fsync: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("atomicfile: parent dir close: %w", closeErr)
	}
	return nil
}

// WriteFile writes data to path atomically. The caller-supplied mode
// is applied to the destination file.
//
// Failure modes:
//   - Temp creation, write, fsync, and rename errors are returned wrapped.
//   - The temp file is removed on any error (best effort).
//   - If path already exists, it is replaced atomically.
func WriteFile(path string, data []byte, mode os.FileMode) (retErr error) {
	if path == "" {
		return errors.New("atomicfile: empty path")
	}
	dir := filepath.Dir(path)
	base := filepath.Base(path)

	tmp, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return fmt.Errorf("atomicfile: create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if retErr != nil {
			_ = os.Remove(tmpName)
		}
	}()

	n, err := writeTemp(tmp, data)
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("atomicfile: write: %w", err)
	}
	if n != len(data) {
		_ = tmp.Close()
		return fmt.Errorf("atomicfile: short write: %d of %d bytes", n, len(data))
	}
	if err := syncFile(tmp); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("atomicfile: fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("atomicfile: close temp: %w", err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return fmt.Errorf("atomicfile: chmod: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("atomicfile: rename: %w", err)
	}
	if err := syncParentDir(path); err != nil {
		return err
	}
	return nil
}

// NewWriter returns a *Writer whose Close renames the temp file
// over path. If Close is never called, or an error occurs before Close, the
// temp file is removed.
//
// The returned writer is not safe for concurrent use. Call Abort to
// explicitly discard the temp file without publishing.
//
// Write semantics: the returned Write forwards directly to the underlying
// *os.File and returns (n, err) per io.Writer. Callers are responsible for
// handling short writes (n < len(p) with err == nil); NewWriter does
// not synthesize a short-write error on their behalf.
func NewWriter(path string, mode os.FileMode) (*Writer, error) {
	if path == "" {
		return nil, errors.New("atomicfile: empty path")
	}
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	tmp, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return nil, fmt.Errorf("atomicfile: create temp: %w", err)
	}
	return &Writer{
		f:      tmp,
		tmp:    tmp.Name(),
		target: path,
		mode:   mode,
	}, nil
}

var _ io.WriteCloser = (*Writer)(nil)

// Writer streams data into a sibling temp file and publishes it atomically on
// Close. Obtain one with NewWriter; it is not safe for concurrent use.
type Writer struct {
	f      *os.File
	tmp    string
	target string
	mode   os.FileMode
	closed bool
	// writeErr is the first failed Write; once set, Close aborts instead of
	// publishing.
	writeErr error
}

// Write appends p to the temp file. It forwards to the underlying *os.File and
// does not synthesize short-write errors of its own, but it REMEMBERS a failed
// write: the first error (or an io.ErrShortWrite when fewer bytes than asked
// were stored with no error) is recorded, later Writes return it without writing,
// and Close then discards the temp file and returns it instead of publishing a
// truncated file. Write after Close returns an error.
func (w *Writer) Write(p []byte) (int, error) {
	if w.closed {
		return 0, errors.New("atomicfile: write after close")
	}
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	n, err := writeTemp(w.f, p)
	if err == nil && n < len(p) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.writeErr = err
	}
	return n, err
}

// Close fsyncs and renames the temp over the target. If any step fails, the
// temp is removed. If an earlier Write failed, Close removes the temp and returns
// that error without touching the target. The parent-directory fsync runs AFTER
// the rename, so an error from it is returned even though the new content is
// already in place. Close after Abort returns nil.
func (w *Writer) Close() (retErr error) {
	if w.closed {
		return nil
	}
	w.closed = true
	defer func() {
		if retErr != nil {
			_ = os.Remove(w.tmp)
		}
	}()

	if w.writeErr != nil {
		_ = w.f.Close()
		return fmt.Errorf("atomicfile: aborted after failed write: %w", w.writeErr)
	}
	if err := syncFile(w.f); err != nil {
		_ = w.f.Close()
		return fmt.Errorf("atomicfile: fsync: %w", err)
	}
	if err := w.f.Close(); err != nil {
		return fmt.Errorf("atomicfile: close temp: %w", err)
	}
	if err := os.Chmod(w.tmp, w.mode); err != nil {
		return fmt.Errorf("atomicfile: chmod: %w", err)
	}
	if err := os.Rename(w.tmp, w.target); err != nil {
		return fmt.Errorf("atomicfile: rename: %w", err)
	}
	if err := syncParentDir(w.target); err != nil {
		return err
	}
	return nil
}

// Abort discards the temp without renaming. Safe to call after Close (no-op).
func (w *Writer) Abort() error {
	if w.closed {
		return nil
	}
	w.closed = true
	_ = w.f.Close()
	return os.Remove(w.tmp)
}
