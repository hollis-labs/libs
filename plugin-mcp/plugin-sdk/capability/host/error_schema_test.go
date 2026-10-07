package host

import (
	"context"
	"errors"
	"testing"

	"github.com/hollis-labs/plugin-sdk/capability"
)

func TestCallRefusesInvalidProfileIDBeforeReservation(t *testing.T) {
	for _, id := range []capability.RequestID{0, capability.RequestID(capability.MaxSafeInteger + 1)} {
		e, _, c, _, releases, _ := enforcementFixture(t)
		c.RequestID = id
		failureCode(t, e.Run(context.Background(), c, func(context.Context, *Permit) error { t.Fatal("invalid correlation caused effect"); return nil }), capability.InvalidRequest)
		if releases.Load() != 0 {
			t.Fatal("invalid correlation reserved budget")
		}
	}
}

func TestParentDetailsSurviveInternalAdmissionAndAudit(t *testing.T) {
	for _, detail := range []capability.FailureDetail{capability.ParentInvalid, capability.ParentTerminal} {
		e, _, c, _, _, events := enforcementFixture(t)
		e.Budget = budgetFunc(func(context.Context, Authority, Call) (func(), error) {
			return nil, &capability.Error{Code: capability.TargetUnavailable, EffectState: capability.NotStarted, Detail: detail}
		})
		err := e.Run(context.Background(), c, func(context.Context, *Permit) error { t.Fatal("invalid parent effect"); return nil })
		failureCode(t, err, capability.TargetUnavailable)
		var failure *capability.Error
		errors.As(err, &failure)
		data, serialization := failure.RPCData()
		if serialization != nil || data.RequestID != c.RequestID || data.Detail != detail || data.Retryable {
			t.Fatal("parent error lost correlation/classification")
		}
		if len(*events) != 1 || (*events)[0].Reason != detail || (*events)[0].RequestID != "1" || e.Audit.Denials()[DenialKey{capability.TargetUnavailable, detail}] != 1 {
			t.Fatal("parent refusal lost audit/counter")
		}
	}
}

func TestTypedCallbackClassificationDoesNotRequirePremintedID(t *testing.T) {
	e, _, c, _, _, events := enforcementFixture(t)
	err := e.Run(context.Background(), c, func(context.Context, *Permit) error {
		return &capability.Error{Code: capability.Conflict, EffectState: capability.NotCommitted}
	})
	failureCode(t, err, capability.Conflict)
	var failure *capability.Error
	errors.As(err, &failure)
	data, serialization := failure.RPCData()
	if serialization != nil || data.RequestID != c.RequestID || data.EffectState != capability.NotCommitted || (*events)[0].EffectState != capability.NotCommitted {
		t.Fatal("internal typed callback error became unclassified outcome")
	}
}
