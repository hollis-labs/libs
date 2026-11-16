package chathost

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/hollis-labs/libs/agent/sdk/session"
	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
)

type principalKey struct{}

func principal(owner string) context.Context {
	return context.WithValue(context.Background(), principalKey{}, owner)
}
func authorized(ctx context.Context, owner runtimecontract.OwnerID, _ Permission) error {
	if ctx.Value(principalKey{}) != string(owner) {
		return errors.New("denied")
	}
	return nil
}
func binding(owner, agent, conversation, attempt string, gen runtimecontract.Generation) runtimecontract.Binding {
	return runtimecontract.Binding{OwnerID: runtimecontract.OwnerID(owner), OperativeID: runtimecontract.OperativeID(agent), SeatID: "seat", ConversationID: runtimecontract.ConversationID(conversation), AttemptID: runtimecontract.AttemptID(attempt), Generation: gen}
}
func active(t *testing.T, h *Host, ctx context.Context, b runtimecontract.Binding) session.Record {
	t.Helper()
	r, err := h.Create(ctx, session.Record{Binding: b, Phase: session.Fenced})
	if err != nil {
		t.Fatal(err)
	}
	r.Phase = session.Active
	r, err = h.CompareAndSwap(ctx, session.Scope{OwnerID: b.OwnerID, OperativeID: b.OperativeID, ConversationID: b.ConversationID}, r.Version, r)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func makeHost(t *testing.T, root string) *Host {
	t.Helper()
	h, err := Open(root, authorized)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestDurableTwoViewsOneWriterAndChildIsolation(t *testing.T) {
	root := t.TempDir()
	h := makeHost(t, root)
	ctx := principal("owner")
	if err := h.RegisterAgent(ctx, Agent{ID: "agent", Owner: "owner", Seat: "seat", Kind: Embedded, Supervisor: App}); err != nil {
		t.Fatal(err)
	}
	b := binding("owner", "agent", "conversation", "attempt-1", 1)
	active(t, h, ctx, b)
	h2 := makeHost(t, root)
	sc := session.Scope{OwnerID: b.OwnerID, OperativeID: b.OperativeID, ConversationID: b.ConversationID}
	if r, err := h2.Load(ctx, sc); err != nil || r.Binding != b {
		t.Fatalf("reopen: %+v %v", r, err)
	}
	view1, err := h.Attach(ctx, b, Viewer)
	if err != nil {
		t.Fatal(err)
	}
	view2, err := h2.Attach(ctx, b, Viewer)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := h2.Attach(ctx, b, Controller)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.Acquire(ctx, b, session.WriterID(view1.ID)); !errors.Is(err, session.ErrLease) {
		t.Fatalf("viewer acquired input: %v", err)
	}
	lease, err := h.Acquire(ctx, b, session.WriterID(controller.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h2.Acquire(ctx, b, session.WriterID(controller.ID)); !errors.Is(err, session.ErrLease) {
		t.Fatalf("second writer acquired: %v", err)
	}
	input := session.Input{ID: "direct-1", Lease: lease, Text: "exact target"}
	cursor, err := h.Submit(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := h2.Submit(ctx, input); err != nil || again != cursor {
		t.Fatalf("idempotency: %q %v", again, err)
	}
	outputCursor, err := h2.Publish(ctx, b, "reply-1", "response")
	if err != nil {
		t.Fatal(err)
	}
	if outputCursor == cursor {
		t.Fatal("output reused input cursor")
	}
	if _, err = h2.Publish(ctx, b, "reply-1", "changed"); !errors.Is(err, session.ErrConflict) {
		t.Fatalf("changed duplicate output: %v", err)
	}
	for _, view := range []Attachment{view1, view2} {
		events, err := h2.ReadEvents(ctx, view, "")
		if err != nil || len(events) != 2 || events[0].Text != "exact target" || events[0].Cursor != cursor || events[1].Text != "response" {
			t.Fatalf("replay: %+v %v", events, err)
		}
		if later, err := h2.ReadEvents(ctx, view, cursor); err != nil || len(later) != 1 || later[0].Cursor != outputCursor {
			t.Fatalf("cursor replay: %+v %v", later, err)
		}
	}
	child := binding("owner", "agent", "child", "attempt-child", 1)
	childRec, err := h.CreateChild(ctx, b, session.Record{Binding: child, Phase: session.Fenced})
	if err != nil {
		t.Fatal(err)
	}
	childRec.Phase = session.Active
	_, err = h.CompareAndSwap(ctx, session.Scope{OwnerID: child.OwnerID, OperativeID: child.OperativeID, ConversationID: child.ConversationID}, childRec.Version, childRec)
	if err != nil {
		t.Fatal(err)
	}
	childView, err := h.Attach(ctx, child, Viewer)
	if err != nil {
		t.Fatal(err)
	}
	childEvents, err := h.ReadEvents(ctx, childView, "")
	if err != nil || len(childEvents) != 0 {
		t.Fatalf("child leaked parent history: %+v %v", childEvents, err)
	}
	if err := h.Detach(ctx, view1); err != nil {
		t.Fatal(err)
	}
	if _, err = h.ReadEvents(ctx, view1, ""); !errors.Is(err, session.ErrStale) {
		t.Fatalf("detached view reads: %v", err)
	}
	events, err := h.ReadEvents(ctx, view2, "")
	if err != nil || len(events) != 2 {
		t.Fatalf("detach removed history: %+v %v", events, err)
	}
}

func TestGenerationFenceAndOwnerIsolation(t *testing.T) {
	h := makeHost(t, t.TempDir())
	ctx := principal("one")
	if err := h.RegisterAgent(ctx, Agent{ID: "agent", Owner: "one", Seat: "seat", Kind: Stable, Supervisor: Nanite}); err != nil {
		t.Fatal(err)
	}
	old := binding("one", "agent", "conversation", "attempt-old", 1)
	r := active(t, h, ctx, old)
	view, err := h.Attach(ctx, old, Controller)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := h.Acquire(ctx, old, session.WriterID(view.ID))
	if err != nil {
		t.Fatal(err)
	}
	next := old
	next.AttemptID = "attempt-new"
	next.Generation = 2
	fenced, err := h.CompareAndSwap(ctx, session.Scope{OwnerID: old.OwnerID, OperativeID: old.OperativeID, ConversationID: old.ConversationID}, r.Version, session.Record{Binding: next, Parent: &old, Phase: session.Fenced})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.Submit(ctx, session.Input{ID: "late", Lease: lease, Text: "must reject"}); !errors.Is(err, session.ErrStale) {
		t.Fatalf("old input: %v", err)
	}
	if _, err = h.ReadEvents(ctx, view, ""); !errors.Is(err, session.ErrStale) {
		t.Fatalf("old view: %v", err)
	}
	fenced.Phase = session.Active
	_, err = h.CompareAndSwap(ctx, session.Scope{OwnerID: old.OwnerID, OperativeID: old.OperativeID, ConversationID: old.ConversationID}, fenced.Version, fenced)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.Attach(ctx, old, Viewer); !errors.Is(err, session.ErrStale) {
		t.Fatalf("old attach: %v", err)
	}
	if _, err = h.Attach(principal("intruder"), next, Viewer); err == nil {
		t.Fatal("owner isolation failed")
	}
	if _, err = h.Load(principal("intruder"), session.Scope{OwnerID: "one", OperativeID: "agent", ConversationID: "conversation"}); err == nil {
		t.Fatal("owner read isolation failed")
	}
	if _, err = h.CompareAndSwap(ctx, session.Scope{OwnerID: "one", OperativeID: "agent", ConversationID: "conversation"}, r.Version, session.Record{Binding: old, Phase: session.Active}); !errors.Is(err, session.ErrConflict) {
		t.Fatalf("stale CAS: %v", err)
	}
}

func TestConcurrentHostInstancesAdmitOneWriter(t *testing.T) {
	root := t.TempDir()
	h := makeHost(t, root)
	ctx := principal("one")
	if err := h.RegisterAgent(ctx, Agent{ID: "agent", Owner: "one", Seat: "seat", Kind: Task, Supervisor: Tether}); err != nil {
		t.Fatal(err)
	}
	b := binding("one", "agent", "conversation", "attempt", 1)
	active(t, h, ctx, b)
	a1, _ := h.Attach(ctx, b, Controller)
	a2, _ := h.Attach(ctx, b, Controller)
	h2 := makeHost(t, root)
	var wg sync.WaitGroup
	wg.Add(2)
	errs := make(chan error, 2)
	go func() { defer wg.Done(); _, err := h.Acquire(ctx, b, session.WriterID(a1.ID)); errs <- err }()
	go func() { defer wg.Done(); _, err := h2.Acquire(ctx, b, session.WriterID(a2.ID)); errs <- err }()
	wg.Wait()
	close(errs)
	wins := 0
	refused := 0
	for err := range errs {
		if err == nil {
			wins++
		} else if errors.Is(err, session.ErrLease) {
			refused++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || refused != 1 {
		t.Fatalf("wins=%d refused=%d", wins, refused)
	}
}
