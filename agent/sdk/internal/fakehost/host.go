// Package fakehost is synthetic executable evidence and example scaffolding.
// It authenticates only its own in-memory fixtures and is not a production host.
package fakehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/hollis-labs/libs/agent/sdk"
	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
	"github.com/hollis-labs/libs/llm/core/llmtypes"
	"github.com/hollis-labs/libs/mesh/core/agentdef"
	"github.com/hollis-labs/libs/mesh/core/gate"
	"github.com/hollis-labs/libs/mesh/core/gate/gatetest"
	"github.com/hollis-labs/libs/mesh/core/hitl"
	"github.com/hollis-labs/libs/mesh/core/hitl/memstore"
	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
	"github.com/hollis-labs/libs/security/policy"
)

type runKey struct {
	Owner        runtimecontract.OwnerID
	Operative    runtimecontract.OperativeID
	Conversation runtimecontract.ConversationID
	ID           string
}

func key(r sdk.RunReservation) runKey {
	return runKey{r.Binding.OwnerID, r.Binding.OperativeID, r.Binding.ConversationID, r.RunID}
}

// RunLedger models authoritative retained host state across SDK instances and
// replacement hosts. Production hosts need durable transactional storage.
type RunLedger struct {
	mu      sync.Mutex
	entries map[runKey]sdk.RunReservation
}

type Host struct {
	Runs            *RunLedger
	mu              sync.Mutex
	Binding         runtimecontract.Binding
	Environment     runtimecontract.EnvironmentRef
	NowTime         time.Time
	Service         *hitl.Service
	Mode            string // allow, deny, pending, cancelled, expired, unverified
	Claims          map[string]bool
	Effects         int
	ModelCalls      int
	GateRequests    int
	Events          []runtimeevents.Event
	Receipts        []gate.ExecutionReceipt
	BeforeAdmission func(sdk.Admission)
	PolicyResult    *sdk.PolicyResult
	Model           *Model
}

func New() *Host {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := gatetest.Request(now)
	// These are the same neutral fixtures consumed by the gate conformance suite.
	h := &Host{Runs: &RunLedger{entries: map[runKey]sdk.RunReservation{}}, Binding: r.Binding, Environment: *r.Environment, NowTime: now, Claims: map[string]bool{}, Mode: "allow"}
	h.Service = hitl.NewService(memstore.New(), hitl.Options{Clock: h.Now})
	h.Model = &Model{Host: h}
	return h
}
func (h *Host) Now() time.Time { return h.NowTime }
func (h *Host) Input() sdk.Input {
	return sdk.Input{ID: "run-1", Binding: h.Binding, Environment: h.Environment, Text: "Read the synthetic value."}
}
func Definition() agentdef.Definition {
	return agentdef.Definition{SchemaVersion: agentdef.SchemaVersion, DefinitionID: "synthetic-operative", Revision: "1", Name: "synthetic-operative", Description: "Synthetic headless example", Body: "Use read_value once, then report its result.", Behavior: agentdef.Behavior{Purpose: "Exercise the native loop with fake effects"}, Requirements: agentdef.Requirements{Tools: []string{"read_value"}}, HarnessProfile: agentdef.HarnessProfile{Permissions: agentdef.PermissionProfile{Profile: "synthetic"}}, Continuity: agentdef.Continuity{Mode: agentdef.Ephemeral}}
}

func (h *Host) Ports() sdk.Ports {
	router, _ := sdk.NewRouter("fake", sdk.Route{Name: "fake", Model: "fake-model", Provider: h.Model})
	tool := sdk.Tool{Definition: llmtypes.ToolDefinition{Name: "read_value", Description: "Read a synthetic value", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}, Versions: []runtimecontract.Reference{gatetest.Reference("read-value-v1")}, Execute: func(ctx context.Context, args map[string]any) (sdk.ToolResult, error) {
		h.Effects++
		return sdk.ToolResult{Content: "42"}, nil
	}}
	return sdk.Ports{Router: router, Tools: []sdk.Tool{tool}, Environment: environmentPort{h}, Admission: h, Policy: h, Gate: sdk.GateAdapter{Port: h}, Authority: authorityPort{h}, Events: runtimeevents.SinkFunc(func(ctx context.Context, event runtimeevents.Event) error {
		if err := event.Validate(); err != nil {
			return err
		}
		h.Events = append(h.Events, event)
		return nil
	}), Now: h.Now, Capabilities: []string{"tool.execute"}}
}

type environmentPort struct{ h *Host }

func (p environmentPort) Verify(ctx context.Context, b runtimecontract.Binding, e runtimecontract.EnvironmentRef, required []string) error {
	p.h.mu.Lock()
	defer p.h.mu.Unlock()
	return p.h.verifyEnvironment(ctx, b, e, required)
}
func (h *Host) verifyEnvironment(ctx context.Context, b runtimecontract.Binding, e runtimecontract.EnvironmentRef, required []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b != h.Binding || !reflect.DeepEqual(e, h.Environment) {
		return gate.ErrBinding
	}
	return e.ValidateAt(h.Now(), required)
}
func (h *Host) CheckDefinition(_ context.Context, d agentdef.Definition, _ runtimecontract.Binding, _ runtimecontract.EnvironmentRef) error {
	if d.HarnessProfile.Permissions.Profile != "synthetic" {
		return gate.ErrAuthority
	}
	return nil
}
func (h *Host) Evaluate(_ context.Context, op sdk.Operation) (sdk.PolicyResult, error) {
	if h.PolicyResult != nil {
		return *h.PolicyResult, nil
	}
	return sdk.PolicyResult{Decision: policy.Decision{Effect: policy.Ask, Obligations: []policy.Obligation{{Kind: policy.Approval, PolicyID: "synthetic-policy", Reference: "approval-rule-1"}}}, Reference: gatetest.Reference("policy-1"), Obligations: []gate.ObligationRef{{Kind: "approval", Ref: gatetest.Reference("approval-rule-1")}}}, nil
}

// ReserveRun retains a single-use conversation-scoped ID atomically. It never
// interprets matching arguments as equivalent work or clears failed reservations.
func (h *Host) ReserveRun(ctx context.Context, r sdk.RunReservation) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.verifyEnvironment(ctx, r.Binding, r.Environment, nil); err != nil {
		return err
	}
	if r.RunID == "" || r.DefinitionDigest == "" || r.Route == "" || r.Model == "" || r.InputDigest.DescriptorVersion != "sdk-run-input-v1" {
		return sdk.ErrAdmission
	}
	if err := r.InputDigest.Validate(); err != nil {
		return err
	}
	h.Runs.mu.Lock()
	defer h.Runs.mu.Unlock()
	if _, exists := h.Runs.entries[key(r)]; exists {
		return sdk.ErrRunUsed
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	var snapshot sdk.RunReservation
	if err = json.Unmarshal(raw, &snapshot); err != nil {
		return err
	}
	h.Runs.entries[key(r)] = snapshot
	return nil
}

// Admit's lock fences this synthetic host's current binding through callback
// completion and keeps uncertain once claims consumed. It does not prove any
// production issuer, policy, filesystem or network authority.
func (h *Host) Admit(ctx context.Context, a sdk.Admission, start func(context.Context) error) error {
	if h.BeforeAdmission != nil {
		h.BeforeAdmission(a)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.verifyEnvironment(ctx, a.Binding, a.Environment, nil); err != nil {
		return err
	}
	h.Runs.mu.Lock()
	reserved, exists := h.Runs.entries[key(a.Run)]
	matches := exists && reflect.DeepEqual(reserved, a.Run)
	h.Runs.mu.Unlock()
	if !matches || a.Run.Binding != a.Binding || !reflect.DeepEqual(a.Run.Environment, a.Environment) || a.Run.DefinitionDigest != a.DefinitionDigest || (a.Kind == "model" && a.Route != a.Run.Route) {
		return sdk.ErrAdmission
	}
	if a.Kind == "tool" {
		if a.Operation == nil || a.Policy == nil || a.Operation.RunID != a.Run.RunID || a.Operation.InputDigest != a.Run.InputDigest {
			return sdk.ErrAdmission
		}
		digest, err := a.Operation.Digest()
		if err != nil {
			return err
		}
		if a.Operation.Tool != "read_value" || !reflect.DeepEqual(a.Operation.Versions, []runtimecontract.Reference{gatetest.Reference("read-value-v1")}) || a.Operation.Binding != h.Binding || !reflect.DeepEqual(a.Operation.Environment, h.Environment) {
			return gate.ErrAuthority
		}
		current, err := h.Evaluate(ctx, *a.Operation)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(current, *a.Policy) || current.Decision.Effect == policy.Deny {
			return gate.ErrAuthority
		}
		key := a.Operation.ID
		decisionRef := current.Reference.ID + "@" + current.Reference.Revision
		if current.Decision.Effect == policy.Ask || len(current.Decision.Obligations) > 0 {
			if a.Request == nil || a.Decision == nil {
				return sdk.ErrAdmission
			}
			if err = gate.ValidateAdmission(ctx, *a.Request, *a.Decision, gate.Admission{Binding: h.Binding, Adapter: sdk.NativeAdapterScope(), EnforcementPoint: "tradecraft/tool-admission", OperationDigest: digest, Now: h.Now()}, authorityPort{h}); err != nil {
				return err
			}
			decisionRef = a.Decision.Resolution.ResolutionID
		} else if a.Request != nil || a.Decision != nil {
			return sdk.ErrAdmission
		}
		if h.Claims[key] {
			return errors.New("synthetic claim consumed: reconcile")
		}
		h.Claims[key] = true
		h.Receipts = append(h.Receipts, gate.ExecutionReceipt{DecisionRef: decisionRef, OperationID: a.Operation.ID, Binding: h.Binding, State: gate.ExecutionStarted})
		err = start(ctx)
		state := gate.ExecutionSucceeded
		if err != nil {
			state = gate.ExecutionUnknown
		}
		h.Receipts = append(h.Receipts, gate.ExecutionReceipt{DecisionRef: decisionRef, OperationID: a.Operation.ID, Binding: h.Binding, State: state})
		return err
	}
	return start(ctx)
}

type authorityPort struct{ h *Host }

func (p authorityPort) Verify(ctx context.Context, r gate.Request, d gate.Decision) error {
	if p.h.Mode == "unverified" {
		return gate.ErrAuthority
	}
	actual, stored, err := p.h.project(ctx, d.Ticket)
	if err != nil {
		return err
	}
	digest, err := r.Digest()
	if err != nil {
		return err
	}
	storedDigest, err := stored.Digest()
	if err != nil {
		return err
	}
	if digest != storedDigest || actual.Decision == nil || !reflect.DeepEqual(*actual.Decision, d) || d.Resolution.Participant.PrincipalRef != "synthetic-responder" {
		return gate.ErrAuthority
	}
	if r.Binding != p.h.Binding || !reflect.DeepEqual(r.Environment, &p.h.Environment) {
		return gate.ErrBinding
	}
	// Verify the entire immutable policy intent; shape or caller labels alone
	// cannot substitute a different source, owner or mandatory obligation.
	if r.Policy != gatetest.Reference("policy-1") || !reflect.DeepEqual(r.Obligations, []gate.ObligationRef{{Kind: "approval", Ref: gatetest.Reference("approval-rule-1")}}) {
		return gate.ErrAuthority
	}
	return nil
}

func (h *Host) Capabilities(_ context.Context, a gate.AdapterScope) (gate.Capabilities, error) {
	if a != sdk.NativeAdapterScope() {
		return gate.Capabilities{}, gate.ErrUnsupported
	}
	return gate.Capabilities{Adapter: a, OperationClasses: []string{"tool"}, DecisionKinds: []gate.DecisionKind{gate.Allow, gate.Deny}, ScopeKinds: []gate.ScopeKind{gate.Once}, ObligationKinds: []string{"approval"}}, nil
}
func (h *Host) Request(ctx context.Context, r gate.Request) (gate.Ticket, error) {
	h.GateRequests++
	caps, _ := h.Capabilities(ctx, r.Adapter)
	if err := caps.ValidateRequest(r, h.Now()); err != nil {
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
	handle, err := h.Service.Enqueue(ctx, hitl.EnqueueRequest{ContractVersion: hitl.ContractVersion, Kind: "gate", IdempotencyKey: r.IdempotencyKey, Source: hitl.SourceAssertion{ApplicationID: r.EnforcementPoint, AgentID: string(r.Binding.OperativeID)}, ExpiresAt: &snapshot.ExpiresAt, Extra: map[string]json.RawMessage{"gate_request": raw}})
	if err != nil {
		return gate.Ticket{}, err
	}
	digest, err := r.Digest()
	return gate.Ticket{AuthorityRef: "synthetic-hitl", Handle: handle, RequestDigest: digest, Binding: r.Binding}, err
}
func (h *Host) project(ctx context.Context, ticket gate.Ticket) (gate.Resolution, gate.Request, error) {
	v, err := h.Service.Get(ctx, hitl.GetCommand{ContractVersion: hitl.ContractVersion, ItemID: ticket.Handle.ItemID, Caller: hitl.CallerAssertion{ApplicationID: "tradecraft/tool-admission"}})
	if err != nil {
		return gate.Resolution{}, gate.Request{}, err
	}
	var snapshot struct {
		Request gate.Request `json:"gate_request"`
	}
	if err = json.Unmarshal(v.Item.RequestSnapshot, &snapshot); err != nil {
		return gate.Resolution{}, gate.Request{}, err
	}
	digest, err := snapshot.Request.Digest()
	if err != nil {
		return gate.Resolution{}, gate.Request{}, err
	}
	if digest != ticket.RequestDigest || ticket.Binding != snapshot.Request.Binding || ticket.AuthorityRef != "synthetic-hitl" {
		return gate.Resolution{}, gate.Request{}, gate.ErrBinding
	}
	out := gate.Resolution{State: gate.Pending, Ticket: ticket, Outcome: v.Item.TerminalOutcome}
	switch o := v.Item.TerminalOutcome.(type) {
	case hitl.Resolved:
		for _, option := range snapshot.Request.Options {
			if option.ID == o.Resolution.Response.Decision {
				out.State = gate.Decided
				out.Decision = &gate.Decision{Ticket: ticket, Resolution: o.Resolution, OptionID: option.ID, Scope: option.Scope, OperationDigest: snapshot.Request.OperationDigest, AuthorityRef: "synthetic-authority", ExpiresAt: option.Scope.ExpiresAt}
				return out, snapshot.Request, nil
			}
		}
		return gate.Resolution{}, gate.Request{}, gate.ErrUnsupported
	case hitl.Expired:
		out.State = gate.Expired
	case hitl.Canceled:
		out.State = gate.Cancelled
	}
	return out, snapshot.Request, nil
}
func (h *Host) Await(ctx context.Context, ticket gate.Ticket, wait gate.WaitOptions) (gate.Resolution, error) {
	if err := wait.Validate(); err != nil {
		return gate.Resolution{}, err
	}
	current, r, err := h.project(ctx, ticket)
	if err != nil {
		return gate.Resolution{}, err
	}
	if current.State == gate.Pending {
		switch h.Mode {
		case "cancelled":
			return h.Cancel(ctx, ticket, hitl.CauseCallerWithdrawn)
		case "expired":
			h.NowTime = r.ExpiresAt
		case "pending":
		default:
			option := "allow-once"
			if h.Mode == "deny" {
				option = "deny"
			}
			_, err = h.Service.Respond(ctx, hitl.RespondCommand{ItemID: ticket.Handle.ItemID, Response: hitl.Response{Kind: "gate", Decision: option}, Participant: hitl.Participant{PrincipalRef: "synthetic-responder", Assurance: "synthetic-authenticated-channel"}})
			if err != nil {
				return gate.Resolution{}, err
			}
		}
	}
	ms := 0
	_, err = h.Service.Await(ctx, hitl.AwaitCommand{ContractVersion: hitl.ContractVersion, ItemID: ticket.Handle.ItemID, Caller: hitl.CallerAssertion{ApplicationID: "tradecraft/tool-admission"}, WaitMs: &ms})
	if err != nil {
		return gate.Resolution{}, err
	}
	out, _, err := h.project(ctx, ticket)
	return out, err
}
func (h *Host) Cancel(ctx context.Context, ticket gate.Ticket, cause hitl.CancelCause) (gate.Resolution, error) {
	if cause != hitl.CauseCallerWithdrawn {
		return gate.Resolution{}, gate.ErrUnsupported
	}
	_, err := h.Service.Withdraw(ctx, hitl.WithdrawCommand{ContractVersion: hitl.ContractVersion, ItemID: ticket.Handle.ItemID, Caller: hitl.CallerAssertion{ApplicationID: "tradecraft/tool-admission"}})
	if err != nil {
		return gate.Resolution{}, err
	}
	out, _, err := h.project(ctx, ticket)
	return out, err
}

// Model is a scripted provider which proves the second model request receives
// the tool result. Stream context is the admission context supplied by the host.
type Model struct {
	Host   *Host
	Stream func(context.Context, llmtypes.ChatRequest) (<-chan llmtypes.StreamEvent, error)
}

func (m *Model) Capabilities() llmtypes.ProviderCapabilities {
	return llmtypes.ProviderCapabilities{SupportsToolCalling: true, ContextWindowSize: 32000}
}
func (m *Model) Complete(context.Context, llmtypes.ChatRequest) (string, error) {
	return "", sdk.ErrUnsupported
}
func (m *Model) StreamChat(ctx context.Context, r llmtypes.ChatRequest) (<-chan llmtypes.StreamEvent, error) {
	m.Host.ModelCalls++
	if m.Stream != nil {
		return m.Stream(ctx, r)
	}
	var events []llmtypes.StreamEvent
	if len(r.Messages) == 1 {
		events = append(events, llmtypes.StreamEvent{Type: llmtypes.EventToolUse, ToolUse: &llmtypes.ToolUseBlock{ID: "call-1", Name: "read_value", Input: map[string]any{}}})
	} else {
		last := r.Messages[len(r.Messages)-1]
		if len(last.ContentBlocks) != 1 || last.ContentBlocks[0].ToolUseID != "call-1" || last.ContentBlocks[0].Content != "42" {
			return nil, fmt.Errorf("synthetic model: missing paired tool result")
		}
		events = append(events, llmtypes.StreamEvent{Type: llmtypes.EventDelta, Content: "The value is 42."})
	}
	events = append(events, llmtypes.StreamEvent{Type: llmtypes.EventUsage, Usage: &llmtypes.Usage{InputTokens: 10, OutputTokens: 5}}, llmtypes.StreamEvent{Type: llmtypes.EventDone})
	return Stream(events...), nil
}
func Stream(events ...llmtypes.StreamEvent) <-chan llmtypes.StreamEvent {
	ch := make(chan llmtypes.StreamEvent, len(events))
	for _, event := range events {
		ch <- event
	}
	close(ch)
	return ch
}
