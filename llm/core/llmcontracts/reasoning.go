package llmcontracts

import "context"

// ReasoningConfig carries per-turn reasoning/thinking configuration for
// providers that support it (e.g. Anthropic interleaved thinking).
// Callers inject this via WithReasoningConfig before calling StreamChat.
// F3 / CW-20260420-0023.
type ReasoningConfig struct {
	// Enabled reports whether reasoning/thinking blocks should be requested.
	Enabled bool
	// BudgetTokens is the token budget for the reasoning pass. Required
	// (must be > 0) for reasoning to actually be requested. With Enabled=true
	// and BudgetTokens=0, providers will not send a reasoning request — the
	// pair must be set explicitly.
	BudgetTokens int
	// BetasHeader is an additional beta header value to append (e.g.
	// "interleaved-thinking-2025-05-14"). Empty means no extra flag.
	BetasHeader string
}

type reasoningConfigKeyType struct{}

// WithReasoningConfig returns a context carrying the given ReasoningConfig.
// The Anthropic adapter reads this to decide whether to send the
// interleaved-thinking beta header and thinking_config parameter.
func WithReasoningConfig(ctx context.Context, cfg ReasoningConfig) context.Context {
	return context.WithValue(ctx, reasoningConfigKeyType{}, cfg)
}

// ReasoningConfigFromContext extracts the ReasoningConfig from ctx, if set.
// Returns zero-value ReasoningConfig (Enabled=false) when not set.
func ReasoningConfigFromContext(ctx context.Context) ReasoningConfig {
	cfg, _ := ctx.Value(reasoningConfigKeyType{}).(ReasoningConfig)
	return cfg
}
