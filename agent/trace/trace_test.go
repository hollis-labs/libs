package trace

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func validTurn() Turn {
	return Turn{
		Version: Version, At: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC), DurationMS: 1500,
		ConversationID: "c1", RunID: "r1", TurnID: "r1", Role: "chat", Source: SourceUser,
		Provider: "anthropic", Model: "claude-sonnet-5-5",
		Slots:        []Slot{{Name: "universal", Tokens: 600}, {Name: "agent", Tokens: 40, Changed: true}},
		Trimmed:      []string{},
		ToolsOffered: []string{"read_file", "write_file"},
		ToolCalls:    []ToolCall{{ToolCallID: "t1", Tool: "read_file", DurationMS: 3, Permission: PermissionAllow}},
		Compactions:  []Compaction{{Before: 9000, After: 3000}},
		Usage:        Usage{InputTokens: 100, OutputTokens: 20},
		Outcome:      Outcome{Status: StatusCompleted, FinishReason: "end_turn", Steps: 2},
	}
}

func TestJSONShape(t *testing.T) {
	raw, err := json.Marshal(validTurn())
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"version":1,"at":"2026-11-19T12:00:00Z","duration_ms":1500,"conversation_id":"c1","run_id":"r1","turn_id":"r1","role":"chat","source":"user","provider":"anthropic","model":"claude-sonnet-5-5","slots":[{"name":"universal","tokens":600,"changed":false},{"name":"agent","tokens":40,"changed":true}],"trimmed":[],"tools_offered":["read_file","write_file"],"tool_calls":[{"tool_call_id":"t1","tool":"read_file","duration_ms":3,"permission":"allow"}],"compactions":[{"before":9000,"after":3000}],"usage":{"input_tokens":100,"output_tokens":20,"cache_read_tokens":0,"cache_write_tokens":0},"outcome":{"status":"completed","finish_reason":"end_turn","steps":2}}`
	if string(raw) != want {
		t.Fatalf("JSON shape changed; a field rename needs a new Version.\ngot  %s\nwant %s", raw, want)
	}
	var back Turn
	if err := json.Unmarshal(raw, &back); err != nil || back.Validate() != nil {
		t.Fatalf("round trip: %v, %v", err, back.Validate())
	}
}

func TestValidate(t *testing.T) {
	if err := validTurn().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Turn){
		"version":    func(t *Turn) { t.Version = 2 },
		"at":         func(t *Turn) { t.At = time.Time{} },
		"ids":        func(t *Turn) { t.RunID = "" },
		"duration":   func(t *Turn) { t.DurationMS = -1 },
		"source":     func(t *Turn) { t.Source = "cron" },
		"status":     func(t *Turn) { t.Outcome.Status = "ok" },
		"call id":    func(t *Turn) { t.ToolCalls[0].ToolCallID = "" },
		"permission": func(t *Turn) { t.ToolCalls[0].Permission = "maybe" },
	} {
		turn := validTurn()
		mutate(&turn)
		if err := turn.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestAttributes(t *testing.T) {
	got := map[string]any{}
	for _, a := range validTurn().Attributes() {
		got[a.Key] = a.Value
	}
	if got["gen_ai.request.model"] != "claude-sonnet-5-5" || got["gen_ai.usage.input_tokens"] != 100 || got["agent.turn.tool_calls"] != 1 {
		t.Fatalf("attributes %v", got)
	}
	if reasons, _ := got["gen_ai.response.finish_reasons"].([]string); strings.Join(reasons, ",") != "end_turn" {
		t.Fatalf("finish reasons %v", got["gen_ai.response.finish_reasons"])
	}
}
