package fake_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	mesh "github.com/hollis-labs/substrate/mesh"
	"github.com/hollis-labs/substrate/mesh/conformance"
	"github.com/hollis-labs/substrate/mesh/fake"
)

func TestDispatchConformance(t *testing.T) {
	conformance.RunDispatch(t, func(t *testing.T) conformance.DispatchFixture {
		p := fake.New()
		return conformance.DispatchFixture{Provider: p, Log: fake.LogURN, GrantFollow: p.GrantFollow, RetainLast: p.RetainLast, Restart: func() mesh.Provider { p = fake.Restore(p.Export()); return p }}
	})
}
func TestLiveFollowAndRevocation(t *testing.T) {
	p := fake.New()
	actor := mesh.Actor{URN: "msg://user/test/owner", Kind: mesh.ActorUser}
	p.GrantFollow(actor.URN)
	read := mesh.Request{Verb: mesh.EventFollow, Actor: actor, Follow: &mesh.FollowRequest{Log: fake.LogURN, Limit: 1, Wait: true}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		out, err := p.Invoke(ctx, read)
		if err == nil && len(out.Replay.Events) != 1 {
			t.Error("tail did not return event")
		}
		done <- err
	}()
	if _, err := p.Invoke(ctx, mesh.Request{Verb: mesh.AgentLaunch, Actor: actor}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	page, err := p.Invoke(ctx, mesh.Request{Verb: mesh.EventFollow, Actor: actor, Follow: &mesh.FollowRequest{Log: fake.LogURN, Limit: 1}})
	if err != nil {
		t.Fatal(err)
	}
	read.Follow.Cursor = page.Replay.Position.Head
	waitCtx := &observedWaitContext{Context: ctx, reached: make(chan struct{})}
	go func() { _, err := p.Invoke(waitCtx, read); done <- err }()
	select {
	case <-waitCtx.reached:
	case <-ctx.Done():
		t.Fatal("tail did not block")
	}
	p.RevokeFollow(actor.URN)
	err = <-done
	var typed *mesh.Error
	if !errors.As(err, &typed) || typed.Code != mesh.ErrorDenied {
		t.Fatalf("mid-wait revocation did not deny tail: %v", err)
	}
}

func TestQueuedAdmissionAndSerializedRestart(t *testing.T) {
	p := fake.New()
	p.QueueAssignments(true)
	actor := mesh.Actor{URN: "msg://user/test/owner", Kind: mesh.ActorUser}
	worker := mesh.Actor{URN: "msg://agent/test/worker", Kind: mesh.ActorAgent}
	call := func(r mesh.Request) mesh.Response {
		t.Helper()
		r.Actor = actor
		out, err := p.Invoke(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	call(mesh.Request{Verb: mesh.AgentLaunch, Target: worker.URN})
	req := mesh.Request{Verb: mesh.Assign, Target: worker.URN, IdempotencyKey: "queued"}
	out := call(req)
	if out.Receipt.State != mesh.TaskSubmitted || out.Receipt.Delivery != mesh.DeliveryPending || out.Receipt.SessionURN != "" {
		t.Fatal("admission claimed readiness")
	}
	data, err := json.Marshal(p.Export())
	if err != nil {
		t.Fatal(err)
	}
	var image fake.RestartImage
	if err := json.Unmarshal(data, &image); err != nil {
		t.Fatal(err)
	}
	p = fake.Restore(image)
	if got := call(req); got.Receipt.TaskURN != out.Receipt.TaskURN || got.Receipt.Delivery != mesh.DeliveryPending {
		t.Fatal("restart lost queued intent")
	}
	if err := p.DeliverTask(out.Task.ID); err != nil {
		t.Fatal(err)
	}
	snap := call(mesh.Request{Verb: mesh.TaskLookup, Lookup: &mesh.LookupRequest{IntentKey: "queued"}}).Snapshot
	if snap.Task.State != mesh.TaskWorking || snap.Receipt.Delivery != mesh.DeliveryDelivered || snap.Receipt.SessionURN == "" {
		t.Fatal("delivery did not update snapshot atomically")
	}
}

func TestAssignmentScopeRefusalsAndResultBytes(t *testing.T) {
	p := fake.New()
	actor := mesh.Actor{URN: "msg://user/test/owner", Kind: mesh.ActorUser}
	worker := mesh.Actor{URN: "msg://agent/test/worker", Kind: mesh.ActorAgent}
	call := func(r mesh.Request) mesh.Response {
		t.Helper()
		if r.Actor.URN == "" {
			r.Actor = actor
		}
		out, err := p.Invoke(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	call(mesh.Request{Verb: mesh.AgentLaunch, Target: worker.URN})
	team := call(mesh.Request{Verb: mesh.TeamForm}).Team
	call(mesh.Request{Verb: mesh.MemberAdd, Team: team.URN, Target: worker.URN, Slot: "worker"})
	for _, r := range []mesh.Request{{Verb: mesh.Assign, Target: worker.URN}, {Verb: mesh.Assign, Target: worker.URN, IdempotencyKey: "bad", Limits: mesh.Limits{Budget: -1}}, {Verb: mesh.Assign, Team: team.URN, Address: "@all", IdempotencyKey: "broadcast"}, {Verb: mesh.Assign, Team: team.URN, Address: "@worker*", IdempotencyKey: "broadcast"}} {
		r.Actor = actor
		before := len(p.Events())
		if _, err := p.Invoke(context.Background(), r); err == nil {
			t.Fatal("invalid assignment admitted")
		}
		if len(p.Events()) != before {
			t.Fatal("refusal changed log")
		}
	}
	req := mesh.Request{Verb: mesh.Assign, Target: worker.URN, IdempotencyKey: "scoped"}
	a := call(req)
	req.Actor = mesh.Actor{URN: "msg://user/test/other", Kind: mesh.ActorUser}
	b := call(req)
	if a.Task.ID == b.Task.ID {
		t.Fatal("different actors shared intent")
	}
	call(mesh.Request{Verb: mesh.AgentStop, Target: worker.URN})
	_, err := p.Invoke(context.Background(), mesh.Request{Verb: mesh.Assign, Actor: actor, Team: team.URN, Target: worker.URN, IdempotencyKey: "busy"})
	var diagnostic *mesh.DispatchError
	if !errors.As(err, &diagnostic) || diagnostic.Diagnostic != mesh.DiagnosticTargetBusy {
		t.Fatal("explicit unavailable target did not fail loudly")
	}
	call(mesh.Request{Verb: mesh.AgentResume, Target: worker.URN})
	content := []byte("{\n  \"answer\": 42\n}")
	result := mesh.VersionedResult{SchemaVersion: mesh.ResultSchemaV1, ContentType: "application/json", Content: content, Digest: mesh.ContentDigest(content)}
	call(mesh.Request{Verb: mesh.ReportResult, Actor: worker, Target: a.Task.ID, Result: &result})
	snapshot := call(mesh.Request{Verb: mesh.TaskLookup, Lookup: &mesh.LookupRequest{TaskURN: a.Task.ID}}).Snapshot
	if string(snapshot.Task.Result) != string(content) || mesh.ContentDigest(snapshot.Task.Result) != result.Digest || string(snapshot.Receipt.Result.Content) != string(content) || snapshot.Receipt.Result.Validate() != nil {
		t.Fatal("snapshot changed result bytes/digest")
	}
}

func TestAdmissionConstraintsAreCheckedBeforeEffects(t *testing.T) {
	p := fake.New()
	actor := mesh.Actor{URN: "msg://user/test/owner", Kind: mesh.ActorUser}
	worker := mesh.URN("msg://agent/test/worker")
	call := func(r mesh.Request) mesh.Response {
		t.Helper()
		r.Actor = actor
		out, err := p.Invoke(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	call(mesh.Request{Verb: mesh.AgentLaunch, Target: worker})
	team := call(mesh.Request{Verb: mesh.TeamForm}).Team
	member := call(mesh.Request{Verb: mesh.MemberAdd, Team: team.URN, Target: worker, Slot: "worker"}).Team
	pin := mesh.DefinitionRef{ID: "worker", Revision: "revision-1", Digest: mesh.ContentDigest([]byte("definition"))}
	p.SetDefinition(worker, pin)
	tests := []struct {
		constraints mesh.AdmissionConstraints
		diagnostic  mesh.Diagnostic
	}{
		{mesh.AdmissionConstraints{Definition: &mesh.DefinitionRef{ID: "worker", Revision: "wrong"}}, mesh.DiagnosticInvalidPin},
		{mesh.AdmissionConstraints{ExpectedProviderRosterVersion: member.RosterVersion + 1}, mesh.DiagnosticStaleSnapshot},
		{mesh.AdmissionConstraints{Requirements: []mesh.Requirement{{URI: "urn:missing:capability/v99"}}}, mesh.DiagnosticUnsupportedRequirement},
		{mesh.AdmissionConstraints{HostStoreVersion: "host-1"}, mesh.DiagnosticUnsupportedRequirement},
	}
	for _, test := range tests {
		before := len(p.Events())
		_, err := p.Invoke(context.Background(), mesh.Request{Verb: mesh.Assign, Actor: actor, Team: team.URN, Address: "@worker", IdempotencyKey: "refused", Admission: &test.constraints})
		var diagnostic *mesh.DispatchError
		if !errors.As(err, &diagnostic) || diagnostic.Diagnostic != test.diagnostic {
			t.Fatalf("wanted %s: %v", test.diagnostic, err)
		}
		if len(p.Events()) != before {
			t.Fatal("constraint refusal left effects")
		}
	}
	out := call(mesh.Request{Verb: mesh.Assign, Team: team.URN, Address: "@worker", IdempotencyKey: "accepted", Admission: &mesh.AdmissionConstraints{Definition: &pin, ExpectedProviderRosterVersion: member.RosterVersion, Requirements: []mesh.Requirement{{URI: mesh.DispatchCapabilityURI, Verbs: []mesh.Verb{mesh.TaskLookup}}}}})
	if out.Receipt.ResolvedMember != worker {
		t.Fatal("verified constraints did not admit")
	}
}

func TestConcurrentAssignmentAndLookup(t *testing.T) {
	p := fake.New()
	actor := mesh.Actor{URN: "msg://user/test/owner", Kind: mesh.ActorUser}
	worker := mesh.URN("msg://agent/test/worker")
	if _, err := p.Invoke(context.Background(), mesh.Request{Verb: mesh.AgentLaunch, Actor: actor, Target: worker}); err != nil {
		t.Fatal(err)
	}
	req := mesh.Request{Verb: mesh.Assign, Actor: actor, Target: worker, IdempotencyKey: "race"}
	var wg sync.WaitGroup
	tasks := make(chan mesh.URN, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := p.Invoke(context.Background(), req)
			if err != nil {
				t.Error(err)
				return
			}
			tasks <- out.Receipt.TaskURN
			snap, err := p.Invoke(context.Background(), mesh.Request{Verb: mesh.TaskLookup, Actor: actor, Lookup: &mesh.LookupRequest{IntentKey: "race"}})
			if err != nil {
				t.Error(err)
				return
			}
			if snap.Snapshot.Task.ID != out.Receipt.TaskURN || snap.Snapshot.Position.Watermark == "" {
				t.Error("atomic lookup disagreement")
			}
			out.Receipt.IntentKey = "mutated"
		}()
	}
	wg.Wait()
	close(tasks)
	var first mesh.URN
	for task := range tasks {
		if first == "" {
			first = task
		}
		if task != first {
			t.Fatal("concurrent intent admitted twice")
		}
	}
	if len(p.Events()) != 2 {
		t.Fatal("concurrent intent duplicated admission event")
	}
}

// Done is evaluated only after the follow loop checks its grant and captures
// its wake channel. This handshake makes mid-wait revocation deterministic.
type observedWaitContext struct {
	context.Context
	reached chan struct{}
	once    sync.Once
}

func (c *observedWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.reached) })
	return c.Context.Done()
}

func TestQueuedResultsAndDeliveryPreserveWaitingState(t *testing.T) {
	p := fake.New()
	p.QueueAssignments(true)
	actor := mesh.Actor{URN: "msg://user/test/owner", Kind: mesh.ActorUser}
	worker := mesh.Actor{URN: "msg://agent/test/worker", Kind: mesh.ActorAgent}
	call := func(r mesh.Request) mesh.Response {
		t.Helper()
		if r.Actor.URN == "" {
			r.Actor = actor
		}
		out, err := p.Invoke(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	call(mesh.Request{Verb: mesh.AgentLaunch, Target: worker.URN})
	req := mesh.Request{Verb: mesh.Assign, Target: worker.URN, IdempotencyKey: "queued-wait"}
	task := call(req).Task
	content := []byte(`{"answer":42}`)
	result := mesh.VersionedResult{SchemaVersion: mesh.ResultSchemaV1, ContentType: "application/json", Content: content, Digest: mesh.ContentDigest(content)}
	before := len(p.Events())
	_, err := p.Invoke(context.Background(), mesh.Request{Verb: mesh.ReportResult, Actor: worker, Target: task.ID, Result: &result})
	var typed *mesh.Error
	if !errors.As(err, &typed) || typed.Code != mesh.ErrorConflict {
		t.Fatalf("undelivered task accepted result: %v", err)
	}
	if len(p.Events()) != before {
		t.Fatal("undelivered result changed log")
	}
	call(mesh.Request{Verb: mesh.RequestInput, Target: task.ID})
	if err := p.DeliverTask(task.ID); err != nil {
		t.Fatal(err)
	}
	snapshot := call(mesh.Request{Verb: mesh.TaskLookup, Lookup: &mesh.LookupRequest{TaskURN: task.ID}}).Snapshot
	if snapshot.Task.State != mesh.TaskInputRequired || snapshot.Receipt.State != mesh.TaskInputRequired || snapshot.Receipt.Delivery != mesh.DeliveryDelivered {
		t.Fatal("delivery overwrote waiting state")
	}
	events := p.Events()
	last := events[len(events)-1]
	if last.Kind != "task.delivered" || last.Actor.Kind != mesh.ActorService || last.Actor.URN != mesh.URN("msg://service/fake/provider") || last.IdempotencyKey == req.IdempotencyKey || last.IdempotencyKey == "" {
		t.Fatal("delivery reused assignment attribution/key")
	}
	if err := last.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := p.DeliverTask(task.ID); err != nil {
		t.Fatal(err)
	}
	if len(p.Events()) != len(events) {
		t.Fatal("duplicate delivery emitted another fact")
	}
}
