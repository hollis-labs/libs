package desk

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestScreen(t *testing.T) {
	for _, tc := range []struct {
		msg   string
		multi bool
		task  bool
		parts int
	}{
		{"what time is it?", false, false, 0},
		{"thanks, that works", false, false, 0},
		{"Can you research how Codex compacts? And what does Claude do?", true, true, 2},
		{"Please:\n1. fix the tether stall\n2. look into the nanite trace gap\n3. tell me when both are done", true, true, 3},
		{"- one\n- two", true, false, 2},
		{"Draft the release notes", false, true, 0},
		{"ship it, and also ping the release seat", true, false, 0},
		{"Ask the tether agent to restart the mailer", false, true, 0},
	} {
		s := Screen(tc.msg)
		if s.MultiPart != tc.multi || s.Task != tc.task || len(s.Parts) != tc.parts {
			t.Errorf("%q: %+v", tc.msg, s)
		}
		if s.Worth() != (tc.multi || tc.task) {
			t.Errorf("%q: Worth %v", tc.msg, s.Worth())
		}
	}
}

func TestRulesDecomposer(t *testing.T) {
	p, err := Rules{}.Decompose(t.Context(), Request{Message: "1. research the stall\n2. ask the tether agent to restart\n3. compare the two traces\n4. hello"})
	if err != nil {
		t.Fatal(err)
	}
	kinds := []WorkKind{KindResearch, KindDelegate, KindAnalysis, KindAnswer}
	if len(p.Items) != 4 || p.Source != "rules" || p.Validate() != nil {
		t.Fatalf("plan %+v", p)
	}
	for i, k := range kinds {
		if p.Items[i].Kind != k {
			t.Errorf("item %d kind %s, want %s", i, p.Items[i].Kind, k)
		}
	}
	single, _ := Rules{}.Decompose(t.Context(), Request{Message: "investigate the flaky test"})
	if len(single.Items) != 1 || single.Items[0].Kind != KindResearch || single.Trivial() {
		t.Fatalf("single task %+v", single)
	}
}

type scripted struct {
	answers []string
	err     error
	calls   []string
}

func (s *scripted) complete(_ context.Context, system, user string) (string, error) {
	s.calls = append(s.calls, user)
	if s.err != nil {
		return "", s.err
	}
	if len(s.answers) == 0 {
		return "", errors.New("no more answers")
	}
	a := s.answers[0]
	s.answers = s.answers[1:]
	return a, nil
}

const twoResearch = "```json\n" + `{"topic":"t1","items":[{"id":"1","kind":"research","goal":"how Codex compacts"},{"id":"2","kind":"research","goal":"how Claude Code compacts"},{"id":"3","kind":"analysis","goal":"compare","depends_on":["1","2"]}],"confidence":0.9,"why":"two lookups and a comparison"}` + "\n```"

func TestModelDecomposer(t *testing.T) {
	s := &scripted{answers: []string{twoResearch}}
	req := Request{Message: "compare how codex and claude compact", Mode: "research", Topics: []Topic{{ID: "t1", Title: "Compaction"}}}
	p, err := Model{Complete: s.complete, Instructions: "Seats: tether, nanite."}.Decompose(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if p.Source != "model" || p.Topic != "t1" || len(p.Items) != 3 || p.Message != req.Message || p.Mode != "research" {
		t.Fatalf("plan %+v", p)
	}
	if !strings.Contains(s.calls[0], "- t1: Compaction (t1)") || !strings.Contains(s.calls[0], "Mode: research") {
		t.Fatalf("input:\n%s", s.calls[0])
	}
	if !strings.Contains(PlannerPrompt("Seats: tether, nanite."), "Seats: tether, nanite.") {
		t.Fatal("instructions not in the prompt")
	}

	// An unknown topic is dropped, not trusted.
	s = &scripted{answers: []string{strings.Replace(twoResearch, `"t1"`, `"t9"`, 1)}}
	p, err = Model{Complete: s.complete}.Decompose(t.Context(), req)
	if err != nil || p.Topic != "" {
		t.Fatalf("unknown topic kept: %q %v", p.Topic, err)
	}

	// An invalid answer is retried once with the error.
	s = &scripted{answers: []string{`{"items":[{"id":"1","kind":"dance","goal":"x"}],"confidence":0.5}`, twoResearch}}
	p, err = Model{Complete: s.complete}.Decompose(t.Context(), req)
	if err != nil || len(s.calls) != 2 || !strings.Contains(s.calls[1], `unknown kind "dance"`) || len(p.Items) != 3 {
		t.Fatalf("retry: %v, calls %d", err, len(s.calls))
	}
	s = &scripted{answers: []string{"not json", "still not json"}}
	if _, err := (Model{Complete: s.complete}).Decompose(t.Context(), req); err == nil {
		t.Fatal("two bad answers accepted")
	}
	if _, err := (Model{}).Decompose(t.Context(), req); err == nil {
		t.Fatal("nil Complete accepted")
	}
}

func TestIntakeDispositions(t *testing.T) {
	multi := "research how codex compacts and also how claude does"
	model := func(answer string) Decomposer {
		return Model{Complete: (&scripted{answers: []string{answer}}).complete}
	}
	trivial := `{"items":[{"id":"1","kind":"answer","goal":"answer it"}],"confidence":0.95}`
	low := strings.Replace(twoResearch, "0.9", "0.5", 1)
	for _, tc := range []struct {
		name    string
		in      Intake
		msg     string
		want    Disposition
		planned bool
		errSet  bool
	}{
		{"off", Intake{Mode: IntakeOff, Planner: model(twoResearch)}, multi, Chat, false, false},
		{"empty mode is off", Intake{}, multi, Chat, false, false},
		{"plain message skips the planner", Intake{Mode: IntakeAuto, Planner: model(twoResearch)}, "hi there", Chat, false, false},
		{"shadow plans but chats", Intake{Mode: IntakeShadow, Planner: model(twoResearch)}, multi, Chat, true, false},
		{"suggest", Intake{Mode: IntakeSuggest, Planner: model(twoResearch)}, multi, Suggest, true, false},
		{"trivial plan chats", Intake{Mode: IntakeSuggest, Planner: model(trivial)}, multi, Chat, true, false},
		{"auto confident", Intake{Mode: IntakeAuto, Planner: model(twoResearch)}, multi, Dispatch, true, false},
		{"auto unsure suggests", Intake{Mode: IntakeAuto, Planner: model(low)}, multi, Suggest, true, false},
		{"auto threshold", Intake{Mode: IntakeAuto, AutoConfidence: 0.4, Planner: model(low)}, multi, Dispatch, true, false},
		{"model failure falls back and suggests", Intake{Mode: IntakeAuto, Planner: Model{Complete: (&scripted{err: errors.New("down")}).complete}}, "1. research a\n2. research b", Suggest, true, true},
		{"rules only", Intake{Mode: IntakeSuggest}, "1. research a\n2. research b", Suggest, true, false},
	} {
		r, err := tc.in.Read(t.Context(), Request{Message: tc.msg})
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if r.Disposition != tc.want || r.Planned != tc.planned || (r.Err != "") != tc.errSet {
			t.Errorf("%s: %+v", tc.name, r)
		}
		if r.Plan.Validate() != nil {
			t.Errorf("%s: invalid plan %v", tc.name, r.Plan.Validate())
		}
	}
	if _, err := (Intake{Mode: "loud"}).Read(t.Context(), Request{Message: "x"}); err == nil {
		t.Fatal("unknown mode accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	failing := Intake{Mode: IntakeSuggest, Planner: Model{Complete: func(ctx context.Context, _, _ string) (string, error) { return "", ctx.Err() }}}
	if _, err := failing.Read(ctx, Request{Message: multi}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	c := Correction{Proposed: Plan{}, Final: &Plan{}, Edited: true}
	if !c.Accepted() || (Correction{}).Accepted() {
		t.Fatal("Correction.Accepted")
	}
}
