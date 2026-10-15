package permission

import (
	"context"
	"errors"
	"testing"
)

func TestBuiltInProfileBindings(t *testing.T) {
	for _, c := range []struct {
		name  string
		mode  Mode
		skips bool
	}{{"default", ModeDefault, false}, {"plan", ModePlan, false}, {"accept-edits", ModeAcceptEdits, false}, {"yolo", ModeYolo, true}, {"catalog-auto", ModeYolo, true}} {
		got, err := BindProfile(c.name, Ceiling{Modes: []Mode{c.mode}})
		if err != nil || got.Mode != c.mode || got.Name != c.name || got.SkipsDenyRules != c.skips || got.Version != "harness-permission-profiles-v2" {
			t.Fatalf("%s: %+v %v", c.name, got, err)
		}
	}
}
func TestProfileRefusals(t *testing.T) {
	for _, name := range []string{"YOLO", "unknown", "default ", "auto", "CATALOG-AUTO", "catalog-auto "} {
		_, err := BindProfile(name, Ceiling{Modes: []Mode{ModeYolo, ModeDefault}})
		var pe *ProfileError
		if !errors.As(err, &pe) || pe.Code != "unknown_permission_profile" {
			t.Fatalf("%q: %v", name, err)
		}
	}
	for _, name := range []string{"yolo", "catalog-auto"} {
		for _, allowed := range [][]Mode{nil, {ModePlan}, {ModeDefault, ModeAcceptEdits}} {
			_, err := BindProfile(name, Ceiling{Modes: allowed})
			var pe *ProfileError
			if !errors.As(err, &pe) || pe.Code != "permission_ceiling" {
				t.Fatal(err)
			}
		}
	}
	_, err := BindProfile("default", Ceiling{Modes: []Mode{ModePlan}})
	if err == nil {
		t.Fatal("silently downgraded profile")
	}
}
func TestYoloEvidenceMatchesEngine(t *testing.T) {
	for _, name := range []string{"yolo", "catalog-auto"} {
		b, err := BindProfile(name, Ceiling{Modes: []Mode{ModeYolo}})
		if err != nil {
			t.Fatal(err)
		}
		e := NewEngine(b.Mode, &RuleSet{Rules: []Rule{{Tool: "*", Behavior: DecisionDeny, Pattern: "**"}}})
		r := e.Check(context.Background(), "session", "write", nil, ToolMeta{IsDestructive: true})
		if r.Decision != DecisionAllow || !b.SkipsDenyRules {
			t.Fatalf("%s must not claim deny enforcement: %+v", name, r)
		}
	}
	all := ProfileBindings()
	all[0].Name = "changed"
	b, err := BindProfile("default", Ceiling{Modes: []Mode{ModeDefault}})
	if err != nil || b.Name != "default" {
		t.Fatal("binding alias")
	}
}
