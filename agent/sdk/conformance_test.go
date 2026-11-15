package sdk_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/hollis-labs/libs/agent/sdk"
	"github.com/hollis-labs/libs/agent/sdk/internal/fakehost"
	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
	"github.com/hollis-labs/libs/llm/core/llmtypes"
	"github.com/hollis-labs/libs/mesh/core/agentdef"
	"github.com/hollis-labs/libs/mesh/core/gate"
	"github.com/hollis-labs/libs/mesh/core/gate/gatetest"
	"github.com/hollis-labs/libs/mesh/core/hitl"
	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
	"github.com/hollis-labs/libs/security/policy"
)

func newOperative(t *testing.T, h *fakehost.Host, configure func(*sdk.Ports)) *sdk.Operative {
	t.Helper()
	p := h.Ports()
	if configure != nil {
		configure(&p)
	}
	o, err := sdk.New(fakehost.Definition(), p)
	if err != nil {
		t.Fatal(err)
	}
	return o
}
func payload(t *testing.T, e runtimeevents.Event) sdk.EventPayload {
	t.Helper()
	var p sdk.EventPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestActualLoopGateAndToolPairing(t *testing.T) {
	h := fakehost.New()
	o := newOperative(t, h, nil)
	result, err := o.Run(context.Background(), h.Input())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != sdk.Succeeded || result.Turns != 2 || result.Text != "The value is 42." || h.Effects != 1 || h.ModelCalls != 2 {
		t.Fatalf("unexpected real-loop result: %+v effects=%d models=%d", result, h.Effects, h.ModelCalls)
	}
	if result.Usage.InputTokens != 20 || result.Usage.OutputTokens != 10 {
		t.Fatalf("lost usage: %+v", result.Usage)
	}
	var permission, tool bool
	for _, event := range h.Events {
		if err := event.Validate(); err != nil {
			t.Fatal(err)
		}
		p := payload(t, event)
		if p.Binding != h.Binding || p.RunID != result.RunID || p.Environment != h.Environment.Receipt || p.DefinitionDigest == "" {
			t.Fatalf("lost correlation: %+v", p)
		}
		if event.Kind == runtimeevents.KindAgentPermissionResolved {
			permission = true
			if p.ApprovalID == "" || p.ApprovalState != gate.Decided {
				t.Fatal("lost approval evidence")
			}
		}
		if event.Kind == runtimeevents.KindAgentToolResult {
			tool = true
			if p.Execution != gate.ExecutionSucceeded || p.DecisionID == "" || p.OperationID == "" || p.ToolCallID == "" {
				t.Fatal("lost effect evidence")
			}
		}
	}
	if !permission || !tool || h.Receipts[len(h.Receipts)-1].State != gate.ExecutionSucceeded {
		t.Fatal("missing approval/effect observations")
	}
	if last := payload(t, h.Events[len(h.Events)-1]); last.Status != sdk.Succeeded {
		t.Fatalf("missing terminal evidence: %+v", last)
	}
}

func TestGateRefusalsExecuteNoEffects(t *testing.T) {
	for _, mode := range []string{"deny", "pending", "expired", "cancelled", "unverified"} {
		t.Run(mode, func(t *testing.T) {
			h := fakehost.New()
			h.Mode = mode
			o := newOperative(t, h, nil)
			result, err := o.Run(context.Background(), h.Input())
			if err == nil || h.Effects != 0 || h.ModelCalls != 1 {
				t.Fatalf("gate refusal failed: %v effects=%d calls=%d", err, h.Effects, h.ModelCalls)
			}
			if mode == "pending" {
				if !errors.Is(err, sdk.ErrPending) || result.Status != sdk.Pending || result.PendingTicket == nil {
					t.Fatalf("pending flattened: %+v %v", result, err)
				}
			}
			if last := payload(t, h.Events[len(h.Events)-1]); last.Status != result.Status {
				t.Fatal("missing failure terminal")
			}
		})
	}
}

func TestDefinitionAndConfigurationRefuseBeforeEffects(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*agentdef.Definition, *sdk.Ports)
	}{
		{"malformed", func(d *agentdef.Definition, p *sdk.Ports) { d.SchemaVersion = "1" }},
		{"mandatory capability", func(d *agentdef.Definition, p *sdk.Ports) { d.Requirements.Requires = []string{"unsupported-host"} }},
		{"mandatory extension", func(d *agentdef.Definition, p *sdk.Ports) {
			d.Extensions = map[string]agentdef.Extension{"example.org/runtime": {Version: "1", Area: "behavior", Mandatory: true, Data: map[string]any{}}}
		}},
		{"hooks", func(d *agentdef.Definition, p *sdk.Ports) { d.Behavior.Hooks = []string{"before-call"} }},
		{"missing tool", func(d *agentdef.Definition, p *sdk.Ports) { d.Requirements.Tools = []string{"absent"} }},
		{"unpinned implementation", func(d *agentdef.Definition, p *sdk.Ports) { p.Tools[0].Versions = nil }},
		{"missing authority", func(d *agentdef.Definition, p *sdk.Ports) {
			d.Requirements.Requires = []string{sdk.CapabilityApproval}
			p.Capabilities = append(p.Capabilities, sdk.CapabilityApproval)
			p.Authority = nil
		}},
		{"missing environment", func(d *agentdef.Definition, p *sdk.Ports) { p.Environment = nil }},
		{"negative limit", func(d *agentdef.Definition, p *sdk.Ports) { p.Limits.MaxTurns = -1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := fakehost.New()
			d := fakehost.Definition()
			p := h.Ports()
			test.change(&d, &p)
			if _, err := sdk.New(d, p); err == nil {
				t.Fatal("invalid definition accepted")
			}
			if h.ModelCalls != 0 || h.Effects != 0 {
				t.Fatal("constructor started effect")
			}
		})
	}
}

func TestPreflightRejectsInvalidReceiptLeaseCapabilitiesAndPolicy(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*sdk.Input, *fakehost.Host)
	}{
		{"wrong receipt", func(in *sdk.Input, h *fakehost.Host) { in.Environment.Receipt.ID = "unissued" }},
		{"expired", func(in *sdk.Input, h *fakehost.Host) { in.Environment.ExpiresAt = h.Now() }},
		{"missing capability", func(in *sdk.Input, h *fakehost.Host) { in.Environment.Capabilities = nil }},
		{"stale lease", func(in *sdk.Input, h *fakehost.Host) { in.Environment.Lease = gatetest.Reference("stale-lease") }},
		{"stale generation", func(in *sdk.Input, h *fakehost.Host) { in.Binding.Generation++ }},
		{"cross owner", func(in *sdk.Input, h *fakehost.Host) { in.Binding.OwnerID = "other-owner" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := fakehost.New()
			o := newOperative(t, h, nil)
			in := h.Input()
			test.change(&in, h)
			if _, err := o.Run(context.Background(), in); err == nil {
				t.Fatal("invalid authority accepted")
			}
			if h.ModelCalls != 0 || h.Effects != 0 {
				t.Fatal("effect before preflight")
			}
		})
	}
	h := fakehost.New()
	d := fakehost.Definition()
	d.HarnessProfile.Permissions.Profile = "unbound"
	o, err := sdk.New(d, h.Ports())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = o.Run(context.Background(), h.Input()); err == nil || h.ModelCalls != 0 {
		t.Fatal("unbound permission profile accepted")
	}
}

func TestCurrentBindingFencedAtEveryEffect(t *testing.T) {
	for _, kind := range []string{"first-model", "tool", "second-model"} {
		t.Run(kind, func(t *testing.T) {
			h := fakehost.New()
			in := h.Input()
			h.BeforeAdmission = func(a sdk.Admission) {
				if (kind == "first-model" && a.Kind == "model") || (kind == "tool" && a.Kind == "tool") || (kind == "second-model" && a.Kind == "model" && h.ModelCalls == 1) {
					h.Binding.Generation++
					h.Binding.AttemptID = "successor"
				}
			}
			o := newOperative(t, h, nil)
			if _, err := o.Run(context.Background(), in); err == nil {
				t.Fatal("stale admission succeeded")
			}
			switch kind {
			case "first-model":
				if h.ModelCalls != 0 || h.Effects != 0 {
					t.Fatal("stale model started")
				}
			case "tool":
				if h.ModelCalls != 1 || h.Effects != 0 {
					t.Fatal("stale tool started")
				}
			case "second-model":
				if h.ModelCalls != 1 || h.Effects != 1 {
					t.Fatal("stale follow-up model started")
				}
			}
		})
	}
}

func TestChangedDescriptorRefusedAtAtomicClaim(t *testing.T) {
	h := fakehost.New()
	o := newOperative(t, h, func(p *sdk.Ports) {
		original := p.Admission
		p.Admission = admissionOverride{AdmissionPort: original, admit: func(ctx context.Context, a sdk.Admission, start func(context.Context) error) error {
			if a.Operation != nil {
				a.Operation.Arguments = json.RawMessage(`{"changed":true}`)
			}
			return original.Admit(ctx, a, start)
		}}
	})
	if _, err := o.Run(context.Background(), h.Input()); err == nil || h.Effects != 0 {
		t.Fatalf("changed digest admitted: %v", err)
	}
}

type admissionOverride struct {
	sdk.AdmissionPort
	admit   func(context.Context, sdk.Admission, func(context.Context) error) error
	reserve func(context.Context, sdk.RunReservation) error
}

func (f admissionOverride) Admit(ctx context.Context, a sdk.Admission, start func(context.Context) error) error {
	if f.admit != nil {
		return f.admit(ctx, a, start)
	}
	return f.AdmissionPort.Admit(ctx, a, start)
}

func (f admissionOverride) ReserveRun(ctx context.Context, r sdk.RunReservation) error {
	if f.reserve != nil {
		return f.reserve(ctx, r)
	}
	return f.AdmissionPort.ReserveRun(ctx, r)
}

type resolverFunc func(context.Context, runtimecontract.Binding, agentdef.Ref) ([]byte, error)

func (f resolverFunc) Resolve(ctx context.Context, b runtimecontract.Binding, r agentdef.Ref) ([]byte, error) {
	return f(ctx, b, r)
}
func TestPinnedInstructionsAndConstructorSnapshot(t *testing.T) {
	h := fakehost.New()
	d := fakehost.Definition()
	raw := []byte("Pinned instruction payload.")
	d.Behavior.Instructions = []agentdef.Ref{{URI: "synthetic://instructions", Digest: agentdef.ArtifactDigest(raw)}}
	p := h.Ports()
	p.Resources = resolverFunc(func(_ context.Context, b runtimecontract.Binding, r agentdef.Ref) ([]byte, error) {
		if b != h.Binding {
			t.Fatal("unscoped resource read")
		}
		return raw, nil
	})
	h.Model.Stream = func(ctx context.Context, req llmtypes.ChatRequest) (<-chan llmtypes.StreamEvent, error) {
		if !strings.Contains(req.EffectiveSystemPrompt(), string(raw)) || strings.Contains(req.EffectiveSystemPrompt(), "mutated") {
			t.Fatal("definition snapshot or resolved instruction lost")
		}
		return fakehost.Stream(llmtypes.StreamEvent{Type: llmtypes.EventDelta, Content: "done"}, llmtypes.StreamEvent{Type: llmtypes.EventDone}), nil
	}
	o, err := sdk.New(d, p)
	if err != nil {
		t.Fatal(err)
	}
	d.Behavior.Instructions[0].Digest = "mutated"
	d.Body = "mutated"
	if _, err = o.Run(context.Background(), h.Input()); err != nil {
		t.Fatal(err)
	}
	h = fakehost.New()
	d = fakehost.Definition()
	d.Behavior.Instructions = []agentdef.Ref{{URI: "synthetic://bad", Digest: agentdef.ArtifactDigest(raw)}}
	p = h.Ports()
	p.Resources = resolverFunc(func(context.Context, runtimecontract.Binding, agentdef.Ref) ([]byte, error) {
		return []byte("changed bytes"), nil
	})
	o, err = sdk.New(d, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = o.Run(context.Background(), h.Input()); err == nil || h.ModelCalls != 0 {
		t.Fatal("bad pin reached model")
	}
}

type memoryFunc func(context.Context, sdk.MemoryQuery) ([]sdk.MemoryItem, error)

func (f memoryFunc) Read(ctx context.Context, q sdk.MemoryQuery) ([]sdk.MemoryItem, error) {
	return f(ctx, q)
}
func TestBoundedMemoryAndScopeIsolation(t *testing.T) {
	for _, mode := range []string{"valid", "owner", "conversation", "too-many", "too-large"} {
		t.Run(mode, func(t *testing.T) {
			h := fakehost.New()
			o := newOperative(t, h, func(p *sdk.Ports) {
				p.Memory = memoryFunc(func(ctx context.Context, q sdk.MemoryQuery) ([]sdk.MemoryItem, error) {
					if q.Binding != h.Binding || q.MaxItems <= 0 || q.MaxBytes <= 0 {
						t.Fatal("unbounded or unscoped query")
					}
					item := sdk.MemoryItem{Binding: q.Binding, Reference: gatetest.Reference("memory-1"), Content: "relevant synthetic memory"}
					switch mode {
					case "owner":
						item.Reference.OwnerID = "other-owner"
					case "conversation":
						item.Binding.ConversationID = "another"
					case "too-large":
						item.Content = strings.Repeat("x", q.MaxBytes+1)
					case "too-many":
						return make([]sdk.MemoryItem, q.MaxItems+1), nil
					}
					return []sdk.MemoryItem{item}, nil
				})
			})
			_, err := o.Run(context.Background(), h.Input())
			if mode == "valid" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || h.ModelCalls != 0 {
				t.Fatalf("memory boundary failed: %v models=%d", err, h.ModelCalls)
			}
		})
	}
}

func TestCancellationAndProviderFailuresHaveTerminalEvidence(t *testing.T) {
	for _, mode := range []string{"cancelled", "deadline", "start", "stream", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			h := fakehost.New()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			h.Model.Stream = func(context.Context, llmtypes.ChatRequest) (<-chan llmtypes.StreamEvent, error) {
				switch mode {
				case "cancelled":
					cancel()
					return make(chan llmtypes.StreamEvent), nil
				case "deadline":
					return nil, context.DeadlineExceeded
				case "start":
					return nil, errors.New("synthetic start failure")
				case "stream":
					return fakehost.Stream(llmtypes.StreamEvent{Type: llmtypes.EventError, Error: "synthetic failure"}), nil
				default:
					return fakehost.Stream(llmtypes.StreamEvent{Type: llmtypes.EventDelta, Content: "partial"}), nil
				}
			}
			o := newOperative(t, h, nil)
			result, err := o.Run(ctx, h.Input())
			if err == nil || h.Effects != 0 {
				t.Fatal("provider failure reached tool/success")
			}
			if mode == "cancelled" || mode == "deadline" {
				if result.Status != sdk.Cancelled {
					t.Fatalf("lost cancellation: %+v", result)
				}
			} else if result.Status != sdk.Failed {
				t.Fatal("lost failure status")
			}
			last := h.Events[len(h.Events)-1]
			if last.Kind != "sdk.run.failed" || payload(t, last).Status != result.Status {
				t.Fatal("lost terminal event on cancelled context")
			}
		})
	}
}

func TestUnknownToolEffectIsNeverRetried(t *testing.T) {
	h := fakehost.New()
	o := newOperative(t, h, func(p *sdk.Ports) {
		p.Tools[0].Execute = func(context.Context, map[string]any) (sdk.ToolResult, error) {
			h.Effects++
			return sdk.ToolResult{}, errors.New("synthetic disconnect after effect")
		}
	})
	_, err := o.Run(context.Background(), h.Input())
	if err == nil || h.Effects != 1 {
		t.Fatal("unknown effect missing")
	}
	if h.Receipts[len(h.Receipts)-1].State != gate.ExecutionUnknown {
		t.Fatal("uncertainty flattened")
	}
	_, err = o.Run(context.Background(), h.Input())
	if err == nil || h.Effects != 1 {
		t.Fatalf("unknown effect repeated: %v effects=%d", err, h.Effects)
	}
}

func TestDuplicateToolIDsAndUnadvertisedToolsRefuseBeforeEffects(t *testing.T) {
	for _, mode := range []string{"duplicate", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			h := fakehost.New()
			h.Model.Stream = func(context.Context, llmtypes.ChatRequest) (<-chan llmtypes.StreamEvent, error) {
				call := llmtypes.ToolUseBlock{ID: "same", Name: "read_value", Input: map[string]any{}}
				if mode == "unknown" {
					call.Name = "not-granted"
				}
				return fakehost.Stream(llmtypes.StreamEvent{Type: llmtypes.EventToolUse, ToolUse: &call}, llmtypes.StreamEvent{Type: llmtypes.EventToolUse, ToolUse: &call}, llmtypes.StreamEvent{Type: llmtypes.EventDone}), nil
			}
			o := newOperative(t, h, nil)
			if _, err := o.Run(context.Background(), h.Input()); err == nil || h.Effects != 0 {
				t.Fatal("invalid turn ran effects")
			}
		})
	}
}

func TestUnsupportedObligationsRefuse(t *testing.T) {
	h := fakehost.New()
	h.PolicyResult = &sdk.PolicyResult{Decision: policy.Decision{Effect: policy.Allow, Obligations: []policy.Obligation{{Kind: policy.DryRunOnly, PolicyID: "dry-run"}}}, Reference: gatetest.Reference("policy-1"), Obligations: []gate.ObligationRef{{Kind: "dry_run_only", Ref: gatetest.Reference("dry-run")}}}
	o := newOperative(t, h, nil)
	if _, err := o.Run(context.Background(), h.Input()); !errors.Is(err, sdk.ErrUnsupported) || h.Effects != 0 {
		t.Fatalf("unsupported obligation allowed: %v", err)
	}
}

func TestLimitsAndObservationFailureStopEffects(t *testing.T) {
	t.Run("turn budget", func(t *testing.T) {
		h := fakehost.New()
		o := newOperative(t, h, func(p *sdk.Ports) { p.Limits.MaxTurns = 1 })
		result, err := o.Run(context.Background(), h.Input())
		if !errors.Is(err, sdk.ErrLimit) || result.Turns != 1 || h.ModelCalls != 1 || h.Effects != 1 {
			t.Fatalf("turn bound failed: %+v %v", result, err)
		}
	})
	t.Run("context budget", func(t *testing.T) {
		h := fakehost.New()
		o := newOperative(t, h, nil)
		in := h.Input()
		in.Text = strings.Repeat("x", 200000)
		if _, err := o.Run(context.Background(), in); !errors.Is(err, sdk.ErrContextBudget) || h.ModelCalls != 0 {
			t.Fatalf("context bound failed: %v", err)
		}
	})
	t.Run("event failure", func(t *testing.T) {
		h := fakehost.New()
		sentinel := errors.New("event sink unavailable")
		o := newOperative(t, h, func(p *sdk.Ports) {
			p.Events = runtimeevents.SinkFunc(func(context.Context, runtimeevents.Event) error { return sentinel })
		})
		if _, err := o.Run(context.Background(), h.Input()); !errors.Is(err, sentinel) || h.ModelCalls != 0 {
			t.Fatal("event sink failure hidden")
		}
	})
}

func TestRouterExplicitSelectionAndCapabilities(t *testing.T) {
	h := fakehost.New()
	router, err := sdk.NewRouter("fake", sdk.Route{Name: "fake", Model: "fake-model", Provider: h.Model})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sdk.NewRouter("absent", sdk.Route{Name: "fake", Model: "fake-model", Provider: h.Model}); err == nil {
		t.Fatal("missing default accepted")
	}
	if _, err = sdk.NewRouter("fake", sdk.Route{Name: "fake", Model: "fake-model", Provider: h.Model}, sdk.Route{Name: "fake", Model: "other", Provider: h.Model}); err == nil {
		t.Fatal("duplicate route accepted")
	}
	o := newOperative(t, h, func(p *sdk.Ports) { p.Router = router })
	in := h.Input()
	in.Route = "absent"
	if _, err = o.Run(context.Background(), in); !errors.Is(err, sdk.ErrUnsupported) || h.ModelCalls != 0 {
		t.Fatal("implicit route fallback")
	}
}

// A forged projection cannot self-authorize, even when its request validates.
func TestNativeGateAdapterPreservesExactScopeAndRejectsWidening(t *testing.T) {
	h := fakehost.New()
	r := gatetest.Request(h.Now())
	r.Adapter = sdk.NativeAdapterScope()
	a := sdk.GateAdapter{Port: h}
	r.Options[0].Scope.Kind = gate.Session
	if _, err := a.Resolve(context.Background(), r, h.Now()); !errors.Is(err, sdk.ErrUnsupported) {
		t.Fatalf("scope widened: %v", err)
	}
	r = gatetest.Request(h.Now())
	r.Adapter = sdk.NativeAdapterScope()
	r.EnforcementPoint = "tradecraft/tool-admission"
	r.NativeRequestID = gate.NativeRequestID(`9007199254740993`)
	// The fake responder selects the SDK's exact native allow option.
	r.Options[0].ID = "allow-once"
	r.Options[1].ID = "deny"
	out, err := a.Resolve(context.Background(), r, h.Now())
	if err != nil {
		t.Fatal(err)
	}
	if out.Decision.OptionID != "allow-once" || !reflect.DeepEqual(out.Decision.Scope, r.Options[0].Scope) {
		t.Fatal("native option flattened")
	}
}

func TestPolicyAllowAskAndDenyRemainDistinct(t *testing.T) {
	for _, effect := range []policy.Effect{policy.Allow, policy.Ask, policy.Deny} {
		t.Run(string(effect), func(t *testing.T) {
			h := fakehost.New()
			h.Mode = "pending"
			h.PolicyResult = &sdk.PolicyResult{Decision: policy.Decision{Effect: effect}, Reference: gatetest.Reference("policy-1")}
			o := newOperative(t, h, nil)
			result, err := o.Run(context.Background(), h.Input())
			switch effect {
			case policy.Allow:
				if err != nil || result.Status != sdk.Succeeded || h.Effects != 1 || h.GateRequests != 0 {
					t.Fatalf("policy allow prompted/refused: %+v %v requests=%d effects=%d", result, err, h.GateRequests, h.Effects)
				}
			case policy.Ask:
				if !errors.Is(err, sdk.ErrPending) || h.GateRequests != 1 || h.Effects != 0 {
					t.Fatalf("ask flattened: %v", err)
				}
			case policy.Deny:
				if !errors.Is(err, gate.ErrDenied) || h.GateRequests != 0 || h.Effects != 0 {
					t.Fatalf("deny flattened: %v", err)
				}
			}
		})
	}
}

type gateWrapper struct {
	gate.GatePort
	await func(context.Context, gate.Ticket, gate.WaitOptions) (gate.Resolution, error)
	caps  func(context.Context, gate.AdapterScope) (gate.Capabilities, error)
}

func (g gateWrapper) Await(ctx context.Context, t gate.Ticket, w gate.WaitOptions) (gate.Resolution, error) {
	if g.await != nil {
		return g.await(ctx, t, w)
	}
	return g.GatePort.Await(ctx, t, w)
}
func (g gateWrapper) Capabilities(ctx context.Context, a gate.AdapterScope) (gate.Capabilities, error) {
	if g.caps != nil {
		return g.caps(ctx, a)
	}
	return g.GatePort.Capabilities(ctx, a)
}

func TestAwaitCancellationPreservesTicketWithoutCancellingInteraction(t *testing.T) {
	h := fakehost.New()
	h.Mode = "pending"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o := newOperative(t, h, func(p *sdk.Ports) {
		p.Gate.Port = gateWrapper{GatePort: h, await: func(context.Context, gate.Ticket, gate.WaitOptions) (gate.Resolution, error) {
			cancel()
			return gate.Resolution{}, context.Canceled
		}}
	})
	result, err := o.Run(ctx, h.Input())
	if !errors.Is(err, context.Canceled) || result.PendingTicket == nil || h.Effects != 0 {
		t.Fatalf("wait cancellation lost ticket: %+v %v", result, err)
	}
	resolution, err := h.Await(context.Background(), *result.PendingTicket, gate.WaitOptions{})
	if err != nil || resolution.State != gate.Pending {
		t.Fatalf("wait implicitly cancelled interaction: %+v %v", resolution, err)
	}
	resolution, err = h.Cancel(context.Background(), *result.PendingTicket, hitl.CauseCallerWithdrawn)
	if err != nil || resolution.State != gate.Cancelled {
		t.Fatal("explicit cancellation failed")
	}
}

func TestUnsupportedGateModeFailsBeforeModel(t *testing.T) {
	h := fakehost.New()
	h.Environment.Capabilities = append(h.Environment.Capabilities, sdk.CapabilityApproval)
	p := h.Ports()
	p.Capabilities = append(p.Capabilities, sdk.CapabilityApproval)
	p.Gate.Port = gateWrapper{GatePort: h, caps: func(ctx context.Context, a gate.AdapterScope) (gate.Capabilities, error) {
		caps, _ := h.Capabilities(ctx, a)
		caps.ScopeKinds = []gate.ScopeKind{gate.Session}
		return caps, nil
	}}
	d := fakehost.Definition()
	d.Requirements.Requires = []string{sdk.CapabilityApproval}
	o, err := sdk.New(d, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Run(context.Background(), h.Input()); !errors.Is(err, sdk.ErrUnsupported) || h.ModelCalls != 0 {
		t.Fatalf("unsupported mandatory gate degraded: %v", err)
	}
}

func TestConcurrentRunRefusedWithoutSecondModel(t *testing.T) {
	h := fakehost.New()
	started := make(chan struct{})
	release := make(chan struct{})
	h.Model.Stream = func(context.Context, llmtypes.ChatRequest) (<-chan llmtypes.StreamEvent, error) {
		close(started)
		<-release
		return fakehost.Stream(llmtypes.StreamEvent{Type: llmtypes.EventDone}), nil
	}
	o := newOperative(t, h, nil)
	done := make(chan error, 1)
	go func() { _, err := o.Run(context.Background(), h.Input()); done <- err }()
	<-started
	if _, err := o.Run(context.Background(), h.Input()); !errors.Is(err, sdk.ErrBusy) {
		t.Fatalf("concurrent input accepted: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type pdpFunc func(context.Context, policy.Request) (policy.Decision, error)

func (f pdpFunc) Evaluate(ctx context.Context, r policy.Request) (policy.Decision, error) {
	return f(ctx, r)
}
func TestSharedPDPAdapterAndEvaluationErrors(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "allow"
		if fail {
			name = "error"
		}
		t.Run(name, func(t *testing.T) {
			h := fakehost.New()
			h.PolicyResult = &sdk.PolicyResult{Decision: policy.Decision{Effect: policy.Allow}, Reference: gatetest.Reference("policy-1")}
			calls := 0
			p := h.Ports()
			p.Policy = sdk.PDPAdapter{PDP: pdpFunc(func(ctx context.Context, r policy.Request) (policy.Decision, error) {
				calls++
				if r.Action != "tool.execute" || r.Principal.URN != "urn:synthetic:verified" {
					t.Fatal("lost host projection")
				}
				if fail {
					return policy.Decision{Effect: policy.Allow}, errors.New("PDP unavailable")
				}
				return policy.Decision{Effect: policy.Allow}, nil
			}), Check: h.CheckDefinition, Project: func(ctx context.Context, op sdk.Operation) (policy.Request, sdk.PolicyResult, error) {
				return policy.Request{Principal: policy.Principal{URN: "urn:synthetic:verified", Kind: "agent"}, Action: "tool.execute", Resource: policy.Resource{Type: "tool", ID: op.Tool}, Scope: "synthetic"}, *h.PolicyResult, nil
			}}
			o, err := sdk.New(fakehost.Definition(), p)
			if err != nil {
				t.Fatal(err)
			}
			_, err = o.Run(context.Background(), h.Input())
			if calls != 1 || h.GateRequests != 0 {
				t.Fatal("shared evaluator not called once or unnecessary approval")
			}
			if fail {
				if err == nil || h.Effects != 0 {
					t.Fatal("PDP error authorized")
				}
			} else if err != nil || h.Effects != 1 {
				t.Fatalf("PDP allow failed: %v", err)
			}
		})
	}
}

func TestToolPanicRemainsUnknownAndTerminal(t *testing.T) {
	h := fakehost.New()
	o := newOperative(t, h, func(p *sdk.Ports) {
		p.Tools[0].Execute = func(context.Context, map[string]any) (sdk.ToolResult, error) {
			h.Effects++
			panic("synthetic sensitive value")
		}
	})
	result, err := o.Run(context.Background(), h.Input())
	if !errors.Is(err, sdk.ErrEffectPanic) || result.Status != sdk.Failed || h.Effects != 1 {
		t.Fatalf("panic lost: %+v %v", result, err)
	}
	if h.Receipts[len(h.Receipts)-1].State != gate.ExecutionUnknown {
		t.Fatal("panic after effect became known failure")
	}
	if strings.Contains(err.Error(), "sensitive") {
		t.Fatal("panic payload leaked")
	}
}

func TestTruncatedToolTurnNeverExecutes(t *testing.T) {
	h := fakehost.New()
	h.Model.Stream = func(context.Context, llmtypes.ChatRequest) (<-chan llmtypes.StreamEvent, error) {
		return fakehost.Stream(llmtypes.StreamEvent{Type: llmtypes.EventToolUse, ToolUse: &llmtypes.ToolUseBlock{ID: "call-1", Name: "read_value", Input: map[string]any{}}}, llmtypes.StreamEvent{Type: llmtypes.EventUsage, Usage: &llmtypes.Usage{StopReason: llmtypes.StopReasonMaxTokens}}, llmtypes.StreamEvent{Type: llmtypes.EventDone}), nil
	}
	o := newOperative(t, h, nil)
	if _, err := o.Run(context.Background(), h.Input()); !errors.Is(err, sdk.ErrLimit) || h.Effects != 0 {
		t.Fatalf("truncated model authorized tool: %v", err)
	}
}

func TestPolicyAllowNeedsNoApprovalService(t *testing.T) {
	for _, effect := range []policy.Effect{policy.Allow, policy.Ask} {
		t.Run(string(effect), func(t *testing.T) {
			h := fakehost.New()
			h.PolicyResult = &sdk.PolicyResult{Decision: policy.Decision{Effect: effect}, Reference: gatetest.Reference("policy-1")}
			o := newOperative(t, h, func(p *sdk.Ports) { p.Gate = sdk.GateAdapter{}; p.Authority = nil })
			_, err := o.Run(context.Background(), h.Input())
			if effect == policy.Allow {
				if err != nil || h.Effects != 1 || h.GateRequests != 0 {
					t.Fatalf("allow required approval service: %v", err)
				}
			} else if !errors.Is(err, sdk.ErrUnsupported) || h.Effects != 0 || h.GateRequests != 0 {
				t.Fatalf("Ask without approval ports authorized: %v", err)
			}
		})
	}
}

func freshCallModel(h *fakehost.Host) {
	h.Model.Stream = func(context.Context, llmtypes.ChatRequest) (<-chan llmtypes.StreamEvent, error) {
		call := llmtypes.ToolUseBlock{ID: fmt.Sprintf("%s/provider-call-%d", h.Binding.AttemptID, h.ModelCalls), Name: "read_value", Input: map[string]any{}}
		return fakehost.Stream(llmtypes.StreamEvent{Type: llmtypes.EventToolUse, ToolUse: &call}, llmtypes.StreamEvent{Type: llmtypes.EventDone}), nil
	}
}

func TestRunReservationBlocksUncertainReplayWithFreshProviderCallID(t *testing.T) {
	for _, gated := range []bool{false, true} {
		name := "policy-allow"
		if gated {
			name = "approval"
		}
		t.Run(name, func(t *testing.T) {
			h := fakehost.New()
			freshCallModel(h)
			if !gated {
				h.PolicyResult = &sdk.PolicyResult{Decision: policy.Decision{Effect: policy.Allow}, Reference: gatetest.Reference("policy-1")}
			}
			configure := func(p *sdk.Ports) {
				p.Tools[0].Execute = func(context.Context, map[string]any) (sdk.ToolResult, error) {
					h.Effects++
					return sdk.ToolResult{}, errors.New("disconnect after effect")
				}
			}
			o := newOperative(t, h, configure)
			if _, err := o.Run(context.Background(), h.Input()); err == nil || h.Effects != 1 || h.ModelCalls != 1 {
				t.Fatalf("uncertain first run not exercised: effects=%d models=%d err=%v", h.Effects, h.ModelCalls, err)
			}
			// A new SDK instance must consult the same host ledger before a second
			// provider can invent a new call ID. A changed prompt is not a fresh run ID.
			o = newOperative(t, h, configure)
			in := h.Input()
			in.Text = "Changed prompt for the same logical run ID"
			if _, err := o.Run(context.Background(), in); !errors.Is(err, sdk.ErrRunUsed) || h.Effects != 1 || h.ModelCalls != 1 {
				t.Fatalf("uncertain run replayed: effects=%d models=%d err=%v", h.Effects, h.ModelCalls, err)
			}
			// Explicitly new logical input remains possible with identical tool args.
			in = h.Input()
			in.ID = "run-2"
			if _, err := o.Run(context.Background(), in); err == nil || h.Effects != 2 || h.ModelCalls != 2 {
				t.Fatalf("distinct run ID was argument-deduplicated: effects=%d models=%d err=%v", h.Effects, h.ModelCalls, err)
			}
		})
	}
}

func TestRunReservationSurvivesReplacementHostAndAttempt(t *testing.T) {
	h := fakehost.New()
	freshCallModel(h)
	o := newOperative(t, h, func(p *sdk.Ports) {
		p.Tools[0].Execute = func(context.Context, map[string]any) (sdk.ToolResult, error) {
			h.Effects++
			return sdk.ToolResult{}, errors.New("uncertain effect")
		}
	})
	if _, err := o.Run(context.Background(), h.Input()); err == nil || h.Effects != 1 {
		t.Fatal("uncertain first run missing")
	}
	replacement := fakehost.New()
	replacement.Runs = h.Runs
	replacement.Binding.Generation++
	replacement.Binding.AttemptID = "replacement-attempt"
	freshCallModel(replacement)
	o = newOperative(t, replacement, nil)
	if _, err := o.Run(context.Background(), replacement.Input()); !errors.Is(err, sdk.ErrRunUsed) || replacement.ModelCalls != 0 || replacement.Effects != 0 {
		t.Fatalf("replacement replayed retained logical run: %v", err)
	}
}

func TestReservationErrorAndAmbiguousCommitStartNoEffects(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		name := "refused"
		if ambiguous {
			name = "committed-response-lost"
		}
		t.Run(name, func(t *testing.T) {
			h := fakehost.New()
			memoryCalls, reserveCalls := 0, 0
			unavailable := errors.New("reservation unavailable or uncertain")
			memory := memoryFunc(func(context.Context, sdk.MemoryQuery) ([]sdk.MemoryItem, error) { memoryCalls++; return nil, nil })
			o := newOperative(t, h, func(p *sdk.Ports) {
				original := p.Admission
				p.Memory = memory
				p.Admission = admissionOverride{AdmissionPort: original, reserve: func(ctx context.Context, r sdk.RunReservation) error {
					reserveCalls++
					if ambiguous {
						if err := original.ReserveRun(ctx, r); err != nil {
							return err
						}
					}
					return unavailable
				}}
			})
			if _, err := o.Run(context.Background(), h.Input()); !errors.Is(err, unavailable) || reserveCalls != 1 || memoryCalls != 0 || h.ModelCalls != 0 || h.Effects != 0 {
				t.Fatalf("reservation error authorized/retried: %v reserves=%d memory=%d models=%d effects=%d", err, reserveCalls, memoryCalls, h.ModelCalls, h.Effects)
			}
			if ambiguous {
				o = newOperative(t, h, func(p *sdk.Ports) { p.Memory = memory })
				if _, err := o.Run(context.Background(), h.Input()); !errors.Is(err, sdk.ErrRunUsed) || memoryCalls != 0 || h.ModelCalls != 0 || h.Effects != 0 {
					t.Fatalf("ambiguous reservation retried fresh work: %v", err)
				}
			}
		})
	}
}

func TestHostRunReservationHasOneWinnerAcrossSDKInstances(t *testing.T) {
	first, second := fakehost.New(), fakehost.New()
	second.Runs = first.Runs
	noTools := func(ctx context.Context, r llmtypes.ChatRequest) (<-chan llmtypes.StreamEvent, error) {
		return fakehost.Stream(llmtypes.StreamEvent{Type: llmtypes.EventDone}), nil
	}
	first.Model.Stream = noTools
	second.Model.Stream = noTools
	a, b := newOperative(t, first, nil), newOperative(t, second, nil)
	ready := make(chan struct{})
	results := make(chan error, 2)
	for _, entry := range []struct {
		o *sdk.Operative
		h *fakehost.Host
	}{{a, first}, {b, second}} {
		go func(o *sdk.Operative, h *fakehost.Host) {
			<-ready
			_, err := o.Run(context.Background(), h.Input())
			results <- err
		}(entry.o, entry.h)
	}
	close(ready)
	succeeded, refused := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			succeeded++
		} else if errors.Is(err, sdk.ErrRunUsed) {
			refused++
		} else {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || refused != 1 || first.ModelCalls+second.ModelCalls != 1 {
		t.Fatalf("run reservation lacked atomic winner: succeeded=%d refused=%d models=%d", succeeded, refused, first.ModelCalls+second.ModelCalls)
	}
}
