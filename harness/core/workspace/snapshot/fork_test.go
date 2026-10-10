//go:build linux || darwin

package snapshot

import (
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"github.com/hollis-labs/substrate/harness/sandbox"
	"os"
	"path/filepath"
	"strings"
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

func TestPinnedCapturedReadRejectsCorruptObjects(t *testing.T) {
	for _, kind := range []string{"commit", "tree", "blob"} {
		t.Run(kind, func(t *testing.T) {
			a, r, _ := capturedReadFixture(t, map[string]string{"code": "captured"})
			l, err := r.Pin(context.Background(), "reader", PinFork)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			root := l.Set().Roots["r1"]
			dir, err := a.shadow.openShadowRepo("r1")
			if err != nil {
				t.Fatal(err)
			}
			oid := root.CommitHash
			if kind == "tree" {
				oid = root.TreeHash
			}
			if kind == "blob" {
				oid = gitCapturedHash("blob", []byte("captured"), 40)
			}
			raw, err := boundedSnapshotGit(context.Background(), a.shadow, dir, 1<<20, "cat-file", kind, oid)
			if err != nil {
				t.Fatal(err)
			}
			// Replace only a disposable fixture object at its old object name.
			// Git cat-file decodes it; the reader must independently verify identity.
			raw = append(raw, 'x')
			var compressed bytes.Buffer
			w := zlib.NewWriter(&compressed)
			if _, err = w.Write(append([]byte(kind+" "+fmt.Sprint(len(raw))+"\x00"), raw...)); err != nil {
				t.Fatal(err)
			}
			if err = w.Close(); err != nil {
				t.Fatal(err)
			}
			objectPath := filepath.Join(dir, "objects", oid[:2], oid[2:])
			if err = os.Chmod(objectPath, 0600); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(objectPath, compressed.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = l.ReadFiles(context.Background(), "r1"); !errors.Is(err, ErrAdmissionUnavailable) {
				t.Fatalf("corrupt %s admitted: %v", kind, err)
			}
		})
	}
}

func TestCapturedReferenceValidation(t *testing.T) {
	for _, ref := range []string{"refs/heads/main", "refs/snapshots/../escape", "refs/snapshots/a.lock", "refs/snapshots/a@{b", "refs/snapshots/a\n", "refs/snapshots/a//b"} {
		if validRetainedReference(ref) {
			t.Fatalf("unsafe ref admitted: %q", ref)
		}
	}
	if !validRetainedReference("refs/snapshots/20261010T010000.123456789Z-abcdef") {
		t.Fatal("owned ref refused")
	}
	a := testAdmission(t)
	m := testManifest(a, "capture")
	m.Roots[0].References = []string{"refs/snapshots/owned"}
	d := detachedManifest(m)
	d.Roots[0].References[0] = "refs/snapshots/foreign"
	if strings.Contains(m.Roots[0].References[0], "foreign") {
		t.Fatal("receipt refs alias caller")
	}
	m.Roots[0].References = append(m.Roots[0].References, m.Roots[0].References[0])
	if err := m.validate(testSet("capture"), a.storeID); err == nil {
		t.Fatal("duplicate owned ref admitted")
	}
}

func TestPinnedCapturedReadComposesGuardedCapture(t *testing.T) {
	config := ownedGuardedConfig(t)
	root := config.Targets.roots[0].Binding.Root
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("fake-excluded-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := newOwnedGuardedProvider(config)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	intent := testCaptureIntent("fork-guarded")
	intent.TargetMapDigest = config.Targets.Digest()
	capture, err := p.CaptureBound(context.Background(), intent)
	if err != nil {
		t.Fatal(err)
	}
	r, err := capture.Retained()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "source.txt"), []byte("later source edit"), 0600); err != nil {
		t.Fatal(err)
	}
	l, err := r.Pin(context.Background(), "new-root-fork", PinFork)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	files, err := l.ReadFiles(context.Background(), "root")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "source.txt" || string(files[0].Bytes) != "eligible owned content\n" {
		t.Fatal("guarded fork read lost captured bytes or included excluded secret")
	}
	if err = l.ValidateForkDestination(context.Background(), root); err == nil {
		t.Fatal("source accepted as new root")
	}
	if err = l.ValidateForkDestination(context.Background(), filepath.Join(t.TempDir(), "new-root")); err != nil {
		t.Fatal(err)
	}
}
