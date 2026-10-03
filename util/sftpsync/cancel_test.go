package sftpsync_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	sftpsync "github.com/hollis-labs/go-sftpsync"
)

// waitFor polls until cond is true, so a test can cancel a transfer at a known
// point rather than after an arbitrary sleep. It reports rather than aborting
// on timeout: it runs on a helper goroutine, where t.Fatal is not allowed, and
// the test's own assertions will fail informatively anyway.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(200 * time.Microsecond)
	}
	t.Errorf("timed out waiting for %s", what)
}

func tempDebris(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.sftpsync-tmp"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	return matches
}

// TestCancelBetweenFilesStopsPromptly would pass trivially if cancellation
// were only checked once at the start of the sync: the context is live when
// Upload is called and only cancelled after the first file has landed. An
// implementation that checks once would go on to transfer all of them and
// return nil.
func TestCancelBetweenFilesStopsPromptly(t *testing.T) {
	client := newSlowTestClient(t, 500*time.Microsecond)

	const fileCount = 40
	spec := tree{}
	for i := range fileCount {
		spec[fmt.Sprintf("f%02d.txt", i)] = entry{content: string(bytes.Repeat([]byte("x"), 64*1024)), mode: 0o644}
	}
	src := filepath.Join(tmpdir(t), "app")
	writeTree(t, src, spec)
	dst := filepath.Join(tmpdir(t), "deployed")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		waitFor(t, "the first file to land", func() bool {
			entries, err := os.ReadDir(dst)
			if err != nil {
				return false
			}
			for _, e := range entries {
				if filepath.Ext(e.Name()) == ".txt" {
					return true
				}
			}
			return false
		})
		cancel()
	}()

	res, err := sftpsync.Upload(ctx, client, src, dst)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if res == nil {
		t.Fatal("no Result returned alongside the cancellation")
	}
	if res.Files >= fileCount {
		t.Errorf("Result.Files = %d of %d: the sync ran to completion after being cancelled", res.Files, fileCount)
	}
	if res.Files == 0 {
		t.Errorf("Result.Files = 0: cancelled before anything was transferred, so this test proves nothing")
	}

	landed, err := os.ReadDir(dst)
	if err != nil {
		t.Fatal(err)
	}
	if len(landed) >= fileCount {
		t.Errorf("%d files at the destination of %d: cancellation did not stop the walk", len(landed), fileCount)
	}
	if debris := tempDebris(t, dst); len(debris) != 0 {
		t.Errorf("temp files left behind: %v", debris)
	}
}

// TestCancelDuringOneFileStopsPromptly cancels while a single file is still
// being written, which io.Copy alone would not notice: it has no context, and
// a sync over a dropped VPN must stop when the caller says so rather than when
// the TCP stack works it out.
func TestCancelDuringOneFileStopsPromptly(t *testing.T) {
	client := newSlowTestClient(t, 2*time.Millisecond)

	src := filepath.Join(tmpdir(t), "app")
	writeTree(t, src, tree{"big.bin": {content: string(bytes.Repeat([]byte("A"), 4<<20)), mode: 0o644}})
	dst := filepath.Join(tmpdir(t), "deployed")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		waitFor(t, "the temp file to start filling", func() bool {
			debris := tempDebris(t, dst)
			if len(debris) == 0 {
				return false
			}
			info, err := os.Stat(debris[0])
			return err == nil && info.Size() > 0
		})
		cancel()
	}()

	started := time.Now()
	_, err := sftpsync.Upload(ctx, client, src, dst)
	elapsed := time.Since(started)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	// 4MB at 2ms per 32KB packet is roughly four minutes if the copy runs to
	// completion; anything close to that means the cancel was not observed.
	if elapsed > 30*time.Second {
		t.Errorf("cancellation took %s: the copy was not interrupted mid-file", elapsed)
	}
	if _, err := os.Stat(filepath.Join(dst, "big.bin")); err == nil {
		t.Error("the destination file exists: a cancelled copy must not be renamed into place")
	}
	if debris := tempDebris(t, dst); len(debris) != 0 {
		t.Errorf("temp files left behind after cancellation: %v", debris)
	}
}

// TestInterruptedTransferLeavesPreviousFileIntact is the whole reason for
// temp-and-rename. A transfer that dies partway must not have replaced a
// working file with half of a new one.
func TestInterruptedTransferLeavesPreviousFileIntact(t *testing.T) {
	client := newSlowTestClient(t, 2*time.Millisecond)

	const previous = "the good copy that is already deployed\n"

	src := filepath.Join(tmpdir(t), "app")
	writeTree(t, src, tree{"app.bin": {content: string(bytes.Repeat([]byte("B"), 4<<20)), mode: 0o755}})

	dst := filepath.Join(tmpdir(t), "deployed")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "app.bin"), []byte(previous), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		waitFor(t, "the replacement to start writing", func() bool {
			debris := tempDebris(t, dst)
			if len(debris) == 0 {
				return false
			}
			info, err := os.Stat(debris[0])
			return err == nil && info.Size() > 0
		})
		cancel()
	}()

	if _, err := sftpsync.Upload(ctx, client, src, dst); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}

	got, err := os.ReadFile(filepath.Join(dst, "app.bin"))
	if err != nil {
		t.Fatalf("the previously deployed file is gone: %v", err)
	}
	if string(got) != previous {
		t.Errorf("the previously deployed file was damaged by an interrupted transfer: %d bytes, want %q", len(got), previous)
	}
	if debris := tempDebris(t, dst); len(debris) != 0 {
		t.Errorf("temp files left behind: %v", debris)
	}
}

// TestCancelledContextBeforeStart is the trivial case, kept so the prompt path
// does not regress into "only checks mid-transfer".
func TestCancelledContextBeforeStart(t *testing.T) {
	client := newTestClient(t)

	src := filepath.Join(tmpdir(t), "app")
	writeTree(t, src, tree{"a.txt": {content: "a", mode: 0o644}})
	dst := filepath.Join(tmpdir(t), "deployed")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := sftpsync.Upload(ctx, client, src, dst); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if _, err := os.Stat(dst); err == nil {
		t.Error("an already-cancelled Upload created the destination root")
	}
}
