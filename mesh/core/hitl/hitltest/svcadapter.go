package hitltest

import (
	"context"
	"encoding/json"

	"github.com/hollis-labs/substrate/mesh/hitl"
)

// ServiceAdapter adapts a *hitl.Service to Adapter, so the reference
// implementation runs through the same conformance suite as any other. It
// declares CapExpiry and CapCallerIsolation (see ServiceCaps).
type ServiceAdapter struct {
	svc *hitl.Service
	// Participant is recorded on every ParticipantResolve. NewServiceAdapter
	// sets a human responder with asserted assurance.
	Participant hitl.Participant
}

var _ Adapter = (*ServiceAdapter)(nil)

// NewServiceAdapter returns an Adapter over svc.
func NewServiceAdapter(svc *hitl.Service) *ServiceAdapter {
	return &ServiceAdapter{svc: svc, Participant: hitl.Participant{
		Responder: &hitl.Responder{Kind: "human", Ref: "conformance-operator"},
		Assurance: "asserted",
	}}
}

// ServiceCaps are the capabilities hitl.Service provides.
func ServiceCaps() []Capability { return []Capability{CapExpiry, CapCallerIsolation} }

func reply(v any, err error) ([]byte, error) {
	if err != nil {
		return hitl.EncodeError(err), err
	}
	b, mErr := json.Marshal(v)
	return b, mErr
}

// Enqueue implements Adapter.
func (a *ServiceAdapter) Enqueue(ctx context.Context, doc []byte) ([]byte, error) {
	var req hitl.EnqueueRequest
	if err := json.Unmarshal(doc, &req); err != nil {
		return reply(nil, hitl.NewInvalidRequest(err))
	}
	h, err := a.svc.Enqueue(ctx, req)
	return reply(h, err)
}

// Get implements Adapter.
func (a *ServiceAdapter) Get(ctx context.Context, doc []byte) ([]byte, error) {
	cmd, err := hitl.DecodeGetCommand(doc)
	if err != nil {
		return reply(nil, err)
	}
	res, err := a.svc.Get(ctx, cmd)
	return reply(res, err)
}

// Await implements Adapter.
func (a *ServiceAdapter) Await(ctx context.Context, doc []byte) ([]byte, error) {
	cmd, err := hitl.DecodeAwaitCommand(doc)
	if err != nil {
		return reply(nil, err)
	}
	res, err := a.svc.Await(ctx, cmd)
	return reply(res, err)
}

// Withdraw implements Adapter.
func (a *ServiceAdapter) Withdraw(ctx context.Context, doc []byte) ([]byte, error) {
	cmd, err := hitl.DecodeWithdrawCommand(doc)
	if err != nil {
		return reply(nil, err)
	}
	out, err := a.svc.Withdraw(ctx, cmd)
	if err != nil {
		return reply(nil, err)
	}
	return reply(out, nil)
}

// ParticipantResolve implements Adapter.
func (a *ServiceAdapter) ParticipantResolve(ctx context.Context, itemID string, response []byte) ([]byte, error) {
	var r hitl.Response
	if err := json.Unmarshal(response, &r); err != nil {
		return reply(nil, hitl.NewInvalidRequest(err))
	}
	out, err := a.svc.Respond(ctx, hitl.RespondCommand{ItemID: itemID, Response: r, Participant: a.Participant})
	if err != nil {
		return reply(nil, err)
	}
	return reply(out, nil)
}

// ExpireDue implements Adapter.
func (a *ServiceAdapter) ExpireDue(ctx context.Context) error {
	_, err := a.svc.ExpireDue(ctx)
	return err
}
