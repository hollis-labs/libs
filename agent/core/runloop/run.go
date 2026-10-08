// Package runloop owns the native request, consume, and tool-settlement cycle.
// Hosts supply the provider, policy, preparation, persistence, and tool ports.
package runloop

import (
	"context"
	"fmt"
)

// Directive is the next action after a run stage. Retry repeats the current
// iteration; Continue starts a new iteration; Finish runs finalization exactly
// once; Terminate exits without finalization because the host has settled the
// interrupted outcome. Proceed advances to the next stage.
type Directive uint8

const (
	Proceed Directive = iota
	Retry
	Continue
	Finish
	Terminate
)

// Ports separates host effects from the loop's ordering. Request constructs
// and starts a provider attempt; Consume drains it and produces a tool turn;
// Settle executes that turn's tools through the host's approval boundary.
// Iteration is called before every attempt, including retries, so host limits,
// accounting and diagnostics see the same index. Finalize persists the result.
// Each stage is synchronous; ports must finish their own resources before
// returning. Termination and errors do not invoke later stages.
type Ports[Attempt, Turn any] struct {
	Iteration func(int)
	Request   func(context.Context) (Attempt, Directive)
	Consume   func(context.Context, Attempt) (Turn, Directive)
	Settle    func(context.Context, Turn) Directive
	Finalize  func(context.Context) Directive
}

// Result reports the last iteration and whether normal finalization ran.
// A terminated run's persisted outcome remains the host's responsibility.
type Result struct {
	Iteration int
	Finalized bool
}

// Execute runs the native loop. Preparation and admission happen before this
// call, through the embedding host. The host's Request port owns cancellation
// and limit classification so accepted turns retain their durable outcome.
// An invalid stage transition is returned as an error, never treated as a
// successful run.
func Execute[Attempt, Turn any](ctx context.Context, ports Ports[Attempt, Turn]) (Result, error) {
	var result Result
	if ports.Request == nil || ports.Consume == nil || ports.Settle == nil || ports.Finalize == nil {
		return result, fmt.Errorf("runloop: incomplete host ports")
	}
	for iteration := 0; ; {
		result.Iteration = iteration
		if ports.Iteration != nil {
			ports.Iteration(iteration)
		}
		attempt, directive := ports.Request(ctx)
		if directive != Proceed {
			switch directive {
			case Retry:
				continue
			case Finish:
				return finalize(ctx, ports, result)
			case Terminate:
				return result, nil
			default:
				return result, transitionError("request", directive)
			}
		}
		turn, directive := ports.Consume(ctx, attempt)
		if directive != Proceed {
			switch directive {
			case Retry:
				continue
			case Continue:
				iteration++
				continue
			case Finish:
				return finalize(ctx, ports, result)
			case Terminate:
				return result, nil
			default:
				return result, transitionError("consume", directive)
			}
		}
		switch directive = ports.Settle(ctx, turn); directive {
		case Continue, Proceed:
			iteration++
		case Finish:
			return finalize(ctx, ports, result)
		case Terminate:
			return result, nil
		default:
			return result, transitionError("settle", directive)
		}
	}
}

func finalize[Attempt, Turn any](ctx context.Context, ports Ports[Attempt, Turn], result Result) (Result, error) {
	switch directive := ports.Finalize(ctx); directive {
	case Proceed, Finish:
		result.Finalized = true
		return result, nil
	case Terminate:
		return result, nil
	default:
		return result, transitionError("finalize", directive)
	}
}

func transitionError(stage string, directive Directive) error {
	return fmt.Errorf("runloop: invalid directive %d from %s", directive, stage)
}
