package teams_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hollis-labs/libs/mesh/core/teams"
	"github.com/hollis-labs/libs/mesh/core/teams/memory"
)

type plannedWorkflows struct {
	*memory.Host
	calls    int
	err      error
	empty    bool
	mismatch bool
}

func (p *plannedWorkflows) PlanWorkflowRunID(_ context.Context, key string, _ teams.WorkflowDefinition) (string, error) {
	p.calls++
	if p.err != nil || p.empty {
		return "", p.err
	}
	if p.mismatch {
		return "different-" + key, nil
	}
	return "run-" + key, nil
}

func TestPlannedWorkflowRunIDReachesEveryIntentAndSurvivesResume(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "planned", true: "legacy"}[legacy], func(t *testing.T) {
			h := memory.New()
			d := team()
			if err := putDefinition(h, d); err != nil {
				t.Fatal(err)
			}
			l := launcher(h)
			p := &plannedWorkflows{Host: h}
			if !legacy {
				l.Workflows = p
			}
			h.After = func(op, key string) error {
				if op == "ledger_planning" {
					return errors.New("lost initial write acknowledgement")
				}
				return nil
			}
			req := teams.LaunchRequest{Key: "planned", TeamID: d.ID, Version: d.Version}
			if _, err := l.Launch(ctx, req); err == nil {
				t.Fatal("fault not exercised")
			}
			record, err := h.GetLaunch(ctx, req.Key)
			if err != nil {
				t.Fatal(err)
			}
			want := "run-" + req.Key
			if legacy {
				want = ""
			}
			if record.PlannedRunID != want {
				t.Fatalf("planned ID = %q", record.PlannedRunID)
			}
			for _, intent := range record.Intents {
				if intent.Request.RunID != want {
					t.Fatalf("intent lost ID: %+v", intent)
				}
			}
			h.After = nil
			// A fresh launcher with a failing planner must resume without planning again,
			// including a record first created by a host without the optional interface.
			next := &plannedWorkflows{Host: h, err: errors.New("must not replan")}
			l = launcher(h)
			l.Workflows = next
			run, err := l.Launch(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			if run.ID != "run-"+req.Key || next.calls != 0 {
				t.Fatal(run, next.calls)
			}
			roster, err := h.Snapshot(ctx, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range roster.Members {
				if m.Intent != nil && m.Intent.RunID != want {
					t.Fatalf("member intent changed: %+v", m.Intent)
				}
			}
			expectedCalls := 1
			if legacy {
				expectedCalls = 0
			}
			if p.calls != expectedCalls {
				t.Fatal(p.calls)
			}
		})
	}
}

func TestPlannedWorkflowRunIDFailureWritesNothing(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "empty"}[empty], func(t *testing.T) {
			h := memory.New()
			d := team()
			if err := putDefinition(h, d); err != nil {
				t.Fatal(err)
			}
			p := &plannedWorkflows{Host: h, empty: empty}
			if !empty {
				p.err = errors.New("planner unavailable")
			}
			l := launcher(h)
			l.Workflows = p
			effects := 0
			h.Before = func(op, key string) error {
				if op == "provision" || op == "workflow" {
					effects++
				}
				return nil
			}
			_, err := l.Launch(ctx, teams.LaunchRequest{Key: "invalid", TeamID: d.ID, Version: d.Version})
			if err == nil {
				t.Fatal("invalid plan accepted")
			}
			if !empty && !errors.Is(err, p.err) {
				t.Fatal("planner cause lost", err)
			}
			if _, err = h.GetLaunch(ctx, "invalid"); !errors.Is(err, teams.ErrNotFound) {
				t.Fatal("record persisted", err)
			}
			if effects != 0 || p.calls != 1 {
				t.Fatal(effects, p.calls)
			}
		})
	}
}

func TestPlannedWorkflowRunIDMismatchAbortsAndFences(t *testing.T) {
	h := memory.New()
	d := team()
	if err := putDefinition(h, d); err != nil {
		t.Fatal(err)
	}
	p := &plannedWorkflows{Host: h, mismatch: true}
	l := launcher(h)
	l.Workflows = p
	_, err := l.Launch(ctx, teams.LaunchRequest{Key: "mismatch", TeamID: d.ID, Version: d.Version})
	if !errors.Is(err, teams.ErrLaunchFailed) || !strings.Contains(err.Error(), "workflow run id differs") {
		t.Fatal(err)
	}
	record, err := h.GetLaunch(ctx, "mismatch")
	if err != nil {
		t.Fatal(err)
	}
	if record.State != teams.Failed || record.Run.ID != "" {
		t.Fatal(record.State, record.Run)
	}
	for _, intent := range record.Intents {
		if !intent.Cleaned {
			t.Fatalf("uncleaned intent: %+v", intent)
		}
	}
	if _, err = h.LaunchWorkflow(ctx, "mismatch", record.Definition); !errors.Is(err, teams.ErrLaunchFailed) {
		t.Fatal("workflow not fenced", err)
	}
	record.PlannedRunID = "tampered"
	if err = h.PutLaunch(ctx, record); !errors.Is(err, teams.ErrConflict) {
		t.Fatal("plan mutated", err)
	}
}
