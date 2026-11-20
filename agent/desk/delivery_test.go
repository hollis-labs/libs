package desk

import (
	"strings"
	"testing"
	"time"
)

func editor(t *testing.T, p Policy, l *Ledger, c *clock) *Editor {
	t.Helper()
	e, err := NewEditor(p, WithLedger(l), WithClock(c.now))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestDefaultPolicyDecisions(t *testing.T) {
	p := DefaultPolicy()
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		item Item
		st   State
		open bool
		want Action
		rule string
	}{
		{"critical beats focus", Item{Kind: ItemNotice, Urgency: Critical}, State{Mode: "focus"}, false, Now, "invariant:critical"},
		{"question always now", Item{Kind: ItemQuestion, Urgency: Low}, State{Mode: "focus"}, true, Now, "invariant:question"},
		{"focus digests a result", Item{Kind: ItemResult}, State{Mode: "focus"}, false, Digest, "focus"},
		{"high urgency now", Item{Kind: ItemResult, Urgency: High}, State{}, true, Now, "urgent"},
		{"result merges", Item{Kind: ItemResult}, State{}, false, Merge, "results"},
		{"result held while owner answers", Item{Kind: ItemResult}, State{}, true, Hold, "waiting-on-owner"},
		{"producer asks now", Item{Kind: ItemResult, Prefer: Now}, State{}, true, Now, "preferred-now"},
		{"producer asks to file", Item{Kind: ItemResult, Prefer: File}, State{}, false, File, "preferred-file"},
		{"progress filed", Item{Kind: ItemProgress}, State{}, false, File, "progress"},
		{"low notice digested", Item{Kind: ItemNotice, Urgency: Low}, State{}, false, Digest, "notices"},
		{"normal notice default", Item{Kind: ItemNotice}, State{}, false, Merge, "default"},
	} {
		d := p.Decide(tc.item, tc.st, tc.open)
		if d.Action != tc.want || d.Rule != tc.rule {
			t.Errorf("%s: got %s by %s, want %s by %s", tc.name, d.Action, d.Rule, tc.want, tc.rule)
		}
	}
}

func TestLayerOrderAndInvariants(t *testing.T) {
	app := Policy{Rules: []Rule{{Name: "app-results-digest", When: Match{Kinds: []ItemKind{ItemResult}}, Do: Digest}}, MergeWindow: time.Minute}
	module := Policy{Rules: []Rule{
		{Name: "mail-file", When: Match{Sources: []string{"mail*"}}, Do: File},
		{Name: "swallow-questions", When: Match{Kinds: []ItemKind{ItemQuestion}}, Do: File},
	}, Quiet: true}
	p := Layer(DefaultPolicy(), app, module)
	if p.MergeWindow != time.Minute || !p.Quiet || p.DigestMax != 10 {
		t.Fatalf("settings: %+v", p)
	}
	if d := p.Decide(Item{Kind: ItemResult, Source: "mail.sorter"}, State{}, false); d.Rule != "mail-file" {
		t.Fatalf("module rule should win: %+v", d)
	}
	if d := p.Decide(Item{Kind: ItemResult, Source: "subtask"}, State{}, false); d.Rule != "app-results-digest" {
		t.Fatalf("app rule should beat defaults: %+v", d)
	}
	if d := p.Decide(Item{Kind: ItemQuestion, Source: "mail"}, State{}, false); d.Action != Now {
		t.Fatalf("a rule swallowed a question: %+v", d)
	}
	if len(DefaultPolicy().Rules) != len(Layer(DefaultPolicy()).Rules) {
		t.Fatal("Layer with no layers changed the rules")
	}
	if err := (Policy{Rules: []Rule{{Do: "shout"}}}).Validate(); err == nil {
		t.Fatal("bad action accepted")
	}
	if _, err := NewEditor(Policy{MergeWindow: -1}); err == nil {
		t.Fatal("negative window accepted")
	}
}

func TestMergeWindowAndTopicGrouping(t *testing.T) {
	c := newClock()
	l := NewLedger(c.now)
	_, _ = l.Open("rel", "Release", "")
	e := editor(t, DefaultPolicy(), l, c)
	e.Submit(Item{ID: "1", Topic: "rel", Kind: ItemResult, Text: "changelog drafted"}, State{})
	if _, ok := e.Due(State{}); ok {
		t.Fatal("merge released before its window")
	}
	c.advance(10 * time.Second)
	e.Submit(Item{ID: "2", Topic: "rel", Kind: ItemFailure, Text: "tag push failed"}, State{})
	if next, ok := e.Next(State{}); !ok || !next.Equal(c.now().Add(10*time.Second)) {
		t.Fatalf("next %v %v", next, ok)
	}
	c.advance(10 * time.Second)
	m, ok := e.Due(State{})
	if !ok || len(m.Items) != 2 || len(m.Topics) != 1 {
		t.Fatalf("release %+v %v", m, ok)
	}
	want := "Release:\n- changelog drafted\n- Failed: tag push failed"
	if m.Text != want {
		t.Fatalf("text:\n%s\nwant:\n%s", m.Text, want)
	}
	if len(e.Pending()) != 0 {
		t.Fatal("released items still pending")
	}
}

func TestNowCarriesMergesOnItsTopic(t *testing.T) {
	c := newClock()
	e := editor(t, DefaultPolicy(), nil, c)
	e.Submit(Item{ID: "1", Topic: "a", Kind: ItemResult, Text: "first"}, State{})
	e.Submit(Item{ID: "2", Topic: "b", Kind: ItemResult, Text: "other topic"}, State{})
	e.Submit(Item{ID: "3", Topic: "a", Kind: ItemResult, Urgency: High, Text: "urgent"}, State{})
	m, ok := e.Due(State{})
	if !ok || len(m.Items) != 2 || m.Items[0].Item.ID != "1" || m.Items[1].Item.ID != "3" {
		t.Fatalf("release %+v", m.Items)
	}
	if p := e.Pending(); len(p) != 1 || p[0].Item.ID != "2" {
		t.Fatalf("pending %+v", p)
	}
}

func TestHoldUntilAnsweredOrTimeout(t *testing.T) {
	c := newClock()
	l := NewLedger(c.now)
	_, _ = l.Open("a", "A", "")
	_, _ = l.Open("b", "B", "")
	q, _ := l.Ask("a", "", "deploy now?")
	_, _ = l.Ask("b", "", "which host?")
	e := editor(t, DefaultPolicy(), l, c)
	if d := e.Submit(Item{ID: "1", Topic: "a", Kind: ItemResult, Text: "tests passed"}, State{}); d.Action != Hold {
		t.Fatalf("decision %+v", d)
	}
	e.Submit(Item{ID: "2", Topic: "b", Kind: ItemResult, Text: "b result"}, State{})
	c.advance(time.Minute)
	if _, ok := e.Due(State{}); ok {
		t.Fatal("held item released with the question open")
	}
	if err := l.Answer("a", q.ID, "yes"); err != nil {
		t.Fatal(err)
	}
	m, ok := e.Due(State{})
	if !ok || len(m.Items) != 1 || m.Items[0].Item.ID != "1" {
		t.Fatalf("after answer %+v %v", m.Items, ok)
	}
	c.advance(30 * time.Minute)
	m, ok = e.Due(State{})
	if !ok || m.Items[0].Item.ID != "2" {
		t.Fatalf("HoldMax did not release: %+v %v", m.Items, ok)
	}
}

func TestDigestByCountTimeAndRideAlong(t *testing.T) {
	c := newClock()
	p := DefaultPolicy()
	p.DigestMax = 3
	e := editor(t, p, nil, c)
	low := func(id string) Item { return Item{ID: id, Kind: ItemNotice, Urgency: Low, Text: "fyi " + id} }
	e.Submit(low("1"), State{})
	e.Submit(low("2"), State{})
	if _, ok := e.Due(State{}); ok {
		t.Fatal("digest released early")
	}
	e.Submit(low("3"), State{})
	if m, ok := e.Due(State{}); !ok || len(m.Items) != 3 {
		t.Fatalf("digest at max: %+v %v", m.Items, ok)
	}
	e.Submit(low("4"), State{})
	c.advance(30 * time.Minute)
	if m, ok := e.Due(State{}); !ok || len(m.Items) != 1 {
		t.Fatalf("digest by time: %+v %v", m.Items, ok)
	}
	// Ride along: a release that goes out anyway carries pending digest items.
	e.Submit(low("5"), State{})
	e.Submit(Item{ID: "6", Kind: ItemResult, Urgency: High, Text: "done"}, State{})
	if m, ok := e.Due(State{}); !ok || len(m.Items) != 2 {
		t.Fatalf("ride along: %+v %v", m.Items, ok)
	}
	quiet := p
	quiet.Quiet = true
	e = editor(t, quiet, nil, c)
	e.Submit(low("7"), State{})
	e.Submit(Item{ID: "8", Kind: ItemResult, Urgency: High, Text: "done"}, State{})
	if m, _ := e.Due(State{}); len(m.Items) != 1 {
		t.Fatalf("quiet policy let a digest ride along: %+v", m.Items)
	}
}

func TestBusyOwnerOnlyInterrupted(t *testing.T) {
	c := newClock()
	p := DefaultPolicy()
	p.BusyMax = 5 * time.Minute
	e := editor(t, p, nil, c)
	e.Submit(Item{ID: "1", Kind: ItemResult, Urgency: High, Text: "done"}, State{Busy: true})
	if _, ok := e.Due(State{Busy: true}); ok {
		t.Fatal("non-critical item interrupted a busy owner")
	}
	e.Submit(Item{ID: "2", Kind: ItemQuestion, Text: "ok to push?"}, State{Busy: true})
	m, ok := e.Due(State{Busy: true})
	if !ok || len(m.Items) != 1 || m.Items[0].Item.ID != "2" {
		t.Fatalf("question should interrupt: %+v", m.Items)
	}
	if next, ok := e.Next(State{Busy: true}); !ok || !next.Equal(c.now().Add(5*time.Minute)) {
		t.Fatalf("next should be BusyMax: %v %v", next, ok)
	}
	c.advance(5 * time.Minute)
	if m, ok := e.Due(State{Busy: true}); !ok || m.Items[0].Item.ID != "1" {
		t.Fatalf("BusyMax did not release: %+v %v", m.Items, ok)
	}
	e.Submit(Item{ID: "3", Kind: ItemResult, Urgency: High, Text: "x"}, State{})
	if _, ok := e.Due(State{}); !ok {
		t.Fatal("idle owner did not get a due item")
	}
}

func TestFileFlushHeldAndComposer(t *testing.T) {
	c := newClock()
	e := editor(t, DefaultPolicy(), nil, c)
	e.Submit(Item{ID: "p", Kind: ItemProgress, Text: "50%"}, State{})
	e.Submit(Item{ID: "m", Topic: "t", Kind: ItemResult, Text: "r"}, State{})
	e.Submit(Item{ID: "d", Kind: ItemNotice, Urgency: Low, Text: "n"}, State{})
	if f := e.Filed(); len(f) != 1 || f[0].Item.ID != "p" {
		t.Fatalf("filed %+v", f)
	}
	held := e.Held()
	if held[Merge] != 1 || held[Digest] != 1 || held[File] != 0 {
		t.Fatalf("held %v", held)
	}
	m, ok := e.Flush()
	if !ok || len(m.Items) != 2 || m.Items[1].Item.Topic != "" {
		t.Fatalf("flush %+v", m.Items)
	}
	if !strings.Contains(m.Text, "t:\n- r") || !strings.Contains(m.Text, "Other:\n- n") {
		t.Fatalf("flush text:\n%s", m.Text)
	}
	if _, ok := e.Flush(); ok {
		t.Fatal("second flush returned items")
	}
	if got := e.TakeFiled(); len(got) != 1 || len(e.Filed()) != 0 {
		t.Fatal("TakeFiled")
	}
	one := DefaultComposer([]Queued{{Item: Item{Kind: ItemQuestion, Title: "Deploy", Text: "now?"}}}, nil)
	if one != "Question: Deploy: now?" {
		t.Fatalf("single item text %q", one)
	}
	mixed := DefaultComposer([]Queued{{Item: Item{Topic: "x", Kind: ItemResult, Text: "r"}}, {Item: Item{Topic: "x", Kind: ItemQuestion, Text: "q?"}}}, map[TopicID]string{"x": "X"})
	if mixed != "X:\n- Question: q?\n- r" {
		t.Fatalf("questions first:\n%s", mixed)
	}
}

func TestSetPolicyKeepsQueuedDecisions(t *testing.T) {
	c := newClock()
	e := editor(t, DefaultPolicy(), nil, c)
	e.Submit(Item{ID: "1", Kind: ItemResult, Text: "r"}, State{})
	if err := e.SetPolicy(Layer(DefaultPolicy(), Policy{Rules: []Rule{{Name: "all-file", Do: File}}})); err != nil {
		t.Fatal(err)
	}
	if d := e.Submit(Item{ID: "2", Kind: ItemResult}, State{}); d.Action != File {
		t.Fatalf("new policy not applied: %+v", d)
	}
	if p := e.Pending(); len(p) != 1 || p[0].Decision.Action != Merge {
		t.Fatalf("queued decision changed: %+v", p)
	}
	if err := e.SetPolicy(Policy{Default: "x"}); err == nil {
		t.Fatal("invalid policy accepted")
	}
}
