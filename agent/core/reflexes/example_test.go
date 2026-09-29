package reflexes_test

import (
	"context"
	"fmt"

	reflexes "github.com/hollis-labs/go-reflexes"
)

// rows is a fake Source and KindCatalog.
type rows struct {
	reflexes []reflexes.Reflex
	kinds    []reflexes.ActionKind
}

func (r rows) Candidates(context.Context, string, string) ([]reflexes.Reflex, error) {
	return r.reflexes, nil
}

func (r rows) ActionKinds(context.Context) ([]reflexes.ActionKind, error) { return r.kinds, nil }

func ExampleEngine_Run() {
	src := rows{
		reflexes: []reflexes.Reflex{
			{ID: "low", Name: "route-low", Priority: 10, TriggerKind: "event", TriggerSpec: `{"name":"probe"}`, ActionKind: "force_tool_choice", ActionSpec: `{"tool_name":"a"}`},
			{ID: "high", Name: "route-high", Priority: 90, TriggerKind: "event", TriggerSpec: `{"name":"probe"}`, ActionKind: "force_tool_choice", ActionSpec: `{"tool_name":"b"}`},
		},
		kinds: []reflexes.ActionKind{{Name: "force_tool_choice", CombiningAlgorithm: "first_applicable"}},
	}
	engine, _ := reflexes.New(src, src)
	res, _ := engine.Run(context.Background(), reflexes.RunInput{
		State: &reflexes.State{Events: []reflexes.EventSignal{{EventType: "probe"}}},
	})
	fmt.Println(len(res.Applied.Actions), res.Applied.Actions[0].ReflexName, res.Considered)
	// Output: 1 route-high 2
}

// loopEngine stands in for an application's loop runtime.
type loopEngine struct{}

func (loopEngine) ResumeLoopRun(_ context.Context, id string) error {
	fmt.Println("resuming", id)
	return nil
}

// Register resume_loop_run at the composition root: the handler is a closure
// over the application's loop engine, so the library needs no Resumer
// interface and no import of the application.
func ExampleExecutor_Handle() {
	src := rows{
		reflexes: []reflexes.Reflex{{
			ID: "wake", Name: "resume-when-unblocked", TriggerKind: "event", TriggerSpec: `{"name":"loop_unblocked"}`,
			ActionKind: "resume_loop_run", ActionSpec: `{"loop_run_id":"lr-1"}`,
		}},
		kinds: []reflexes.ActionKind{{Name: "resume_loop_run", CombiningAlgorithm: "all_applicable"}},
	}
	engine, _ := reflexes.New(src, src)

	loops := loopEngine{}
	engine.Executor().Handle("resume_loop_run", reflexes.HandlerFunc(func(ctx context.Context, f reflexes.Firing) error {
		id, _ := f.Spec["loop_run_id"].(string)
		return loops.ResumeLoopRun(ctx, id)
	}), reflexes.WithPhase(reflexes.PhaseAfterEmit))

	res, err := engine.Run(context.Background(), reflexes.RunInput{
		Kinds: []string{"resume_loop_run"},
		State: &reflexes.State{Events: []reflexes.EventSignal{{EventType: "loop_unblocked"}}},
	})
	fmt.Println(len(res.Applied.Actions), res.Considered, err)
	// Output:
	// resuming lr-1
	// 1 1 <nil>
}

func ExampleEvaluateTrigger() {
	state := reflexes.State{Messages: []reflexes.MessageSignal{{ToolCalls: 0}, {ToolCalls: 0}, {ToolCalls: 0}}}
	fired, err := reflexes.EvaluateTrigger("predicate", `{"kind":"tool_calls_window","window":3,"op":"=","value":0}`, state)
	fmt.Println(fired, err)
	// Output: true <nil>
}

func ExampleEffectiveCooldown() {
	zero, sixty := int64(0), int64(60)
	fmt.Println(reflexes.EffectiveCooldown(nil, nil))
	fmt.Println(reflexes.EffectiveCooldown(&sixty, nil))
	fmt.Println(reflexes.EffectiveCooldown(&sixty, &zero))
	// Output:
	// 15m0s
	// 1m0s
	// 0s
}

func ExampleResolve() {
	exec := &reflexes.Executor{}
	cands := []reflexes.Reflex{
		{ID: "h", Name: "halt", Priority: 1, TriggerKind: "event", TriggerSpec: `{"name":"probe"}`, ActionKind: "halt_session"},
		{ID: "r", Name: "remind", Priority: 99, TriggerKind: "event", TriggerSpec: `{"name":"probe"}`, ActionKind: "inject_reminder"},
	}
	lookup := func(_ context.Context, kind string) (*reflexes.ActionKind, error) {
		algo := "all_applicable"
		if kind == "halt_session" {
			algo = "deny_overrides"
		}
		return &reflexes.ActionKind{Name: kind, CombiningAlgorithm: algo}, nil
	}
	applied, _, _ := reflexes.Resolve(context.Background(), cands,
		reflexes.State{Events: []reflexes.EventSignal{{EventType: "probe"}}}, exec, nil, lookup)
	fmt.Println(len(applied.Actions), applied.Actions[0].ActionKind)
	// Output: 1 halt_session
}
