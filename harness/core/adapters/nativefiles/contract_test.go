package nativefiles

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestDuplicateInputsRefuseWithoutValues(t *testing.T) {
	ctx := Context{Provider: "codex", Mode: "per-turn", Concern: "mcp"}
	for _, servers := range [][]Server{
		{{Name: "x", Command: "a"}, {Name: "x", HTTPURL: "http://example.invalid"}},
		{{Name: "x", Command: "a", HTTPURL: "http://example.invalid"}},
		{{Name: "x", Command: "a", Env: []Variable{{Name: "A", Value: "SECRET"}, {Name: "A", Value: "SECRET"}}}},
	} {
		var refusal *Refusal
		if err := ValidateServers(ctx, servers); !errors.As(err, &refusal) || refusal.Provider != ctx.Provider || refusal.Mode != ctx.Mode || refusal.Concern != ctx.Concern {
			t.Fatalf("expected contextual refusal, got %v", err)
		}
	}
	for _, slots := range [][]Slot{
		{{Key: "x", Value: 1}, {Key: "x", Value: 2}},
		{{Key: "x", Value: json.RawMessage(`{"a":1,"a":2}`)}},
		{{Key: "x", Value: json.RawMessage(`{"a":{"b":1,"b":2}}`)}},
	} {
		if _, err := Object(ctx, slots); err == nil {
			t.Fatal("duplicate accepted")
		}
	}
}
func TestExplicitComposition(t *testing.T) {
	ctx := Context{Provider: "codex", Concern: "settings"}
	for _, claims := range [][]Claim{
		{{Path: "config", Owner: "codex"}, {Path: "config", Owner: "codex"}},
		{{Path: "config", Owner: "codex", Composition: "config"}, {Path: "config", Owner: "overlay", Composition: "config"}},
	} {
		if err := ValidateComposition(ctx, claims); err == nil {
			t.Fatal("implicit or cross-owner composition accepted")
		}
	}
	if err := ValidateComposition(ctx, []Claim{{Path: "config", Owner: "codex", Composition: "native"}, {Path: "config", Owner: "codex", Composition: "native"}}); err != nil {
		t.Fatal(err)
	}
}
func TestObjectMergesDisjointSlots(t *testing.T) {
	ctx := Context{Provider: "claude"}
	doc, err := Object(ctx, []Slot{{Key: "permissions", Value: map[string]any{"allow": []string{"Read"}}}}, []Slot{{Key: "permissions", Value: map[string]any{"defaultMode": "plan"}}})
	if err != nil || len(doc["permissions"].(map[string]any)) != 2 {
		t.Fatalf("%v %v", doc, err)
	}
	if _, err := Object(ctx, []Slot{{Key: "permissions", Value: map[string]any{"defaultMode": "plan"}}}, []Slot{{Key: "permissions", Value: map[string]any{"defaultMode": "default"}}}); err == nil {
		t.Fatal("conflicting native slot accepted")
	}
}
