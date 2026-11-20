package desk

import (
	"strings"
	"testing"
)

func TestPlanValidate(t *testing.T) {
	good := Plan{Confidence: 0.9, Items: []PlanItem{
		{ID: "a", Kind: KindResearch, Goal: "find x"},
		{ID: "b", Kind: KindResearch, Goal: "find y"},
		{ID: "c", Kind: KindAnalysis, Goal: "compare", DependsOn: []string{"a", "b"}, Delivery: Now},
	}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]Plan{
		"empty":          {},
		"no id":          {Items: []PlanItem{{Kind: KindAnswer, Goal: "g"}}},
		"repeat id":      {Items: []PlanItem{{ID: "a", Kind: KindAnswer, Goal: "g"}, {ID: "a", Kind: KindAnswer, Goal: "g"}}},
		"bad kind":       {Items: []PlanItem{{ID: "a", Kind: "dance", Goal: "g"}}},
		"no goal":        {Items: []PlanItem{{ID: "a", Kind: KindAnswer, Goal: "  "}}},
		"bad delivery":   {Items: []PlanItem{{ID: "a", Kind: KindAnswer, Goal: "g", Delivery: "shout"}}},
		"unknown dep":    {Items: []PlanItem{{ID: "a", Kind: KindAnswer, Goal: "g", DependsOn: []string{"z"}}}},
		"cycle":          {Items: []PlanItem{{ID: "a", Kind: KindAnswer, Goal: "g", DependsOn: []string{"b"}}, {ID: "b", Kind: KindAnswer, Goal: "g", DependsOn: []string{"a"}}}},
		"bad confidence": {Confidence: 1.5, Items: []PlanItem{{ID: "a", Kind: KindAnswer, Goal: "g"}}},
	} {
		if err := p.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPlanWavesAndCard(t *testing.T) {
	p := Plan{TopicTitle: "Release", Items: []PlanItem{
		{ID: "q", Kind: KindQuestion, Goal: "which version?"},
		{ID: "r1", Kind: KindResearch, Goal: "list changes"},
		{ID: "d", Kind: KindDelegate, Goal: "tag it", DependsOn: []string{"q", "r1"}, Target: "release seat"},
	}}
	waves, err := p.Waves()
	if err != nil || len(waves) != 2 || len(waves[0]) != 2 || waves[1][0].ID != "d" {
		t.Fatalf("waves %v, %v", waves, err)
	}
	card := p.Card()
	for _, want := range []string{"Plan: 3 items in 2 steps (topic: Release)", "1. [question] which version?", "2. [delegate] tag it → release seat"} {
		if !strings.Contains(card, want) {
			t.Errorf("card lacks %q:\n%s", want, card)
		}
	}
	par := Plan{Items: []PlanItem{{ID: "a", Kind: KindResearch, Goal: "x"}, {ID: "b", Kind: KindResearch, Goal: "y"}}}
	if !strings.HasPrefix(par.Card(), "Plan: 2 items in parallel\n- [research] x") {
		t.Fatalf("parallel card:\n%s", par.Card())
	}
	if !(Plan{Items: []PlanItem{{ID: "1", Kind: KindAnswer, Goal: "hi"}}}).Trivial() || par.Trivial() {
		t.Fatal("Trivial")
	}
}
