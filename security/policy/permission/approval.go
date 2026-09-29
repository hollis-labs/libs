package permission

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync/atomic"
	"time"
)

// ApprovalRequest represents a pending permission prompt waiting for a
// human (or other) answer.
type ApprovalRequest struct {
	ID        string
	SessionID string
	ToolName  string
	Input     map[string]any
	Reason    string
	CreatedAt time.Time
	// Response receives exactly one ApprovalResponse. It is never closed;
	// read it through Engine.WaitForApproval.
	Response chan ApprovalResponse

	answered atomic.Bool // set by the first Respond
}

// ApprovalResponse is the answer to an approval request.
type ApprovalResponse struct {
	Decision Decision
	Scope    Scope
	TimedOut bool // true when denial was due to timeout, not explicit action
}

// RequestApproval creates a pending approval request and returns it. Surface
// the request to whoever decides (over whatever transport the host has), then
// call WaitForApproval. Requests that are never waited on stay pending, so
// always pair the two.
func (e *Engine) RequestApproval(sessionID, toolName string, input map[string]any, reason string) *ApprovalRequest {
	req := &ApprovalRequest{
		ID:        newRequestID(),
		SessionID: sessionID,
		ToolName:  toolName,
		Input:     input,
		Reason:    reason,
		CreatedAt: time.Now(),
		Response:  make(chan ApprovalResponse, 1),
	}
	e.pendingApprovals.Store(req.ID, req)
	e.audit(context.Background(), Event{
		Kind: EventApprovalRequested, SessionID: sessionID, Tool: toolName, RequestID: req.ID,
	})
	return req
}

// WaitForApproval blocks until Respond answers, the approval timeout expires,
// or ctx ends. Timeout and cancellation both yield a deny with ScopeOnce; only
// a timeout sets TimedOut.
func (e *Engine) WaitForApproval(ctx context.Context, req *ApprovalRequest) ApprovalResponse {
	e.mu.RLock()
	timeout := e.approvalTimeout
	e.mu.RUnlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	defer e.pendingApprovals.Delete(req.ID)

	var resp ApprovalResponse
	var kind EventKind
	select {
	case resp = <-req.Response:
		kind = EventApprovalResolved
	case <-timer.C:
		resp = ApprovalResponse{Decision: DecisionDeny, Scope: ScopeOnce, TimedOut: true}
		kind = EventApprovalTimeout
	case <-ctx.Done():
		resp = ApprovalResponse{Decision: DecisionDeny, Scope: ScopeOnce}
		kind = EventApprovalCanceled
	}
	e.audit(ctx, Event{
		Kind: kind, SessionID: req.SessionID, Tool: req.ToolName, RequestID: req.ID, Response: &resp,
	})
	return resp
}

// Respond delivers a response to a pending approval request. It returns false
// when the request does not exist (already timed out or answered), or when
// sessionID is not exactly the session the request was made for; an empty
// sessionID does not bypass that check.
//
// An allow at ScopeSession records a session grant for the tool name. An allow
// at ScopeProject is handed to the RuleStore; with no RuleStore, or if Append
// fails, the response is delivered as ScopeOnce (a failure is reported to the
// Auditor).
func (e *Engine) Respond(requestID string, decision Decision, scope Scope, sessionID string) bool {
	val, ok := e.pendingApprovals.Load(requestID)
	if !ok {
		return false
	}
	req := val.(*ApprovalRequest)

	if req.SessionID != sessionID {
		return false
	}
	// Claim the request so a second Respond cannot record a second grant.
	if !req.answered.CompareAndSwap(false, true) {
		return false
	}

	ctx := context.Background()
	if decision == DecisionAllow {
		switch scope {
		case ScopeOnce:
			// nothing to record
		case ScopeSession:
			e.mu.Lock()
			if e.sessionGrants[req.SessionID] == nil {
				e.sessionGrants[req.SessionID] = make(map[string]Decision)
			}
			e.sessionGrants[req.SessionID][req.ToolName] = DecisionAllow
			e.mu.Unlock()
			e.audit(ctx, Event{Kind: EventGrantRecorded, SessionID: req.SessionID, Tool: req.ToolName, RequestID: req.ID})
		case ScopeProject:
			scope = e.persistProjectGrant(ctx, req)
		}
	} else if scope == ScopeProject {
		scope = ScopeOnce // only allows are persisted
	}

	req.Response <- ApprovalResponse{Decision: decision, Scope: scope}
	return true
}

// persistProjectGrant hands an allowed project-scope approval to the RuleStore
// and returns the scope to report: ScopeProject on success, ScopeOnce when
// there is no store or it failed.
func (e *Engine) persistProjectGrant(ctx context.Context, req *ApprovalRequest) Scope {
	if e.ruleStore == nil {
		return ScopeOnce
	}
	rule := Rule{Tool: req.ToolName, Behavior: DecisionAllow, Source: "approval"}
	ev := Event{Kind: EventGrantRecorded, SessionID: req.SessionID, Tool: req.ToolName, RequestID: req.ID}
	if err := e.ruleStore.Append(ctx, ScopeProject, req.SessionID, rule); err != nil {
		ev.Result = CheckResult{Reason: "project grant not persisted: " + err.Error()}
		e.audit(ctx, ev)
		return ScopeOnce
	}
	e.audit(ctx, ev)
	return ScopeProject
}

// newRequestID returns 16 random bytes, hex-encoded.
func newRequestID() string {
	var b [16]byte
	// crypto/rand.Read does not return an error (it aborts the program if the
	// system's secure random source fails).
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
