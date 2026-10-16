package reflexes

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func reminder(id string, prio int64, created string) Reflex {
	return alwaysFireReflex(id, "reminder-"+id, ActionInjectReminder, prio, created)
}

func TestNew_NilSeamsReturnError(t *testing.T) {
	fs := &fakeStore{}
	if _, err := New(nil, fs); !errors.Is(err, ErrNilSeam) {
		t.Errorf("New(nil Source) err = %v, want ErrNilSeam", err)
	}
	if _, err := New(fs, nil); !errors.Is(err, ErrNilSeam) {
		t.Errorf("New(nil KindCatalog) err = %v, want ErrNilSeam", err)
	}
	var e *Engine
	if _, err := e.Run(context.Background(), RunInput{}); err == nil {
		t.Error("Run on a nil Engine must return an error, not panic")
	}
}

func TestRun_NoStateAndNoStateSourceIsAnError(t *testing.T) {
	e := newEngine(t, &fakeStore{kinds: catalog()})
	if _, err := e.Run(context.Background(), RunInput{AgentID: "a"}); err == nil {
		t.Fatal("Run without State or StateSource: err = nil")
	}
}

func TestRun_UsesStateSourceWhenStateNil(t *testing.T) {
	fs := &fakeStore{kinds: catalog(), rows: []Reflex{reminder("r1", 1, "2026-01-01")}}
	var got [4]string
	e := newEngine(t, fs, WithStateSource(fakeStates{got: &got, st: probeState()}))
	res, err := e.Run(context.Background(), RunInput{SessionID: "s9", AgentID: "a9", AgentClass: "c9"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != [4]string{"collect", "s9", "a9", "c9"} {
		t.Errorf("StateSource args = %v", got)
	}
	if len(res.Applied.Actions) != 1 {
		t.Errorf("Actions = %d, want 1", len(res.Applied.Actions))
	}
	e2 := newEngine(t, fs, WithStateSource(fakeStates{err: errBoom}))
	if _, err := e2.Run(context.Background(), RunInput{}); !errors.Is(err, errBoom) {
		t.Errorf("collect error not propagated: %v", err)
	}
}

func TestRun_SourceErrorPropagates(t *testing.T) {
	fs := &fakeStore{kinds: catalog(), listErr: errBoom}
	e := newEngine(t, fs)
	st := probeState()
	if _, err := e.Run(context.Background(), RunInput{State: &st}); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want wrapped errBoom", err)
	}
}

// TestRun_HaltSessionPreemptsInjectReminder_SamePass is the halt-preemption
// contract through the whole pipeline: deny_overrides beats a far
// higher-priority reminder, and the preempted reflex is not counted fired.
func TestRun_HaltSessionPreemptsInjectReminder_SamePass(t *testing.T) {
	fs := &fakeStore{kinds: catalog(), rows: []Reflex{
		alwaysFireReflex("halt", "halt_probe", ActionHaltSession, 10, "2026-01-01"),
		alwaysFireReflex("rem", "reminder_probe", ActionInjectReminder, 999, "2026-01-01"),
	}}
	fl := &fakeFilters{}
	e := newEngine(t, fs, WithFilters(fl))
	st := probeState()
	res, err := e.Run(context.Background(), RunInput{AgentID: "agent-a", AgentClass: "advisor", State: &st})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Applied.Actions) != 1 || res.Applied.Actions[0].ActionKind != ActionHaltSession {
		t.Fatalf("Actions = %+v, want only the halt action", res.Applied.Actions)
	}
	if fs.row("halt").FiredCount != 1 {
		t.Errorf("halt FiredCount = %d, want 1", fs.row("halt").FiredCount)
	}
	if fs.row("rem").FiredCount != 0 {
		t.Errorf("preempted reminder FiredCount = %d, want 0", fs.row("rem").FiredCount)
	}
	if fl.fired != 1 || fl.staged != 1 {
		t.Errorf("fired=%d staged=%d, want 1 each", fl.fired, fl.staged)
	}
	if res.Considered != 2 {
		t.Errorf("Considered = %d, want 2", res.Considered)
	}
}

// TestRun_DispatchAndResumeRowsInvisibleToGenericPass pins the
// ExcludeKinds contract: excluded rows produce no action, no bump, no
// filter or hook call, while a sibling reminder still fires.
func TestRun_DispatchAndResumeRowsInvisibleToGenericPass(t *testing.T) {
	fs := &fakeStore{kinds: catalog(), rows: []Reflex{
		alwaysFireReflex("disp", "dispatch_probe", ActionDispatchToAgent, 20, "2026-01-01"),
		alwaysFireReflex("resume", "resume_probe", ActionResumeLoopRun, 15, "2026-01-01"),
		reminder("rem", 10, "2026-01-01"),
	}}
	fl := &fakeFilters{}
	e := newEngine(t, fs, WithFilters(fl))
	st := probeState()
	res, err := e.Run(context.Background(), RunInput{
		AgentID: "agent-a", AgentClass: "advisor", State: &st,
		ExcludeKinds: []string{ActionDispatchToAgent, ActionResumeLoopRun},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Applied.Actions) != 1 || res.Applied.Actions[0].ReflexID != "rem" {
		t.Fatalf("Actions = %+v, want only the reminder", res.Applied.Actions)
	}
	if res.Considered != 1 || len(res.Outcomes) != 1 {
		t.Errorf("Considered=%d Outcomes=%d, want 1/1 (excluded rows are invisible)", res.Considered, len(res.Outcomes))
	}
	for _, id := range []string{"disp", "resume"} {
		if fs.row(id).FiredCount != 0 || fs.row(id).LastFiredAt != "" {
			t.Errorf("%s was touched: %+v", id, fs.row(id))
		}
	}
	if fl.actionFilters != 1 || fl.fired != 1 || fl.staged != 1 {
		t.Errorf("filters action=%d fired=%d staged=%d, want 1 each", fl.actionFilters, fl.fired, fl.staged)
	}
}

// TestRun_FiltersRewriteActionBeforeTrace pins the ordering asked about in
// the brief: FilterAction runs before EmitFirings, so the trace carries
// the filtered action, and Fired/Staged follow the trace write.
func TestRun_FiltersRewriteActionBeforeTrace(t *testing.T) {
	var order []string
	fs := &fakeStore{kinds: catalog(), rows: []Reflex{reminder("rem", 1, "2026-01-01")}, order: &order}
	fl := &fakeFilters{order: &order}
	e := newEngine(t, fs, WithFilters(fl))
	st := probeState()
	res, err := e.Run(context.Background(), RunInput{AgentID: "agent-a", AgentClass: "advisor", State: &st})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Applied.Actions[0].Spec["body"] != "filtered" {
		t.Fatalf("body = %v, want filtered", res.Applied.Actions[0].Spec["body"])
	}
	want := "filter-state,filter-action,trace:reminder-rem,fired,staged"
	if got := strings.Join(order, ","); got != want {
		t.Errorf("order = %s, want %s", got, want)
	}
	if !strings.Contains(fs.events[0].Metadata, `"body":"filtered"`) {
		t.Errorf("trace does not carry the filtered action: %s", fs.events[0].Metadata)
	}
	if fl.lastMeta["reflex_id"] != "rem" || fl.lastMeta["agent_class"] != "advisor" {
		t.Errorf("FilterAction meta = %v", fl.lastMeta)
	}
}

func TestRun_SkipFiltersStillCallsObservers(t *testing.T) {
	fs := &fakeStore{kinds: catalog(), rows: []Reflex{reminder("rem", 1, "2026-01-01")}}
	fl := &fakeFilters{}
	e := newEngine(t, fs, WithFilters(fl))
	st := probeState()
	res, _ := e.Run(context.Background(), RunInput{State: &st, SkipFilters: true})
	if fl.stateFilters != 0 || fl.actionFilters != 0 {
		t.Errorf("filters ran despite SkipFilters: %d/%d", fl.stateFilters, fl.actionFilters)
	}
	if fl.fired != 1 || fl.staged != 1 {
		t.Errorf("observers fired=%d staged=%d, want 1 each", fl.fired, fl.staged)
	}
	if res.Applied.Actions[0].Spec["body"] == "filtered" {
		t.Error("action was filtered despite SkipFilters")
	}
}

func TestRun_FilterErrorsAreSwallowed(t *testing.T) {
	fs := &fakeStore{kinds: catalog(), rows: []Reflex{reminder("rem", 1, "2026-01-01")}}
	fl := &fakeFilters{stateErr: errBoom, actionErr: errBoom}
	e := newEngine(t, fs, WithFilters(fl))
	st := probeState()
	res, err := e.Run(context.Background(), RunInput{State: &st})
	if err != nil || len(res.Applied.Actions) != 1 {
		t.Fatalf("res=%+v err=%v, want 1 unfiltered action", res.Applied, err)
	}
}

func TestRun_TraceRecordsFiringAndBump(t *testing.T) {
	fs := &fakeStore{kinds: catalog(), rows: []Reflex{reminder("rem", 1, "2026-01-01")}}
	e := newEngine(t, fs)
	st := probeState()
	if _, err := e.Run(context.Background(), RunInput{AgentID: "agent-a", AgentClass: "advisor", State: &st}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fs.events) != 1 {
		t.Fatalf("events = %d, want 1", len(fs.events))
	}
	ev := fs.events[0]
	if ev.EventType != ActionInjectReminder || ev.Category != "reflex" || ev.Detail != "reminder-rem" || ev.SessionID != "sess-1" {
		t.Errorf("event = %+v", ev)
	}
	if fs.row("rem").FiredCount != 1 || fs.row("rem").LastFiredAt == "" {
		t.Errorf("row not bumped: %+v", fs.row("rem"))
	}
}

func TestRun_UnknownActionKindIsAnApplyErrorNotAFire(t *testing.T) {
	fs := &fakeStore{kinds: catalog(), rows: []Reflex{alwaysFireReflex("x", "mystery", "no_such_kind", 1, "2026-01-01")}}
	e := newEngine(t, fs)
	st := probeState()
	res, err := e.Run(context.Background(), RunInput{State: &st})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Applied.Actions) != 0 || !strings.Contains(res.Outcomes[0].ApplyError, "unknown action_kind") {
		t.Fatalf("res = %+v", res)
	}
	if len(fs.events) != 0 || len(fs.bumps) != 0 {
		t.Error("a failed apply must not be traced or counted")
	}
}

func TestRun_ConsideredCountsCandidatesEvenWhenNoneFire(t *testing.T) {
	fs := &fakeStore{kinds: catalog(), rows: []Reflex{
		neverFireReflex("n1", "never", ActionResumeLoopRun),
		reminder("rem", 1, "2026-01-01"),
	}}
	e := newEngine(t, fs)
	st := probeState()
	res, err := e.Run(context.Background(), RunInput{State: &st, Kinds: []string{ActionResumeLoopRun}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Applied.Actions) != 0 || res.Considered != 1 {
		t.Errorf("Actions=%d Considered=%d, want 0/1: a gating reflex is attached but did not fire", len(res.Applied.Actions), res.Considered)
	}
	res, _ = e.Run(context.Background(), RunInput{State: &st, Kinds: []string{"absent_kind"}})
	if res.Considered != 0 {
		t.Errorf("Considered = %d, want 0 when nothing of the kind is attached", res.Considered)
	}
}

func TestRun_ExplicitEmptyCandidatesDoesNotConsultSource(t *testing.T) {
	fs := &fakeStore{kinds: catalog(), rows: []Reflex{reminder("rem", 1, "2026-01-01")}}
	e := newEngine(t, fs)
	st := probeState()
	res, err := e.Run(context.Background(), RunInput{State: &st, Candidates: []Reflex{}})
	if err != nil || res.Considered != 0 || len(res.Applied.Actions) != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestRun_BeforeEmitEditsAreTracedAndReturned(t *testing.T) {
	fs := &fakeStore{kinds: catalog(), rows: []Reflex{alwaysFireReflex("d", "route", ActionDispatchToAgent, 1, "2026-01-01")}}
	e := newEngine(t, fs)
	st := probeState()
	res, err := e.Run(context.Background(), RunInput{State: &st, BeforeEmit: func(a *AppliedAction) { a.Spec["reason"] = "reflex:" + a.ReflexName }})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Applied.Actions[0].Spec["reason"] != "reflex:route" {
		t.Errorf("returned action lacks the edit: %v", res.Applied.Actions[0].Spec)
	}
	if !strings.Contains(fs.events[0].Metadata, `"reason":"reflex:route"`) {
		t.Errorf("trace lacks the BeforeEmit edit: %s", fs.events[0].Metadata)
	}
}

// --- recurrence cascade through the engine (fake clock, fake store) ---

func runTwice(t *testing.T, fs *fakeStore, clock *time.Time) (first, second int) {
	t.Helper()
	e := newEngine(t, fs, WithClock(func() time.Time { return *clock }))
	st := probeState()
	r1, err := e.Run(context.Background(), RunInput{State: &st})
	if err != nil {
		t.Fatalf("Run 1: %v", err)
	}
	r2, err := e.Run(context.Background(), RunInput{State: &st})
	if err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	return len(r1.Applied.Actions), len(r2.Applied.Actions)
}

func TestRun_DefaultCooldown_DebouncesAt15Minutes(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	fs := &fakeStore{kinds: catalog(), rows: []Reflex{reminder("rem", 1, "2026-01-01")}}
	first, second := runTwice(t, fs, &now)
	if first != 1 || second != 0 {
		t.Fatalf("first=%d second=%d, want 1/0 (system default cooldown suppresses an immediate re-fire)", first, second)
	}
	// After the window has elapsed it fires again.
	now = now.Add(16 * time.Minute)
	e := newEngine(t, fs, WithClock(func() time.Time { return now }))
	st := probeState()
	res, _ := e.Run(context.Background(), RunInput{State: &st})
	if len(res.Applied.Actions) != 1 {
		t.Errorf("after 16m actions = %d, want 1", len(res.Applied.Actions))
	}
}

func TestRun_ReflexLevelZeroOverride_BypassesCooldown(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	r := reminder("rem", 1, "2026-01-01")
	r.RecurrenceOverrideSeconds = int64p(0)
	fs := &fakeStore{kinds: catalog(), rows: []Reflex{r}}
	first, second := runTwice(t, fs, &now)
	if first != 1 || second != 1 {
		t.Fatalf("first=%d second=%d, want 1/1 (explicit 0 is not unset)", first, second)
	}
}

func TestRun_ReflexLevelPositiveOverride_ShortensWindow(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	r := reminder("rem", 1, "2026-01-01")
	r.RecurrenceOverrideSeconds = int64p(1)
	fs := &fakeStore{kinds: catalog(), rows: []Reflex{r}}
	first, second := runTwice(t, fs, &now)
	if first != 1 || second != 0 {
		t.Fatalf("first=%d second=%d, want 1/0 within the 1s window", first, second)
	}
	now = now.Add(2 * time.Second)
	e := newEngine(t, fs, WithClock(func() time.Time { return now }))
	st := probeState()
	res, _ := e.Run(context.Background(), RunInput{State: &st})
	if len(res.Applied.Actions) != 1 {
		t.Errorf("after 2s actions = %d, want 1 (the override, not the 15m default, governs)", len(res.Applied.Actions))
	}
}

func TestRun_KindLevelZeroDefault_BypassesCooldown(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	fs := &fakeStore{kinds: catalog(), rows: []Reflex{alwaysFireReflex("d", "route", ActionDispatchToAgent, 1, "2026-01-01")}}
	first, second := runTwice(t, fs, &now)
	if first != 1 || second != 1 {
		t.Fatalf("first=%d second=%d, want 1/1 (dispatch kind default is 0)", first, second)
	}
}

func TestEngine_KindDefaultRecurrence_LoadsSeededZero(t *testing.T) {
	e := newEngine(t, &fakeStore{kinds: catalog()})
	if got := e.KindDefaultRecurrence(ActionDispatchToAgent); got == nil || *got != 0 {
		t.Fatalf("dispatch default = %v, want pointer to 0", got)
	}
	if got := e.KindDefaultRecurrence(ActionInjectReminder); got != nil {
		t.Fatalf("reminder default = %v, want nil", got)
	}
	if got := e.KindDefaultRecurrence("nope"); got != nil {
		t.Fatalf("unknown kind default = %v, want nil", got)
	}
}

func TestEngine_RefreshKinds_PicksUpChange(t *testing.T) {
	fs := &fakeStore{kinds: catalog()}
	e := newEngine(t, fs)
	if e.KindDefaultRecurrence(ActionInjectReminder) != nil {
		t.Fatal("before: want nil")
	}
	sixty := int64(60)
	fs.mu.Lock()
	fs.kinds[0].DefaultRecurrenceSeconds = &sixty
	fs.mu.Unlock()
	if e.KindDefaultRecurrence(ActionInjectReminder) != nil {
		t.Fatal("cache must be stale until RefreshKinds")
	}
	if err := e.RefreshKinds(context.Background()); err != nil {
		t.Fatalf("RefreshKinds: %v", err)
	}
	if got := e.KindDefaultRecurrence(ActionInjectReminder); got == nil || *got != 60 {
		t.Fatalf("after refresh = %v, want 60", got)
	}
}

func TestEngine_KindCatalogFailureDegradesInsteadOfFailingConstruction(t *testing.T) {
	fs := &fakeStore{kindsErr: errBoom, rows: []Reflex{
		alwaysFireReflex("a", "f1", ActionForceToolChoice, 2, "2026-01-01"),
		alwaysFireReflex("b", "f2", ActionForceToolChoice, 1, "2026-01-01"),
	}}
	e := newEngine(t, fs)
	if err := e.RefreshKinds(context.Background()); !errors.Is(err, errBoom) {
		t.Errorf("RefreshKinds err = %v", err)
	}
	st := probeState()
	res, _ := e.Run(context.Background(), RunInput{State: &st})
	if len(res.Applied.Actions) != 2 {
		t.Fatalf("Actions = %d, want 2: an unresolvable kind fails open to all_applicable", len(res.Applied.Actions))
	}
}

// TestRun_ConcurrentRunAndRefreshKinds is meaningful under -race.
func TestRun_ConcurrentRunAndRefreshKinds(t *testing.T) {
	rows := make([]Reflex, 0, 8)
	for i := 0; i < 8; i++ {
		r := reminder(fmt.Sprintf("r%d", i), int64(i), "2026-01-01")
		r.RecurrenceOverrideSeconds = int64p(0)
		rows = append(rows, r)
	}
	fs := &fakeStore{kinds: catalog(), rows: rows}
	e := newEngine(t, fs)
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			st := probeState()
			for i := 0; i < 50; i++ {
				if _, err := e.Run(context.Background(), RunInput{State: &st}); err != nil {
					t.Errorf("Run: %v", err)
					return
				}
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if err := e.RefreshKinds(context.Background()); err != nil {
					t.Errorf("RefreshKinds: %v", err)
					return
				}
				_ = e.KindDefaultRecurrence(ActionInjectReminder)
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				e.Executor().Handle("custom", HandlerFunc(func(context.Context, Firing) error { return nil }))
				e.Executor().Stage("other")
			}
		}()
	}
	wg.Wait()
}
