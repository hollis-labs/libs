package host

import (
	"context"
	"sync/atomic"
	"time"

	cap "github.com/hollis-labs/plugin-sdk/capability"
)

// AuditEvent carries only authorization metadata. There is intentionally no
// arguments, content, arbitrary details, error message or bearer token field.
// Actor/caller/target identifiers must be host-verified safe identifiers.
type AuditEvent struct {
	Timestamp                           time.Time
	TraceID, RequestID                  string
	Actor                               Subject
	InitiatingCaller                    *Subject
	Owner                               cap.RuntimeIdentity
	Capability, GrantID, PolicyRevision string
	Target, Server, Tool                string
	Effect                              cap.Effect
	Outcome                             cap.Code // Empty means success.
	EffectState                         cap.EffectState
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
}

func NewAuditor(sink AuditSink) *Auditor { return &Auditor{sink: sink} }
func (a *Auditor) Record(ctx context.Context, event AuditEvent) (ok bool) {
	if a == nil || a.sink == nil {
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
	if err := a.sink.Record(ctx, event); err != nil {
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
