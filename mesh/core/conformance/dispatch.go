package conformance

import (
	"context"
	"errors"
	"reflect"
	"testing"

	mesh "github.com/hollis-labs/substrate/mesh"
)

// DispatchFixture supplies restart, grants and retention controls for reusable
// dispatch checks. Restart must preserve admitted work, keys, selection and log.
type DispatchFixture struct {
	Provider    mesh.Provider
	Log         mesh.URN
	GrantFollow func(mesh.URN)
	Restart     func() mesh.Provider
	RetainLast  func(int)
}

// RunDispatch verifies the dispatch capability independently of process launch.
// Setup uses the portable agent/team verbs; implementations supply isolated hosts.
func RunDispatch(t *testing.T, newFixture func(*testing.T) DispatchFixture) {
	t.Helper()
	checkDispatchReview(t, newFixture)
	t.Run("receipts_restart_selection_results", func(t *testing.T) {
		f := newFixture(t)
		p := f.Provider
		invoke := func(r mesh.Request) mesh.Response {
			t.Helper()
			if r.Actor.URN == "" {
				r.Actor = caller
			}
			out, err := p.Invoke(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			return out
		}
		invoke(mesh.Request{Verb: mesh.AgentLaunch, Target: worker.URN})
		team := invoke(mesh.Request{Verb: mesh.TeamForm}).Team
		member := invoke(mesh.Request{Verb: mesh.MemberAdd, Team: team.URN, Target: worker.URN, Slot: "worker"}).Team
		req := mesh.Request{Verb: mesh.Assign, Actor: caller, Team: team.URN, Address: "@worker", IdempotencyKey: "intent", Body: []byte(`{"work":1}`)}
		admitted := invoke(req)
		receipt := admitted.Receipt
		digest, _ := mesh.AssignmentDigest(req)
		if receipt == nil || receipt.TaskURN != admitted.Task.ID || receipt.IntentKey != req.IdempotencyKey || receipt.RequestDigest != digest || receipt.AcceptedTarget != team.URN || receipt.ResolvedMember != worker.URN || receipt.Roster == nil || receipt.Roster.ProviderVersion != member.RosterVersion || receipt.SessionURN == "" {
			t.Fatalf("incomplete atomic receipt: %+v", receipt)
		}
		invoke(mesh.Request{Verb: mesh.MemberRemove, Team: team.URN, Target: worker.URN})
		p = f.Restart()
		retried := invoke(req)
		if !reflect.DeepEqual(receipt, retried.Receipt) {
			t.Fatal("restart changed receipt/selection")
		}
		snapshot := invoke(mesh.Request{Verb: mesh.TaskLookup, Lookup: &mesh.LookupRequest{IntentKey: "intent"}}).Snapshot
		if snapshot == nil || snapshot.Receipt.TaskURN != receipt.TaskURN || snapshot.Position.Watermark == "" || snapshot.Position.Log != f.Log {
			t.Fatal("intent lookup missing atomic snapshot watermark")
		}
		byURN := invoke(mesh.Request{Verb: mesh.TaskLookup, Lookup: &mesh.LookupRequest{TaskURN: receipt.TaskURN}}).Snapshot
		if !reflect.DeepEqual(snapshot, byURN) {
			t.Fatal("lookup selectors disagree")
		}
		req.Body = []byte(`{"work":2}`)
		expectError(t, p, req, mesh.ErrorConflict)
		outsider := mesh.Actor{URN: "msg://user/conformance/outsider", Kind: mesh.ActorUser}
		expectError(t, p, mesh.Request{Verb: mesh.TaskLookup, Actor: outsider, Lookup: &mesh.LookupRequest{TaskURN: receipt.TaskURN}}, mesh.ErrorNotFound)
		result := mesh.VersionedResult{SchemaVersion: "urn:result:unknown/v99", ContentType: "application/json", Content: []byte(`{"answer":42}`), Digest: mesh.ContentDigest([]byte(`{"answer":42}`))}
		expectError(t, p, mesh.Request{Verb: mesh.ReportResult, Actor: worker, Target: receipt.TaskURN, Result: &result}, mesh.ErrorUnsupported)
		if invoke(mesh.Request{Verb: mesh.TaskLookup, Lookup: &mesh.LookupRequest{TaskURN: receipt.TaskURN}}).Snapshot.Task.State.Terminal() {
			t.Fatal("unknown schema completed work")
		}
		result.SchemaVersion = mesh.ResultSchemaV1
		invoke(mesh.Request{Verb: mesh.ReportResult, Actor: worker, Target: receipt.TaskURN, Result: &result})
		snapshot = invoke(mesh.Request{Verb: mesh.TaskLookup, Lookup: &mesh.LookupRequest{TaskURN: receipt.TaskURN}}).Snapshot
		if snapshot.Task.State != mesh.TaskCompleted || snapshot.Receipt.Result == nil || snapshot.Receipt.Result.Digest != result.Digest {
			t.Fatal("current versioned result absent")
		}
		if invoke(mesh.Request{Verb: mesh.Assign, Actor: caller, Team: team.URN, Address: "@worker", IdempotencyKey: "intent", Body: []byte(`{"work":1}`)}).Receipt.State != mesh.TaskCompleted {
			t.Fatal("retry lost current outcome")
		}
	})
	t.Run("follow_authorization_retention", func(t *testing.T) {
		f := newFixture(t)
		p := f.Provider
		read := func(q mesh.FollowRequest) mesh.ReplayPage {
			t.Helper()
			out, err := p.Invoke(context.Background(), mesh.Request{Verb: mesh.EventFollow, Actor: caller, Follow: &q})
			if err != nil {
				t.Fatal(err)
			}
			if out.Replay == nil {
				t.Fatal("no replay page")
			}
			return *out.Replay
		}
		q := mesh.FollowRequest{Log: f.Log, Limit: mesh.MaxReplayPageSize}
		expectError(t, p, mesh.Request{Verb: mesh.EventFollow, Follow: &q}, mesh.ErrorDenied)
		f.GrantFollow(caller.URN)
		start := read(q).Next
		invoke := invoker(t, p)
		invoke(mesh.Request{Verb: mesh.AgentLaunch, Target: worker.URN})
		invoke(mesh.Request{Verb: mesh.AgentLaunch, Actor: worker})
		q.Cursor = start
		q.Limit = 1
		first := read(q)
		if len(first.Events) != 1 || !first.HasMore || first.Next == start || first.Position.Head == "" {
			t.Fatal("unbounded or incomplete replay")
		}
		q.Cursor = first.Next
		masked := read(q)
		if len(masked.Events) != 0 || masked.Next == first.Next || masked.HasMore {
			t.Fatal("private event leaked or masked cursor stuck")
		}
		f.RetainLast(0)
		q.Cursor = start
		gap := read(q)
		if !gap.Gap || gap.Diagnostic != mesh.DiagnosticReplayGap || len(gap.Events) != 0 || gap.Next != start {
			t.Fatal("expired cursor did not signal gap")
		}
		q.Cursor = gap.Next
		if again := read(q); !again.Gap || again.Next != start {
			t.Fatal("unreconciled gap silently advanced")
		}
		q.Cursor = gap.Position.Watermark
		if page := read(q); page.Gap || page.HasMore {
			t.Fatal("watermark cannot restart replay")
		}
		q.Cursor = "foreign"
		expectError(t, p, mesh.Request{Verb: mesh.EventFollow, Follow: &q}, mesh.ErrorInvalid)
		q.Cursor = ""
		q.Limit = mesh.MaxReplayPageSize + 1
		expectError(t, p, mesh.Request{Verb: mesh.EventFollow, Follow: &q}, mesh.ErrorInvalid)
	})
	t.Run("diagnostic_errors", func(t *testing.T) {
		err := mesh.NewDispatchError(mesh.ErrorUnsupported, mesh.DiagnosticUnsupportedRequirement, "capability", "negotiate")
		var legacy *mesh.Error
		var diagnostic *mesh.DispatchError
		if !errors.As(err, &legacy) || !errors.As(err, &diagnostic) || legacy.Code != mesh.ErrorUnsupported || diagnostic.Diagnostic != mesh.DiagnosticUnsupportedRequirement {
			t.Fatal("typed diagnostic lost stable code")
		}
	})
}
