package turn

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/hollis-labs/libs/harness/core/adapters/agentsessions"
	"github.com/hollis-labs/libs/mesh/core/gate"
	"github.com/hollis-labs/libs/mesh/core/hitl"
	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
)

// CodexGateFacts comes from a trusted host's current item/operation registry,
// never a display message. Request supplies host identity, environment, resource
// versions, policy, expiry and idempotency key. The adapter replaces its native
// ID, operation class/digest and options. Descriptor must be a versioned canonical
// JSON operation descriptor including targets, arguments/plan/resource versions,
// environment and effects. It is retained in scoped adapter custody.
type CodexGateFacts struct {
	Request gate.Request
	// MaxScope is an explicit host policy ceiling. Only Once is implemented.
	MaxScope                 gate.ScopeKind
	Descriptor               json.RawMessage
	ThreadID, TurnID, ItemID string
	Command, CWD             string
	MCPServer, MCPTool       string
	MCPArguments             json.RawMessage
}

// CodexGateResponder routes a bounded subset of structured Codex approvals to
// GatePort. It never changes launch posture. Resolve must return current trusted
// facts both before requesting and immediately before delivery. Claim atomically
// consumes the exact once grant against current host binding/lease; failure must
// refuse, and uncertain claims must be reconciled rather than retried.
//
// This adapter supports once choices only. The explicit host ceiling filters
// known remembered options. Unsupported methods/options, managed network prompts
// and missing exact operation facts fail closed.
// Authority must resolve the actual authoritative HITL decision, not trust fields
// in the supplied projection. No built-in persistence or identity verifier exists.
type CodexGateResponder struct {
	Gate      gate.GatePort
	Authority gate.AuthorityVerifier
	Resolve   func(context.Context, agentsessions.JsonRpcServerRequest) (CodexGateFacts, error)
	Claim     func(context.Context, gate.Request, gate.Decision) error
	Now       func() time.Time
}

func (r CodexGateResponder) clock() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Respond fits StartOptions.JsonRpcRequestResponder. Return means a proposed
// transport response, not acknowledgement or execution success. Lifecycle clearing
// cancels ctx and invalidates the runtime's pending reply before it is written.
func (r CodexGateResponder) Respond(ctx context.Context, native agentsessions.JsonRpcServerRequest) (any, *agentsessions.JsonRpcError) {
	if r.Gate == nil || r.Authority == nil || r.Resolve == nil || r.Claim == nil {
		return gateRPCError(gate.ErrUnsupported)
	}
	req, err := r.request(ctx, native)
	if err != nil {
		return gateRPCError(err)
	}
	caps, err := r.Gate.Capabilities(ctx, req.Adapter)
	if err != nil {
		return gateRPCError(err)
	}
	if err = caps.ValidateRequest(req, r.clock()); err != nil {
		return gateRPCError(err)
	}
	ticket, err := r.Gate.Request(ctx, req)
	if err != nil {
		return gateRPCError(err)
	}
	digest, _ := req.Digest()
	if ticket.Validate() != nil || ticket.Binding != req.Binding || ticket.RequestDigest != digest {
		return gateRPCError(gate.ErrBinding)
	}
	defer func() {
		if ctx.Err() != nil {
			// Native request cancellation is explicit withdrawal, unlike a single
			// Await timeout. Bound cleanup independently of the dead transport ctx.
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _ = r.Gate.Cancel(cleanup, ticket, hitl.CauseCallerWithdrawn)
		}
	}()
	waitCtx, cancel := context.WithDeadline(ctx, req.ExpiresAt)
	defer cancel()
	for {
		resolution, err := r.Gate.Await(waitCtx, ticket, gate.WaitOptions{MaxWait: time.Second * 30})
		if err != nil {
			return gateRPCError(err)
		}
		if err := resolution.Validate(); err != nil {
			return gateRPCError(err)
		}
		if resolution.Ticket != ticket {
			return gateRPCError(gate.ErrBinding)
		}
		if resolution.State == gate.Pending {
			if err := waitCtx.Err(); err != nil {
				return gateRPCError(err)
			}
			continue
		}
		if resolution.State == gate.Cancelled || resolution.State == gate.Expired {
			return codexGateCancel(native.Method), nil
		}
		d := *resolution.Decision
		live, err := r.request(ctx, native)
		if err != nil {
			return gateRPCError(err)
		}
		liveDigest, _ := live.Digest()
		if liveDigest != digest {
			return gateRPCError(gate.ErrBinding)
		}
		option, err := r.validateDecision(ctx, req, d)
		if err != nil {
			return gateRPCError(err)
		}
		if option.Kind == gate.Allow {
			if err := r.Claim(ctx, req, d); err != nil {
				return gateRPCError(err)
			}
		}
		if err := ctx.Err(); err != nil {
			return gateRPCError(err)
		}
		return json.RawMessage(bytes.Clone(option.Payload)), nil
	}
}

func (r CodexGateResponder) validateDecision(ctx context.Context, req gate.Request, d gate.Decision) (gate.NativeOption, error) {
	digest, _ := req.Digest()
	now := r.clock()
	if d.Ticket.RequestDigest != digest || d.Ticket.Binding != req.Binding || d.OperationDigest != req.OperationDigest {
		return gate.NativeOption{}, gate.ErrBinding
	}
	if d.ExpiresAt.IsZero() || !now.Before(d.ExpiresAt) || d.ExpiresAt.After(req.ExpiresAt) || d.Resolution.ResolvedAt.After(now) || !d.Resolution.ResolvedAt.Before(req.ExpiresAt) {
		return gate.NativeOption{}, gate.ErrExpired
	}
	for _, option := range req.Options {
		if option.ID != d.OptionID {
			continue
		}
		if !reflect.DeepEqual(option.Scope, d.Scope) || d.ExpiresAt.After(option.Scope.ExpiresAt) {
			return gate.NativeOption{}, gate.ErrBinding
		}
		if option.Kind == gate.Allow {
			err := gate.ValidateAdmission(ctx, req, d, gate.Admission{Binding: req.Binding, Adapter: req.Adapter, EnforcementPoint: req.EnforcementPoint, OperationDigest: req.OperationDigest, Now: now}, r.Authority)
			return option, err
		}
		if err := r.Authority.Verify(ctx, req, d); err != nil {
			return gate.NativeOption{}, fmt.Errorf("%w: %w", gate.ErrAuthority, err)
		}
		return option, ctx.Err()
	}
	return gate.NativeOption{}, gate.ErrUnsupported
}

func (r CodexGateResponder) request(ctx context.Context, native agentsessions.JsonRpcServerRequest) (gate.Request, error) {
	if native.Binding.Validate() != nil || gate.NativeRequestID(native.ID).Validate() != nil {
		return gate.Request{}, gate.ErrBinding
	}
	facts, err := r.Resolve(ctx, native)
	if err != nil {
		return gate.Request{}, err
	}
	req := facts.Request
	if facts.MaxScope != gate.Once {
		return gate.Request{}, gate.ErrUnsupported
	}
	if req.Binding != native.Binding || !json.Valid(facts.Descriptor) || bytes.Equal(bytes.TrimSpace(facts.Descriptor), []byte("null")) {
		return gate.Request{}, gate.ErrBinding
	}
	var p struct {
		Thread, Turn, Item string
	}
	var params struct {
		ThreadID         string          `json:"threadId"`
		TurnID           string          `json:"turnId"`
		ItemID           string          `json:"itemId"`
		Kind             string          `json:"kind"`
		Command          string          `json:"command"`
		CWD              string          `json:"cwd"`
		Available        json.RawMessage `json:"availableDecisions"`
		Network          json.RawMessage `json:"networkApprovalContext"`
		Permissions      json.RawMessage `json:"additionalPermissions"`
		ExecAmendment    json.RawMessage `json:"proposedExecpolicyAmendment"`
		NetworkAmendment json.RawMessage `json:"proposedNetworkPolicyAmendments"`
		Server           string          `json:"serverName"`
		Mode             string          `json:"mode"`
		Schema           json.RawMessage `json:"requestedSchema"`
		Meta             struct {
			Kind    string          `json:"codex_approval_kind"`
			Tool    string          `json:"tool_name"`
			Args    json.RawMessage `json:"tool_params"`
			Persist json.RawMessage `json:"persist"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(native.Params, &params); err != nil {
		return gate.Request{}, gate.ErrInvalid
	}
	p.Thread, p.Turn, p.Item = params.ThreadID, params.TurnID, params.ItemID
	if p.Thread == "" || p.Turn == "" || p.Thread != facts.ThreadID || p.Turn != facts.TurnID || facts.ItemID == "" {
		return gate.Request{}, gate.ErrBinding
	}
	var choices []string
	switch native.Method {
	case CodexCommandApprovalMethod:
		if p.Item == "" || p.Item != facts.ItemID || facts.Command == "" || facts.CWD == "" || (params.Command != "" && params.Command != facts.Command) || (params.CWD != "" && params.CWD != facts.CWD) {
			return gate.Request{}, fmt.Errorf("%w: unsupported_operation_binding", gate.ErrUnsupported)
		}
		if (params.Kind != "" && params.Kind != "command") || present(params.Network) || present(params.Permissions) || present(params.ExecAmendment) || present(params.NetworkAmendment) {
			return gate.Request{}, gate.ErrUnsupported
		}
		// The pinned command decision schema defines this default menu. A host
		// max-once ceiling filters remembered acceptance, never relabels it.
		var available []json.RawMessage
		if !present(params.Available) {
			choices = []string{"accept", "acceptForSession", "decline", "cancel"}
		} else if json.Unmarshal(params.Available, &available) != nil || len(available) == 0 {
			return gate.Request{}, gate.ErrUnsupported
		}
		for _, raw := range available {
			var choice string
			if json.Unmarshal(raw, &choice) != nil {
				return gate.Request{}, gate.ErrUnsupported
			}
			choices = append(choices, choice)
		}
		req.OperationClass = "command"
	case CodexElicitationMethod:
		if params.Meta.Kind != string(CodexApprovalMCPToolCall) || params.Mode != "form" || params.Server == "" || params.Meta.Tool == "" || params.Server != facts.MCPServer || params.Meta.Tool != facts.MCPTool || !json.Valid(params.Meta.Args) || !json.Valid(facts.MCPArguments) || !jsonBytesEqual(params.Meta.Args, facts.MCPArguments) {
			return gate.Request{}, fmt.Errorf("%w: unsupported_operation_binding", gate.ErrUnsupported)
		}
		if !knownCodexPersistence(params.Meta.Persist) || !emptyCodexForm(params.Schema) {
			return gate.Request{}, gate.ErrUnsupported
		}
		choices = []string{"accept", "decline", "cancel"}
		req.OperationClass = "mcp_tool_call"
	default:
		return gate.Request{}, gate.ErrUnsupported
	}
	req.NativeRequestID = gate.NativeRequestID(bytes.Clone(native.ID))
	req.TurnID = runtimecontract.TurnID(facts.TurnID)
	req.ToolCallID = runtimecontract.ToolCallID(facts.ItemID)
	req.Options = nil
	for _, choice := range choices {
		if choice == "acceptForSession" {
			continue
		}
		kind := gate.Allow
		switch choice {
		case "accept":
		case "decline":
			kind = gate.Deny
		case "cancel":
			kind = gate.Cancel
		default:
			return gate.Request{}, fmt.Errorf("%w: native option %s", gate.ErrUnsupported, choice)
		}
		payload, _ := json.Marshal(map[string]any{"decision": choice})
		if native.Method == CodexElicitationMethod {
			var content any
			if choice == "accept" {
				content = map[string]any{}
			}
			payload, _ = json.Marshal(map[string]any{"action": choice, "content": content})
		}
		req.Options = append(req.Options, gate.NativeOption{ID: choice, Kind: kind, Scope: gate.Scope{Kind: gate.Once, Target: req.OperationID, MaxUses: 1, ExpiresAt: req.ExpiresAt}, Payload: payload})
	}
	// The descriptor includes the exact wire payload AND joined host facts. Raw
	// arguments remain raw; no float64 conversion can alter large numeric tokens.
	descriptor, err := json.Marshal(struct {
		Version                                        string
		Method                                         string
		Params                                         json.RawMessage
		Host                                           json.RawMessage
		Thread, Turn, Item, Command, CWD, Server, Tool string
		Arguments                                      json.RawMessage
	}{"codex-gate-operation-v1", native.Method, native.Params, facts.Descriptor, facts.ThreadID, facts.TurnID, facts.ItemID, facts.Command, facts.CWD, facts.MCPServer, facts.MCPTool, facts.MCPArguments})
	if err != nil {
		return gate.Request{}, err
	}
	sum := sha256.Sum256(descriptor)
	req.OperationDigest = runtimecontract.Digest{Scheme: "sha256", DescriptorVersion: "codex-gate-operation-v1", Value: hex.EncodeToString(sum[:])}
	return req.Snapshot()
}

// Only known remembered choices may be filtered by the explicit once ceiling.
func knownCodexPersistence(raw json.RawMessage) bool {
	if !present(raw) {
		return true
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return value == "session" || value == "always"
	}
	var values []string
	if json.Unmarshal(raw, &values) != nil || len(values) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, v := range values {
		if (v != "session" && v != "always") || seen[v] {
			return false
		}
		seen[v] = true
	}
	return true
}
func present(raw json.RawMessage) bool {
	return len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}
func jsonBytesEqual(a, b json.RawMessage) bool {
	var x, y bytes.Buffer
	return json.Compact(&x, a) == nil && json.Compact(&y, b) == nil && bytes.Equal(x.Bytes(), y.Bytes())
}
func emptyCodexForm(raw json.RawMessage) bool {
	var form map[string]json.RawMessage
	if json.Unmarshal(raw, &form) != nil || len(form) != 2 || string(form["type"]) != `"object"` {
		return false
	}
	var props map[string]json.RawMessage
	return json.Unmarshal(form["properties"], &props) == nil && props != nil && len(props) == 0
}
func codexGateCancel(method string) any {
	if method == CodexElicitationMethod {
		return map[string]any{"action": "cancel", "content": nil}
	}
	return map[string]any{"decision": "cancel"}
}
func gateRPCError(err error) (any, *agentsessions.JsonRpcError) {
	code := -32000
	if errors.Is(err, gate.ErrUnsupported) {
		code = -32601
	}
	// Do not expose raw operation arguments, policy internals or host errors.
	message := "Codex gate refused request"
	if errors.Is(err, gate.ErrUnsupported) {
		message += ": unsupported binding or native options"
	}
	return nil, &agentsessions.JsonRpcError{Code: code, Message: message}
}
