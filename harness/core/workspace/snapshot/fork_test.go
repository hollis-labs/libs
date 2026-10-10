//go:build linux || darwin

package snapshot

import (
	"context"
	"errors"
	"github.com/hollis-labs/substrate/harness/sandbox"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Private zero-agent kernels establish Git/file behavior, not live isolation.
func capturedReadFixture(t *testing.T, files map[string]string) (*Admission, *RetainedSet, string) {
	t.Helper()
	root := t.TempDir()
	store := filepath.Join(t.TempDir(), "store")
	if e := os.Mkdir(store, 0700); e != nil {
		t.Fatal(e)
	}
	for name, value := range files {
		full := filepath.Join(root, name)
		os.MkdirAll(filepath.Dir(full), 0700)
		if e := os.WriteFile(full, []byte(value), 0700); e != nil {
			t.Fatal(e)
		}
	}
	policy := testCapturePolicy()
	policy.Budgets.MaxCaptureBytes = 1 << 20
	policy.Budgets.MaxRootBytes = 1 << 20
	policy.Budgets.MaxStorageBytes = 4 << 20
	policy.Budgets.MaxRunBytes = 4 << 20
	a, e := newAdmission(store, policy)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { a.directory.Close() })
	guard, e := openStoreGuard(store, sandbox.ResolvedAccessPolicy{Mode: sandbox.ConfinementRequired})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { guard.close() })
	sg, e := NewShadowGit(store)
	if e != nil {
		t.Fatal(e)
	}
	if e = a.bindCapturedStore(sg, guard); e != nil {
		t.Fatal(e)
	}
	l, e := a.beginCapture(context.Background(), testCaptureIntent("capture"))
	if e != nil {
		t.Fatal(e)
	}
	set, e := sg.Capture(context.Background(), []Target{{ID: "r1", Root: root}})
	if e != nil || set.Roots["r1"].Err != nil {
		l.retainUncertain()
		t.Fatal("private fixture capture failed", e)
	}
	r := set.Roots["r1"]
	now := time.Now()
	m := testManifest(a, "capture")
	m.Roots[0].TreeHash = r.TreeHash
	m.Roots[0].CommitHash = r.CommitHash
	m.Observation = SnapshotInterval{StartedAt: set.CapturedAt.Add(-time.Second), FinishedAt: now}
	m.Roots[0].Observation = m.Observation
	receipt, e := l.finish(context.Background(), set, 1024, m)
	if e != nil {
		l.retainUncertain()
		t.Fatal(e)
	}
	return a, receipt, root
}
func TestPinnedCapturedTreeReadUsesSnapshotNotWorkingRoot(t *testing.T) {
	a, r, root := capturedReadFixture(t, map[string]string{"nested/code": "captured", "empty": ""})
	if e := os.WriteFile(filepath.Join(root, "nested/code"), []byte("later live edit"), 0700); e != nil {
		t.Fatal(e)
	}
	l, e := r.Pin(context.Background(), "fork-owner", PinFork)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	files, e := l.ReadFiles(context.Background(), "r1")
	if e != nil {
		t.Fatal(e)
	}
	got := map[string]CapturedFile{}
	for _, f := range files {
		got[f.Path] = f
	}
	if string(got["nested/code"].Bytes) != "captured" || got["nested/code"].Mode != 0700 || got["empty"].Bytes == nil || len(got["empty"].Bytes) != 0 {
		t.Fatal("captured modes/content/empty file not preserved")
	}
	live, _ := os.ReadFile(filepath.Join(root, "nested/code"))
	if string(live) != "later live edit" {
		t.Fatal("fork read wrote source")
	}
	if e = l.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e = l.ReadFiles(context.Background(), "r1"); !errors.Is(e, ErrAdmissionUnavailable) {
		t.Fatal("closed lease read")
	}
	if e = a.Purge(context.Background()); !errors.Is(e, ErrSnapshotPinned) {
		t.Fatal("fork references did not dominate purge")
	}
}
func TestPinnedCapturedReadBudgetAndSecretRefusal(t *testing.T) {
	for _, secret := range []bool{false, true} {
		t.Run(map[bool]string{false: "byte_budget", true: "secret"}[secret], func(t *testing.T) {
			name := "source"
			if secret {
				name = ".env.local"
			}
			a, r, _ := capturedReadFixture(t, map[string]string{name: "fixture only"})
			if !secret {
				a.policy.Budgets.MaxRootBytes = 2
			}
			l, e := r.Pin(context.Background(), "reader", PinFork)
			if e != nil {
				t.Fatal(e)
			}
			defer l.Close()
			if _, e = l.ReadFiles(context.Background(), "r1"); e == nil {
				t.Fatal("unsafe/oversized captured source read")
			}
		})
	}
}
func TestCapturedReadPathValidation(t *testing.T) {
	for _, p := range []string{"../escape", "a/../b", "/absolute", ".git/config", "nested/.git/index", "windows\\escape", "nul\x00name"} {
		if safeCapturedPath(p) {
			t.Fatalf("unsafe path accepted %q", p)
		}
	}
	if !safeCapturedPath("a space/normal.txt") {
		t.Fatal("ordinary path refused")
	}
}
