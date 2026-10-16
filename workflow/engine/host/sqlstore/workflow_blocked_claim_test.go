package sqlstore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/hollis-labs/libs/workflow/engine/graph"
	rt "github.com/hollis-labs/libs/workflow/engine/runtime"
	"github.com/hollis-labs/libs/workflow/engine/stepkind"
	"github.com/hollis-labs/libs/workflow/engine/stepkind/stepkindtest"
)

type unavailableClaimRelease struct{ *Store }

func (s *unavailableClaimRelease) ReleaseNodeClaim(context.Context, rt.ReleaseClaimRequest) error {
	return errors.New("injected release failure")
}

func TestWorkflowBlockedDemotionReleasesLeaseAndHoldersAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blocked-recovery.db")
	db, state := openWorkflowStateTest(t, path)
	ctx, base := context.Background(), workflowTestTime()
	run := createWorkflowTestRun(t, state, "blocked-recovery", base)
	if _, err := state.TransitionRun(ctx, rt.RunTransitionRequest{RunID: run.ID, ExpectedGeneration: run.Generation, To: rt.RunRunning, At: base}); err != nil {
		t.Fatal(err)
	}
	node := createWorkflowTestNode(t, state, run.ID, "work", base)
	ready, err := state.TransitionNode(ctx, rt.NodeTransitionRequest{InvocationID: node.ID, ExpectedGeneration: node.Generation, To: rt.NodeReady, At: base})
	if err != nil {
		t.Fatal(err)
	}
	requirements := workflowSchedulerRequirements(t, run.ID, rt.SchedulerLimits{Workers: 1}, rt.SchedulerDemand{})
	admitted, err := state.AdmitNode(ctx, workflowSchedulerAdmission(ready.Snapshot.ID, "blocked", requirements, base.Add(time.Second), base.Add(time.Minute)))
	if err != nil || !admitted.Claim.Acquired {
		t.Fatalf("admit: %#v, %v", admitted, err)
	}
	current, err := state.LoadNodeInvocation(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	proof := rt.ClaimProof{Owner: admitted.Claim.Lease.Owner, Token: admitted.Claim.Lease.Token, Generation: admitted.Claim.Lease.Generation}
	transition := rt.NodeTransitionRequest{InvocationID: node.ID, ExpectedGeneration: current.Generation, To: rt.NodeBlocked, Blocked: &rt.BlockedReason{Code: "inputs_unbound", Message: "node inputs require durable binding"}, Claim: &proof, At: base.Add(2 * time.Second)}
	// A failed event append must roll back the status, lease and scheduler release.
	if _, err := db.DB().Exec(`CREATE TRIGGER reject_blocked_event BEFORE INSERT ON workflow_events BEGIN SELECT RAISE(ABORT, 'injected event failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := state.TransitionNode(ctx, transition); err == nil {
		t.Fatal("expected injected event failure")
	}
	rolledBack, err := state.LoadNodeInvocation(ctx, node.ID)
	if err != nil || rolledBack.Status != rt.NodeReady || rolledBack.Generation != current.Generation || rolledBack.Lease == nil {
		t.Fatalf("rollback: %#v, %v", rolledBack, err)
	}
	resources, err := state.InspectSchedulerResources(ctx, rt.SchedulerResourceQuery{RunID: run.ID, Now: base.Add(2 * time.Second)})
	if err != nil || len(resources.Holders) == 0 {
		t.Fatalf("rollback lost holders: %#v, %v", resources, err)
	}
	if _, err := db.DB().Exec(`DROP TRIGGER reject_blocked_event`); err != nil {
		t.Fatal(err)
	}
	// Generation and owner fences still reject stale or foreign callers.
	stale := transition
	stale.ExpectedGeneration--
	if _, err := state.TransitionNode(ctx, stale); !errors.Is(err, rt.ErrCASMismatch) {
		t.Fatalf("stale generation: %v", err)
	}
	foreign := proof
	foreign.Owner = "other-worker"
	stale = transition
	stale.Claim = &foreign
	if _, err := state.TransitionNode(ctx, stale); !errors.Is(err, rt.ErrClaimMismatch) {
		t.Fatalf("foreign claim: %v", err)
	}
	registry := stepkind.NewRegistry()
	if err := registry.Register(stepkindtest.NewNoopKind("test", "v1")); err != nil {
		t.Fatal(err)
	}
	definition := graph.Node{ID: node.ID.NodeID, Kind: "test", KindVersion: "v1"}
	// Release is deliberately unavailable: dispatch must not need a second write.
	dispatcher, err := rt.NewStepDispatcher(rt.DispatcherOptions{Store: &unavailableClaimRelease{state}, Registry: registry, Now: func() time.Time { return base.Add(2 * time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	claim := rt.ReadyClaim{Candidate: rt.ReadyCandidate{InvocationID: node.ID}, Lease: *admitted.Claim.Lease}
	_, err = dispatcher.Dispatch(ctx, rt.DispatchRequest{Claim: claim, Node: definition})
	if !errors.Is(err, rt.ErrInputsUnbound) {
		t.Fatalf("dispatch: %v", err)
	}
	blocked, err := state.LoadNodeInvocation(ctx, node.ID)
	if err != nil || blocked.Status != rt.NodeBlocked || blocked.Lease != nil || blocked.LatestAttempt != 0 {
		t.Fatalf("atomic demotion: %#v, %v", blocked, err)
	}
	resources, err = state.InspectSchedulerResources(ctx, rt.SchedulerResourceQuery{RunID: run.ID, Now: base.Add(2 * time.Second)})
	if err != nil || len(resources.Holders) != 0 {
		t.Fatalf("demotion holders: %#v, %v", resources, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedDB, err := openTestDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopenedDB.Close() })
	reopened, err := newTestStore(reopenedDB)
	if err != nil {
		t.Fatal(err)
	}
	workflow := graph.Graph{ID: run.Plan.ID, Version: run.Plan.Version, Digest: run.Plan.Digest, Nodes: []graph.Node{definition}}
	recovery := rt.RecoveryCoordinator{Store: reopened, Recovery: reopened, Inputs: reopened, Control: reopened, Plans: workflowPersistenceRecoveryPlans{graph: workflow}, Registry: registry}
	if _, err := recovery.Recover(ctx, rt.RecoveryRequest{Now: base.Add(2 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	recovered, err := reopened.LoadNodeInvocation(ctx, node.ID)
	if err != nil || recovered.Status != rt.NodeReady || recovered.Lease != nil || recovered.Inputs == nil {
		t.Fatalf("reopened recovery: %#v, %v", recovered, err)
	}

	claim, acquired, err := rt.NewReadyQueueCoordinator(reopened, nil).ClaimNext(ctx, rt.ReadyClaimRequest{Owner: "restarted", Token: "new-claim", IdempotencyKey: "recovered-claim", Now: base.Add(2 * time.Minute), LeaseUntil: base.Add(3 * time.Minute)})
	if err != nil || !acquired {
		t.Fatalf("recovered claim: %v, %v", acquired, err)
	}
	dispatcher, err = rt.NewStepDispatcher(rt.DispatcherOptions{Store: reopened, Registry: registry, Now: func() time.Time { return base.Add(2 * time.Minute) }})
	if err != nil {
		t.Fatal(err)
	}
	result, err := dispatcher.Dispatch(ctx, rt.DispatchRequest{Claim: claim, Node: definition})
	if err != nil || result.Node.Status != rt.NodeSucceeded {
		t.Fatalf("recovered dispatch: %#v, %v", result, err)
	}
}
