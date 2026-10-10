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
	r, err := l.finish(context.Background(), testSet(id), 50, testManifest(a, id))
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
func TestAdmissionConstructorHasFiniteLockWait(t *testing.T) {
	a := testAdmission(t)
	l, err := a.beginCapture(context.Background(), testCaptureIntent("held"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.retainUncertain()
	p := testCapturePolicy()
	p.Budgets.MaxDuration = 15 * time.Millisecond
	if _, err = newAdmission(a.root, p); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unbounded constructor wait: %v", err)
	}
}
func TestAdmissionExpiredReaderRetainsPin(t *testing.T) {
	a := testAdmission(t)
	r := testReceipt(t, a, "one")
	l, err := r.Pin(context.Background(), "reader", PinFork)
	if err != nil {
		t.Fatal(err)
	}
	l.deadline = time.Now().Add(-time.Second)
	if err = l.Verify(context.Background()); !errors.Is(err, ErrSnapshotBudget) {
		t.Fatalf("expired reader admitted: %v", err)
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	if err = a.Purge(context.Background()); !errors.Is(err, ErrSnapshotPinned) {
		t.Fatal("timeout released durable pin")
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
	if _, err = l.finish(context.Background(), testSet("one"), 50, testManifest(a, "one")); !errors.Is(err, ErrAdmissionUnavailable) {
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

func testManifest(a *Admission, id string) RetainedManifest {
	now := time.Now()
	return RetainedManifest{Observation: SnapshotInterval{StartedAt: now.Add(-time.Hour), FinishedAt: now.Add(time.Hour)}, Complete: true, Roots: []RetainedRootOutcome{{RootID: "r1", StoreID: rootStoreID(a.storeID, "r1"), TreeHash: strings.Repeat("a", 40), CommitHash: strings.Repeat("b", 40), Observation: SnapshotInterval{StartedAt: now.Add(-time.Minute), FinishedAt: now.Add(time.Minute)}}}}
}

func TestAdmissionForeignLedgerCannotMintStoreOrigin(t *testing.T) {
	a := testAdmission(t)
	testReceipt(t, a, "one")
	dir := filepath.Join(t.TempDir(), "foreign-store")
	os.Mkdir(dir, 0700)
	data, e := os.ReadFile(filepath.Join(a.root, "admission.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(dir, "admission.json"), data, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = newAdmission(dir, testCapturePolicy()); !errors.Is(e, ErrAdmissionUnavailable) {
		t.Fatalf("foreign durable origin adopted: %v", e)
	}
}
func TestAdmissionPartialManifestPersistedAndDetached(t *testing.T) {
	a := testAdmission(t)
	l, e := a.beginCapture(context.Background(), testCaptureIntent("partial"))
	if e != nil {
		t.Fatal(e)
	}
	m := testManifest(a, "partial")
	m.Complete = false
	m.Roots[0].Skipped = []RetainedSkip{{Reason: "excluded", Count: 2}}
	m.Roots = append(m.Roots, RetainedRootOutcome{RootID: "r2", StoreID: rootStoreID(a.storeID, "r2"), Code: "coverage_unavailable", Observation: m.Roots[0].Observation})
	r, e := l.finish(context.Background(), testSet("partial"), 50, m)
	if e != nil {
		t.Fatal(e)
	}
	m.Roots[1].Code = "foreign"
	m.Roots[0].Skipped[0].Count = 999
	lease, e := r.Pin(context.Background(), "reader", PinExport)
	if e != nil {
		t.Fatal(e)
	}
	saved := lease.Manifest()
	if saved.Roots[1].Code != "coverage_unavailable" || saved.Roots[0].Skipped[0].Count != 2 {
		t.Fatal("caller mutated earned outcomes")
	}
	saved.Roots[1].Code = "mutated"
	if e = lease.Verify(context.Background()); e != nil {
		t.Fatal(e)
	}
	lease.Close()
	reopened, e := newAdmission(a.root, testCapturePolicy())
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.directory.Close()
	h, e := reopened.lock(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	state, e := reopened.load()
	h.close()
	if e != nil || state.Sets["partial"].Manifest.Roots[1].Code != "coverage_unavailable" {
		t.Fatal("partial outcome lost across restart", e)
	}
	d, e := reopened.Cleanup(context.Background())
	if e != nil || d.Uncertain != 1 || len(d.Eligible) != 0 {
		t.Fatalf("partial effects not retained: %+v %v", d, e)
	}
}
func TestAdmissionAllFailedDoesNotIssueRetainedSuccess(t *testing.T) {
	a := testAdmission(t)
	l, e := a.beginCapture(context.Background(), testCaptureIntent("failed"))
	if e != nil {
		t.Fatal(e)
	}
	defer l.retainUncertain()
	s := testSet("failed")
	s.Roots = map[string]RootSnapshot{}
	if r, e := l.finish(context.Background(), s, 0, testManifest(a, "failed")); r != nil || e == nil {
		t.Fatal("all-failed attempt minted retained success")
	}
}
