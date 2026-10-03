package host

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hollis-labs/plugin-sdk/capability"
	"github.com/hollis-labs/plugin-sdk/internal/strictjson"
)

const (
	PluginActor SubjectKind = "plugin"
	UserActor   SubjectKind = "user"
)

func callerValid(s Subject) bool { return s.valid() || (s.Kind == UserActor && identifier(s.ID)) }

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
	Background              bool
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
// binding and returns a consistent immutable snapshot of current grant, tuple,
// policy and target state. It must be safe for concurrent calls.
// Request fields/headers cannot stand in for those host-verified facts. Resolve
// must honor ctx and return promptly; the library cannot stop an uncooperative
// host callback without leaving its work running.
type AuthorityResolver interface {
	Resolve(context.Context, Call) (Authority, error)
}

// Budget reserves cumulative/rate/concurrency budgets before effects. It must
// be atomic across calls, return a nonnil release function and not block without
// observing ctx. Reserve must roll back partial work if it panics before
// returning a release handle. Release must return promptly. Missing adapters
// fail closed even for byte-only limits.
type Budget interface {
	Reserve(context.Context, Authority, Call) (release func(), err error)
}

// EnforcerConfig fixes the host epoch and audience independently of resolver output.
type EnforcerConfig struct {
	HostInstance, Audience string
	Catalog                *capability.Catalog
	Resolver               AuthorityResolver
	Budget                 Budget
	Audit                  *Auditor
	Clock                  Clock
}

func NewEnforcer(cfg EnforcerConfig) (*Enforcer, error) {
	if !identifier(cfg.HostInstance) || !identifier(cfg.Audience) || cfg.Catalog == nil || cfg.Resolver == nil || cfg.Budget == nil {
		return nil, refusal(capability.InvalidRequest, "")
	}
	return &Enforcer{host: cfg.HostInstance, audience: cfg.Audience, Catalog: cfg.Catalog, Resolver: cfg.Resolver, Budget: cfg.Budget, Audit: cfg.Audit, Clock: cfg.Clock}, nil
}

type Enforcer struct {
	host, audience  string
	cleanupFailures atomic.Uint64
	Catalog         *capability.Catalog
	Resolver        AuthorityResolver
	Budget          Budget
	Audit           *Auditor
	Clock           Clock
	allowed         atomic.Uint64
	denied          atomic.Uint64
}

func (e *Enforcer) clock() Clock {
	if e.Clock != nil {
		return e.Clock
	}
	return wallClock{}
}

func decodeScope(name string, raw json.RawMessage) (capability.Scope, error) {
	var scope capability.Scope
	fields, err := strictjson.ObjectFields(raw, nil, []string{"allowlists", "limits"})
	if err != nil {
		return scope, refusal(capability.ScopeDenied, name)
	}
	if raw, ok := fields["allowlists"]; ok {
		if json.Unmarshal(raw, &scope.Allowlists) != nil {
			return capability.Scope{}, refusal(capability.ScopeDenied, name)
		}
	}
	if raw, ok := fields["limits"]; ok {
		if json.Unmarshal(raw, &scope.Limits) != nil {
			return capability.Scope{}, refusal(capability.ScopeDenied, name)
		}
	}
	return scope, nil
}
func contextFailure(ctx context.Context, name string) error {
	if ctx == nil {
		return refusal(capability.InvalidRequest, name)
	}
	if ctx.Err() == context.DeadlineExceeded {
		return refusal(capability.DeadlineExceeded, name)
	}
	if ctx.Err() != nil {
		return refusal(capability.Cancelled, name)
	}
	return nil
}
func staleBinding(name string) *capability.Error {
	return &capability.Error{Code: capability.TargetUnavailable, Capability: name, EffectState: capability.NotStarted, Detail: capability.StaleBinding}
}

// Admission failures are classified by our state, never by a caller's asserted outcome.
func admissionFailure(err error, name, requestID string) *capability.Error {
	failure := safeFailure(err, name, requestID)
	if failure.Code == capability.UnknownOutcome {
		failure.Code = capability.InternalError
	}
	failure.EffectState = capability.NotStarted
	return failure
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
func (e *Enforcer) check(ctx context.Context, call Call) (auth Authority, expiry time.Time, err error) {
	defer func() {
		if recover() != nil {
			err = refusal(capability.InternalError, call.Capability)
		}
	}()
	if err := contextFailure(ctx, call.Capability); err != nil {
		return Authority{}, time.Time{}, err
	}
	if e == nil || e.Resolver == nil || e.Catalog == nil || e.host == "" || e.audience == "" {
		return Authority{}, time.Time{}, refusal(capability.InternalError, call.Capability)
	}
	if call.Capability == "" || call.GrantID == "" || call.Operation == "" || call.Target == "" || call.RequestID == "" {
		return Authority{}, time.Time{}, refusal(capability.InvalidRequest, call.Capability)
	}
	auth, err = e.Resolver.Resolve(ctx, cloneCall(call))
	auth = cloneAuthority(auth)
	if err != nil {
		return auth, time.Time{}, err
	}
	if !auth.Authenticated || !identifier(auth.Actor.ID) || (!callerValid(auth.Actor) && auth.Actor.Kind != PluginActor) || auth.Owner.Validate() != nil || auth.Audience == "" {
		return auth, time.Time{}, refusal(capability.Unauthenticated, call.Capability)
	}
	if auth.Actor.Kind == PluginActor && auth.Actor.ID != auth.Owner.OwnerID {
		return auth, time.Time{}, refusal(capability.Unauthenticated, call.Capability)
	}
	if auth.Owner.HostInstance != e.host {
		return auth, time.Time{}, staleBinding(call.Capability)
	}
	if auth.Audience != e.audience {
		return auth, time.Time{}, refusal(capability.Unauthenticated, call.Capability)
	}
	if auth.Background {
		if auth.Actor.Kind != PluginActor {
			return auth, time.Time{}, refusal(capability.InvalidRequest, call.Capability)
		}
		if auth.InitiatingCaller != nil || auth.CallerPolicy != nil {
			return auth, time.Time{}, refusal(capability.InvalidRequest, call.Capability)
		}
	} else if auth.InitiatingCaller == nil || !callerValid(*auth.InitiatingCaller) || auth.CallerPolicy == nil {
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
	if grant.OwnerID != auth.Owner.OwnerID {
		return auth, time.Time{}, refusal(capability.CapabilityDenied, call.Capability)
	}
	if grant.HostInstance != auth.Owner.HostInstance || grant.OwnerGeneration != auth.Owner.OwnerGeneration {
		return auth, time.Time{}, staleBinding(call.Capability)
	}
	if grant.Audience != e.audience {
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
	if !slices.Contains(descriptor.Operations, call.Operation) {
		return auth, time.Time{}, refusal(capability.ScopeDenied, call.Capability)
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
// be called on every path; it attempts each cleanup callback once, isolates
// panic and reports cleanup failures separately. Close during Commit cancels
// work but retains its reservation until the callback actually returns.
type Permit struct {
	Context        context.Context
	requestContext context.Context
	e              *Enforcer
	call           Call
	authority      Authority
	cancel         context.CancelCauseFunc
	stopAuthority  func() bool
	timer          Timer
	release        func()
	once           sync.Once
	releaseOnce    sync.Once
	stateMu        sync.Mutex
	running        bool
	consumed       atomic.Bool
	closed         atomic.Bool
	deadline       time.Time
	deadlineCode   capability.Code
}

func (p *Permit) cleanup(fn func()) {
	defer func() {
		if recover() != nil && p.e != nil {
			p.e.cleanupFailures.Add(1)
		}
	}()
	fn()
}
func (p *Permit) Close() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		p.closed.Store(true)
		if p.cancel != nil {
			p.cleanup(func() { p.cancel(refusal(capability.Cancelled, p.call.Capability)) })
		}
		if p.stopAuthority != nil {
			p.cleanup(func() { p.stopAuthority() })
		}
		if p.timer != nil {
			p.cleanup(func() { p.timer.Stop() })
		}
		p.stateMu.Lock()
		running := p.running
		p.stateMu.Unlock()
		if !running {
			p.releaseBudget()
		}
	})
}
func (p *Permit) releaseBudget() {
	p.releaseOnce.Do(func() {
		if p.release != nil {
			p.cleanup(p.release)
		}
	})
}
func (p *Permit) finishCommit() {
	p.stateMu.Lock()
	p.running = false
	closed := p.closed.Load()
	p.stateMu.Unlock()
	if closed {
		p.releaseBudget()
	}
}
func (p *Permit) Recheck() (err error) {
	defer func() {
		if recover() != nil {
			if p != nil {
				p.Close()
			}
			err = refusal(capability.InternalError, "")
			if p != nil {
				err = admissionFailure(err, p.call.Capability, p.call.RequestID)
			}
		}
		if p != nil && err != nil {
			err = admissionFailure(err, p.call.Capability, p.call.RequestID)
		}
	}()
	if p == nil || p.e == nil || p.Context == nil || p.requestContext == nil {
		return refusal(capability.InvalidRequest, "")
	}
	if p.closed.Load() {
		return refusal(capability.Cancelled, p.call.Capability)
	}
	if !p.e.clock().Now().Before(p.deadline) {
		return refusal(p.deadlineCode, p.call.Capability)
	}
	if err := contextFailure(p.requestContext, p.call.Capability); err != nil {
		return err
	}
	current, _, err := p.e.check(p.requestContext, p.call)
	if err != nil {
		return admissionFailure(err, p.call.Capability, p.call.RequestID)
	}
	if current.Owner.OwnerID != p.authority.Owner.OwnerID {
		return refusal(capability.CapabilityDenied, p.call.Capability)
	}
	if current.Owner != p.authority.Owner {
		return staleBinding(p.call.Capability)
	}
	if current.Actor != p.authority.Actor || current.Audience != p.authority.Audience || current.PolicyRevision != p.authority.PolicyRevision || current.Background != p.authority.Background || !sameCaller(current.InitiatingCaller, p.authority.InitiatingCaller) {
		return refusal(capability.Unauthenticated, p.call.Capability)
	}
	if p.authority.LeaseContext.Err() != nil {
		return refusal(capability.CapabilityDenied, p.call.Capability)
	}
	if err := contextFailure(p.Context, p.call.Capability); err != nil {
		return err
	}
	return nil
}

// Commit consumes this permit once, rechecks current authority and releases on
// every path. Call it under the host's commit guard after any wait. A failed
// attempt cannot be retried through this permit.
func (p *Permit) Commit(fn func(context.Context) error) (err error) {
	if p == nil || p.e == nil || p.Context == nil {
		return refusal(capability.InvalidRequest, "")
	}
	defer func() {
		if err != nil {
			err = safeFailure(err, p.call.Capability, p.call.RequestID)
		}
	}()
	if !p.consumed.CompareAndSwap(false, true) {
		return refusal(capability.Conflict, p.call.Capability)
	}
	p.stateMu.Lock()
	if p.closed.Load() {
		p.stateMu.Unlock()
		return refusal(capability.Cancelled, p.call.Capability)
	}
	p.running = true
	p.stateMu.Unlock()
	defer p.finishCommit()
	defer p.Close()
	executing := false
	defer func() {
		if recover() != nil {
			if executing {
				err = callbackFailure(errors.New("callback panic"), p.call)
			} else {
				err = refusal(capability.InternalError, p.call.Capability)
			}
		}
	}()
	if fn == nil {
		return refusal(capability.InvalidRequest, p.call.Capability)
	}
	if err := p.Recheck(); err != nil {
		return admissionFailure(err, p.call.Capability, p.call.RequestID)
	}
	executing = true
	if err := fn(p.Context); err != nil {
		return callbackFailure(err, p.call)
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
func (e *Enforcer) auditTime() (now time.Time) {
	now = time.Now()
	defer func() {
		if recover() != nil && e.Audit != nil {
			e.Audit.failures.Add(1)
		}
	}()
	now = e.clock().Now()
	return now
}
func (e *Enforcer) record(ctx context.Context, call Call, auth Authority, start time.Time, err error, successState capability.EffectState) {
	defer func() {
		if recover() != nil && e != nil && e.Audit != nil {
			e.Audit.failures.Add(1)
		}
	}()
	if e == nil {
		return
	}
	if !auth.Authenticated {
		auth.Actor = Subject{}
		auth.InitiatingCaller = nil
	}
	now := e.auditTime()
	event := AuditEvent{Timestamp: now, TraceID: call.TraceID, RequestID: call.RequestID, Actor: Actor{Kind: string(auth.Actor.Kind), ID: auth.Actor.ID}, InitiatingCaller: auditActor(auth.InitiatingCaller), Owner: auth.Owner, Capability: call.Capability, GrantID: call.GrantID, PolicyRevision: auth.PolicyRevision, Target: call.Target, Server: call.Server, Tool: call.Tool, Effect: call.Effect, EffectState: successState, Duration: max(time.Duration(0), now.Sub(start))}
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
	if ctx == nil {
		ctx = context.Background()
	}
	e.Audit.Record(context.WithoutCancel(ctx), sanitizeAudit(event))
}

// RequireCapability performs call-time checks and budget reservation before any
// application side effect. Use Run for automatic release and success/denial audit.
func (e *Enforcer) RequireCapability(ctx context.Context, call Call) (permit *Permit, err error) {
	start := time.Now()
	var auth Authority
	defer func() {
		if recover() != nil {
			if permit != nil {
				permit.Close()
				permit = nil
			}
			err = refusal(capability.InternalError, call.Capability)
		}
		if err != nil {
			err = admissionFailure(err, call.Capability, call.RequestID)
		}
		e.record(ctx, call, auth, start, err, capability.NotStarted)
	}()
	if e != nil {
		start = e.clock().Now()
	}
	permit, auth, err = e.require(ctx, call)
	return permit, err
}
func (e *Enforcer) require(ctx context.Context, call Call) (permit *Permit, auth Authority, err error) {
	var pending *Permit
	defer func() {
		if recover() != nil {
			err = refusal(capability.InternalError, call.Capability)
		}
		if err != nil {
			if pending != nil {
				pending.Close()
			}
			permit = nil
			err = admissionFailure(err, call.Capability, call.RequestID)
		}
	}()
	call = cloneCall(call)
	auth, expires, err := e.check(ctx, call)
	if err != nil {
		return nil, auth, err
	}
	if e.Budget == nil {
		return nil, auth, refusal(capability.BudgetExceeded, call.Capability)
	}
	pending = &Permit{e: e, requestContext: ctx, call: call, authority: auth, deadline: expires, deadlineCode: capability.CapabilityDenied}
	pending.Context, pending.cancel = context.WithCancelCause(ctx)
	pending.stopAuthority = context.AfterFunc(auth.LeaseContext, func() { pending.cancel(refusal(capability.CapabilityDenied, call.Capability)) })
	if millis, ok := call.Usage["deadline_ms"]; ok {
		maxMillis := int64(time.Duration(1<<63-1) / time.Millisecond)
		deadline := e.clock().Now().Add(time.Duration(min(millis, maxMillis)) * time.Millisecond)
		if deadline.Before(pending.deadline) {
			pending.deadline = deadline
			pending.deadlineCode = capability.DeadlineExceeded
		}
	}
	pending.timer = e.clock().AfterFunc(pending.deadline.Sub(e.clock().Now()), func() { pending.cancel(refusal(pending.deadlineCode, call.Capability)) })
	pending.release, err = e.Budget.Reserve(pending.Context, cloneAuthority(auth), cloneCall(call))
	if err != nil {
		return nil, auth, err
	}
	if pending.release == nil {
		return nil, auth, refusal(capability.BudgetExceeded, call.Capability)
	}
	if err := pending.Recheck(); err != nil {
		return nil, auth, err
	}
	return pending, auth, nil
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
	var auth Authority
	var permit *Permit
	defer func() {
		if recover() != nil {
			err = refusal(capability.InternalError, call.Capability)
		}
		if permit != nil {
			permit.Close()
		}
		if err != nil {
			err = safeFailure(err, call.Capability, call.RequestID)
		}
		e.record(ctx, call, auth, start, err, capability.Committed)
	}()
	if e != nil {
		start = e.clock().Now()
	}
	if fn == nil {
		return refusal(capability.InvalidRequest, call.Capability)
	}
	permit, auth, err = e.require(ctx, call)
	if err != nil {
		return err
	}
	return permit.Commit(func(ctx context.Context) error { return fn(ctx, permit) })
}

// Counters remain available even when telemetry persistence/presentation fails.
// Enforcer must not be copied after use. Counts reflect helper admission/outcome
// events, not downstream application records or a host's complete registry.
type Counters struct{ Allowed, Denied, AuditFailures, CleanupFailures uint64 }

func (e *Enforcer) Counters() Counters {
	if e == nil {
		return Counters{}
	}
	return Counters{Allowed: e.allowed.Load(), Denied: e.denied.Load(), AuditFailures: e.Audit.Failures(), CleanupFailures: e.cleanupFailures.Load()}
}

func auditActor(s *Subject) *Actor {
	if s == nil {
		return nil
	}
	return &Actor{Kind: string(s.Kind), ID: s.ID}
}
