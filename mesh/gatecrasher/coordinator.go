package gatecrasher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/hollis-labs/libs/mesh/core/gate"
	"github.com/hollis-labs/libs/mesh/core/hitl"
)

const InteractionKind = "gate_native_v1"

type Config struct {
	// OwnerRef and CallerScope are trusted host configuration, not caller input.
	OwnerRef      string
	CallerScope   string
	Interactions  InteractionPort
	Journal       Journal
	Policy        PolicyPort
	Responses     ResponseAuthority
	Verifier      gate.AuthorityVerifier
	Origin        Origin
	Clock         func() time.Time
	LeaseDuration time.Duration
}

type Coordinator struct{ cfg Config }

var _ gate.GatePort = (*Coordinator)(nil)
var _ gate.ReceiptSink = (*Coordinator)(nil)

func New(c Config) (*Coordinator, error) {
	if c.OwnerRef == "" || c.CallerScope == "" || c.Interactions == nil || c.Journal == nil || c.Responses == nil || c.Verifier == nil || c.Origin == nil || c.Policy.Binding == nil {
		return nil, gate.ErrInvalid
	}
	if err := c.Policy.Options.Validate(); err != nil {
		return nil, gate.ErrInvalid
	}
	if c.Clock == nil {
		c.Clock = time.Now
	}
	if c.LeaseDuration <= 0 {
		return nil, gate.ErrInvalid
	}
	return &Coordinator{cfg: c}, nil
}

func (c *Coordinator) Capabilities(ctx context.Context, a gate.AdapterScope) (gate.Capabilities, error) {
	if err := a.Validate(); err != nil {
		return gate.Capabilities{}, err
	}
	cap, err := c.cfg.Origin.Capabilities(ctx, a)
	if err != nil {
		return gate.Capabilities{}, err
	}
	if cap.Adapter != a {
		return gate.Capabilities{}, gate.ErrBinding
	}
	// The first coordinator supports exact once options and approval only.
	if slices.Contains(cap.ScopeKinds, gate.Once) {
		cap.ScopeKinds = []gate.ScopeKind{gate.Once}
	} else {
		cap.ScopeKinds = nil
	}
	if slices.Contains(cap.ObligationKinds, "approval") {
		cap.ObligationKinds = []string{"approval"}
	} else {
		cap.ObligationKinds = nil
	}
	return cap, nil
}

func (c *Coordinator) Request(ctx context.Context, input gate.Request) (gate.Ticket, error) {
	r, err := input.Snapshot()
	if err != nil {
		return gate.Ticket{}, err
	}
	// Reserve first: reconnect conflicts are detected even if a changed request
	// is expired/unsupported. A failed initial validation never enqueues HITL.
	if old, err := c.cfg.Journal.Load(ctx, r.IdempotencyKey); err == nil {
		d, _ := r.Digest()
		od, _ := old.Request.Digest()
		if d != od {
			return gate.Ticket{}, ErrConflict
		}
		return c.attach(ctx, old)
	} else if !errors.Is(err, ErrNotFound) {
		return gate.Ticket{}, err
	}
	cap, err := c.Capabilities(ctx, r.Adapter)
	if err != nil {
		return gate.Ticket{}, err
	}
	if err := cap.ValidateRequest(r, c.cfg.Clock()); err != nil {
		return gate.Ticket{}, err
	}
	if err := c.cfg.Origin.Validate(ctx, r); err != nil {
		return gate.Ticket{}, err
	}
	if err := c.cfg.Policy.evaluate(ctx, r); err != nil {
		return gate.Ticket{}, err
	}
	route, err := c.cfg.Journal.Reserve(ctx, Route{Request: r, OwnerRef: c.cfg.OwnerRef, CallerScope: c.cfg.CallerScope})
	if err != nil {
		return gate.Ticket{}, err
	}
	return c.attach(ctx, route)
}

func (c *Coordinator) enqueue(r gate.Request) (hitl.EnqueueRequest, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return hitl.EnqueueRequest{}, err
	}
	// HITL canonicalizes nested JSON objects recursively. Keep the exact gate
	// snapshot as a string so opaque native payload bytes and numeric tokens
	// cannot be rewritten by the lifecycle owner. Presentation adapters decode
	// this explicit profile; it is not an existing application's approval card.
	b, err = json.Marshal(string(b))
	if err != nil {
		return hitl.EnqueueRequest{}, err
	}
	return hitl.EnqueueRequest{ContractVersion: hitl.ContractVersion, Kind: InteractionKind,
		IdempotencyKey: r.IdempotencyKey, Source: hitl.SourceAssertion{ApplicationID: c.cfg.CallerScope, AgentID: string(r.Binding.OperativeID)},
		ExpiresAt: &r.ExpiresAt, Extra: map[string]json.RawMessage{"gate_request": b}}, nil
}

func (c *Coordinator) attach(ctx context.Context, route Route) (gate.Ticket, error) {
	for {
		if route.OwnerRef != c.cfg.OwnerRef || route.CallerScope != c.cfg.CallerScope {
			return gate.Ticket{}, gate.ErrBinding
		}
		if err := ctx.Err(); err != nil {
			return gate.Ticket{}, err
		}
		if route.Ticket != nil {
			d, err := route.Request.Digest()
			if err != nil || route.Ticket.Validate() != nil || route.Ticket.AuthorityRef != c.cfg.OwnerRef || route.Ticket.Binding != route.Request.Binding || route.Ticket.RequestDigest != d {
				return gate.Ticket{}, gate.ErrBinding
			}
			return *route.Ticket, nil
		}
		req, err := c.enqueue(route.Request)
		if err != nil {
			return gate.Ticket{}, err
		}
		h, err := c.cfg.Interactions.Enqueue(ctx, req)
		if err != nil {
			return gate.Ticket{}, err
		}
		d, err := route.Request.Digest()
		if err != nil {
			return gate.Ticket{}, err
		}
		t := gate.Ticket{AuthorityRef: c.cfg.OwnerRef, Handle: h, RequestDigest: d, Binding: route.Request.Binding}
		if err := t.Validate(); err != nil {
			return gate.Ticket{}, ErrInteraction
		}
		// Validate returned owner snapshot before committing the mapping, including
		// the ambiguous enqueue/reconnect case returning a terminal handle.
		if _, err := c.getItem(ctx, route.Request, t); err != nil {
			return gate.Ticket{}, err
		}
		route.Ticket = &t
		_, err = c.cfg.Journal.Swap(ctx, route.Request.IdempotencyKey, route.Version, route)
		if err == nil {
			return t, nil
		}
		if !errors.Is(err, ErrCAS) {
			return gate.Ticket{}, err
		}
		route, err = c.cfg.Journal.Load(ctx, route.Request.IdempotencyKey)
		if err != nil {
			return gate.Ticket{}, err
		}
	}
}

func (c *Coordinator) caller() hitl.CallerAssertion {
	return hitl.CallerAssertion{ApplicationID: c.cfg.CallerScope}
}

func (c *Coordinator) route(ctx context.Context, t gate.Ticket) (Route, error) {
	if err := t.Validate(); err != nil {
		return Route{}, err
	}
	if t.AuthorityRef != c.cfg.OwnerRef {
		return Route{}, gate.ErrBinding
	}
	r, err := c.cfg.Journal.Find(ctx, t)
	if err != nil {
		return Route{}, err
	}
	d, err := r.Request.Digest()
	if err != nil || r.OwnerRef != c.cfg.OwnerRef || r.CallerScope != c.cfg.CallerScope || r.Ticket == nil || !reflect.DeepEqual(*r.Ticket, t) || t.RequestDigest != d || t.Binding != r.Request.Binding {
		return Route{}, gate.ErrBinding
	}
	return r, nil
}

func (c *Coordinator) getItem(ctx context.Context, r gate.Request, t gate.Ticket) (hitl.ItemView, error) {
	v, err := c.cfg.Interactions.Get(ctx, hitl.GetCommand{ContractVersion: hitl.ContractVersion, ItemID: t.Handle.ItemID, Caller: c.caller()})
	if err != nil {
		return hitl.ItemView{}, err
	}
	return v.Item, c.checkItem(r, t, v.Item)
}

func (c *Coordinator) checkItem(r gate.Request, t gate.Ticket, v hitl.ItemView) error {
	if v.ContractVersion != hitl.ContractVersion || v.ItemID != t.Handle.ItemID || v.Revision < t.Handle.Revision || !v.State.Valid() {
		return ErrInteraction
	}
	if t.Handle.State.IsTerminal() && (v.State != t.Handle.State || v.Revision != t.Handle.Revision) {
		return ErrInteraction
	}
	var req hitl.EnqueueRequest
	if err := json.Unmarshal(v.RequestSnapshot, &req); err != nil {
		return ErrInteraction
	}
	expected, err := c.enqueue(r)
	if err != nil {
		return ErrInteraction
	}
	// The complete core request and gate payload must survive the owner adapter.
	a, _ := json.Marshal(req)
	b, _ := json.Marshal(expected)
	if !reflect.DeepEqual(a, b) {
		return ErrInteraction
	}
	if v.State.IsTerminal() {
		if v.TerminalOutcome == nil || v.TerminalOutcome.OutcomeItemID() != v.ItemID || v.TerminalOutcome.OutcomeRevision() != v.Revision || v.TerminalOutcome.OutcomeState() != v.State {
			return ErrInteraction
		}
	} else if v.TerminalOutcome != nil {
		return ErrInteraction
	}
	return nil
}

func (c *Coordinator) project(ctx context.Context, r Route, v hitl.ItemView) (gate.Resolution, error) {
	out, err := c.observe(r, v)
	if err != nil {
		return gate.Resolution{}, err
	}
	if out.Decision != nil {
		if err := c.cfg.Verifier.Verify(ctx, r.Request, *out.Decision); err != nil {
			return gate.Resolution{}, gate.ErrAuthority
		}
	}
	return out, nil
}

// observe reconstructs a historical projection from the authenticated owner.
// It does not verify current grant authority and can never authorize a send.
func (c *Coordinator) observe(r Route, v hitl.ItemView) (gate.Resolution, error) {
	t := *r.Ticket
	if err := c.checkItem(r.Request, t, v); err != nil {
		return gate.Resolution{}, err
	}
	out := gate.Resolution{Ticket: t, State: gate.Pending, Outcome: v.TerminalOutcome}
	if !v.State.IsTerminal() {
		return out, nil
	}
	switch v.State {
	case hitl.StateCanceled:
		out.State = gate.Cancelled
	case hitl.StateExpired:
		out.State = gate.Expired
	case hitl.StateResolved:
		// Re-decode sealed union so pointer/value choices by an adapter cannot
		// bypass the same validation used for wire owners.
		b, err := json.Marshal(v.TerminalOutcome)
		if err != nil {
			return gate.Resolution{}, ErrInteraction
		}
		o, err := hitl.UnmarshalOutcome(b)
		if err != nil {
			return gate.Resolution{}, ErrInteraction
		}
		resolved, ok := o.(hitl.Resolved)
		if !ok || resolved.Resolution.InteractionRevision != v.Revision || resolved.Resolution.Response.Kind != InteractionKind || !resolved.Resolution.ResolvedAt.Before(r.Request.ExpiresAt) {
			return gate.Resolution{}, ErrInteraction
		}
		var choice *gate.NativeOption
		for i := range r.Request.Options {
			if r.Request.Options[i].ID == resolved.Resolution.Response.Decision {
				choice = &r.Request.Options[i]
				break
			}
		}
		if choice == nil {
			return gate.Resolution{}, gate.ErrUnsupported
		}
		if !resolved.Resolution.ResolvedAt.Before(choice.Scope.ExpiresAt) {
			return gate.Resolution{}, ErrInteraction
		}
		d := gate.Decision{Ticket: t, Resolution: resolved.Resolution, OptionID: choice.ID, Scope: choice.Scope,
			OperationDigest: r.Request.OperationDigest, AuthorityRef: c.cfg.OwnerRef, ExpiresAt: choice.Scope.ExpiresAt}
		out.State, out.Decision = gate.Decided, &d
	default:
		return gate.Resolution{}, ErrInteraction
	}
	if err := out.Validate(); err != nil {
		return gate.Resolution{}, ErrInteraction
	}
	return out, nil
}

func (c *Coordinator) Await(ctx context.Context, t gate.Ticket, w gate.WaitOptions) (gate.Resolution, error) {
	if err := w.Validate(); err != nil {
		return gate.Resolution{}, err
	}
	r, err := c.route(ctx, t)
	if err != nil {
		return gate.Resolution{}, err
	}
	ms := int(w.MaxWait / time.Millisecond)
	v, err := c.cfg.Interactions.Await(ctx, hitl.AwaitCommand{ContractVersion: hitl.ContractVersion, ItemID: t.Handle.ItemID, Caller: c.caller(), WaitMs: &ms})
	if err != nil {
		return gate.Resolution{}, err
	}
	return c.project(ctx, r, v.Item)
}

// Respond authorizes before handing the exact native option to the sole HITL
// decision CAS. The returned resolution is always reloaded, never built from
// the submitted response. Races return the owner's typed conflict.
func (c *Coordinator) Respond(ctx context.Context, t gate.Ticket, a ResponseAttempt) (gate.Resolution, error) {
	r, err := c.route(ctx, t)
	if err != nil {
		return gate.Resolution{}, err
	}
	v, err := c.getItem(ctx, r.Request, t)
	if err != nil {
		return gate.Resolution{}, err
	}
	if a.ExpectedRevision != v.Revision || a.ExpectedRevision < 1 {
		return gate.Resolution{}, gate.ErrBinding
	}
	var choice *gate.NativeOption
	for i := range r.Request.Options {
		if r.Request.Options[i].ID == a.OptionID {
			choice = &r.Request.Options[i]
			break
		}
	}
	if choice == nil {
		return gate.Resolution{}, gate.ErrUnsupported
	}
	if !c.cfg.Clock().Before(choice.Scope.ExpiresAt) {
		return gate.Resolution{}, gate.ErrExpired
	}
	p, err := c.cfg.Responses.Authorize(ctx, r.Request, t, a)
	if err != nil {
		return gate.Resolution{}, gate.ErrAuthority
	}
	if err := p.Validate(); err != nil {
		return gate.Resolution{}, gate.ErrAuthority
	}
	if err := ctx.Err(); err != nil {
		return gate.Resolution{}, err
	}
	_, err = c.cfg.Interactions.Respond(ctx, hitl.RespondCommand{ItemID: t.Handle.ItemID, ExpectedRevision: &a.ExpectedRevision,
		Response: hitl.Response{Kind: InteractionKind, Decision: choice.ID}, Participant: p})
	if err != nil {
		return gate.Resolution{}, err
	}
	return c.Await(ctx, t, gate.WaitOptions{})
}

func (c *Coordinator) Cancel(ctx context.Context, t gate.Ticket, cause hitl.CancelCause) (gate.Resolution, error) {
	if cause != hitl.CauseCallerWithdrawn {
		return gate.Resolution{}, gate.ErrUnsupported
	}
	if _, err := c.route(ctx, t); err != nil {
		return gate.Resolution{}, err
	}
	_, err := c.cfg.Interactions.Withdraw(ctx, hitl.WithdrawCommand{ContractVersion: hitl.ContractVersion, ItemID: t.Handle.ItemID, Caller: c.caller()})
	if err != nil {
		return gate.Resolution{}, err
	}
	return c.Await(ctx, t, gate.WaitOptions{})
}

// Recover closes enqueue/mapping windows for a bounded page. It does not send
// decisions or repeat effects. Call Deliver separately on attached terminal
// routes, allowing each host to schedule retries and reconcile uncertainty.
func (c *Coordinator) Recover(ctx context.Context, cursor string, limit int) ([]gate.Ticket, string, error) {
	if limit < 1 || limit > 1000 {
		return nil, "", gate.ErrInvalid
	}
	routes, next, err := c.cfg.Journal.List(ctx, cursor, limit)
	if err != nil {
		return nil, "", err
	}
	var tickets []gate.Ticket
	for _, r := range routes {
		t, err := c.attach(ctx, r)
		if err != nil {
			return tickets, next, err
		}
		tickets = append(tickets, t)
	}
	return tickets, next, nil
}

func resolutionRef(r gate.Resolution) string {
	if r.Decision != nil {
		return r.Decision.Resolution.ResolutionID
	}
	if r.Outcome != nil {
		return fmt.Sprintf("%s/%s/%d", r.Ticket.AuthorityRef, r.Ticket.Handle.ItemID, r.Outcome.OutcomeRevision())
	}
	return ""
}
