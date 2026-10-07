package host

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/capability"
)

// Actor identifies a host-verified audit subject, including plugins.
type Actor struct{ Kind, ID string }

// AuditEvent carries only authorization metadata. There is intentionally no
// arguments, content, arbitrary details, error message or bearer token field.
// Actor/caller/target identifiers must be host-verified safe identifiers.
type AuditEvent struct {
	Timestamp                           time.Time
	TraceID, RequestID                  string
	Actor                               Actor
	InitiatingCaller                    *Actor
	Owner                               capability.RuntimeIdentity
	Capability, GrantID, PolicyRevision string
	Target, Server, Tool                string
	Effect                              capability.Effect
	Outcome                             capability.Code // Empty means success.
	Reason                              capability.FailureDetail
	EffectState                         capability.EffectState
	Duration                            time.Duration
}

// AuditSink must return promptly and honor cancellation. The host chooses
// bounded buffering/retention; the library creates no unbounded telemetry queue.
type AuditSink interface {
	Record(context.Context, AuditEvent) error
}

// Auditor isolates telemetry failure from enforcement decisions. The returned
// bool and failure count report sink failure separately, without exposing the
// sink's raw error. Presentations may read Failures after their sink recovers.
type Auditor struct {
	sink     AuditSink
	failures atomic.Uint64
	denials  sync.Map
}

func NewAuditor(sink AuditSink) *Auditor { return &Auditor{sink: sink} }
func (a *Auditor) Record(ctx context.Context, event AuditEvent) (ok bool) {
	if a == nil {
		return true
	}
	if event.Outcome != "" {
		key := denialKey(event.Outcome, event.Reason)
		event.Outcome = key.Code
		event.Reason = key.Reason
		counter, _ := a.denials.LoadOrStore(key, new(atomic.Uint64))
		counter.(*atomic.Uint64).Add(1)
	}
	if a.sink == nil {
		return true
	}
	if event.InitiatingCaller != nil {
		copy := *event.InitiatingCaller
		event.InitiatingCaller = &copy
	}
	defer func() {
		if recover() != nil {
			a.failures.Add(1)
			ok = false
		}
	}()
	if err := a.sink.Record(ctx, sanitizeAudit(event)); err != nil {
		a.failures.Add(1)
		return false
	}
	return true
}
func (a *Auditor) Failures() uint64 {
	if a == nil {
		return 0
	}
	return a.failures.Load()
}

// DenialKey has a bounded vocabulary so malformed telemetry cannot allocate
// counters per arbitrary user-supplied string.
type DenialKey struct {
	Code   capability.Code
	Reason capability.FailureDetail
}

func denialKey(code capability.Code, reason capability.FailureDetail) DenialKey {
	switch code {
	case capability.InvalidRequest, capability.Unauthenticated, capability.CapabilityDenied, capability.ScopeDenied, capability.UnsupportedCapability, capability.TargetUnavailable, capability.BudgetExceeded, capability.RateLimited, capability.Cancelled, capability.DeadlineExceeded, capability.UnknownOutcome, capability.InternalError, capability.Conflict:
	default:
		code = capability.InternalError
	}
	switch reason {
	case "", capability.StaleBinding, capability.CallbackCycle, capability.DepthExceeded, capability.ParentInvalid, capability.ParentTerminal:
	default:
		reason = ""
	}
	return DenialKey{code, reason}
}

// Denials returns a copy of per-code/reason counts, independent of sink success.
func (a *Auditor) Denials() map[DenialKey]uint64 {
	out := map[DenialKey]uint64{}
	if a == nil {
		return out
	}
	a.denials.Range(func(key, value any) bool { out[key.(DenialKey)] = value.(*atomic.Uint64).Load(); return true })
	return out
}

// MaxAuditIdentifierBytes caps every untrusted identifier copied to telemetry.
const MaxAuditIdentifierBytes = 256

// The wildcard is forbidden by identifier, so it cannot alias a real identifier.
const invalidAuditIdentifier = "*"

func auditIdentifier(value string) string {
	if value == "" {
		return ""
	}
	if len(value) > MaxAuditIdentifierBytes || !identifier(value) || strings.ContainsFunc(value, func(r rune) bool { return unicode.Is(unicode.Cf, r) }) {
		return invalidAuditIdentifier
	}
	return value
}
func sanitizeAudit(e AuditEvent) AuditEvent {
	e.TraceID = auditIdentifier(e.TraceID)
	e.RequestID = auditIdentifier(e.RequestID)
	e.Actor = Actor{auditIdentifier(e.Actor.Kind), auditIdentifier(e.Actor.ID)}
	if e.InitiatingCaller != nil {
		e.InitiatingCaller = &Actor{auditIdentifier(e.InitiatingCaller.Kind), auditIdentifier(e.InitiatingCaller.ID)}
	}
	e.Owner.HostInstance = auditIdentifier(e.Owner.HostInstance)
	e.Owner.OwnerID = auditIdentifier(e.Owner.OwnerID)
	if e.Owner.OwnerGeneration > capability.MaxSafeInteger {
		e.Owner.OwnerGeneration = 0
	}
	e.Capability = auditIdentifier(e.Capability)
	e.GrantID = auditIdentifier(e.GrantID)
	e.PolicyRevision = auditIdentifier(e.PolicyRevision)
	e.Target = auditIdentifier(e.Target)
	e.Server = auditIdentifier(e.Server)
	e.Tool = auditIdentifier(e.Tool)
	switch e.Effect {
	case capability.Read, capability.Write, capability.Destructive:
	default:
		e.Effect = ""
	}
	return e
}
