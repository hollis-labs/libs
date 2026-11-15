// Package gatetest supplies synthetic shared contract inputs for SDK-native and
// provider-adapter tests. Nothing here issues real identity, grants or receipts.
package gatetest

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/hollis-labs/libs/mesh/core/gate"
	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
)

// Digest hashes already canonical host descriptor bytes. Production hosts must
// define the versioned descriptor and include all operation-relevant facts.
func Digest(version, descriptor string) runtimecontract.Digest {
	sum := sha256.Sum256([]byte(descriptor))
	return runtimecontract.Digest{Scheme: "sha256", DescriptorVersion: version, Value: hex.EncodeToString(sum[:])}
}
func Reference(id string) runtimecontract.Reference {
	return runtimecontract.Reference{ID: id, Revision: "1", Digest: Digest("", id), IssuerRef: "fake-host", OwnerID: "owner-1"}
}

// Request returns fresh mutable slices and pointers on every call. Its options
// intentionally use opaque native IDs rather than display labels or booleans.
func Request(now time.Time) gate.Request {
	expiry := now.Add(time.Minute)
	scope := gate.Scope{Kind: gate.Once, Target: "operation-1", MaxUses: 1, ExpiresAt: expiry}
	return gate.Request{
		ContractVersion: gate.ContractVersion, IdempotencyKey: "fake-request-1",
		Binding:          runtimecontract.Binding{OperativeID: "operative-1", SeatID: "seat-1", ConversationID: "conversation-1", OwnerID: "owner-1", AttemptID: "attempt-7", Generation: 7},
		Adapter:          gate.AdapterScope{AdapterID: "fake-adapter", Protocol: "fake-json-rpc", LaunchMode: "on-request"},
		Environment:      &runtimecontract.EnvironmentRef{Receipt: Reference("environment-1"), Workspace: Reference("workspace-1"), Lease: Reference("lease-7"), Grants: Reference("grant-1"), Capabilities: []string{"tool.execute"}, ExpiresAt: expiry},
		EnforcementPoint: "fake-host/tool-admission", OperationID: "operation-1", OperationClass: "tool",
		OperationDigest:  Digest("tool-v1", `{"action":"tool.execute","args":{"value":1},"environment":"environment-1","resource":"resource-1@1","effect":"write"}`),
		ResourceVersions: []runtimecontract.Reference{Reference("resource-1")},
		TurnID:           "turn-1", ToolCallID: "tool-call-1", NativeRequestID: gate.NativeRequestID(`9007199254740993`),
		Options:   []gate.NativeOption{{ID: "native-allow-once", Kind: gate.Allow, Scope: scope, Payload: []byte(`{"native":"once"}`)}, {ID: "native-deny", Kind: gate.Deny, Scope: scope}},
		ExpiresAt: expiry, Policy: Reference("policy-1"), Provenance: []runtimecontract.Reference{Reference("rule-1")},
		Obligations: []gate.ObligationRef{{Kind: "approval", Ref: Reference("approval-rule-1")}},
	}
}
func Capabilities(r gate.Request) gate.Capabilities {
	return gate.Capabilities{Adapter: r.Adapter, OperationClasses: []string{"tool"}, DecisionKinds: []gate.DecisionKind{gate.Allow, gate.Deny, gate.Cancel}, ScopeKinds: []gate.ScopeKind{gate.Once}, ObligationKinds: []string{"approval"}}
}
