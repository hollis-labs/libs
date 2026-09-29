package agentcontracts

// Limits bounds a run. A nil field means "no limit stated here"; the host's own
// maximum, which bounds a caller-supplied limit, is not this package's concern.
type Limits struct {
	MaxDurationMs *int64   `json:"max_duration_ms,omitempty" yaml:"max_duration_ms,omitempty"`
	IdleTimeoutMs *int64   `json:"idle_timeout_ms,omitempty" yaml:"idle_timeout_ms,omitempty"`
	CostBudget    *float64 `json:"cost_budget,omitempty" yaml:"cost_budget,omitempty"`
	TokenBudget   *int64   `json:"token_budget,omitempty" yaml:"token_budget,omitempty"`
	MaxTurns      *int     `json:"max_turns,omitempty" yaml:"max_turns,omitempty"`
	MaxRetries    *int     `json:"max_retries,omitempty" yaml:"max_retries,omitempty"`
}
