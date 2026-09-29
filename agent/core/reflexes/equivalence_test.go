package reflexes

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The four TestRunEquivalence_* tests pin Engine.Run against the four
// hand-copied Resolve -> EmitFirings pipelines it replaces. Each asserts the
// winner, Considered and the emitted trace JSON byte for byte against a
// golden written from telemetry.go's traceRecord field order (struct order;
// merged ExtraMetadata re-marshals through a map, so its keys are sorted).

func dispatchRow(id string, prio int64, spec string) Reflex {
	return Reflex{
		ID: id, Name: "dispatch-" + id, Priority: prio, CreatedAt: "2026-01-01",
		TriggerKind: "predicate",
		TriggerSpec: `{"kind":"AND","clauses":[{"kind":"scope_tier","value":"open"},{"kind":"attr","key":"execution_pattern","value":"subagent"},{"kind":"user_regex_window","window":1,"pattern":"research"}]}`,
		ActionKind:  ActionDispatchToAgent, ActionSpec: spec,
	}
}

func dispatchState() State {
	return State{
		SessionID: "sess-d", AgentID: "agent-a", AgentClass: "advisor",
		Attrs:        map[string]string{AttrScopeTier: "open", AttrExecutionPattern: "subagent"},
		UserMessages: []MessageSignal{{Content: "please research this"}},
	}
}

// Copy 1: the generic per-turn pass (engine.go EvaluateState).
func TestRunEquivalence_GenericPass(t *testing.T) {
	f1 := alwaysFireReflex("f1", "force-f1", ActionForceToolChoice, 90, "2026-01-01")
	f1.ActionSpec = `{"tool_name":"x"}`
	f2 := alwaysFireReflex("f2", "force-f2", ActionForceToolChoice, 10, "2026-01-02")
	r1 := reminder("r1", 10, "2026-01-01")
	r1.ActionSpec = `{"body":"b1"}`
	r1.ProvenanceTier = "system"
	r2 := reminder("r2", 5, "2026-01-01")
	r2.ActionSpec = `{"body":"b2"}`
	disp := alwaysFireReflex("d", "dispatch-d", ActionDispatchToAgent, 100, "2026-01-01")
	fs := &fakeStore{kinds: catalog(), rows: []Reflex{disp, f1, r1, r2, f2}}
	e := newEngine(t, fs)
	st := probeState()

	res, err := e.Run(context.Background(), RunInput{
		AgentID: "agent-a", AgentClass: "advisor", State: &st,
		ExcludeKinds: []string{ActionDispatchToAgent, ActionResumeLoopRun},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Considered != 4 {
		t.Errorf("Considered = %d, want 4 (dispatch row excluded)", res.Considered)
	}
	var ids []string
	for _, r := range res.Applied.FiredReflexes {
		ids = append(ids, r.ID)
	}
	if got := strings.Join(ids, ","); got != "f1,r1,r2" {
		t.Fatalf("winners = %s, want f1,r1,r2 (first_applicable keeps f1, all_applicable keeps both reminders)", got)
	}
	want := []string{
		`{"reflex_id":"f1","reflex_name":"force-f1","action_kind":"force_tool_choice","category":"system_message","combining_algorithm":"first_applicable","priority":90,"agent_id":"agent-a","agent_class":"advisor","session_id":"sess-1","spec":{"tool_name":"x"},"alternatives_considered":[{"reflex_id":"f2","reflex_name":"force-f2","fired":true}]}`,
		`{"reflex_id":"r1","reflex_name":"reminder-r1","action_kind":"inject_reminder","category":"system_message","combining_algorithm":"all_applicable","provenance_tier":"system","priority":10,"agent_id":"agent-a","agent_class":"advisor","session_id":"sess-1","spec":{"body":"b1"}}`,
		`{"reflex_id":"r2","reflex_name":"reminder-r2","action_kind":"inject_reminder","category":"system_message","combining_algorithm":"all_applicable","priority":5,"agent_id":"agent-a","agent_class":"advisor","session_id":"sess-1","spec":{"body":"b2"}}`,
	}
	assertTraces(t, fs, want)
}

// Copy 2: attemptReflexDispatch: hand-built State, run-scoped candidates,
// one winner, and the Spec["reason"] default edited between Resolve and
// EmitFirings so the trace carries it.
func TestRunEquivalence_DispatchAttempt(t *testing.T) {
	rows := []Reflex{
		dispatchRow("d1", 50, `{"agent_slug":"researcher","confidence":0.9}`),
		dispatchRow("d2", 10, `{"agent_slug":"coordinator"}`),
	}
	fs := &fakeStore{kinds: catalog()}
	e := newEngine(t, fs)
	st := dispatchState()
	res, err := e.Run(context.Background(), RunInput{
		AgentID: "agent-a", AgentClass: "advisor", State: &st, Candidates: rows,
		Kinds: []string{ActionDispatchToAgent},
		BeforeEmit: func(a *AppliedAction) {
			if r, _ := a.Spec["reason"].(string); r == "" {
				a.Spec["reason"] = "reflex:" + a.ReflexName
			}
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Applied.Actions) != 1 || res.Applied.FiredReflexes[0].ID != "d1" || res.Considered != 2 {
		t.Fatalf("winner/considered: %+v considered=%d", res.Applied.FiredReflexes, res.Considered)
	}
	want := `{"reflex_id":"d1","reflex_name":"dispatch-d1","action_kind":"dispatch_to_agent","category":"execute_action","combining_algorithm":"first_applicable","priority":50,"agent_id":"agent-a","agent_class":"advisor","session_id":"sess-d","scope_tier":"open","execution_pattern":"subagent","spec":{"agent_slug":"researcher","confidence":0.9,"reason":"reflex:dispatch-d1"},"alternatives_considered":[{"reflex_id":"d2","reflex_name":"dispatch-d2","fired":true}]}`
	assertTraces(t, fs, []string{want})

	// The same result must come out of the hand-composed pipeline.
	manual := &fakeStore{}
	exec := &Executor{Logger: quietLogger()}
	k := catalog()
	lookup := func(_ context.Context, name string) (*ActionKind, error) {
		for i := range k {
			if k[i].Name == name {
				return &k[i], nil
			}
		}
		return nil, errKindNotFound
	}
	resolved, outcomes, err := Resolve(context.Background(), rows, st, exec, func(Reflex) bool { return false }, lookup)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	resolved.Actions[0].Spec["reason"] = "reflex:" + resolved.Actions[0].ReflexName
	EmitFirings(context.Background(), manual, nil, resolved, outcomes, st, FiringContext{AgentID: "agent-a", AgentClass: "advisor"}, quietLogger())
	if len(manual.events) != 1 || manual.events[0].Metadata != want {
		t.Errorf("manual pipeline trace differs:\n got %v\nwant %s", manual.events, want)
	}
}

// Copy 3: matchDispatchToAgentReflex: no engine plugins in the resolve step,
// ExtraMetadata audit keys merged into the trace.
func TestRunEquivalence_SelfToolsDispatch(t *testing.T) {
	rows := []Reflex{dispatchRow("d1", 50, `{"agent_slug":"researcher"}`)}
	fs := &fakeStore{kinds: catalog()}
	fl := &fakeFilters{}
	e := newEngine(t, fs, WithFilters(fl))
	st := dispatchState()
	st.Attrs = nil
	st.Attrs = map[string]string{AttrScopeTier: "open", AttrExecutionPattern: "subagent"}
	st.SessionID = "sess-s"
	res, err := e.Run(context.Background(), RunInput{
		AgentID: "agent-a", AgentClass: "advisor", State: &st, Candidates: rows,
		Kinds: []string{ActionDispatchToAgent}, SkipFilters: true,
		Extra: map[string]any{
			"raw_input_text":         "raw",
			"sent_input_text":        "sent",
			"matched_input_excerpt":  "research",
			"unrelated_extra_number": 3,
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Considered != 1 || res.Applied.Actions[0].Spec["agent_slug"] != "researcher" {
		t.Fatalf("res = %+v", res)
	}
	if fl.actionFilters != 0 || fl.stateFilters != 0 || fl.fired != 1 {
		t.Errorf("filters=%d/%d fired=%d, want 0/0/1", fl.stateFilters, fl.actionFilters, fl.fired)
	}
	want := `{"action_kind":"dispatch_to_agent","agent_class":"advisor","agent_id":"agent-a","category":"execute_action","combining_algorithm":"first_applicable","execution_pattern":"subagent","matched_input_excerpt":"research","priority":50,"raw_input_text":"raw","reflex_id":"d1","reflex_name":"dispatch-d1","scope_tier":"open","sent_input_text":"sent","session_id":"sess-s","spec":{"agent_slug":"researcher"},"unrelated_extra_number":3}`
	assertTraces(t, fs, []string{want})
}

// Copy 4: EvaluateLoopRunResumeReflexes: Kinds-scoped candidates, Considered
// as hadCandidates, resume after the trace, "fired, then errored".
func TestRunEquivalence_LoopResume(t *testing.T) {
	newRow := func(id, trigger string) Reflex {
		return Reflex{ID: id, Name: "resume-" + id, CreatedAt: "2026-01-01", TriggerKind: "event",
			TriggerSpec: trigger, ActionKind: ActionResumeLoopRun, ActionSpec: `{"loop_run_id":"lr-1"}`}
	}
	setup := func(order *[]string, resume func(id string) error, rows ...Reflex) (*Engine, *fakeStore) {
		fs := &fakeStore{kinds: catalog(), rows: rows, order: order}
		e := newEngine(t, fs)
		e.Executor().Handle(ActionResumeLoopRun, HandlerFunc(func(_ context.Context, f Firing) error {
			id, _ := f.Spec["loop_run_id"].(string)
			*order = append(*order, "resume:"+f.Reflex.ID+":"+id)
			return resume(f.Reflex.ID)
		}), WithPhase(PhaseAfterEmit))
		return e, fs
	}
	in := func(events ...string) RunInput {
		st := State{SessionID: "s", AgentID: "a", AgentClass: "c"}
		for _, ev := range events {
			st.Events = append(st.Events, EventSignal{EventType: ev})
		}
		return RunInput{AgentID: "a", AgentClass: "c", State: &st, Kinds: []string{ActionResumeLoopRun}}
	}

	t.Run("attached but not fired is Considered=1, no resume", func(t *testing.T) {
		var order []string
		e, _ := setup(&order, func(string) error { return nil }, newRow("l1", `{"name":"loop_unblocked"}`))
		res, err := e.Run(context.Background(), in())
		if err != nil || len(res.Applied.Actions) != 0 || res.Considered != 1 || len(order) != 0 {
			t.Fatalf("res=%+v err=%v order=%v", res, err, order)
		}
	})
	t.Run("nothing attached is Considered=0", func(t *testing.T) {
		var order []string
		e, _ := setup(&order, func(string) error { return nil })
		res, err := e.Run(context.Background(), in("loop_unblocked"))
		if err != nil || res.Considered != 0 {
			t.Fatalf("res=%+v err=%v", res, err)
		}
	})
	t.Run("resume runs after the trace write", func(t *testing.T) {
		var order []string
		e, fs := setup(&order, func(string) error { return nil },
			newRow("l1", `{"name":"loop_unblocked"}`), newRow("l2", `{"name":"loop_unblocked"}`))
		res, err := e.Run(context.Background(), in("loop_unblocked"))
		if err != nil || len(res.Applied.Actions) != 2 || res.Considered != 2 {
			t.Fatalf("res=%+v err=%v", res, err)
		}
		want := "trace:resume-l1,trace:resume-l2,resume:l1:lr-1,resume:l2:lr-1"
		if got := strings.Join(order, ","); got != want {
			t.Errorf("order = %s, want %s", got, want)
		}
		if len(fs.bumps) != 2 {
			t.Errorf("bumps = %v", fs.bumps)
		}
	})
	t.Run("resume error is fired-then-errored and every candidate is attempted", func(t *testing.T) {
		var order []string
		e, fs := setup(&order, func(id string) error {
			if id == "l1" {
				return errBoom
			}
			return nil
		}, newRow("l1", `{"name":"loop_unblocked"}`), newRow("l2", `{"name":"loop_unblocked"}`))
		res, err := e.Run(context.Background(), in("loop_unblocked"))
		if !errors.Is(err, errBoom) {
			t.Fatalf("err = %v, want first resume error", err)
		}
		if len(res.Applied.Actions) != 2 || len(fs.bumps) != 2 {
			t.Fatalf("actions=%d bumps=%d: a failed resume is still counted fired", len(res.Applied.Actions), len(fs.bumps))
		}
		if !strings.Contains(strings.Join(order, ","), "resume:l2:lr-1") {
			t.Errorf("l2 was not attempted after l1 failed: %v", order)
		}
	})
	t.Run("without a registered handler the kind is unknown", func(t *testing.T) {
		fs := &fakeStore{kinds: catalog(), rows: []Reflex{newRow("l1", `{"name":"loop_unblocked"}`)}}
		e := newEngine(t, fs)
		res, err := e.Run(context.Background(), in("loop_unblocked"))
		if err != nil || len(res.Applied.Actions) != 0 || !strings.Contains(res.Outcomes[0].ApplyError, "unknown action_kind") {
			t.Fatalf("res=%+v err=%v", res, err)
		}
	})
}

func assertTraces(t *testing.T, fs *fakeStore, want []string) {
	t.Helper()
	if len(fs.events) != len(want) {
		t.Fatalf("trace events = %d, want %d: %+v", len(fs.events), len(want), fs.events)
	}
	for i, w := range want {
		if fs.events[i].Metadata != w {
			t.Errorf("trace %d mismatch\n got: %s\nwant: %s", i, fs.events[i].Metadata, w)
		}
	}
}
