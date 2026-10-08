package subagent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type reviewRunner struct {
	calls        atomic.Int64
	db           Database
	entered      chan string
	release      chan struct{}
	failure      error
	beforeReturn func(*Run) error
}

func (r *reviewRunner) Run(ctx context.Context, run *Run) (*Result, error) {
	r.calls.Add(1)
	if r.db != nil {
		if _, err := r.db.ExecContext(ctx, `UPDATE subagent_runs SET child_session_id=? WHERE id=? AND status=?`, "child-"+run.ID, run.ID, StatusRunning); err != nil {
			return nil, err
		}
	}
	if r.entered != nil {
		r.entered <- run.ID
	}
	if r.release != nil {
		select {
		case <-r.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if r.beforeReturn != nil {
		if err := r.beforeReturn(run); err != nil {
			return nil, err
		}
	}
	return &Result{Summary: "child result"}, r.failure
}
func waitReviewStatus(t *testing.T, svc *Service, id, want string) *Run {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		run, err := svc.Status(t.Context(), id)
		if err == nil && run.Status == want {
			return run
		}
		select {
		case <-deadline:
			t.Fatalf("status did not become %s: %+v %v", want, run, err)
		case <-time.After(time.Millisecond):
		}
	}
}

func TestInteractiveAuthorizedBypassDispatchesExactlyOnce(t *testing.T) {
	db, _ := newTestDB(t)
	runner := &reviewRunner{}
	emitter := &stubEmitter{}
	svc := newTestService(db, runner, nil, emitter, nil)
	svc.SetSpawnAuthorizer(&stubTrustResolver{tier: "trusted"})
	id, err := svc.Spawn(t.Context(), SpawnRequest{ParentSessionID: "parent", Role: "worker", Prompt: "work", Mode: ModeInteractive, AgentProfileID: "trusted"})
	if err != nil {
		t.Fatal(err)
	}
	waitReviewStatus(t, svc, id, StatusCompleted)
	if runner.calls.Load() != 1 || emitter.Count() != 0 {
		t.Fatalf("calls=%d approvals=%d", runner.calls.Load(), emitter.Count())
	}
}

func TestBoundReaperPreservesQueueAndTerminalAdmissionWins(t *testing.T) {
	for _, outcome := range []string{"live", "orphan", "canceled"} {
		t.Run(outcome, func(t *testing.T) {
			db, _ := newTestDB(t)
			runner := &reviewRunner{db: db, entered: make(chan string, 4), release: make(chan struct{})}
			svc := newTestService(db, runner, nil, nil, nil)
			var admitted []string
			for i := 0; i < 3; i++ {
				if id, err := svc.Spawn(t.Context(), SpawnRequest{ParentSessionID: "parent", Role: "worker", Prompt: "occupy", Mode: ModeAsync}); err != nil {
					t.Fatal(err)
				} else {
					admitted = append(admitted, id)
				}
				<-runner.entered
			}
			ids := make(chan string, 1)
			svc.SetStreamSink(&runningRunSink{runIDs: ids})
			done := make(chan error, 1)
			go func() {
				_, err := svc.Spawn(t.Context(), SpawnRequest{ParentSessionID: "parent", Role: "worker", Prompt: "queued", Mode: ModeAsync})
				done <- err
			}()
			var id string
			select {
			case id = <-ids:
			case <-time.After(time.Second):
				t.Fatal("queue not observable")
			}
			future := time.Now().Add(2 * time.Minute)
			reaper := svc.NewReaper(ReaperOptions{Now: func() time.Time { return future }})
			counts, err := reaper.SweepOnce(t.Context())
			if err != nil || counts.Total() != 0 {
				t.Fatalf("live queue reaped: %+v %v", counts, err)
			}
			if outcome == "orphan" {
				claimed, err := NewReaper(db, ReaperOptions{Now: func() time.Time { return future }}).SweepOnce(t.Context())
				if err != nil || claimed.Orphans != 1 {
					t.Fatalf("ownerless recovery did not claim orphan: %+v %v", claimed, err)
				}
			}
			if outcome == "canceled" {
				if err := svc.Cancel(t.Context(), id); err != nil {
					t.Fatal(err)
				}
			}
			close(runner.release)
			select {
			case err := <-done:
				if outcome == "orphan" && !errors.Is(err, ErrRunTerminated) {
					t.Fatalf("dispatch error=%v", err)
				}
				if outcome == "canceled" && err == nil {
					t.Fatal("canceled queue dispatched")
				}
				if outcome == "live" && err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("capacity wait stuck")
			}
			for _, activeID := range admitted {
				waitReviewStatus(t, svc, activeID, StatusCompleted)
			}
			if outcome != "live" {
				want := StatusFailed
				if outcome == "canceled" {
					want = StatusCanceled
				}
				waitReviewStatus(t, svc, id, want)
				if runner.calls.Load() != 3 {
					t.Fatalf("terminal child executed: %d", runner.calls.Load())
				}
			} else {
				waitReviewStatus(t, svc, id, StatusCompleted)
				if runner.calls.Load() != 4 {
					t.Fatalf("live child not dispatched: %d", runner.calls.Load())
				}
			}
		})
	}
}

func TestRetryCannotReviveReaperTerminal(t *testing.T) {
	db, _ := newTestDB(t)
	runner := &reviewRunner{}
	svc := newTestService(db, runner, nil, nil, nil)
	run := &Run{ID: "reaped", ParentSessionID: "parent", Role: "worker", Prompt: "work", Mode: ModeSync, Status: StatusRunning, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), MaxRetries: 3, OnFail: OnFailRetry, AttemptsJSON: "[]"}
	if err := svc.insertRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE subagent_runs SET status=?,error=? WHERE id=?`, StatusFailed, ReasonOrphanReaper, run.ID); err != nil {
		t.Fatal(err)
	}
	run.Status = StatusFailed
	if svc.shouldRetry(t.Context(), run, errors.New("retryable runner error")) {
		t.Fatal("durable terminal authorized retry")
	}
	if err := svc.persistRetryCheckpoint(t.Context(), run); !errors.Is(err, ErrRunTerminated) {
		t.Fatalf("checkpoint=%v", err)
	}
	persisted := waitReviewStatus(t, svc, run.ID, StatusFailed)
	if persisted.Error != ReasonOrphanReaper || persisted.RetryCount != 0 {
		t.Fatalf("terminal replaced: %+v", persisted)
	}
}

func TestReaperTerminalDuringAttemptStopsRetry(t *testing.T) {
	db, _ := newTestDB(t)
	runner := &reviewRunner{db: db, failure: errors.New("retryable runner error")}
	svc := newTestService(db, runner, nil, nil, nil)
	reaper := svc.NewReaper(ReaperOptions{Now: func() time.Time { return time.Now().Add(24 * time.Hour) }})
	runner.beforeReturn = func(_ *Run) error {
		counts, err := reaper.SweepOnce(t.Context())
		if err != nil {
			return err
		}
		if counts.HardCeiling != 1 {
			t.Errorf("running child not claimed: %+v", counts)
		}
		return nil
	}
	id, err := svc.Spawn(t.Context(), SpawnRequest{ParentSessionID: "parent", Role: "worker", Prompt: "work", Mode: ModeSync, OnFail: OnFailRetry, MaxRetries: 3})
	if err != nil {
		t.Fatal(err)
	}
	row := waitReviewStatus(t, svc, id, StatusFailed)
	if runner.calls.Load() != 1 || row.RetryCount != 0 || row.Error != ReasonTimeoutReaper {
		t.Fatalf("reaper terminal revived: calls=%d row=%+v", runner.calls.Load(), row)
	}
}
