package reflexes

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestExecutor_ZeroValueKnowsBuiltinKinds(t *testing.T) {
	var x Executor
	for _, kind := range []string{ActionInjectReminder, ActionForceToolChoice, ActionDispatchToAgent, ActionHaltSession, ActionAddSchedule, ActionSendMessage} {
		a, err := x.Apply(context.Background(), Reflex{ID: "r", Name: "n", ActionKind: kind, ActionSpec: `{"k":1}`}, State{})
		if err != nil {
			t.Errorf("%s: %v", kind, err)
		}
		if a.ActionKind != kind || a.Spec["k"] != float64(1) {
			t.Errorf("%s: applied = %+v", kind, a)
		}
	}
}

func TestExecutor_UnknownKindAndBadSpecError(t *testing.T) {
	var x Executor
	if _, err := x.Apply(context.Background(), Reflex{ActionKind: ActionResumeLoopRun}, State{}); err == nil || !strings.Contains(err.Error(), "unknown action_kind") {
		t.Errorf("resume_loop_run without a handler: err = %v", err)
	}
	if _, err := x.Apply(context.Background(), Reflex{ActionKind: ActionInjectReminder, ActionSpec: `{bad`}, State{}); err == nil || !strings.Contains(err.Error(), "parse action_spec") {
		t.Errorf("bad spec: err = %v", err)
	}
}

func TestExecutor_HandlerReceivesFiring(t *testing.T) {
	var x Executor
	var got Firing
	x.Handle(ActionHaltSession, HandlerFunc(func(_ context.Context, f Firing) error { got = f; return nil }))
	_, err := x.Apply(context.Background(), Reflex{ID: "r1", Name: "halt", ActionKind: ActionHaltSession, ActionSpec: `{"reason":"why"}`}, State{SessionID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Reflex.ID != "r1" || got.Spec["reason"] != "why" || got.State.SessionID != "s1" {
		t.Errorf("Firing = %+v", got)
	}
}

func TestExecutor_HandleNilIsIgnoredAndStageReplaces(t *testing.T) {
	var x Executor
	x.Handle("custom", nil)
	if _, err := x.Apply(context.Background(), Reflex{ActionKind: "custom"}, State{}); err == nil {
		t.Error("a nil handler must not register the kind")
	}
	called := false
	x.Handle("custom", HandlerFunc(func(context.Context, Firing) error { called = true; return nil }))
	x.Stage("custom")
	if _, err := x.Apply(context.Background(), Reflex{ActionKind: "custom"}, State{}); err != nil || called {
		t.Errorf("Stage must replace the handler: err=%v called=%v", err, called)
	}
}

// TestPhaseResolve_ErrorSwallowed_ActionStillFires: a failing PhaseResolve
// handler is logged, not fatal; the action is applied, traced and counted.
func TestPhaseResolve_ErrorSwallowed_ActionStillFires(t *testing.T) {
	fs := &fakeStore{kinds: catalog(), rows: []Reflex{alwaysFireReflex("h", "halt", ActionHaltSession, 1, "2026-01-01")}}
	e := newEngine(t, fs)
	e.Executor().Handle(ActionHaltSession, HandlerFunc(func(context.Context, Firing) error { return errBoom }))
	st := probeState()
	res, err := e.Run(context.Background(), RunInput{State: &st})
	if err != nil {
		t.Fatalf("Run err = %v, want nil (PhaseResolve errors are swallowed)", err)
	}
	if len(res.Applied.Actions) != 1 || len(fs.bumps) != 1 || len(fs.events) != 1 {
		t.Fatalf("actions=%d bumps=%d events=%d, want 1/1/1", len(res.Applied.Actions), len(fs.bumps), len(fs.events))
	}
}

// TestPhaseResolve_RunsBeforeTrace pins the timing difference between the two phases.
func TestPhaseResolve_RunsBeforeTrace(t *testing.T) {
	var order []string
	fs := &fakeStore{kinds: catalog(), rows: []Reflex{alwaysFireReflex("h", "halt", ActionHaltSession, 1, "2026-01-01")}, order: &order}
	e := newEngine(t, fs)
	e.Executor().Handle(ActionHaltSession, HandlerFunc(func(context.Context, Firing) error { order = append(order, "handler"); return nil }))
	st := probeState()
	if _, err := e.Run(context.Background(), RunInput{State: &st}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(order, ","); got != "handler,trace:halt" {
		t.Errorf("order = %s, want handler,trace:halt", got)
	}
}

// TestPhaseAfterEmit_ErrorAfterFired: the action is already counted fired,
// Run returns the first error and still attempts every fired candidate.
func TestPhaseAfterEmit_ErrorAfterFired(t *testing.T) {
	var attempted []string
	fs := &fakeStore{kinds: catalog(), rows: []Reflex{
		alwaysFireReflex("a", "sched-a", ActionAddSchedule, 2, "2026-01-01"),
		alwaysFireReflex("b", "sched-b", ActionAddSchedule, 1, "2026-01-01"),
	}}
	fs.kinds = append(fs.kinds, ActionKind{Name: ActionAddSchedule, CombiningAlgorithm: "all_applicable"})
	e := newEngine(t, fs)
	e.Executor().Handle(ActionAddSchedule, HandlerFunc(func(_ context.Context, f Firing) error {
		attempted = append(attempted, f.Reflex.ID)
		return errors.New("fail " + f.Reflex.ID)
	}), WithPhase(PhaseAfterEmit))
	st := probeState()
	res, err := e.Run(context.Background(), RunInput{State: &st})
	if err == nil || !strings.Contains(err.Error(), "fail a") {
		t.Fatalf("err = %v, want the first handler error", err)
	}
	if strings.Join(attempted, ",") != "a,b" {
		t.Errorf("attempted = %v, want both", attempted)
	}
	if len(res.Applied.Actions) != 2 || len(fs.bumps) != 2 {
		t.Errorf("actions=%d bumps=%d, want 2/2 (already counted fired)", len(res.Applied.Actions), len(fs.bumps))
	}
}

func TestExecutor_AfterEmitOnDirectResolvePath(t *testing.T) {
	var x Executor
	n := 0
	x.Handle(ActionResumeLoopRun, HandlerFunc(func(context.Context, Firing) error { n++; return nil }), WithPhase(PhaseAfterEmit))
	// Apply must not run an after-emit handler.
	a, err := x.Apply(context.Background(), Reflex{ID: "r", ActionKind: ActionResumeLoopRun}, State{})
	if err != nil || n != 0 {
		t.Fatalf("Apply ran an after-emit handler: err=%v n=%d", err, n)
	}
	if err := x.AfterEmit(context.Background(), AppliedActions{Actions: []AppliedAction{a}, FiredReflexes: []Reflex{{ID: "r"}}}, State{}); err != nil || n != 1 {
		t.Fatalf("AfterEmit: err=%v n=%d", err, n)
	}
}
