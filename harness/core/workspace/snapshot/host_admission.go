package snapshot

import (
	"context"
	"errors"
	"os"
)

// SnapshotOperation binds a trusted host admission to exact mechanism inputs.
// The observation digest proves physical facts, never fabric authority.
type SnapshotOperation struct {
	Kind                                                                        string
	Intent                                                                      CaptureIntent
	StoreID, IsolationDigest, ProtectedDigest, RequestDigest, RedactionRevision string
}

// SnapshotHost is an independently implemented host authority boundary. The
// host must resolve current principal, policy, binding, instance and controller
// from its authoritative stores and continuously fence all submission/writers.
// A test callback returning nil, policy data, or a decoded receipt is not a
// supported host producer. Physical verification is performed separately here.
type SnapshotHost interface {
	AcquireSnapshot(context.Context, SnapshotOperation) (SnapshotAdmission, error)
}

// SnapshotAdmission holds host admission through observation, irreversible
// effects and their recorded outcome. Record must durably bind operation and
// phase and refuse changed or already-uncertain effect retries. Close must keep
// uncertain writer admission fenced; it cannot mean unconditional unfreeze.
type SnapshotAdmission interface {
	Verify(context.Context, SnapshotOperation) error
	Record(context.Context, SnapshotOperation, SnapshotEffect) error
	Close() error
}

// SnapshotEffect is sanitized outcome data, not a completion/GC grant.
type SnapshotEffect struct {
	Phase, Outcome string
	Changed        int
	Partial        bool
}

// ContentRedactor adapts the host's resolved redaction layer BEFORE bytes enter
// the mirror or Git. It must be bounded, deterministic and safe for concurrent
// calls, with no filesystem/credential discovery. Arbitrary PII detection is
// not promised. Supplied revision is bound to current host policy.
type ContentRedactor interface {
	RedactSnapshot(context.Context, []byte) ([]byte, error)
}

// IsolationRequest requests an observation of an already-frozen, provider-only
// Linux cgroup and its exact canonical namespace payload. It does not freeze,
// start, stop or authorize a process. All descendants must satisfy the same
// namespace/store exclusion contract. Empty groups cannot prove real isolation.
type IsolationRequest struct {
	ProcessID    int
	StartTime    uint64
	ControlGroup *os.File
	StorePath    string
	// ProtectedPaths are the host's complete canonical control/journal/lock
	// directory inventory. At least one independent control root is required.
	// The observer checks physical exclusion; the host separately verifies that
	// this inventory is complete for the current execution. Paths are not grants.
	ProtectedPaths []string
	Targets        TargetPlan
}

// IsolationProof is issued only by actual platform observations. It cannot be
// decoded or minted with a public constructor accepting an assertion. Holding
// it is not authority; current independent host admission remains required.
type IsolationProof struct {
	store                string
	targetDigest, digest string
	storeID              string
	protectedDigest      string
	check                func(context.Context) error
	close                func() error
}

func (p *GuardedProvider) operation(kind string, intent CaptureIntent, digest string) SnapshotOperation {
	return SnapshotOperation{Kind: kind, Intent: intent, StoreID: p.isolation.storeID, IsolationDigest: p.isolation.Digest(), ProtectedDigest: p.isolation.ProtectedDigest(), RequestDigest: digest, RedactionRevision: p.redactionRevision}
}

// ProtectedDigest binds the immutable protected-root inventory and native
// identities. The independent host must reconcile it with its complete current
// control-plane inventory; an omitted path cannot become authorized by a hash.
func (p *IsolationProof) ProtectedDigest() string {
	if p == nil {
		return ""
	}
	return p.protectedDigest
}

func (p *IsolationProof) Digest() string {
	if p == nil {
		return ""
	}
	return p.digest
}
func (p *IsolationProof) Verify(ctx context.Context) error {
	if ctx == nil || p == nil || p.check == nil || p.digest == "" {
		return ErrStoreCustodyUnsupported
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return p.check(ctx)
}

// Close releases only observation descriptors. It never unfreezes processes,
// deletes evidence or releases host authority/pins.
func (p *IsolationProof) Close() error {
	if p == nil || p.close == nil {
		return nil
	}
	return p.close()
}

func (p *GuardedProvider) acquireHost(ctx context.Context, op SnapshotOperation) (SnapshotAdmission, error) {
	if p == nil || p.host == nil || p.isolation == nil || op.IsolationDigest != p.isolation.Digest() || op.Intent.TargetMapDigest != p.plan.Digest() {
		return nil, ErrAdmissionUnavailable
	}
	if err := p.isolation.Verify(ctx); err != nil {
		return nil, err
	}
	h, err := p.host.AcquireSnapshot(ctx, op)
	if err != nil {
		return nil, ErrAdmissionUnavailable
	}
	if h == nil {
		return nil, ErrAdmissionUnavailable
	}
	if err := p.verifyHost(ctx, h, op); err != nil {
		return nil, errors.Join(err, closeSnapshotAdmission(h))
	}
	return h, nil
}

func closeSnapshotAdmission(h SnapshotAdmission) error {
	if h != nil && h.Close() != nil {
		return ErrAdmissionUnavailable
	}
	return nil
}
func (p *GuardedProvider) verifyHost(ctx context.Context, h SnapshotAdmission, op SnapshotOperation) error {
	if h == nil || p == nil || p.isolation == nil {
		return ErrAdmissionUnavailable
	}
	if err := p.isolation.Verify(ctx); err != nil {
		return err
	}
	if err := p.guard.check(); err != nil {
		return err
	}
	if err := h.Verify(ctx, op); err != nil {
		return ErrAdmissionUnavailable
	}
	return p.isolation.Verify(ctx)
}
