package pluginhooks

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// RemoteHandler is implemented by a trusted host adapter over its own transport.
// It must honor ctx, keep reader/control/reply progress independent of a call,
// and return privately owned results. The core imports no wire or SDK module.
type RemoteHandler interface {
	Handle(context.Context, RemoteRequest) (RemoteResult, error)
}
type RemoteHandlerFunc func(context.Context, RemoteRequest) (RemoteResult, error)

func (f RemoteHandlerFunc) Handle(ctx context.Context, r RemoteRequest) (RemoteResult, error) {
	return f(ctx, r)
}

// RemoteConnection is a host-owned fence for a single negotiated connection.
// Close is terminal: replacing a process requires a fresh connection and scope.
// This is not an authentication grant or a plugin-supplied incarnation counter.
type RemoteConnection struct {
	id     string
	ctx    context.Context
	cancel context.CancelFunc
}

func NewRemoteConnection() (*RemoteConnection, error) {
	id, err := remoteToken()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background()) //nolint:gosec // The host owns Close for the connection lifetime.
	return &RemoteConnection{id: id, ctx: ctx, cancel: cancel}, nil
}
func (c *RemoteConnection) ID() string {
	if c == nil {
		return ""
	}
	return c.id
}
func (c *RemoteConnection) Close() {
	if c != nil && c.cancel != nil {
		c.cancel()
	}
}
func (c *RemoteConnection) live() bool { return c != nil && c.ctx != nil && c.ctx.Err() == nil }
func remoteToken() (string, error) {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("remote binding identity: %w", err)
	}
	return hex.EncodeToString(token[:]), nil
}

// RemoteRegistration is supplied only by the host, after profile negotiation.
// LatencyEstimate is the host's positive round-trip estimate, not plugin data.
// It must fit the declaration's ceiling; every send also checks remaining time.
type RemoteRegistration struct {
	Handler         RemoteHandler
	Connection      *RemoteConnection
	LatencyEstimate time.Duration
}

func validateRemoteRegistration(d Definition, r RemoteRegistration) error {
	if !*d.RemoteOK {
		return &RemoteFailure{Code: RemoteNotAllowed}
	}
	if r.Handler == nil || r.Connection == nil || r.Connection.ctx == nil || r.LatencyEstimate <= 0 {
		return ErrInvalidOptions
	}
	if !r.Connection.live() {
		return &RemoteFailure{Code: StaleBinding}
	}
	if r.LatencyEstimate > d.RemoteLatencyBudget {
		return &RemoteFailure{Code: LatencyBudgetExceeded, admission: true}
	}
	return nil
}
func (s *Scope) AddRemoteAction(hook, name string, o Options, r RemoteRegistration) (Handle, error) {
	private := r
	return s.add(hook, name, o, nil, nil, Action, &private)
}
func (s *Scope) AddRemoteFilter(hook, name string, o Options, r RemoteRegistration) (Handle, error) {
	private := r
	return s.add(hook, name, o, nil, nil, Filter, &private)
}

// RemoteScope is a private-copy identity snapshot, never a registration grant.
// Generation remains the registry's opaque string; a wire bridge maps its own
// host-verified incarnation representation without inferring authority from it.
type RemoteScope struct{ HostInstance, Owner, Generation, RegistrationID string }

// RemoteContext carries host-established diagnostic values. Part A single-call
// transport does not expose reverse-callback authorization or a depth override.
// Deadlines remain host-monotonic context deadlines; do not assume clock sync.
type RemoteContext struct {
	BindingID, ConnectionID, RootInvocationID, ParentInvocationID string
	Deadline                                                      time.Time
	Timeout, AggregateBudget                                      time.Duration
	Depth                                                         int
	Trace                                                         TraceContext
}
type RemoteRequest struct {
	InvocationID, CatalogVersion, Hook, SchemaDigest string
	Kind                                             Kind
	Mode                                             Mode
	Scope                                            RemoteScope
	Context                                          RemoteContext
	Payload                                          json.RawMessage
	Metadata                                         map[string]string
}
type RemoteStatus string

const (
	RemoteOK               RemoteStatus = "ok"
	RemoteCancelled        RemoteStatus = "cancelled"
	RemoteApprovalRequired RemoteStatus = "approval_required"
	RemoteFailed           RemoteStatus = "failed"
	RemoteUnavailable      RemoteStatus = "unavailable"
)

type RemoteFailureCode string

const (
	RemoteNotAllowed      RemoteFailureCode = "remote_not_allowed"
	LatencyBudgetExceeded RemoteFailureCode = "latency_budget_exceeded"
	StaleScope            RemoteFailureCode = "stale_scope"
	StaleBinding          RemoteFailureCode = "stale_binding"
	CapacityExhausted     RemoteFailureCode = "capacity_exhausted"
	DeadlineExceeded      RemoteFailureCode = "deadline_exceeded"
	CallerCancellation    RemoteFailureCode = "caller_cancelled"
	DepthExceeded         RemoteFailureCode = "depth_exceeded"
	CallbackCycle         RemoteFailureCode = "callback_cycle"
	TransportFailure      RemoteFailureCode = "transport_failure"
	HandlerPanic          RemoteFailureCode = "handler_panic"
	InvalidOutput         RemoteFailureCode = "invalid_output"
	HandlerError          RemoteFailureCode = "handler_error"
	SchemaMismatch        RemoteFailureCode = "schema_mismatch"
	ProfileUnavailable    RemoteFailureCode = "profile_unavailable"
)

var ErrRemoteHandler = errors.New("remote handler failed")

// RemoteFailure has a closed code vocabulary. Message is diagnostic only; Error
// returns only the code, and telemetry never consumes Message or remote reasons.
type RemoteFailure struct {
	Code      RemoteFailureCode
	Message   string
	admission bool
}

func (f *RemoteFailure) Error() string { return string(f.Code) }
func (f *RemoteFailure) Unwrap() error {
	switch f.Code {
	case RemoteNotAllowed:
		return ErrUnauthorized
	case LatencyBudgetExceeded, StaleScope, StaleBinding, CapacityExhausted, ProfileUnavailable, CallbackCycle:
		return ErrUnavailable
	case DeadlineExceeded:
		return context.DeadlineExceeded
	case CallerCancellation:
		return context.Canceled
	case DepthExceeded:
		return ErrDepthExceeded
	case TransportFailure:
		return ErrTransport
	case HandlerPanic:
		return ErrPanic
	case InvalidOutput:
		return ErrInvalidOutput
	case SchemaMismatch:
		return ErrInvalidOptions
	default:
		return ErrRemoteHandler
	}
}
func validRemoteCode(code RemoteFailureCode) bool {
	switch code {
	case RemoteNotAllowed, LatencyBudgetExceeded, StaleScope, StaleBinding, CapacityExhausted, DeadlineExceeded, CallerCancellation, DepthExceeded, CallbackCycle, TransportFailure, HandlerPanic, InvalidOutput, HandlerError, SchemaMismatch, ProfileUnavailable:
		return true
	default:
		return false
	}
}

// RemoteResult is a per-handler result, not an Engine dispatch status.
// Payload nil means absent; JSON null must be supplied as bytes("null").
// Only bail actions may return deliberate cancelled/approval_required statuses.
type RemoteResult struct {
	InvocationID string
	Status       RemoteStatus
	Payload      json.RawMessage
	Reason       string
	Failure      *RemoteFailure
}

func validateRemoteResult(d Definition, id string, result RemoteResult) (json.RawMessage, error) {
	if result.InvocationID != id {
		return nil, ErrInvalidOutput
	}
	switch result.Status {
	case RemoteOK:
		if result.Failure != nil || result.Reason != "" || (d.Kind == Action && result.Payload != nil) || (d.Kind == Filter && (result.Payload == nil || !json.Valid(result.Payload))) {
			return nil, ErrInvalidOutput
		}
		return bytes.Clone(result.Payload), nil
	case RemoteCancelled, RemoteApprovalRequired:
		if d.Kind != Action || d.Mode != Bail || result.Payload != nil || result.Failure != nil {
			return nil, ErrInvalidOutput
		}
		if result.Status == RemoteCancelled {
			return nil, ErrCancelled
		}
		return nil, ErrApprovalRequired
	case RemoteFailed, RemoteUnavailable:
		if result.Payload != nil || result.Reason != "" || result.Failure == nil || !validRemoteCode(result.Failure.Code) {
			return nil, ErrInvalidOutput
		}
		// Copy the diagnostic failure so the transport cannot mutate a published error.
		failure := *result.Failure
		failure.admission = false
		return nil, &failure
	default:
		return nil, ErrInvalidOutput
	}
}
func remoteEntryUnavailable(e *entry) bool { return e.remote != nil && !e.remote.Connection.live() }
func remoteAdmission(ctx context.Context, entry *entry) error {
	if entry.remote == nil {
		return nil
	}
	if !entry.remote.Connection.live() {
		return &RemoteFailure{Code: StaleBinding}
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) < entry.remote.LatencyEstimate {
		return &RemoteFailure{Code: LatencyBudgetExceeded, admission: true}
	}
	return nil
}
func (e *Engine) invokeRemote(ctx context.Context, d *dispatch, entry *entry, payload json.RawMessage, metadata map[string]string) (json.RawMessage, error) {
	if err := remoteAdmission(ctx, entry); err != nil {
		return nil, err
	}
	binding, err := remoteToken()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrTransport, err)
	}
	reg := entry.registration
	deadline, _ := ctx.Deadline()
	rootDeadline, _ := d.ctx.Deadline()
	trace, _ := TraceContextFrom(ctx)
	now := time.Now()
	remaining := deadline.Sub(now)
	if remaining < entry.remote.LatencyEstimate {
		return nil, &RemoteFailure{Code: LatencyBudgetExceeded, admission: true}
	}
	privateMetadata := cloneMetadata(metadata)
	if privateMetadata == nil {
		privateMetadata = map[string]string{}
	}
	id := fmt.Sprintf("%s:%s:%d", e.registry.hostInstance, d.id, reg.Handle.id)
	request := RemoteRequest{InvocationID: id, CatalogVersion: e.registry.catalogVersion, Hook: reg.Hook, SchemaDigest: d.definition.SchemaDigest, Kind: d.definition.Kind, Mode: d.definition.Mode,
		Scope:   RemoteScope{HostInstance: e.registry.hostInstance, Owner: reg.Owner, Generation: reg.Generation, RegistrationID: fmt.Sprintf("%s:%d", e.registry.hostInstance, reg.Handle.id)},
		Context: RemoteContext{BindingID: binding, ConnectionID: entry.remote.Connection.ID(), RootInvocationID: d.id, Deadline: deadline, Timeout: remaining, AggregateBudget: rootDeadline.Sub(now), Depth: d.trace.Depth, Trace: trace}, Payload: bytes.Clone(payload), Metadata: privateMetadata}
	// The trusted adapter must preserve the host's tuple when mapping its wire DTO;
	// the expected id/deadline are retained separately from the mutable request copy.
	result, callErr := entry.remote.Handler.Handle(ctx, request)
	if !entry.remote.Connection.live() {
		return nil, &RemoteFailure{Code: StaleBinding}
	}
	if callErr != nil {
		// A transport error is never a veto, even when its text/sentinel claims one.
		return nil, fmt.Errorf("%w: remote request failed", ErrTransport)
	}
	return validateRemoteResult(d.definition, id, result)
}
