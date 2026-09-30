package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// These tests drive the durability steps through the package's injection seams.
// The seed's regression tests for the same steps only checked the success path
// and an error-string literal, so removing the parent-directory fsync, the
// short-write check or the temp-file fsync left them green. Each test below goes
// red when the step it names is removed.

// swap replaces a seam for the duration of the test. None of these tests run in
// parallel, so a plain swap-and-restore is safe.
func swap[T any](t *testing.T, seam *T, v T) {
	t.Helper()
	old := *seam
	*seam = v
	t.Cleanup(func() { *seam = old })
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func noTempLeft(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

type publisher struct {
	name string
	// publish writes "payload" to path through one of the two entry points.
	publish func(path string) error
}

func publishers() []publisher {
	return []publisher{
		{"WriteFile", func(path string) error { return WriteFile(path, []byte("payload"), 0o600) }},
		{"Writer", func(path string) error {
			w, err := NewWriter(path, 0o600)
			if err != nil {
				return err
			}
			if _, err := w.Write([]byte("payload")); err != nil { //nolint:govet // scoped err in test
				_ = w.Abort()
				return err
			}
			return w.Close()
		}},
	}
}

// The temp file is fsynced BEFORE it is published: at the moment of its fsync
// the target must not exist yet, and the hook must actually run.
func TestTempFileIsSyncedBeforePublish(t *testing.T) {
	for _, p := range publishers() {
		t.Run(p.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "out.txt")
			calls := 0
			swap(t, &syncFile, func(f *os.File) error {
				calls++
				if exists(path) {
					t.Errorf("temp fsync ran after the target was published")
				}
				return f.Sync()
			})
			if err := p.publish(path); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("temp file fsynced %d times, want exactly 1", calls)
			}
		})
	}
}

// After the rename, the PARENT DIRECTORY is fsynced, and it is the directory
// that holds the target. At that moment the target already exists.
func TestParentDirIsSyncedAfterRename(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory fsync is a no-op on windows")
	}
	for _, p := range publishers() {
		t.Run(p.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "out.txt")
			var synced []string
			swap(t, &syncDir, func(d *os.File) error {
				synced = append(synced, d.Name())
				if !exists(path) {
					t.Errorf("parent dir fsynced before the rename published the target")
				}
				return d.Sync()
			})
			if err := p.publish(path); err != nil {
				t.Fatal(err)
			}
			if len(synced) != 1 || filepath.Clean(synced[0]) != filepath.Clean(dir) {
				t.Fatalf("parent dir fsyncs = %v, want exactly [%s]", synced, dir)
			}
		})
	}
}

// A failing fsync must fail the write, not be swallowed.
func TestFsyncFailuresAreReported(t *testing.T) {
	boom := errors.New("injected fsync failure")
	for _, p := range publishers() {
		t.Run(p.name+"/temp", func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "out.txt")
			swap(t, &syncFile, func(*os.File) error { return boom })
			err := p.publish(path)
			if !errors.Is(err, boom) {
				t.Fatalf("err = %v, want it to wrap the fsync failure", err)
			}
			if exists(path) {
				t.Error("target published although the temp file could not be synced")
			}
			noTempLeft(t, dir)
		})
		t.Run(p.name+"/parentdir", func(t *testing.T) {
			if runtime.GOOS == "windows" {
				t.Skip("directory fsync is a no-op on windows")
			}
			dir := t.TempDir()
			path := filepath.Join(dir, "out.txt")
			swap(t, &syncDir, func(*os.File) error { return boom })
			err := p.publish(path)
			if !errors.Is(err, boom) || !strings.Contains(err.Error(), "parent dir fsync") {
				t.Fatalf("err = %v, want the parent dir fsync failure reported", err)
			}
		})
	}
}

// A writer that stores fewer bytes than asked without returning an error must
// not get its truncated file published over the target.
func TestShortWriteIsAnErrorAndNothingIsPublished(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	swap(t, &writeTemp, func(f *os.File, p []byte) (int, error) {
		return f.Write(p[:len(p)-1]) // silently under-write, no error
	})
	err := WriteFile(path, []byte("payload"), 0o600)
	if err == nil || !strings.Contains(err.Error(), "short write: 6 of 7 bytes") {
		t.Fatalf("err = %v, want a short-write error", err)
	}
	got, readErr := os.ReadFile(path) //nolint:gosec // test reads a path under t.TempDir()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "original" {
		t.Fatalf("target = %q, want the original untouched", got)
	}
	noTempLeft(t, dir)
}

// A failed Write must not be forgotten: a caller that ignores the error, or an
// io.Copy that returns early, still calls Close, which used to fsync and rename a
// truncated file over the target.
func TestClosePublishesNothingAfterAFailedWrite(t *testing.T) {
	boom := errors.New("injected write failure")
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := NewWriter(path, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("good-prefix")); err != nil { //nolint:govet // scoped err in test
		t.Fatal(err)
	}
	swap(t, &writeTemp, func(*os.File, []byte) (int, error) { return 0, boom })
	if _, err := w.Write([]byte("more")); !errors.Is(err, boom) { //nolint:govet // scoped err in test
		t.Fatalf("Write err = %v, want the injected failure", err)
	}
	// The failure is sticky: a later Write does not reach the file.
	if _, err := w.Write([]byte("later")); !errors.Is(err, boom) { //nolint:govet // scoped err in test
		t.Fatalf("Write after failure = %v, want the recorded error", err)
	}
	err = w.Close()
	if !errors.Is(err, boom) {
		t.Fatalf("Close err = %v, want it to return the write failure", err)
	}
	got, readErr := os.ReadFile(path) //nolint:gosec // test reads a path under t.TempDir()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "original" {
		t.Fatalf("target = %q, want the original untouched", got)
	}
	noTempLeft(t, dir)
}

// A short write with no error is a failed write too.
func TestShortWriteOnAWriterAbortsTheClose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	w, err := NewWriter(path, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	swap(t, &writeTemp, func(f *os.File, p []byte) (int, error) { return f.Write(p[:len(p)-1]) })
	if _, err := w.Write([]byte("payload")); err == nil { //nolint:govet // scoped err in test
		t.Fatal("a short write returned no error")
	}
	if err := w.Close(); err == nil || exists(path) { //nolint:govet // scoped err in test
		t.Fatalf("Close = %v, target exists = %v; want an error and nothing published", err, exists(path))
	}
	noTempLeft(t, dir)
}
