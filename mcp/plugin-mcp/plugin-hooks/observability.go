package pluginhooks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"time"
)

// Clock controls breaker cooldown and telemetry timestamps. It must be bounded,
// concurrency-safe and monotonic. Execution deadlines still use context timers.
type Clock interface{ Now() time.Time }
type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// Sink receives detached value records, never payloads, arbitrary invocation
// metadata or error messages. Implementations must be bounded and concurrency-safe.
// Calls occur outside registry, engine and breaker locks and may arrive out of
// order; transition Sequence identifies their serialized state order.
type Sink interface {
	Record(TraceRecord)
	BreakerChanged(BreakerEvent)
}

// TraceRecord describes one completed dispatch or terminal handler attempt.
// HandlerCount is populated on dispatch records; one handler record is one
// attempt, including unavailable/skipped attempts. Times are milliseconds.
type TraceRecord struct {
	Stage                string       `json:"stage"`
	Started              bool         `json:"started"`
	StartedAt            time.Time    `json:"started_at"`
	EndedAt              time.Time    `json:"ended_at"`
	TraceID              string       `json:"trace_id"`
	SpanID               string       `json:"span_id"`
	ParentSpanID         string       `json:"parent_span_id,omitempty"`
	InvocationID         string       `json:"invocation_id"`
	Hook                 string       `json:"hook_name"`
	CatalogVersion       string       `json:"catalog_version"`
	SchemaDigest         string       `json:"schema_digest"`
	HostInstance         string       `json:"host_instance"`
	Owner                string       `json:"owner_id,omitempty"`
	Generation           string       `json:"owner_generation,omitempty"`
	Registration         string       `json:"registration_name,omitempty"`
	HandleID             uint64       `json:"handle_id,omitempty"`
	RegistrationSequence uint64       `json:"registration_sequence,omitempty"`
	Priority             int          `json:"priority"`
	Mode                 Mode         `json:"mode"`
	Depth                int          `json:"depth"`
	Transport            string       `json:"transport,omitempty"`
	QueueMS              float64      `json:"queue_ms"`
	DurationMS           float64      `json:"duration_ms"`
	EffectiveTimeoutMS   float64      `json:"effective_timeout_ms"`
	OnError              ErrorPolicy  `json:"on_error,omitempty"`
	Outcome              string       `json:"outcome"`
	ErrorClass           string       `json:"error_class"`
	BreakerState         BreakerState `json:"breaker_state,omitempty"`
	HandlerCount         int          `json:"handler_count"`
}

// TraceContext carries only validated tracing identifiers, not baggage or
// application metadata. SpanID is the parent for the next dispatch span.
type TraceContext struct{ TraceID, SpanID string }
type traceContextKey struct{}

var traceIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var spanIDPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

// WithTraceContext installs an incoming host-verified tracing parent. Plugins
// cannot supply authentication or lower the engine's depth through trace data.
func WithTraceContext(ctx context.Context, parent TraceContext) (context.Context, error) {
	if !traceIDPattern.MatchString(parent.TraceID) || !spanIDPattern.MatchString(parent.SpanID) || parent.TraceID == "00000000000000000000000000000000" || parent.SpanID == "0000000000000000" {
		return nil, ErrInvalidOptions
	}
	return context.WithValue(ctx, traceContextKey{}, parent), nil
}

// TraceContextFrom returns the immutable tracing parent carried in ctx.
func TraceContextFrom(ctx context.Context) (TraceContext, bool) {
	parent, ok := ctx.Value(traceContextKey{}).(TraceContext)
	return parent, ok
}
func (e *Engine) traceIdentifiers(ctx context.Context) (string, string, string) {
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", e.registry.hostInstance, e.traceIDs.Add(1))))
	span := hex.EncodeToString(hash[16:24])
	if parent, ok := TraceContextFrom(ctx); ok {
		return parent.TraceID, span, parent.SpanID
	}
	return hex.EncodeToString(hash[:16]), span, ""
}
func milliseconds(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
func elapsed(now, start time.Time) float64 {
	if now.Before(start) {
		return 0
	}
	return milliseconds(now.Sub(start))
}
func (e *Engine) dispatchRecord(ctx context.Context, hook string) TraceRecord {
	trace, span, parent := e.traceIdentifiers(ctx)
	depth, _ := ctx.Value(depthKey{}).(int)
	record := TraceRecord{StartedAt: e.config.Clock.Now(), InvocationID: fmt.Sprintf("inv-%d", e.ids.Add(1)), Stage: "dispatch", TraceID: trace, SpanID: span, ParentSpanID: parent, Hook: hook, CatalogVersion: e.registry.catalogVersion, HostInstance: e.registry.hostInstance, Depth: depth + 1}
	if def, ok := e.registry.definitions[hook]; ok {
		record.Mode = def.Mode
		record.SchemaDigest = def.SchemaDigest
		record.EffectiveTimeoutMS = milliseconds(def.Budget)
	}
	return record
}
func (e *Engine) record(r TraceRecord) {
	if e.config.Sink == nil {
		return
	}
	e.observe(func() { e.config.Sink.Record(r) })
}
func (e *Engine) breakerEvent(event *BreakerEvent) {
	if event == nil || e.config.Sink == nil {
		return
	}
	value := *event
	e.observe(func() { e.config.Sink.BreakerChanged(value) })
}
func (e *Engine) observe(f func()) {
	defer func() {
		if recover() != nil {
			e.telemetryFailures.Add(1)
		}
	}()
	f()
}

// TelemetryFailures counts panics recovered from host sink callbacks. A panic
// never changes dispatch/breaker behavior. Hosts should monitor this counter.
func (e *Engine) TelemetryFailures() uint64 { return e.telemetryFailures.Load() }

func (e *Engine) finishDispatch(d *dispatch, result DispatchResult, err error) {
	record := d.trace
	record.EndedAt = e.config.Clock.Now()
	record.DurationMS = elapsed(record.EndedAt, d.beganAt)
	record.Outcome = string(result.Status)
	record.ErrorClass = classify(err)
	record.HandlerCount = len(result.Outcomes)
	e.record(record)
}
func (e *Engine) rejectDispatch(ctx context.Context, hook string, err error) (DispatchResult, error) {
	record := e.dispatchRecord(ctx, hook)
	result := dispatchFailure(err)
	result.InvocationID = record.InvocationID
	record.Outcome = string(result.Status)
	record.ErrorClass = classify(err)
	record.EndedAt = e.config.Clock.Now()
	record.DurationMS = elapsed(record.EndedAt, record.StartedAt)
	e.record(record)
	return result, err
}
