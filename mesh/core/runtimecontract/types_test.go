package runtimecontract_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/libs/mesh/core/gate/gatetest"
	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
)

func TestReferenceShapeAndEnvironmentCapabilities(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := gatetest.Request(now)
	if err := r.Environment.ValidateAt(now, []string{"tool.execute"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Environment.ValidateAt(now, []string{"deploy"}); !errors.Is(err, runtimecontract.ErrUnsupported) {
		t.Fatalf("missing capability accepted: %v", err)
	}
	if err := r.Environment.ValidateAt(r.Environment.ExpiresAt, nil); !errors.Is(err, runtimecontract.ErrExpired) {
		t.Fatalf("expired receipt accepted: %v", err)
	}
	for _, change := range []func(*runtimecontract.EnvironmentRef){
		func(e *runtimecontract.EnvironmentRef) { e.Receipt.ID = " " },
		func(e *runtimecontract.EnvironmentRef) { e.Receipt.Digest.Scheme = "unknown-hash" },
		func(e *runtimecontract.EnvironmentRef) { e.Receipt.Digest.Value = "not-hex" },
		func(e *runtimecontract.EnvironmentRef) { e.Receipt.Digest.Value = strings.Repeat("A", 64) },
		func(e *runtimecontract.EnvironmentRef) { e.Lease.OwnerID = "another-owner" },
		func(e *runtimecontract.EnvironmentRef) { e.Capabilities = []string{" "} },
		func(e *runtimecontract.EnvironmentRef) { e.Capabilities = []string{"tool.execute", "tool.execute"} },
	} {
		candidate := gatetest.Request(now).Environment
		change(candidate)
		if err := candidate.Validate(); err == nil {
			t.Fatalf("malformed environment accepted: %+v", candidate)
		}
	}
	for _, change := range []func(*runtimecontract.Binding){
		func(b *runtimecontract.Binding) { b.OwnerID = " " },
		func(b *runtimecontract.Binding) { b.AttemptID = " " },
		func(b *runtimecontract.Binding) { b.Generation = 0 },
	} {
		b := r.Binding
		change(&b)
		if err := b.Validate(); err == nil {
			t.Fatal("malformed binding accepted")
		}
	}
}

// fakeRecoveryHost holds separately trusted, owner-scoped records. Reference
// shape/JSON round trips alone do not permit handing history to another attempt.
type fakeRecoveryHost struct {
	stored    runtimecontract.RecoveryLocator
	successor runtimecontract.Binding
	now       time.Time
}

func (h fakeRecoveryHost) resolve(locator runtimecontract.RecoveryLocator, target runtimecontract.Binding) (runtimecontract.Reference, error) {
	if err := locator.Validate(); err != nil {
		return runtimecontract.Reference{}, err
	}
	if !reflect.DeepEqual(locator, h.stored) || target != h.successor || target.OwnerID != locator.Binding.OwnerID || target.OperativeID != locator.Binding.OperativeID || target.ConversationID != locator.Binding.ConversationID || target.Generation <= locator.Binding.Generation {
		return runtimecontract.Reference{}, errors.New("host refused recovery binding")
	}
	if err := locator.Environment.ValidateAt(h.now, []string{"tool.execute"}); err != nil {
		return runtimecontract.Reference{}, err
	}
	return locator.Transcript, nil
}
func TestPortableRecoveryHandoffRequiresHostAuthority(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := gatetest.Request(now)
	plan := gatetest.Reference("plan-1")
	snapshot := gatetest.Reference("snapshot-1")
	pending := gatetest.Reference("pending-operations-1")
	locator := runtimecontract.RecoveryLocator{Binding: r.Binding, Environment: *r.Environment, Transcript: gatetest.Reference("transcript-1"), Workspace: r.Environment.Workspace, Plan: &plan, Snapshot: &snapshot, PendingOperations: &pending, CommittedCursor: "opaque-cursor-a", LastCommittedTurn: "turn-1"}
	successor := r.Binding
	successor.AttemptID = "attempt-8"
	successor.Generation = 8
	host := fakeRecoveryHost{stored: locator, successor: successor, now: now}
	raw, err := json.Marshal(locator)
	if err != nil {
		t.Fatal(err)
	}
	var recovered runtimecontract.RecoveryLocator
	if err := json.Unmarshal(raw, &recovered); err != nil {
		t.Fatal(err)
	}
	transcript, err := host.resolve(recovered, successor)
	if err != nil || transcript != locator.Transcript {
		t.Fatalf("portable handoff failed: %v", err)
	}
	if recovered.CommittedCursor != locator.CommittedCursor || *recovered.Plan != plan || *recovered.Snapshot != snapshot || *recovered.PendingOperations != pending {
		t.Fatal("portable recovery evidence lost")
	}
	forged := recovered
	forged.Binding.OwnerID = "other-owner"
	if _, err := host.resolve(forged, successor); err == nil {
		t.Fatal("cross-owner history leaked")
	}
	forged = recovered
	forged.Workspace = gatetest.Reference("other-workspace")
	if _, err := host.resolve(forged, successor); err == nil {
		t.Fatal("workspace mismatch accepted")
	}
	forged = recovered
	forged.Binding.AttemptID = "unissued-attempt"
	if err := forged.Validate(); err != nil {
		t.Fatal("fixture should be shape-valid")
	}
	if _, err := host.resolve(forged, successor); err == nil {
		t.Fatal("shape-valid forged binding accepted as authority")
	}
	wrong := successor
	wrong.ConversationID = "another-conversation"
	if _, err := host.resolve(recovered, wrong); err == nil {
		t.Fatal("history transferred to unissued target")
	}
}
