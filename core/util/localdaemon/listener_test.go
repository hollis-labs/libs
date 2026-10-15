package localdaemon

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestListenerTCP(t *testing.T) {
	ln, err := Listener("tcp:127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if ln.Addr().Network() != "tcp" {
		t.Fatalf("network = %s", ln.Addr().Network())
	}
}

func TestListenerRejectsBadAddresses(t *testing.T) {
	for _, a := range []string{"", "unix:", "tcp:", "http://x", "127.0.0.1:80"} {
		if ln, err := Listener(a); err == nil {
			ln.Close()
			t.Fatalf("Listener(%q) should fail", a)
		}
	}
}

func unixOnly(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are not supported by this package on windows")
	}
}

// shortDir returns a temp dir with a short path: sun_path is limited to about
// 104 bytes on macOS, and t.TempDir() paths there exceed it.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ld")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestListenerUnixCreatesDirRemovesStaleSocketAndSetsMode(t *testing.T) {
	unixOnly(t)
	sock := filepath.Join(shortDir(t), "run", "d.sock")

	// A crashed daemon's leftover: a socket file with nothing listening.
	if err := os.MkdirAll(filepath.Dir(sock), 0o750); err != nil {
		t.Fatal(err)
	}
	stale, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	stale.Close()
	if fi, serr := os.Stat(sock); serr != nil || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("setup: stale socket missing: %v", serr)
	}

	ln, err := Listener("unix:" + sock)
	if err != nil {
		t.Fatalf("stale socket should have been replaced: %v", err)
	}
	defer ln.Close()
	fi, err := os.Stat(sock)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v, want 0600", fi.Mode().Perm())
	}
}

// TestListenerUnixDoesNotStealLiveSocket is the removeStaleSocket regression
// from Tether's CW-20260906-0042 split-brain: a socket file with a live
// listener behind it must survive, and the second daemon must be refused.
func TestListenerUnixDoesNotStealLiveSocket(t *testing.T) {
	unixOnly(t)
	sock := filepath.Join(shortDir(t), "d.sock")
	live, err := Listener("unix:" + sock)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	accepted := make(chan struct{}, 1)
	go func() {
		if c, aerr := live.Accept(); aerr == nil {
			c.Close()
			accepted <- struct{}{}
		}
	}()

	if ln, lerr := Listener("unix:" + sock); !errors.Is(lerr, ErrAlreadyRunning) {
		if ln != nil {
			ln.Close()
		}
		t.Fatalf("second Listener = %v, want ErrAlreadyRunning", lerr)
	}
	// The original must still be reachable.
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("live socket was removed or broken: %v", err)
	}
	c.Close()
	<-accepted
}

func TestListenerUnixRefusesNonSocketFile(t *testing.T) {
	unixOnly(t)
	p := filepath.Join(t.TempDir(), "important.txt")
	if err := os.WriteFile(p, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ln, err := Listener("unix:" + p); err == nil {
		ln.Close()
		t.Fatal("Listener must refuse a regular file")
	}
	if b, err := os.ReadFile(p); err != nil || string(b) != "data" { //nolint:gosec // G304: test-controlled path/binary
		t.Fatalf("regular file was touched: %q, %v", b, err)
	}
}
