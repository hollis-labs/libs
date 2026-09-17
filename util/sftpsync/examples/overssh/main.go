// Package main demonstrates the shape a real caller uses: you own the
// connection, this library only uses it.
//
// Every decision that makes an SSH connection trustworthy — which key, which
// host-key policy, which timeout, when to close it — is made here, in the
// caller, before go-sftpsync is involved at all. The library is handed a live
// client and never dials, never reads ~/.ssh/config and never sees a
// credential.
//
// Run from the repo root, against a host you already have key access to:
//
//	go run ./examples/overssh \
//	    -host example.internal:22 \
//	    -user deploy \
//	    -key ~/.ssh/id_ed25519 \
//	    -local ./examples \
//	    -remote /srv/app/examples \
//	    -dry-run
//
// Drop -dry-run to transfer. For a version that runs with no host at all, see
// examples/preview.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	sftpsync "github.com/hollis-labs/go-sftpsync"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func main() {
	var (
		host       = flag.String("host", "", "host:port to connect to (required)")
		user       = flag.String("user", os.Getenv("USER"), "ssh user")
		keyPath    = flag.String("key", filepath.Join(os.Getenv("HOME"), ".ssh", "id_ed25519"), "private key file")
		knownHosts = flag.String("known-hosts", filepath.Join(os.Getenv("HOME"), ".ssh", "known_hosts"), "known_hosts file")
		local      = flag.String("local", "", "local directory to upload (required)")
		remote     = flag.String("remote", "", "remote directory to upload into (required)")
		download   = flag.Bool("download", false, "copy remote -> local instead of local -> remote")
		dryRun     = flag.Bool("dry-run", false, "report what would happen without writing anything")
		timeout    = flag.Duration("timeout", 10*time.Minute, "overall transfer timeout")
	)
	flag.Parse()

	if *host == "" || *local == "" || *remote == "" {
		flag.Usage()
		os.Exit(2)
	}

	// Cancelling on SIGINT is the reason transfers take a context: a sync over
	// a link that has gone away stops when you say so, mid-file if need be,
	// and leaves the file it was replacing intact.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, cancelTimeout := context.WithTimeout(ctx, *timeout)
	defer cancelTimeout()

	client, err := dial(*host, *user, *keyPath, *knownHosts)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer client.Close() //nolint:errcheck // the connection is ours to close, not the library's

	opts := []sftpsync.Option{sftpsync.WithDryRun(*dryRun)}

	var result *sftpsync.Result
	if *download {
		result, err = sftpsync.DownloadOverSSH(ctx, client, *remote, *local, opts...)
	} else {
		result, err = sftpsync.UploadOverSSH(ctx, client, *local, *remote, opts...)
	}

	// The Result comes back even on failure, so an operator is told how far
	// the transfer got rather than only that it stopped.
	if result != nil {
		fmt.Println(result)
		for _, e := range result.Entries {
			fmt.Printf("  %-8s %s\n", e.Action, e.Path)
		}
	}
	if err != nil {
		log.Fatalf("transfer: %v", err)
	}
}

// dial is the part this library deliberately does not do for you.
func dial(host, user, keyPath, knownHostsPath string) (*ssh.Client, error) {
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("read key %s: %w", keyPath, err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("parse key %s: %w", keyPath, err)
	}

	// Host-key policy belongs to the caller. This example verifies against
	// known_hosts; a control plane might pin a key it manages itself. What it
	// must not be is ssh.InsecureIgnoreHostKey.
	hostKeyCallback, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", knownHostsPath, err)
	}

	return ssh.Dial("tcp", host, &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: hostKeyCallback,
		Timeout:         30 * time.Second,
	})
}
