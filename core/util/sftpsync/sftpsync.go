package sftpsync

import (
	"context"
	"fmt"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Upload recursively copies localDir to remoteDir over sc, which must be a
// live client the caller opened and continues to own. Nothing here dials,
// authenticates, or reads an SSH config.
//
// remoteDir is created if it does not exist, taking localDir's mode; if it
// already exists its own mode is left alone. Everything below it is created
// as needed.
//
// A [*Result] is returned even when err is non-nil, describing how far the
// transfer got. Errors are [*PathError] and name the side and the path.
func Upload(ctx context.Context, sc *sftp.Client, localDir, remoteDir string, opts ...Option) (*Result, error) {
	if sc == nil {
		return nil, fmt.Errorf("sftpsync: nil *sftp.Client")
	}
	s := newSession(newConfig(opts), localFS{}, newRemoteFS(sc), localDir, remoteDir, DirectionUpload)
	return s.run(ctx)
}

// Download recursively copies remoteDir to localDir over sc, which must be a
// live client the caller opened and continues to own.
//
// It is the same walk as [Upload] with the two ends exchanged — the same
// escape check, the same temp-and-rename writer, the same cancellation. That
// is deliberate: the download path is where "it is only reading" quietly
// becomes "it wrote outside the destination", and one implementation cannot
// drift into two sets of safety properties.
func Download(ctx context.Context, sc *sftp.Client, remoteDir, localDir string, opts ...Option) (*Result, error) {
	if sc == nil {
		return nil, fmt.Errorf("sftpsync: nil *sftp.Client")
	}
	s := newSession(newConfig(opts), newRemoteFS(sc), localFS{}, remoteDir, localDir, DirectionDownload)
	return s.run(ctx)
}

// UploadOverSSH opens an SFTP subsystem on c, runs [Upload] across it and
// closes the subsystem again. c itself is neither opened nor closed by this
// package — it stays exactly as the caller left it, still usable for sessions
// and for further transfers.
//
// Use it when a transfer is a one-off. When several transfers share a
// connection, open one [*sftp.Client] yourself and call [Upload], so they
// share a subsystem too.
func UploadOverSSH(ctx context.Context, c *ssh.Client, localDir, remoteDir string, opts ...Option) (*Result, error) {
	sc, err := openSubsystem(c)
	if err != nil {
		return nil, err
	}
	defer sc.Close() //nolint:errcheck // closing the subsystem cannot change what was transferred
	return Upload(ctx, sc, localDir, remoteDir, opts...)
}

// DownloadOverSSH opens an SFTP subsystem on c, runs [Download] across it and
// closes the subsystem again. c is left open; see [UploadOverSSH].
func DownloadOverSSH(ctx context.Context, c *ssh.Client, remoteDir, localDir string, opts ...Option) (*Result, error) {
	sc, err := openSubsystem(c)
	if err != nil {
		return nil, err
	}
	defer sc.Close() //nolint:errcheck // closing the subsystem cannot change what was transferred
	return Download(ctx, sc, remoteDir, localDir, opts...)
}

func openSubsystem(c *ssh.Client) (*sftp.Client, error) {
	if c == nil {
		return nil, fmt.Errorf("sftpsync: nil *ssh.Client")
	}
	sc, err := sftp.NewClient(c)
	if err != nil {
		return nil, fmt.Errorf("sftpsync: open sftp subsystem: %w", err)
	}
	return sc, nil
}
