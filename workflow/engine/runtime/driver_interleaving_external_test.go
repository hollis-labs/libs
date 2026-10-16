package runtime_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hollis-labs/libs/workflow/engine/graph"
	workflowruntime "github.com/hollis-labs/libs/workflow/engine/runtime"
	"github.com/hollis-labs/libs/workflow/engine/runtime/inmemory"
	"github.com/hollis-labs/libs/workflow/engine/stepkind"
	"github.com/hollis-labs/libs/workflow/engine/stepkind/stepkindtest"
	"github.com/hollis-labs/libs/workflow/engine/values"
)

// Completing the producer immediately after its first snapshot forces the
// driver and the progression coordinator to observe different readiness.
type completeAfterDependencyRead struct {
	*inmemory.Store
	source   workflowruntime.NodeInvocationID
	complete func()
}

func (s *completeAfterDependencyRead) LoadNodeInvocation(ctx context.Context, id workflowruntime.NodeInvocationID) (workflowruntime.NodeInvocationSnapshot, error) {
	snapshot, err := s.Store.LoadNodeInvocation(ctx, id)
	if err == nil && id == s.source && s.complete != nil {
		complete := s.complete
		s.complete = nil
		complete()
	}
	return snapshot, err
}

func TestNodeDriverDependencyCompletionCannotPublishUnboundReadyNode(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	store := inmemory.NewStore()
	createRun(t, store, "driver-interleaving", base)
	run, err := store.TransitionRun(ctx, workflowruntime.RunTransitionRequest{RunID: "driver-interleaving", ExpectedGeneration: 1, To: workflowruntime.RunRunning, At: base})
	if err != nil {
		t.Fatal(err)
	}
	source := invocationID(run.Snapshot.ID, "source")
	target := invocationID(run.Snapshot.ID, "target")
	createNode(t, store, source, workflowruntime.NodePending, 0, base)
	createNode(t, store, target, workflowruntime.NodePending, 0, base)
	workflow := graph.Graph{ID: "plan", Version: "v1", Nodes: []graph.Node{
		{ID: "source", Kind: "safe", KindVersion: "v1"},
		{ID: "target", Kind: "safe", KindVersion: "v1", Needs: []graph.Need{{Node: "source"}}, InputBindings: map[string]graph.Binding{"value": {Kind: graph.BindingLiteral, Literal: "bound"}}},
	}}
	plan := driverRecoveryPlan(t, run.Snapshot, workflow)
	interleaved := &completeAfterDependencyRead{Store: store, source: source}
	interleaved.complete = func() { makeTerminalNode(t, store, source, workflowruntime.NodeSucceeded, base.Add(time.Second)) }
	driver := &workflowruntime.NodeDriver{Store: interleaved, Inputs: store, Control: store, Registry: driverRegistry(t)}
	request := workflowruntime.DriveNodeRequest{Run: run.Snapshot, Plan: plan, InvocationID: target, Node: workflow.Nodes[1], ExpressionContext: values.ExpressionContext{}, At: base.Add(2 * time.Second)}
	first, firstErr := driver.Drive(ctx, request)
	snapshot, err := store.LoadNodeInvocation(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status == workflowruntime.NodeReady && snapshot.Inputs == nil {
		t.Fatalf("dependency completion published Ready with unbound inputs; Drive error=%v", firstErr)
	}
	if firstErr != nil || first.Progressed.Disposition != workflowruntime.ReadinessWaiting {
		t.Fatalf("concurrent completion must defer without failing execution: %#v, %v", first, firstErr)
	}
	// A subsequent drive must bind and progress normally, without a deadline
	// change, manual repair, or an execution retry.
	request.At = base.Add(3 * time.Second)
	result, err := driver.Drive(ctx, request)
	if err != nil || result.Progressed.Snapshot.Status != workflowruntime.NodeReady || result.Progressed.Snapshot.Inputs == nil {
		t.Fatalf("subsequent drive=%#v, %v; first error=%v", result, err, firstErr)
	}
	bound, err := store.LoadValues(ctx, *result.Progressed.Snapshot.Inputs)
	if err != nil || bound["value"].Inline != "bound" {
		t.Fatalf("bound inputs=%#v, %v", bound, err)
	}
	// Repeated driving and direct progression preserve the immutable binding.
	request.At = base.Add(4 * time.Second)
	replayed, err := driver.Drive(ctx, request)
	if err != nil || replayed.Binding != nil || replayed.Progressed.Snapshot.Generation != result.Progressed.Snapshot.Generation || *replayed.Progressed.Snapshot.Inputs != *result.Progressed.Snapshot.Inputs {
		t.Fatalf("replayed drive changed binding: %#v, %v", replayed, err)
	}
	progressed, err := workflowruntime.NewProgressionCoordinator(store, nil).ProgressNode(ctx, workflowruntime.ProgressNodeRequest{InvocationID: target, At: request.At})
	if err != nil || progressed.Event != nil || progressed.Snapshot.Generation != result.Progressed.Snapshot.Generation || *progressed.Snapshot.Inputs != *result.Progressed.Snapshot.Inputs {
		t.Fatalf("direct progression changed binding: %#v, %v", progressed, err)
	}
}

func TestProgressNodeRefusesUnboundInputs(t *testing.T) {
	for _, status := range []workflowruntime.NodeStatus{workflowruntime.NodePending, workflowruntime.NodeReady} {
		t.Run(string(status), func(t *testing.T) {
			ctx := context.Background()
			store := inmemory.NewStore()
			now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
			id := invocationID("unbound-progression", "target")
			createNode(t, store, id, status, 0, now)
			before, err := store.LoadNodeInvocation(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			_, err = workflowruntime.NewProgressionCoordinator(store, nil).ProgressNode(ctx, workflowruntime.ProgressNodeRequest{InvocationID: id, At: now.Add(time.Second)})
			if !errors.Is(err, workflowruntime.ErrInputsUnbound) {
				t.Fatalf("unbound progression error = %v", err)
			}
			after, err := store.LoadNodeInvocation(ctx, id)
			if err != nil || after.Generation != before.Generation || after.Status != status || after.Inputs != nil {
				t.Fatalf("unbound progression mutated invocation: %#v, %v", after, err)
			}
		})
	}
}

func TestStepDispatcherRejectsUnboundInputsBeforeAttempt(t *testing.T) {
	ctx := context.Background()
	store := inmemory.NewStore()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	id := invocationID("unbound-dispatch", "target")
	// Simulate a legacy Ready record published before the readiness guard.
	createNode(t, store, id, workflowruntime.NodeReady, 0, now)
	claim, ok, err := workflowruntime.NewReadyQueueCoordinator(store, nil).ClaimNext(ctx, workflowruntime.ReadyClaimRequest{
		Owner: "worker", Token: "claim", IdempotencyKey: "unbound-claim", Now: now.Add(time.Second), LeaseUntil: now.Add(time.Hour),
	})
	if err != nil || !ok {
		t.Fatalf("claim = %#v, %v, %v", claim, ok, err)
	}
	registry := stepkind.NewRegistry()
	kind := stepkindtest.NewNoopKind("fixture", "v1")
	kind.ExecuteFunc = func(context.Context, stepkind.PreparedInvocation) (stepkind.StepResult, error) {
		t.Fatal("executed with unbound inputs")
		return stepkind.StepResult{}, nil
	}
	if err := registry.Register(kind); err != nil {
		t.Fatal(err)
	}
	dispatcher, err := workflowruntime.NewStepDispatcher(workflowruntime.DispatcherOptions{Store: store, Registry: registry, Now: func() time.Time { return now.Add(2 * time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	result, err := dispatcher.Dispatch(ctx, workflowruntime.DispatchRequest{Claim: claim, Node: graph.Node{ID: id.NodeID, Kind: "fixture", KindVersion: "v1"}})
	var typed *workflowruntime.DispatchError
	if !errors.Is(err, workflowruntime.ErrInputsUnbound) || !errors.As(err, &typed) || typed.Stage != workflowruntime.DispatchValidateInput {
		t.Fatalf("unbound dispatch error = %T %v", err, err)
	}
	loaded, loadErr := store.LoadNodeInvocation(ctx, id)
	attempts, listErr := store.ListAttempts(ctx, id)
	if loadErr != nil || listErr != nil || loaded.Status != workflowruntime.NodeReady || loaded.Lease != nil || result.Node.Lease != nil || len(attempts) != 0 {
		t.Fatalf("unbound dispatch started an attempt or stranded a claim: %#v, %#v, %v, %v", loaded, attempts, loadErr, listErr)
	}
}
