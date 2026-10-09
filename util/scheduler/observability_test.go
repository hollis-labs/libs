package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMetricsProjectsLifecycleWithoutDuplicateFailure(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var got []MetricObservation
	observer := NewMetricsObserver(MetricsRecorderFunc(func(_ context.Context, m MetricObservation) error { got = append(got, m); return nil }))
	for _, kind := range []ObserverEventKind{ObserverClaim, ObserverFire, ObserverFailure, ObserverRetry, ObserverFailure, ObserverExhaustion, ObserverSuccess, ObserverSkip, ObserverMisfire, ObserverDisable, ObserverEngineError} {
		event := ObserverEvent{Kind: kind, At: at, Fire: Fire{ID: "fire-id", ScheduleID: "schedule-id", JobType: "job", Attempt: 2, Payload: []byte("private payload"), LastError: "private error"}, Duration: 3 * time.Second, Lag: 5 * time.Second, MisfireCount: 4, Reason: "coalesced", Err: errors.New("private error")}
		if err := observer.Observe(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	wantKinds := []ObserverEventKind{ObserverFire, ObserverFailure, ObserverFailure, ObserverSuccess, ObserverSkip, ObserverMisfire}
	kinds := make([]ObserverEventKind, len(got))
	for i, m := range got {
		kinds[i] = m.Kind
		want := MetricObservation{Kind: m.Kind, At: at, FireID: "fire-id", ScheduleID: "schedule-id", JobType: "job", Attempt: 2, Duration: 3 * time.Second, Lag: 5 * time.Second, MisfireCount: 4, Reason: "coalesced"}
		if !reflect.DeepEqual(m, want) {
			t.Fatalf("measurement = %#v, want %#v", m, want)
		}
	}
	if !reflect.DeepEqual(kinds, wantKinds) {
		t.Fatalf("metrics = %v, want %v", kinds, wantKinds)
	}
}

func TestMetricsClampInvalidMeasurements(t *testing.T) {
	var got MetricObservation
	observer := NewMetricsObserver(MetricsRecorderFunc(func(_ context.Context, m MetricObservation) error { got = m; return nil }))
	if err := observer.Observe(context.Background(), ObserverEvent{Kind: ObserverMisfire, Duration: -time.Second, Lag: -time.Second, MisfireCount: -2}); err != nil {
		t.Fatal(err)
	}
	if got.Duration != 0 || got.Lag != 0 || got.MisfireCount != 0 {
		t.Fatalf("negative measurement leaked: %#v", got)
	}
}

func TestSlogObserverStructuredFactsOmitPayloadAndRawErrors(t *testing.T) {
	var output bytes.Buffer
	observer := NewSlogObserver(slog.New(slog.NewJSONHandler(&output, nil)))
	event := ObserverEvent{Kind: ObserverFailure, At: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Fire: Fire{ID: "fire-id", ScheduleID: "schedule-id", JobType: "job", Attempt: 2, Status: FireRetrying, Payload: []byte("private payload"), LastError: "private last error"}, Duration: 3 * time.Second, Lag: 5 * time.Second, Reason: "dispatch_failed", Operation: "enqueue", Err: errors.New("private runner error")}
	if err := observer.Observe(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{"level": "WARN", "kind": "failure", "fire_id": "fire-id", "schedule_id": "schedule-id", "job_type": "job", "attempt": float64(2), "status": "retrying", "reason": "dispatch_failed", "operation": "enqueue", "has_error": true, "duration": float64(3 * time.Second), "lag": float64(5 * time.Second)} {
		if got[key] != want {
			t.Errorf("%s=%v, want %v", key, got[key], want)
		}
	}
	if strings.Contains(output.String(), "private") {
		t.Fatalf("opaque data leaked into telemetry: %s", output.String())
	}
}

func TestDisabledObserversDoNotEmit(t *testing.T) {
	var metrics *MetricsObserver
	var logs *SlogObserver
	for _, observer := range []Observer{metrics, logs, NewMetricsObserver(nil), NewSlogObserver(nil)} {
		if err := observer.Observe(context.Background(), ObserverEvent{Kind: ObserverFire}); err != nil {
			t.Fatal(err)
		}
	}
}
