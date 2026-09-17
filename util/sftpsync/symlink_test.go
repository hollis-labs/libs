package sftpsync_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sftpsync "github.com/hollis-labs/go-sftpsync"
)

func TestUploadRefusesSymlinkEscape(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
	}{
		{"absolute target", "/etc/shadow"},
		{"relative parent traversal", "../../etc/shadow"},
		{"traversal disguised by a subdirectory", "sub/../../../etc/shadow"},
		{"bare parent", ".."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			client := newTestClient(t)

			src := filepath.Join(tmpdir(t), "app")
			writeTree(t, src, tree{
				"keep.txt": {content: "ordinary\n", mode: 0o644},
				"leak":     {symlinkTo: tc.target},
			})
			dst := filepath.Join(tmpdir(t), "deployed")

			res, err := sftpsync.Upload(ctx, client, src, dst)
			if !errors.Is(err, sftpsync.ErrSymlinkEscape) {
				t.Fatalf("got %v, want ErrSymlinkEscape", err)
			}

			// The refusal must name the offending link, not just the condition.
			var pe *sftpsync.PathError
			if !errors.As(err, &pe) {
				t.Fatalf("got %T, want *sftpsync.PathError", err)
			}
			if !strings.HasSuffix(pe.Path, "leak") {
				t.Errorf("PathError.Path = %q, want the link", pe.Path)
			}
			if pe.Side != sftpsync.Local {
				t.Errorf("Side = %q, want local", pe.Side)
			}
			if !strings.Contains(err.Error(), tc.target) {
				t.Errorf("error does not name the target %q: %v", tc.target, err)
			}

			// Nothing was read through the link.
			if _, err := os.Lstat(filepath.Join(dst, "leak")); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("the refused link was created at the destination anyway: %v", err)
			}

			// A partial Result is still returned, so a caller can say how far it got.
			if res == nil {
				t.Fatal("no Result returned alongside the error")
			}
		})
	}
}

// TestUploadRefusesAbsoluteTargetInsideSourceRoot covers the case that looks
// safe and is not: the target really does sit inside the source tree, but the
// same absolute path on the far side is a different place.
func TestUploadRefusesAbsoluteTargetInsideSourceRoot(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	src := filepath.Join(tmpdir(t), "app")
	writeTree(t, src, tree{"real.txt": {content: "inside\n", mode: 0o644}})
	if err := os.Symlink(filepath.Join(src, "real.txt"), filepath.Join(src, "alias")); err != nil {
		t.Fatal(err)
	}

	_, err := sftpsync.Upload(ctx, client, src, filepath.Join(tmpdir(t), "deployed"))
	if !errors.Is(err, sftpsync.ErrSymlinkEscape) {
		t.Fatalf("got %v, want ErrSymlinkEscape for an absolute target", err)
	}
}

// TestDownloadRefusesSymlinkEscape is the symmetry guarantee: the direction
// where "it is only reading" would otherwise quietly become "it wrote outside
// the destination".
func TestDownloadRefusesSymlinkEscape(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	remote := filepath.Join(tmpdir(t), "served")
	writeTree(t, remote, tree{
		"keep.txt": {content: "ordinary\n", mode: 0o644},
		"leak":     {symlinkTo: "../../../etc/shadow"},
	})
	local := filepath.Join(tmpdir(t), "fetched")

	_, err := sftpsync.Download(ctx, client, remote, local)
	if !errors.Is(err, sftpsync.ErrSymlinkEscape) {
		t.Fatalf("got %v, want ErrSymlinkEscape", err)
	}

	var pe *sftpsync.PathError
	if !errors.As(err, &pe) {
		t.Fatalf("got %T, want *sftpsync.PathError", err)
	}
	if pe.Side != sftpsync.Remote {
		t.Errorf("Side = %q, want remote -- the link is on the far end", pe.Side)
	}
	if _, err := os.Lstat(filepath.Join(local, "leak")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the refused link was created locally: %v", err)
	}
}

func TestSymlinkInsideRootIsReplicated(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	src := filepath.Join(tmpdir(t), "app")
	writeTree(t, src, tree{
		"config/settings.yaml": {content: "a: 1\n", mode: 0o644},
		// A sibling reference.
		"config/current.yaml": {symlinkTo: "settings.yaml"},
		// A reference down into a subdirectory.
		"top": {symlinkTo: "config/settings.yaml"},
		// A reference that goes up and back down: it uses "..", but resolves
		// inside the root, so it is not an escape.
		"config/sibling": {symlinkTo: "../config/settings.yaml"},
	})

	dst := filepath.Join(tmpdir(t), "deployed")
	res, err := sftpsync.Upload(ctx, client, src, dst)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	for rel, want := range map[string]string{
		"config/current.yaml": "settings.yaml",
		"top":                 "config/settings.yaml",
		"config/sibling":      "../config/settings.yaml",
	} {
		got, err := os.Readlink(filepath.Join(dst, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("readlink %s: %v", rel, err)
			continue
		}
		if got != want {
			t.Errorf("%s target: got %q, want %q", rel, got, want)
		}
	}
	if res.Symlinks != 3 {
		t.Errorf("Result.Symlinks = %d, want 3 (%s)", res.Symlinks, res)
	}
}

func TestSymlinkSkipPolicySkipsEvenEscapingLinks(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	src := filepath.Join(tmpdir(t), "app")
	writeTree(t, src, tree{
		"keep.txt": {content: "ordinary\n", mode: 0o644},
		"leak":     {symlinkTo: "/etc/shadow"},
		"inside":   {symlinkTo: "keep.txt"},
	})
	dst := filepath.Join(tmpdir(t), "deployed")

	res, err := sftpsync.Upload(ctx, client, src, dst, sftpsync.WithSymlinkPolicy(sftpsync.SymlinkSkip))
	if err != nil {
		t.Fatalf("Upload: %v -- nothing is read or written under SymlinkSkip, so an escape is not an error", err)
	}
	if res.Skipped != 2 {
		t.Errorf("Result.Skipped = %d, want 2 (%s)", res.Skipped, res)
	}
	if res.Symlinks != 0 {
		t.Errorf("Result.Symlinks = %d, want 0", res.Symlinks)
	}

	for _, rel := range []string{"leak", "inside"} {
		if _, err := os.Lstat(filepath.Join(dst, rel)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s exists at the destination under SymlinkSkip: %v", rel, err)
		}
	}
	// The skip is reported with a reason, not silently dropped.
	for _, e := range res.Entries {
		if e.Action == sftpsync.ActionSkip && e.Reason == "" {
			t.Errorf("skipped %s with no reason", e.Path)
		}
	}
}

func TestDereferencePolicyFollowsOutsideRoot(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	outside := filepath.Join(tmpdir(t), "elsewhere")
	writeTree(t, outside, tree{"secret.txt": {content: "followed\n", mode: 0o644}})

	src := filepath.Join(tmpdir(t), "app")
	writeTree(t, src, tree{"keep.txt": {content: "ordinary\n", mode: 0o644}})
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(src, "reached")); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(tmpdir(t), "deployed")
	res, err := sftpsync.Upload(ctx, client, src, dst,
		sftpsync.WithSymlinkPolicy(sftpsync.SymlinkDereferenceIncludingOutsideRoot))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(dst, "reached"))
	if err != nil {
		t.Fatalf("read dereferenced file: %v", err)
	}
	if string(b) != "followed\n" {
		t.Errorf("content: got %q, want %q", b, "followed\n")
	}
	// It arrives as a regular file, not as a link.
	info, err := os.Lstat(filepath.Join(dst, "reached"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("dereferenced entry arrived as a symlink")
	}
	if res.Symlinks != 0 {
		t.Errorf("Result.Symlinks = %d, want 0 under the dereference policy", res.Symlinks)
	}
}

// TestDereferenceLoopTerminates: following links can cycle, and the depth
// bound is what stops it.
func TestDereferenceLoopTerminates(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	src := filepath.Join(tmpdir(t), "app")
	writeTree(t, src, tree{"dir/file.txt": {content: "x", mode: 0o644}})
	if err := os.Symlink("..", filepath.Join(src, "dir", "loop")); err != nil {
		t.Fatal(err)
	}

	_, err := sftpsync.Upload(ctx, client, src, filepath.Join(tmpdir(t), "deployed"),
		sftpsync.WithSymlinkPolicy(sftpsync.SymlinkDereferenceIncludingOutsideRoot),
		sftpsync.WithMaxDepth(8))
	if !errors.Is(err, sftpsync.ErrMaxDepthExceeded) {
		t.Fatalf("got %v, want ErrMaxDepthExceeded -- a symlink loop must terminate", err)
	}
}

func TestSymlinkReplicationReplacesAnExistingEntry(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	src := filepath.Join(tmpdir(t), "app")
	writeTree(t, src, tree{
		"settings.yaml": {content: "a: 1\n", mode: 0o644},
		"current":       {symlinkTo: "settings.yaml"},
	})

	dst := filepath.Join(tmpdir(t), "deployed")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	// A stale link pointing somewhere else must be replaced, not doubled.
	if err := os.Symlink("stale.yaml", filepath.Join(dst, "current")); err != nil {
		t.Fatal(err)
	}

	if _, err := sftpsync.Upload(ctx, client, src, dst); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	got, err := os.Readlink(filepath.Join(dst, "current"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "settings.yaml" {
		t.Errorf("target: got %q, want %q", got, "settings.yaml")
	}
}
