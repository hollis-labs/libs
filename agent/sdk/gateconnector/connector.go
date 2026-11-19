// Package gateconnector opts one host integration into an injected GatePort.
// It retains immutable requests before opening interactions so a replacement
// client can retry an ambiguous request by its idempotency key. A GatePort that
// refuses re-request after expiry needs its own durable journal recovery path.
// The host owns the durable store, identity, admission and execution effects.
package gateconnector

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hollis-labs/libs/mesh/core/gate"
	"github.com/hollis-labs/libs/mesh/core/hitl"
	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
)

var (
	ErrDisabled     = errors.New("gateconnector: integration disabled")
	ErrUnconfigured = errors.New("gateconnector: port and durable store required")
	ErrStale        = errors.New("gateconnector: attempt binding changed")
)

// Store must durably reserve the complete immutable request before Open calls
// GatePort.Request. Reserve is idempotent for an identical snapshot and conflicts
// on any changed snapshot under the same key. Attach is idempotent for the exact
// ticket and conflicts on a different ticket. Load returns detached values.
// A committed reservation or an ambiguous commit is never silently discarded.
type Store interface {
	Reserve(context.Context, gate.Request) error
	Attach(context.Context, string, gate.Ticket) error
	Load(context.Context, string) (gate.Request, *gate.Ticket, error)
}

// Connector is configured once per host integration point. The zero value is
// off. Enabling it never grants execution authority; the executor still uses
// gate.ValidateAdmission and its own atomic claim before any effect.
type Connector struct {
	Enabled bool
	Port    gate.GatePort
	Store   Store
	Now     func() time.Time
}

func (c Connector) ready() error {
	if !c.Enabled {
		return ErrDisabled
	}
	if c.Port == nil || c.Store == nil {
		return ErrUnconfigured
	}
	return nil
}

func (c Connector) clock() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func checkTicket(request gate.Request, ticket gate.Ticket) error {
	if err := ticket.Validate(); err != nil {
		return err
	}
	digest, err := request.Digest()
	if err != nil {
		return err
	}
	if ticket.Binding != request.Binding || ticket.RequestDigest != digest {
		return gate.ErrBinding
	}
	return nil
}

// Open reserves the snapshot first. If Request or Attach returns an ambiguous
// error, the retained reservation lets Reconcile retry the same idempotent
// GatePort.Request. If that port cannot re-request an expired interaction, its
// owner must recover from its authoritative journal; this package cannot
// manufacture a missing ticket or infer approval from UI text.
func (c Connector) Open(ctx context.Context, request gate.Request) (gate.Ticket, error) {
	if err := c.ready(); err != nil {
		return gate.Ticket{}, err
	}
	snapshot, err := request.Snapshot()
	if err != nil {
		return gate.Ticket{}, err
	}
	caps, err := c.Port.Capabilities(ctx, snapshot.Adapter)
	if err != nil {
		return gate.Ticket{}, err
	}
	if err := caps.ValidateRequest(snapshot, c.clock()); err != nil {
		return gate.Ticket{}, err
	}
	if err := c.Store.Reserve(ctx, snapshot); err != nil {
		return gate.Ticket{}, err
	}
	ticket, err := c.Port.Request(ctx, snapshot)
	if err != nil {
		return gate.Ticket{}, err
	}
	if err := checkTicket(snapshot, ticket); err != nil {
		return gate.Ticket{}, err
	}
	if err := c.Store.Attach(ctx, snapshot.IdempotencyKey, ticket); err != nil {
		return gate.Ticket{}, err
	}
	return ticket, nil
}

func (c Connector) retained(ctx context.Context, key string, binding runtimecontract.Binding) (gate.Request, gate.Ticket, error) {
	if err := c.ready(); err != nil {
		return gate.Request{}, gate.Ticket{}, err
	}
	request, attached, err := c.Store.Load(ctx, key)
	if err != nil {
		return gate.Request{}, gate.Ticket{}, err
	}
	if request.IdempotencyKey != key || request.Binding != binding {
		return gate.Request{}, gate.Ticket{}, ErrStale
	}
	if err := request.Validate(); err != nil {
		return gate.Request{}, gate.Ticket{}, err
	}
	if attached != nil {
		if err := checkTicket(request, *attached); err != nil {
			return gate.Request{}, gate.Ticket{}, err
		}
		return request, *attached, nil
	}
	// The first client may have died after GatePort accepted Request but before
	// Attach committed. The same immutable key and snapshot recover that ticket.
	ticket, err := c.Port.Request(ctx, request)
	if err != nil {
		return gate.Request{}, gate.Ticket{}, err
	}
	if err := checkTicket(request, ticket); err != nil {
		return gate.Request{}, gate.Ticket{}, err
	}
	if err := c.Store.Attach(ctx, key, ticket); err != nil {
		return gate.Request{}, gate.Ticket{}, err
	}
	return request, ticket, nil
}

// Reconcile queries the retained Gatecrasher interaction. A stale generation
// is rejected before any port call. This is an observation only: an old allow
// cannot be delivered to a replacement runtime or used as effect admission.
func (c Connector) Reconcile(ctx context.Context, key string, binding runtimecontract.Binding) (gate.Resolution, error) {
	_, ticket, err := c.retained(ctx, key, binding)
	if err != nil {
		return gate.Resolution{}, err
	}
	out, err := c.Port.Await(ctx, ticket, gate.WaitOptions{MaxWait: 0})
	if err != nil {
		return gate.Resolution{}, err
	}
	if err := out.Validate(); err != nil {
		return gate.Resolution{}, fmt.Errorf("gateconnector: invalid authoritative resolution: %w", err)
	}
	if out.Ticket != ticket {
		return gate.Resolution{}, gate.ErrBinding
	}
	return out, nil
}

// Cancel explicitly withdraws the retained interaction for the exact attempt.
// Cancelling a wait context is never treated as withdrawing the interaction.
func (c Connector) Cancel(ctx context.Context, key string, binding runtimecontract.Binding) (gate.Resolution, error) {
	_, ticket, err := c.retained(ctx, key, binding)
	if err != nil {
		return gate.Resolution{}, err
	}
	out, err := c.Port.Cancel(ctx, ticket, hitl.CauseCallerWithdrawn)
	if err != nil {
		return gate.Resolution{}, err
	}
	if err := out.Validate(); err != nil {
		return gate.Resolution{}, err
	}
	if out.Ticket != ticket {
		return gate.Resolution{}, gate.ErrBinding
	}
	return out, nil
}
