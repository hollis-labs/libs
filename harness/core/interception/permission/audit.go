package permission

import (
	"context"
	"log/slog"
	"time"
)

// EventKind identifies the step an Event reports.
type EventKind string

// The EventKind values.
const (
	// EventDecision is emitted by every Engine.Check.
	EventDecision EventKind = "decision"
	// EventApprovalRequested is emitted by Engine.RequestApproval.
	EventApprovalRequested EventKind = "approval_requested"
	// EventApprovalResolved is emitted when WaitForApproval returns an
	// explicit response.
	EventApprovalResolved EventKind = "approval_resolved"
	// EventApprovalTimeout is emitted when WaitForApproval times out.
	EventApprovalTimeout EventKind = "approval_timeout"
	// EventApprovalCanceled is emitted when WaitForApproval's context ends.
	EventApprovalCanceled EventKind = "approval_canceled"
	// EventGrantRecorded is emitted when Respond records a session grant, or
	// hands a project-scope grant to the RuleStore. If the RuleStore rejects
	// it, the event's Result.Reason says so and the response is delivered as
	// ScopeOnce.
	EventGrantRecorded EventKind = "grant_recorded"
	// EventGrantsCleared is emitted by Engine.ClearSessionGrants.
	EventGrantsCleared EventKind = "grants_cleared"
)

// Event is one auditable step in the engine. It deliberately carries no tool
// input, which may hold secrets.
type Event struct {
	Kind      EventKind
	At        time.Time
	SessionID string
	Tool      string
	// Result is set for EventDecision; for EventGrantRecorded its Reason
	// describes a RuleStore failure, if any.
	Result CheckResult
	// RequestID is set on approval events and EventGrantRecorded.
	RequestID string
	// Response is set on EventApprovalResolved, EventApprovalTimeout and
	// EventApprovalCanceled.
	Response *ApprovalResponse
	// Actor and OnBehalfOf are reserved and always empty in v1.
	Actor, OnBehalfOf string
}

// Auditor receives Events from an Engine. The engine calls Audit
// synchronously and never while holding its internal locks, so an Auditor may
// call back into the Engine. Audit should return quickly and must be safe for
// concurrent use.
type Auditor interface {
	Audit(ctx context.Context, e Event)
}

// SlogAuditor logs approval lifecycle events through log/slog. It ignores
// decision and grant events. A nil Logger uses slog.Default.
type SlogAuditor struct {
	Logger *slog.Logger
}

var _ Auditor = SlogAuditor{}

// Audit implements Auditor.
func (a SlogAuditor) Audit(ctx context.Context, e Event) {
	l := a.Logger
	if l == nil {
		l = slog.Default()
	}
	switch e.Kind {
	case EventApprovalRequested:
		l.InfoContext(ctx, "permission: approval request created",
			"id", e.RequestID, "tool", e.Tool, "session_id", e.SessionID)
	case EventApprovalResolved:
		if e.Response != nil {
			l.InfoContext(ctx, "permission: approval responded",
				"id", e.RequestID, "decision", e.Response.Decision, "scope", e.Response.Scope)
		}
	case EventApprovalTimeout:
		l.WarnContext(ctx, "permission: approval timed out — defaulting to deny", "id", e.RequestID)
	case EventApprovalCanceled:
		l.WarnContext(ctx, "permission: approval canceled — defaulting to deny", "id", e.RequestID)
	}
}

// audit emits e to the configured Auditor, stamping the time. Callers must not
// hold e.mu.
func (e *Engine) audit(ctx context.Context, ev Event) {
	if e.auditor == nil {
		return
	}
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	e.auditor.Audit(ctx, ev)
}
