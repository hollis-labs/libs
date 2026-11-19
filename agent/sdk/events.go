package sdk

import (
	"context"

	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
	"github.com/hollis-labs/libs/llm/core/llmtypes"
	"github.com/hollis-labs/libs/mesh/core/gate"
	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
	"github.com/hollis-labs/libs/security/policy"
)

// ModelCallID identifies one model invocation, separately from its parent turn.
type ModelCallID string

// EventPayload is the experimental SDK payload inside runtimeevents envelope 1.
// It carries references and outcomes, never raw prompts, arguments, native opaque
// payloads or error strings. Sequence is producer-local; it is not a replay cursor.
type EventPayload struct {
	RunID            string                     `json:"run_id"`
	Binding          runtimecontract.Binding    `json:"binding"`
	Environment      runtimecontract.Reference  `json:"environment"`
	DefinitionDigest string                     `json:"definition_digest"`
	Route            string                     `json:"route,omitempty"`
	Model            string                     `json:"model,omitempty"`
	ModelCallID      ModelCallID                `json:"model_call_id,omitempty"`
	Tool             string                     `json:"tool,omitempty"`
	TurnID           runtimecontract.TurnID     `json:"turn_id,omitempty"`
	ToolCallID       runtimecontract.ToolCallID `json:"tool_call_id,omitempty"`
	OperationID      string                     `json:"operation_id,omitempty"`
	ApprovalID       string                     `json:"approval_id,omitempty"`
	DecisionID       string                     `json:"decision_id,omitempty"`
	PolicyEffect     policy.Effect              `json:"policy_effect,omitempty"`
	Policy           *runtimecontract.Reference `json:"policy,omitempty"`
	ApprovalState    gate.ResolutionState       `json:"approval_state,omitempty"`
	Execution        gate.ExecutionState        `json:"execution,omitempty"`
	Status           Status                     `json:"status,omitempty"`
	Usage            *llmtypes.Usage            `json:"usage,omitempty"`
}

type runEvents struct {
	emitter runtimeevents.Emitter
	base    EventPayload
}

func (e *runEvents) emit(ctx context.Context, kind runtimeevents.EventKind, p EventPayload) error {
	p.RunID = e.base.RunID
	p.Binding = e.base.Binding
	p.Environment = e.base.Environment
	p.DefinitionDigest = e.base.DefinitionDigest
	p.Route = e.base.Route
	p.Model = e.base.Model
	if p.TurnID != "" {
		p.ModelCallID = ModelCallID(string(p.TurnID) + "/model/1")
	}
	return e.emitter.Emit(ctx, kind, runtimeevents.Source{Channel: "sdk-native", Confidence: runtimeevents.ConfidenceExact}, p, runtimeevents.WithTurnID(string(p.TurnID)))
}
