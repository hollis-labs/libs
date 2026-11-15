package turn

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/libs/harness/core/adapters/agentsessions"
	"github.com/hollis-labs/libs/mesh/core/gate"
	"github.com/hollis-labs/libs/mesh/core/gate/gatetest"
	"github.com/hollis-labs/libs/mesh/core/hitl"
	"github.com/hollis-labs/libs/mesh/core/hitl/memstore"
)

var codexGateTime = time.Now().UTC()

// Fake routing uses the existing HITL Service and real terminal outcome. It is
// not a production durable store or identity provider.
type codexTestGate struct {
	service      *hitl.Service
	request      gate.Request
	choice       string
	afterRequest func()
	mutate       func(*gate.Resolution)
	cancelled    bool
	wait         bool
}

func (f *codexTestGate) Capabilities(_ context.Context, a gate.AdapterScope) (gate.Capabilities, error) {
	c := gatetest.Capabilities(gatetest.Request(codexGateTime))
	c.Adapter = a
	c.OperationClasses = []string{"command", "mcp_tool_call"}
	return c, nil
}
func (f *codexTestGate) Request(ctx context.Context, r gate.Request) (gate.Ticket, error) {
	f.request = r
	h, err := f.service.Enqueue(ctx, hitl.EnqueueRequest{ContractVersion: hitl.ContractVersion, Kind: "gate", IdempotencyKey: r.IdempotencyKey, ExpiresAt: &r.ExpiresAt, Source: hitl.SourceAssertion{ApplicationID: r.EnforcementPoint, AgentID: string(r.Binding.OperativeID)}})
	if err != nil {
		return gate.Ticket{}, err
	}
	digest, _ := r.Digest()
	ticket := gate.Ticket{AuthorityRef: "existing-owner", Handle: h, RequestDigest: digest, Binding: r.Binding}
	if !f.wait {
		_, err = f.service.Respond(ctx, hitl.RespondCommand{ItemID: h.ItemID, Response: hitl.Response{Kind: "gate", Decision: f.choice}, Participant: hitl.Participant{PrincipalRef: "human-1", Assurance: "fake-verified-channel"}})
	}
	if f.afterRequest != nil {
		f.afterRequest()
	}
	return ticket, err
}
func (f *codexTestGate) Await(ctx context.Context, ticket gate.Ticket, _ gate.WaitOptions) (gate.Resolution, error) {
	if f.wait {
		<-ctx.Done()
		return gate.Resolution{}, ctx.Err()
	}
	v, err := f.service.Get(ctx, hitl.GetCommand{ContractVersion: hitl.ContractVersion, ItemID: ticket.Handle.ItemID, Caller: hitl.CallerAssertion{ApplicationID: f.request.EnforcementPoint}})
	if err != nil {
		return gate.Resolution{}, err
	}
	o := v.Item.TerminalOutcome.(hitl.Resolved)
	var scope gate.Scope
	for _, option := range f.request.Options {
		if option.ID == f.choice {
			scope = option.Scope
		}
	}
	d := &gate.Decision{Ticket: ticket, Resolution: o.Resolution, OptionID: f.choice, Scope: scope, OperationDigest: f.request.OperationDigest, AuthorityRef: "verified-host-authority", ExpiresAt: f.request.ExpiresAt}
	out := gate.Resolution{State: gate.Decided, Ticket: ticket, Decision: d, Outcome: o}
	if f.mutate != nil {
		f.mutate(&out)
	}
	return out, nil
}
func (f *codexTestGate) Cancel(_ context.Context, _ gate.Ticket, _ hitl.CancelCause) (gate.Resolution, error) {
	f.cancelled = true
	return gate.Resolution{}, nil
}

type codexTestAuthority struct{ refuse bool }

func (a *codexTestAuthority) Verify(_ context.Context, _ gate.Request, d gate.Decision) error {
	if a.refuse || d.AuthorityRef != "verified-host-authority" || d.Resolution.Participant.PrincipalRef != "human-1" || d.Resolution.Response.Decision != d.OptionID {
		return gate.ErrAuthority
	}
	return nil
}

func codexGateFixture(t *testing.T, method string) (*CodexGateResponder, *codexTestGate, *CodexGateFacts, agentsessions.JsonRpcServerRequest, *int) {
	t.Helper()
	req := gatetest.Request(codexGateTime)
	req.Adapter = gate.AdapterScope{AdapterID: "codex", Protocol: "app-server-v2", LaunchMode: "on-request"}
	facts := &CodexGateFacts{Request: req, MaxScope: gate.Once, Descriptor: json.RawMessage(gatetest.OperationDescriptor), ThreadID: "thread-1", TurnID: "turn-1", ItemID: "item-1", Command: "printf hello", CWD: "/workspace", MCPServer: "example", MCPTool: "write", MCPArguments: json.RawMessage(`{"number":9007199254740993}`)}
	native := agentsessions.JsonRpcServerRequest{ID: json.RawMessage(`9007199254740993`), Binding: req.Binding, Method: method}
	if method == CodexCommandApprovalMethod {
		native.Params = json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","command":"printf hello","cwd":"/workspace"}`)
	} else {
		native.Params = json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","serverName":"example","mode":"form","message":"Untrusted display","requestedSchema":{"type":"object","properties":{}},"_meta":{"codex_approval_kind":"mcp_tool_call","tool_name":"write","tool_params":{"number":9007199254740993},"persist":["session","always"]}}`)
	}
	f := &codexTestGate{choice: "accept", service: hitl.NewService(memstore.New(), hitl.Options{Clock: func() time.Time { return codexGateTime }})}
	claims := new(int)
	r := &CodexGateResponder{Gate: f, Authority: &codexTestAuthority{}, Now: func() time.Time { return codexGateTime }, Resolve: func(context.Context, agentsessions.JsonRpcServerRequest) (CodexGateFacts, error) { return *facts, nil }, Claim: func(context.Context, gate.Request, gate.Decision) error { *claims++; return nil }}
	return r, f, facts, native, claims
}

func TestCodexGateNativeOnceChoices(t *testing.T) {
	for _, method := range []string{CodexCommandApprovalMethod, CodexElicitationMethod} {
		for _, choice := range []string{"accept", "decline", "cancel"} {
			t.Run(method+"/"+choice, func(t *testing.T) {
				r, f, _, native, claims := codexGateFixture(t, method)
				f.choice = choice
				result, rpcErr := r.Respond(context.Background(), native)
				if rpcErr != nil {
					t.Fatal(rpcErr)
				}
				b, _ := json.Marshal(result)
				if !strings.Contains(string(b), `"`+choice+`"`) {
					t.Fatalf("native choice lost: %s", b)
				}
				if choice == "accept" && *claims != 1 || choice != "accept" && *claims != 0 {
					t.Fatal("incorrect allow claim")
				}
				for _, option := range f.request.Options {
					if option.Scope.Kind != gate.Once || option.ID == "acceptForSession" {
						t.Fatal("once ceiling widened")
					}
				}
				if !f.request.NativeRequestID.Equal(gate.NativeRequestID(native.ID)) {
					t.Fatal("native ID changed")
				}
			})
		}
	}
}

func TestCodexGateRefusesUnsupportedFactsAndChoices(t *testing.T) {
	cases := []struct {
		name, method string
		change       func(*CodexGateFacts, *agentsessions.JsonRpcServerRequest)
	}{
		{"no host scope", CodexCommandApprovalMethod, func(f *CodexGateFacts, _ *agentsessions.JsonRpcServerRequest) { f.MaxScope = "" }},
		{"missing command", CodexCommandApprovalMethod, func(f *CodexGateFacts, _ *agentsessions.JsonRpcServerRequest) { f.Command = "" }},
		{"changed command", CodexCommandApprovalMethod, func(f *CodexGateFacts, _ *agentsessions.JsonRpcServerRequest) { f.Command = "rm changed" }},
		{"old generation", CodexCommandApprovalMethod, func(f *CodexGateFacts, _ *agentsessions.JsonRpcServerRequest) { f.Request.Binding.Generation++ }},
		{"unknown command option", CodexCommandApprovalMethod, func(_ *CodexGateFacts, n *agentsessions.JsonRpcServerRequest) {
			n.Params = json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","availableDecisions":["accept","surprise"]}`)
		}},
		{"empty native menu", CodexCommandApprovalMethod, func(_ *CodexGateFacts, n *agentsessions.JsonRpcServerRequest) {
			n.Params = json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","availableDecisions":[]}`)
		}},
		{"network grouping", CodexCommandApprovalMethod, func(_ *CodexGateFacts, n *agentsessions.JsonRpcServerRequest) {
			n.Params = json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","networkApprovalContext":{"host":"example.test"}}`)
		}},
		{"permission subset", CodexCommandApprovalMethod, func(_ *CodexGateFacts, n *agentsessions.JsonRpcServerRequest) {
			n.Params = json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","additionalPermissions":{}}`)
		}},
		{"display only MCP", CodexElicitationMethod, func(_ *CodexGateFacts, n *agentsessions.JsonRpcServerRequest) {
			n.Params = json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","serverName":"example","message":"Allow the example MCP server to run tool \"write\"?","_meta":{"codex_approval_kind":"mcp_tool_call"}}`)
		}},
		{"changed MCP args", CodexElicitationMethod, func(f *CodexGateFacts, _ *agentsessions.JsonRpcServerRequest) {
			f.MCPArguments = json.RawMessage(`{"number":9007199254740992}`)
		}},
		{"ordinary elicitation", CodexElicitationMethod, func(_ *CodexGateFacts, n *agentsessions.JsonRpcServerRequest) {
			n.Params = json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","mode":"form","requestedSchema":{"type":"object","properties":{}},"_meta":{}}`)
		}},
		{"unknown method", "item/permissions/requestApproval", func(_ *CodexGateFacts, _ *agentsessions.JsonRpcServerRequest) {}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, f, facts, native, claims := codexGateFixture(t, tc.method)
			tc.change(facts, &native)
			result, rpcErr := r.Respond(context.Background(), native)
			if rpcErr == nil || result != nil || f.request.OperationID != "" || *claims != 0 {
				t.Fatal("unsupported request reached gate or claim")
			}
		})
	}
}

func TestCodexGateRechecksLiveBindingAuthorityAndClaim(t *testing.T) {
	for _, fault := range []string{"digest", "generation", "authority", "claim", "selected option", "scope", "ticket", "expiry"} {
		t.Run(fault, func(t *testing.T) {
			r, f, facts, native, claims := codexGateFixture(t, CodexCommandApprovalMethod)
			switch fault {
			case "digest":
				f.afterRequest = func() { facts.Descriptor = json.RawMessage(`{"changed":"target"}`) }
			case "generation":
				f.afterRequest = func() { facts.Request.Binding.Generation++ }
			case "authority":
				r.Authority = &codexTestAuthority{refuse: true}
			case "claim":
				r.Claim = func(context.Context, gate.Request, gate.Decision) error { return errors.New("uncertain claim") }
			case "selected option":
				f.mutate = func(out *gate.Resolution) { out.Decision.OptionID = "acceptForSession" }
			case "scope":
				f.mutate = func(out *gate.Resolution) { out.Decision.Scope.Kind = gate.Session }
			case "ticket":
				f.mutate = func(out *gate.Resolution) { out.Ticket.AuthorityRef = "other-owner" }
			case "expiry":
				f.mutate = func(out *gate.Resolution) { out.Decision.ExpiresAt = codexGateTime.Add(-time.Second) }
			}
			result, rpcErr := r.Respond(context.Background(), native)
			if rpcErr == nil || result != nil || *claims != 0 {
				t.Fatalf("fault permitted native acceptance: %s", fault)
			}
		})
	}
}

func TestCodexGateLifecycleCancellationWithdraws(t *testing.T) {
	r, f, _, native, claims := codexGateFixture(t, CodexCommandApprovalMethod)
	f.wait = true
	ctx, cancel := context.WithCancel(context.Background())
	f.afterRequest = cancel
	result, rpcErr := r.Respond(ctx, native)
	if result != nil || rpcErr == nil || !f.cancelled || *claims != 0 {
		t.Fatal("cancelled native request not withdrawn")
	}
}
