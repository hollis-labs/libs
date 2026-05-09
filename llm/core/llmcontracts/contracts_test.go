package llmcontracts

import (
	"context"
	"reflect"
	"testing"

	"github.com/hollis-labs/go-llm-types"
)

type testRateLimited struct{}

func (testRateLimited) RateLimitTPM() int { return 0 }

type testCacheableProvider struct{}

func (testCacheableProvider) SetCacheHints([]CacheHint) {}

type testCacheable struct{}

func (testCacheable) EstimateCacheablePrefix(context.Context, llmtypes.ChatRequest) int { return 0 }

var (
	_ RateLimited       = testRateLimited{}
	_ CacheableProvider = testCacheableProvider{}
	_ Cacheable         = testCacheable{}
)

func TestDefaultCacheStrategy(t *testing.T) {
	want := []CacheHint{
		{Position: "system", Index: 0},
		{Position: "tools", Index: 0},
		{Position: "recent_message", Index: 0},
		{Position: "recent_message", Index: 1},
	}
	if got := DefaultCacheStrategy(); !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultCacheStrategy() mismatch: got %#v want %#v", got, want)
	}
}

func TestReasoningConfigRoundTrip(t *testing.T) {
	cfg := ReasoningConfig{
		Enabled:      true,
		BudgetTokens: 2048,
		BetasHeader:  "interleaved-thinking-2025-05-14",
	}

	got := ReasoningConfigFromContext(WithReasoningConfig(context.Background(), cfg))
	if got != cfg {
		t.Fatalf("ReasoningConfigFromContext() mismatch: got %#v want %#v", got, cfg)
	}
}

func TestReasoningConfigFromContextMissing(t *testing.T) {
	if got := ReasoningConfigFromContext(context.Background()); got != (ReasoningConfig{}) {
		t.Fatalf("expected zero-value config, got %#v", got)
	}
}
