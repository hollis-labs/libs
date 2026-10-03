package agentcontracts

import (
	"time"

	"github.com/hollis-labs/substrate/llm-core/contracts/capabilities"
)

// Digests are the four content hashes a launch record pins: the definition, the
// assignment, the launch profile and the kickoff. There is no hydrated snapshot.
// The string form of a digest (for example "sha256:<hex>") is the computing
// library's choice; this package only carries it.
type Digests struct {
	Definition    string `json:"definition" yaml:"definition"`
	Assignment    string `json:"assignment" yaml:"assignment"`
	LaunchProfile string `json:"launch_profile" yaml:"launch_profile"`
	Kickoff       string `json:"kickoff" yaml:"kickoff"`
}

// GrantDiagnostic reports a grant that was requested but is not enforced, so
// "not enforced" is never silent. Enforced is always false when a diagnostic is
// present.
type GrantDiagnostic struct {
	Grant    string `json:"grant" yaml:"grant"`
	Enforced bool   `json:"enforced" yaml:"enforced"`
	Reason   string `json:"reason,omitempty" yaml:"reason,omitempty"`
}

// EffectiveGrants is what a run actually received, with a diagnostic for every
// grant the host did not enforce.
type EffectiveGrants struct {
	Tools       []string          `json:"tools,omitempty" yaml:"tools,omitempty"`
	Permissions []string          `json:"permissions,omitempty" yaml:"permissions,omitempty"`
	MCP         MCPGrant          `json:"mcp,omitempty" yaml:"mcp,omitempty"`
	Diagnostics []GrantDiagnostic `json:"diagnostics,omitempty" yaml:"diagnostics,omitempty"`
}

// LaunchRecord is the recorded materialization of a launch: the one noun for
// "what was actually launched". A host assembling a full persisted record
// attaches its own sub-records (such as a go-materialize manifest) around this
// one; they are never imported here.
type LaunchRecord struct {
	Digests         Digests         `json:"digests" yaml:"digests"`
	EffectiveGrants EffectiveGrants `json:"effective_grants" yaml:"effective_grants"`
	EffectiveLimits Limits          `json:"effective_limits" yaml:"effective_limits"`
	EffectiveTrust  Trust           `json:"effective_trust" yaml:"effective_trust"`
	// Forced lists which unmet requires a caller overrode. It records
	// capabilities only — never grants, auth, posture or trust.
	Forced      []capabilities.Name `json:"forced,omitempty" yaml:"forced,omitempty"`
	Requester   Requester           `json:"requester" yaml:"requester"`
	Correlation Correlation         `json:"correlation,omitempty" yaml:"correlation,omitempty"`
	Timestamp   time.Time           `json:"timestamp" yaml:"timestamp"`
}
