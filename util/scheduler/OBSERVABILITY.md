# Scheduler observation

Install an `Observer` with `WithObserver`. It runs synchronously and may be
called concurrently by dispatch workers. Keep recorder and handler calls
bounded, and synchronize application-owned state. The engine isolates observer
errors and panics from dispatch and durable transitions. Passive does not mean
asynchronous: a blocking callback can still delay the calling worker.

## Metrics without a metrics dependency

`NewMetricsObserver` accepts a `MetricsRecorder`; `MetricsRecorderFunc` adapts
an application function. The library adds no metrics backend or global registry.
Each selected event produces a `MetricObservation`:

| Kind | Measurement meaning |
| --- | --- |
| `fire` | A claimed attempt is about to call `Runner.Enqueue`. |
| `success` | Enqueue accepted and the durable success transition committed. |
| `failure` | Enqueue failed and its durable retry or exhaustion transition committed. |
| `skip` | Dispatch or materialization was skipped; inspect `Reason`. |
| `misfire` | A winning materialization CAS committed a misfire policy decision. |

Count `failure` directly. Counting `retry` or `exhaustion` too would count the
same failed attempt twice. A transition error does not produce a committed
success/failure measurement; it remains an `engine_error` lifecycle event.
Successful dispatch is acceptance by the runner, not completion of the job.
A recovered ambiguous claim can produce another `fire` event for the same
attempt. Observer calls are not a durable telemetry outbox.

`Lag` measures nonnegative Enqueue-start time minus the scheduled occurrence time.
`Duration` measures the elapsed Enqueue call using the engine's clock; it is not
job execution duration. Record lag and duration distributions from outcome events; start events
do not carry completed dispatch timing measurements. `MisfireCount` is the bounded count on that
committed policy decision, not an estimate of all historical missed work.
Application exporters choose metric names and units (for example seconds).

```go
observer := scheduler.NewMetricsObserver(scheduler.MetricsRecorderFunc(
    func(ctx context.Context, observation scheduler.MetricObservation) error {
        // Update your own concurrency-safe counters/histograms here.
        // Prefer low-cardinality kind/job-type labels; IDs are correlation data.
        return nil
    },
))
engine := scheduler.New(store, runner, scheduler.WithObserver(observer))
```

The observation excludes payload and arbitrary error text. Fire and schedule
IDs are available for correlation, but using them as metric labels usually
creates unbounded cardinality. Labels and export policy belong to the app.
A nil recorder disables measurement. Direct `Observe` calls return recorder
errors; engine isolation applies when installed through `WithObserver`.

## Structured logs

`NewSlogObserver(logger)` records all lifecycle events using standard-library
`slog`. It logs failures and engine errors at warning level and other events at
info level. Fields include kind, time, fire/schedule identity, job type, attempt,
status, reason, operation, lag, duration, misfire count and `has_error`.

Opaque payload, `Fire.LastError` and arbitrary runner/store error text are
omitted. Applications can correlate operation/identity with their own detailed
error logging and redaction policy. A nil logger disables this adapter.

```go
observer := scheduler.NewSlogObserver(logger)
engine := scheduler.New(store, runner, scheduler.WithObserver(observer))
```

`WithObserver` selects one observer. To send to both adapters, compose them in
an `ObserverFunc` and call each, joining their returned errors; do not apply two
`WithObserver` options and expect fanout. Handlers/recorders must support the
worker concurrency configured by the application.

## Platform evidence

The dedicated repository workflow runs native scheduler builds, vet and race
checks (including SQLite store conformance) on Linux and macOS. Windows runs
the same commands as an advisory job: its result is visible and does not assert
Windows readiness. The existing util workflow is the full Linux module gate.
Cross-compilation alone is not a native platform execution result.
