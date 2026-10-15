package localdaemon

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestPIDFileRoundTrip(t *testing.T) {
	f := PIDFile{Path: filepath.Join(t.TempDir(), "nested", "d.pid")}
	if _, err := f.Read(); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Read missing = %v, want ErrNotExist", err)
	}
	if err := f.Write(4242); err != nil {
		t.Fatal(err)
	}
	got, err := f.Read()
	if err != nil || got != 4242 {
		t.Fatalf("Read = %d, %v", got, err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(f.Path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
		}
	}
	if err := f.Remove(); err != nil {
		t.Fatal(err)
	}
	if err := f.Remove(); err != nil {
		t.Fatalf("Remove of missing file: %v", err)
	}
}

func tmpPath(t *testing.T, name string) string { return filepath.Join(t.TempDir(), name) }

func TestPIDFileWriteRejectsInvalidPID(t *testing.T) {
	f := PIDFile{Path: tmpPath(t, "d.pid")}
	for _, pid := range []int{0, -1} {
		if err := f.Write(pid); !errors.Is(err, ErrInvalidPID) {
			t.Fatalf("Write(%d) = %v, want ErrInvalidPID", pid, err)
		}
	}
}

func TestPIDFileReadCorrupt(t *testing.T) {
	for _, content := range []string{"", "abc\n", "-5\n", "0\n", "12 34\n"} {
		f := PIDFile{Path: tmpPath(t, "d.pid")}
		if err := os.WriteFile(f.Path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Read(); !errors.Is(err, ErrCorruptPIDFile) {
			t.Fatalf("Read(%q) = %v, want ErrCorruptPIDFile", content, err)
		}
	}
}

func TestPIDFileWriteSamePIDIsAllowed(t *testing.T) {
	f := PIDFile{Path: tmpPath(t, "d.pid")}
	for range 3 {
		if err := f.Write(os.Getpid()); err != nil {
			t.Fatal(err)
		}
	}
}

// TestWriteOverCorruptFileSucceeds: a corrupt file is stale, not a live daemon.
func TestPIDFileWriteReplacesCorruptFile(t *testing.T) {
	f := PIDFile{Path: tmpPath(t, "d.pid")}
	if err := os.WriteFile(f.Path, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.Write(os.Getpid()); err != nil {
		t.Fatal(err)
	}
}

// TestWriteAtomicNoTornRead runs readers against a writer that alternates
// between a short and a long payload. Readers use raw reads: any observation
// other than one of the two complete payloads is a torn (or empty) file.
func TestWriteAtomicNoTornRead(t *testing.T) {
	path := tmpPath(t, "d.pid")
	short, long := []byte("1\n"), []byte(strings.Repeat("9", 4000)+"\n")
	if err := writeFileAtomic(path, short, 0o600); err != nil {
		t.Fatal(err)
	}
	var stop atomic.Bool
	var bad atomic.Value
	var reads atomic.Int64
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				b, err := os.ReadFile(path) //nolint:gosec // G304: test-controlled path/binary
				if err != nil {
					bad.Store("read error: " + err.Error())
					return
				}
				reads.Add(1)
				if string(b) != string(short) && string(b) != string(long) {
					bad.Store("torn read of length " + strconv.Itoa(len(b)))
					return
				}
			}
		}()
	}
	for i := range 500 {
		data := short
		if i%2 == 1 {
			data = long
		}
		if err := writeFileAtomic(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stop.Store(true)
	wg.Wait()
	if v := bad.Load(); v != nil {
		t.Fatal(v)
	}
	if reads.Load() == 0 {
		t.Fatal("readers never ran")
	}
	matches, _ := filepath.Glob(path + ".tmp-*")
	if len(matches) != 0 {
		t.Fatalf("temp files left behind: %v", matches)
	}
}

// TestPIDFileWriteAtomicUnderConcurrentWriters drives the public Write path
// from several goroutines while readers use PIDFile.Read; concurrent writers
// must not corrupt each other through a shared temp file.
func TestPIDFileWriteAtomicUnderConcurrentWriters(t *testing.T) {
	f := PIDFile{Path: tmpPath(t, "d.pid")}
	pid := os.Getpid()
	if err := f.Write(pid); err != nil {
		t.Fatal(err)
	}
	var stop atomic.Bool
	var bad atomic.Value
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				if got, err := f.Read(); err != nil || got != pid {
					bad.Store("Read = " + strconv.Itoa(got) + ", " + errString(err))
					return
				}
			}
		}()
	}
	var ww sync.WaitGroup
	for range 4 {
		ww.Add(1)
		go func() {
			defer ww.Done()
			for range 200 {
				if err := f.Write(pid); err != nil {
					bad.Store(err.Error())
					return
				}
			}
		}()
	}
	ww.Wait()
	stop.Store(true)
	wg.Wait()
	if v := bad.Load(); v != nil {
		t.Fatal(v)
	}
}

func errString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}
