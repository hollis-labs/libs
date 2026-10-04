package nativefiles

import (
	"encoding/json"
	"errors"
	"github.com/hollis-labs/substrate/harness/adapters/internal/owner"
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
	ctx := Context{Provider: "codex", Mode: "subprocess-per-turn", Concern: "composition"}
	real := owner.New("codex")
	for _, claims := range [][]Claim{
		{SerializerClaim("config.toml", real, ""), SerializerClaim("./config.toml", real, "")},
		{SerializerClaim("config.toml", real, "native"), OverlayClaim(Overlay{Path: "config.toml", Owner: real})},
		{SerializerClaim("config.toml", Owner{}, "native")},
		{Claim{}},
	} {
		var d *Refusal
		if err := ValidateComposition(ctx, claims, nil); !errors.As(err, &d) || d.Provider != ctx.Provider || d.Mode != ctx.Mode || d.Concern != ctx.Concern || d.Reason == "" {
			t.Fatalf("expected contextual refusal: %v", err)
		}
	}
	for _, rel := range []string{"config.toml", "opencode.json", ".claude/settings.json", ".mcp.json", ".agents/plugins/tether/plugin.json", "auth.json", ".materialize/manifest.json"} {
		if err := ValidateComposition(ctx, []Claim{OverlayClaim(Overlay{Path: "./" + rel, Owner: real})}, []string{rel}); err == nil {
			t.Errorf("lone overlay accepted for %s", rel)
		}
	}
	if err := ValidateComposition(ctx, []Claim{SerializerClaim("config.toml", real, "native"), SerializerClaim("./config.toml", real, "native")}, []string{"config.toml"}); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"/config.toml", "../config.toml", "a/../config.toml"} {
		if err := ValidateComposition(ctx, []Claim{SerializerClaim(rel, real, "native")}, nil); err == nil {
			t.Errorf("unsafe path accepted: %s", rel)
		}
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
