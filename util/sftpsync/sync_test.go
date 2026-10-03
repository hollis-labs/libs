package sftpsync_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	sftpsync "github.com/hollis-labs/go-sftpsync"
)

// acceptanceTree is the shape the README promises round-trips: nested
// subdirectories, an executable script, a file with restrictive bits, an
// empty directory and a symlink that stays inside the root.
var acceptanceTree = tree{
	"run.sh":                  {content: "#!/bin/sh\necho hello\n", mode: 0o755},
	"config/settings.yaml":    {content: "listen: 127.0.0.1:8080\n", mode: 0o644},
	"config/.env":             {content: "TOKEN=shh\n", mode: 0o600},
	"config/current.yaml":     {symlinkTo: "settings.yaml"},
	"data/nested/deep/id.txt": {content: "deep\n", mode: 0o444},
	"empty":                   {dir: true, mode: 0o750},
	"readonly":                {dir: true, mode: 0o500},
}

// snap is one entry of a tree snapshot: enough to prove two trees are the same.
type snap struct {
	mode    fs.FileMode
	content string
	link    string
	isDir   bool
}

// snapshot walks root with lstat semantics and records every entry relative to
// it, so two trees can be compared as values.
func snapshot(t *testing.T, root string) map[string]snap {
	t.Helper()

	out := map[string]snap{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		info, err := d.Info() // lstat semantics: a symlink stays a symlink
		if err != nil {
			return err
		}
		s := snap{mode: info.Mode().Perm(), isDir: info.IsDir()}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			s.link = target
			s.mode = 0 // symlink bits are not portable and are not preserved
		case info.Mode().IsRegular():
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			s.content = string(b)
		}
		out[rel] = s
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return out
}

// readableCopy relaxes directory modes so WalkDir can descend. A 0500
// directory is readable, so this is only needed for the 0300-style cases; it
// is here so a future test that adds one does not mysteriously fail.
func mustEqualTrees(t *testing.T, want, got map[string]snap, wantRoot, gotRoot string) {
	t.Helper()

	for rel, w := range want {
		g, ok := got[rel]
		if !ok {
			t.Errorf("%s missing from %s", rel, gotRoot)
			continue
		}
		if w != g {
			t.Errorf("%s differs:\n  %s: %+v\n  %s: %+v", rel, wantRoot, w, gotRoot, g)
		}
	}
	for rel := range got {
		if _, ok := want[rel]; !ok {
			t.Errorf("%s present in %s but not in %s", rel, gotRoot, wantRoot)
		}
	}
}

func TestRoundTripPreservesTree(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	src := filepath.Join(tmpdir(t), "app")
	writeTree(t, src, acceptanceTree)

	remote := filepath.Join(tmpdir(t), "deployed")
	back := filepath.Join(tmpdir(t), "roundtrip")

	up, err := sftpsync.Upload(ctx, client, src, remote)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	down, err := sftpsync.Download(ctx, client, remote, back)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}

	mustEqualTrees(t, snapshot(t, src), snapshot(t, back), "source", "round-trip")

	// The two directions must agree on what the tree contains; if they do not,
	// one of them is walking something the other is not.
	if up.Files != down.Files || up.Dirs != down.Dirs || up.Symlinks != down.Symlinks {
		t.Errorf("upload and download disagree on the tree:\n  up:   %s\n  down: %s", up, down)
	}
	if up.Bytes != down.Bytes {
		t.Errorf("byte totals differ: upload %d, download %d", up.Bytes, down.Bytes)
	}
}

func TestUploadPreservesExecutableBit(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	src := filepath.Join(tmpdir(t), "app")
	writeTree(t, src, tree{"run.sh": {content: "#!/bin/sh\n", mode: 0o755}})
	dst := filepath.Join(tmpdir(t), "deployed")

	if _, err := sftpsync.Upload(ctx, client, src, dst); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	info, err := os.Stat(filepath.Join(dst, "run.sh"))
	if err != nil {
		t.Fatalf("stat uploaded script: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Errorf("uploaded script mode: got %v, want 0755 -- a script that arrives non-executable is the bug this option exists for", got)
	}
}

func TestPreserveModeOffAppliesConfiguredModes(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	src := filepath.Join(tmpdir(t), "app")
	writeTree(t, src, tree{
		"run.sh":       {content: "#!/bin/sh\n", mode: 0o755},
		"sub/kept.txt": {content: "x", mode: 0o600},
	})
	dst := filepath.Join(tmpdir(t), "deployed")

	if _, err := sftpsync.Upload(ctx, client, src, dst,
		sftpsync.WithPreserveMode(false),
		sftpsync.WithFileMode(0o640),
		sftpsync.WithDirMode(0o700),
	); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	for _, tc := range []struct {
		rel  string
		want fs.FileMode
	}{
		{"run.sh", 0o640},
		{"sub/kept.txt", 0o640},
		{"sub", 0o700},
	} {
		info, err := os.Stat(filepath.Join(dst, filepath.FromSlash(tc.rel)))
		if err != nil {
			t.Fatalf("stat %s: %v", tc.rel, err)
		}
		if got := info.Mode().Perm(); got != tc.want {
			t.Errorf("%s mode: got %v, want %v", tc.rel, got, tc.want)
		}
	}
}

func TestEmptyDirectoryIsCreated(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	src := filepath.Join(tmpdir(t), "app")
	writeTree(t, src, tree{"empty": {dir: true, mode: 0o750}})
	dst := filepath.Join(tmpdir(t), "deployed")

	if _, err := sftpsync.Upload(ctx, client, src, dst); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	info, err := os.Stat(filepath.Join(dst, "empty"))
	if err != nil {
		t.Fatalf("stat empty dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("empty is not a directory at the destination")
	}
	if got := info.Mode().Perm(); got != 0o750 {
		t.Errorf("empty dir mode: got %v, want 0750", got)
	}
}

// TestReadOnlyDirectoryReceivesItsContents is why directory modes are applied
// on the way out of the walk rather than at creation, as the SCP prior art
// does. A 0500 directory cannot accept the files that belong in it.
func TestReadOnlyDirectoryReceivesItsContents(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	src := filepath.Join(tmpdir(t), "app")
	writeTree(t, src, tree{"locked/inside.txt": {content: "present\n", mode: 0o444}})
	if err := os.Chmod(filepath.Join(src, "locked"), 0o500); err != nil {
		t.Fatalf("chmod source dir: %v", err)
	}
	dst := filepath.Join(tmpdir(t), "deployed")

	if _, err := sftpsync.Upload(ctx, client, src, dst); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(dst, "locked", "inside.txt"))
	if err != nil {
		t.Fatalf("read file inside read-only dir: %v", err)
	}
	if string(b) != "present\n" {
		t.Errorf("content: got %q", b)
	}
	info, err := os.Stat(filepath.Join(dst, "locked"))
	if err != nil {
		t.Fatalf("stat locked dir: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o500 {
		t.Errorf("locked dir mode: got %v, want 0500", got)
	}
}

func TestDryRunReportsTheSameSetItWouldTransfer(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	src := filepath.Join(tmpdir(t), "app")
	writeTree(t, src, acceptanceTree)

	planned, err := sftpsync.Upload(ctx, client, src, filepath.Join(tmpdir(t), "nope"),
		sftpsync.WithDryRun(true))
	if err != nil {
		t.Fatalf("dry-run Upload: %v", err)
	}
	if !planned.DryRun {
		t.Error("Result.DryRun is false after a dry run")
	}

	dst := filepath.Join(tmpdir(t), "deployed")
	actual, err := sftpsync.Upload(ctx, client, src, dst)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	if !reflect.DeepEqual(planned.Entries, actual.Entries) {
		t.Errorf("dry run and transfer disagree:\n  planned: %+v\n  actual:  %+v", planned.Entries, actual.Entries)
	}
	if planned.Files != actual.Files || planned.Dirs != actual.Dirs ||
		planned.Symlinks != actual.Symlinks || planned.Skipped != actual.Skipped ||
		planned.Bytes != actual.Bytes {
		t.Errorf("dry run and transfer counts differ:\n  planned: %s\n  actual:  %s", planned, actual)
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	src := filepath.Join(tmpdir(t), "app")
	writeTree(t, src, acceptanceTree)
	dst := filepath.Join(tmpdir(t), "untouched")

	if _, err := sftpsync.Upload(ctx, client, src, dst, sftpsync.WithDryRun(true)); err != nil {
		t.Fatalf("dry-run Upload: %v", err)
	}

	if _, err := os.Stat(dst); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("dry run created the destination root: stat err = %v", err)
	}
}

func TestNonDirectoryRootIsRefused(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	dir := tmpdir(t)
	file := filepath.Join(dir, "single.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := sftpsync.Upload(ctx, client, file, filepath.Join(tmpdir(t), "dst"))
	if !errors.Is(err, sftpsync.ErrNotDirectory) {
		t.Fatalf("got %v, want ErrNotDirectory", err)
	}
}

// TestErrorNamesSideAndPath guards the whole reason PathError exists.
func TestErrorNamesSideAndPath(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	missing := filepath.Join(tmpdir(t), "does-not-exist")

	_, err := sftpsync.Upload(ctx, client, missing, filepath.Join(tmpdir(t), "dst"))
	if err == nil {
		t.Fatal("expected an error for a missing source root")
	}

	var pe *sftpsync.PathError
	if !errors.As(err, &pe) {
		t.Fatalf("got %T, want *sftpsync.PathError", err)
	}
	if pe.Side != sftpsync.Local {
		t.Errorf("Side: got %q, want %q", pe.Side, sftpsync.Local)
	}
	if pe.Path != missing {
		t.Errorf("Path: got %q, want %q", pe.Path, missing)
	}
	if !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), "local") {
		t.Errorf("error text names neither the side nor the path: %q", err)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error does not unwrap to fs.ErrNotExist: %v", err)
	}
}

func TestExistingDestinationRootKeepsItsMode(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	src := filepath.Join(tmpdir(t), "app")
	writeTree(t, src, tree{"a.txt": {content: "a", mode: 0o644}})
	if err := os.Chmod(src, 0o700); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(tmpdir(t), "deployed")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dst, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := sftpsync.Upload(ctx, client, src, dst); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Errorf("existing destination root mode changed to %v; syncing into a directory must not retighten it", got)
	}
}

func TestPreserveModTime(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	src := filepath.Join(tmpdir(t), "app")
	writeTree(t, src, tree{"a.txt": {content: "a", mode: 0o644}})
	dst := filepath.Join(tmpdir(t), "deployed")

	srcInfo, err := os.Stat(filepath.Join(src, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := sftpsync.Upload(ctx, client, src, dst, sftpsync.WithPreserveModTime(true)); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	dstInfo, err := os.Stat(filepath.Join(dst, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	// SFTP carries whole seconds, so compare at that resolution.
	if got, want := dstInfo.ModTime().Unix(), srcInfo.ModTime().Unix(); got != want {
		t.Errorf("mtime: got %d, want %d", got, want)
	}
}

func TestMaxDepthIsAnErrorNotATruncation(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	src := filepath.Join(tmpdir(t), "app")
	writeTree(t, src, tree{"a/b/c/d.txt": {content: "deep", mode: 0o644}})
	dst := filepath.Join(tmpdir(t), "deployed")

	_, err := sftpsync.Upload(ctx, client, src, dst, sftpsync.WithMaxDepth(2))
	if !errors.Is(err, sftpsync.ErrMaxDepthExceeded) {
		t.Fatalf("got %v, want ErrMaxDepthExceeded", err)
	}
}

func TestNilClientIsRejected(t *testing.T) {
	if _, err := sftpsync.Upload(context.Background(), nil, "a", "b"); err == nil {
		t.Error("Upload with a nil client returned no error")
	}
	if _, err := sftpsync.Download(context.Background(), nil, "a", "b"); err == nil {
		t.Error("Download with a nil client returned no error")
	}
}
