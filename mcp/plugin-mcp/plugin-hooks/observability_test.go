package pluginhooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordingSink struct {
	mu       sync.Mutex
	records  []TraceRecord
	events   []BreakerEvent
	onRecord func(TraceRecord)
	onEvent  func(BreakerEvent)
}

func (s *recordingSink) Record(r TraceRecord) {
	if s.onRecord != nil {
		s.onRecord(r)
	}
	s.mu.Lock()
	s.records = append(s.records, r)
	s.mu.Unlock()
}
func (s *recordingSink) BreakerChanged(e BreakerEvent) {
	if s.onEvent != nil {
		s.onEvent(e)
	}
	s.mu.Lock()
	s.events = append(s.events, e)
	s.mu.Unlock()
}
func (s *recordingSink) Records() []TraceRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]TraceRecord(nil), s.records...)
}
func (s *recordingSink) Events() []BreakerEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]BreakerEvent(nil), s.events...)
}

func TestTelemetryRecordsArePayloadFreeAndReentrant(t *testing.T) {
	clock := newFakeClock()
	sink := &recordingSink{}
	d := definition("event.ready", Action)
	e, s := engineFixture(t, d, ExecutionConfig{Clock: clock, Sink: sink, Breaker: BreakerConfig{FailureThreshold: 1}})
	// Sink may inspect the same engine; this would deadlock inside core locks.
	sink.onRecord = func(TraceRecord) { _ = e.Breakers() }
	sink.onEvent = func(BreakerEvent) { _ = e.Breakers() }
	action(t, s, d, "handler", Options{}, func(context.Context, Invocation) error {
		clock.Advance(12 * time.Millisecond)
		return errors.New("secret-handler-text")
	})
	parent := TraceContext{TraceID: "1234567890abcdef1234567890abcdef", SpanID: "1234567890abcdef"}
	ctx, err := WithTraceContext(context.Background(), parent)
	if err != nil {
		t.Fatal(err)
	}
	result, err := e.EmitAction(ctx, d.Name, json.RawMessage(`{"secret":"secret-payload"}`), map[string]string{"private": "secret-metadata"})
	if err == nil {
		t.Fatal("missing failure")
	}
	records := sink.Records()
	if len(records) != 2 {
		t.Fatal(records)
	}
	handler, dispatch := records[0], records[1]
	if handler.Stage != "handler" || dispatch.Stage != "dispatch" || handler.TraceID != parent.TraceID || dispatch.TraceID != parent.TraceID || dispatch.ParentSpanID != parent.SpanID || handler.ParentSpanID != dispatch.SpanID || handler.SpanID == dispatch.SpanID {
		t.Fatal(records)
	}
	if handler.InvocationID != result.InvocationID || handler.HostInstance != e.registry.HostInstance() || handler.HandleID == 0 || handler.RegistrationSequence == 0 || handler.Owner != "fixture-owner" || handler.Generation != "one" || handler.CatalogVersion != "1" || handler.SchemaDigest != d.SchemaDigest || handler.OnError != Closed || handler.Priority != 10 || handler.Transport != "local" || handler.Depth != 1 {
		t.Fatal(handler)
	}
	if handler.DurationMS != 12 || handler.QueueMS != 0 || handler.EffectiveTimeoutMS != 500 || handler.ErrorClass != "handler_error" || handler.BreakerState != BreakerOpen || dispatch.DurationMS != 12 || dispatch.HandlerCount != 1 || dispatch.Outcome != string(FailedClosed) {
		t.Fatal(records)
	}
	serialized, _ := json.Marshal(records)
	if strings.Contains(string(serialized), "secret") {
		t.Fatal("telemetry leaked data", string(serialized))
	}
	events := sink.Events()
	if len(events) != 1 || events[0].Previous != BreakerClosed || events[0].Snapshot.State != BreakerOpen || events[0].Snapshot.Sequence != 1 || events[0].Snapshot.LastFailureClass != "handler_error" {
		t.Fatal(events)
	}
	if err := e.ResetBreaker("fixture-owner", "one"); err != nil {
		t.Fatal(err)
	}
	if err := s.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	events = sink.Events()
	if len(events) != 3 || events[1].Reason != "manual_reset" || events[2].Snapshot.State != BreakerDisposed || events[2].Snapshot.Sequence != 3 {
		t.Fatal(events)
	}
	events[2].Snapshot.State = BreakerClosed
	if breakerState(t, e).State != BreakerDisposed {
		t.Fatal("event snapshot aliases state")
	}
}
func TestNestedTraceAndDepthRejection(t *testing.T) {
	sink := &recordingSink{}
	d := definition("event.ready", Action)
	e, s := engineFixture(t, d, ExecutionConfig{Clock: newFakeClock(), Sink: sink, MaxDepth: 2})
	action(t, s, d, "nest", Options{}, func(ctx context.Context, _ Invocation) error {
		parent, ok := TraceContextFrom(ctx)
		if !ok || parent.TraceID == "" || parent.SpanID == "" {
			t.Error("missing handler trace parent")
		}
		_, err := e.EmitAction(ctx, d.Name, json.RawMessage(`{}`), nil)
		return err
	})
	if _, err := emit(t, e, d); !errors.Is(err, ErrDepthExceeded) {
		t.Fatal(err)
	}
	records := sink.Records()
	bySpan := map[string]TraceRecord{}
	rootTrace := ""
	rejected := false
	for _, r := range records {
		if rootTrace == "" {
			rootTrace = r.TraceID
		}
		if r.TraceID != rootTrace {
			t.Fatal("nested trace split", records)
		}
		if _, duplicate := bySpan[r.SpanID]; duplicate {
			t.Fatal("reused span", r)
		}
		bySpan[r.SpanID] = r
		if r.ErrorClass == "depth_rejected" && r.Stage == "dispatch" && r.HandlerCount == 0 {
			rejected = true
			if r.Depth != 3 {
				t.Fatal(r)
			}
		}
	}
	if !rejected {
		t.Fatal("depth failure has no span", records)
	}
	for _, r := range records {
		if r.ParentSpanID != "" {
			parent, ok := bySpan[r.ParentSpanID]
			if !ok || parent.TraceID != r.TraceID {
				t.Fatal("orphan", r)
			}
		}
	}
	if snapshot := breakerState(t, e); snapshot.ConsecutiveFailures != 0 {
		t.Fatal("depth counted", snapshot)
	}
}
func TestAsyncAndAfterCommitTracing(t *testing.T) {
	for _, mode := range []Mode{Async, AfterCommit} {
		t.Run(string(mode), func(t *testing.T) {
			clock := newFakeClock()
			sink := &recordingSink{}
			d := definition("event.ready", Action)
			d.Mode = mode
			e, s := engineFixture(t, d, ExecutionConfig{Clock: clock, Sink: sink})
			parent := TraceContext{TraceID: "1234567890abcdef1234567890abcdef", SpanID: "1234567890abcdef"}
			ctx, err := WithTraceContext(context.Background(), parent)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			action(t, s, d, "observer", Options{}, func(ctx context.Context, _ Invocation) error {
				carried, ok := TraceContextFrom(ctx)
				if !ok || carried.TraceID != parent.TraceID || carried.SpanID == parent.SpanID {
					t.Error("detached trace lost")
				}
				clock.Advance(3 * time.Millisecond)
				return nil
			})
			var receipt DispatchResult
			if mode == AfterCommit {
				pending, prepareErr := e.PrepareAfterCommit(ctx, d.Name, json.RawMessage(`{}`), nil)
				if prepareErr != nil {
					t.Fatal(prepareErr)
				}
				clock.Advance(7 * time.Millisecond)
				cancel()
				receipt, err = pending.Commit()
			} else {
				receipt, err = e.EmitAction(ctx, d.Name, json.RawMessage(`{}`), nil)
				cancel()
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := receipt.Future.Await(context.Background()); err != nil {
				t.Fatal(err)
			}
			records := sink.Records()
			if len(records) != 2 || records[1].TraceID != parent.TraceID || records[1].HandlerCount != 1 || records[1].Outcome != string(Success) {
				t.Fatal(records)
			}
			if mode == AfterCommit && (records[1].QueueMS != 7 || records[1].DurationMS != 10) {
				t.Fatal(records)
			}
		})
	}
}
func TestTelemetryRejectionsAndSinkPanics(t *testing.T) {
	clock := newFakeClock()
	sink := &recordingSink{onRecord: func(TraceRecord) { panic("sink private error") }, onEvent: func(BreakerEvent) { panic("sink private error") }}
	d := definition("event.ready", Action)
	e, s := engineFixture(t, d, ExecutionConfig{Clock: clock, Sink: sink, Breaker: BreakerConfig{FailureThreshold: 1}})
	action(t, s, d, "handler", Options{}, func(context.Context, Invocation) error { return errors.New("failure") })
	if _, err := emit(t, e, d); err == nil {
		t.Fatal("sink panic changed handler failure")
	}
	if e.TelemetryFailures() != 3 {
		t.Fatal(e.TelemetryFailures())
	}
	sink.onRecord = nil
	sink.onEvent = nil
	if _, err := e.EmitAction(context.Background(), "unknown.event", json.RawMessage(`{}`), nil); !errors.Is(err, ErrUnknownHook) {
		t.Fatal(err)
	}
	if _, err := e.EmitAction(context.Background(), d.Name, json.RawMessage(`{invalid`), nil); !errors.Is(err, ErrInvalidPayload) {
		t.Fatal(err)
	}
	records := sink.Records()
	if len(records) != 2 || records[0].Stage != "dispatch" || records[0].Outcome != string(FailedClosed) || records[1].Stage != "dispatch" {
		t.Fatal(records)
	}
	for _, parent := range []TraceContext{{}, {TraceID: "secret", SpanID: "secret"}, {TraceID: "00000000000000000000000000000000", SpanID: "1234567890abcdef"}} {
		if _, err := WithTraceContext(context.Background(), parent); !errors.Is(err, ErrInvalidOptions) {
			t.Fatal("invalid trace accepted", parent, err)
		}
	}
}
func TestConcurrentBreakerSuccessDoesNotResetOpen(t *testing.T) {
	clock := newFakeClock()
	sink := &recordingSink{}
	d := definition("event.ready", Action)
	d.Mode = Parallel
	e, s := engineFixture(t, d, ExecutionConfig{Clock: clock, Sink: sink, Breaker: BreakerConfig{FailureThreshold: 1}})
	entered, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	calls := 0
	action(t, s, d, "handler", Options{}, func(context.Context, Invocation) error {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			close(entered)
			<-release
			return nil
		}
		return fmt.Errorf("%w: detail", ErrTransport)
	})
	done := make(chan error, 1)
	go func() { _, err := emit(t, e, d); done <- err }()
	<-entered
	_, _ = emit(t, e, d)
	if breakerState(t, e).State != BreakerOpen {
		t.Fatal("not open")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if snapshot := breakerState(t, e); snapshot.State != BreakerOpen || snapshot.LastFailureClass != "transport_error" {
		t.Fatal(snapshot)
	}
}
