package scheduler

import (
	"context"
	"log/slog"
	"time"
)

// MetricObservation is one passive dispatch measurement. IDs identify an
// occurrence; applications choose their own metric names, units and labels.
// Payload and arbitrary error text are deliberately omitted.
type MetricObservation struct {
	Kind         ObserverEventKind
	At           time.Time
	FireID       string
	ScheduleID   string
	JobType      string
	Attempt      int
	Lag          time.Duration
	Duration     time.Duration
	MisfireCount int
	Reason       string
}

// MetricsRecorder receives lifecycle measurements without requiring a metrics
// library. Implementations must support concurrent calls when dispatch workers
// run concurrently and should bound their own I/O latency.
type MetricsRecorder interface {
	Record(context.Context, MetricObservation) error
}

// MetricsRecorderFunc adapts a function to MetricsRecorder.
type MetricsRecorderFunc func(context.Context, MetricObservation) error

// Record calls f.
func (f MetricsRecorderFunc) Record(ctx context.Context, observation MetricObservation) error {
	return f(ctx, observation)
}

// MetricsObserver projects lifecycle events into dependency-free measurements.
// Install it with WithObserver. Errors and panics are isolated by the Engine's
// observer boundary; direct Observe calls return recorder errors to the caller.
type MetricsObserver struct{ recorder MetricsRecorder }

// NewMetricsObserver returns an observer. A nil recorder disables measurement.
func NewMetricsObserver(recorder MetricsRecorder) *MetricsObserver {
	return &MetricsObserver{recorder: recorder}
}

// Observe records started, durably succeeded/failed, skipped and misfired fires.
// A failure is a failed Enqueue whose retry/exhaustion transition committed;
// retry and exhaustion events are not counted again. Durable success means
// dispatch acceptance, not completion of the application's job.
func (o *MetricsObserver) Observe(ctx context.Context, event ObserverEvent) error {
	if o == nil || o.recorder == nil {
		return nil
	}
	switch event.Kind {
	case ObserverFire, ObserverSuccess, ObserverFailure, ObserverSkip, ObserverMisfire:
	default:
		return nil
	}
	return o.recorder.Record(ctx, MetricObservation{
		Kind: event.Kind, At: event.At, FireID: event.Fire.ID,
		ScheduleID: event.Fire.ScheduleID, JobType: event.Fire.JobType,
		Attempt: event.Fire.Attempt, Lag: nonnegativeDuration(event.Lag),
		Duration: nonnegativeDuration(event.Duration), MisfireCount: max(event.MisfireCount, 0),
		Reason: event.Reason,
	})
}

func nonnegativeDuration(value time.Duration) time.Duration {
	if value < 0 {
		return 0
	}
	return value
}

// SlogObserver writes neutral lifecycle facts without opaque payloads or
// arbitrary runner/store error text. Use the logged operation and has_error
// fields to correlate detailed errors logged by the owning application.
type SlogObserver struct{ logger *slog.Logger }

// NewSlogObserver returns a structured logger adapter. A nil logger disables it.
func NewSlogObserver(logger *slog.Logger) *SlogObserver { return &SlogObserver{logger: logger} }

// Observe logs every event at info level, or warning level for failures and
// engine errors. Logging remains passive; slog handler failures do not alter
// scheduling. Handler concurrency and latency follow slog's Handler contract.
func (o *SlogObserver) Observe(ctx context.Context, event ObserverEvent) error {
	if o == nil || o.logger == nil {
		return nil
	}
	level := slog.LevelInfo
	if event.Kind == ObserverFailure || event.Kind == ObserverEngineError {
		level = slog.LevelWarn
	}
	o.logger.LogAttrs(ctx, level, "scheduler event",
		slog.String("kind", string(event.Kind)), slog.Time("at", event.At),
		slog.String("fire_id", event.Fire.ID), slog.String("schedule_id", event.Fire.ScheduleID),
		slog.String("job_type", event.Fire.JobType), slog.Int("attempt", event.Fire.Attempt),
		slog.String("status", string(event.Fire.Status)), slog.String("reason", event.Reason),
		slog.String("operation", event.Operation), slog.Bool("has_error", event.Err != nil),
		slog.Duration("lag", nonnegativeDuration(event.Lag)),
		slog.Duration("duration", nonnegativeDuration(event.Duration)),
		slog.Int("misfire_count", max(event.MisfireCount, 0)),
	)
	return nil
}
