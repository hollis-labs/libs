package gate_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/libs/mesh/core/gate"
	"github.com/hollis-labs/libs/mesh/core/gate/gatetest"
	"github.com/hollis-labs/libs/mesh/core/hitl"
	"github.com/hollis-labs/libs/mesh/core/hitl/memstore"
)

var ctx = context.Background()
var instant = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// fakeGate is a test-only adapter around the actual HITL lifecycle, not a
// coordinator implementation. Request snapshots live only in HITL's store.
type fakeGate struct {
	service *hitl.Service
	now     time.Time
	caps    gate.Capabilities
}

var _ gate.GatePort = (*fakeGate)(nil)

func newGate(r gate.Request) *fakeGate {
	f := &fakeGate{now: instant, caps: gatetest.Capabilities(r)}
	f.service = hitl.NewService(memstore.New(), hitl.Options{Clock: func() time.Time { return f.now }})
	return f
}
func (f *fakeGate) Capabilities(_ context.Context, a gate.AdapterScope) (gate.Capabilities, error) {
	if a != f.caps.Adapter {
		return gate.Capabilities{}, gate.ErrUnsupported
	}
	return f.caps, nil
}
func (f *fakeGate) Request(c context.Context, r gate.Request) (gate.Ticket, error) {
	if err := f.caps.ValidateRequest(r, f.now); err != nil {
		return gate.Ticket{}, err
	}
	snapshot, err := r.Snapshot()
	if err != nil {
		return gate.Ticket{}, err
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return gate.Ticket{}, err
	}
	h, err := f.service.Enqueue(c, hitl.EnqueueRequest{ContractVersion: hitl.ContractVersion, Kind: "gate", IdempotencyKey: r.IdempotencyKey, Source: hitl.SourceAssertion{ApplicationID: r.EnforcementPoint, AgentID: string(r.Binding.OperativeID)}, ExpiresAt: &snapshot.ExpiresAt, Extra: map[string]json.RawMessage{"gate_request": raw}})
	if err != nil {
		return gate.Ticket{}, err
	}
	digest, err := snapshot.Digest()
	return gate.Ticket{AuthorityRef: "fake-hitl-owner", Handle: h, RequestDigest: digest, Binding: snapshot.Binding}, err
}
func (f *fakeGate) view(c context.Context, ticket gate.Ticket) (hitl.ItemView, gate.Request, error) {
	v, err := f.service.Get(c, hitl.GetCommand{ContractVersion: hitl.ContractVersion, ItemID: ticket.Handle.ItemID, Caller: hitl.CallerAssertion{ApplicationID: "fake-host/tool-admission"}})
	if err != nil {
		return hitl.ItemView{}, gate.Request{}, err
	}
	var wrapper struct {
		Request gate.Request `json:"gate_request"`
	}
	if err = json.Unmarshal(v.Item.RequestSnapshot, &wrapper); err != nil {
		return hitl.ItemView{}, gate.Request{}, err
	}
	digest, err := wrapper.Request.Digest()
	if digest != ticket.RequestDigest || wrapper.Request.Binding != ticket.Binding {
		return hitl.ItemView{}, gate.Request{}, gate.ErrBinding
	}
	return v.Item, wrapper.Request, err
}
func (f *fakeGate) project(c context.Context, ticket gate.Ticket) (gate.Resolution, error) {
	item, r, err := f.view(c, ticket)
	if err != nil {
		return gate.Resolution{}, err
	}
	out := gate.Resolution{Ticket: ticket, State: gate.Pending, Outcome: item.TerminalOutcome}
	switch o := item.TerminalOutcome.(type) {
	case hitl.Resolved:
		for _, option := range r.Options {
			if option.ID == o.Resolution.Response.Decision {
				out.State = gate.Decided
				out.Decision = &gate.Decision{Ticket: ticket, Resolution: o.Resolution, OptionID: option.ID, Scope: option.Scope, OperationDigest: r.OperationDigest, AuthorityRef: "fake-authority", ExpiresAt: option.Scope.ExpiresAt}
				return out, nil
			}
		}
		return gate.Resolution{}, gate.ErrUnsupported
	case hitl.Expired:
		out.State = gate.Expired
	case hitl.Canceled:
		out.State = gate.Cancelled
	}
	return out, nil
}
func (f *fakeGate) Await(c context.Context, ticket gate.Ticket, w gate.WaitOptions) (gate.Resolution, error) {
	if err := w.Validate(); err != nil {
		return gate.Resolution{}, err
	}
	if err := c.Err(); err != nil {
		return gate.Resolution{}, err
	}
	ms := int(w.MaxWait / time.Millisecond)
	_, err := f.service.Await(c, hitl.AwaitCommand{ContractVersion: hitl.ContractVersion, ItemID: ticket.Handle.ItemID, Caller: hitl.CallerAssertion{ApplicationID: "fake-host/tool-admission"}, WaitMs: &ms})
	if err != nil {
		return gate.Resolution{}, err
	}
	return f.project(c, ticket)
}
func (f *fakeGate) Cancel(c context.Context, ticket gate.Ticket, cause hitl.CancelCause) (gate.Resolution, error) {
	if cause != hitl.CauseCallerWithdrawn {
		return gate.Resolution{}, gate.ErrUnsupported
	}
	_, err := f.service.Withdraw(c, hitl.WithdrawCommand{ContractVersion: hitl.ContractVersion, ItemID: ticket.Handle.ItemID, Caller: hitl.CallerAssertion{ApplicationID: "fake-host/tool-admission"}})
	if err != nil {
		return gate.Resolution{}, err
	}
	return f.project(c, ticket)
}
func (f *fakeGate) respond(ticket gate.Ticket, option, principal string) error {
	// A real host verifies the authenticated channel BEFORE the authoritative CAS.
	if principal != "human-1" && principal != "human-2" {
		return gate.ErrAuthority
	}
	_, r, err := f.view(ctx, ticket)
	if err != nil {
		return err
	}
	found := false
	for _, o := range r.Options {
		if o.ID == option {
			found = true
		}
	}
	if !found {
		return gate.ErrUnsupported
	}
	_, err = f.service.Respond(ctx, hitl.RespondCommand{ItemID: ticket.Handle.ItemID, Response: hitl.Response{Kind: "gate", Decision: option}, Participant: hitl.Participant{PrincipalRef: principal, Assurance: "fake-authenticated-channel"}})
	return err
}

// fakeHost resolves authoritative state rather than accepting a forged receipt.
// It also owns the live binding, environment, descriptor and atomic claim lock.
type fakeHost struct {
	gate       *fakeGate
	live       gate.Request
	descriptor string
	mu         sync.Mutex
	claims     map[string]bool
	effects    int
	delivery   []gate.DeliveryReceipt
	execution  []gate.ExecutionReceipt
}

func (h *fakeHost) Verify(c context.Context, r gate.Request, d gate.Decision) error {
	actual, err := h.gate.project(c, d.Ticket)
	if err != nil {
		return err
	}
	if actual.Decision == nil || !reflect.DeepEqual(*actual.Decision, d) {
		return gate.ErrAuthority
	}
	if r.Binding != h.live.Binding || r.EnforcementPoint != h.live.EnforcementPoint || r.OperationDigest != gatetest.Digest("tool-v1", h.descriptor) || r.Adapter != h.live.Adapter || !r.NativeRequestID.Equal(h.live.NativeRequestID) || !reflect.DeepEqual(r.Environment, h.live.Environment) {
		return gate.ErrAuthority
	}
	if r.Environment == nil {
		return gate.ErrAuthority
	}
	return r.Environment.ValidateAt(h.gate.now, []string{"tool.execute"})
}
func (h *fakeHost) RecordDelivery(_ context.Context, r gate.DeliveryReceipt) error {
	if err := r.Validate(); err != nil {
		return err
	}
	h.delivery = append(h.delivery, r)
	return nil
}
func (h *fakeHost) RecordExecution(_ context.Context, r gate.ExecutionReceipt) error {
	if err := r.Validate(); err != nil {
		return err
	}
	h.execution = append(h.execution, r)
	return nil
}
func (h *fakeHost) execute(r gate.Request, d gate.Decision, uncertain bool) error {
	// Holding the host lock fences replacement through verification, claim and
	// effect start; a real executor needs equivalent transactional admission.
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.claimLocked(r, d); err != nil {
		return err
	}
	return h.effectLocked(r, d, uncertain)
}
func (h *fakeHost) claimLocked(r gate.Request, d gate.Decision) error {
	a := gate.Admission{Binding: h.live.Binding, Adapter: h.live.Adapter, EnforcementPoint: h.live.EnforcementPoint, OperationDigest: gatetest.Digest("tool-v1", h.descriptor), Now: h.gate.now}
	if err := gate.ValidateAdmission(ctx, r, d, a, h); err != nil {
		return err
	}
	if h.claims[d.Resolution.ResolutionID] {
		return errors.New("already claimed: reconcile")
	}
	h.claims[d.Resolution.ResolutionID] = true
	return nil
}
func (h *fakeHost) effectLocked(r gate.Request, d gate.Decision, uncertain bool) error {
	h.effects++
	state := gate.ExecutionSucceeded
	if uncertain {
		state = gate.ExecutionUnknown
	}
	return h.RecordExecution(ctx, gate.ExecutionReceipt{DecisionRef: d.Resolution.ResolutionID, OperationID: r.OperationID, Binding: r.Binding, State: state, EffectRef: "effect-1"})
}

type consumer func(*fakeHost, gate.Request, gate.Resolution, bool) error

func sdkTool(h *fakeHost, r gate.Request, res gate.Resolution, uncertain bool) error {
	if res.State != gate.Decided || res.Decision == nil {
		return gate.ErrDenied
	}
	return h.execute(r, *res.Decision, uncertain)
}
func nativeAdapter(h *fakeHost, r gate.Request, res gate.Resolution, uncertain bool) error {
	if res.State != gate.Decided || res.Decision == nil {
		return gate.ErrDenied
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	d := *res.Decision
	for _, receipt := range h.delivery {
		if receipt.Status == gate.DeliveryUnknown {
			return errors.New("delivery unknown: reconcile before retry")
		}
	}
	if !r.NativeRequestID.Equal(h.live.NativeRequestID) || r.Binding != h.live.Binding {
		return gate.ErrBinding
	}
	// The callback reply transports the exact option; no boolean/session fallback.
	var selected gate.NativeOption
	for _, o := range r.Options {
		if o.ID == d.OptionID {
			selected = o
		}
	}
	if selected.ID == "" {
		return gate.ErrUnsupported
	}
	if err := h.claimLocked(r, d); err != nil {
		return err
	}
	if err := h.RecordDelivery(ctx, gate.DeliveryReceipt{DecisionRef: d.Resolution.ResolutionID, Binding: r.Binding, AdapterID: r.Adapter.AdapterID, NativeRequestID: r.NativeRequestID, Status: gate.DeliveryAcknowledged}); err != nil {
		return err
	}
	return h.effectLocked(r, d, uncertain)
}
func setup(t *testing.T) (gate.Request, *fakeGate, gate.Ticket, *fakeHost) {
	t.Helper()
	r := gatetest.Request(instant)
	f := newGate(r)
	ticket, err := f.Request(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	live, err := r.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return r, f, ticket, &fakeHost{gate: f, live: live, descriptor: gatetest.OperationDescriptor, claims: map[string]bool{}}
}
func decided(t *testing.T, f *fakeGate, ticket gate.Ticket, option string) gate.Resolution {
	t.Helper()
	if err := f.respond(ticket, option, "human-1"); err != nil {
		t.Fatal(err)
	}
	res, err := f.Await(ctx, ticket, gate.WaitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestSharedConsumersAdmissionAndUncertainEffects(t *testing.T) {
	for name, consume := range map[string]consumer{"sdk-tool": sdkTool, "native-adapter": nativeAdapter} {
		t.Run(name, func(t *testing.T) {
			t.Run("allow-once", func(t *testing.T) {
				r, f, ticket, h := setup(t)
				res := decided(t, f, ticket, "native-allow-once")
				if err := consume(h, r, res, false); err != nil {
					t.Fatal(err)
				}
				if err := consume(h, r, res, false); err == nil || h.effects != 1 {
					t.Fatalf("repeat admission: %v, effects %d", err, h.effects)
				}
			})
			for _, state := range []string{"pending", "deny", "expired", "cancelled"} {
				t.Run(state, func(t *testing.T) {
					r, f, ticket, h := setup(t)
					switch state {
					case "deny":
						decided(t, f, ticket, "native-deny")
					case "expired":
						f.now = r.ExpiresAt
					case "cancelled":
						if _, err := f.Cancel(ctx, ticket, hitl.CauseCallerWithdrawn); err != nil {
							t.Fatal(err)
						}
					}
					res, err := f.Await(ctx, ticket, gate.WaitOptions{})
					if err != nil {
						t.Fatal(err)
					}
					if err := consume(h, r, res, false); err == nil || h.effects != 0 {
						t.Fatalf("%s authorized effect: %v", state, err)
					}
				})
			}
			t.Run("unknown-effect-never-replayed", func(t *testing.T) {
				r, f, ticket, h := setup(t)
				res := decided(t, f, ticket, "native-allow-once")
				if err := consume(h, r, res, true); err != nil {
					t.Fatal(err)
				}
				if h.execution[0].State != gate.ExecutionUnknown {
					t.Fatal("effect uncertainty lost")
				}
				if err := consume(h, r, res, false); err == nil || h.effects != 1 {
					t.Fatal("uncertain effect replayed")
				}
			})
		})
	}
}

func TestImmutableRequestIdempotencyAndCompetingResponders(t *testing.T) {
	r, f, ticket, _ := setup(t)
	again, err := f.Request(ctx, r)
	if err != nil || again.Handle.ItemID != ticket.Handle.ItemID {
		t.Fatalf("idempotency: %v", err)
	}
	original := r.Options[0].Payload[0]
	r.Options[0].Payload[0] = '['
	r.Environment.Capabilities[0] = "changed"
	_, stored, err := f.view(ctx, ticket)
	if err != nil || stored.Options[0].Payload[0] != original || stored.Environment.Capabilities[0] != "tool.execute" {
		t.Fatalf("snapshot aliased: %v", err)
	}
	r = gatetest.Request(instant)
	r.OperationDigest = gatetest.Digest("tool-v1", "changed plan")
	if _, err := f.Request(ctx, r); !errors.Is(err, hitl.ErrIdempotencyConflict) {
		t.Fatalf("changed operation did not conflict: %v", err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i, choice := range []string{"native-allow-once", "native-deny"} {
		wg.Add(1)
		go func(i int, choice string) {
			defer wg.Done()
			results <- f.respond(ticket, choice, []string{"human-1", "human-2"}[i])
		}(i, choice)
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("got %d terminal winners", wins)
	}
	before, err := f.project(ctx, ticket)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.respond(ticket, "native-allow-once", "human-1"); err == nil {
		t.Fatal("terminal overwritten")
	}
	after, _ := f.project(ctx, ticket)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("immutable outcome changed")
	}
}

func TestWaitCancellationAndAbsoluteExpiry(t *testing.T) {
	r, f, ticket, _ := setup(t)
	c, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := f.Await(c, ticket, gate.WaitOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait: %v", err)
	}
	res, err := f.Await(ctx, ticket, gate.WaitOptions{})
	if err != nil || res.State != gate.Pending {
		t.Fatalf("wait cancelled item: %+v %v", res, err)
	}
	f.now = r.ExpiresAt
	if err := f.respond(ticket, "native-allow-once", "human-1"); err == nil {
		t.Fatal("late reply accepted without sweeper")
	}
	res, _ = f.Await(ctx, ticket, gate.WaitOptions{})
	if res.State != gate.Expired {
		t.Fatal("expiry not authoritative")
	}
}

func TestTerminalReplayStillAdmitsExactAuthoritativeDecision(t *testing.T) {
	r, f, ticket, h := setup(t)
	res := decided(t, f, ticket, "native-allow-once")
	replay, err := f.Request(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Handle.ItemID != ticket.Handle.ItemID || replay.Handle.Revision != res.Decision.Resolution.InteractionRevision {
		t.Fatalf("not a current terminal replay: %+v", replay)
	}
	recovered, err := f.Await(ctx, replay, gate.WaitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := sdkTool(h, r, recovered, false); err != nil {
		t.Fatalf("recoverable decision refused: %v", err)
	}
	if h.effects != 1 {
		t.Fatal("recovery did not execute exactly one local effect")
	}
}

func TestAdmissionRefusesChangedLiveFactsAndForgedAuthority(t *testing.T) {
	tests := []struct {
		name   string
		change func(*fakeHost, *gate.Request, *gate.Decision)
		want   error
	}{
		{"new-generation", func(h *fakeHost, r *gate.Request, d *gate.Decision) { h.live.Binding.Generation++ }, gate.ErrBinding},
		{"new-attempt", func(h *fakeHost, r *gate.Request, d *gate.Decision) { h.live.Binding.AttemptID = "attempt-8" }, gate.ErrBinding},
		{"changed-plan", func(h *fakeHost, r *gate.Request, d *gate.Decision) {
			h.descriptor = strings.Replace(h.descriptor, "plan-1@1", "plan-1@2", 1)
		}, gate.ErrBinding},
		{"changed-enforcement", func(h *fakeHost, r *gate.Request, d *gate.Decision) { h.live.EnforcementPoint = "other-host" }, gate.ErrBinding},
		{"expired-at-admission", func(h *fakeHost, r *gate.Request, d *gate.Decision) { h.gate.now = r.ExpiresAt }, gate.ErrExpired},
		{"scope-widening", func(h *fakeHost, r *gate.Request, d *gate.Decision) { d.Scope.Kind = gate.Session; d.Scope.MaxUses = 0 }, gate.ErrBinding},
		{"unknown-option", func(h *fakeHost, r *gate.Request, d *gate.Decision) { d.OptionID = "invented-allow" }, gate.ErrUnsupported},
		{"forged-participant", func(h *fakeHost, r *gate.Request, d *gate.Decision) { d.Resolution.Participant.PrincipalRef = "self" }, gate.ErrAuthority},
		{"forged-authority-ref", func(h *fakeHost, r *gate.Request, d *gate.Decision) { d.AuthorityRef = "looks-verified" }, gate.ErrAuthority},
		{"expired-environment", func(h *fakeHost, r *gate.Request, d *gate.Decision) { h.live.Environment.ExpiresAt = instant }, gate.ErrAuthority},
		{"stale-lease", func(h *fakeHost, r *gate.Request, d *gate.Decision) { h.live.Environment.Lease.Revision = "2" }, gate.ErrAuthority},
		{"changed-resource", func(h *fakeHost, r *gate.Request, d *gate.Decision) { r.ResourceVersions[0].Revision = "2" }, gate.ErrBinding},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, f, ticket, h := setup(t)
			res := decided(t, f, ticket, "native-allow-once")
			d := *res.Decision
			tc.change(h, &r, &d)
			if err := h.execute(r, d, false); !errors.Is(err, tc.want) || h.effects != 0 {
				t.Fatalf("admitted invalid effect or wrong refusal: %v, effects %d", err, h.effects)
			}
		})
	}
	r, f, ticket, h := setup(t)
	res := decided(t, f, ticket, "native-allow-once")
	a := gate.Admission{Binding: r.Binding, Adapter: r.Adapter, EnforcementPoint: r.EnforcementPoint, OperationDigest: r.OperationDigest, Now: instant}
	if err := gate.ValidateAdmission(ctx, r, *res.Decision, a, nil); !errors.Is(err, gate.ErrAuthority) {
		t.Fatalf("nil verifier: %v", err)
	}
	if h.effects != 0 {
		t.Fatal("validation executed an effect")
	}
}

func TestNativeIDsAndChoiceFidelity(t *testing.T) {
	for _, raw := range []string{`"9007199254740993"`, `9007199254740993`, `-37`, `1.25`} {
		t.Run(raw, func(t *testing.T) {
			r := gatetest.Request(instant)
			r.NativeRequestID = gate.NativeRequestID(raw)
			b, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			var out gate.Request
			if err := json.Unmarshal(b, &out); err != nil {
				t.Fatal(err)
			}
			if string(out.NativeRequestID) != raw || !reflect.DeepEqual(r.Options, out.Options) {
				t.Fatalf("native fidelity lost: %s", b)
			}
		})
	}
	for _, raw := range []string{`null`, `true`, `{}`, `[]`, `1 2`, ``} {
		if err := gate.NativeRequestID(raw).Validate(); err == nil {
			t.Fatalf("invalid ID %s accepted", raw)
		}
	}
	if gate.NativeRequestID(`"1"`).Equal(gate.NativeRequestID(`1`)) {
		t.Fatal("string ID flattened to number")
	}
	r := gatetest.Request(instant)
	c := gatetest.Capabilities(r)
	for _, kind := range []gate.ScopeKind{gate.Session, gate.PolicyAmendment, gate.ScopeKind("future-scope")} {
		changed, _ := r.Snapshot()
		changed.Options[0].Scope.Kind = kind
		changed.Options[0].Scope.MaxUses = 0
		if err := c.ValidateRequest(changed, instant); !errors.Is(err, gate.ErrUnsupported) {
			t.Fatalf("unsupported scope %s: %v", kind, err)
		}
	}
	for _, kind := range []gate.ScopeKind{gate.Session, gate.PolicyAmendment} {
		changed, _ := r.Snapshot()
		changed.Options[0].Scope.Kind = kind
		changed.Options[0].Scope.MaxUses = 0
		c.ScopeKinds = append(c.ScopeKinds, kind)
		if err := c.ValidateRequest(changed, instant); err != nil {
			t.Fatal(err)
		}
		f := newGate(changed)
		f.caps = c
		ticket, err := f.Request(ctx, changed)
		if err != nil {
			t.Fatal(err)
		}
		res := decided(t, f, ticket, "native-allow-once")
		if res.Decision.Scope.Kind != kind || res.Decision.OptionID != "native-allow-once" {
			t.Fatal("advertised exact scope flattened")
		}
	}
	for _, kind := range []string{"dry_run_only", "rate_limit", "future-obligation"} {
		changed, _ := r.Snapshot()
		changed.Obligations = append(changed.Obligations, gate.ObligationRef{Kind: kind, Ref: gatetest.Reference("intent-2")})
		if err := c.ValidateRequest(changed, instant); !errors.Is(err, gate.ErrUnsupported) {
			t.Fatalf("mandatory obligation %s disappeared: %v", kind, err)
		}
	}
	r.Adapter.LaunchMode = "bypass"
	if err := c.ValidateRequest(r, instant); !errors.Is(err, gate.ErrUnsupported) {
		t.Fatalf("unsupported launch mode: %v", err)
	}
}

func TestDecisionDeliveryExecutionAndUnknownAckAreSeparate(t *testing.T) {
	r, f, ticket, h := setup(t)
	res := decided(t, f, ticket, "native-allow-once")
	if h.effects != 0 || len(h.delivery) != 0 || len(h.execution) != 0 {
		t.Fatal("decision implied delivery/execution")
	}
	receipt := gate.DeliveryReceipt{DecisionRef: res.Decision.Resolution.ResolutionID, Binding: r.Binding, AdapterID: r.Adapter.AdapterID, NativeRequestID: r.NativeRequestID, Status: gate.DeliverySent}
	if err := h.RecordDelivery(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	if h.effects != 0 || len(h.execution) != 0 {
		t.Fatal("transport write implied execution")
	}
	receipt.Status = gate.DeliveryUnknown
	if err := h.RecordDelivery(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	// Simulate reconnect using recovered authoritative decision and retained
	// delivery evidence; an unknown acknowledgement cannot cause a resend/effect.
	recovered, err := f.Await(ctx, ticket, gate.WaitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := nativeAdapter(h, r, recovered, false); err == nil || h.effects != 0 {
		t.Fatal("unknown delivery replayed without reconciliation")
	}
}

func TestResponseAuthorizationAndCancellationRaces(t *testing.T) {
	_, f, ticket, _ := setup(t)
	if err := f.respond(ticket, "native-allow-once", "operative-1"); !errors.Is(err, gate.ErrAuthority) {
		t.Fatalf("self-approval: %v", err)
	}
	res, _ := f.Await(ctx, ticket, gate.WaitOptions{})
	if res.State != gate.Pending {
		t.Fatal("unauthorized response persisted")
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = f.respond(ticket, "native-allow-once", "human-1") }()
	go func() { defer wg.Done(); _, _ = f.Cancel(ctx, ticket, hitl.CauseCallerWithdrawn) }()
	wg.Wait()
	before, err := f.Await(ctx, ticket, gate.WaitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if before.State != gate.Decided && before.State != gate.Cancelled {
		t.Fatalf("race has no terminal outcome: %s", before.State)
	}
	_, _ = f.Cancel(ctx, ticket, hitl.CauseCallerWithdrawn)
	_ = f.respond(ticket, "native-deny", "human-2")
	after, _ := f.Await(ctx, ticket, gate.WaitOptions{})
	if !reflect.DeepEqual(before, after) {
		t.Fatal("terminal race outcome changed")
	}
}

func TestOnceClaimIsAtomicAtEffectStart(t *testing.T) {
	r, f, ticket, h := setup(t)
	res := decided(t, f, ticket, "native-allow-once")
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- sdkTool(h, r, res, false) }()
	}
	wg.Wait()
	close(results)
	admitted := 0
	for err := range results {
		if err == nil {
			admitted++
		}
	}
	if admitted != 1 || h.effects != 1 {
		t.Fatalf("once claim admitted %d, effects %d", admitted, h.effects)
	}
}

func TestNativeAllowRequiresAuthorityBeforeDelivery(t *testing.T) {
	for _, name := range []string{"forged-authority", "expired", "changed-digest", "stale-generation", "unknown-native-request"} {
		t.Run(name, func(t *testing.T) {
			r, f, ticket, h := setup(t)
			res := decided(t, f, ticket, "native-allow-once")
			switch name {
			case "forged-authority":
				res.Decision.Resolution.Participant.PrincipalRef = "unverified"
			case "expired":
				f.now = r.ExpiresAt
			case "changed-digest":
				h.descriptor = strings.Replace(h.descriptor, `"value":1`, `"value":2`, 1)
			case "stale-generation":
				h.live.Binding.Generation++
			case "unknown-native-request":
				h.live.NativeRequestID = gate.NativeRequestID(`"other"`)
			}
			if err := nativeAdapter(h, r, res, false); err == nil {
				t.Fatal("invalid native approval accepted")
			}
			if len(h.delivery) != 0 || h.effects != 0 {
				t.Fatal("native allow delivered before trusted admission")
			}
		})
	}
}

func TestResolutionWireRetainsAuthoritativeOutcomeAndDiscriminant(t *testing.T) {
	for _, state := range []gate.ResolutionState{gate.Pending, gate.Decided, gate.Cancelled, gate.Expired} {
		t.Run(string(state), func(t *testing.T) {
			r, f, ticket, _ := setup(t)
			switch state {
			case gate.Decided:
				decided(t, f, ticket, "native-allow-once")
			case gate.Cancelled:
				if _, err := f.Cancel(ctx, ticket, hitl.CauseCallerWithdrawn); err != nil {
					t.Fatal(err)
				}
			case gate.Expired:
				f.now = r.ExpiresAt
			}
			res, err := f.Await(ctx, ticket, gate.WaitOptions{})
			if err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(res)
			if err != nil {
				t.Fatal(err)
			}
			var out gate.Resolution
			if err := json.Unmarshal(b, &out); err != nil {
				t.Fatal(err)
			}
			if out.State != state || out.Ticket != res.Ticket {
				t.Fatalf("resolution changed: %s", b)
			}
			if state != gate.Pending && (out.Outcome == nil || out.Outcome.OutcomeItemID() != ticket.Handle.ItemID) {
				t.Fatalf("lost authoritative outcome: %s", b)
			}
			malformed := res
			malformed.State = gate.ResolutionState("unknown")
			if err := malformed.Validate(); err == nil {
				t.Fatal("unknown discriminant accepted")
			}
			if state != gate.Pending {
				malformed = res
				malformed.State = gate.Pending
				if err := malformed.Validate(); err == nil {
					t.Fatal("terminal data accepted as pending")
				}
			}
		})
	}
}
