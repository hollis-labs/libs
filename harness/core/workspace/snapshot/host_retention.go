package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// SnapshotCompletionRequest identifies the exact durable pin whose owner has
// completed. It is descriptive data, not proof of completion or permission to
// remove objects. LedgerStoreID differs from the physical operation StoreID.
type SnapshotCompletionRequest struct {
	Operation                                                  SnapshotOperation
	LedgerStoreID, SetID, SetDigest, PinID, Owner, OperationID string
	Kind                                                       PinKind
}

// SnapshotCompletionProof is implemented privately by the independent host.
// Verify must establish the durable terminal owner outcome and all of its
// obligations, bound to this exact request, from authoritative host stores.
// A decoded completion, callback returning nil, or process death is not a
// supported producer. The library separately checks physical and held custody.
// Record must revalidate mutable host predicates in its original atomic write
// transaction, bind the exact request and phase, and return only after durable
// commit. It must not call back into the database through a second connection
// or hold a transaction over uncontrolled provider/OS work.
type SnapshotCompletionProof interface {
	VerifySnapshotCompletion(context.Context, SnapshotCompletionRequest) error
	RecordSnapshotCompletion(context.Context, SnapshotCompletionRequest, SnapshotEffect) error
}

// SnapshotCompletionAdmission extends an already-held read admission. It must
// reuse that fence, not recursively acquire the store lock or an incompatible
// database transaction. Issuance remains the host's responsibility.
type SnapshotCompletionAdmission interface {
	CompletionProof(context.Context, SnapshotCompletionRequest) (SnapshotCompletionProof, error)
}

// SnapshotGCRequest binds independent deletion authority to exact physical
// custody, finite retention policy, ledger and operation. It never overrides
// KEEP, outstanding pins, unknown accounting or the complete ref/object map.
type SnapshotGCRequest struct {
	Operation     SnapshotOperation
	LedgerStoreID string
	Intent        GCIntent
}

// SnapshotGCProof is an independent, held host deletion capability. Verify
// must also fence every noncooperating store/object reader through reclamation.
// Current capture permission alone cannot issue this capability.
// Record has the same connection-bound revalidation/commit requirement as
// SnapshotCompletionProof; ambiguity keeps the durable mechanism barrier.
type SnapshotGCProof interface {
	VerifySnapshotGC(context.Context, SnapshotGCRequest) error
	RecordSnapshotGC(context.Context, SnapshotGCRequest, SnapshotEffect) error
}

type SnapshotGCAdmission interface {
	GCProof(context.Context, SnapshotGCRequest) (SnapshotGCProof, error)
}

// retentionOperation is a durable uncertainty barrier. Completion keeps the
// pin until host accounting AND successful host close; GC keeps this barrier
// until its effect journal and host accounting/close have all completed.
// No retry or timeout clears an unresolved barrier.
type retentionOperation struct {
	Kind, Digest, Phase string
	SetID, PinID        string
}

func retentionRequestDigest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (a *Admission) reserveRetention(h *admissionLock, id string, r retentionOperation) error {
	s, err := a.load()
	if err != nil {
		return err
	}
	if _, exists := s.RetentionOperations[id]; exists || collectionPending(s) {
		return ErrAdmissionUnavailable
	}
	if s.RetentionOperations == nil {
		s.RetentionOperations = map[string]retentionOperation{}
	}
	s.RetentionOperations[id] = r
	return a.save(h, s)
}

// CompleteFromHost consumes an independently issued owner proof from the
// already-held host admission. Missing/unissued/stale authority refuses before
// accounting. Any later uncertainty retains the durable pin and barrier.
// It closes this lease; repeated calls never replay a completion transition.
func (l *ReadLease) CompleteFromHost(ctx context.Context, operationID string) (err error) {
	if ctx == nil || l == nil || l.admission == nil || l.admission.isolation == nil || l.hostLease == nil || !safeReceiptID(operationID) {
		return ErrAdmissionUnavailable
	}
	if err = l.Verify(ctx); err != nil {
		return err
	}
	issuer, ok := l.hostLease.(SnapshotCompletionAdmission)
	if !ok {
		return ErrAdmissionUnavailable
	}
	s, err := l.admission.load()
	if err != nil {
		return err
	}
	pin := s.Pins[l.pin]
	r := SnapshotCompletionRequest{Operation: l.hostOperation, LedgerStoreID: l.admission.storeID, SetID: l.set.Intent.SetID, SetDigest: l.set.Digest, PinID: l.pin, Owner: pin.Owner, Kind: pin.Kind, OperationID: operationID}
	proof, err := issuer.CompletionProof(ctx, r)
	if err != nil || proof == nil {
		return ErrAdmissionUnavailable
	}
	verify := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !l.active || !time.Now().Before(l.deadline) || l.held.check() != nil || l.admission.isolation.Verify(ctx) != nil || l.hostLease == nil || l.hostLease.Verify(ctx, l.hostOperation) != nil || proof.VerifySnapshotCompletion(ctx, r) != nil {
			return ErrAdmissionUnavailable
		}
		state, e := l.admission.load()
		if e != nil {
			return e
		}
		current, exists := state.Pins[l.pin]
		set, hasSet := state.Sets[r.SetID]
		if !exists || current != pin || !hasSet || set.Digest != r.SetDigest || set.Pending || set.Collected || collectionPendingExcept(state, operationID) {
			return ErrAdmissionUnavailable
		}
		return nil
	}
	if err = verify(); err != nil {
		return err
	}
	if err = l.admission.reserveRetention(l.held, operationID, retentionOperation{Kind: "completion", Digest: retentionRequestDigest(r), Phase: "prepared", SetID: r.SetID, PinID: r.PinID}); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, l.Close()) }()
	if proof.RecordSnapshotCompletion(ctx, r, SnapshotEffect{Phase: "completion_intent", Outcome: "pending"}) != nil {
		return ErrAdmissionUnavailable
	}
	if err = verify(); err != nil {
		return err
	}
	if proof.RecordSnapshotCompletion(ctx, r, SnapshotEffect{Phase: "owner_completed", Outcome: "complete"}) != nil {
		return ErrAdmissionUnavailable
	}
	if err = verify(); err != nil {
		return err
	}
	// Closing the independent owner fence is an outcome, not authorization for
	// more source effects. The store lock remains held for final bookkeeping.
	host := l.hostLease
	l.hostLease = nil
	if err = closeSnapshotAdmission(host); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	s, err = l.admission.load()
	if err != nil {
		return err
	}
	pin.Completion = operationID
	s.Pins[l.pin] = pin
	record := s.RetentionOperations[operationID]
	record.Phase = "complete"
	s.RetentionOperations[operationID] = record
	return l.admission.save(l.held, s)
}

func hostGCOperation(g *GCGrant, i GCIntent) string {
	if g != nil && g.hostCheck != nil {
		return i.OperationID
	}
	return ""
}

// CollectFromHost consumes an independently issued deletion proof through the
// existing guarded collector. It never exposes a grant constructor. Host and
// physical checks remain held through ref/object effects and durable outcomes.
// No production host issuer is supplied by this package.
func (p *GuardedProvider) CollectFromHost(ctx context.Context, intent CaptureIntent, i GCIntent) (out GCResult, err error) {
	out.OperationID = i.OperationID
	if ctx == nil || p == nil || p.admission == nil || p.isolation == nil || intent.validate() != nil || i.validate() != nil || intent.OperationID != i.OperationID || intent.InputDigest != i.InputDigest || intent.InstanceID != i.InstanceID || intent.BindingFence != i.BindingFence || intent.ControllerEpoch != i.ControllerEpoch || intent.PolicyRevision != p.admission.policy.Revision || i.PolicyDigest != p.RetentionDigest() {
		return out, ErrAdmissionUnavailable
	}
	if !p.operationMu.TryLock() {
		return out, ErrAdmissionUnavailable
	}
	defer p.operationMu.Unlock()
	op := p.operation("gc", intent, gcDigest(i))
	host, err := p.acquireHost(ctx, op)
	if err != nil {
		return out, err
	}
	var held *admissionLock
	defer func() {
		if host != nil {
			if e := closeSnapshotAdmission(host); e != nil {
				out.Complete = false
				err = errors.Join(err, e)
			}
		}
		if held != nil {
			err = errors.Join(err, held.close())
		}
		if err != nil {
			out.Complete = false
			out.Partial = out.Partial || out.RemovedSets > 0
		}
	}()
	issuer, ok := host.(SnapshotGCAdmission)
	if !ok {
		return out, ErrAdmissionUnavailable
	}
	r := SnapshotGCRequest{Operation: op, LedgerStoreID: p.admission.storeID, Intent: i}
	proof, err := issuer.GCProof(ctx, r)
	if err != nil || proof == nil {
		return out, ErrAdmissionUnavailable
	}
	check := func(ctx context.Context) error {
		if e := p.verifyHost(ctx, host, op); e != nil {
			return e
		}
		if proof.VerifySnapshotGC(ctx, r) != nil {
			return ErrAdmissionUnavailable
		}
		return p.verifyHost(ctx, host, op)
	}
	if err = check(ctx); err != nil {
		return out, err
	}
	h, err := p.admission.lock(ctx)
	if err != nil {
		return out, err
	}
	held = h
	err = p.admission.reserveRetention(h, i.OperationID, retentionOperation{Kind: "gc", Digest: retentionRequestDigest(r), Phase: "prepared"})
	if err != nil {
		return out, err
	}
	if proof.RecordSnapshotGC(ctx, r, SnapshotEffect{Phase: "gc_intent", Outcome: "pending"}) != nil {
		return out, ErrAdmissionUnavailable
	}
	grant := &GCGrant{provider: p, digest: gcDigest(i), deadline: time.Now().Add(p.admission.policy.Budgets.MaxDuration), hostCheck: check, held: h}
	out, err = p.Collect(ctx, i, grant)
	if err != nil || !out.Complete {
		return out, err
	}
	out.Complete = false
	if err = check(ctx); err != nil {
		return out, err
	}
	if proof.RecordSnapshotGC(ctx, r, SnapshotEffect{Phase: "gc_accounted", Outcome: "complete", Changed: out.RemovedSets}) != nil {
		return out, ErrAdmissionUnavailable
	}
	if err = check(ctx); err != nil {
		return out, err
	}
	closing := host
	host = nil
	if err = closeSnapshotAdmission(closing); err != nil {
		return out, err
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	s, err := p.admission.load()
	if err != nil {
		return out, err
	}
	record := s.RetentionOperations[i.OperationID]
	record.Phase = "complete"
	s.RetentionOperations[i.OperationID] = record
	if err = p.admission.save(h, s); err != nil {
		return out, err
	}
	out.Complete = true
	return out, nil
}
