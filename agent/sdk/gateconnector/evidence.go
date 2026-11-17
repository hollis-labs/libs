package gateconnector

import (
	"fmt"

	"github.com/hollis-labs/libs/mesh/core/gate"
	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
)

// Evidence is a reference-only projection for a transcript sideband, CLI
// invocation or tool card. It is an observation, never an admission token.
// Delivery and execution remain separate even when they share a decision.
type Evidence struct {
	Binding         runtimecontract.Binding `json:"binding"`
	OperationID     string                  `json:"operation_id"`
	OperationDigest runtimecontract.Digest  `json:"operation_digest"`
	Ticket          gate.Ticket             `json:"ticket"`
	State           gate.ResolutionState    `json:"state"`
	DecisionID      string                  `json:"decision_id,omitempty"`
	OptionID        string                  `json:"option_id,omitempty"`
	Scope           *gate.Scope             `json:"scope,omitempty"`
	Delivery        *DeliveryEvidence       `json:"delivery,omitempty"`
	Execution       *ExecutionEvidence      `json:"execution,omitempty"`
}

type DeliveryEvidence struct {
	Status      gate.DeliveryStatus `json:"status"`
	EvidenceRef string              `json:"evidence_ref,omitempty"`
}
type ExecutionEvidence struct {
	State       gate.ExecutionState `json:"state"`
	EvidenceRef string              `json:"evidence_ref,omitempty"`
	EffectRef   string              `json:"effect_ref,omitempty"`
}

// Project checks correlation and separates the authoritative decision from
// later delivery/effect observations. Hosts must reload a current Gatecrasher
// resolution and verify authority before taking any action; this value cannot
// be used to admit or repeat an effect.
func Project(request gate.Request, resolution gate.Resolution, delivery *gate.DeliveryReceipt, execution *gate.ExecutionReceipt) (Evidence, error) {
	if err := request.Validate(); err != nil {
		return Evidence{}, err
	}
	if err := resolution.Validate(); err != nil {
		return Evidence{}, err
	}
	if err := checkTicket(request, resolution.Ticket); err != nil {
		return Evidence{}, err
	}
	out := Evidence{Binding: request.Binding, OperationID: request.OperationID, OperationDigest: request.OperationDigest, Ticket: resolution.Ticket, State: resolution.State}
	if decision := resolution.Decision; decision != nil {
		if decision.OperationDigest != request.OperationDigest || decision.Resolution.ResolutionID == "" {
			return Evidence{}, gate.ErrBinding
		}
		found := false
		for _, option := range request.Options {
			if option.ID == decision.OptionID && option.Scope.Kind == decision.Scope.Kind && option.Scope.Target == decision.Scope.Target && option.Scope.MaxUses == decision.Scope.MaxUses && option.Scope.ExpiresAt.Equal(decision.Scope.ExpiresAt) {
				found = true
				break
			}
		}
		if !found {
			return Evidence{}, gate.ErrBinding
		}
		scope := decision.Scope
		out.DecisionID, out.OptionID, out.Scope = decision.Resolution.ResolutionID, decision.OptionID, &scope
	}
	if delivery != nil {
		if out.DecisionID == "" {
			return Evidence{}, fmt.Errorf("%w: delivery without decision", gate.ErrInvalid)
		}
		if err := delivery.Validate(); err != nil {
			return Evidence{}, err
		}
		if delivery.DecisionRef != out.DecisionID || delivery.Binding != request.Binding || delivery.AdapterID != request.Adapter.AdapterID || !delivery.NativeRequestID.Equal(request.NativeRequestID) {
			return Evidence{}, gate.ErrBinding
		}
		out.Delivery = &DeliveryEvidence{Status: delivery.Status, EvidenceRef: delivery.EvidenceRef}
	}
	if execution != nil {
		if out.DecisionID == "" {
			return Evidence{}, fmt.Errorf("%w: execution without decision", gate.ErrInvalid)
		}
		if err := execution.Validate(); err != nil {
			return Evidence{}, err
		}
		if execution.DecisionRef != out.DecisionID || execution.Binding != request.Binding || execution.OperationID != request.OperationID {
			return Evidence{}, gate.ErrBinding
		}
		out.Execution = &ExecutionEvidence{State: execution.State, EvidenceRef: execution.EvidenceRef, EffectRef: execution.EffectRef}
	}
	return out, nil
}
