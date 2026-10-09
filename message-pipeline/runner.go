package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
	"unicode/utf8"
)

// Options requires an application-selected original byte ceiling. Stage workers
// are bounded even when an implementation ignores cancellation. Such a worker
// must eventually return; Go cannot forcibly terminate it. Late results are ignored.
type Options struct {
	MaxOriginalBytes    int
	MaxConcurrentStages int
}
type Runner struct {
	specs   []StageSpec
	store   ResultStore
	options Options
	runs    chan struct{}
	workers chan struct{}
}

func New(specs []StageSpec, store ResultStore, options Options) (*Runner, error) {
	if store == nil || options.MaxOriginalBytes <= 0 || len(specs) == 0 || len(specs) > MaxEntries {
		return nil, ErrInvalid
	}
	if options.MaxConcurrentStages == 0 {
		options.MaxConcurrentStages = 1
	}
	if options.MaxConcurrentStages < 1 || options.MaxConcurrentStages > MaxEntries {
		return nil, ErrInvalid
	}
	owned := append([]StageSpec(nil), specs...)
	seen := make(map[string]bool)
	for i := range owned {
		s := &owned[i]
		if s.FailMode == "" {
			s.FailMode = FailOpen
		}
		if !validIdentity(s.ID) || !validIdentity(s.Version) || !validIdentity(s.ConfigDigest) || !validIdentity(s.InstructionDigest) || s.Timeout <= 0 || s.Stage == nil || s.FailMode != FailOpen || seen[s.ID] {
			return nil, ErrInvalid
		}
		seen[s.ID] = true
	}
	sort.SliceStable(owned, func(i, j int) bool { return owned[i].Priority < owned[j].Priority })
	return &Runner{specs: owned, store: store, options: options, runs: make(chan struct{}, 1), workers: make(chan struct{}, options.MaxConcurrentStages)}, nil
}

// Run does not deliver a message, advance a cursor, or execute follow-ups.
// Per-run serialization prevents duplicate execution within this Runner. Other
// processes must coordinate through adapter ownership; provider exactly-once is
// not promised. Original and every stage's input state are immutable snapshots.
func (r *Runner) Run(ctx context.Context, message Message, initial State) (Settlement, error) {
	pending := Settlement{Message: message, State: initial.Clone(), Disposition: Hold}
	if !validText(message.Identity.Source, 4096, false) || !validText(message.Identity.Message, 4096, false) || !utf8.ValidString(message.Original) || len(message.Original) > r.options.MaxOriginalBytes || initial.Validate() != nil || len(initial.Traces)+len(r.specs) > MaxEntries {
		return pending, ErrInvalid
	}
	for _, s := range []string{message.Attribution.Sender, message.Attribution.AgentID, message.Attribution.SessionID, message.Attribution.TurnID, message.Attribution.OutputID} {
		if !validText(s, 4096, true) {
			return pending, ErrInvalid
		}
	}
	select {
	case r.runs <- struct{}{}:
		defer func() { <-r.runs }()
	case <-ctx.Done():
		return pending, pendingError(ctx.Err())
	}
	state := initial.Clone()
	for _, spec := range r.specs {
		if ctx.Err() != nil {
			pending.State = state
			return pending, pendingError(ctx.Err())
		}
		key := Key{Message: message.Identity, StageID: spec.ID, StageVersion: spec.Version, ConfigDigest: spec.ConfigDigest, InstructionDigest: spec.InstructionDigest, TimeoutNS: int64(spec.Timeout), FailMode: spec.FailMode}
		in := Input{Message: message, State: state.Clone()}
		inputDigest := digest(in)
		record, found, err := r.store.Get(ctx, key)
		if ctx.Err() != nil {
			pending.State = state
			return pending, pendingError(ctx.Err())
		}
		if err != nil {
			pending.State = state
			return pending, pendingError(err)
		}
		if !found {
			record, err = r.execute(ctx, spec, in)
			if err != nil {
				pending.State = state
				return pending, err
			}
			record.InputDigest = inputDigest
			// Verify the composed bounds before persistence. An excessive output becomes
			// a failed marker, preserving previous annotations and the original message.
			if _, err = appendRecord(state, spec, record); err != nil {
				record = failureRecord(spec, AnnotationLimit, record.Trace.DurationMS)
				record.InputDigest = inputDigest
				if _, boundErr := appendRecord(state, spec, record); boundErr != nil {
					pending.State = state
					return pending, pendingError(ErrInvalid)
				}
			}
			if ctx.Err() != nil {
				pending.State = state
				return pending, pendingError(ctx.Err())
			}
			record, err = r.store.PutIfAbsent(ctx, key, record.Clone())
			if ctx.Err() != nil {
				pending.State = state
				return pending, pendingError(ctx.Err())
			}
			if err != nil {
				pending.State = state
				return pending, pendingError(err)
			}
		}
		if record.InputDigest != inputDigest {
			pending.State = state
			return pending, ErrConflict
		}
		state, err = appendRecord(state, spec, record)
		if err != nil {
			pending.State = state
			return pending, pendingError(ErrInvalid)
		}
	}
	if ctx.Err() != nil {
		pending.State = state
		return pending, pendingError(ctx.Err())
	}
	return Settlement{Message: message, State: state.Clone(), Disposition: Pass}, nil
}
func pendingError(cause error) error { return fmt.Errorf("%w: %w", ErrPending, cause) }
func failureRecord(s StageSpec, code FailureCode, duration int64) Record {
	outcome := Failed
	if code == StageTimeout {
		outcome = TimedOut
	}
	return Record{SchemaVersion: 1, Result: Result{Disposition: Pass}, Trace: Trace{StageID: s.ID, StageVersion: s.Version, Outcome: outcome, DurationMS: duration, FailureCode: code}}
}

type stageAnswer struct {
	result   Result
	err      error
	panicked bool
}

func (r *Runner) execute(parent context.Context, s StageSpec, in Input) (Record, error) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(parent, s.Timeout)
	defer cancel()
	// The same stage timeout includes waiting for a bounded worker slot.
	select {
	case r.workers <- struct{}{}:
	case <-ctx.Done():
		if parent.Err() != nil {
			return Record{}, pendingError(parent.Err())
		}
		return failureRecord(s, StageTimeout, time.Since(start).Milliseconds()), nil
	}
	answers := make(chan stageAnswer, 1)
	go func() {
		defer func() { <-r.workers }()
		a := stageAnswer{}
		defer func() {
			if recover() != nil {
				a.panicked = true
			}
			answers <- a
		}()
		a.result, a.err = s.Stage.Run(ctx, Input{Message: in.Message, State: in.State.Clone()})
		a.result = a.result.clone()
	}()
	select {
	case <-ctx.Done():
		if parent.Err() != nil {
			return Record{}, pendingError(parent.Err())
		}
		return failureRecord(s, StageTimeout, time.Since(start).Milliseconds()), nil
	case a := <-answers:
		if parent.Err() != nil {
			return Record{}, pendingError(parent.Err())
		}
		if ctx.Err() != nil {
			return failureRecord(s, StageTimeout, time.Since(start).Milliseconds()), nil
		}
		duration := time.Since(start).Milliseconds()
		if a.panicked {
			return failureRecord(s, StagePanic, duration), nil
		}
		if a.err != nil {
			code := StageError
			if errors.Is(a.err, ErrRefused) {
				code = StageRefused
			}
			return failureRecord(s, code, duration), nil
		}
		if a.result.Disposition == "" {
			a.result.Disposition = Pass
		}
		if validateResult(a.result) != nil {
			return failureRecord(s, InvalidOutput, duration), nil
		}
		return Record{SchemaVersion: 1, Result: a.result.clone(), Trace: Trace{StageID: s.ID, StageVersion: s.Version, Outcome: Passed, DurationMS: duration}}, nil
	}
}
func validateResult(result Result) error {
	// Filtering and follow-up execution are not active in this MVP contract.
	if result.Disposition != Pass || len(result.FollowUps) != 0 || len(result.Summaries) > MaxEntries {
		return ErrInvalid
	}
	for _, summary := range result.Summaries {
		if !validText(summary.Text, MaxSummaryCharacters, false) {
			return ErrInvalid
		}
	}
	return nil
}
func appendRecord(state State, s StageSpec, record Record) (State, error) {
	if record.SchemaVersion != 1 || record.Trace.StageID != s.ID || record.Trace.StageVersion != s.Version || validateResult(record.Result) != nil {
		return State{}, ErrInvalid
	}
	if record.Trace.Outcome != Passed && len(record.Result.Summaries) != 0 {
		return State{}, ErrInvalid
	}
	next := state.Clone()
	for _, summary := range record.Result.Summaries {
		next.Annotations = append(next.Annotations, Annotation{SchemaVersion: 1, StageID: s.ID, StageVersion: s.Version, Kind: "summary", Summary: summary})
	}
	next.Traces = append(next.Traces, record.Trace)
	if err := next.Validate(); err != nil {
		return State{}, err
	}
	return next, nil
}
