package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestIntervalRestartKeepsAnchorAndDispatchJitter(t *testing.T) {
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	store := newFakeStore(Schedule{ID: "interval", Interval: time.Minute, Anchor: at, NextRun: at, Enabled: true, Jitter: 10 * time.Second})
	clock := newFakeClock(at)
	runner := &fakeRunner{}
	first := New(store, runner, WithClock(clock), WithRandomSource(fixedRandom(10*time.Second)))
	if err := first.TickNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.snapshot()) != 0 {
		t.Fatal("jitter dispatched early")
	}
	clock.set(at.Add(10 * time.Second))
	restarted := New(store, runner, WithClock(clock), WithRandomSource(fixedRandom(10*time.Second)))
	if err := restarted.TickNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	jobs := runner.snapshot()
	if len(jobs) != 1 || jobs[0].FireID != DeriveFireID("interval", at) {
		t.Fatalf("changed nominal identity %+v", jobs)
	}
	store.mu.Lock()
	next := store.schedules["interval"].NextRun
	store.mu.Unlock()
	if !next.Equal(at.Add(time.Minute)) {
		t.Fatalf("anchor drifted %v", next)
	}
	clock.set(at.Add(time.Minute))
	if err := restarted.TickNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.snapshot()) != 1 {
		t.Fatal("second jitter dispatched early")
	}
	clock.set(at.Add(70 * time.Second))
	if err := restarted.TickNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if jobs = runner.snapshot(); len(jobs) != 2 || jobs[1].FireID != DeriveFireID("interval", at.Add(time.Minute)) {
		t.Fatalf("second nominal occurrence %+v", jobs)
	}
}

func TestBackoffJitterSurvivesEngineRestart(t *testing.T) {
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	store := newFakeStore(Schedule{ID: "retry-jitter", NextRun: at, Enabled: true, Retry: RetryPolicy{MaxAttempts: 2, Backoff: BackoffPolicy{Strategy: BackoffConstant, InitialDelay: time.Second, Jitter: time.Second}}})
	clock := newFakeClock(at)
	runner := &fakeRunner{responses: []error{errors.New("retry"), nil}}
	first := New(store, runner, WithClock(clock), WithRandomSource(fixedRandom(time.Second)))
	if err := first.TickNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	clock.set(at.Add(time.Second))
	restarted := New(store, runner, WithClock(clock), WithRandomSource(fixedRandom(time.Second)))
	if err := restarted.TickNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.snapshot()) != 1 {
		t.Fatal("retry ignored persisted jitter")
	}
	clock.set(at.Add(2 * time.Second))
	if err := restarted.TickNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	jobs := runner.snapshot()
	if len(jobs) != 2 || jobs[0].FireID != jobs[1].FireID || jobs[1].Attempt != 2 {
		t.Fatalf("retry identity %+v", jobs)
	}
}
