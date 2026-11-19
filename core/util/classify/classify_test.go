package classify

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/libs/core/util/ledger"
)

const actPack = `{
  "name": "test", "version": "1",
  "facets": [{
    "name": "act", "min_p": 0.5, "min_margin": 0.1,
    "labels": [
      {"name": "command", "examples": ["/open notes", "/layout focus"]},
      {"name": "question", "examples": ["what is open?", "how do I pin a slot?", "why did that fail?"]},
      {"name": "task", "examples": ["add a note about the release", "fix the failing test", "open the notes window"]},
      {"name": "approval", "min_p": 0.8, "examples": ["yes go ahead", "lgtm", "approve it"]}
    ],
    "rules": [
      {"id": "slash", "label": "command", "prefix": "/", "weight": 5, "decisive": true},
      {"id": "qmark", "label": "question", "pattern": "\\?$", "weight": 2},
      {"id": "wh", "label": "question", "words": ["what", "why", "how", "where"], "at_start": true, "weight": 1.5},
      {"id": "verb", "label": "task", "words": ["add", "fix", "open", "create"], "at_start": true, "weight": 1.5},
      {"id": "yes", "label": "approval", "words": ["yes", "lgtm", "approve"], "at_start": true, "weight": 2},
      {"id": "review-mode", "label": "approval", "when": [{"field": "mode", "op": "eq", "value": "review"}], "weight": 1}
    ]
  }]
}`

func mustEngine(t *testing.T, src string, extra ...Judge) *Engine {
	t.Helper()
	p, err := ParsePack([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	c, err := Compile(p)
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(c, extra...)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestRulesVoteAndCombine(t *testing.T) {
	e := mustEngine(t, actPack)
	ctx := context.Background()
	for text, want := range map[string]string{
		"/open notes":           "command",
		"what is in front?":     "question",
		"fix the parser please": "task",
		"so, hmm":               "", // nothing voted: abstain
	} {
		got := e.Classify(ctx, Input{Text: text}).Facets["act"]
		if got.Label != want {
			t.Errorf("%q: got %q (top %q p=%.2f), want %q", text, got.Label, got.Top, got.P, want)
		}
	}
}

func TestDecisiveSettlesAndConflictingDecisivesCancel(t *testing.T) {
	e := mustEngine(t, actPack)
	fr := e.Classify(context.Background(), Input{Text: "/what is this?"}).Facets["act"]
	if fr.Label != "command" || fr.Decided == "" || fr.P != 1 {
		t.Fatalf("decisive vote did not settle: %+v", fr)
	}
	two := &staticJudge{name: "other", votes: []Vote{{Facet: "act", Label: "task", Weight: 5, Decisive: true}}}
	e2 := mustEngine(t, actPack, two)
	fr = e2.Classify(context.Background(), Input{Text: "/open"}).Facets["act"]
	if fr.Decided != "" {
		t.Fatalf("two decisive votes for different labels must not settle: %+v", fr)
	}
}

func TestRiskyLabelNeedsMore(t *testing.T) {
	// "yes" (2) alone gives approval p well under its own min_p 0.8.
	e := mustEngine(t, actPack)
	fr := e.Classify(context.Background(), Input{Text: "yes"}).Facets["act"]
	if fr.Top != "approval" || !fr.Abstained {
		t.Fatalf("approval below its min_p must abstain: %+v", fr)
	}
	// With the review-mode prior it clears.
	fr = e.Classify(context.Background(), Input{Text: "yes", Context: map[string]string{"mode": "review"}}).Facets["act"]
	if fr.Top != "approval" {
		t.Fatalf("prior did not add: %+v", fr)
	}
}

func TestPriorVotesBeforeAnythingIsTyped(t *testing.T) {
	e := mustEngine(t, actPack)
	fr := e.Classify(context.Background(), Input{Text: "", Context: map[string]string{"mode": "review"}}).Facets["act"]
	if fr.Top != "approval" || len(fr.Votes) != 1 {
		t.Fatalf("a context rule should vote on empty text: %+v", fr)
	}
}

type staticJudge struct {
	name  string
	phase Phase
	votes []Vote
	err   error
	calls int
}

func (s *staticJudge) Name() string { return s.name }
func (s *staticJudge) Phase() Phase {
	if s.phase == "" {
		return Keystroke
	}
	return s.phase
}
func (s *staticJudge) Judge(context.Context, Input) ([]Vote, error) { s.calls++; return s.votes, s.err }

func TestPhaseGatesJudges(t *testing.T) {
	slow := &staticJudge{name: "slow", phase: Async, votes: []Vote{{Facet: "act", Label: "task", Weight: 9}}}
	e := mustEngine(t, actPack, slow)
	e.Classify(context.Background(), Input{Text: "x", Phase: Submit})
	if slow.calls != 0 {
		t.Fatal("an async judge ran at submit")
	}
	if got := e.Classify(context.Background(), Input{Text: "x", Phase: Async}).Label("act"); got != "task" {
		t.Fatalf("async judge's vote missing: %q", got)
	}
}

func TestFailingJudgeIsRecordedNotFatal(t *testing.T) {
	bad := &staticJudge{name: "bad", err: errors.New("boom"), votes: []Vote{{Facet: "act", Label: "task", Weight: 9}}}
	res := mustEngine(t, actPack, bad).Classify(context.Background(), Input{Text: "what now?"})
	if len(res.Failed) != 1 || res.Failed[0] != "bad" || res.Label("act") != "question" {
		t.Fatalf("got %+v", res)
	}
}

func TestUnknownFacetsLabelsAndNaNAreIgnored(t *testing.T) {
	noisy := &staticJudge{name: "noisy", votes: []Vote{
		{Facet: "nope", Label: "task", Weight: 9},
		{Facet: "act", Label: "nope", Weight: 9},
		{Facet: "act", Label: "task", Weight: math.NaN()},
	}}
	fr := mustEngine(t, actPack, noisy).Classify(context.Background(), Input{Text: "hmm"}).Facets["act"]
	if len(fr.Votes) != 0 || !fr.Abstained {
		t.Fatalf("bad votes leaked: %+v", fr)
	}
}

func TestProbabilitiesSumToOne(t *testing.T) {
	fr := mustEngine(t, actPack).Classify(context.Background(), Input{Text: "how do I fix this?"}).Facets["act"]
	var s float64
	for _, p := range fr.Probabilities {
		s += p
	}
	if math.Abs(s-1) > 1e-9 || len(fr.Probabilities) != 4 {
		t.Fatalf("probabilities %v", fr.Probabilities)
	}
}

func TestCompileRejects(t *testing.T) {
	for name, src := range map[string]string{
		"no name":         `{"facets":[{"name":"a","labels":[{"name":"x"},{"name":"y"}]}]}`,
		"one label":       `{"name":"p","facets":[{"name":"a","labels":[{"name":"x"}]}]}`,
		"unknown label":   `{"name":"p","facets":[{"name":"a","labels":[{"name":"x"},{"name":"y"}],"rules":[{"id":"r","label":"z","prefix":"/"}]}]}`,
		"two tests":       `{"name":"p","facets":[{"name":"a","labels":[{"name":"x"},{"name":"y"}],"rules":[{"id":"r","label":"x","prefix":"/","pattern":"a"}]}]}`,
		"bad regexp":      `{"name":"p","facets":[{"name":"a","labels":[{"name":"x"},{"name":"y"}],"rules":[{"id":"r","label":"x","pattern":"("}]}]}`,
		"bad op":          `{"name":"p","facets":[{"name":"a","labels":[{"name":"x"},{"name":"y"}],"rules":[{"id":"r","label":"x","when":[{"field":"f","op":"glob"}]}]}]}`,
		"repeated rule":   `{"name":"p","facets":[{"name":"a","labels":[{"name":"x"},{"name":"y"}],"rules":[{"id":"r","label":"x"},{"id":"r","label":"y"}]}]}`,
		"min_p range":     `{"name":"p","facets":[{"name":"a","min_p":2,"labels":[{"name":"x"},{"name":"y"}]}]}`,
		"at_start alone":  `{"name":"p","facets":[{"name":"a","labels":[{"name":"x"},{"name":"y"}],"rules":[{"id":"r","label":"x","at_start":true}]}]}`,
		"repeated facet":  `{"name":"p","facets":[{"name":"a","labels":[{"name":"x"},{"name":"y"}]},{"name":"a","labels":[{"name":"x"},{"name":"y"}]}]}`,
		"repeated label":  `{"name":"p","facets":[{"name":"a","labels":[{"name":"x"},{"name":"x"}]}]}`,
		"empty word":      `{"name":"p","facets":[{"name":"a","labels":[{"name":"x"},{"name":"y"}],"rules":[{"id":"r","label":"x","words":[" "]}]}]}`,
		"no facets":       `{"name":"p"}`,
		"condition field": `{"name":"p","facets":[{"name":"a","labels":[{"name":"x"},{"name":"y"}],"rules":[{"id":"r","label":"x","when":[{"op":"exists"}]}]}]}`,
	} {
		p, err := ParsePack([]byte(src))
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		if _, err := Compile(p); err == nil {
			t.Errorf("%s: compiled", name)
		}
	}
	if _, err := ParsePack([]byte(`{"name":"p","bogus":1}`)); err == nil {
		t.Error("unknown field parsed")
	}
}

func TestLayerOverridesByNameAndID(t *testing.T) {
	base, _ := ParsePack([]byte(actPack))
	mod := Pack{Name: "test.notes", Version: "m1", Facets: []FacetSpec{{
		Name:   "act",
		MinP:   0.6,
		Labels: []LabelSpec{{Name: "approval", Disabled: true}, {Name: "chat"}},
		Rules: []RuleSpec{
			{ID: "yes", Disabled: true},
			{ID: "review-mode", Disabled: true},
			{ID: "thanks", Label: "chat", Words: []string{"thanks"}, Weight: 2},
		},
	}}}
	c, err := Compile(Layer(base, mod))
	if err != nil {
		t.Fatal(err)
	}
	f, _ := c.Facet("act")
	if c.Name != "test.notes" || c.Version != "1+m1" || f.MinP != 0.6 {
		t.Fatalf("layer settings: %s %s %v", c.Name, c.Version, f.MinP)
	}
	var labels []string
	for _, l := range f.Labels {
		labels = append(labels, l.Name)
	}
	if strings.Join(labels, ",") != "command,question,task,chat" {
		t.Fatalf("labels %v", labels)
	}
	e, _ := NewEngine(c)
	if got := e.Classify(context.Background(), Input{Text: "thanks!"}).Label("act"); got != "chat" {
		t.Fatalf("added rule: %q", got)
	}
	if base.Facets[0].Labels[3].Name != "approval" {
		t.Fatal("Layer changed its input")
	}
}

func TestHashTracksContentNotVersion(t *testing.T) {
	a, _ := ParsePack([]byte(actPack))
	b := a
	b.Version = "2"
	ca, _ := Compile(a)
	cb, _ := Compile(b)
	if ca.Hash != cb.Hash {
		t.Fatal("version label changed the hash")
	}
	c := Layer(a, Pack{Facets: []FacetSpec{{Name: "act", MinP: 0.9}}})
	cc, _ := Compile(c)
	if cc.Hash == ca.Hash {
		t.Fatal("content change kept the hash")
	}
}

func TestBayesLearnsAndRoundTrips(t *testing.T) {
	c, _ := Compile(mustPack(t))
	b := NewBayes("bayes:act", "act", BayesOptions{})
	if v, _ := b.Judge(context.Background(), Input{Text: "fix it"}); v != nil {
		t.Fatal("an empty model voted")
	}
	b.LearnAll(c.Examples("act"))
	for i := 0; i < 5; i++ {
		b.Learn("please fix the build", "task", 1)
		b.Learn("where are my notes", "question", 1)
	}
	post := b.Posterior("could you fix the notes build")
	if post["task"] < post["question"] {
		t.Fatalf("posterior %v", post)
	}
	before := b.Version()
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	r := NewBayes("bayes:act", "act", BayesOptions{})
	if err := json.Unmarshal(raw, r); err != nil {
		t.Fatal(err)
	}
	if r.Version() != before || math.Abs(r.Posterior("fix it")["task"]-b.Posterior("fix it")["task"]) > 1e-12 {
		t.Fatal("restored model differs")
	}
	if err := json.Unmarshal(raw, NewBayes("x", "other", BayesOptions{})); err == nil {
		t.Fatal("restored into another facet")
	}
	b.Learn("one more", "task", 1)
	if b.Version() == before {
		t.Fatal("learning did not change the version")
	}
	votes, _ := b.Judge(context.Background(), Input{Text: strings.Repeat("fix the build ", 200)})
	for _, v := range votes {
		if math.Abs(v.Weight) > 3 {
			t.Fatalf("vote not clamped: %v", v)
		}
	}
}

func TestEngineFingerprintFollowsLearning(t *testing.T) {
	b := NewBayes("bayes:act", "act", BayesOptions{})
	e := mustEngine(t, actPack, b)
	before := e.Fingerprint()
	b.Learn("x y", "task", 1)
	if e.Fingerprint() == before {
		t.Fatal("fingerprint ignored a learning judge")
	}
}

type fakeEmbedder struct{ dims map[string]int }

func (f fakeEmbedder) Name() string { return "fake" }
func (f fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, 4)
		for _, w := range Tokens(t) {
			if d, ok := f.dims[w]; ok {
				v[d]++
			}
		}
		v[3] += 0.1
		out[i] = v
	}
	return out, nil
}

func TestCentroidVotesForTheNearerLabel(t *testing.T) {
	emb := fakeEmbedder{dims: map[string]int{"fix": 0, "add": 0, "what": 1, "how": 1, "yes": 2}}
	c := NewCentroid("centroid:act", "act", emb, CentroidOptions{})
	if c.Phase() != Submit {
		t.Fatal("centroid defaults to submit")
	}
	comp, _ := Compile(mustPack(t))
	if err := c.Fit(context.Background(), comp.Examples("act")); err != nil {
		t.Fatal(err)
	}
	e := mustEngine(t, actPack, c)
	res := e.Classify(context.Background(), Input{Text: "fix and add stuff", Phase: Submit})
	var best Vote
	for _, v := range res.Facets["act"].Votes {
		if v.Judge == "centroid:act" && v.Weight > best.Weight {
			best = v
		}
	}
	if best.Label != "task" {
		t.Fatalf("centroid best vote %+v", best)
	}
}

func TestEvaluateReportsFloorAndBands(t *testing.T) {
	e := mustEngine(t, actPack)
	xs := []Example{
		{Text: "/open notes", Labels: map[string]string{"act": "command"}},
		{Text: "what now?", Labels: map[string]string{"act": "question"}},
		{Text: "fix it", Labels: map[string]string{"act": "task"}},
		{Text: "hmm", Labels: map[string]string{"act": "task"}},
	}
	r := Evaluate(context.Background(), e, Keystroke, xs)
	if len(r) != 1 {
		t.Fatalf("reports %d", len(r))
	}
	a := r[0]
	if a.N != 4 || a.Answered != 3 || a.Accuracy != 0.75 || a.Precision != 1 || a.Majority != 0.5 || a.MajorityLabel != "task" {
		t.Fatalf("report %+v", a)
	}
	if a.Confusion["task"][""] != 1 || len(a.Errors) != 1 {
		t.Fatalf("confusion %v errors %v", a.Confusion, a.Errors)
	}
	n := 0
	for _, b := range a.Bands {
		n += b.N
	}
	if n != 4 {
		t.Fatalf("bands cover %d", n)
	}
}

func TestLedgerRecordsOnePerFacetAndReplayIsIdempotent(t *testing.T) {
	e := mustEngine(t, actPack)
	in := Input{ID: "turn-1", Text: "what now?", Phase: Keystroke}
	res := e.Classify(context.Background(), in)
	now := time.Unix(1_800_000_000, 0)
	recs, err := res.LedgerRecords(in, ledger.DefaultRetention(), ledger.Envelope{TurnID: "turn-1"}, "", false, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Output.Label != "question" || recs[0].Stage != ledger.StageClassifier {
		t.Fatalf("records %+v", recs)
	}
	store := &ledger.MemStore{}
	if _, _, err := store.Append(context.Background(), recs[0]); err != nil {
		t.Fatal(err)
	}
	again, _ := e.Classify(context.Background(), in).LedgerRecords(in, ledger.DefaultRetention(), ledger.Envelope{}, "", false, now)
	if _, dup, err := store.Append(context.Background(), again[0]); err != nil || !dup {
		t.Fatalf("replay appended a row (duplicate=%v err=%v)", dup, err)
	}
	in.Phase = Submit
	sub, _ := e.Classify(context.Background(), in).LedgerRecords(in, ledger.DefaultRetention(), ledger.Envelope{}, "", false, now)
	if sub[0].IdempotencyKey == recs[0].IdempotencyKey {
		t.Fatal("a later phase reused the earlier phase's key")
	}
	abst := e.Classify(context.Background(), Input{ID: "t2", Text: "hmm"})
	ar, _ := abst.LedgerRecords(Input{ID: "t2", Text: "hmm"}, ledger.DefaultRetention(), ledger.Envelope{}, "", false, now)
	if ar[0].Output.Label != Abstained {
		t.Fatalf("abstention label %q", ar[0].Output.Label)
	}
	if _, err := (Result{}).LedgerRecords(in, ledger.DefaultRetention(), ledger.Envelope{}, "", false, now); err == nil {
		t.Fatal("no input id accepted")
	}
}

func TestTokens(t *testing.T) {
	got := strings.Join(Tokens("Open /notes, then fix foo_bar.go -- don't stop!"), "|")
	if got != "open|/notes|then|fix|foo_bar.go|don't|stop" {
		t.Fatalf("tokens %s", got)
	}
}

func mustPack(t *testing.T) Pack {
	t.Helper()
	p, err := ParsePack([]byte(actPack))
	if err != nil {
		t.Fatal(err)
	}
	return p
}
