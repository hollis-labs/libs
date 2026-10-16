//go:build linux || darwin

package snapshot

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These constructors exist ONLY in package-owned tests with no live agents.
// They are not production completion/deletion/isolation producers.
func ownedGCFixture(t *testing.T) (*GuardedProvider, GCIntent, *RetainedSet) {
	t.Helper()
	config := ownedGuardedConfig(t)
	config.Policy.Retention = RetentionPolicy{MaxAge: time.Nanosecond}
	p, err := newOwnedGuardedProvider(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	intent := testCaptureIntent("gc-source")
	intent.TargetMapDigest = config.Targets.Digest()
	result, err := p.CaptureBound(context.Background(), intent)
	if err != nil {
		t.Fatal(err)
	}
	r, err := result.Retained()
	if err != nil {
		t.Fatal(err)
	}
	gc := GCIntent{OperationID: "gc-owned", InputDigest: strings.Repeat("1", 64), PolicyDigest: p.RetentionDigest(), InstanceID: "fixture", BindingFence: "fixture-fence", ControllerEpoch: 1, Mode: GCCollect}
	return p, gc, r
}
func ownedGCGrant(p *GuardedProvider, i GCIntent) *GCGrant {
	return &GCGrant{provider: p, digest: gcDigest(i), deadline: time.Now().Add(time.Minute)}
}
func completeOwnedJournal(t *testing.T, r *RetainedSet) {
	t.Helper()
	ctx := context.Background()
	l, err := r.Pin(ctx, "op-"+r.ID(), PinJournal)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	// Actual durable zero-agent owner outcome precedes the private completion.
	if err = os.WriteFile(filepath.Join(t.TempDir(), "journal-completion.json"), []byte(`{"terminal_recorded":true,"owner":"owned-fixture"}`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := l.ownerCompletion(ctx, "owned-terminal-recorded")
	if err != nil {
		t.Fatal(err)
	}
	if err = l.Complete(ctx, c); err != nil {
		t.Fatal(err)
	}
}
func TestGuardedGCRequiresIssuedGrantAndCompletePins(t *testing.T) {
	p, i, r := ownedGCFixture(t)
	for _, g := range []*GCGrant{nil, {}} {
		if out, err := p.Collect(context.Background(), i, g); !errors.Is(err, ErrAdmissionUnavailable) || out.Complete {
			t.Fatal("data minted GC authority")
		}
	}
	var decoded GCGrant
	b, _ := json.Marshal(ownedGCGrant(p, i))
	json.Unmarshal(b, &decoded)
	if _, err := p.Collect(context.Background(), i, &decoded); !errors.Is(err, ErrAdmissionUnavailable) {
		t.Fatal("decoded grant admitted")
	}
	out, err := p.Collect(context.Background(), i, ownedGCGrant(p, i))
	if err != nil || !out.Complete || out.RemovedSets != 0 || out.Decision.Pinned != 1 {
		t.Fatalf("initial journal pin did not KEEP: %+v %v", out, err)
	}
	l, err := r.Pin(context.Background(), "fork", PinFork)
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
	completeOwnedJournal(t, r)
	i.OperationID = "gc-after-journal"
	out, err = p.Collect(context.Background(), i, ownedGCGrant(p, i))
	if err != nil || out.RemovedSets != 0 || out.Decision.Pinned != 1 {
		t.Fatal("fork close released pin")
	}
}
func TestGuardedGCCASObjectsAndDurableBudgetHistory(t *testing.T) {
	p, i, r := ownedGCFixture(t)
	completeOwnedJournal(t, r)
	before, err := p.admission.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Sets[r.ID()].Manifest.Roots[0].References) != 1 {
		t.Fatal("capture did not bind its exact created ref")
	}
	dir, _ := p.git.openShadowRepo("root")
	commit := before.Sets[r.ID()].Set.Roots["root"].CommitHash
	out, err := p.Collect(context.Background(), i, ownedGCGrant(p, i))
	if err != nil || !out.Complete || out.Partial || out.RemovedSets != 1 {
		t.Fatalf("private GC failed: %+v %v", out, err)
	}
	if _, err = boundedSnapshotGit(context.Background(), p.git, dir, 1024, "cat-file", "-e", commit); err == nil {
		t.Fatal("collected commit still reachable from private objects")
	}
	state, err := p.admission.load()
	if err != nil {
		t.Fatal(err)
	}
	if !state.Sets[r.ID()].Collected || state.StorageBytes != 0 || state.Runs["run"] != before.Runs["run"] || state.Collections[i.OperationID].Phase != "complete" {
		t.Fatal("GC renewed budget or lost tombstone")
	}
	if _, err = r.Pin(context.Background(), "stale", PinFork); !errors.Is(err, ErrAdmissionUnavailable) {
		t.Fatal("collected receipt acquired pin")
	}
	again, err := p.Collect(context.Background(), i, ownedGCGrant(p, i))
	if err != nil || again.RemovedSets != 1 {
		t.Fatal("recorded operation not idempotent")
	}
	reloaded, err := newAdmission(p.admission.root, &p.admission.policy)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.directory.Close()
	reloaded.policy.Budgets.MaxRunCaptures = before.Runs["run"].Captures
	next := testCaptureIntent("after-gc")
	next.TargetMapDigest = p.plan.Digest()
	if _, err = reloaded.beginCapture(context.Background(), next); !errors.Is(err, ErrSnapshotBudget) {
		t.Fatalf("GC reset run budget: %v", err)
	}
}
func TestGuardedGCUnknownRefsAndObjectsRetained(t *testing.T) {
	for _, kind := range []string{"foreign_ref", "foreign_object", "missing_owned_ref", "expired_grant", "changed_policy"} {
		t.Run(kind, func(t *testing.T) {
			p, i, r := ownedGCFixture(t)
			completeOwnedJournal(t, r)
			dir, _ := p.git.openShadowRepo("root")
			state, _ := p.admission.load()
			root := state.Sets[r.ID()].Manifest.Roots[0]
			grant := ownedGCGrant(p, i)
			switch kind {
			case "foreign_ref":
				_, err := boundedSnapshotGit(context.Background(), p.git, dir, 1024, "update-ref", "refs/heads/foreign", root.CommitHash)
				if err != nil {
					t.Fatal(err)
				}
			case "foreign_object":
				cmd := exec.Command(p.git.gitBin, "--git-dir="+dir, "hash-object", "-w", "--stdin")
				cmd.Stdin = strings.NewReader("unowned fixture object")
				cmd.Env = isolatedEnv(nil)
				if err := cmd.Run(); err != nil {
					t.Fatal(err)
				}
			case "missing_owned_ref":
				if _, err := boundedSnapshotGit(context.Background(), p.git, dir, 1024, "update-ref", "-d", root.References[0], root.CommitHash); err != nil {
					t.Fatal(err)
				}
			case "expired_grant":
				grant.deadline = time.Now().Add(-time.Second)
			case "changed_policy":
				p.admission.policy.Retention.MaxAge = time.Hour
			}
			if out, err := p.Collect(context.Background(), i, grant); err == nil || out.RemovedSets != 0 || out.Complete {
				t.Fatalf("unknown ownership/authority admitted: %+v %v", out, err)
			}
			after, _ := p.admission.load()
			if after.Sets[r.ID()].Collected || len(after.Collections) != 0 {
				t.Fatal("failed preflight recorded GC effects")
			}
		})
	}
}
func TestGuardedGCInterruptedDeletionRetainsJournal(t *testing.T) {
	p, i, r := ownedGCFixture(t)
	completeOwnedJournal(t, r)
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(t.TempDir(), "git-fixture")
	script := "#!/bin/sh\nfor arg in \"$@\"; do if [ \"$arg\" = \"-d\" ]; then exit 42; fi; done\nexec '" + strings.ReplaceAll(gitBin, "'", "'\\''") + "' \"$@\"\n"
	if err = os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	p.git.gitBin = wrapper
	out, err := p.Collect(context.Background(), i, ownedGCGrant(p, i))
	if err == nil || !out.Partial || out.Complete {
		t.Fatal("interrupted delete became complete")
	}
	state, err := p.admission.load()
	if err != nil || state.Collections[i.OperationID].Phase != "prepared" || state.Sets[r.ID()].Collected {
		t.Fatal("uncertain intent/tombstone lost")
	}
	if _, err = r.Pin(context.Background(), "new-reader", PinFork); !errors.Is(err, ErrAdmissionUnavailable) {
		t.Fatal("reader entered uncertain GC")
	}
	next := testCaptureIntent("new-capture")
	next.TargetMapDigest = p.plan.Digest()
	if _, err = p.CaptureBound(context.Background(), next); !errors.Is(err, ErrAdmissionUnavailable) {
		t.Fatal("capture crossed uncertain GC")
	}
}
func TestGuardedGCRootOverridesKeepWholeSet(t *testing.T) {
	p, i, r := ownedGCFixture(t)
	completeOwnedJournal(t, r)
	p.admission.policy.Retention.Roots = map[string]RootRetention{"root": {}}
	i.PolicyDigest = p.RetentionDigest()
	out, err := p.Collect(context.Background(), i, ownedGCGrant(p, i))
	if err != nil || out.RemovedSets != 0 || len(out.Decision.Keep) != 1 {
		t.Fatal("explicit root KEEP lost")
	}
	i.Mode = GCPurge
	i.OperationID = "gc-purge"
	if _, err = p.Collect(context.Background(), i, ownedGCGrant(p, i)); !errors.Is(err, ErrSnapshotPinned) {
		t.Fatal("purge bypassed root KEEP")
	}
}

func TestGuardedGCLeavesPinnedCaptureAndSharedObjectsReadable(t *testing.T) {
	p, i, old := ownedGCFixture(t)
	completeOwnedJournal(t, old)
	root := p.plan.roots[0].Binding.Root
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("newer owned content"), 0600); err != nil {
		t.Fatal(err)
	}
	next := testCaptureIntent("newer-pinned")
	next.TargetMapDigest = p.plan.Digest()
	result, err := p.CaptureBound(context.Background(), next)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := result.Retained()
	if err != nil {
		t.Fatal(err)
	}
	before, err := p.admission.load()
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.Collect(context.Background(), i, ownedGCGrant(p, i))
	if err != nil || !out.Complete || out.RemovedSets != 1 || out.Decision.Pinned != 1 {
		t.Fatalf("selected collection failed: %+v %v", out, err)
	}
	lease, err := retained.Pin(context.Background(), "reader-after-gc", PinExport)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	files, err := lease.ReadFiles(context.Background(), "root")
	if err != nil {
		t.Fatal(err)
	}
	contents := map[string]string{}
	for _, file := range files {
		contents[file.Path] = string(file.Bytes)
	}
	if contents["source.txt"] != "eligible owned content\n" || contents["new.txt"] != "newer owned content" {
		t.Fatal("pinned capture lost shared or new content")
	}
	state, err := p.admission.load()
	if err != nil || state.Sets[retained.ID()].Collected || state.Runs["run"] != before.Runs["run"] {
		t.Fatal("collection changed pinned receipt/run budget")
	}
}

func TestGuardedGCRetentionOverrideIsDetachedAndValidated(t *testing.T) {
	config := ownedGuardedConfig(t)
	config.Policy.Retention = RetentionPolicy{MaxAge: time.Nanosecond, Roots: map[string]RootRetention{"root": {}}}
	p, err := newOwnedGuardedProvider(config)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	config.Policy.Retention.Roots["root"] = RootRetention{MaxAge: time.Nanosecond}
	if p.admission.policy.Retention.Roots["root"].MaxAge != 0 {
		t.Fatal("caller changed admitted retention policy")
	}
	config = ownedGuardedConfig(t)
	config.Policy.Retention.Roots = map[string]RootRetention{"root": {MaxSnapshotSets: -1}}
	if _, err = newOwnedGuardedProvider(config); !errors.Is(err, ErrCaptureDisabled) {
		t.Fatal("invalid retention override admitted")
	}
	entries, err := os.ReadDir(config.StorePath)
	if err != nil || len(entries) != 0 {
		t.Fatal("invalid policy affected store")
	}
}

func TestGuardedGCRetentionCountsEachRootAndKeepsWholeSets(t *testing.T) {
	now := time.Now()
	a := Admission{policy: CapturePolicy{Retention: RetentionPolicy{MaxSnapshotSets: 1, Roots: map[string]RootRetention{"a": {MaxSnapshotSets: 1}, "b": {MaxSnapshotSets: 1}}}}}
	state := admissionLedger{Sets: map[string]setAccount{
		"new-a":   {Set: SnapshotSet{CapturedAt: now, Roots: map[string]RootSnapshot{"a": {}}}},
		"old-ab":  {Set: SnapshotSet{CapturedAt: now.Add(-time.Second), Roots: map[string]RootSnapshot{"a": {}, "b": {}}}},
		"older-b": {Set: SnapshotSet{CapturedAt: now.Add(-2 * time.Second), Roots: map[string]RootSnapshot{"b": {}}}},
	}}
	d := a.retentionDecisionLocked(state, now)
	if len(d.Keep) != 2 || len(d.Eligible) != 1 || d.Eligible[0] != "older-b" {
		t.Fatalf("global rank replaced per-root rank/split multi-root set: %+v", d)
	}
	a.policy.Retention.Roots["b"] = RootRetention{}
	d = a.retentionDecisionLocked(state, now)
	if len(d.Eligible) != 0 {
		t.Fatal("explicit root KEEP lost")
	}
}

func TestGuardedGCCollectedAccountingRequiresCompleteJournal(t *testing.T) {
	p, _, r := ownedGCFixture(t)
	state, err := p.admission.load()
	if err != nil {
		t.Fatal(err)
	}
	account := state.Sets[r.ID()]
	account.Collected = true
	state.Sets[r.ID()] = account
	state.StorageBytes = 0
	for key, pin := range state.Pins {
		pin.Completion = "decoded-outcome"
		state.Pins[key] = pin
	}
	if p.admission.validateLedger(state) == nil {
		t.Fatal("unrecorded collected accounting admitted")
	}
}

func TestGuardedGCWaitsForActualReadLeaseAndRefusesPendingCapture(t *testing.T) {
	p, i, r := ownedGCFixture(t)
	l, err := r.Pin(context.Background(), "held-reader", PinExport)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if out, err := p.Collect(ctx, i, ownedGCGrant(p, i)); !errors.Is(err, context.DeadlineExceeded) || out.Partial || out.Complete {
		t.Fatal("GC bypassed held read lease")
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	intent := testCaptureIntent("unfinished")
	intent.TargetMapDigest = p.plan.Digest()
	capture, err := p.admission.beginCapture(context.Background(), intent)
	if err != nil {
		t.Fatal(err)
	}
	if err = capture.retainUncertain(); err != nil {
		t.Fatal(err)
	}
	if out, err := p.Collect(context.Background(), i, ownedGCGrant(p, i)); !errors.Is(err, ErrAdmissionUnavailable) || out.Partial || out.Complete {
		t.Fatal("pending capture accounted as completed no-op")
	}
}

func TestGuardedGCChangedReferenceFailsExactCAS(t *testing.T) {
	p, i, r := ownedGCFixture(t)
	completeOwnedJournal(t, r)
	state, err := p.admission.load()
	if err != nil {
		t.Fatal(err)
	}
	root := state.Sets[r.ID()].Manifest.Roots[0]
	dir, err := p.git.openShadowRepo("root")
	if err != nil {
		t.Fatal(err)
	}
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	// Change the target after preflight, immediately before the real delete.
	// The exact old-object CAS must refuse, leaving an uncertain journal.
	wrapper := filepath.Join(t.TempDir(), "git-cas-fixture")
	script := "#!/bin/sh\nfor arg in \"$@\"; do if [ \"$arg\" = \"-d\" ]; then " + quote(gitBin) + " --git-dir=" + quote(dir) + " -c core.hooksPath=/dev/null update-ref " + quote(root.References[0]) + " " + quote(root.TreeHash) + " || exit 43; fi; done\nexec " + quote(gitBin) + " \"$@\"\n"
	if err = os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	p.git.gitBin = wrapper
	out, err := p.Collect(context.Background(), i, ownedGCGrant(p, i))
	if err == nil || !out.Partial || out.Complete || out.RemovedSets != 0 {
		t.Fatal("changed reference bypassed exact old-object CAS")
	}
	p.git.gitBin = gitBin
	refs, err := snapshotRefs(context.Background(), p.git, dir, 1024)
	if err != nil || refs[root.References[0]] != root.TreeHash {
		t.Fatal("failed CAS deleted or rewrote changed reference")
	}
	state, err = p.admission.load()
	if err != nil || state.Collections[i.OperationID].Phase != "prepared" || state.Sets[r.ID()].Collected {
		t.Fatal("failed CAS lost uncertainty or collected accounting")
	}
}
