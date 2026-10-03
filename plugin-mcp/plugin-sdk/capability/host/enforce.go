package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hollis-labs/plugin-sdk/capability"
)

const PluginActor SubjectKind = "plugin"

// Call contains host-normalized operation demands, never raw RPC arguments.
// Dimensions and Usage must cover every descriptor dimension/limit; adapters
// resolve canonical identifiers and actual effects rather than trusting hints.
type Call struct {
	Capability, GrantID, Operation, Target string
	Effect                                 capability.Effect
	Dimensions                             map[string]string
	Usage                                  map[string]int64
	TraceID, RequestID, Server, Tool       string
}

// Authority is resolved afresh from a trusted host transport/policy adapter.
// Actor and initiating caller MUST be authenticated by that adapter. Plugin
// calls require a connection-bound narrow scope, never a bearer credential.
// LeaseContext is cancelled by lifecycle, grant or binding withdrawal.
type Authority struct {
	Authenticated           bool
	Actor                   Subject
	InitiatingCaller        *Subject
	Owner                   capability.RuntimeIdentity
	Audience                string
	Grant                   capability.Grant
	PolicyRevision          string
	Policy                  capability.Scope
	CallerPolicy            *capability.Scope
	TransportScope          *capability.Scope
	LeaseContext            context.Context
	Active, TargetAvailable bool
	ProvisionalLogging      bool
}

// AuthorityResolver authenticates the actual connection/client, validates its
// binding and obtains the current live grant, tuple, policy and target state.
// Request fields/headers cannot stand in for those host-verified facts.
type AuthorityResolver interface {
	Resolve(context.Context, Call) (Authority, error)
}

// Budget reserves cumulative/rate/concurrency budgets before effects. It must
// be atomic across calls, return a nonnil release function and not block without
// observing ctx. Missing budget adapters fail closed even for byte-only limits.
type Budget interface {
	Reserve(context.Context, Authority, Call) (release func(), err error)
}

type Enforcer struct {
	Catalog  *capability.Catalog
	Resolver AuthorityResolver
	Budget   Budget
	Audit    *Auditor
	Clock    Clock
	allowed  atomic.Uint64
	denied   atomic.Uint64
}

func (e *Enforcer) clock() Clock {
	if e.Clock != nil {
		return e.Clock
	}
	return wallClock{}
}

func decodeScope(name string, raw json.RawMessage) (capability.Scope, error) {
	var scope capability.Scope
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&scope); err != nil {
		return capability.Scope{}, refusal(capability.ScopeDenied, name)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return capability.Scope{}, refusal(capability.ScopeDenied, name)
	}
	return scope, nil
}
func contextFailure(ctx context.Context, name string) error {
	var cause *capability.Error
	if errors.As(context.Cause(ctx), &cause) && cause != nil {
		copy := *cause
		copy.Capability = name
		return &copy
	}
	if ctx.Err() == context.DeadlineExceeded {
		return refusal(capability.DeadlineExceeded, name)
	}
	if ctx.Err() != nil {
		return refusal(capability.Cancelled, name)
	}
	return nil
}
func safeFailure(err error, name, requestID string) *capability.Error {
	var failure *capability.Error
	if errors.As(err, &failure) && failure != nil {
		copy := *failure
		copy.Capability = name
		copy.RequestID = requestID
		if payload, validation := copy.RPCData(); validation == nil {
			copy.EffectState = payload.EffectState
			return &copy
		}
	}
	return &capability.Error{Code: capability.InternalError, Capability: name, RequestID: requestID, EffectState: capability.NotStarted}
}
func (e *Enforcer) check(ctx context.Context, call Call) (Authority, time.Time, error) {
	if err := contextFailure(ctx, call.Capability); err != nil {
		return Authority{}, time.Time{}, err
	}
	if e == nil || e.Resolver == nil || e.Catalog == nil {
		return Authority{}, time.Time{}, refusal(capability.InternalError, call.Capability)
	}
	if call.Capability == "" || call.GrantID == "" || call.Operation == "" || call.Target == "" || call.RequestID == "" {
		return Authority{}, time.Time{}, refusal(capability.InvalidRequest, call.Capability)
	}
	auth, err := e.Resolver.Resolve(ctx, call)
	auth = cloneAuthority(auth)
	if err != nil {
		return auth, time.Time{}, err
	}
	if !auth.Authenticated || !identifier(auth.Actor.ID) || (!auth.Actor.valid() && auth.Actor.Kind != PluginActor) || auth.Owner.Validate() != nil || auth.Audience == "" {
		return auth, time.Time{}, refusal(capability.Unauthenticated, call.Capability)
	}
	if auth.Actor.Kind == PluginActor && auth.Actor.ID != auth.Owner.OwnerID {
		return auth, time.Time{}, refusal(capability.Unauthenticated, call.Capability)
	}
	if auth.InitiatingCaller != nil && (!auth.InitiatingCaller.valid() || auth.CallerPolicy == nil) {
		return auth, time.Time{}, refusal(capability.Unauthenticated, call.Capability)
	}
	if (!auth.Active && !(auth.ProvisionalLogging && call.Capability == capability.LogWrite && call.Operation == "host/log")) || !auth.TargetAvailable {
		return auth, time.Time{}, refusal(capability.TargetUnavailable, call.Capability)
	}
	if auth.LeaseContext == nil || auth.LeaseContext.Err() != nil {
		return auth, time.Time{}, refusal(capability.CapabilityDenied, call.Capability)
	}
	grant := auth.Grant
	if grant.Validate() != nil || grant.GrantID != call.GrantID || grant.Name != call.Capability {
		return auth, time.Time{}, refusal(capability.CapabilityDenied, call.Capability)
	}
	if grant.HostInstance != auth.Owner.HostInstance || grant.OwnerID != auth.Owner.OwnerID || grant.OwnerGeneration != auth.Owner.OwnerGeneration || grant.Audience != auth.Audience {
		return auth, time.Time{}, refusal(capability.Unauthenticated, call.Capability)
	}
	now := e.clock().Now()
	issued, err := time.Parse(time.RFC3339Nano, grant.IssuedAt)
	if err != nil {
		return auth, time.Time{}, refusal(capability.CapabilityDenied, call.Capability)
	}
	expires, err := time.Parse(time.RFC3339Nano, grant.ExpiresAt)
	if err != nil || now.Before(issued) || !now.Before(expires) {
		return auth, time.Time{}, refusal(capability.CapabilityDenied, call.Capability)
	}
	if auth.PolicyRevision == "" || auth.PolicyRevision != grant.PolicyRevision {
		return auth, time.Time{}, refusal(capability.CapabilityDenied, call.Capability)
	}
	// uint32 is checked before conversion for platforms with a 32-bit int.
	if uint64(grant.SchemaVersion) > uint64(^uint(0)>>1) {
		return auth, time.Time{}, refusal(capability.UnsupportedCapability, call.Capability)
	}
	descriptor, err := e.Catalog.Lookup(grant.Name, int(grant.SchemaVersion))
	if err != nil {
		return auth, time.Time{}, err
	}
	scope, err := decodeScope(grant.Name, grant.Scope)
	if err != nil {
		return auth, time.Time{}, err
	}
	scopes := []capability.Scope{scope, auth.Policy}
	callerPolicy := auth.Policy
	if auth.InitiatingCaller != nil {
		callerPolicy = *auth.CallerPolicy
		scopes = append(scopes, callerPolicy)
	}
	if auth.TransportScope == nil {
		return auth, time.Time{}, refusal(capability.CapabilityDenied, call.Capability)
	}
	scopes = append(scopes, *auth.TransportScope)
	for _, s := range scopes {
		if err := descriptor.ValidateScope(int(grant.SchemaVersion), s); err != nil {
			return auth, time.Time{}, err
		}
	}
	effective, err := capability.Intersect(grant.Name, scope, scope, auth.Policy, callerPolicy)
	if err != nil {
		return auth, time.Time{}, err
	}
	if err := capability.CheckNarrowing(grant.Name, scope, *auth.TransportScope); err != nil {
		return auth, time.Time{}, err
	}
	effective, err = capability.Intersect(grant.Name, effective, effective, effective, *auth.TransportScope)
	if err != nil {
		return auth, time.Time{}, err
	}
	if !effective.Allows("operations", call.Operation) || !effective.Allows("targets", call.Target) || !effective.Allows("effects", string(call.Effect)) {
		return auth, time.Time{}, refusal(capability.ScopeDenied, call.Capability)
	}
	for key := range call.Dimensions {
		if !slices.Contains(descriptor.ScopeSchema.Allowlists, key) || key == "operations" || key == "targets" || key == "effects" {
			return auth, time.Time{}, refusal(capability.InvalidRequest, call.Capability)
		}
	}
	for _, dimension := range descriptor.ScopeSchema.Allowlists {
		if dimension == "operations" || dimension == "targets" || dimension == "effects" {
			continue
		}
		if !effective.Allows(dimension, call.Dimensions[dimension]) {
			return auth, time.Time{}, refusal(capability.ScopeDenied, call.Capability)
		}
	}
	for key := range call.Usage {
		if !slices.Contains(descriptor.ScopeSchema.Limits, key) {
			return auth, time.Time{}, refusal(capability.InvalidRequest, call.Capability)
		}
	}
	for _, limit := range descriptor.ScopeSchema.Limits {
		amount, ok := call.Usage[limit]
		if !ok {
			return auth, time.Time{}, refusal(capability.InvalidRequest, call.Capability)
		}
		if limit == "deadline_ms" && amount <= 0 {
			return auth, time.Time{}, refusal(capability.DeadlineExceeded, call.Capability)
		}
		if !effective.Within(limit, amount) {
			return auth, time.Time{}, refusal(capability.BudgetExceeded, call.Capability)
		}
	}
	return auth, expires, nil
}

// Permit owns a reserved call and observes request/authority/expiry cancellation.
// Recheck after waiting and under the host's commit/admission guard. Close must
// be called on every path; it releases budget reservations exactly once.
type Permit struct {
	Context       context.Context
	e             *Enforcer
	call          Call
	authority     Authority
	cancel        context.CancelCauseFunc
	stopAuthority func() bool
	timer         Timer
	release       func()
	once          sync.Once
	deadline      time.Time
	deadlineCode  capability.Code
}

func (p *Permit) Close() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		p.cancel(refusal(capability.Cancelled, p.call.Capability))
		p.stopAuthority()
		p.timer.Stop()
		p.release()
	})
}
func (p *Permit) Recheck() error {
	if !p.e.clock().Now().Before(p.deadline) {
		return refusal(p.deadlineCode, p.call.Capability)
	}
	if err := contextFailure(p.Context, p.call.Capability); err != nil {
		return err
	}
	current, _, err := p.e.check(p.Context, p.call)
	if err != nil {
		return err
	}
	if current.Actor != p.authority.Actor || current.Owner != p.authority.Owner || current.Audience != p.authority.Audience || current.PolicyRevision != p.authority.PolicyRevision || !sameCaller(current.InitiatingCaller, p.authority.InitiatingCaller) {
		return refusal(capability.Unauthenticated, p.call.Capability)
	}
	return nil
}
func sameCaller(a, b *Subject) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
func cloneAuthority(a Authority) Authority {
	a.Grant.Scope = append(json.RawMessage(nil), a.Grant.Scope...)
	a.Policy = cloneScope(a.Policy)
	if a.InitiatingCaller != nil {
		c := *a.InitiatingCaller
		a.InitiatingCaller = &c
	}
	if a.CallerPolicy != nil {
		c := cloneScope(*a.CallerPolicy)
		a.CallerPolicy = &c
	}
	if a.TransportScope != nil {
		c := cloneScope(*a.TransportScope)
		a.TransportScope = &c
	}
	return a
}
func cloneCall(c Call) Call {
	dimensions := map[string]string{}
	for k, v := range c.Dimensions {
		dimensions[k] = v
	}
	c.Dimensions = dimensions
	usage := map[string]int64{}
	for k, v := range c.Usage {
		usage[k] = v
	}
	c.Usage = usage
	return c
}
func (e *Enforcer) record(ctx context.Context, call Call, auth Authority, start time.Time, err error, successState capability.EffectState) {
	if e == nil {
		return
	}
	if !auth.Authenticated {
		auth.Actor = Subject{}
		auth.InitiatingCaller = nil
	}
	event := AuditEvent{Timestamp: e.clock().Now(), TraceID: call.TraceID, RequestID: call.RequestID, Actor: Actor{Kind: string(auth.Actor.Kind), ID: auth.Actor.ID}, InitiatingCaller: auditActor(auth.InitiatingCaller), Owner: auth.Owner, Capability: call.Capability, GrantID: call.GrantID, PolicyRevision: auth.PolicyRevision, Target: call.Target, Server: call.Server, Tool: call.Tool, Effect: call.Effect, EffectState: successState, Duration: e.clock().Now().Sub(start)}
	if err != nil {
		failure := safeFailure(err, call.Capability, call.RequestID)
		event.Outcome = failure.Code
		event.EffectState = failure.EffectState
		event.Reason = failure.Detail
	}
	if err == nil {
		e.allowed.Add(1)
	} else {
		e.denied.Add(1)
	}
	e.Audit.Record(ctx, event)
}

// RequireCapability performs call-time checks and budget reservation before any
// application side effect. Use Run for automatic release and success/denial audit.
func (e *Enforcer) RequireCapability(ctx context.Context, call Call) (*Permit, error) {
	start := time.Now()
	if e != nil {
		start = e.clock().Now()
	}
	permit, auth, err := e.require(ctx, call)
	if err != nil {
		err = safeFailure(err, call.Capability, call.RequestID)
	}
	e.record(ctx, call, auth, start, err, capability.NotStarted)
	return permit, err
}
func (e *Enforcer) require(ctx context.Context, call Call) (*Permit, Authority, error) {
	call = cloneCall(call)
	auth, expires, err := e.check(ctx, call)
	if err != nil {
		return nil, auth, safeFailure(err, call.Capability, call.RequestID)
	}
	if e.Budget == nil {
		return nil, auth, refusal(capability.BudgetExceeded, call.Capability)
	}
	merged, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(auth.LeaseContext, func() { cancel(refusal(capability.CapabilityDenied, call.Capability)) })
	deadline := expires
	expiryCode := capability.CapabilityDenied
	if millis, ok := call.Usage["deadline_ms"]; ok {
		maxMillis := int64((time.Duration(1<<63 - 1)) / time.Millisecond)
		callDeadline := e.clock().Now().Add(time.Duration(min(millis, maxMillis)) * time.Millisecond)
		if callDeadline.Before(deadline) {
			deadline = callDeadline
			expiryCode = capability.DeadlineExceeded
		}
	}
	timer := e.clock().AfterFunc(deadline.Sub(e.clock().Now()), func() { cancel(refusal(expiryCode, call.Capability)) })
	release, err := e.Budget.Reserve(merged, auth, call)
	if err != nil || release == nil {
		cancel(refusal(capability.BudgetExceeded, call.Capability))
		stop()
		timer.Stop()
		if release != nil {
			release()
		}
		if err == nil {
			err = refusal(capability.BudgetExceeded, call.Capability)
		}
		return nil, auth, safeFailure(err, call.Capability, call.RequestID)
	}
	permit := &Permit{Context: merged, e: e, call: call, authority: auth, cancel: cancel, stopAuthority: stop, timer: timer, release: release, deadline: deadline, deadlineCode: expiryCode}
	if err := permit.Recheck(); err != nil {
		permit.Close()
		return nil, auth, safeFailure(err, call.Capability, call.RequestID)
	}
	return permit, auth, nil
}

func callbackFailure(err error, call Call) *capability.Error {
	var typed *capability.Error
	if errors.As(err, &typed) && typed != nil {
		if _, validation := typed.RPCData(); validation == nil {
			failure := safeFailure(err, call.Capability, call.RequestID)
			if failure.EffectState == capability.Unknown && call.Effect != capability.Read {
				failure.Code = capability.UnknownOutcome
			}
			return failure
		}
	}
	code := capability.InternalError
	if call.Effect != capability.Read {
		code = capability.UnknownOutcome
	}
	return &capability.Error{Code: code, Capability: call.Capability, RequestID: call.RequestID, EffectState: capability.Unknown}
}

// Run is the side-effect entry point for RPC/HTTP adapters. It always audits the
// decision/outcome and releases reservations, including panic/error paths. It
// preserves a definite successful commit despite a late cancellation; raw
// callback failures become safe internal errors with an unknown effect state.
func (e *Enforcer) Run(ctx context.Context, call Call, fn func(context.Context, *Permit) error) (err error) {
	start := time.Now()
	if e != nil {
		start = e.clock().Now()
	}
	var authority Authority
	executing := false
	defer func() {
		if recover() != nil {
			if executing {
				err = callbackFailure(errors.New("callback panic"), call)
			} else {
				err = refusal(capability.InternalError, call.Capability)
			}
		}
		if err != nil {
			err = safeFailure(err, call.Capability, call.RequestID)
		}
		e.record(ctx, call, authority, start, err, capability.Committed)
	}()
	if fn == nil {
		return refusal(capability.InvalidRequest, call.Capability)
	}
	permit, auth, err := e.require(ctx, call)
	authority = auth
	if err != nil {
		return err
	}
	defer permit.Close()
	authority = permit.authority
	executing = true
	if err := fn(permit.Context, permit); err != nil {
		return callbackFailure(err, call)
	}
	return nil
}

// Counters remain available even when telemetry persistence/presentation fails.
// Enforcer must not be copied after use. Counts reflect helper admission/outcome
// events, not downstream application records or a host's complete registry.
type Counters struct{ Allowed, Denied, AuditFailures uint64 }

func (e *Enforcer) Counters() Counters {
	if e == nil {
		return Counters{}
	}
	return Counters{e.allowed.Load(), e.denied.Load(), e.Audit.Failures()}
}

func auditActor(s *Subject) *Actor {
	if s == nil {
		return nil
	}
	return &Actor{Kind: string(s.Kind), ID: s.ID}
}
