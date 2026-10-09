package reflexes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// ErrNilSeam is wrapped by New when a required seam is nil.
var ErrNilSeam = errors.New("reflexes: nil seam")

// Source lists the candidate reflexes for one agent and class, already
// ordered priority DESC, created-at ASC. Nanite's
// Store.ListAgentReflexesForAgent satisfies it.
type Source interface {
	Candidates(ctx context.Context, agentID, classTag string) ([]Reflex, error)
}

// KindCatalog lists the known action kinds and their facets. Nanite's
// Store.ListReflexActionKinds satisfies it.
type KindCatalog interface {
	ActionKinds(ctx context.Context) ([]ActionKind, error)
}

// StateSource builds a fresh State. It is an open seam: this module does
// not implement one. Nanite's SQL StateCollector is the source system's.
type StateSource interface {
	Collect(ctx context.Context, sessionID, agentID, class string) (State, error)
}

// Option configures New.
type Option func(*Engine)

// WithTrace sets the TraceStore that receives one event and one fired-count
// bump per firing. Without it nothing is persisted.
func WithTrace(ts TraceStore) Option { return func(e *Engine) { e.trace = ts } }

// WithFilters sets the plugin Filters.
func WithFilters(f Filters) Option { return func(e *Engine) { e.filters = f } }

// WithLogger sets the logger; nil keeps slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(e *Engine) {
		if l != nil {
			e.logger = l
		}
	}
}

// WithClock sets the time source used for cooldown and the fired-count
// bump; nil keeps time.Now.
func WithClock(now func() time.Time) Option {
	return func(e *Engine) {
		if now != nil {
			e.now = now
		}
	}
}

// WithStateSource sets the StateSource used when RunInput.State is nil.
func WithStateSource(s StateSource) Option { return func(e *Engine) { e.states = s } }

// Engine runs the sense, integrate, act pipeline: list candidates, filter
// them, apply the cooldown cascade, resolve per-kind combining algorithms,
// apply actions, trace, and run after-emit handlers. It is safe for
// concurrent use.
type Engine struct {
	src    Source
	kinds  KindCatalog
	states StateSource
	trace  TraceStore

	filters Filters
	logger  *slog.Logger
	now     func() time.Time
	exec    *Executor

	// kindsMu guards the action-kind cache. Kinds change rarely, so they
	// are loaded once by New and re-read by RefreshKinds.
	kindsMu    sync.RWMutex
	actionKind map[string]*ActionKind
}

// New builds an Engine. src and kinds are required; a nil seam is an error
// wrapping ErrNilSeam. The kind cache is loaded eagerly on a best-effort
// basis: a load failure is logged and leaves the cache empty, which
// degrades every kind to the system default cooldown and all_applicable.
func New(src Source, kinds KindCatalog, opts ...Option) (*Engine, error) {
	if src == nil {
		return nil, fmt.Errorf("%w: Source", ErrNilSeam)
	}
	if kinds == nil {
		return nil, fmt.Errorf("%w: KindCatalog", ErrNilSeam)
	}
	e := &Engine{src: src, kinds: kinds, logger: slog.Default(), now: time.Now}
	for _, o := range opts {
		if o != nil {
			o(e)
		}
	}
	e.exec = NewExecutor(e.logger)
	if err := e.RefreshKinds(context.Background()); err != nil {
		e.logger.Warn("reflex engine: initial action-kind cache load failed", "err", err)
	}
	return e, nil
}

// Executor returns the engine's Executor, where handlers are registered.
func (e *Engine) Executor() *Executor { return e.exec }

// RefreshKinds reloads the action-kind cache from the KindCatalog.
func (e *Engine) RefreshKinds(ctx context.Context) error {
	kinds, err := e.kinds.ActionKinds(ctx)
	if err != nil {
		return fmt.Errorf("refresh action kinds: %w", err)
	}
	m := make(map[string]*ActionKind, len(kinds))
	for i := range kinds {
		k := kinds[i]
		m[k.Name] = &k
	}
	e.kindsMu.Lock()
	e.actionKind = m
	e.kindsMu.Unlock()
	return nil
}

// KindDefaultRecurrence returns the cached DefaultRecurrenceSeconds for
// kind, or nil if the kind is unknown or the cache is empty.
func (e *Engine) KindDefaultRecurrence(kind string) *int64 {
	e.kindsMu.RLock()
	defer e.kindsMu.RUnlock()
	if k, ok := e.actionKind[kind]; ok {
		return k.DefaultRecurrenceSeconds
	}
	return nil
}

func (e *Engine) kindLookup(_ context.Context, kind string) (*ActionKind, error) {
	e.kindsMu.RLock()
	defer e.kindsMu.RUnlock()
	if k, ok := e.actionKind[kind]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("action kind %q not cached", kind)
}

// RunInput is one evaluation pass.
type RunInput struct {
	AgentID    string
	AgentClass string
	// SessionID is passed to the StateSource when State is nil.
	SessionID string
	// State is the snapshot to evaluate; nil asks the StateSource.
	State *State
	// Candidates overrides the Source; nil asks the Source for
	// (AgentID, AgentClass). A non-nil empty slice means "no candidates".
	Candidates []Reflex
	// Kinds, when non-empty, keeps only candidates of these action kinds.
	Kinds []string
	// ExcludeKinds drops candidates of these action kinds.
	ExcludeKinds []string
	// Extra is merged into every trace record (FiringContext.ExtraMetadata).
	Extra map[string]any
	// BeforeEmit, when non-nil, is called for each applied action after
	// Filters and before the trace write, and may edit it (for example
	// Spec["reason"]) so the trace carries the edit. The action is the one
	// returned in Result.Applied.
	BeforeEmit func(*AppliedAction)
	// SkipFilters bypasses Filters.FilterState and Filters.FilterAction for
	// this pass. Fired and Staged are still called. Nanite's dispatch and
	// resume paths passed plugin hooks only to the trace step.
	SkipFilters bool
}

// Result is the outcome of Run.
type Result struct {
	Applied  AppliedActions
	Outcomes []CandidateOutcome
	// Considered is the number of candidates that remained after Kinds and
	// ExcludeKinds, whether or not any fired. A host gating a blind retry
	// on "no reflex is attached at all" must use this rather than
	// len(Applied.Actions).
	Considered int
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Run evaluates one pass. The pipeline is: filter State (Filters), list
// candidates, keep/drop kinds, Resolve with the cascading cooldown and the
// cached kind catalog, warn on trigger and apply errors, filter each
// applied action (Filters), BeforeEmit, EmitFirings, then PhaseAfterEmit
// handlers.
//
// The error is a setup or resolve failure (Result is zero), or, after a
// successful pass, the first PhaseAfterEmit handler error (Result is
// complete and the actions are already counted as fired).
func (e *Engine) Run(ctx context.Context, in RunInput) (Result, error) {
	if e == nil {
		return Result{}, errors.New("reflexes: Engine is nil")
	}

	var state State
	switch {
	case in.State != nil:
		state = *in.State
	case e.states != nil:
		s, err := e.states.Collect(ctx, in.SessionID, in.AgentID, in.AgentClass)
		if err != nil {
			return Result{}, fmt.Errorf("collect state: %w", err)
		}
		state = s
	default:
		return Result{}, errors.New("reflexes: RunInput.State is nil and no StateSource is configured")
	}

	if e.filters != nil && !in.SkipFilters {
		filtered, err := e.filters.FilterState(ctx, state)
		if err != nil {
			e.logger.Warn("reflex state filter failed", "err", err)
		} else {
			state = filtered
		}
	}

	rows := in.Candidates
	if rows == nil {
		var err error
		rows, err = e.src.Candidates(ctx, in.AgentID, in.AgentClass)
		if err != nil {
			return Result{}, fmt.Errorf("list reflexes: %w", err)
		}
	}
	candidates := make([]Reflex, 0, len(rows))
	for _, r := range rows {
		if len(in.Kinds) > 0 && !contains(in.Kinds, r.ActionKind) {
			continue
		}
		if contains(in.ExcludeKinds, r.ActionKind) {
			continue
		}
		candidates = append(candidates, r)
	}

	now := e.now()
	cooldown := func(r Reflex) bool {
		window := EffectiveCooldown(e.KindDefaultRecurrence(r.ActionKind), r.RecurrenceOverrideSeconds)
		suppressed := RecentlyFired(r, now, window)
		if suppressed {
			e.logger.Info("reflex fired but suppressed by cooldown",
				"session_id", state.SessionID, "reflex", r.Name, "cooldown", window)
		}
		return suppressed
	}

	resolved, outcomes, err := Resolve(ctx, candidates, state, e.exec, cooldown, e.kindLookup)
	if err != nil {
		return Result{}, fmt.Errorf("resolve reflexes: %w", err)
	}
	for _, oc := range outcomes {
		if oc.TriggerError != "" {
			e.logger.Warn("reflex trigger evaluate failed",
				"session_id", state.SessionID, "reflex", oc.ReflexName, "err", oc.TriggerError)
		}
		if oc.ApplyError != "" {
			e.logger.Warn("reflex apply failed",
				"session_id", state.SessionID, "reflex", oc.ReflexName, "err", oc.ApplyError)
		}
	}

	out := AppliedActions{
		Actions:       make([]AppliedAction, 0, len(resolved.Actions)),
		FiredReflexes: make([]Reflex, 0, len(resolved.FiredReflexes)),
	}
	for i, action := range resolved.Actions {
		r := resolved.FiredReflexes[i]
		if e.filters != nil && !in.SkipFilters {
			filtered, ferr := e.filters.FilterAction(ctx, action, map[string]any{
				"session_id":  state.SessionID,
				"agent_id":    in.AgentID,
				"agent_class": in.AgentClass,
				"reflex_id":   r.ID,
				"reflex_name": r.Name,
			})
			if ferr != nil {
				e.logger.Warn("reflex action filter failed", "reflex", r.Name, "err", ferr)
			} else {
				action = filtered
			}
		}
		out.Actions = append(out.Actions, action)
		out.FiredReflexes = append(out.FiredReflexes, r)
	}
	if in.BeforeEmit != nil {
		for i := range out.Actions {
			in.BeforeEmit(&out.Actions[i])
		}
	}

	emitFirings(ctx, e.trace, e.filters, out, outcomes, state, FiringContext{
		AgentID:       in.AgentID,
		AgentClass:    in.AgentClass,
		ExtraMetadata: in.Extra,
	}, e.logger, now)

	res := Result{Applied: out, Outcomes: outcomes, Considered: len(candidates)}
	return res, e.exec.AfterEmit(ctx, out, state)
}
