package reflexes

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
)

// Action kind names the Executor knows without registration. They are
// Nanite's seven kinds; the strings are the persisted values.
const (
	ActionInjectReminder  = "inject_reminder"
	ActionForceToolChoice = "force_tool_choice"
	ActionDispatchToAgent = "dispatch_to_agent"
	ActionHaltSession     = "halt_session"
	ActionAddSchedule     = "add_schedule"
	ActionSendMessage     = "send_message"
	ActionResumeLoopRun   = "resume_loop_run"
)

// Firing is what an ActionHandler receives: the fired reflex, its parsed
// action_spec and the State it fired against.
type Firing struct {
	Reflex Reflex
	Spec   map[string]any
	State  State
}

// ActionHandler applies the live effect of one action kind.
type ActionHandler interface {
	Handle(ctx context.Context, f Firing) error
}

// HandlerFunc adapts a function to ActionHandler.
type HandlerFunc func(context.Context, Firing) error

// Handle calls f(ctx, fi).
func (f HandlerFunc) Handle(ctx context.Context, fi Firing) error { return f(ctx, fi) }

var _ ActionHandler = HandlerFunc(nil)

// Phase says when a handler runs relative to the trace write.
type Phase int

const (
	// PhaseResolve runs the handler inside Resolve (Executor.Apply), before
	// the trace write and the fired-count bump. A handler error is logged
	// and swallowed: the action still fires. This is the timing of the
	// source system's halt, schedule and send-message hooks.
	PhaseResolve Phase = iota
	// PhaseAfterEmit runs the handler after the trace write, from
	// Engine.Run or Executor.AfterEmit. A handler error does not undo the
	// firing: the action is already counted as fired, and the first error
	// is returned after every fired action has been attempted. This is the
	// timing of the source system's resume_loop_run.
	PhaseAfterEmit
)

// HandlerOption configures Executor.Handle.
type HandlerOption func(*registration)

// WithPhase sets the handler's Phase. The default is PhaseResolve.
func WithPhase(p Phase) HandlerOption { return func(r *registration) { r.phase = p } }

type registration struct {
	handler ActionHandler // nil = staged: the caller acts on the returned action
	phase   Phase
}

// Executor maps action kinds to their effect. The zero value is usable and
// knows the seven built-in kinds: inject_reminder, force_tool_choice and
// dispatch_to_agent are staged (the caller acts on the returned action);
// halt_session, add_schedule and send_message are staged until a handler
// is registered; any other unregistered kind, including resume_loop_run,
// makes Apply return an "unknown action_kind" error.
//
// Handle and Stage may be called at any time and are safe for concurrent
// use with Apply.
type Executor struct {
	// Logger is used for diagnostics; nil means slog.Default().
	Logger *slog.Logger

	mu  sync.RWMutex
	reg map[string]registration
}

// NewExecutor returns an Executor logging to logger (nil = slog.Default()).
func NewExecutor(logger *slog.Logger) *Executor { return &Executor{Logger: logger} }

func (x *Executor) logger() *slog.Logger {
	if x.Logger != nil {
		return x.Logger
	}
	return slog.Default()
}

// Handle registers h as the handler for kind, replacing any earlier
// registration. A nil handler is ignored.
func (x *Executor) Handle(kind string, h ActionHandler, opts ...HandlerOption) {
	if h == nil {
		return
	}
	r := registration{handler: h, phase: PhaseResolve}
	for _, o := range opts {
		o(&r)
	}
	x.set(kind, r)
}

// Stage marks kinds as staged: firing one returns the AppliedAction to the
// caller and runs no handler. It replaces any earlier registration.
func (x *Executor) Stage(kinds ...string) {
	for _, k := range kinds {
		x.set(k, registration{})
	}
}

func (x *Executor) set(kind string, r registration) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.reg == nil {
		x.reg = make(map[string]registration)
	}
	x.reg[kind] = r
}

func (x *Executor) lookup(kind string) (registration, bool) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	r, ok := x.reg[kind]
	return r, ok
}

// Apply translates a fired reflex into an AppliedAction, running its
// PhaseResolve handler if one is registered. The AppliedAction is always
// returned for a known kind, so the caller can trace the firing even when
// no handler is wired. It errors only for an unparseable action_spec or an
// unknown kind.
func (x *Executor) Apply(ctx context.Context, reflex Reflex, state State) (AppliedAction, error) {
	spec := map[string]any{}
	if reflex.ActionSpec != "" {
		if err := json.Unmarshal([]byte(reflex.ActionSpec), &spec); err != nil {
			return AppliedAction{}, fmt.Errorf("parse action_spec: %w", err)
		}
	}
	applied := AppliedAction{
		ReflexID:   reflex.ID,
		ReflexName: reflex.Name,
		ActionKind: reflex.ActionKind,
		Spec:       spec,
	}

	if r, ok := x.lookup(reflex.ActionKind); ok {
		if r.handler != nil && r.phase == PhaseResolve {
			if err := r.handler.Handle(ctx, Firing{Reflex: reflex, Spec: spec, State: state}); err != nil {
				x.logger().Warn("reflex handler failed",
					"session_id", state.SessionID, "reflex", reflex.Name,
					"action_kind", reflex.ActionKind, "err", err)
			}
		}
		return applied, nil
	}

	switch reflex.ActionKind {
	case ActionInjectReminder, ActionForceToolChoice, ActionDispatchToAgent,
		ActionAddSchedule, ActionSendMessage:
		return applied, nil
	case ActionHaltSession:
		x.logger().Info("reflex halt staged (no halt hook wired)",
			"session_id", state.SessionID, "reflex", reflex.Name)
		return applied, nil
	default:
		return applied, fmt.Errorf("unknown action_kind %q", reflex.ActionKind)
	}
}

// AfterEmit runs the PhaseAfterEmit handlers for every action in applied,
// in order, and returns the first error after every action has been
// attempted. Engine.Run calls it after the trace write; callers of Resolve
// and EmitFirings call it themselves.
func (x *Executor) AfterEmit(ctx context.Context, applied AppliedActions, state State) error {
	var first error
	for i, action := range applied.Actions {
		r, ok := x.lookup(action.ActionKind)
		if !ok || r.handler == nil || r.phase != PhaseAfterEmit {
			continue
		}
		var reflex Reflex
		if i < len(applied.FiredReflexes) {
			reflex = applied.FiredReflexes[i]
		}
		err := r.handler.Handle(ctx, Firing{Reflex: reflex, Spec: action.Spec, State: state})
		if err != nil {
			x.logger().Warn("reflex after-emit handler failed",
				"session_id", state.SessionID, "reflex", action.ReflexName,
				"action_kind", action.ActionKind, "err", err)
			if first == nil {
				first = fmt.Errorf("reflex %q (%s): %w", action.ReflexName, action.ActionKind, err)
			}
		}
	}
	return first
}
