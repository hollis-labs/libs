package permission

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type recorder struct {
	mu     sync.Mutex
	events []Event
	// probe, if set, is called for every event; used to prove the engine
	// holds no lock during Audit.
	probe func()
}

func (r *recorder) Audit(_ context.Context, e Event) {
	if r.probe != nil {
		r.probe()
	}
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
}

func (r *recorder) snapshot() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

func kinds(evs []Event) []EventKind {
	out := make([]EventKind, len(evs))
	for i, e := range evs {
		out[i] = e.Kind
	}
	return out
}

func TestAuditor_EveryKindFiredOnce(t *testing.T) {
	rec := &recorder{}
	var e *Engine
	// If the engine held e.mu during Audit, this write-lock attempt from
	// another goroutine would deadlock the probe (and time out below).
	rec.probe = func() {
		done := make(chan struct{})
		go func() {
			e.mu.Lock()
			e.mu.Unlock() //nolint:staticcheck // empty critical section is the point: prove the lock is free
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Auditor was called while the engine lock was held")
		}
	}
	e = NewEngine(ModeDefault, nil, WithAuditor(rec), WithApprovalTimeout(time.Second))
	ctx := context.Background()

	// decision
	e.Check(ctx, "s1", "shell", map[string]any{"secret": "hunter2"}, ToolMeta{IsDestructive: true})

	// approval_requested -> grant_recorded -> approval_resolved
	req := e.RequestApproval("s1", "shell", map[string]any{"secret": "hunter2"}, "why")
	e.Respond(req.ID, DecisionAllow, ScopeSession, "s1")
	e.WaitForApproval(ctx, req)

	// approval_timeout
	e.SetApprovalTimeout(20 * time.Millisecond)
	e.WaitForApproval(ctx, e.RequestApproval("s1", "shell", nil, ""))

	// approval_canceled
	e.SetApprovalTimeout(time.Minute)
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	e.WaitForApproval(cctx, e.RequestApproval("s1", "shell", nil, ""))

	// grants_cleared
	e.ClearSessionGrants("s1")

	counts := map[EventKind]int{}
	for _, ev := range rec.snapshot() {
		counts[ev.Kind]++
		if ev.At.IsZero() {
			t.Errorf("%s event has no timestamp", ev.Kind)
		}
		if ev.Actor != "" || ev.OnBehalfOf != "" {
			t.Errorf("%s: reserved fields must stay empty", ev.Kind)
		}
	}
	want := map[EventKind]int{
		EventDecision:          1,
		EventApprovalRequested: 3,
		EventApprovalResolved:  1,
		EventApprovalTimeout:   1,
		EventApprovalCanceled:  1,
		EventGrantRecorded:     1,
		EventGrantsCleared:     1,
	}
	for k, n := range want {
		if counts[k] != n {
			t.Errorf("%s fired %d times, want %d", k, counts[k], n)
		}
	}
}

func TestAuditor_EventContent(t *testing.T) {
	rec := &recorder{}
	e := NewEngine(ModeDefault, nil, WithAuditor(rec), WithApprovalTimeout(time.Second))
	res := e.Check(context.Background(), "s1", "shell", nil, ToolMeta{IsDestructive: true})

	req := e.RequestApproval("s1", "shell", nil, "why")
	e.Respond(req.ID, DecisionDeny, ScopeOnce, "s1")
	e.WaitForApproval(context.Background(), req)

	evs := rec.snapshot()
	if got := kinds(evs); len(got) != 3 || got[0] != EventDecision || got[1] != EventApprovalRequested || got[2] != EventApprovalResolved {
		t.Fatalf("kinds = %v", got)
	}
	if evs[0].Tool != "shell" || evs[0].SessionID != "s1" || evs[0].Result.Decision != res.Decision {
		t.Errorf("decision event = %+v", evs[0])
	}
	if evs[1].RequestID != req.ID || evs[2].RequestID != req.ID {
		t.Error("approval events must carry the request id")
	}
	if evs[2].Response == nil || evs[2].Response.Decision != DecisionDeny {
		t.Errorf("resolved event response = %+v", evs[2].Response)
	}
}

func TestSlogAuditor(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, nil))
	e := NewEngine(ModeDefault, nil, WithAuditor(SlogAuditor{Logger: l}), WithApprovalTimeout(20*time.Millisecond))

	e.Check(context.Background(), "s1", "shell", nil, ToolMeta{})
	if buf.Len() != 0 {
		t.Errorf("decisions must not be logged, got %q", buf.String())
	}
	req := e.RequestApproval("s1", "shell", nil, "")
	e.WaitForApproval(context.Background(), req) // times out
	out := buf.String()
	for _, want := range []string{"approval request created", "approval timed out", "id=" + req.ID} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q:\n%s", want, out)
		}
	}
	// The zero-value logger falls back to slog.Default without panicking.
	SlogAuditor{}.Audit(context.Background(), Event{Kind: EventApprovalCanceled})
}

func TestNoAuditorIsFine(t *testing.T) {
	e := NewEngine(ModeDefault, nil, WithAuditor(nil))
	e.Check(context.Background(), "s", "t", nil, ToolMeta{})
	e.ClearSessionGrants("s")
}
