package llmtypes

import "testing"

func TestIsTurnComplete(t *testing.T) {
	if !IsTurnComplete(StreamEvent{Type: EventDone}) {
		t.Fatal("EventDone should be terminal")
	}
	if !IsTurnComplete(StreamEvent{Type: EventError}) {
		t.Fatal("EventError should be terminal")
	}
	if IsTurnComplete(StreamEvent{Type: EventDelta}) {
		t.Fatal("EventDelta should not be terminal")
	}
}

func TestEffectiveSystemPromptNoSlots(t *testing.T) {
	req := ChatRequest{SystemPrompt: "system"}
	if got := req.EffectiveSystemPrompt(); got != "system" {
		t.Fatalf("EffectiveSystemPrompt() = %q, want %q", got, "system")
	}
}

func TestChatRequestCacheHints(t *testing.T) {
	req := ChatRequest{
		CacheHints: []CacheHint{
			{Position: "system", Index: 0},
			{Position: "recent_message", Index: 1},
		},
	}
	if len(req.CacheHints) != 2 {
		t.Fatalf("CacheHints len = %d, want 2", len(req.CacheHints))
	}
	if req.CacheHints[0].Position != "system" || req.CacheHints[1].Index != 1 {
		t.Fatalf("CacheHints not preserved: %#v", req.CacheHints)
	}
}

func TestEffectiveSystemPromptWithSlots(t *testing.T) {
	req := ChatRequest{
		SystemPrompt: "system",
		SlotBlocks: []SlotBlock{
			{Name: "a", Content: "slot-a"},
			{Name: "b", Content: ""},
			{Name: "c", Content: "slot-c"},
		},
	}
	want := "system\n\nslot-a\n\nslot-c"
	if got := req.EffectiveSystemPrompt(); got != want {
		t.Fatalf("EffectiveSystemPrompt() = %q, want %q", got, want)
	}
}

func TestUsageCostUSDZeroValueMeansNoCostReported(t *testing.T) {
	var u Usage
	if u.CostUSD != 0 {
		t.Fatalf("zero Usage.CostUSD = %v, want 0", u.CostUSD)
	}
	u = Usage{OutputTokens: 4, CostUSD: 0.016}
	if u.CostUSD != 0.016 {
		t.Fatalf("CostUSD = %v", u.CostUSD)
	}
}

func TestNormalizeStopReason(t *testing.T) {
	cases := map[string]string{
		// Anthropic / Claude
		"end_turn":      StopReasonEndTurn,
		"stop_sequence": StopReasonEndTurn,
		"max_tokens":    StopReasonMaxTokens,
		"tool_use":      StopReasonToolUse,
		"refusal":       StopReasonRefusal,
		// OpenAI chat / responses, Codex
		"stop":              StopReasonEndTurn,
		"length":            StopReasonMaxTokens,
		"max_output_tokens": StopReasonMaxTokens,
		"tool_calls":        StopReasonToolUse,
		// OpenCode step reasons
		"tool-calls": StopReasonToolUse,
		// ACP stopReason
		"max_turn_requests": StopReasonTurnLimit,
		"cancelled":         StopReasonCancelled,
		// near spellings, case and whitespace
		"canceled":   StopReasonCancelled,
		" End_Turn ": StopReasonEndTurn,
		"Max Tokens": StopReasonMaxTokens,
		"error":      StopReasonError,
		// outside the vocabulary: passed through, trimmed
		"pause_turn":  "pause_turn",
		" weird-one ": "weird-one",
		"":            "",
	}
	for raw, want := range cases {
		if got := NormalizeStopReason(raw); got != want {
			t.Errorf("NormalizeStopReason(%q) = %q, want %q", raw, got, want)
		}
	}
}

// Normalising is idempotent: a normalised value maps to itself.
func TestNormalizeStopReasonIsIdempotent(t *testing.T) {
	for _, v := range []string{StopReasonEndTurn, StopReasonMaxTokens, StopReasonToolUse, StopReasonTurnLimit, StopReasonRefusal, StopReasonCancelled, StopReasonError} {
		if got := NormalizeStopReason(v); got != v {
			t.Errorf("NormalizeStopReason(%q) = %q, want it unchanged", v, got)
		}
	}
}

// One spelling end to end: the phase value is the wire's "thought".
func TestPhaseValues(t *testing.T) {
	if PhaseThinking != "thought" || PhaseNarration != "narration" || PhaseFinal != "final" {
		t.Errorf("phases = %q %q %q", PhaseThinking, PhaseNarration, PhaseFinal)
	}
}
