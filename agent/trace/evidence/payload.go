package evidence

import (
	"github.com/hollis-labs/libs/llm/core/llmtypes"
	"github.com/hollis-labs/libs/mesh/core/gate"
	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
	"github.com/hollis-labs/libs/security/policy"
)

// ModelCallID identifies one model invocation, separately from its parent turn.
type ModelCallID string

// Status is a run's terminal state as reported in an event payload.
type Status string

const (
	Succeeded Status = "succeeded"
	Failed    Status = "failed"
	Cancelled Status = "cancelled"
	Pending   Status = "pending"
)

// EventPayload is the payload a recorded runtimeevents.Event carries, envelope
// version 1. It holds references and outcomes, never raw prompts, arguments,
// native opaque payloads or error strings. The recorder decodes it strictly:
// an unknown field is rejected rather than dropped.
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
