package gateconnector_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/libs/agent/sdk/gateconnector"
	"github.com/hollis-labs/libs/mesh/core/gate"
	"github.com/hollis-labs/libs/mesh/core/gate/gatetest"
	"github.com/hollis-labs/libs/mesh/core/hitl"
)

type saved struct {
	request gate.Request
	ticket  *gate.Ticket
}
type store struct {
	mu         sync.Mutex
	rows       map[string]saved
	failAttach bool
	calls      int
}

func newStore() *store { return &store{rows: map[string]saved{}} }
func (s *store) Reserve(_ context.Context, request gate.Request) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	snapshot, err := request.Snapshot()
	if err != nil {
		return err
	}
	if old, ok := s.rows[request.IdempotencyKey]; ok {
		if !reflect.DeepEqual(old.request, snapshot) {
			return errors.New("immutable request conflict")
		}
		return nil
	}
	s.rows[request.IdempotencyKey] = saved{request: snapshot}
	return nil
}
func (s *store) Attach(_ context.Context, key string, ticket gate.Ticket) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.failAttach {
		s.failAttach = false
		return errors.New("ambiguous attach commit")
	}
	row, ok := s.rows[key]
	if !ok {
		return errors.New("missing reservation")
	}
	if row.ticket != nil && *row.ticket != ticket {
		return errors.New("different ticket")
	}
	row.ticket = &ticket
	s.rows[key] = row
	return nil
}
func (s *store) Load(_ context.Context, key string) (gate.Request, *gate.Ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	row, ok := s.rows[key]
	if !ok {
		return gate.Request{}, nil, errors.New("missing reservation")
	}
	request, err := row.request.Snapshot()
	if err != nil {
		return gate.Request{}, nil, err
	}
	if row.ticket == nil {
		return request, nil, nil
	}
	ticket := *row.ticket
	return request, &ticket, nil
}

type fakeGate struct {
	request   gate.Request
	now       time.Time
	calls     int
	requests  int
	awaits    int
	cancels   int
	cancelled bool
	expired   bool
	selected  string
}

func (f *fakeGate) Capabilities(_ context.Context, a gate.AdapterScope) (gate.Capabilities, error) {
	f.calls++
	if a != f.request.Adapter {
		return gate.Capabilities{}, gate.ErrUnsupported
	}
	return gatetest.Capabilities(f.request), nil
}
func (f *fakeGate) Request(_ context.Context, request gate.Request) (gate.Ticket, error) {
	f.calls++
	f.requests++
	if !reflect.DeepEqual(request, f.request) {
		return gate.Ticket{}, gate.ErrBinding
	}
	digest, err := request.Digest()
	if err != nil {
		return gate.Ticket{}, err
	}
	return gate.Ticket{AuthorityRef: "fake-owner", Handle: hitl.Handle{ContractVersion: hitl.ContractVersion, ItemID: "interaction-1", State: hitl.StatePresented, Revision: 1}, RequestDigest: digest, Binding: request.Binding}, nil
}
func (f *fakeGate) Await(_ context.Context, ticket gate.Ticket, _ gate.WaitOptions) (gate.Resolution, error) {
	f.calls++
	f.awaits++
	if f.cancelled {
		return gate.Resolution{State: gate.Cancelled, Ticket: ticket, Outcome: hitl.Canceled{ItemID: ticket.Handle.ItemID, InteractionRevision: 2, Cause: hitl.CauseCallerWithdrawn, TerminatedAt: f.now}}, nil
	}
	if f.expired {
		return gate.Resolution{State: gate.Expired, Ticket: ticket, Outcome: hitl.Expired{ItemID: ticket.Handle.ItemID, InteractionRevision: 2, TerminatedAt: f.now}}, nil
	}
	if f.selected != "" {
		for _, option := range f.request.Options {
			if option.ID == f.selected {
				resolution := hitl.ResolutionRecord{ResolutionID: "decision-1", Response: hitl.Response{Kind: "gate", Decision: option.ID}, Participant: hitl.Participant{PrincipalRef: "human-1", Assurance: "verified"}, ResolvedAt: f.now, InteractionRevision: 2}
				return gate.Resolution{State: gate.Decided, Ticket: ticket, Outcome: hitl.Resolved{ItemID: ticket.Handle.ItemID, InteractionRevision: 2, Resolution: resolution}, Decision: &gate.Decision{Ticket: ticket, Resolution: resolution, OptionID: option.ID, Scope: option.Scope, OperationDigest: f.request.OperationDigest, AuthorityRef: "fake-authority", ExpiresAt: f.request.ExpiresAt}}, nil
			}
		}
		return gate.Resolution{}, gate.ErrUnsupported
	}
	return gate.Resolution{State: gate.Pending, Ticket: ticket}, nil
}
func (f *fakeGate) Cancel(ctx context.Context, ticket gate.Ticket, cause hitl.CancelCause) (gate.Resolution, error) {
	f.calls++
	f.cancels++
	if cause != hitl.CauseCallerWithdrawn {
		return gate.Resolution{}, gate.ErrUnsupported
	}
	f.cancelled = true
	return f.Await(ctx, ticket, gate.WaitOptions{})
}

func fixture(t *testing.T) (gate.Request, *fakeGate, *store, gateconnector.Connector) {
	t.Helper()
	now := time.Date(2026, 11, 16, 20, 0, 0, 0, time.UTC)
	request := gatetest.Request(now)
	port := &fakeGate{request: request, now: now}
	storage := newStore()
	connector := gateconnector.Connector{Enabled: true, Port: port, Store: storage, Now: func() time.Time { return now }}
	return request, port, storage, connector
}

func TestDisabledMakesNoGateOrStoreCalls(t *testing.T) {
	r, port, storage, connector := fixture(t)
	connector.Enabled = false
	if _, err := connector.Open(context.Background(), r); !errors.Is(err, gateconnector.ErrDisabled) {
		t.Fatal(err)
	}
	if _, err := connector.Reconcile(context.Background(), r.IdempotencyKey, r.Binding); !errors.Is(err, gateconnector.ErrDisabled) {
		t.Fatal(err)
	}
	if _, err := connector.Cancel(context.Background(), r.IdempotencyKey, r.Binding); !errors.Is(err, gateconnector.ErrDisabled) {
		t.Fatal(err)
	}
	if port.calls != 0 || storage.calls != 0 {
		t.Fatalf("disabled touched gate=%d store=%d", port.calls, storage.calls)
	}
}

func TestRestartReadsRetainedTicketAndCancelIsExplicit(t *testing.T) {
	r, port, storage, connector := fixture(t)
	ticket, err := connector.Open(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if port.requests != 1 {
		t.Fatalf("request calls %d", port.requests)
	}
	restarted := gateconnector.Connector{Enabled: true, Port: port, Store: storage, Now: connector.Now}
	result, err := restarted.Reconcile(context.Background(), r.IdempotencyKey, r.Binding)
	if err != nil || result.State != gate.Pending || result.Ticket != ticket || port.requests != 1 {
		t.Fatalf("restart: %+v %v, requests=%d", result, err, port.requests)
	}
	result, err = restarted.Cancel(context.Background(), r.IdempotencyKey, r.Binding)
	if err != nil || result.State != gate.Cancelled || port.cancels != 1 {
		t.Fatalf("cancel: %+v %v", result, err)
	}
}

func TestAmbiguousAttachReusesIdenticalRequest(t *testing.T) {
	r, port, storage, connector := fixture(t)
	storage.failAttach = true
	if _, err := connector.Open(context.Background(), r); err == nil {
		t.Fatal("expected attach error")
	}
	restarted := gateconnector.Connector{Enabled: true, Port: port, Store: storage, Now: connector.Now}
	result, err := restarted.Reconcile(context.Background(), r.IdempotencyKey, r.Binding)
	if err != nil || result.State != gate.Pending || port.requests != 2 {
		t.Fatalf("ambiguous recovery: %+v %v requests=%d", result, err, port.requests)
	}
}

func TestReconcileReadsAllowDenyAndExpiryFromPort(t *testing.T) {
	for _, state := range []string{"allow", "deny", "expired"} {
		t.Run(state, func(t *testing.T) {
			r, port, _, connector := fixture(t)
			if _, err := connector.Open(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			switch state {
			case "allow":
				port.selected = r.Options[0].ID
			case "deny":
				port.selected = r.Options[1].ID
			case "expired":
				port.expired = true
			}
			result, err := connector.Reconcile(context.Background(), r.IdempotencyKey, r.Binding)
			if err != nil {
				t.Fatal(err)
			}
			if state == "expired" {
				if result.State != gate.Expired || result.Decision != nil {
					t.Fatalf("expiry: %+v", result)
				}
			} else if result.State != gate.Decided || result.Decision == nil || result.Decision.OptionID != port.selected {
				t.Fatalf("selected native decision changed: %+v", result)
			}
		})
	}
}

func TestStaleAttemptAndChangedDigestRefuse(t *testing.T) {
	r, port, _, connector := fixture(t)
	if _, err := connector.Open(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	stale := r.Binding
	stale.Generation++
	stale.AttemptID = "attempt-8"
	before := port.calls
	if _, err := connector.Reconcile(context.Background(), r.IdempotencyKey, stale); !errors.Is(err, gateconnector.ErrStale) {
		t.Fatal(err)
	}
	if port.calls != before {
		t.Fatal("stale attempt contacted Gatecrasher")
	}
	changed := r
	changed.OperationDigest = gatetest.Digest("tool-v1", "changed operation")
	if _, err := connector.Open(context.Background(), changed); err == nil {
		t.Fatal("changed operation reused immutable key")
	}
	if port.requests != 1 {
		t.Fatal("changed operation opened a second interaction")
	}
}

func TestEvidenceSeparatesDecisionDeliveryAndExecution(t *testing.T) {
	r, _, _, connector := fixture(t)
	ticket, err := connector.Open(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	resolution := hitl.ResolutionRecord{
		ResolutionID: "decision-1", Response: hitl.Response{Kind: "gate", Decision: r.Options[0].ID},
		Participant: hitl.Participant{PrincipalRef: "human-1", Assurance: "verified"},
		ResolvedAt:  connector.Now(), InteractionRevision: 2,
	}
	decided := gate.Resolution{State: gate.Decided, Ticket: ticket,
		Outcome: hitl.Resolved{ItemID: ticket.Handle.ItemID, InteractionRevision: 2, Resolution: resolution},
		Decision: &gate.Decision{Ticket: ticket, Resolution: resolution, OptionID: r.Options[0].ID,
			Scope: r.Options[0].Scope, OperationDigest: r.OperationDigest, AuthorityRef: "fake-authority", ExpiresAt: r.ExpiresAt},
	}
	delivery := gate.DeliveryReceipt{DecisionRef: "decision-1", Binding: r.Binding, AdapterID: r.Adapter.AdapterID,
		NativeRequestID: r.NativeRequestID, Status: gate.DeliverySent, EvidenceRef: "write-attempt-1"}
	execution := gate.ExecutionReceipt{DecisionRef: "decision-1", Binding: r.Binding, OperationID: r.OperationID,
		State: gate.ExecutionUnknown, EvidenceRef: "effect-uncertain-1"}
	evidence, err := gateconnector.Project(r, decided, &delivery, &execution)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.DecisionID != "decision-1" || evidence.Scope == nil || evidence.Scope.Kind != gate.Once || evidence.Delivery.Status != gate.DeliverySent || evidence.Execution.State != gate.ExecutionUnknown {
		t.Fatalf("lost separate facts: %+v", evidence)
	}
	if evidence.Delivery.EvidenceRef != "write-attempt-1" || evidence.Execution.EvidenceRef != "effect-uncertain-1" {
		t.Fatalf("receipt refs collapsed: %+v", evidence)
	}
	line, err := gateconnector.EncodeApprovalLine(r, decided, &delivery, &execution, gateconnector.TranscriptIdentity{UUID: "approval-line-1", SessionID: "session-1", Timestamp: connector.Now()})
	if err != nil {
		t.Fatal(err)
	}
	var sideband struct {
		Type    string                     `json:"type"`
		Subtype string                     `json:"subtype"`
		Payload gateconnector.ApprovalLine `json:"payload"`
	}
	if err := json.Unmarshal(line, &sideband); err != nil {
		t.Fatal(err)
	}
	if sideband.Type != "system" || sideband.Subtype != "approval" || sideband.Payload.DecisionID != "decision-1" || sideband.Payload.DeliveryReceiptRef != "write-attempt-1" || sideband.Payload.ExecutionReceiptRef != "effect-uncertain-1" || sideband.Payload.ExecutionState != gate.ExecutionUnknown || sideband.Payload.Scope == nil || sideband.Payload.Scope.Kind != gate.Once {
		t.Fatalf("transcript line lost approval refs or scope: %+v", sideband)
	}
	delivery.DecisionRef = "different-decision"
	if _, err := gateconnector.Project(r, decided, &delivery, &execution); !errors.Is(err, gate.ErrBinding) {
		t.Fatalf("wrong decision accepted: %v", err)
	}
	if _, err := gateconnector.Project(r, gate.Resolution{State: gate.Pending, Ticket: ticket}, nil, &execution); !errors.Is(err, gate.ErrInvalid) {
		t.Fatalf("effect without decision accepted: %v", err)
	}
}

var _ gateconnector.Store = (*store)(nil)
var _ gate.GatePort = (*fakeGate)(nil)
