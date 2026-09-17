// Package main is a runnable end-to-end demonstration of go-sftpsync: it
// previews a directory transfer, then performs it, and prints both reports.
//
// It runs against an in-process pkg/sftp server over a net.Pipe, so there is
// no host to reach, no credential to supply and nothing to clean up outside a
// temp directory. The "remote" side is an ordinary directory that the server
// serves; everything the library does to it — MkdirAll, SSH_FXP_SETSTAT,
// posix-rename, SSH_FXP_SYMLINK — goes over the real protocol.
//
// Run from the repo root:
//
//	go run ./examples/preview
//
// Expected output (temp paths and duration vary):
//
//	plan: upload /tmp/.../app -> /tmp/.../deployed (dry run): 4 files, 3 dirs, 1 symlinks, 96 bytes in 0s
//	  dir      .                      0755
//	  file     README                 0644     24 B
//	  dir      bin                    0755
//	  file     bin/start.sh           0755     29 B
//	  dir      config                 0755
//	  symlink  config/current.yaml    -> settings.yaml
//	  file     config/production.yaml 0644     20 B
//	  file     config/settings.yaml   0644     23 B
//	nothing was written yet; the destination does not exist
//
//	done: upload /tmp/.../app -> /tmp/.../deployed: 4 files, 3 dirs, 1 symlinks, 96 bytes in 2ms
//	deployed bin/start.sh with mode -rwxr-xr-x
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"

	sftpsync "github.com/hollis-labs/go-sftpsync"
	"github.com/pkg/sftp"
)

func main() {
	ctx := context.Background()

	root, err := os.MkdirTemp("", "sftpsync-example-*")
	if err != nil {
		log.Fatalf("temp dir: %v", err)
	}
	defer os.RemoveAll(root) //nolint:errcheck // best effort on an example's scratch dir

	source := filepath.Join(root, "app")
	dest := filepath.Join(root, "deployed")
	if err := buildSourceTree(source); err != nil {
		log.Fatalf("build source tree: %v", err)
	}

	client, stop, err := inProcessSFTP()
	if err != nil {
		log.Fatalf("start in-process sftp: %v", err)
	}
	defer stop()

	// Plan first. The dry run walks exactly what the transfer would walk, so
	// this is the same file set, not a second opinion about it.
	plan, err := sftpsync.Upload(ctx, client, source, dest, sftpsync.WithDryRun(true))
	if err != nil {
		log.Fatalf("preview: %v", err)
	}
	fmt.Println("plan:", plan)
	for _, e := range plan.Entries {
		printEntry(e)
	}
	if _, err := os.Stat(dest); os.IsNotExist(err) {
		fmt.Println("nothing was written yet; the destination does not exist")
	}

	// Then do it.
	result, err := sftpsync.Upload(ctx, client, source, dest)
	if err != nil {
		log.Fatalf("upload: %v", err)
	}
	fmt.Println("\ndone:", result)

	// The executable bit survived, which is the point of WithPreserveMode.
	info, err := os.Stat(filepath.Join(dest, "bin", "start.sh"))
	if err != nil {
		log.Fatalf("stat deployed script: %v", err)
	}
	fmt.Printf("deployed bin/start.sh with mode %v\n", info.Mode())
}

func printEntry(e sftpsync.Entry) {
	switch e.Action {
	case sftpsync.ActionFile:
		fmt.Printf("  %-8s %-22s %04o %6d B\n", e.Action, e.Path, e.Mode.Perm(), e.Size)
	case sftpsync.ActionSymlink:
		fmt.Printf("  %-8s %-22s -> %s\n", e.Action, e.Path, e.Target)
	case sftpsync.ActionSkip:
		fmt.Printf("  %-8s %-22s (%s)\n", e.Action, e.Path, e.Reason)
	default:
		fmt.Printf("  %-8s %-22s %04o\n", e.Action, e.Path, e.Mode.Perm())
	}
}

func buildSourceTree(root string) error {
	files := []struct {
		rel     string
		content string
		mode    os.FileMode
	}{
		{"bin/start.sh", "#!/bin/sh\nexec ./app --serve\n", 0o755},
		{"config/settings.yaml", "listen: 127.0.0.1:8080\n", 0o644},
		{"config/production.yaml", "listen: 0.0.0.0:443\n", 0o644},
		{"README", "deployed by go-sftpsync\n", 0o644},
	}
	for _, f := range files {
		full := filepath.Join(root, filepath.FromSlash(f.rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(f.content), f.mode); err != nil {
			return err
		}
		// WriteFile applies the umask; say what we mean.
		if err := os.Chmod(full, f.mode); err != nil {
			return err
		}
	}
	// A symlink that stays inside the root is replicated. One pointing at,
	// say, "../../etc/shadow" would be refused with sftpsync.ErrSymlinkEscape.
	return os.Symlink("settings.yaml", filepath.Join(root, "config", "current.yaml"))
}

// inProcessSFTP wires pkg/sftp's server implementation to a client over a
// net.Pipe. Real callers do not do this — they hand Upload an *sftp.Client
// they already opened over their own authenticated *ssh.Client. See
// examples/overssh.
func inProcessSFTP() (*sftp.Client, func(), error) {
	serverConn, clientConn := net.Pipe()

	server, err := sftp.NewServer(serverConn)
	if err != nil {
		return nil, nil, err
	}
	go server.Serve() //nolint:errcheck // returns io.EOF when the pipe closes

	client, err := sftp.NewClientPipe(clientConn, clientConn)
	if err != nil {
		return nil, nil, err
	}
	stop := func() {
		_ = client.Close()
		_ = server.Close()
	}
	return client, stop, nil
}
