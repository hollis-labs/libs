package conformance

import (
	"bytes"
	"context"
	"testing"

	mesh "github.com/hollis-labs/substrate/mesh"
)

func checkDispatchReview(t *testing.T, newFixture func(*testing.T) DispatchFixture) {
	t.Helper()
	t.Run("task_authority_and_masking", func(t *testing.T) {
		f := newFixture(t)
		p := f.Provider
		call := invoker(t, p)
		call(mesh.Request{Verb: mesh.AgentLaunch, Target: worker.URN})
		task := call(mesh.Request{Verb: mesh.Assign, Target: worker.URN, IdempotencyKey: "authority"}).Task
		stranger := mesh.Actor{URN: "msg://user/conformance/stranger", Kind: mesh.ActorUser}
		expectError(t, p, mesh.Request{Verb: mesh.Cancel, Actor: stranger, Target: worker.URN}, mesh.ErrorNotFound)
		valid := mesh.VersionedResult{SchemaVersion: mesh.ResultSchemaV1, ContentType: "application/json", Content: []byte(`{"answer":42}`), Digest: mesh.ContentDigest([]byte(`{"answer":42}`))}
		// Non-assignee uses a valid envelope, so only authorization can refuse it.
		expectError(t, p, mesh.Request{Verb: mesh.ReportResult, Target: task.ID, Result: &valid}, mesh.ErrorDenied)
		for _, v := range []mesh.Verb{mesh.Cancel, mesh.RequestInput, mesh.RequestApproval, mesh.ReportResult} {
			expectError(t, p, mesh.Request{Verb: v, Actor: stranger, Target: task.ID, Result: &valid}, mesh.ErrorNotFound)
			expectError(t, p, mesh.Request{Verb: v, Actor: stranger, Target: "msg://task/conformance/missing", Result: &valid}, mesh.ErrorNotFound)
		}
		expectError(t, p, mesh.Request{Verb: mesh.Assign, Actor: stranger, Target: worker.URN, Parent: task.ID, IdempotencyKey: "stolen-parent"}, mesh.ErrorNotFound)
		snap, err := p.Invoke(context.Background(), mesh.Request{Verb: mesh.TaskLookup, Actor: caller, Lookup: &mesh.LookupRequest{TaskURN: task.ID}})
		if err != nil || snap.Snapshot.Task.State != mesh.TaskWorking {
			t.Fatal("unauthorized command changed task")
		}
		invalid := valid
		invalid.Digest = mesh.ContentDigest([]byte("wrong"))
		expectError(t, p, mesh.Request{Verb: mesh.ReportResult, Actor: worker, Target: task.ID, Result: &invalid}, mesh.ErrorInvalid)
		completed := call(mesh.Request{Verb: mesh.ReportResult, Actor: worker, Target: task.ID, Result: &valid})
		expectError(t, p, mesh.Request{Verb: mesh.ReportResult, Actor: worker, Target: task.ID, Result: &valid}, mesh.ErrorConflict)
		snapshot, err := p.Invoke(context.Background(), mesh.Request{Verb: mesh.TaskLookup, Actor: caller, Lookup: &mesh.LookupRequest{TaskURN: task.ID}})
		if err != nil || !bytes.Equal(snapshot.Snapshot.Task.Result, valid.Content) || !bytes.Equal(completed.Task.Result, valid.Content) {
			t.Fatal("duplicate result changed original bytes")
		}
		// The assigned actor may attach its own delegated child to this task.
		call(mesh.Request{Verb: mesh.Assign, Target: worker.URN, IdempotencyKey: "second-parent"})
		parent := call(mesh.Request{Verb: mesh.Assign, Target: worker.URN, IdempotencyKey: "worker-parent"}).Task
		call(mesh.Request{Verb: mesh.Assign, Actor: worker, Target: worker.URN, Parent: parent.ID, IdempotencyKey: "owned-parent"})
	})
	t.Run("immutable_intent_fields", func(t *testing.T) {
		f := newFixture(t)
		p := f.Provider
		call := invoker(t, p)
		call(mesh.Request{Verb: mesh.AgentLaunch, Target: worker.URN})
		team := call(mesh.Request{Verb: mesh.TeamForm}).Team
		call(mesh.Request{Verb: mesh.MemberAdd, Team: team.URN, Target: worker.URN, Slot: "worker"})
		parent := call(mesh.Request{Verb: mesh.Assign, Target: worker.URN, IdempotencyKey: "parent"}).Task
		req := mesh.Request{Verb: mesh.Assign, Actor: caller, Team: team.URN, Address: "@worker", IdempotencyKey: "immutable", History: mesh.HistorySummary, Parent: parent.ID, Admission: &mesh.AdmissionConstraints{}, Limits: mesh.Limits{Budget: 1}, CorrelationID: "trace-1"}
		first := call(req)
		changes := map[string]func(*mesh.Request){
			"admission": func(r *mesh.Request) { r.Admission = &mesh.AdmissionConstraints{HostStoreVersion: "different"} },
			"limits":    func(r *mesh.Request) { r.Limits.Budget = 2 },
			"history":   func(r *mesh.Request) { r.History = mesh.HistoryNone },
			"team":      func(r *mesh.Request) { r.Team = "msg://team/conformance/different" },
			"address":   func(r *mesh.Request) { r.Address = string(worker.URN) },
			"parent":    func(r *mesh.Request) { r.Parent = "" },
		}
		for name, change := range changes {
			t.Run(name, func(t *testing.T) { r := req; change(&r); expectError(t, p, r, mesh.ErrorConflict) })
		}
		req.CorrelationID = "trace-2"
		retry := call(req)
		digest, _ := mesh.AssignmentDigest(req)
		if retry.Receipt.TaskURN != first.Receipt.TaskURN || retry.Receipt.RequestDigest != first.Receipt.RequestDigest || digest != first.Receipt.RequestDigest {
			t.Fatal("trace metadata changed assignment identity")
		}
	})
	t.Run("follow_filters_do_not_grant_visibility", func(t *testing.T) {
		f := newFixture(t)
		p := f.Provider
		call := invoker(t, p)
		f.GrantFollow(caller.URN)
		own := call(mesh.Request{Verb: mesh.AgentLaunch, Target: worker.URN}).Instance
		call(mesh.Request{Verb: mesh.AgentStatus, Target: worker.URN})
		private := call(mesh.Request{Verb: mesh.AgentLaunch, Actor: worker}).Instance
		for _, q := range []mesh.FollowRequest{
			{Log: f.Log, Limit: mesh.MaxReplayPageSize, Subjects: []mesh.URN{own.URN}, Kinds: []string{string(mesh.AgentStatus)}},
			{Log: f.Log, Limit: mesh.MaxReplayPageSize, Subjects: []mesh.URN{private.URN}, Kinds: []string{string(mesh.AgentLaunch)}},
			{Log: f.Log, Limit: mesh.MaxReplayPageSize, Subjects: []mesh.URN{own.URN}, Kinds: []string{"never"}},
			{Log: f.Log, Limit: mesh.MaxReplayPageSize, Subjects: []mesh.URN{"msg://agent/conformance/missing"}, Kinds: []string{string(mesh.AgentLaunch)}},
		} {
			out, err := p.Invoke(context.Background(), mesh.Request{Verb: mesh.EventFollow, Actor: caller, Follow: &q})
			if err != nil {
				t.Fatal(err)
			}
			expected := 0
			if q.Subjects[0] == own.URN && q.Kinds[0] == string(mesh.AgentStatus) {
				expected = 1
			}
			if len(out.Replay.Events) != expected {
				t.Fatal("filters bypassed visibility or did not narrow it")
			}
			for _, e := range out.Replay.Events {
				if e.Subject != own.URN || e.Kind != string(mesh.AgentStatus) {
					t.Fatal("wrong filtered event")
				}
			}
		}
	})
	t.Run("explicit_member_never_reroutes", func(t *testing.T) {
		f := newFixture(t)
		p := f.Provider
		call := invoker(t, p)
		call(mesh.Request{Verb: mesh.AgentLaunch, Target: worker.URN})
		expectError(t, p, mesh.Request{Verb: mesh.Assign, Target: worker.URN, Address: "@other", IdempotencyKey: "unscoped-address"}, mesh.ErrorInvalid)
		other := mesh.URN("msg://agent/conformance/alternate")
		call(mesh.Request{Verb: mesh.AgentLaunch, Target: other})
		team := call(mesh.Request{Verb: mesh.TeamForm}).Team
		call(mesh.Request{Verb: mesh.MemberAdd, Team: team.URN, Target: worker.URN, Slot: "worker"})
		call(mesh.Request{Verb: mesh.MemberAdd, Team: team.URN, Target: other, Slot: "worker"})
		call(mesh.Request{Verb: mesh.AgentStop, Target: worker.URN})
		expectError(t, p, mesh.Request{Verb: mesh.Assign, Team: team.URN, Target: worker.URN, Address: "@worker", IdempotencyKey: "explicit"}, mesh.ErrorConflict)
		expectError(t, p, mesh.Request{Verb: mesh.Assign, Team: team.URN, Target: worker.URN, Address: string(other), IdempotencyKey: "conflicting"}, mesh.ErrorInvalid)
	})
}
