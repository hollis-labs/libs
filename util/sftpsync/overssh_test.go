package sftpsync_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	sftpsync "github.com/hollis-labs/go-sftpsync"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// newTestSSHClient stands up a complete SSH server in-process — host key,
// handshake, session channel, sftp subsystem — over a net.Pipe, and returns a
// connected *ssh.Client.
//
// It is more machinery than the other tests need, and it is here for one
// reason: "take a connection, do not make one" is the API decision this whole
// library turns on, and an *OverSSH entry point that closed the caller's
// connection, or leaked a subsystem per call, would be exactly the failure
// that makes it unusable in its first consumer. That has to be asserted
// against a real ssh.Client, not reasoned about.
func newTestSSHClient(t *testing.T) *ssh.Client {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("host key signer: %v", err)
	}

	serverCfg := &ssh.ServerConfig{NoClientAuth: true}
	serverCfg.AddHostKey(signer)

	serverPipe, clientPipe, err := pipePair()
	if err != nil {
		t.Fatalf("pipe pair: %v", err)
	}

	go serveSSH(serverPipe, serverCfg)

	conn, chans, reqs, err := ssh.NewClientConn(clientPipe, "pipe", &ssh.ClientConfig{
		User:            "tester",
		HostKeyCallback: ssh.FixedHostKey(signer.PublicKey()),
	})
	if err != nil {
		t.Fatalf("ssh handshake: %v", err)
	}
	client := ssh.NewClient(conn, chans, reqs)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func serveSSH(rw net.Conn, cfg *ssh.ServerConfig) {
	conn, chans, reqs, err := ssh.NewServerConn(rw, cfg)
	if err != nil {
		return
	}
	defer conn.Close() //nolint:errcheck // test server teardown
	go ssh.DiscardRequests(reqs)

	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "sessions only")
			continue
		}
		channel, requests, err := newChannel.Accept()
		if err != nil {
			return
		}
		go func() {
			for req := range requests {
				serveSFTPSubsystem(channel, req)
			}
		}()
	}
}

func serveSFTPSubsystem(channel ssh.Channel, req *ssh.Request) {
	ok := req.Type == "subsystem" && subsystemName(req.Payload) == "sftp"
	if req.WantReply {
		_ = req.Reply(ok, nil)
	}
	if !ok {
		return
	}
	server, err := sftp.NewServer(channel)
	if err != nil {
		_ = channel.Close()
		return
	}
	_ = server.Serve()
	_ = server.Close()
	_ = channel.Close()
}

// subsystemName decodes the SSH string in a "subsystem" request payload.
func subsystemName(payload []byte) string {
	if len(payload) < 4 {
		return ""
	}
	n := binary.BigEndian.Uint32(payload)
	if uint64(n)+4 > uint64(len(payload)) {
		return ""
	}
	return string(payload[4 : 4+n])
}

func TestUploadOverSSH(t *testing.T) {
	ctx := context.Background()
	client := newTestSSHClient(t)

	src := filepath.Join(tmpdir(t), "app")
	writeTree(t, src, tree{
		"bin/run.sh":   {content: "#!/bin/sh\n", mode: 0o755},
		"config/a.yml": {content: "a: 1\n", mode: 0o644},
	})
	dst := filepath.Join(tmpdir(t), "deployed")

	res, err := sftpsync.UploadOverSSH(ctx, client, src, dst)
	if err != nil {
		t.Fatalf("UploadOverSSH: %v", err)
	}
	if res.Files != 2 {
		t.Errorf("Result.Files = %d, want 2 (%s)", res.Files, res)
	}

	info, err := os.Stat(filepath.Join(dst, "bin", "run.sh"))
	if err != nil {
		t.Fatalf("stat uploaded script: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Errorf("mode: got %v, want 0755", got)
	}
}

// TestOverSSHLeavesTheConnectionOpen is the contract: these helpers open an
// SFTP subsystem and close that subsystem. The *ssh.Client belongs to the
// caller and must survive, still usable for further transfers and for
// sessions of its own.
func TestOverSSHLeavesTheConnectionOpen(t *testing.T) {
	ctx := context.Background()
	client := newTestSSHClient(t)

	src := filepath.Join(tmpdir(t), "app")
	writeTree(t, src, tree{"a.txt": {content: "first\n", mode: 0o644}})

	first := filepath.Join(tmpdir(t), "one")
	if _, err := sftpsync.UploadOverSSH(ctx, client, src, first); err != nil {
		t.Fatalf("first UploadOverSSH: %v", err)
	}

	// If the first call had closed the connection, this would fail rather than
	// simply reopening a subsystem on the same live client.
	second := filepath.Join(tmpdir(t), "two")
	if _, err := sftpsync.UploadOverSSH(ctx, client, src, second); err != nil {
		t.Fatalf("second UploadOverSSH on the same client: %v -- the connection is the caller's, not ours to close", err)
	}

	// And the caller can still do their own thing with it afterwards.
	sc, err := sftp.NewClient(client)
	if err != nil {
		t.Fatalf("caller opening their own subsystem afterwards: %v", err)
	}
	if err := sc.Close(); err != nil {
		t.Errorf("closing the caller's own subsystem: %v", err)
	}
}

func TestDownloadOverSSH(t *testing.T) {
	ctx := context.Background()
	client := newTestSSHClient(t)

	remote := filepath.Join(tmpdir(t), "served")
	writeTree(t, remote, tree{"config/a.yml": {content: "a: 1\n", mode: 0o600}})
	local := filepath.Join(tmpdir(t), "fetched")

	res, err := sftpsync.DownloadOverSSH(ctx, client, remote, local)
	if err != nil {
		t.Fatalf("DownloadOverSSH: %v", err)
	}
	if res.Direction != sftpsync.DirectionDownload {
		t.Errorf("Direction = %q, want %q", res.Direction, sftpsync.DirectionDownload)
	}

	b, err := os.ReadFile(filepath.Join(local, "config", "a.yml"))
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(b) != "a: 1\n" {
		t.Errorf("content: got %q", b)
	}
}

func TestOverSSHRejectsNilClient(t *testing.T) {
	if _, err := sftpsync.UploadOverSSH(context.Background(), nil, "a", "b"); err == nil {
		t.Error("UploadOverSSH with a nil client returned no error")
	}
	if _, err := sftpsync.DownloadOverSSH(context.Background(), nil, "a", "b"); err == nil {
		t.Error("DownloadOverSSH with a nil client returned no error")
	}
}

// pipeConn is a net.Conn backed by a pair of os.Pipes.
//
// net.Pipe cannot be used for the SSH handshake: it is completely unbuffered,
// and the SSH version exchange has both ends write their identification string
// before either reads the other's, so both block in Write and the handshake
// deadlocks. An os.Pipe has a kernel buffer, so the exchange completes. No
// socket is opened and nothing binds a port — these tests still need no
// network.
type pipeConn struct {
	r *os.File
	w *os.File
}

func pipePair() (a, b pipeConn, err error) {
	aRead, bWrite, err := os.Pipe()
	if err != nil {
		return a, b, err
	}
	bRead, aWrite, err := os.Pipe()
	if err != nil {
		_ = aRead.Close()
		_ = bWrite.Close()
		return a, b, err
	}
	return pipeConn{r: aRead, w: aWrite}, pipeConn{r: bRead, w: bWrite}, nil
}

func (c pipeConn) Read(p []byte) (int, error)  { return c.r.Read(p) }
func (c pipeConn) Write(p []byte) (int, error) { return c.w.Write(p) }

func (c pipeConn) Close() error {
	err := c.w.Close()
	if rerr := c.r.Close(); err == nil {
		err = rerr
	}
	return err
}

func (pipeConn) LocalAddr() net.Addr  { return pipeAddr{} }
func (pipeConn) RemoteAddr() net.Addr { return pipeAddr{} }

// Deadlines are accepted and ignored. Nothing in these tests sets one, and the
// transfers they drive are bounded by context instead.
func (pipeConn) SetDeadline(time.Time) error      { return nil }
func (pipeConn) SetReadDeadline(time.Time) error  { return nil }
func (pipeConn) SetWriteDeadline(time.Time) error { return nil }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }
