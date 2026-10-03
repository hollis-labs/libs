package sftpsync

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/pkg/sftp"
)

// internalTestClient is a second, deliberately minimal harness. The behaviour
// tests live in package sftpsync_test and have their own; this one exists only
// so remoteFS — which is unexported — can be driven directly, and in particular
// so the remove-then-rename fallback can be exercised. The pkg/sftp server
// advertises posix-rename@openssh.com, so that branch is otherwise unreachable
// from a test even though it is the branch every non-OpenSSH server takes.
func internalTestClient(t *testing.T) *sftp.Client {
	t.Helper()

	serverConn, clientConn := net.Pipe()
	server, err := sftp.NewServer(serverConn)
	if err != nil {
		t.Fatalf("sftp.NewServer: %v", err)
	}
	go server.Serve() //nolint:errcheck // returns io.EOF when the pipe closes

	client, err := sftp.NewClientPipe(clientConn, clientConn)
	if err != nil {
		t.Fatalf("sftp.NewClientPipe: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	return client
}

func TestNewRemoteFSDetectsPosixRename(t *testing.T) {
	f := newRemoteFS(internalTestClient(t))
	if !f.posixRename {
		t.Error("posix-rename@openssh.com not detected; pkg/sftp's server advertises it")
	}
}

// TestRemoteRenameReplacesExistingFile runs the same replacement through both
// branches. Both must leave the destination holding the new contents and no
// temp file behind — the difference is only whether the swap is atomic.
func TestRemoteRenameReplacesExistingFile(t *testing.T) {
	for _, tc := range []struct {
		name        string
		posixRename bool
	}{
		{"posix-rename extension", true},
		{"remove-then-rename fallback", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := remoteFS{c: internalTestClient(t), posixRename: tc.posixRename}

			dir := t.TempDir()
			target := filepath.Join(dir, "settings.yaml")
			tmp := filepath.Join(dir, ".settings.yaml.abcd.sftpsync-tmp")

			if err := os.WriteFile(target, []byte("old\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(tmp, []byte("new\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			if err := f.rename(tmp, target); err != nil {
				t.Fatalf("rename: %v", err)
			}

			got, err := os.ReadFile(target)
			if err != nil {
				t.Fatalf("read target: %v", err)
			}
			if string(got) != "new\n" {
				t.Errorf("target contents: got %q, want %q", got, "new\n")
			}
			if _, err := os.Stat(tmp); err == nil {
				t.Error("the temp file survived the rename")
			}
		})
	}
}

// TestRemoteRenameCreatesWhenAbsent covers the ordinary case, where the
// fallback's speculative Remove has nothing to remove and must not turn that
// into an error.
func TestRemoteRenameCreatesWhenAbsent(t *testing.T) {
	f := remoteFS{c: internalTestClient(t), posixRename: false}

	dir := t.TempDir()
	tmp := filepath.Join(dir, ".new.abcd.sftpsync-tmp")
	target := filepath.Join(dir, "new.yaml")
	if err := os.WriteFile(tmp, []byte("fresh\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := f.rename(tmp, target); err != nil {
		t.Fatalf("rename onto a path that does not exist: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("target not created: %v", err)
	}
}

// TestRemotePathsAreSlashSeparated pins the one thing that would break this
// package when compiled for Windows: remote paths must never take the local
// separator.
func TestRemotePathsAreSlashSeparated(t *testing.T) {
	var f remoteFS
	if got := f.resolve("/srv/app", "config/settings.yaml"); got != "/srv/app/config/settings.yaml" {
		t.Errorf("resolve: got %q", got)
	}
	if got := f.resolve("/srv/app", ""); got != "/srv/app" {
		t.Errorf("resolve with an empty rel: got %q, want the root", got)
	}
	if got := f.clean("/srv/app/../app/"); got != "/srv/app" {
		t.Errorf("clean: got %q", got)
	}
}
