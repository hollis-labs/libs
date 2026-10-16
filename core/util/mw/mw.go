package mw

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Handler is the one contract: a context and a request in, a response and an error out.
type Handler[Req, Resp any] func(context.Context, Req) (Resp, error)

// Middleware wraps a Handler. Build one once; apply it to as many handlers as needed.
type Middleware[Req, Resp any] func(next Handler[Req, Resp]) Handler[Req, Resp]

// Chain composes middleware; the first listed is outermost.
func Chain[Req, Resp any](mws ...Middleware[Req, Resp]) Middleware[Req, Resp] {
	return func(next Handler[Req, Resp]) Handler[Req, Resp] {
		for i := len(mws) - 1; i >= 0; i-- {
			next = mws[i](next)
		}
		return next
	}
}

// Member is a middleware with its descriptor and mode.
type Member[Req, Resp any] struct {
	Desc Descriptor
	MW   Middleware[Req, Resp]
	Mode Mode
}

// Stack is an assembled, validated chain.
type Stack[Req, Resp any] struct {
	members []Member[Req, Resp]
	opts    Options
}

// Assemble validates the members (outermost first) and returns a Stack.
func Assemble[Req, Resp any](opts Options, members ...Member[Req, Resp]) (*Stack[Req, Resp], error) {
	descs := make([]Descriptor, len(members))
	for i, m := range members {
		descs[i] = m.Desc
		if m.MW == nil {
			return nil, fmt.Errorf("%s: nil middleware", nameOr(m.Desc.Name, i))
		}
	}
	if err := Validate(descs, opts); err != nil {
		return nil, err
	}
	return &Stack[Req, Resp]{members: members, opts: opts}, nil
}

// Wrap returns h behind the whole stack. Each member's middleware function is
// invoked per call (so it can be wrapped with call-local tracking); keep
// expensive state in the closure that builds the Middleware, not inside it.
func (s *Stack[Req, Resp]) Wrap(h Handler[Req, Resp]) Handler[Req, Resp] {
	guarded := make([]Middleware[Req, Resp], 0, len(s.members))
	for _, m := range s.members {
		if m.Mode == Off {
			continue
		}
		guarded = append(guarded, guard(m, s.opts.Observer))
	}
	return Chain(guarded...)(h)
}

// Explain prints the chain.
func (s *Stack[Req, Resp]) Explain() string {
	descs := make([]Descriptor, len(s.members))
	modes := make([]Mode, len(s.members))
	for i, m := range s.members {
		descs[i], modes[i] = m.Desc, m.Mode
	}
	return Explain(descs, modes)
}

func emit(obs func(Event), d Descriptor, mode Mode, o Outcome, err error) {
	if obs != nil {
		obs(Event{Name: d.Name, Phase: d.Phase, Mode: mode, Outcome: o, Err: err})
	}
}

func guard[Req, Resp any](m Member[Req, Resp], obs func(Event)) Middleware[Req, Resp] {
	if m.Mode == Shadow {
		return shadowGuard(m, obs)
	}
	return func(next Handler[Req, Resp]) Handler[Req, Resp] {
		return func(ctx context.Context, req Req) (resp Resp, err error) {
			var (
				called, downPanic bool
				nresp             Resp
				nerr              error
			)
			tracked := func(c context.Context, r Req) (Resp, error) {
				called = true
				defer func() {
					if p := recover(); p != nil {
						downPanic = true
						panic(p) // not ours; let an outer recover see it
					}
				}()
				nresp, nerr = next(c, r)
				return nresp, nerr
			}
			fault := func(cause error) (Resp, error) {
				emit(obs, m.Desc, Enforce, Faulted, cause)
				var zero Resp
				switch {
				case m.Desc.Failure == Open && called:
					return nresp, nerr // downstream already ran; its result stands
				case m.Desc.Failure == Open:
					return next(ctx, req)
				default:
					return zero, &Fault{Err: fmt.Errorf("%s: %w", m.Desc.Name, cause), Owner: m.Desc.Name}
				}
			}
			defer func() {
				if p := recover(); p != nil {
					if downPanic {
						panic(p)
					}
					resp, err = fault(fmt.Errorf("panic: %v", p))
				}
			}()
			resp, err = m.MW(tracked)(ctx, req)
			if f := AsFault(err); f != nil && f.Owner == "" {
				return fault(f.Err)
			}
			switch {
			case err != nil && !called:
				emit(obs, m.Desc, Enforce, Denied, err)
			default:
				emit(obs, m.Desc, Enforce, Passed, nil)
			}
			return resp, err
		}
	}
}

// shadowGuard runs the middleware against a stub next to learn what it would
// do, records that, discards the result, and calls the real next unchanged.
// The middleware's own side effects (counters, logs) still happen. A
// middleware that needs the real response will see a zero one and may fault;
// that is recorded, not propagated.
func shadowGuard[Req, Resp any](m Member[Req, Resp], obs func(Event)) Middleware[Req, Resp] {
	return func(next Handler[Req, Resp]) Handler[Req, Resp] {
		return func(ctx context.Context, req Req) (Resp, error) {
			stubbed := false
			stub := func(context.Context, Req) (Resp, error) {
				stubbed = true
				var z Resp
				return z, nil
			}
			func() {
				defer func() {
					if p := recover(); p != nil {
						emit(obs, m.Desc, Shadow, Faulted, fmt.Errorf("panic: %v", p))
					}
				}()
				_, err := m.MW(stub)(ctx, req)
				switch {
				case AsFault(err) != nil && AsFault(err).Owner == "":
					emit(obs, m.Desc, Shadow, Faulted, err)
				case err != nil && !stubbed:
					emit(obs, m.Desc, Shadow, Denied, err)
				default:
					emit(obs, m.Desc, Shadow, Passed, nil)
				}
			}()
			return next(ctx, req)
		}
	}
}

// ErrPanic is returned by Recover for a recovered panic.
var ErrPanic = errors.New("panic recovered")

// RecoverMember converts a downstream panic into an error wrapping ErrPanic.
func RecoverMember[Req, Resp any]() Member[Req, Resp] {
	return Member[Req, Resp]{
		Desc: Descriptor{Name: "recover", Phase: PhaseRecover, Failure: Closed},
		MW: func(next Handler[Req, Resp]) Handler[Req, Resp] {
			return func(ctx context.Context, req Req) (resp Resp, err error) {
				defer func() {
					if p := recover(); p != nil {
						err = fmt.Errorf("%w: %v", ErrPanic, p)
					}
				}()
				return next(ctx, req)
			}
		},
	}
}

// TimeoutMember bounds the call with a context deadline. The handler must honor ctx.
func TimeoutMember[Req, Resp any](d time.Duration) Member[Req, Resp] {
	return Member[Req, Resp]{
		Desc: Descriptor{Name: "timeout", Phase: PhaseLimits, Failure: Closed},
		MW: func(next Handler[Req, Resp]) Handler[Req, Resp] {
			return func(ctx context.Context, req Req) (Resp, error) {
				ctx, cancel := context.WithTimeout(ctx, d)
				defer cancel()
				return next(ctx, req)
			}
		},
	}
}
