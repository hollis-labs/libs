package fake_test

import (
	"context"
	"encoding/json"
	"errors"
	mesh "github.com/hollis-labs/substrate/mesh"
	"github.com/hollis-labs/substrate/mesh/conformance"
	"github.com/hollis-labs/substrate/mesh/fake"
	"sync"
	"testing"
)

func TestConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) conformance.Fixture {
		p := fake.New()
		return conformance.Fixture{Provider: p, GrantApproval: p.GrantApproval, ReadTask: p.Task}
	})
}
func TestConcurrentIdempotencyAndSnapshotIsolation(t *testing.T) {
	p := fake.New()
	actor := mesh.Actor{URN: "msg://user/test/owner", Kind: mesh.ActorUser}
	req := mesh.Request{Actor: actor, Verb: mesh.AgentLaunch, IdempotencyKey: "once"}
	var wg sync.WaitGroup
	ids := make(chan mesh.URN, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := p.Invoke(context.Background(), req)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- out.Instance.URN
			out.Events[0].Payload[0] = 'x'
		}()
	}
	wg.Wait()
	close(ids)
	var first mesh.URN
	for id := range ids {
		if first == "" {
			first = id
		}
		if first != id {
			t.Fatal("concurrent retry launched another agent")
		}
	}
	events := p.Events()
	if len(events) != 1 || !json.Valid(events[0].Payload) {
		t.Fatal("retry or caller mutation altered journal")
	}
}
func TestErrorsAndCanceledContextHaveNoEffects(t *testing.T) {
	p := fake.New()
	actor := mesh.Actor{URN: "msg://user/test/owner", Kind: mesh.ActorUser}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Invoke(ctx, mesh.Request{Actor: actor, Verb: mesh.AgentLaunch}); err == nil {
		t.Fatal("ignored canceled context")
	}
	if _, err := p.Invoke(context.Background(), mesh.Request{Actor: actor, Verb: mesh.AgentLaunch, Target: "msg://user/test/bad"}); err == nil {
		t.Fatal("invalid launch accepted")
	}
	if len(p.Events()) != 0 {
		t.Fatal("failed commands emitted events")
	}
}

func TestSpawnLimitsAndNonCascadeCancellation(t *testing.T) {
	p := fake.New()
	actor := mesh.Actor{URN: "msg://user/test/owner", Kind: mesh.ActorUser}
	call := func(r mesh.Request) mesh.Response {
		t.Helper()
		r.Actor = actor
		out, err := p.Invoke(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	parent := call(mesh.Request{Verb: mesh.AgentLaunch, Limits: mesh.Limits{MaxDepth: 1, MaxChildren: 1, FanOut: 1}}).Instance
	child := call(mesh.Request{Verb: mesh.AgentLaunch, Parent: parent.URN}).Instance
	for _, r := range []mesh.Request{{Verb: mesh.AgentLaunch, Parent: parent.URN}, {Verb: mesh.AgentLaunch, Parent: child.URN}} {
		r.Actor = actor
		if _, err := p.Invoke(context.Background(), r); err == nil {
			t.Fatal("exceeded inherited spawn limits")
		}
	}
	call(mesh.Request{Verb: mesh.Cancel, Target: parent.URN})
	if call(mesh.Request{Verb: mesh.AgentStatus, Target: child.URN}).Instance.SessionState != mesh.SessionRunning {
		t.Fatal("non-cascade cancellation stopped child")
	}
}
func TestHistoryPoliciesAndResultAttribution(t *testing.T) {
	p := fake.New()
	owner := mesh.Actor{URN: "msg://user/test/owner", Kind: mesh.ActorUser}
	worker := mesh.Actor{URN: "msg://agent/test/worker", Kind: mesh.ActorAgent}
	if _, err := p.Invoke(context.Background(), mesh.Request{Actor: owner, Verb: mesh.AgentLaunch, Target: worker.URN}); err != nil {
		t.Fatal(err)
	}
	for _, policy := range []mesh.HistoryPolicy{mesh.HistoryFull, mesh.HistorySummary, mesh.HistoryFiltered, mesh.HistoryNone} {
		out, err := p.Invoke(context.Background(), mesh.Request{Actor: owner, Verb: mesh.Delegate, Target: worker.URN, History: policy})
		if err != nil || out.Task.History != policy {
			t.Fatalf("history policy lost: %v", err)
		}
		content := []byte(`{"answer":42}`)
		result := mesh.VersionedResult{SchemaVersion: mesh.ResultSchemaV1, ContentType: "application/json", Content: content, Digest: mesh.ContentDigest(content)}
		_, err = p.Invoke(context.Background(), mesh.Request{Actor: owner, Verb: mesh.ReportResult, Target: out.Task.ID, Result: &result})
		var denied *mesh.Error
		if !errors.As(err, &denied) || denied.Code != mesh.ErrorDenied {
			t.Fatalf("unassigned actor was not denied for a valid result: %v", err)
		}
		task, ok := p.Task(out.Task.ID)
		if !ok || task.State != mesh.TaskWorking {
			t.Fatal("failed result command modified task")
		}
	}
}

func TestToolCallerEventAttribution(t *testing.T) {
	p := fake.New()
	actor := mesh.Actor{URN: "msg://tool/test/launcher", Kind: mesh.ActorTool}
	out, err := p.Invoke(context.Background(), mesh.Request{Verb: mesh.AgentLaunch, Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	if out.Events[0].Actor != actor {
		t.Fatal("tool actor attribution lost")
	}
	if err := out.Events[0].Validate(); err != nil {
		t.Fatal(err)
	}
}
