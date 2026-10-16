package runtime_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hollis-labs/libs/workflow/engine/graph"
	rt "github.com/hollis-labs/libs/workflow/engine/runtime"
	"github.com/hollis-labs/libs/workflow/engine/runtime/inmemory"
)

type unavailableClaimRelease struct{ *inmemory.Store }

func (s *unavailableClaimRelease) ReleaseNodeClaim(context.Context, rt.ReleaseClaimRequest) error {
	return errors.New("injected release failure")
}

func TestBlockedUnboundRecoveryDoesNotDependOnSeparateClaimRelease(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	store := inmemory.NewStore()
	id := invocationID("blocked-release-failure", "target")
	createNode(t, store, id, rt.NodeReady, 0, now)
	if _, err := store.TransitionRun(ctx, rt.RunTransitionRequest{RunID: id.RunID, ExpectedGeneration: 1, To: rt.RunRunning, At: now}); err != nil {
		t.Fatal(err)
	}
	claim, ok, err := rt.NewReadyQueueCoordinator(store, nil).ClaimNext(ctx, rt.ReadyClaimRequest{Owner: "worker", Token: "claim", IdempotencyKey: "blocked-claim", Now: now.Add(time.Second), LeaseUntil: now.Add(time.Minute)})
	if err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}
	registry := driverRegistry(t)
	dispatcher, err := rt.NewStepDispatcher(rt.DispatcherOptions{Store: &unavailableClaimRelease{store}, Registry: registry, Now: func() time.Time { return now.Add(2 * time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	node := graph.Node{ID: id.NodeID, Kind: "safe", KindVersion: "v1", InputBindings: map[string]graph.Binding{"value": {Kind: graph.BindingLiteral, Literal: "bound"}}}
	_, err = dispatcher.Dispatch(ctx, rt.DispatchRequest{Claim: claim, Node: node})
	if !errors.Is(err, rt.ErrInputsUnbound) {
		t.Fatalf("dispatch: %v", err)
	}
	before, _ := store.LoadNodeInvocation(ctx, id)
	if before.Status != rt.NodeBlocked || before.Lease != nil {
		t.Fatalf("demotion must atomically clear lease: %#v", before)
	}
	coordinator := rt.RecoveryCoordinator{Store: store, Recovery: store, Inputs: store, Control: store, Plans: staticRecoveryPlans{graph: graph.Graph{ID: "plan", Version: "v1", Nodes: []graph.Node{node}}}, Registry: registry}
	_, err = coordinator.Recover(ctx, rt.RecoveryRequest{Now: now.Add(2 * time.Minute)})
	after, _ := store.LoadNodeInvocation(ctx, id)
	if err != nil || after.Status != rt.NodeReady || after.Lease != nil || after.Inputs == nil {
		t.Fatalf("recovery past lease expiry: err=%v status=%s lease=%+v inputs=%v", err, after.Status, after.Lease, after.Inputs)
	}
}
