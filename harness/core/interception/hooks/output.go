package hooks

import "fmt"

// Decision is a hook's verdict. Its values mirror go-permission's Decision
// string for string ("allow", "deny", "ask") without importing it. Claude
// Code's native wording (for example "block" on UserPromptSubmit) is
// normalized into this vocabulary by the host's adapter, not by this
// package. Not every event honors Decision; see the README event catalog.
type Decision string

// The three decision values.
const (
	DecisionAllow Decision = "allow"
	DecisionDeny  Decision = "deny"
	DecisionAsk   Decision = "ask"
)

// Valid reports whether d is one of the three decision values. The empty
// Decision is not valid; Output.Validate treats it as "unset".
func (d Decision) Valid() bool {
	switch d {
	case DecisionAllow, DecisionDeny, DecisionAsk:
		return true
	}
	return false
}

// Output is the single decoded shape for whatever a hook prints to stdout
// (command kind, exit 0) or returns (mcp_tool kind). One decoder serves all
// eleven events; which fields a given event honors is fixed by the event
// catalog, not by this type.
type Output struct {
	Decision          Decision `json:"decision,omitempty"`
	Reason            string   `json:"reason,omitempty"`
	AdditionalContext string   `json:"additionalContext,omitempty"`
	// UpdatedInput is honored for PreToolUse only. It is carried on Output
	// for every event purely for decoding convenience.
	UpdatedInput  map[string]any `json:"updatedInput,omitempty"`
	SystemMessage string         `json:"systemMessage,omitempty"`
	// Continue is nil when unset; false means stop/block.
	Continue   *bool  `json:"continue,omitempty"`
	StopReason string `json:"stopReason,omitempty"`
}

// Validate reports whether o is well formed: Decision must be empty or one
// of the three values. Unknown JSON fields are tolerated at decode time, but
// an unknown decision word is an error rather than a silent no-op.
func (o Output) Validate() error {
	if o.Decision != "" && !o.Decision.Valid() {
		return fmt.Errorf("hooks: invalid decision %q (want allow, deny or ask)", string(o.Decision))
	}
	return nil
}
