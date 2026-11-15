package gatecrasher

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/hollis-labs/libs/mesh/core/gate"
	"github.com/hollis-labs/libs/mesh/core/hitl"
)

type corruptOwner struct {
	InteractionPort
	corrupt func(*hitl.ItemView)
}

func (o corruptOwner) Get(ctx context.Context, q hitl.GetCommand) (hitl.RetrievalResult, error) {
	v, err := o.InteractionPort.Get(ctx, q)
	if err == nil {
		o.corrupt(&v.Item)
	}
	return v, err
}
func (o corruptOwner) Await(ctx context.Context, q hitl.AwaitCommand) (hitl.RetrievalResult, error) {
	v, err := o.InteractionPort.Await(ctx, q)
	if err == nil {
		o.corrupt(&v.Item)
	}
	return v, err
}

func TestOwnerSnapshotOutcomeAndRevisionMismatchRefuseNativeSend(t *testing.T) {
	for _, corrupt := range []func(*hitl.ItemView){
		func(v *hitl.ItemView) { v.ItemID = "other-owner-item" },
		func(v *hitl.ItemView) { v.Revision++ },
		func(v *hitl.ItemView) { v.TerminalOutcome = nil },
		func(v *hitl.ItemView) { v.State = hitl.StateCanceled },
		func(v *hitl.ItemView) {
			var req hitl.EnqueueRequest
			_ = json.Unmarshal(v.RequestSnapshot, &req)
			req.IdempotencyKey = "other-request"
			v.RequestSnapshot, _ = json.Marshal(req)
		},
	} {
		f := newFixture()
		ticket := f.ticket(t)
		f.respond(t, ticket, "native-allow-once")
		f.c.cfg.Interactions = corruptOwner{InteractionPort: f.service, corrupt: corrupt}
		if _, err := f.c.Await(human(), ticket, gate.WaitOptions{}); !errors.Is(err, ErrInteraction) {
			t.Fatal(err)
		}
		if _, err := f.c.Deliver(human(), ticket); err == nil || f.origin.sends != 0 {
			t.Fatal(err)
		}
	}
}

func TestUnverifiedExistingOwnerResponseCannotEmitAllow(t *testing.T) {
	for _, option := range []string{"unknown-option", "native-allow-once"} {
		f := newFixture()
		ticket := f.ticket(t)
		_, err := f.service.Respond(human(), hitl.RespondCommand{ItemID: ticket.Handle.ItemID, ExpectedRevision: &ticket.Handle.Revision, Response: hitl.Response{Kind: InteractionKind, Decision: option}, Participant: hitl.Participant{PrincipalRef: "urn:human:alice", Authority: "caller-claims-verified", Assurance: "passkey"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.c.Deliver(human(), ticket); err == nil || f.origin.sends != 0 {
			t.Fatal("forged owner response emitted native allow", err)
		}
	}
}

func TestStructuralBindingRequirementsRefuseBeforeEnqueue(t *testing.T) {
	for _, change := range []func(*gate.Request){func(r *gate.Request) { r.Binding.Generation = 0 }, func(r *gate.Request) { r.Binding.OwnerID = "" }, func(r *gate.Request) { r.OperationDigest.Value = "" }, func(r *gate.Request) { r.EnforcementPoint = "" }, func(r *gate.Request) {
		r.ExpiresAt = r.ExpiresAt.Add(-r.ExpiresAt.Sub(r.ExpiresAt))
		r.ExpiresAt = r.ExpiresAt.AddDate(-1, 0, 0)
	}} {
		f := newFixture()
		change(&f.request)
		if _, err := f.c.Request(human(), f.request); err == nil {
			t.Fatal("invalid binding accepted")
		}
	}
}
