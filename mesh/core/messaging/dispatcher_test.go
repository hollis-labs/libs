package messaging_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hollis-labs/libs/mesh/core/messaging"
	"github.com/hollis-labs/libs/mesh/core/messaging/memstore"
)

func addr(kind messaging.AddressKind, id string) messaging.Address {
	return messaging.Address{Kind: kind, Authority: "test", ID: id}
}

func TestDispatcher_Reply_WiresFields(t *testing.T) {
	s := memstore.New()
	d := messaging.NewDispatcher(s)
	ctx := context.Background()

	parent := messaging.Envelope{
		Kind:     messaging.MsgKindRequest,
		From:     addr(messaging.KindAgent, "A"),
		To:       addr(messaging.KindAgent, "B"),
		ThreadID: "T1",
	}
	parentSent, err := d.Send(ctx, parent)
	if err != nil {
		t.Fatalf("Send parent: %v", err)
	}

	reply, err := d.Reply(ctx, parentSent, json.RawMessage(`{"ok":true}`))
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}

	if reply.Kind != messaging.MsgKindResponse {
		t.Errorf("reply.Kind = %q, want response", reply.Kind)
	}
	if reply.InReplyTo != parentSent.ID {
		t.Errorf("reply.InReplyTo = %q, want %q", reply.InReplyTo, parentSent.ID)
	}
	if reply.ThreadID != "T1" {
		t.Errorf("reply.ThreadID = %q, want T1", reply.ThreadID)
	}
	if reply.From != parent.To || reply.To != parent.From {
		t.Errorf("reply From/To not swapped: from=%v to=%v", reply.From, reply.To)
	}
	if string(reply.Payload) != `{"ok":true}` {
		t.Errorf("reply payload: %s", string(reply.Payload))
	}
}

func TestDispatcher_Request_ReceivesResponse(t *testing.T) {
	s := memstore.New()
	d := messaging.NewDispatcher(s)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	A := addr(messaging.KindAgent, "A")
	B := addr(messaging.KindAgent, "B")

	// Goroutine B: subscribe for requests addressed to B, respond to each.
	go func() {
		sub, err := s.Subscribe(ctx, B, messaging.Filter{Kind: []messaging.Kind{messaging.MsgKindRequest}})
		if err != nil {
			return
		}
		for req := range sub {
			_, _ = d.Reply(ctx, req, json.RawMessage(`{"answer":42}`))
			return
		}
	}()
	// Tiny yield to ensure subscribe is established before Request fires.
	time.Sleep(10 * time.Millisecond)

	resp, err := d.Request(ctx, messaging.Envelope{
		From:    A,
		To:      B,
		Payload: json.RawMessage(`{"q":"what"}`),
	})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if resp.Kind != messaging.MsgKindResponse {
		t.Errorf("resp.Kind = %q", resp.Kind)
	}
	if string(resp.Payload) != `{"answer":42}` {
		t.Errorf("resp.Payload = %s", string(resp.Payload))
	}
}

func TestDispatcher_Request_TimesOut(t *testing.T) {
	s := memstore.New()
	d := messaging.NewDispatcher(s)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := d.Request(ctx, messaging.Envelope{
		From: addr(messaging.KindAgent, "A"),
		To:   addr(messaging.KindAgent, "B"),
	})
	if !errors.Is(err, messaging.ErrRequestTimeout) {
		t.Errorf("got %v, want ErrRequestTimeout", err)
	}
}

func TestDispatcher_Request_OverwritesKindAndID(t *testing.T) {
	s := memstore.New()
	d := messaging.NewDispatcher(s)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, _ = d.Request(ctx, messaging.Envelope{
		ID:   "caller-set",
		Kind: messaging.MsgKindNotice, // wrong — should be overwritten
		From: addr(messaging.KindAgent, "A"),
		To:   addr(messaging.KindAgent, "B"),
	})
	// Fetch via Inbox on To=B to retrieve.
	got, err := s.Inbox(context.Background(), addr(messaging.KindAgent, "B"), messaging.Filter{})
	if err != nil || len(got) == 0 {
		t.Fatalf("Inbox: %v / %d", err, len(got))
	}
	if got[0].Kind != messaging.MsgKindRequest {
		t.Errorf("Request did not force Kind=request, got %q", got[0].Kind)
	}
	if got[0].ID == "caller-set" {
		t.Error("Request did not overwrite caller-provided ID")
	}
}

// slowStore blocks in Send (or Subscribe) until the caller's context ends and then reports its own wrapped context error, as a
// store behind a busy host does when the deadline expires before the call returns.
type slowStore struct {
	messaging.Store
	slowSend, slowSubscribe bool
}

func (s slowStore) Send(ctx context.Context, e messaging.Envelope) (messaging.Envelope, error) {
	if s.slowSend {
		<-ctx.Done()
		return messaging.Envelope{}, fmt.Errorf("messaging store: send: %w", ctx.Err())
	}
	return s.Store.Send(ctx, e)
}

func (s slowStore) Subscribe(ctx context.Context, to messaging.Address, f messaging.Filter) (<-chan messaging.Envelope, error) {
	if s.slowSubscribe {
		<-ctx.Done()
		return nil, fmt.Errorf("messaging store: subscribe: %w", ctx.Err())
	}
	return s.Store.Subscribe(ctx, to, f)
}

// A deadline that expires inside the Store calls before the wait is still a request timeout: callers match ErrRequestTimeout.
func TestDispatcher_Request_DeadlineInsideStoreCallIsATimeout(t *testing.T) {
	for name, store := range map[string]slowStore{
		"send":      {Store: memstore.New(), slowSend: true},
		"subscribe": {Store: memstore.New(), slowSubscribe: true},
	} {
		t.Run(name, func(t *testing.T) {
			d := messaging.NewDispatcher(store)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			_, err := d.Request(ctx, messaging.Envelope{From: addr(messaging.KindAgent, "rA"), To: addr(messaging.KindAgent, "rB")})
			if !errors.Is(err, messaging.ErrRequestTimeout) {
				t.Fatalf("err = %v, want ErrRequestTimeout", err)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("err = %v lost the underlying deadline error", err)
			}
		})
	}
}

// A cancellation, and a Store failure that is not the caller's deadline, are not timeouts.
func TestDispatcher_Request_OtherStoreErrorsPassThrough(t *testing.T) {
	d := messaging.NewDispatcher(slowStore{Store: memstore.New(), slowSend: true})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := d.Request(ctx, messaging.Envelope{From: addr(messaging.KindAgent, "rA"), To: addr(messaging.KindAgent, "rB")})
	if errors.Is(err, messaging.ErrRequestTimeout) || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request: %v", err)
	}
}
