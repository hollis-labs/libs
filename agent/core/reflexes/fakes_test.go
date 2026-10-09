package reflexes

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"
)

// Compile-time assertions for the consumer-defined seams.
var (
	_ Source      = (*fakeStore)(nil)
	_ KindCatalog = (*fakeStore)(nil)
	_ TraceStore  = (*fakeStore)(nil)
	_ StateSource = fakeStates{}
	_ Filters     = (*fakeFilters)(nil)
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type loggedEvent struct {
	SessionID, EventType, Category, Detail, Metadata string
}

// fakeStore is an in-memory Source, KindCatalog and TraceStore. BumpAgentReflexFired
// writes LastFiredAt back to the row, like a real store, so cooldowns work
// across passes.
type fakeStore struct {
	mu       sync.Mutex
	rows     []Reflex
	kinds    []ActionKind
	kindsErr error
	listErr  error
	events   []loggedEvent
	bumps    []string
	order    *[]string // shared ordering log, optional
}

func (f *fakeStore) note(s string) {
	if f.order != nil {
		*f.order = append(*f.order, s)
	}
}

func (f *fakeStore) Candidates(_ context.Context, agentID, classTag string) ([]Reflex, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]Reflex(nil), f.rows...), nil
}

func (f *fakeStore) ActionKinds(context.Context) ([]ActionKind, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.kindsErr != nil {
		return nil, f.kindsErr
	}
	return append([]ActionKind(nil), f.kinds...), nil
}

func (f *fakeStore) LogEvent(_ context.Context, sessionID, eventType, category, detail, metadata string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, loggedEvent{sessionID, eventType, category, detail, metadata})
	f.note("trace:" + detail)
}

func (f *fakeStore) BumpAgentReflexFired(_ context.Context, id string, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bumps = append(f.bumps, id)
	for i := range f.rows {
		if f.rows[i].ID == id {
			f.rows[i].FiredCount++
			f.rows[i].LastFiredAt = now.UTC().Format(time.RFC3339)
		}
	}
	return nil
}

func (f *fakeStore) row(id string) Reflex {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.rows {
		if r.ID == id {
			return r
		}
	}
	return Reflex{}
}

type fakeStates struct {
	got *[4]string
	st  State
	err error
}

func (f fakeStates) Collect(_ context.Context, sessionID, agentID, class string) (State, error) {
	if f.got != nil {
		*f.got = [4]string{"collect", sessionID, agentID, class}
	}
	return f.st, f.err
}

// fakeFilters counts calls, records ordering and rewrites Spec["body"].
type fakeFilters struct {
	mu            sync.Mutex
	stateFilters  int
	actionFilters int
	fired         int
	staged        int
	lastMeta      map[string]any
	stateErr      error
	actionErr     error
	order         *[]string
}

func (f *fakeFilters) note(s string) {
	if f.order != nil {
		*f.order = append(*f.order, s)
	}
}

func (f *fakeFilters) FilterState(_ context.Context, s State) (State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stateFilters++
	f.note("filter-state")
	if f.stateErr != nil {
		return State{}, f.stateErr
	}
	return s, nil
}

func (f *fakeFilters) FilterAction(_ context.Context, a AppliedAction, meta map[string]any) (AppliedAction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actionFilters++
	f.lastMeta = meta
	f.note("filter-action")
	if f.actionErr != nil {
		return AppliedAction{}, f.actionErr
	}
	a.Spec["body"] = "filtered"
	return a, nil
}

func (f *fakeFilters) Fired(string, map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fired++
	f.note("fired")
}

func (f *fakeFilters) Staged(string, map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.staged++
	f.note("staged")
}

var errBoom = errors.New("boom")

func catalog() []ActionKind {
	zero := int64(0)
	return []ActionKind{
		{Name: ActionInjectReminder, Category: "system_message", CombiningAlgorithm: "all_applicable"},
		{Name: ActionForceToolChoice, Category: "system_message", CombiningAlgorithm: "first_applicable"},
		{Name: ActionHaltSession, Category: "execute_action", CombiningAlgorithm: "deny_overrides"},
		{Name: ActionDispatchToAgent, Category: "execute_action", CombiningAlgorithm: "first_applicable", DefaultRecurrenceSeconds: &zero},
		{Name: ActionResumeLoopRun, Category: "execute_action", CombiningAlgorithm: "all_applicable", DefaultRecurrenceSeconds: &zero},
	}
}

func probeState() State {
	return State{SessionID: "sess-1", AgentID: "agent-a", AgentClass: "advisor", Events: []EventSignal{{EventType: "probe"}}}
}

func newEngine(t interface{ Fatalf(string, ...any) }, fs *fakeStore, opts ...Option) *Engine {
	opts = append([]Option{WithLogger(quietLogger()), WithTrace(fs)}, opts...)
	e, err := New(fs, fs, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}
