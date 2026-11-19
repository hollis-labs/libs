package classify

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/hollis-labs/libs/core/util/decide"
)

func TestDecisionJudgeAsksThePackAndVotesItsAnswer(t *testing.T) {
	c, _ := Compile(mustPack(t))
	var asked decide.Request
	d := decide.Scripted{ID: "jev", Fn: func(_ context.Context, req decide.Request) (decide.Response, error) {
		asked = req
		return decide.Response{Model: "jev-1.13.0", Answers: map[string]decide.Answer{"act": {Type: decide.Choice, Choice: "task",
			Probabilities: map[string]float64{"task": 0.85, "question": 0.05, "command": 0.05, "approval": 0.05}}}}, nil
	}}
	j, err := NewDecision("decide:act", d, c, DecisionOptions{Purpose: "classify", Model: "jev-1.13.0"})
	if err != nil {
		t.Fatal(err)
	}
	e, _ := NewEngine(c, j)
	text := "hmm, the token " + "sk" + "-" + strings.Repeat("z", 24) + " broke it" // built so scanners pass it
	if res := e.Classify(context.Background(), Input{Text: text, Phase: Submit}); len(res.Facets["act"].Votes) != 0 {
		t.Fatal("an async judge ran at submit")
	}
	res := e.Classify(context.Background(), Input{Text: text, Phase: Async})
	if res.Label("act") != "task" {
		t.Fatalf("act %+v", res.Facets["act"])
	}
	if strings.Contains(asked.State.(map[string]any)["text"].(string), "zzzz") {
		t.Fatal("the text was not scrubbed before it left")
	}
	q := asked.Questions[0]
	if q.Type != decide.Choice || len(q.Options) != 4 || asked.Purpose != "classify" {
		t.Fatalf("question %+v", q)
	}
	for _, o := range q.Options {
		if o.Key == "question" && !strings.Contains(o.Description, `"what is open?"`) {
			t.Errorf("examples missing from %q", o.Description)
		}
	}
	for _, v := range res.Facets["act"].Votes {
		if v.Judge != "decide:act" {
			continue
		}
		want := math.Log(0.85 * 4)
		if v.Label == "task" && math.Abs(v.Weight-want) > 1e-9 {
			t.Errorf("task vote %v, want %v", v.Weight, want)
		}
		if v.Label == "question" && v.Weight >= 0 {
			t.Errorf("a below-uniform label voted for: %v", v.Weight)
		}
	}
	// The same text is asked the same way; option order varies with the text.
	again := j.Request(Input{Text: text})
	if again.Questions[0].Options[0].Key != q.Options[0].Key {
		t.Fatal("rotation is not deterministic")
	}
	orders := map[string]bool{}
	for _, s := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		orders[j.Request(Input{Text: s}).Questions[0].Options[0].Key] = true
	}
	if len(orders) < 2 {
		t.Fatal("option order never rotates")
	}
}

func TestDecisionJudgeFailureIsAFailedJudge(t *testing.T) {
	c, _ := Compile(mustPack(t))
	d := decide.Scripted{Fn: func(context.Context, decide.Request) (decide.Response, error) {
		return decide.Response{}, errors.New("gateway down")
	}}
	j, _ := NewDecision("decide:act", d, c, DecisionOptions{Purpose: "classify"})
	e, _ := NewEngine(c, j)
	res := e.Classify(context.Background(), Input{Text: "what now?", Phase: Async})
	if len(res.Failed) != 1 || res.Label("act") != "question" {
		t.Fatalf("result %+v", res)
	}
	if _, err := NewDecision("x", d, c, DecisionOptions{}); !errors.Is(err, decide.ErrNoPurpose) {
		t.Fatal("no purpose accepted")
	}
	if _, err := NewDecision("x", d, c, DecisionOptions{Purpose: "p"}, "nope"); err == nil {
		t.Fatal("unknown facet accepted")
	}
}

func affectEngine(t *testing.T) *Engine {
	t.Helper()
	c, err := Compile(Pack{Name: "affect", Version: "1", Facets: []FacetSpec{AffectFacet()}})
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(c, NewAffect("affect", "affect"))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestAffect(t *testing.T) {
	e := affectEngine(t)
	for text, want := range map[string]string{
		"open the notes window":              AffectNeutral,
		"why is this STILL broken?!":         AffectFrustrated,
		"ugh, wrong file again":              AffectFrustrated,
		"prod is down, need this fixed asap": AffectUrgent,
		"thanks, that's perfect":             AffectPositive,
		"that is not great":                  AffectFrustrated,
		"WHY":                                AffectFrustrated,
	} {
		fr := e.Classify(context.Background(), Input{Text: text}).Facets["affect"]
		if fr.Top != want {
			t.Errorf("%q: top %q (p=%.2f %v), want %q", text, fr.Top, fr.P, fr.Probabilities, want)
		}
	}
}

func TestFitTemperatureLowersTheLoss(t *testing.T) {
	// Rules that are right but say so too loudly: the fit should cool them.
	p := Pack{Name: "t", Facets: []FacetSpec{{Name: "f", Labels: []LabelSpec{{Name: "a"}, {Name: "b"}},
		Rules: []RuleSpec{{ID: "x", Label: "a", Words: []string{"x"}, Weight: 8}}}}}
	c, _ := Compile(p)
	e, _ := NewEngine(c)
	var xs []Example
	for i := 0; i < 30; i++ {
		gold := "a"
		if i%3 == 0 {
			gold = "b" // the rule is wrong a third of the time
		}
		xs = append(xs, Example{Text: "x", Labels: map[string]string{"f": gold}})
	}
	cal, err := FitTemperature(context.Background(), e, Keystroke, "f", xs)
	if err != nil {
		t.Fatal(err)
	}
	if cal.Temperature <= 1 || cal.After >= cal.Before || cal.N != 30 {
		t.Fatalf("calibration %+v", cal)
	}
	// At the fitted temperature p(a) is near the observed 2/3.
	cooled, _ := Compile(Layer(p, Pack{Facets: []FacetSpec{{Name: "f", Temperature: cal.Temperature}}}))
	e2, _ := NewEngine(cooled)
	if pa := e2.Classify(context.Background(), Input{Text: "x"}).Facets["f"].P; math.Abs(pa-2.0/3) > 0.02 {
		t.Fatalf("calibrated p %v", pa)
	}
	if _, err := FitTemperature(context.Background(), e, Keystroke, "nope", xs); err == nil {
		t.Fatal("unknown facet")
	}
}
