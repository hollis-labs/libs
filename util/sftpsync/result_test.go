package sftpsync_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sftpsync "github.com/hollis-labs/go-sftpsync"
)

// TestResultStringIsOperatorReadable pins the one-liner, because it is what
// callers put in front of a human and a change to it is a change to their logs.
func TestResultStringIsOperatorReadable(t *testing.T) {
	r := &sftpsync.Result{
		Direction:   sftpsync.DirectionUpload,
		Source:      "/home/me/build",
		Destination: "/srv/app",
		Files:       12,
		Dirs:        4,
		Symlinks:    1,
		Skipped:     2,
		Bytes:       84213,
		Duration:    31 * time.Millisecond,
	}
	got := r.String()
	for _, want := range []string{"upload", "/home/me/build", "/srv/app", "12 files", "4 dirs", "1 symlinks", "2 skipped", "84213 bytes", "31ms"} {
		if !strings.Contains(got, want) {
			t.Errorf("Result.String() = %q, missing %q", got, want)
		}
	}

	r.DryRun = true
	if !strings.Contains(r.String(), "dry run") {
		t.Errorf("a dry run must say so: %q", r.String())
	}

	// Zero counts stay out of the way rather than padding the line.
	quiet := (&sftpsync.Result{Direction: sftpsync.DirectionDownload, Source: "a", Destination: "b"}).String()
	if strings.Contains(quiet, "symlinks") || strings.Contains(quiet, "skipped") {
		t.Errorf("empty categories should be omitted: %q", quiet)
	}

	var nilResult *sftpsync.Result
	if nilResult.String() == "" {
		t.Error("a nil Result should still print something rather than panicking")
	}
}

func TestSymlinkPolicyString(t *testing.T) {
	for policy, want := range map[sftpsync.SymlinkPolicy]string{
		sftpsync.SymlinkReplicate:                       "replicate",
		sftpsync.SymlinkSkip:                            "skip",
		sftpsync.SymlinkDereferenceIncludingOutsideRoot: "dereference-including-outside-root",
		sftpsync.SymlinkPolicy(99):                      "unknown",
	} {
		if got := policy.String(); got != want {
			t.Errorf("SymlinkPolicy(%d).String() = %q, want %q", policy, got, want)
		}
	}
}

// TestDownloadPreservesModTime covers the local side of Chtimes, which the
// upload direction never exercises.
func TestDownloadPreservesModTime(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	remote := filepath.Join(tmpdir(t), "served")
	writeTree(t, remote, tree{"sub/a.txt": {content: "a", mode: 0o644}})

	// Backdate it so a fresh mtime at the destination would be obvious.
	past := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	for _, rel := range []string{"sub/a.txt", "sub"} {
		if err := os.Chtimes(filepath.Join(remote, filepath.FromSlash(rel)), past, past); err != nil {
			t.Fatal(err)
		}
	}

	local := filepath.Join(tmpdir(t), "fetched")
	if _, err := sftpsync.Download(ctx, client, remote, local, sftpsync.WithPreserveModTime(true)); err != nil {
		t.Fatalf("Download: %v", err)
	}

	for _, rel := range []string{"sub/a.txt", "sub"} {
		info, err := os.Stat(filepath.Join(local, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("stat %s: %v", rel, err)
		}
		if got := info.ModTime().Unix(); got != past.Unix() {
			t.Errorf("%s mtime: got %d, want %d", rel, got, past.Unix())
		}
	}
}

// TestResultIsReturnedOnFailure is the contract that lets a caller tell an
// operator the difference between "nothing moved" and "half the tree moved".
func TestResultIsReturnedOnFailure(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	src := filepath.Join(tmpdir(t), "app")
	writeTree(t, src, tree{
		"a.txt": {content: "first\n", mode: 0o644},
		"b.txt": {content: "second\n", mode: 0o644},
		// Sorts after the two files, so they transfer before the refusal.
		"zz-leak": {symlinkTo: "/etc/shadow"},
	})
	dst := filepath.Join(tmpdir(t), "deployed")

	res, err := sftpsync.Upload(ctx, client, src, dst)
	if err == nil {
		t.Fatal("expected the escaping symlink to be refused")
	}
	if res == nil {
		t.Fatal("no Result alongside the error")
	}
	if res.Files != 2 {
		t.Errorf("Result.Files = %d, want 2: the transfer got that far before stopping", res.Files)
	}
	if res.Duration == 0 {
		t.Error("Result.Duration is zero on the failure path")
	}
}
