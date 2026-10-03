// Package conformance supplies reusable behavioral checks for the MVP contract.
// Full adapter conformance (streams, reconnects and all capability modes) is later.
package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	mesh "github.com/hollis-labs/substrate/mesh"
	"testing"
)

// Fixture supplies host setup/inspection without expanding the portable verbs.
// Each test receives a clean, isolated provider. GrantApproval must explicitly
// authorize that actor; ReadTask observes state without changing it.
type Fixture struct {
	Provider      mesh.Provider
	GrantApproval func(mesh.URN, mesh.URN)
	ReadTask      func(mesh.URN) (mesh.Task, bool)
}
type Factory func(*testing.T) Fixture

var caller = mesh.Actor{URN: "msg://user/conformance/caller", Kind: mesh.ActorUser}
var worker = mesh.Actor{URN: "msg://agent/conformance/worker", Kind: mesh.ActorAgent}

func Run(t *testing.T, newFixture Factory) {
	t.Helper()
	tests := map[string]func(*testing.T, Fixture){
		"lifecycle": func(t *testing.T, f Fixture) {
			call := invoker(t, f.Provider)
			a := call(mesh.Request{Verb: mesh.AgentLaunch, Target: worker.URN}).Instance
			if a == nil || a.SessionState != mesh.SessionRunning {
				t.Fatal("launch did not return running agent")
			}
			if err := a.SessionURN.Validate(); err != nil || a.SessionURN == a.URN {
				t.Fatal("launch did not return distinct session identity")
			}
			for _, v := range []mesh.Verb{mesh.AgentStatus, mesh.AgentStop, mesh.AgentResume, mesh.Steer, mesh.Interrupt} {
				r := call(mesh.Request{Verb: v, Target: a.URN})
				want := mesh.SessionRunning
				if v == mesh.AgentStop || v == mesh.Interrupt {
					want = mesh.SessionPaused
				}
				if r.Instance == nil || r.Instance.SessionState != want || r.Instance.SessionURN != a.SessionURN {
					t.Fatalf("%s state: %+v", v, r.Instance)
				}
			}
			resumed := call(mesh.Request{Verb: mesh.AgentResume, Target: a.URN})
			if resumed.Instance.SessionURN != a.SessionURN {
				t.Fatal("resume changed session identity")
			}
		},
		"membership": func(t *testing.T, f Fixture) {
			call := invoker(t, f.Provider)
			team := call(mesh.Request{Verb: mesh.TeamForm}).Team
			member := call(mesh.Request{Verb: mesh.MemberAdd, Team: team.URN, Target: worker.URN, Slot: "worker", Role: "engineer"}).Team
			if member.Members[worker.URN].Slot != "worker" || member.Members[worker.URN].Role != "engineer" || member.RosterVersion <= team.RosterVersion {
				t.Fatal("member not added with new roster version")
			}
			member = call(mesh.Request{Verb: mesh.RoleAssign, Team: team.URN, Target: worker.URN, Role: "reviewer"}).Team
			if member.Members[worker.URN].Role != "reviewer" || member.Members[worker.URN].Slot != "worker" {
				t.Fatal("role was not assigned")
			}
			addressed := call(mesh.Request{Verb: mesh.MessageAddress, Team: team.URN, Address: "@worker"}).Message
			if len(addressed.Recipients) != 1 || addressed.Recipients[0] != worker.URN {
				t.Fatal("role.assign changed the functional slot")
			}
			expectError(t, f.Provider, mesh.Request{Verb: mesh.MessageAddress, Team: team.URN, Address: "@reviewer"}, mesh.ErrorNotFound)
			member = call(mesh.Request{Verb: mesh.MemberRemove, Team: team.URN, Target: worker.URN}).Team
			if _, ok := member.Members[worker.URN]; ok {
				t.Fatal("member not removed")
			}
			member = call(mesh.Request{Verb: mesh.MemberJoin, Team: team.URN, Slot: "owner"}).Team
			if _, ok := member.Members[caller.URN]; !ok {
				t.Fatal("caller did not join")
			}
			member = call(mesh.Request{Verb: mesh.MemberLeave, Team: team.URN}).Team
			if _, ok := member.Members[caller.URN]; ok {
				t.Fatal("caller did not leave")
			}
			call(mesh.Request{Verb: mesh.TeamDissolve, Target: team.URN})
			expectError(t, f.Provider, mesh.Request{Verb: mesh.MemberJoin, Team: team.URN, Slot: "owner"}, mesh.ErrorNotFound)
		},
		"routing_reply": func(t *testing.T, f Fixture) {
			call := invoker(t, f.Provider)
			team := call(mesh.Request{Verb: mesh.TeamForm}).Team
			call(mesh.Request{Verb: mesh.MemberAdd, Team: team.URN, Target: worker.URN, Slot: "worker", Role: "engineer"})
			other := mesh.URN("msg://agent/conformance/other")
			team = call(mesh.Request{Verb: mesh.MemberAdd, Team: team.URN, Target: other, Slot: "worker", Role: "engineer"}).Team
			m := call(mesh.Request{Verb: mesh.MessageAddress, Team: team.URN, Address: "@worker*", Body: json.RawMessage(`{"text":"hello"}`)}).Message
			if len(m.Recipients) != 2 || m.RosterVersion != team.RosterVersion {
				t.Fatal("fanout did not snapshot roster")
			}
			one := call(mesh.Request{Verb: mesh.MessageAddress, Team: team.URN, Address: "@worker"}).Message
			if len(one.Recipients) != 1 {
				t.Fatal("slot did not resolve one member")
			}
			for _, address := range []string{"@" + team.Members[worker.URN].ID, string(worker.URN)} {
				byMember := call(mesh.Request{Verb: mesh.MessageAddress, Team: team.URN, Address: address}).Message
				if len(byMember.Recipients) != 1 || byMember.Recipients[0] != worker.URN {
					t.Fatal("member address did not resolve its actor")
				}
			}
			all := call(mesh.Request{Verb: mesh.MessageAddress, Team: team.URN, Address: "@all"}).Message
			if len(all.Recipients) != 2 {
				t.Fatal("@all did not reach every member")
			}
			expectError(t, f.Provider, mesh.Request{Verb: mesh.MessageAddress, Team: team.URN, Address: "@engineer*"}, mesh.ErrorNotFound)
			direct := call(mesh.Request{Verb: mesh.MessageSend, Target: worker.URN}).Message
			reply := call(mesh.Request{Verb: mesh.Reply, Actor: worker, InReplyTo: direct.ID, Target: other}).Message
			if len(reply.Recipients) != 1 || reply.Recipients[0] != caller.URN || reply.InReplyTo != direct.ID {
				t.Fatal("reply did not return to original sender")
			}
			expectError(t, f.Provider, mesh.Request{Verb: mesh.Reply, InReplyTo: direct.ID}, mesh.ErrorDenied)
			expectError(t, f.Provider, mesh.Request{Verb: mesh.MessageAddress, Team: team.URN, Address: "@missing"}, mesh.ErrorNotFound)
		},
		"work_input_approval_result": func(t *testing.T, f Fixture) {
			call := invoker(t, f.Provider)
			call(mesh.Request{Verb: mesh.AgentLaunch, Target: worker.URN})
			for _, v := range []mesh.Verb{mesh.Assign, mesh.Delegate} {
				task := call(mesh.Request{Verb: v, Target: worker.URN, IdempotencyKey: string(v)}).Task
				if task.State != mesh.TaskWorking || task.History != mesh.HistorySummary {
					t.Fatal("incorrect assignment state/history")
				}
				task = call(mesh.Request{Verb: mesh.RequestInput, Target: task.ID}).Task
				if task.State != mesh.TaskInputRequired {
					t.Fatal("input request did not interrupt task")
				}
				call(mesh.Request{Verb: mesh.RequestApproval, Target: task.ID})
				expectError(t, f.Provider, mesh.Request{Verb: mesh.Approve, Target: task.ID}, mesh.ErrorDenied)
				f.GrantApproval(task.ID, caller.URN)
				task = call(mesh.Request{Verb: mesh.Approve, Target: task.ID}).Task
				if task.State != mesh.TaskWorking {
					t.Fatal("approval did not resume task")
				}
				result := call(mesh.Request{Verb: mesh.ReportResult, Actor: worker, Target: task.ID, Result: &mesh.VersionedResult{SchemaVersion: mesh.ResultSchemaV1, ContentType: "application/json", Digest: mesh.ContentDigest([]byte(`{"answer":42}`)), Content: json.RawMessage(`{"answer":42}`)}})
				task = result.Task
				if result.Message == nil || len(result.Message.Recipients) != 1 || result.Message.Recipients[0] != caller.URN {
					t.Fatal("result was not pushed back to caller")
				}
				if task.State != mesh.TaskCompleted || string(task.Result) != `{"answer":42}` {
					t.Fatal("result not recorded")
				}
				expectError(t, f.Provider, mesh.Request{Verb: mesh.Cancel, Target: task.ID}, mesh.ErrorConflict)
			}
		},
		"cancel_cascade": func(t *testing.T, f Fixture) {
			call := invoker(t, f.Provider)
			a := call(mesh.Request{Verb: mesh.AgentLaunch}).Instance
			b := call(mesh.Request{Verb: mesh.AgentLaunch, Parent: a.URN}).Instance
			root := call(mesh.Request{Verb: mesh.Delegate, Target: a.URN}).Task
			child := call(mesh.Request{Verb: mesh.Assign, Target: b.URN, Parent: root.ID, IdempotencyKey: "child"}).Task
			call(mesh.Request{Verb: mesh.Cancel, Target: root.ID, Cascade: true})
			got, ok := f.ReadTask(child.ID)
			if !ok || got.State != mesh.TaskCanceled {
				t.Fatal("task cascade left descendant active")
			}
			call(mesh.Request{Verb: mesh.Cancel, Target: a.URN, Cascade: true})
			if call(mesh.Request{Verb: mesh.AgentStatus, Target: b.URN}).Instance.SessionState != mesh.SessionEnded {
				t.Fatal("agent cascade left descendant running")
			}
		},
		"unclaimed_verbs": func(t *testing.T, f Fixture) {
			d, err := f.Provider.Describe(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err := checkUnclaimed(context.Background(), f.Provider, d); err != nil {
				t.Fatal(err)
			}
		},
		"capabilities_and_idempotency": func(t *testing.T, f Fixture) {
			d, err := f.Provider.Describe(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err = d.Validate(); err != nil {
				t.Fatal(err)
			}
			for _, v := range []mesh.Verb{mesh.AgentLaunch, mesh.AgentStop, mesh.AgentResume, mesh.AgentStatus, mesh.TeamForm, mesh.TeamDissolve, mesh.MemberAdd, mesh.MemberRemove, mesh.MemberJoin, mesh.MemberLeave, mesh.RoleAssign, mesh.Assign, mesh.Delegate, mesh.MessageSend, mesh.MessageAddress, mesh.Reply, mesh.Steer, mesh.Interrupt, mesh.RequestInput, mesh.RequestApproval, mesh.Approve, mesh.Cancel, mesh.ReportResult} {
				if !d.Supports(v) {
					t.Fatalf("MVP verb %s absent", v)
				}
			}
			req := make([]mesh.Requirement, 0, len(d.Capabilities))
			for _, c := range d.Capabilities {
				req = append(req, mesh.Requirement{URI: c.URI, Verbs: c.Verbs, Modes: c.Modes})
			}
			if _, err := mesh.NegotiateCapabilities(d, req); err != nil {
				t.Fatal(err)
			}
			if _, err := mesh.NegotiateCapabilities(d, []mesh.Requirement{{URI: "urn:conformance:unsupported/v99"}}); err == nil {
				t.Fatal("negotiation silently downgraded")
			}
			call := invoker(t, f.Provider)
			r := mesh.Request{Verb: mesh.AgentLaunch, IdempotencyKey: "launch-once"}
			a := call(r)
			b := call(r)
			if a.Instance.URN != b.Instance.URN || a.Instance.SessionURN != b.Instance.SessionURN || a.Events[0].Cursor != b.Events[0].Cursor {
				t.Fatal("idempotent launch re-executed")
			}
			r.Target = worker.URN
			expectError(t, f.Provider, r, mesh.ErrorConflict)
			expectError(t, f.Provider, mesh.Request{Verb: "unknown"}, mesh.ErrorUnsupported)
		},
	}
	for name, run := range tests {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			if f.Provider == nil || f.GrantApproval == nil || f.ReadTask == nil {
				t.Fatal("incomplete conformance fixture")
			}
			run(t, f)
		})
	}
}
func invoker(t *testing.T, p mesh.Provider) func(mesh.Request) mesh.Response {
	t.Helper()
	return func(r mesh.Request) mesh.Response {
		t.Helper()
		if r.Actor.URN == "" {
			r.Actor = caller
		}
		out, err := p.Invoke(context.Background(), r)
		if err != nil {
			t.Fatalf("%s: %v", r.Verb, err)
		}
		if len(out.Events) == 0 {
			t.Fatalf("%s: no event", r.Verb)
		}
		for _, event := range out.Events {
			if err := event.Validate(); err != nil {
				t.Fatalf("invalid event: %v", err)
			}
			if event.Actor != r.Actor {
				t.Fatal("event lost actor attribution")
			}
		}
		return out
	}
}
func expectError(t *testing.T, p mesh.Provider, r mesh.Request, code mesh.ErrorCode) {
	t.Helper()
	if r.Actor.URN == "" {
		r.Actor = caller
	}
	_, err := p.Invoke(context.Background(), r)
	var typed *mesh.Error
	if !errors.As(err, &typed) || typed.Code != code {
		t.Fatalf("%s: wanted %s, got %v", r.Verb, code, err)
	}
}

// Claims are bidirectional: absence is an explicit refusal, not an invitation
// to attempt an operation that the provider implements but did not advertise.
func checkUnclaimed(ctx context.Context, p mesh.Provider, d mesh.Descriptor) error {
	for _, v := range mesh.Verbs() {
		if d.Supports(v) {
			continue
		}
		_, err := p.Invoke(ctx, mesh.Request{Verb: v, Actor: caller})
		var typed *mesh.Error
		if !errors.As(err, &typed) || typed.Code != mesh.ErrorUnsupported {
			return fmt.Errorf("unclaimed verb %s must return unsupported, got %v", v, err)
		}
	}
	return nil
}
