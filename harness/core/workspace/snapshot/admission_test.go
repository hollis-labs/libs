package snapshot

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testCapturePolicy() *CapturePolicy {
	return &CapturePolicy{Revision: "fixture-v1", Budgets: CaptureBudgets{MaxCaptureBytes: 100, MaxRootBytes: 100, MaxStorageBytes: 200, MaxRunBytes: 200, MaxCaptureEntries: 20, MaxRoots: 3, MaxRunCaptures: 2, MaxDuration: time.Minute}}
}
func testAdmission(t *testing.T) *Admission {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "store")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	a, err := newAdmission(dir, testCapturePolicy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.directory.Close() })
	return a
}
func testCaptureIntent(id string) CaptureIntent {
	return CaptureIntent{SetID: id, OperationID: "op-" + id, RunID: "run", InputDigest: strings.Repeat("1", 64), TargetMapDigest: strings.Repeat("2", 64), InstanceID: "instance", PolicyRevision: "fixture-v1", BindingFence: "fence", ControllerEpoch: 1, BootGeneration: "boot", RuntimeGeneration: "runtime"}
}
func testSet(id string) SnapshotSet {
	return SnapshotSet{ID: id, CapturedAt: time.Now(), Roots: map[string]RootSnapshot{"r1": {TargetID: "r1", Root: "/fixture/source", TreeHash: strings.Repeat("a", 40), CommitHash: strings.Repeat("b", 40)}}}
}
func testReceipt(t *testing.T, a *Admission, id string) *RetainedSet {
	t.Helper()
	l, err := a.beginCapture(context.Background(), testCaptureIntent(id))
	if err != nil {
		t.Fatal(err)
	}
	r, err := l.finish(context.Background(), testSet(id), 50)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAdmissionFinitePolicyBeforeStoreEffects(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cases := []*CapturePolicy{nil, {}, testCapturePolicy()}
	cases[2].Budgets.MaxRunBytes = 0
	for _, p := range cases {
		if _, err := newAdmission(dir, p); !errors.Is(err, ErrCaptureDisabled) {
			t.Fatalf("missing finite budget admitted: %v", err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("disabled admission wrote store")
	}
}
func TestAdmissionStableLockSerializesAcrossHandles(t *testing.T) {
	a := testAdmission(t)
	b, err := newAdmission(a.root, testCapturePolicy())
	if err != nil {
		t.Fatal(err)
	}
	defer b.directory.Close()
	l, err := a.beginCapture(context.Background(), testCaptureIntent("first"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err = b.beginCapture(ctx, testCaptureIntent("second")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("capture crossed held lock: %v", err)
	}
	if err = l.retainUncertain(); err != nil {
		t.Fatal(err)
	}
	second, err := b.beginCapture(context.Background(), testCaptureIntent("second"))
	if err != nil {
		t.Fatal(err)
	}
	second.retainUncertain()
	if _, err = b.beginCapture(context.Background(), testCaptureIntent("third")); !errors.Is(err, ErrSnapshotBudget) {
		t.Fatalf("uncertain reservations did not dominate budget: %v", err)
	}
}
func TestAdmissionPinsDominateRetentionAndRestart(t *testing.T) {
	a := testAdmission(t)
	a.policy.Retention = RetentionPolicy{MaxAge: time.Nanosecond, MaxSnapshotSets: 1}
	r := testReceipt(t, a, "one")
	testReceipt(t, a, "two")
	d, err := a.Cleanup(context.Background())
	if err != nil || d.Pinned != 2 || len(d.Eligible) != 0 {
		t.Fatalf("age/count overrode initial pins: %+v %v", d, err)
	}
	b, err := newAdmission(a.root, &a.policy)
	if err != nil {
		t.Fatal(err)
	}
	defer b.directory.Close()
	d, err = b.Cleanup(context.Background())
	if err != nil || d.Pinned != 2 {
		t.Fatalf("restart dropped pins: %+v %v", d, err)
	}
	l, err := r.Pin(context.Background(), "export-owner", PinExport)
	if err != nil {
		t.Fatal(err)
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	if err = a.Purge(context.Background()); !errors.Is(err, ErrSnapshotPinned) {
		t.Fatalf("close/expiry released reference: %v", err)
	}
}
func TestAdmissionDetachedSetAndUnmintableReceipt(t *testing.T) {
	a := testAdmission(t)
	r := testReceipt(t, a, "one")
	l, err := r.Pin(context.Background(), "reader", PinFork)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	copy := l.Set()
	root := copy.Roots["r1"]
	root.TreeHash = strings.Repeat("c", 40)
	copy.Roots["r1"] = root
	if l.Set().Roots["r1"].TreeHash == root.TreeHash {
		t.Fatal("caller mutated earned snapshot")
	}
	if err = l.Complete(context.Background(), &OwnerCompletion{}); !errors.Is(err, ErrAdmissionUnavailable) {
		t.Fatal("decoded completion released pin")
	}
	raw, _ := json.Marshal(r)
	var decoded RetainedSet
	if err = json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, err = decoded.Pin(context.Background(), "foreign", PinFork); !errors.Is(err, ErrAdmissionUnavailable) {
		t.Fatal("JSON minted receipt")
	}
}
func TestAdmissionLostLockCustodyCannotRecordOrCompensate(t *testing.T) {
	a := testAdmission(t)
	l, err := a.beginCapture(context.Background(), testCaptureIntent("one"))
	if err != nil {
		t.Fatal(err)
	}
	old, _ := os.ReadFile(filepath.Join(a.root, "admission.json"))
	if err = os.Rename(filepath.Join(a.root, "admission.lock"), filepath.Join(a.root, "original.lock")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(a.root, "admission.lock"), []byte("foreign"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = l.finish(context.Background(), testSet("one"), 50); !errors.Is(err, ErrAdmissionUnavailable) {
		t.Fatalf("lost inode recorded: %v", err)
	}
	l.retainUncertain()
	now, _ := os.ReadFile(filepath.Join(a.root, "admission.json"))
	if string(now) != string(old) {
		t.Fatal("ledger changed through lost lock custody")
	}
	foreign, _ := os.ReadFile(filepath.Join(a.root, "admission.lock"))
	if string(foreign) != "foreign" {
		t.Fatal("compensated over foreign inode")
	}
}
func TestAdmissionNoExistingStoreAdoption(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	os.Mkdir(dir, 0700)
	os.WriteFile(filepath.Join(dir, "old-objects"), []byte("retained"), 0600)
	if _, err := newAdmission(dir, testCapturePolicy()); !errors.Is(err, ErrAdmissionUnavailable) {
		t.Fatal("unaccounted objects adopted")
	}
	if _, err := os.Stat(filepath.Join(dir, "admission.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("minted ledger over unaccounted store")
	}
}
