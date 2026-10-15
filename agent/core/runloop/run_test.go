package runloop_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/hollis-labs/substrate/agent/runloop"
)

func TestRetryKeepsIterationAndContinueAdvances(t *testing.T) {
	var calls []string
	var iterations []int
	requests, consumes, settles, finals := 0, 0, 0, 0
	result, err := runloop.Execute(t.Context(), runloop.Ports[int, string]{
		Iteration: func(i int) { iterations = append(iterations, i) },
		Request: func(context.Context) (int, runloop.Directive) {
			requests++
			calls = append(calls, "request")
			if requests == 1 {
				return requests, runloop.Retry
			}
			return requests, runloop.Proceed
		},
		Consume: func(_ context.Context, attempt int) (string, runloop.Directive) {
			consumes++
			calls = append(calls, "consume")
			if attempt == 2 {
				return "", runloop.Retry
			}
			if attempt == 3 {
				return "", runloop.Continue
			}
			return "tool", runloop.Proceed
		},
		Settle: func(_ context.Context, turn string) runloop.Directive {
			if turn != "tool" {
				t.Fatalf("settle got %q", turn)
			}
			settles++
			calls = append(calls, "settle")
			if settles == 1 {
				return runloop.Continue
			}
			return runloop.Finish
		},
		Finalize: func(context.Context) runloop.Directive {
			finals++
			calls = append(calls, "finalize")
			return runloop.Proceed
		},
	})
	if err != nil || !result.Finalized || result.Iteration != 2 || finals != 1 {
		t.Fatalf("result=%+v err=%v finals=%d", result, err, finals)
	}
	if !reflect.DeepEqual(iterations, []int{0, 0, 0, 1, 2}) {
		t.Fatalf("iterations=%v", iterations)
	}
	want := []string{"request", "request", "consume", "request", "consume", "request", "consume", "settle", "request", "consume", "settle", "finalize"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v", calls)
	}
}

func TestTerminatedStageSkipsLaterEffects(t *testing.T) {
	for _, stage := range []string{"request", "consume", "settle"} {
		t.Run(stage, func(t *testing.T) {
			var calls []string
			decision := func(name string) runloop.Directive {
				calls = append(calls, name)
				if name == stage {
					return runloop.Terminate
				}
				return runloop.Proceed
			}
			result, err := runloop.Execute(t.Context(), runloop.Ports[int, int]{
				Request:  func(context.Context) (int, runloop.Directive) { return 0, decision("request") },
				Consume:  func(context.Context, int) (int, runloop.Directive) { return 0, decision("consume") },
				Settle:   func(context.Context, int) runloop.Directive { return decision("settle") },
				Finalize: func(context.Context) runloop.Directive { t.Fatal("unexpected finalization"); return runloop.Proceed },
			})
			if err != nil || result.Finalized || calls[len(calls)-1] != stage {
				t.Fatalf("result=%+v err=%v calls=%v", result, err, calls)
			}
		})
	}
}

func TestInvalidTransitionCannotComplete(t *testing.T) {
	result, err := runloop.Execute(t.Context(), runloop.Ports[int, int]{
		Request: func(context.Context) (int, runloop.Directive) { return 0, runloop.Continue },
		Consume: func(context.Context, int) (int, runloop.Directive) {
			t.Fatal("consume after invalid request")
			return 0, runloop.Proceed
		},
		Settle: func(context.Context, int) runloop.Directive { return runloop.Finish },
		Finalize: func(context.Context) runloop.Directive {
			t.Fatal("finalize after invalid request")
			return runloop.Proceed
		},
	})
	if err == nil || result.Finalized {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
