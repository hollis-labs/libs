package sdk

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	agentcontext "github.com/hollis-labs/libs/agent/core/context"
	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
	"github.com/hollis-labs/libs/llm/core/llmtypes"
	"github.com/hollis-labs/libs/mesh/core/gate"
	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
	"github.com/hollis-labs/libs/security/policy"
)

// Digest binds all SDK operation fields. Admission hosts must recompute it from
// authoritative bytes and verify the versions' coverage of actual tool effects.
func (op Operation) Digest() (runtimecontract.Digest, error) {
	b, err := json.Marshal(op)
	if err != nil {
		return runtimecontract.Digest{}, err
	}
	hash := sha256.Sum256(b)
	return runtimecontract.Digest{Scheme: "sha256", DescriptorVersion: op.Version, Value: hex.EncodeToString(hash[:])}, nil
}

func (o *Operative) executeTool(ctx context.Context, in Input, reservation RunReservation, turnID runtimecontract.TurnID, call llmtypes.ToolUseBlock, tool Tool, events *runEvents, result *Result) (out ToolResult, err error) {
	if err = o.verify(ctx, in); err != nil {
		return out, err
	}
	args, err := json.Marshal(call.Input)
	if err != nil {
		return out, err
	}
	op := Operation{RunID: reservation.RunID, InputDigest: reservation.InputDigest, Version: "sdk-tool-v1", ID: fmt.Sprintf("%s/tool/%s", turnID, call.ID), Binding: in.Binding, Environment: in.Environment, DefinitionDigest: o.digest, PermissionProfile: o.definition.HarnessProfile.Permissions.Profile, TurnID: turnID, ToolCallID: runtimecontract.ToolCallID(call.ID), Tool: call.Name, Arguments: args, Versions: tool.Versions}
	projection, err := clone(op)
	if err != nil {
		return out, err
	}
	evaluated, err := o.ports.Policy.Evaluate(ctx, projection)
	if err != nil {
		return out, err
	}
	if err = evaluated.Decision.Validate(); err != nil {
		return out, err
	}
	if err = evaluated.Reference.Validate(); err != nil {
		return out, err
	}
	policyPayload := EventPayload{TurnID: turnID, ToolCallID: op.ToolCallID, OperationID: op.ID, Policy: &evaluated.Reference, PolicyEffect: evaluated.Decision.Effect}
	if err = events.emit(ctx, "sdk.policy.evaluated", policyPayload); err != nil {
		return out, err
	}
	if evaluated.Decision.Effect == policy.Deny {
		return out, gate.ErrDenied
	}
	if err = validateObligations(evaluated); err != nil {
		return out, err
	}
	digest, err := op.Digest()
	if err != nil {
		return out, err
	}
	for _, ref := range evaluated.Provenance {
		if err = ref.Validate(); err != nil {
			return out, err
		}
	}
	evaluated, err = clone(evaluated)
	if err != nil {
		return out, err
	}
	payload := EventPayload{TurnID: turnID, ToolCallID: op.ToolCallID, OperationID: op.ID, Policy: &evaluated.Reference}
	var request *gate.Request
	var decision *gate.Decision
	needsApproval := evaluated.Decision.Effect == policy.Ask || len(evaluated.Decision.Obligations) > 0
	if needsApproval {
		if o.ports.Gate.Port == nil || o.ports.Authority == nil {
			return out, fmt.Errorf("%w: approval requires gate and authority ports", ErrUnsupported)
		}
		expiry := o.ports.Now().Add(o.ports.Limits.ApprovalTTL)
		if in.Environment.ExpiresAt.Before(expiry) {
			expiry = in.Environment.ExpiresAt
		}
		scope := gate.Scope{Kind: gate.Once, Target: op.ID, MaxUses: 1, ExpiresAt: expiry}
		nativeID, _ := json.Marshal(op.ID)
		// Hash the key to remain within HITL's idempotency-key bound even for long IDs.
		keyBytes, _ := json.Marshal(struct {
			Binding   runtimecontract.Binding
			Operation string
		}{in.Binding, op.ID})
		key := sha256.Sum256(keyBytes)
		gateRequest := gate.Request{ContractVersion: gate.ContractVersion, IdempotencyKey: "sdk:" + hex.EncodeToString(key[:]), Binding: in.Binding, Adapter: NativeAdapterScope(), Environment: &in.Environment, EnforcementPoint: "tradecraft/tool-admission", OperationID: op.ID, OperationClass: "tool", OperationDigest: digest, ResourceVersions: tool.Versions, TurnID: turnID, ToolCallID: op.ToolCallID, NativeRequestID: nativeID, Options: []gate.NativeOption{{ID: "allow-once", Kind: gate.Allow, Scope: scope}, {ID: "deny", Kind: gate.Deny, Scope: scope}}, ExpiresAt: expiry, Policy: evaluated.Reference, Provenance: evaluated.Provenance, Obligations: evaluated.Obligations}
		gateRequest, err = gateRequest.Snapshot()
		if err != nil {
			return out, err
		}
		if err = events.emit(ctx, runtimeevents.KindAgentPermissionRequested, payload); err != nil {
			return out, err
		}
		resolution, err := o.ports.Gate.Resolve(ctx, gateRequest, o.ports.Now())
		if resolution.Ticket.Handle.ItemID != "" {
			ticket := resolution.Ticket
			result.PendingTicket = &ticket
			payload.ApprovalID = ticket.Handle.ItemID
		}
		if err != nil {
			return out, err
		}
		payload.ApprovalState = resolution.State
		if err = events.emit(ctx, runtimeevents.KindAgentPermissionResolved, payload); err != nil {
			return out, err
		}
		switch resolution.State {
		case gate.Pending:
			return out, ErrPending
		case gate.Cancelled:
			return out, gate.ErrDenied
		case gate.Expired:
			return out, gate.ErrExpired
		case gate.Decided:
		default:
			return out, ErrInvalid
		}
		resolved := *resolution.Decision
		decision = &resolved
		payload.DecisionID = decision.Resolution.ResolutionID
		if err = gate.ValidateAdmission(ctx, gateRequest, *decision, gate.Admission{Binding: in.Binding, Adapter: NativeAdapterScope(), EnforcementPoint: gateRequest.EnforcementPoint, OperationDigest: digest, Now: o.ports.Now()}, o.ports.Authority); err != nil {
			return out, err
		}
		result.PendingTicket = nil

		request = &gateRequest

	} else {
		if err = events.emit(ctx, "sdk.policy.allowed", payload); err != nil {
			return out, err
		}
	}
	if err = events.emit(ctx, runtimeevents.KindAgentToolUse, payload); err != nil {
		return out, err
	}
	invoked := false
	err = o.admit(ctx, Admission{Run: reservation, Kind: "tool", Binding: in.Binding, Environment: in.Environment, DefinitionDigest: o.digest, Operation: &op, Request: request, Decision: decision, Policy: &evaluated}, func(c context.Context) error {
		// Decode the admitted descriptor, rather than mutable provider/host maps.
		var decoded map[string]any
		decoderErr := decodeArgs(op.Arguments, &decoded)
		if decoderErr != nil {
			return decoderErr
		}
		invoked = true
		var e error
		out, e = tool.Execute(c, decoded)
		return e
	})
	payload.Execution = gate.ExecutionNotStarted
	if invoked {
		payload.Execution = gate.ExecutionSucceeded
		if out.IsError {
			payload.Execution = gate.ExecutionFailed
		}
		if err != nil {
			payload.Execution = gate.ExecutionUnknown
		}
	}
	observeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err = errors.Join(err, events.emit(observeCtx, runtimeevents.KindAgentToolResult, payload))
	if err == nil && len(out.Content) > o.ports.Limits.MaxToolResultBytes {
		return out, fmt.Errorf("%w: tool result bound; effect already observed", ErrLimit)
	}
	return out, err
}

func validateObligations(p PolicyResult) error {
	if len(p.Obligations) != len(p.Decision.Obligations) {
		return fmt.Errorf("%w: obligation reference coverage", ErrUnsupported)
	}
	for i, o := range p.Decision.Obligations {
		// This first adapter supports approval intent only. Dry-run/rate/amendment
		// semantics require a separately supported host adapter, never a bool fallback.
		if o.Kind != policy.Approval || p.Obligations[i].Kind != string(o.Kind) || p.Obligations[i].Ref.ID != o.Reference {
			return fmt.Errorf("%w: mandatory obligation", ErrUnsupported)
		}
	}
	return nil
}

func checkContext(window *agentcontext.ContextWindow, req llmtypes.ChatRequest) error {
	b, err := json.Marshal(struct {
		Messages []llmtypes.ChatMessage
		Tools    []llmtypes.ToolDefinition
	}{req.Messages, req.Tools})
	if err != nil {
		return err
	}
	estimate := (agentcontext.DefaultEstimator{}).Estimate(string(b))
	if window.UsedTokens()+estimate+req.MaxTokens > window.TotalBudget {
		return ErrContextBudget
	}
	return nil
}

func decodeArgs(raw []byte, out *map[string]any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	return d.Decode(out)
}
