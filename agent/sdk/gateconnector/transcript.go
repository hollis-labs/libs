package gateconnector

import (
	"encoding/json"
	"time"

	"github.com/hollis-labs/libs/agent/sdk/transcript"
	"github.com/hollis-labs/libs/mesh/core/gate"
	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
)

// TranscriptIdentity is host-issued; a Gatecrasher interaction cannot choose
// the conversation, session or placement of its own sideband line.
type TranscriptIdentity struct {
	UUID       string
	ParentUUID *string
	SessionID  string
	Timestamp  time.Time
}

// ApprovalLine is a reference-only transcript projection. Delivery and effect
// receipts remain distinct, and an unknown effect is not called successful.
type ApprovalLine struct {
	TicketID            string                 `json:"ticket_id"`
	TicketRevision      int64                  `json:"ticket_revision"`
	OperationID         string                 `json:"operation_id"`
	OperationDigest     runtimecontract.Digest `json:"operation_digest"`
	State               gate.ResolutionState   `json:"state"`
	DecisionID          string                 `json:"decision_id,omitempty"`
	OptionID            string                 `json:"option_id,omitempty"`
	Scope               *gate.Scope            `json:"scope,omitempty"`
	DeliveryReceiptRef  string                 `json:"delivery_receipt_ref,omitempty"`
	ExecutionReceiptRef string                 `json:"execution_receipt_ref,omitempty"`
	ExecutionState      gate.ExecutionState    `json:"execution_state,omitempty"`
}

// EncodeApprovalLine writes a Claude-compatible system sideband payload from
// evidence correlated by Project. The host appends the returned bytes
// using its transcript writer after verifying the current conversation/binding.
func EncodeApprovalLine(request gate.Request, resolution gate.Resolution, delivery *gate.DeliveryReceipt, execution *gate.ExecutionReceipt, identity TranscriptIdentity) (json.RawMessage, error) {
	e, err := Project(request, resolution, delivery, execution)
	if err != nil {
		return nil, err
	}
	line := ApprovalLine{
		TicketID: e.Ticket.Handle.ItemID, TicketRevision: e.Ticket.Handle.Revision,
		OperationID: e.OperationID, OperationDigest: e.OperationDigest,
		State: e.State, DecisionID: e.DecisionID, OptionID: e.OptionID, Scope: e.Scope,
	}
	if e.Delivery != nil {
		line.DeliveryReceiptRef = e.Delivery.EvidenceRef
	}
	if e.Execution != nil {
		line.ExecutionReceiptRef = e.Execution.EvidenceRef
		line.ExecutionState = e.Execution.State
	}
	payload, err := json.Marshal(line)
	if err != nil {
		return nil, err
	}
	return transcript.EncodeSideband(transcript.SidebandRecord{
		Type: "system", Subtype: "approval", UUID: identity.UUID,
		ParentUUID: identity.ParentUUID, SessionID: identity.SessionID,
		Timestamp: identity.Timestamp, Payload: payload,
	})
}
