package approval

import (
	"context"
	"errors"
	"sync"
	"time"

	permissionlib "github.com/hollis-labs/substrate/harness/interception/permission"
)

var (
	ErrNotFound = errors.New("approval not found in this view")
	ErrConflict = errors.New("approval is closed or has a different decision")
	ErrScope    = errors.New("bound native approvals support only once scope")
)

// Decision is the resource outcome, separate from a general
// request-key store. The host retains these alongside its volatile run state.
type Decision struct {
	ApprovalID string                 `json:"approval_id"`
	ViewID     string                 `json:"session_view_id"`
	RunID      string                 `json:"run_id"`
	CallID     string                 `json:"call_id"`
	Decision   permissionlib.Decision `json:"decision"`
	Scope      permissionlib.Scope    `json:"scope"`
}

type cognitiveApproval struct {
	decision Decision
	expires  time.Time
	closed   bool
	answered bool
}

// Registry binds native inband prompts to the exact run before
// publishing them. It accepts only once scope; it cannot broaden a tool grant.
type Registry struct {
	mu      sync.Mutex
	engine  *permissionlib.Engine
	records map[string]*cognitiveApproval
}

func New(engine *permissionlib.Engine) *Registry {
	return &Registry{engine: engine, records: make(map[string]*cognitiveApproval)}
}

// Request creates and binds a native prompt under the same lock used by both
// response facades. A retained response cannot reach the engine between
// request creation and publication of its run/call binding.
func (a *Registry) Request(viewID, runID, callID, tool string, input map[string]any, reason string) *permissionlib.ApprovalRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	req := a.engine.RequestApproval(viewID, tool, input, reason)
	a.bindLocked(req, runID, callID)
	return req
}

func (a *Registry) Bind(req *permissionlib.ApprovalRequest, runID, callID string) {
	if a == nil || req == nil || runID == "" || callID == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.bindLocked(req, runID, callID)
}

func (a *Registry) bindLocked(req *permissionlib.ApprovalRequest, runID, callID string) {
	if _, exists := a.records[req.ID]; exists {
		return
	}
	a.records[req.ID] = &cognitiveApproval{
		decision: Decision{ApprovalID: req.ID, ViewID: req.SessionID, RunID: runID, CallID: callID},
		expires:  req.CreatedAt.Add(permissionlib.DefaultApprovalTimeout),
	}
}

// Finish closes unanswered prompts on timeout/cancellation. A successful
// response was already recorded under the same lock before the waiter resumed.
func (a *Registry) Finish(approvalID string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if record := a.records[approvalID]; record != nil {
		record.closed = true
	}
}

func (a *Registry) Respond(ctx context.Context, viewID, approvalID string, decision permissionlib.Decision, scope permissionlib.Scope) (Decision, error) {
	var zero Decision
	if a == nil || a.engine == nil {
		return zero, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.respondLocked(viewID, approvalID, decision, scope)
}

// RespondRetained preserves the retained facade for genuinely unbound
// requests. Bound requests always use the native resource outcome, including
// wrong-owner, closed and repeated requests; none falls back to the engine.
func (a *Registry) RespondRetained(ctx context.Context, viewID, approvalID string, decision permissionlib.Decision, scope permissionlib.Scope) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.records[approvalID] != nil {
		_, err := a.respondLocked(viewID, approvalID, decision, scope)
		return err
	}
	if a.engine == nil || !a.engine.Respond(approvalID, decision, scope, viewID) {
		return ErrNotFound
	}
	return nil
}

func (a *Registry) respondLocked(viewID, approvalID string, decision permissionlib.Decision, scope permissionlib.Scope) (Decision, error) {
	var zero Decision
	record := a.records[approvalID]
	if record == nil || record.decision.ViewID != viewID {
		return zero, ErrNotFound
	}
	if scope != permissionlib.ScopeOnce {
		return zero, ErrScope
	}
	if record.answered {
		if record.decision.Decision == decision && record.decision.Scope == scope {
			return record.decision, nil
		}
		return zero, ErrConflict
	}
	if record.closed || !time.Now().Before(record.expires) {
		return zero, ErrConflict
	}
	if decision != permissionlib.DecisionAllow && decision != permissionlib.DecisionDeny {
		return zero, ErrConflict
	}
	if !a.engine.Respond(approvalID, decision, scope, viewID) {
		record.closed = true
		return zero, ErrConflict
	}
	record.decision.Decision, record.decision.Scope = decision, scope
	record.answered = true
	return record.decision, nil
}
