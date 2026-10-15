package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMetricsRecorderErrorAndPanicCannotChangeDurableDispatch(t *testing.T) {
	for _, failure := range []string{"error", "panic"} {
		t.Run(failure, func(t *testing.T) {
			now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			store := newFakeStore(Schedule{ID: "scheduled", NextRun: now, Enabled: true})
			runner := &fakeRunner{}
			observer := NewMetricsObserver(MetricsRecorderFunc(func(context.Context, MetricObservation) error {
				if failure == "panic" {
					panic("metrics unavailable")
				}
				return errors.New("metrics unavailable")
			}))
			engine := New(store, runner, WithObserver(observer), WithClock(newFakeClock(now)))
			if err := engine.TickNow(context.Background()); err != nil {
				t.Fatal(err)
			}
			fire := store.fires[DeriveFireID("scheduled", now)]
			if fire.Status != FireSucceeded || len(runner.jobs) != 1 {
				t.Fatalf("telemetry changed durable dispatch: status=%s jobs=%d", fire.Status, len(runner.jobs))
			}
			if engine.Status().ObserverErrors == 0 {
				t.Fatal("observer failure not accounted")
			}
		})
	}
}

func TestMetricsEngineMeasuresOnlyCommittedDispatchOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name          string
		enqueueErr    error
		maxAttempts   int
		transitionErr error
		wantKind      ObserverEventKind
		wantStatus    FireStatus
	}{
		{name: "accepted", maxAttempts: 3, wantKind: ObserverSuccess, wantStatus: FireSucceeded},
		{name: "retry", enqueueErr: errors.New("enqueue failed"), maxAttempts: 3, wantKind: ObserverFailure, wantStatus: FireRetrying},
		{name: "exhausted", enqueueErr: errors.New("enqueue failed"), maxAttempts: 1, wantKind: ObserverFailure, wantStatus: FireExhausted},
		{name: "transition_failed", enqueueErr: errors.New("enqueue failed"), maxAttempts: 3, transitionErr: errors.New("persistence unavailable"), wantStatus: FireClaimed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			due := now.Add(-7 * time.Second)
			store := newFakeStore(Schedule{ID: "scheduled", NextRun: due, Enabled: true, Retry: RetryPolicy{MaxAttempts: tc.maxAttempts}})
			store.transitionErr = tc.transitionErr
			clock := newFakeClock(now)
			runner := runnerFunc(func(context.Context, Job) error { clock.set(now.Add(3 * time.Second)); return tc.enqueueErr })
			var samples []MetricObservation
			observer := NewMetricsObserver(MetricsRecorderFunc(func(_ context.Context, m MetricObservation) error { samples = append(samples, m); return nil }))
			engine := New(store, runner, WithObserver(observer), WithClock(clock))
			if err := engine.TickNow(context.Background()); err != nil {
				t.Fatal(err)
			}
			fire := store.fires[DeriveFireID("scheduled", due)]
			if fire.Status != tc.wantStatus {
				t.Fatalf("durable status=%s,want%s", fire.Status, tc.wantStatus)
			}
			if tc.wantKind == "" {
				if len(samples) != 1 || samples[0].Kind != ObserverFire {
					t.Fatalf("uncommitted outcome measured: %#v", samples)
				}
				return
			}
			if len(samples) != 2 || samples[0].Kind != ObserverFire || samples[1].Kind != tc.wantKind {
				t.Fatalf("measured sequence=%#v", samples)
			}
			result := samples[1]
			if result.Duration != 3*time.Second || result.Lag != 7*time.Second || !result.At.Equal(now.Add(3*time.Second)) {
				t.Fatalf("wrong dispatch timing: %#v", result)
			}
		})
	}
}

func TestMetricsEngineReportsCommittedMisfire(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	due := now.Add(-2 * time.Minute)
	store := newFakeStore(Schedule{ID: "missed", NextRun: due, Enabled: true, Misfire: MisfireSkip})
	runner := &fakeRunner{}
	var samples []MetricObservation
	observer := NewMetricsObserver(MetricsRecorderFunc(func(_ context.Context, m MetricObservation) error { samples = append(samples, m); return nil }))
	engine := New(store, runner, WithClock(newFakeClock(now)), WithObserver(observer))
	if err := engine.TickNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.jobs) != 0 {
		t.Fatal("skipped misfire dispatched")
	}
	if len(samples) != 2 || samples[0].Kind != ObserverMisfire || samples[0].MisfireCount != 1 || samples[1].Kind != ObserverSkip {
		t.Fatalf("misfire measurements=%#v", samples)
	}
	fire := store.fires[DeriveFireID("missed", due)]
	if fire.Status != FireSkipped {
		t.Fatalf("misfire was not durably skipped: %#v", fire)
	}
}
