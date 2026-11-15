package session

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"

	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
)

// Manager is safe for concurrent callers when its host ports are. It holds no
// mutable conversation cache or local lock that could substitute for host CAS.
type Manager struct{ ports Ports }

func New(ports Ports) (*Manager, error) {
	if ports.Conversations == nil || ports.Input == nil || ports.Runtimes == nil || ports.Environments == nil || ports.Recoveries == nil || ports.Reconciler == nil || ports.Transcripts == nil {
		return nil, fmt.Errorf("%w: all host ports are required", ErrInvalid)
	}
	return &Manager{ports: ports}, nil
}

type OpenRequest struct {
	Mode    Mode
	Binding runtimecontract.Binding
	// Parent is required for continue, including recovery from a fenced attempt.
	// No implicit newest-conversation or provider-native selection is performed.
	Parent      *runtimecontract.Binding
	Environment runtimecontract.EnvironmentRef
	Reason      Reason
}

// Open reserves the new generation as Fenced before touching old input,
// checkpointing or preparing a runtime. Only a ready candidate may become Active.
// Any failure after reservation leaves an explicit recoverable fenced attempt;
// recovery names that attempt as parent and supplies another host-issued ID.
// The host owns an activated runtime's lifetime and must continue checking exact
// active binding at every effect. Open does not acquire a writer automatically.
func (m *Manager) Open(ctx context.Context, req OpenRequest) (Record, error) {
	if err := validateOpen(req); err != nil {
		return Record{}, err
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	req.Environment = cloneEnvironment(req.Environment)
	if req.Parent != nil {
		p := *req.Parent
		req.Parent = &p
	}
	if err := m.ports.Environments.Verify(ctx, req.Binding, cloneEnvironment(req.Environment)); err != nil {
		return Record{}, fmt.Errorf("verify environment: %w", err)
	}
	next := Record{Binding: req.Binding, Parent: req.Parent, Phase: Fenced, Reason: req.Reason}
	var reserved Record
	var err error
	if req.Mode == Fresh {
		reserved, err = m.ports.Conversations.Create(ctx, cloneRecord(next))
	} else {
		previous, readErr := m.View(ctx, scope(*req.Parent))
		if readErr != nil {
			return Record{}, readErr
		}
		if previous.Binding != *req.Parent {
			return Record{}, ErrStale
		}
		reserved, err = m.ports.Conversations.CompareAndSwap(ctx, scope(req.Binding), previous.Version, cloneRecord(next))
	}
	if err != nil {
		return Record{}, fmt.Errorf("reserve attempt: %w", err)
	}
	if err = validReturnedRecord(reserved, next); err != nil {
		return Record{}, err
	}

	var recovery *Pack
	if req.Parent != nil {
		if err = m.ports.Input.Revoke(ctx, *req.Parent); err != nil {
			return reserved, fmt.Errorf("revoke old writer: %w", err)
		}
		pack, captureErr := m.ports.Recoveries.Checkpoint(ctx, *req.Parent, req.Reason)
		if captureErr != nil {
			return reserved, fmt.Errorf("checkpoint: %w", captureErr)
		}
		if err = validatePack(pack, *req.Parent, false); err != nil {
			return reserved, err
		}
		pack = clonePack(pack)
		reconciled, reconcileErr := m.ports.Reconciler.Reconcile(ctx, req.Binding, clonePack(pack))
		if reconcileErr != nil {
			return reserved, fmt.Errorf("reconcile: %w", reconcileErr)
		}
		if err = validatePack(reconciled, *req.Parent, true); err != nil {
			return reserved, err
		}
		if err = validateReconciliation(pack, reconciled); err != nil {
			return reserved, err
		}
		// The reconciler can update pending-operation evidence, but cannot swap
		// history, plan, snapshot, workspace or cursor under a valid owner label.
		before, after := pack.Locator, reconciled.Locator
		before.PendingOperations, after.PendingOperations = nil, nil
		if !reflect.DeepEqual(before, after) {
			return reserved, fmt.Errorf("%w: reconciler changed portable history", ErrInvalid)
		}
		if reconciled.Locator.Workspace != req.Environment.Workspace {
			return reserved, fmt.Errorf("%w: continue requires the same workspace", ErrUnsupported)
		}
		pack = clonePack(reconciled)
		pack.Locator.Binding = req.Binding
		pack.Locator.Environment = cloneEnvironment(req.Environment)
		if err = validatePack(pack, req.Binding, true); err != nil {
			return reserved, err
		}
		if err = m.ports.Recoveries.Save(ctx, clonePack(pack)); err != nil {
			return reserved, fmt.Errorf("save recovery: %w", err)
		}
		recovery = &pack
	}
	prepare := PrepareRequest{Binding: req.Binding, Environment: cloneEnvironment(req.Environment)}
	if recovery != nil {
		p := clonePack(*recovery)
		prepare.Recovery = &p
	}
	candidate, err := m.ports.Runtimes.Prepare(ctx, prepare)
	if err != nil {
		if candidate != nil {
			err = errors.Join(err, closeCandidate(ctx, candidate))
		}
		return reserved, fmt.Errorf("prepare runtime: %w", err)
	}
	if candidate == nil {
		return reserved, fmt.Errorf("%w: nil prepared runtime", ErrInvalid)
	}
	cleanup := func(cause error) (Record, error) {
		return reserved, errors.Join(cause, closeCandidate(ctx, candidate))
	}
	ready, err := candidate.Ready(ctx)
	if err != nil {
		return cleanup(fmt.Errorf("runtime readiness: %w", err))
	}
	if ready != req.Binding {
		return cleanup(fmt.Errorf("%w: readiness binding mismatch", ErrStale))
	}
	if err = ctx.Err(); err != nil {
		return cleanup(err)
	}
	// Reverify host authority after slow preparation, even if Ready's adapter
	// performed its own checks. An expired receipt cannot become active.
	if err = m.ports.Environments.Verify(ctx, req.Binding, cloneEnvironment(req.Environment)); err != nil {
		return cleanup(fmt.Errorf("ready environment: %w", err))
	}
	if err = ctx.Err(); err != nil {
		return cleanup(err)
	}
	active := cloneRecord(reserved)
	active.Phase = Active
	committed, err := m.ports.Conversations.CompareAndSwap(ctx, scope(req.Binding), reserved.Version, active)
	if err != nil {
		return cleanup(fmt.Errorf("activate runtime: %w", err))
	}
	if err = validReturnedRecord(committed, active); err != nil {
		return cleanup(err)
	}
	return cloneRecord(committed), nil
}

// View attaches a read-only viewer. It acquires neither a runtime nor a lease.
func (m *Manager) View(ctx context.Context, selected Scope) (Record, error) {
	if err := validateScope(selected); err != nil {
		return Record{}, err
	}
	record, err := m.ports.Conversations.Load(ctx, selected)
	if err != nil {
		return Record{}, err
	}
	if err = validateRecord(record); err != nil {
		return Record{}, err
	}
	if scope(record.Binding) != selected {
		return Record{}, fmt.Errorf("%w: host returned another conversation", ErrInvalid)
	}
	return cloneRecord(record), nil
}

func (m *Manager) AcquireInput(ctx context.Context, binding runtimecontract.Binding, writer WriterID) (Lease, error) {
	if err := binding.Validate(); err != nil {
		return Lease{}, err
	}
	if !nonblank(string(writer)) {
		return Lease{}, ErrInvalid
	}
	lease, err := m.ports.Input.Acquire(ctx, binding, writer)
	if err != nil {
		return Lease{}, err
	}
	if err = validateLease(lease); err != nil {
		return Lease{}, err
	}
	if lease.Binding != binding || lease.Writer != writer {
		return Lease{}, ErrLease
	}
	return lease, nil
}

func (m *Manager) RenewInput(ctx context.Context, lease Lease) (Lease, error) {
	if err := validateLease(lease); err != nil {
		return Lease{}, err
	}
	next, err := m.ports.Input.Renew(ctx, lease)
	if err != nil {
		return Lease{}, err
	}
	if err = validateLease(next); err != nil {
		return Lease{}, err
	}
	if next.ID != lease.ID || next.Binding != lease.Binding || next.Writer != lease.Writer {
		return Lease{}, ErrLease
	}
	return next, nil
}

// RevokeInput releases this attempt's writer. Hosts must authenticate this call;
// a viewer's knowledge of a binding does not permit revocation.
func (m *Manager) RevokeInput(ctx context.Context, binding runtimecontract.Binding) error {
	if err := binding.Validate(); err != nil {
		return err
	}
	return m.ports.Input.Revoke(ctx, binding)
}

func (m *Manager) Submit(ctx context.Context, input Input) (runtimecontract.CommittedCursor, error) {
	if !nonblank(input.ID) || len(input.Text) > 1<<20 {
		return "", ErrInvalid
	}
	if err := validateLease(input.Lease); err != nil {
		return "", err
	}
	return m.ports.Input.Submit(ctx, input)
}

func (m *Manager) Admit(ctx context.Context, message Message) (runtimecontract.CommittedCursor, error) {
	if err := message.Binding.Validate(); err != nil {
		return "", err
	}
	if !nonblank(message.ID) || (message.Kind != Event && message.Kind != Approval) {
		return "", ErrInvalid
	}
	if err := message.Reference.Validate(); err != nil {
		return "", err
	}
	if message.Reference.OwnerID != message.Binding.OwnerID {
		return "", ErrInvalid
	}
	return m.ports.Conversations.Admit(ctx, message)
}

// Recovery reads an explicitly selected durable manifest for inspection. It
// neither restores a workspace nor executes unfinished operations.
func (m *Manager) Recovery(ctx context.Context, binding runtimecontract.Binding) (Pack, error) {
	if err := binding.Validate(); err != nil {
		return Pack{}, err
	}
	pack, err := m.ports.Recoveries.Load(ctx, binding)
	if err != nil {
		return Pack{}, err
	}
	if err = validatePack(pack, binding, false); err != nil {
		return Pack{}, err
	}
	return clonePack(pack), nil
}

func (m *Manager) LocateTranscript(ctx context.Context, selected Scope) (runtimecontract.Reference, error) {
	if _, err := m.View(ctx, selected); err != nil {
		return runtimecontract.Reference{}, err
	}
	ref, err := m.ports.Transcripts.Locate(ctx, selected)
	if err != nil {
		return runtimecontract.Reference{}, err
	}
	if err = ref.Validate(); err != nil {
		return runtimecontract.Reference{}, err
	}
	if ref.OwnerID != selected.OwnerID {
		return runtimecontract.Reference{}, ErrInvalid
	}
	return ref, nil
}

func (m *Manager) SearchTranscript(ctx context.Context, query SearchQuery) ([]TranscriptMatch, error) {
	if query.Limit < 1 || query.Limit > 100 || strings.TrimSpace(query.Text) == "" || len(query.Text) > 4096 {
		return nil, ErrInvalid
	}
	if _, err := m.View(ctx, query.Scope); err != nil {
		return nil, err
	}
	matches, err := m.ports.Transcripts.Search(ctx, query)
	if err != nil {
		return nil, err
	}
	if len(matches) > query.Limit {
		return nil, fmt.Errorf("%w: transcript search exceeds requested bound", ErrInvalid)
	}
	for _, match := range matches {
		if err = match.Reference.Validate(); err != nil {
			return nil, err
		}
		if match.Reference.OwnerID != query.Scope.OwnerID || len(match.Excerpt) > 8192 {
			return nil, ErrInvalid
		}
	}
	return append([]TranscriptMatch(nil), matches...), nil
}

func validateOpen(req OpenRequest) error {
	// Mode negotiation precedes even environment lookup or reservation.
	if req.Mode != Fresh && req.Mode != Continue {
		return ErrUnsupported
	}
	if err := req.Binding.Validate(); err != nil {
		return err
	}
	if err := req.Environment.Validate(); err != nil {
		return err
	}
	if req.Environment.Receipt.OwnerID != req.Binding.OwnerID {
		return ErrInvalid
	}
	if req.Mode == Fresh {
		if req.Parent != nil || req.Binding.Generation != 1 || req.Reason != "" {
			return fmt.Errorf("%w: fresh requires generation one and no parent/reason", ErrInvalid)
		}
		return nil
	}
	if req.Parent == nil {
		return fmt.Errorf("%w: continue requires an explicit parent binding", ErrInvalid)
	}
	if err := req.Parent.Validate(); err != nil {
		return err
	}
	if scope(*req.Parent) != scope(req.Binding) || req.Parent.SeatID != req.Binding.SeatID || req.Parent.AttemptID == req.Binding.AttemptID || req.Parent.Generation == math.MaxUint64 || req.Binding.Generation != req.Parent.Generation+1 {
		return fmt.Errorf("%w: parent scope/seat and successor generation must match", ErrInvalid)
	}
	switch req.Reason {
	case UserContinue, Crash, Quota, Recovery:
	default:
		return fmt.Errorf("%w: continue reason required", ErrInvalid)
	}
	return nil
}

func validateScope(s Scope) error {
	if !nonblank(string(s.OwnerID)) || !nonblank(string(s.OperativeID)) || !nonblank(string(s.ConversationID)) {
		return ErrInvalid
	}
	return nil
}

func validateRecord(r Record) error {
	if err := r.Binding.Validate(); err != nil {
		return err
	}
	if !nonblank(r.Version) || (r.Phase != Active && r.Phase != Fenced) {
		return ErrInvalid
	}
	if r.Parent != nil {
		if err := r.Parent.Validate(); err != nil {
			return err
		}
		if scope(*r.Parent) != scope(r.Binding) || r.Parent.SeatID != r.Binding.SeatID || r.Parent.Generation >= r.Binding.Generation || r.Parent.AttemptID == r.Binding.AttemptID {
			return ErrInvalid
		}
	}
	return nil
}

func validReturnedRecord(got, want Record) error {
	if err := validateRecord(got); err != nil {
		return err
	}
	want.Version = got.Version
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("%w: host returned a different transition", ErrInvalid)
	}
	return nil
}

func validateLease(l Lease) error {
	if err := l.Binding.Validate(); err != nil {
		return err
	}
	if !nonblank(string(l.ID)) || !nonblank(string(l.Writer)) || l.ExpiresAt.IsZero() {
		return ErrLease
	}
	return nil
}

func validatePack(p Pack, binding runtimecontract.Binding, reconciled bool) error {
	if err := p.Locator.Validate(); err != nil {
		return err
	}
	if p.Locator.Binding != binding || len(p.Operations) > 256 || len(p.Approvals) > 256 {
		return fmt.Errorf("%w: recovery binding or bounds", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, op := range p.Operations {
		if !nonblank(op.ID) || seen[op.ID] || !priorBinding(op.Binding, binding) {
			return ErrInvalid
		}
		seen[op.ID] = true
		if err := op.Digest.Validate(); err != nil {
			return err
		}
		switch op.State {
		case NotStarted, Succeeded, Failed, Cancelled, Unknown:
		case Started:
			if reconciled {
				return fmt.Errorf("%w: started effect must reconcile to known or unknown", ErrInvalid)
			}
		default:
			return ErrInvalid
		}
		if op.Evidence != nil {
			if err := op.Evidence.Validate(); err != nil {
				return err
			}
			if op.Evidence.OwnerID != binding.OwnerID {
				return ErrInvalid
			}
		}
	}
	for _, a := range p.Approvals {
		if !priorBinding(a.Binding, binding) {
			return ErrInvalid
		}
		if err := a.Reference.Validate(); err != nil {
			return err
		}
		if a.Reference.OwnerID != binding.OwnerID {
			return ErrInvalid
		}
		switch a.State {
		case Invalidated, ApprovalTerminal, ApprovalUnknown:
		case ApprovalPending:
			if reconciled {
				return fmt.Errorf("%w: old approval still pending", ErrInvalid)
			}
		default:
			return ErrInvalid
		}
	}
	return nil
}

func priorBinding(old, current runtimecontract.Binding) bool {
	return old.Validate() == nil && scope(old) == scope(current) && old.SeatID == current.SeatID && old.Generation <= current.Generation && (old.Generation != current.Generation || old.AttemptID == current.AttemptID)
}

func validateReconciliation(before, after Pack) error {
	if len(before.Operations) != len(after.Operations) || len(before.Approvals) != len(after.Approvals) {
		return fmt.Errorf("%w: reconciliation lost pending evidence", ErrInvalid)
	}
	ops := make(map[string]Operation, len(after.Operations))
	for _, op := range after.Operations {
		ops[op.ID] = op
	}
	for _, old := range before.Operations {
		next, ok := ops[old.ID]
		if !ok || old.Binding != next.Binding || old.Digest != next.Digest {
			return ErrInvalid
		}
		if (old.State == Succeeded || old.State == Failed || old.State == Cancelled) && next.State != old.State {
			return fmt.Errorf("%w: terminal effect changed", ErrInvalid)
		}
		if (old.State == Started || old.State == Unknown) && next.State == NotStarted {
			return fmt.Errorf("%w: uncertain effect marked replayable", ErrInvalid)
		}
	}
	for i, old := range before.Approvals {
		if old.Binding != after.Approvals[i].Binding || old.Reference != after.Approvals[i].Reference {
			return fmt.Errorf("%w: approval evidence changed", ErrInvalid)
		}
	}
	return nil
}

func nonblank(s string) bool { return len(s) <= 1024 && strings.TrimSpace(s) != "" }

func cloneRecord(r Record) Record {
	if r.Parent != nil {
		p := *r.Parent
		r.Parent = &p
	}
	return r
}

func closeCandidate(ctx context.Context, candidate Prepared) error {
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return candidate.Close(closeCtx)
}

func cloneEnvironment(e runtimecontract.EnvironmentRef) runtimecontract.EnvironmentRef {
	e.Capabilities = append([]string(nil), e.Capabilities...)
	return e
}

func clonePack(p Pack) Pack {
	p.Locator.Environment = cloneEnvironment(p.Locator.Environment)
	for _, ref := range []**runtimecontract.Reference{&p.Locator.Plan, &p.Locator.Snapshot, &p.Locator.PendingOperations} {
		if *ref != nil {
			copy := **ref
			*ref = &copy
		}
	}
	p.Operations = append([]Operation(nil), p.Operations...)
	for i := range p.Operations {
		if p.Operations[i].Evidence != nil {
			ref := *p.Operations[i].Evidence
			p.Operations[i].Evidence = &ref
		}
	}
	p.Approvals = append([]PendingApproval(nil), p.Approvals...)
	return p
}
