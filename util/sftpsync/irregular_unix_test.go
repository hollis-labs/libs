//go:build unix

package sftpsync_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	sftpsync "github.com/hollis-labs/go-sftpsync"
)

// TestIrregularFilesAreSkippedNotRecreated covers the default branch of the
// walk. Handing a remote host a device node or a FIFO because it happened to
// be sitting in a config directory is not a transfer anybody asked for, so
// these are reported and left behind rather than copied or errored on.
func TestIrregularFilesAreSkippedNotRecreated(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	src := filepath.Join(tmpdir(t), "app")
	writeTree(t, src, tree{"settings.yaml": {content: "a: 1\n", mode: 0o644}})
	if err := syscall.Mkfifo(filepath.Join(src, "control.sock"), 0o600); err != nil {
		t.Skipf("cannot create a FIFO here: %v", err)
	}
	dst := filepath.Join(tmpdir(t), "deployed")

	res, err := sftpsync.Upload(ctx, client, src, dst)
	if err != nil {
		t.Fatalf("Upload: %v -- an irregular file is a skip, not a failure", err)
	}
	if res.Skipped != 1 {
		t.Fatalf("Result.Skipped = %d, want 1 (%s)", res.Skipped, res)
	}
	if res.Files != 1 {
		t.Errorf("Result.Files = %d, want 1: the ordinary file beside it must still transfer", res.Files)
	}

	var skipped sftpsync.Entry
	for _, e := range res.Entries {
		if e.Action == sftpsync.ActionSkip {
			skipped = e
		}
	}
	if skipped.Path != "control.sock" {
		t.Errorf("skipped entry path = %q, want %q", skipped.Path, "control.sock")
	}
	if !strings.Contains(skipped.Reason, "named pipe") {
		t.Errorf("skip reason = %q, want it to name the kind of file", skipped.Reason)
	}

	if _, err := os.Lstat(filepath.Join(dst, "control.sock")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the FIFO was recreated at the destination: %v", err)
	}
}
