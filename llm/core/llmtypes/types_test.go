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
