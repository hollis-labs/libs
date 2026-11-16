package gateconnector

import (
	"context"
	"fmt"

	"github.com/hollis-labs/libs/mesh/core/gate"
	"github.com/hollis-labs/libs/mesh/core/hitl"
)

// ServiceHTTPPort describes the current Gatecrasher service HTTP seam. At the
// pinned contract in testdata, the service exposes response assessment and a
// bounded recipe endpoint, but no GatePort request, await or cancel routes.
// Supplying this port refuses explicitly; it never treats an assessment as a
// stored decision, retries via screen text or makes an unadvertised HTTP call.
// Use an injected in-process GatePort until a real authenticated API exists.
type ServiceHTTPPort struct{}

var _ gate.GatePort = ServiceHTTPPort{}

func (ServiceHTTPPort) Capabilities(context.Context, gate.AdapterScope) (gate.Capabilities, error) {
	return gate.Capabilities{}, fmt.Errorf("%w: Gatecrasher service HTTP has no GatePort capability endpoint", gate.ErrUnsupported)
}
func (ServiceHTTPPort) Request(context.Context, gate.Request) (gate.Ticket, error) {
	return gate.Ticket{}, fmt.Errorf("%w: Gatecrasher service HTTP has no Request endpoint", gate.ErrUnsupported)
}
func (ServiceHTTPPort) Await(context.Context, gate.Ticket, gate.WaitOptions) (gate.Resolution, error) {
	return gate.Resolution{}, fmt.Errorf("%w: Gatecrasher service HTTP has no Await endpoint", gate.ErrUnsupported)
}
func (ServiceHTTPPort) Cancel(context.Context, gate.Ticket, hitl.CancelCause) (gate.Resolution, error) {
	return gate.Resolution{}, fmt.Errorf("%w: Gatecrasher service HTTP has no Cancel endpoint", gate.ErrUnsupported)
}
