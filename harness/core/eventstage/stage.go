package eventstage

import (
	"context"
	"errors"
	"fmt"
)

// Outcome reports what a stage did to the event.
type Outcome struct {
	// Changed is true when the stage modified the event.
	Changed bool
	// Dropped asks the producer not to emit the event at all. A dropped event
	// stops the chain.
	Dropped bool
	// Note is a short, value-free explanation for logs and traces.
	Note string
}

// Stage is one step of a [Chain]. Apply mutates *e in place. It must be
// synchronous and free of side effects outside *e, and must not retain e.
type Stage[E any] interface {
	Name() string
	Apply(ctx context.Context, e *E) (Outcome, error)
}

// FailClosedStage is a Sanitize stage. When Apply returns an error or panics,
// the chain calls FailClosed, which must leave *e safe to emit: payload
// replaced, class.sanitized false.
type FailClosedStage[E any] interface {
	Stage[E]
	FailClosed(e *E, cause error)
}

// Named wraps f as a [Stage] called name.
func Named[E any](name string, f func(ctx context.Context, e *E) (Outcome, error)) Stage[E] {
	return namedStage[E]{name: name, f: f}
}

type namedStage[E any] struct {
	name string
	f    func(context.Context, *E) (Outcome, error)
}

func (s namedStage[E]) Name() string { return s.name }
func (s namedStage[E]) Apply(ctx context.Context, e *E) (Outcome, error) {
	return s.f(ctx, e)
}

// Slot is a position in the chain.
type Slot string

// The fixed order of a [Chain].
const (
	SlotStamp    Slot = "stamp"
	SlotSanitize Slot = "sanitize"
	SlotClassify Slot = "classify"
)

// ErrConfig is returned by [NewChain] for an unusable configuration.
var ErrConfig = errors.New("eventstage: invalid chain configuration")

// ErrPanic wraps a value recovered from a stage that panicked.
var ErrPanic = errors.New("eventstage: stage panicked")

// Config selects the stages of a [Chain]. Sanitize is required: a chain with
// no way to sanitize would let Classify see raw payloads. Stamp and Classify
// are optional.
//
// Stamp runs before Sanitize, on the raw payload. The chain never keeps any
// stage's error text: [Report.Err] carries only the slot and a failure class,
// which a stage can name by implementing [ClassedError].
type Config[E any] struct {
	Stamp    Stage[E]
	Sanitize FailClosedStage[E]
	Classify Stage[E]
}

// Chain runs Stamp, Sanitize and Classify in that fixed order. It is safe for
// concurrent use when its stages are.
type Chain[E any] struct{ cfg Config[E] }

// NewChain validates cfg and returns the chain.
func NewChain[E any](cfg Config[E]) (*Chain[E], error) {
	if cfg.Sanitize == nil {
		return nil, fmt.Errorf("%w: Sanitize is required", ErrConfig)
	}
	return &Chain[E]{cfg: cfg}, nil
}

// Step records one stage run.
type Step struct {
	Slot    Slot
	Stage   string
	Outcome Outcome
	// Err is a value-free description of the stage's failure (the slot and a
	// cleaned failure class, matching [ErrStamp], [ErrSanitize], [ErrClassify]
	// and, for a panic, [ErrPanic]); the stage's own error text is never kept.
	Err error
	// FailedClosed is set on the Sanitize step when FailClosed ran.
	FailedClosed bool
}

// Report is the result of [Chain.Apply].
type Report struct {
	Steps []Step
	// Dropped is true when a stage asked for the event not to be emitted.
	Dropped bool
}

// Emit reports whether the producer should emit the event. It is true even
// when a stage failed: Sanitize failures leave the event scrubbed, and Stamp
// and Classify failures only leave fields unset.
func (r Report) Emit() bool { return !r.Dropped }

// Err joins the errors of every step, or returns nil.
func (r Report) Err() error {
	var errs []error
	for _, s := range r.Steps {
		if s.Err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", s.Slot, s.Stage, s.Err))
		}
	}
	return errors.Join(errs...)
}

// Apply runs the chain on *e. The order is fixed, and Classify runs only if
// Sanitize completed, normally or by failing closed. A stage error never
// stops the event: Stamp and Classify errors leave their fields unset, and a
// Sanitize error is turned into a fail-closed payload. Only a stage that
// returns Outcome.Dropped stops the chain and suppresses emission, or a
// FailClosed that itself panics (the event cannot be made safe).
func (c *Chain[E]) Apply(ctx context.Context, e *E) Report {
	var rep Report
	// run applies s and records the step. The report keeps only a value-free
	// form of any error, because Stamp runs on the raw payload and a stage
	// error may quote what it choked on; the original goes to the caller of run.
	run := func(slot Slot, s Stage[E]) (cause error) {
		step := Step{Slot: slot, Stage: s.Name()}
		step.Outcome, cause = safeApply(ctx, s, e)
		if cause != nil {
			step.Err = stageFailure{slot: slot, class: cleanClass(classOf(cause)), panicked: errors.Is(cause, ErrPanic)}
		}
		rep.Steps = append(rep.Steps, step)
		if step.Outcome.Dropped {
			rep.Dropped = true
		}
		return cause
	}

	if c.cfg.Stamp != nil {
		run(SlotStamp, c.cfg.Stamp)
		if rep.Dropped {
			return rep
		}
	}

	if cause := run(SlotSanitize, c.cfg.Sanitize); cause != nil {
		last := &rep.Steps[len(rep.Steps)-1]
		last.FailedClosed = true
		if !safeFailClosed(c.cfg.Sanitize, e, cause) {
			// The event could not be made safe: do not emit it.
			last.Err = errors.Join(last.Err, errFailClosedPanic)
			rep.Dropped = true
		}
	}
	if rep.Dropped {
		return rep
	}

	if c.cfg.Classify != nil {
		run(SlotClassify, c.cfg.Classify)
	}
	return rep
}

func safeApply[E any](ctx context.Context, s Stage[E], e *E) (out Outcome, err error) {
	defer func() {
		if r := recover(); r != nil {
			// Only the type: a panic value can carry payload text.
			out, err = Outcome{}, fmt.Errorf("%w: %T", ErrPanic, r)
		}
	}()
	return s.Apply(ctx, e)
}

var errFailClosedPanic = errors.New("eventstage: FailClosed panicked; event dropped")

func safeFailClosed[E any](s FailClosedStage[E], e *E, cause error) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	s.FailClosed(e, cause)
	return true
}

// Sentinels matched by the error a failed step reports.
var (
	ErrSanitize = errors.New("eventstage: sanitize failed")
	ErrStamp    = errors.New("eventstage: stamp failed")
	ErrClassify = errors.New("eventstage: classify failed")
)

// stageFailure is the value-free error [Report] carries for a failed step: the
// slot and a cleaned failure class (see [ClassedError]), never the stage's own
// error text.
type stageFailure struct {
	slot     Slot
	class    string
	panicked bool
}

func (f stageFailure) Error() string {
	return "eventstage: " + string(f.slot) + " failed: " + f.class
}

func (f stageFailure) Is(target error) bool {
	switch target {
	case ErrSanitize:
		return f.slot == SlotSanitize
	case ErrStamp:
		return f.slot == SlotStamp
	case ErrClassify:
		return f.slot == SlotClassify
	case ErrPanic:
		return f.panicked
	}
	return false
}
