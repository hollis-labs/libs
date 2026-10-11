//go:build linux

package snapshot

import (
	"context"
	"errors"
	"testing"
)

// These private zero-agent protocol controls are not independent host
// authority producers. They exercise local pin/uncertainty bookkeeping only.
type ownedRetentionHost struct {
	ownedRestoreHost
	missing, stale bool
	completion     SnapshotCompletionRequest
	gc             SnapshotGCRequest
}

func (h *ownedRetentionHost) AcquireSnapshot(context.Context, SnapshotOperation) (SnapshotAdmission, error) {
	return h, nil
}

func (h *ownedRetentionHost) CompletionProof(_ context.Context, r SnapshotCompletionRequest) (SnapshotCompletionProof, error) {
	if h.missing {
		return nil, nil
	}
	h.completion = r
	return h, nil
}
func (h *ownedRetentionHost) VerifySnapshotCompletion(_ context.Context, r SnapshotCompletionRequest) error {
	if h.stale || r != h.completion {
		return ErrAdmissionUnavailable
	}
	return nil
}
func (h *ownedRetentionHost) RecordSnapshotCompletion(ctx context.Context, r SnapshotCompletionRequest, e SnapshotEffect) error {
	if err := h.VerifySnapshotCompletion(ctx, r); err != nil {
		return err
	}
	return h.Record(ctx, r.Operation, e)
}
func (h *ownedRetentionHost) GCProof(_ context.Context, r SnapshotGCRequest) (SnapshotGCProof, error) {
	if h.missing {
		return nil, nil
	}
	h.gc = r
	return h, nil
}
func (h *ownedRetentionHost) VerifySnapshotGC(_ context.Context, r SnapshotGCRequest) error {
	if h.stale || r != h.gc {
		return ErrAdmissionUnavailable
	}
	return nil
}
func (h *ownedRetentionHost) RecordSnapshotGC(ctx context.Context, r SnapshotGCRequest, e SnapshotEffect) error {
	if err := h.VerifySnapshotGC(ctx, r); err != nil {
		return err
	}
	return h.Record(ctx, r.Operation, e)
}

func ownedRetentionFixture(t *testing.T) (*GuardedProvider, GCIntent, *RetainedSet, *ownedRetentionHost) {
	t.Helper()
	p, i, r := ownedGCFixture(t)
	h := &ownedRetentionHost{}
	p.host = h
	p.isolation = &IsolationProof{store: p.admission.root, storeID: "owned-kernel", digest: "owned-kernel", protectedDigest: "owned-kernel", targetDigest: p.plan.Digest(), check: func(context.Context) error { return p.guard.check() }}
	p.admission.isolation, p.admission.host = p.isolation, h
	return p, i, r, h
}

func TestHostRetentionConsumptionRefusesMissingOrStaleProofBeforeAccounting(t *testing.T) {
	for _, kind := range []string{"completion", "gc"} {
		for _, refusal := range []string{"missing", "stale", "ordinary_host"} {
			t.Run(kind+"/"+refusal, func(t *testing.T) {
				p, i, r, host := ownedRetentionFixture(t)
				host.missing, host.stale = refusal == "missing", refusal == "stale"
				if refusal == "ordinary_host" {
					p.host, p.admission.host = &host.ownedRestoreHost, &host.ownedRestoreHost
				}
				var err error
				if kind == "completion" {
					l, e := r.Pin(context.Background(), "op-"+r.ID(), PinJournal)
					if e != nil {
						t.Fatal(e)
					}
					defer l.Close()
					err = l.CompleteFromHost(context.Background(), "owner-completion")
				} else {
					intent := r.intent
					intent.OperationID, intent.InputDigest = i.OperationID, i.InputDigest
					intent.InstanceID, intent.BindingFence, intent.ControllerEpoch = i.InstanceID, i.BindingFence, i.ControllerEpoch
					_, err = p.CollectFromHost(context.Background(), intent, i)
				}
				if !errors.Is(err, ErrAdmissionUnavailable) {
					t.Fatal("missing authority admitted", err)
				}
				s, e := p.admission.load()
				if e != nil || len(s.RetentionOperations) != 0 || len(s.Collections) != 0 || s.Sets[r.ID()].Collected {
					t.Fatal("refusal changed accounting", e)
				}
				for _, pin := range s.Pins {
					if pin.Completion != "" {
						t.Fatal("refusal released pin")
					}
				}
			})
		}
	}
}

func TestHostCompletionKernelUncertaintyKeepsPinAndBlocksAdmission(t *testing.T) {
	for _, failure := range []string{"completion_intent", "owner_completed", "late_authority", "close"} {
		t.Run(failure, func(t *testing.T) {
			p, _, r, host := ownedRetentionFixture(t)
			host.failPhase = failure
			if failure == "late_authority" {
				host.denyAfter = "owner_completed"
			}
			host.failClose = failure == "close"
			l, err := r.Pin(context.Background(), "op-"+r.ID(), PinJournal)
			if err != nil {
				t.Fatal(err)
			}
			if err = l.CompleteFromHost(context.Background(), "owner-completion"); !errors.Is(err, ErrAdmissionUnavailable) {
				t.Fatal(err)
			}
			s, err := p.admission.load()
			if err != nil || s.Pins[l.pin].Completion != "" || !collectionPending(s) {
				t.Fatal("uncertain owner completion released reference", err)
			}
			if _, err = r.Pin(context.Background(), "new-reader", PinExport); err == nil {
				t.Fatal("uncertain completion reopened admission")
			}
			if d := p.admission.retentionDecisionLocked(s, s.Sets[r.ID()].Set.CapturedAt); d.Pinned != 1 || d.Uncertain != 1 || len(d.Eligible) != 0 {
				t.Fatal("uncertain pin did not KEEP", d)
			}
		})
	}
}

func TestHostRetentionKernelSettlesOnlyAfterSuccessfulHostClose(t *testing.T) {
	p, i, r, host := ownedRetentionFixture(t)
	// A no-op GC must preserve the outstanding journal pin; this test performs
	// no object/ref deletion and claims no production deletion authority.
	intent := r.intent
	intent.OperationID, intent.InputDigest = i.OperationID, i.InputDigest
	intent.InstanceID, intent.BindingFence, intent.ControllerEpoch = i.InstanceID, i.BindingFence, i.ControllerEpoch
	out, err := p.CollectFromHost(context.Background(), intent, i)
	if err != nil || !out.Complete || out.RemovedSets != 0 || out.Decision.Pinned != 1 {
		t.Fatal(out, err)
	}
	l, err := r.Pin(context.Background(), "op-"+r.ID(), PinJournal)
	if err != nil {
		t.Fatal(err)
	}
	if err = l.CompleteFromHost(context.Background(), "owner-completion"); err != nil {
		t.Fatal(err)
	}
	s, err := p.admission.load()
	if err != nil || !host.closed || s.Pins[l.pin].Completion != "owner-completion" || collectionPending(s) {
		t.Fatal("actual fixture protocol did not settle", err)
	}
	if err = l.CompleteFromHost(context.Background(), "owner-completion"); !errors.Is(err, ErrAdmissionUnavailable) {
		t.Fatal("closed proof replayed")
	}
}

func TestHostGCKernelAccountingFailureRetainsBarrierAndPins(t *testing.T) {
	for _, failure := range []string{"gc_intent", "gc_accounted", "close"} {
		t.Run(failure, func(t *testing.T) {
			p, i, r, host := ownedRetentionFixture(t)
			host.failPhase, host.failClose = failure, failure == "close"
			intent := r.intent
			intent.OperationID, intent.InputDigest = i.OperationID, i.InputDigest
			intent.InstanceID, intent.BindingFence, intent.ControllerEpoch = i.InstanceID, i.BindingFence, i.ControllerEpoch
			out, err := p.CollectFromHost(context.Background(), intent, i)
			if !errors.Is(err, ErrAdmissionUnavailable) || out.Complete || out.RemovedSets != 0 {
				t.Fatal(out, err)
			}
			s, e := p.admission.load()
			if e != nil || !collectionPending(s) || s.Sets[r.ID()].Collected {
				t.Fatal("GC uncertainty barrier lost", e)
			}
			for _, pin := range s.Pins {
				if pin.Completion != "" {
					t.Fatal("GC released pin")
				}
			}
			if _, err = p.CollectFromHost(context.Background(), intent, i); !errors.Is(err, ErrAdmissionUnavailable) {
				t.Fatal("uncertain operation replayed")
			}
		})
	}
}
