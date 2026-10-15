package sftpsync_test

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

// newTestClient returns an *sftp.Client wired to an in-process pkg/sftp
// server over a net.Pipe.
//
// The server implementation that ships with pkg/sftp serves the real
// filesystem, so the "remote" side of every test here is an ordinary temp
// directory. That is the point: the tests exercise the SFTP protocol —
// SSH_FXP_LSTAT, SSH_FXP_SYMLINK, SSH_FXP_SETSTAT, posix-rename — rather than
// a mock of what this package believes the protocol does. No network, no ssh
// daemon and no external sftp-server binary is involved.
func newTestClient(t *testing.T) *sftp.Client {
	t.Helper()
	return newSlowTestClient(t, 0)
}

// newSlowTestClient is newTestClient with a delay inserted before every packet
// the client writes. It makes a transfer take long enough to be interrupted at
// a known point, which is what the cancellation and interruption tests need to
// assert something stronger than "it finished".
//
// Borrowed from pkg/sftp's own test suite, which uses the same delayed-writer
// trick against its in-process server.
func newSlowTestClient(t *testing.T, delay time.Duration) *sftp.Client {
	t.Helper()

	serverConn, clientConn := net.Pipe()

	server, err := sftp.NewServer(serverConn)
	if err != nil {
		t.Fatalf("sftp.NewServer: %v", err)
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		if err := server.Serve(); err != nil && !errors.Is(err, io.EOF) {
			t.Logf("sftp server stopped: %v", err)
		}
	}()

	var w io.WriteCloser = clientConn
	if delay > 0 {
		w = &delayedWriter{WriteCloser: clientConn, delay: delay}
	}

	client, err := sftp.NewClientPipe(clientConn, w)
	if err != nil {
		t.Fatalf("sftp.NewClientPipe: %v", err)
	}

	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
		_ = serverConn.Close()
		<-served
	})

	return client
}

// delayedWriter slows the client side of the pipe to a known rate.
type delayedWriter struct {
	io.WriteCloser
	delay time.Duration
}

func (w *delayedWriter) Write(p []byte) (int, error) {
	time.Sleep(w.delay)
	return w.WriteCloser.Write(p)
}

// tree builds a directory tree from a declarative description, so a test says
// what shape it needs rather than how to lay it out.
type tree map[string]entry

type entry struct {
	// content of a regular file.
	content string
	// mode for a file or directory; 0 means 0644 for files, 0755 for dirs.
	mode os.FileMode
	// dir marks an empty directory. Parent directories are implicit.
	dir bool
	// symlinkTo makes this a symlink with the given target, verbatim.
	symlinkTo string
}

func writeTree(t *testing.T, root string, spec tree) {
	t.Helper()

	// Sorted so parents land before children, and so a failing test fails the
	// same way twice.
	for _, rel := range sortedKeys(spec) {
		e := spec[rel]
		full := filepath.Join(root, filepath.FromSlash(rel))

		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir parent of %s: %v", rel, err)
		}

		switch {
		case e.symlinkTo != "":
			if err := os.Symlink(e.symlinkTo, full); err != nil {
				t.Fatalf("symlink %s -> %s: %v", rel, e.symlinkTo, err)
			}
		case e.dir:
			mode := e.mode
			if mode == 0 {
				mode = 0o755
			}
			if err := os.MkdirAll(full, mode); err != nil {
				t.Fatalf("mkdir %s: %v", rel, err)
			}
			// MkdirAll applies the umask; the test means what it says.
			if err := os.Chmod(full, mode); err != nil {
				t.Fatalf("chmod %s: %v", rel, err)
			}
		default:
			mode := e.mode
			if mode == 0 {
				mode = 0o644
			}
			if err := os.WriteFile(full, []byte(e.content), mode); err != nil {
				t.Fatalf("write %s: %v", rel, err)
			}
			if err := os.Chmod(full, mode); err != nil {
				t.Fatalf("chmod %s: %v", rel, err)
			}
		}
	}
}

func sortedKeys(spec tree) []string {
	keys := make([]string, 0, len(spec))
	for k := range spec {
		keys = append(keys, k)
	}
	// Lexical order puts "a" before "a/b", which is the order writeTree needs.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// tmpdir is t.TempDir with one addition: directory modes are relaxed before
// cleanup runs. Trees here deliberately contain 0500 directories — that is the
// point of TestReadOnlyDirectoryReceivesItsContents — and RemoveAll cannot
// delete what it cannot enter. Cleanups run last-registered-first, so this
// lands before TempDir's own.
func tmpdir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(p, 0o700)
			}
			return nil
		})
	})
	return dir
}
