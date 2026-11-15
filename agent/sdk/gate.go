package sdk

import (
	"context"
	"fmt"
	"time"

	"github.com/hollis-labs/libs/mesh/core/gate"
)

// NativeAdapterScope identifies the SDK-native headless execution mode. The initial
// adapter offers exact once choices only, never session or policy amendment.
func NativeAdapterScope() gate.AdapterScope {
	return gate.AdapterScope{AdapterID: "tradecraft", Protocol: "sdk-native-v1", LaunchMode: "headless"}
}

// GateAdapter negotiates the SDK-native request against an injected lifecycle.
// It creates no decision store or responder identity. Await timeout/cancellation
// ends only the wait; the host explicitly cancels a ticket through GatePort.
type GateAdapter struct {
	Port gate.GatePort
	Wait gate.WaitOptions
}

func (a GateAdapter) Resolve(ctx context.Context, request gate.Request, now time.Time) (gate.Resolution, error) {
	if a.Port == nil {
		return gate.Resolution{}, fmt.Errorf("%w: GatePort required", ErrInvalid)
	}
	if request.Adapter != NativeAdapterScope() {
		return gate.Resolution{}, ErrUnsupported
	}
	for _, o := range request.Options {
		if o.Scope.Kind != gate.Once || o.Scope.MaxUses != 1 {
			return gate.Resolution{}, ErrUnsupported
		}
	}
	if err := a.Wait.Validate(); err != nil {
		return gate.Resolution{}, err
	}
	caps, err := a.Port.Capabilities(ctx, request.Adapter)
	if err != nil {
		return gate.Resolution{}, err
	}
	if err = caps.ValidateRequest(request, now); err != nil {
		return gate.Resolution{}, err
	}
	snapshot, err := request.Snapshot()
	if err != nil {
		return gate.Resolution{}, err
	}
	ticket, err := a.Port.Request(ctx, snapshot)
	if err != nil {
		return gate.Resolution{}, err
	}
	digest, err := request.Digest()
	if err != nil {
		return gate.Resolution{}, err
	}
	if err = ticket.Validate(); err != nil {
		return gate.Resolution{}, err
	}
	if ticket.Binding != request.Binding || ticket.RequestDigest != digest {
		return gate.Resolution{}, gate.ErrBinding
	}
	out, err := a.Port.Await(ctx, ticket, a.Wait)
	if err != nil {
		return gate.Resolution{State: gate.Pending, Ticket: ticket}, err
	}
	if err = out.Validate(); err != nil {
		return gate.Resolution{}, err
	}
	if out.Ticket != ticket {
		return gate.Resolution{}, gate.ErrBinding
	}
	return out, nil
}
