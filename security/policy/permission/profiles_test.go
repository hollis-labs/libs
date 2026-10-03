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
	}{{"default", ModeDefault, false}, {"plan", ModePlan, false}, {"accept-edits", ModeAcceptEdits, false}, {"yolo", ModeYolo, true}} {
		got, err := BindProfile(c.name, []Mode{c.mode})
		if err != nil || got.Mode != c.mode || got.Name != c.name || got.SkipsDenyRules != c.skips || got.Version == "" {
			t.Fatalf("%s: %+v %v", c.name, got, err)
		}
	}
}
func TestProfileRefusals(t *testing.T) {
	for _, name := range []string{"", "YOLO", "unknown", "default "} {
		_, err := BindProfile(name, []Mode{ModeYolo, ModeDefault})
		var pe *ProfileError
		if !errors.As(err, &pe) || pe.Code != "unknown_permission_profile" {
			t.Fatalf("%q: %v", name, err)
		}
	}
	for _, allowed := range [][]Mode{nil, {ModePlan}, {ModeDefault, ModeAcceptEdits}} {
		_, err := BindProfile("yolo", allowed)
		var pe *ProfileError
		if !errors.As(err, &pe) || pe.Code != "permission_ceiling" {
			t.Fatal(err)
		}
	}
	_, err := BindProfile("default", []Mode{ModePlan})
	if err == nil {
		t.Fatal("silently downgraded profile")
	}
}
func TestYoloEvidenceMatchesEngine(t *testing.T) {
	b, err := BindProfile("yolo", []Mode{ModeYolo})
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(b.Mode, &RuleSet{Rules: []Rule{{Tool: "*", Behavior: DecisionDeny, Pattern: "**"}}})
	r := e.Check(context.Background(), "session", "write", nil, ToolMeta{IsDestructive: true})
	if r.Decision != DecisionAllow || !b.SkipsDenyRules {
		t.Fatalf("must not claim deny enforcement: %+v", r)
	}
	all := ProfileBindings()
	all[0].Name = "changed"
	b, err = BindProfile("default", []Mode{ModeDefault})
	if err != nil || b.Name != "default" {
		t.Fatal("binding alias")
	}
}
