package router_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/hollis-labs/libs/core/util/ledger"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/libs/core/util/router"
)

func f(v float64) *float64 { return &v }

func subj(conf *float64, kv ...string) router.Subject {
	m := map[string]string{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return router.Subject{ID: "s1", Fields: m, Confidence: conf}
}

const rulesJSON = `{
 "version": "2026-10-16.1",
 "strategy": "fan_out",
 "rules": [
  {"id":"gh-links","when":[{"field":"source","op":"eq","value":"inbox"},{"field":"entity.kind","op":"eq","value":"url"},{"field":"entity.value","op":"glob","value":"https://github.com/*"}],"target":"github-enricher","auto":true},
  {"id":"handoff","priority":10,"when":[{"field":"entity.kind","op":"eq","value":"handoff"},{"field":"action","op":"eq","value":"save"}],"target":"notes.write","auto":true,"min_confidence":0.8}
 ]}`

func mustRouter(t *testing.T, js string, cfg router.Config) *router.Router {
	t.Helper()
	rs, err := router.ParseRuleSet([]byte(js))
	if err != nil {
		t.Fatal(err)
	}
	r, err := router.Compile(rs, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestMatchAndConsidered(t *testing.T) {
	r := mustRouter(t, rulesJSON, router.Config{})
	d, err := r.Route(context.Background(), subj(nil, "source", "inbox", "entity.kind", "url", "entity.value", "https://github.com/a/b"))
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != router.Routed || len(d.Matches) != 1 || d.Matches[0].Target != "github-enricher" {
		t.Fatalf("%+v", d)
	}
	if len(d.Considered) != 2 || d.Considered[0].RuleID != "handoff" || d.Considered[0].Matched {
		t.Fatalf("priority order / considered: %+v", d.Considered)
	}
	if d.RuleHash == "" || d.Stage != "rule" {
		t.Fatalf("ledger fields: %+v", d)
	}
}

func TestConfidenceGateIsReal(t *testing.T) {
	r := mustRouter(t, rulesJSON, router.Config{})
	base := []string{"entity.kind", "handoff", "action", "save"}

	cases := []struct {
		name string
		conf *float64
		want router.Outcome
		disp router.Disposition
	}{
		{"high", f(0.95), router.Routed, router.Apply},
		{"low", f(0.5), router.Suggested, router.Ask},
		{"unknown is not zero but still fails the gate", nil, router.Suggested, router.Ask},
	}
	for _, c := range cases {
		d, _ := r.Route(context.Background(), subj(c.conf, base...))
		if d.Outcome != c.want || d.Matches[0].Disposition != c.disp {
			t.Errorf("%s: %+v", c.name, d)
		}
	}
	d, _ := r.Route(context.Background(), subj(nil, base...))
	if !strings.Contains(d.Matches[0].Why, "unknown") {
		t.Errorf("why should say unknown: %s", d.Matches[0].Why)
	}
}

func TestFirstMatchStopsAndAbstainFallsThrough(t *testing.T) {
	js := `{"version":"v","strategy":"first_match","rules":[
	 {"id":"gated","priority":2,"when":[{"field":"type","op":"eq","value":"q"}],"target":"a","auto":true,"min_confidence":0.9,"on_low_confidence":"abstain"},
	 {"id":"general","priority":1,"when":[{"field":"type","op":"exists"}],"target":"b","auto":true},
	 {"id":"never","when":[{"field":"type","op":"exists"}],"target":"c","auto":true}]}`
	r := mustRouter(t, js, router.Config{})

	d, _ := r.Route(context.Background(), subj(f(0.2), "type", "q"))
	if len(d.Matches) != 1 || d.Matches[0].RuleID != "general" {
		t.Fatalf("abstain should fall through: %+v", d.Matches)
	}
	if d.Considered[2].Matched || !strings.Contains(d.Considered[2].Why, "first_match") {
		t.Fatalf("%+v", d.Considered)
	}
	d, _ = r.Route(context.Background(), subj(f(0.99), "type", "q"))
	if d.Matches[0].RuleID != "gated" || len(d.Matches) != 1 {
		t.Fatalf("%+v", d.Matches)
	}
}

func TestNonAutoIsSuggestion(t *testing.T) {
	js := `{"version":"v","strategy":"fan_out","rules":[{"id":"r","when":[{"field":"a","op":"exists"}],"target":"t"}]}`
	d, _ := mustRouter(t, js, router.Config{}).Route(context.Background(), subj(nil, "a", "1"))
	if d.Outcome != router.Suggested {
		t.Fatalf("%+v", d)
	}
}

type pol func(router.Match) (router.Verdict, error)

func (p pol) Evaluate(_ context.Context, _ router.Subject, m router.Match) (router.Verdict, error) {
	return p(m)
}

func TestPolicyMostRestrictiveWins(t *testing.T) {
	denyAll := pol(func(router.Match) (router.Verdict, error) {
		return router.Verdict{Effect: router.Deny, Reason: "no egress"}, nil
	})
	s := subj(nil, "source", "inbox", "entity.kind", "url", "entity.value", "https://github.com/a/b")
	d, _ := mustRouter(t, rulesJSON, router.Config{Policy: denyAll}).Route(context.Background(), s)
	if d.Outcome != router.Denied || !strings.Contains(d.Matches[0].Why, "no egress") {
		t.Fatalf("%+v", d)
	}

	// policy cannot loosen a rule: allow verdict leaves Ask as Ask
	allow := pol(func(router.Match) (router.Verdict, error) { return router.Verdict{Effect: router.Apply}, nil })
	d, _ = mustRouter(t, rulesJSON, router.Config{Policy: allow}).Route(context.Background(), subj(f(0.1), "entity.kind", "handoff", "action", "save"))
	if d.Outcome != router.Suggested {
		t.Fatalf("policy loosened: %+v", d)
	}

	// policy error fails closed
	broken := pol(func(router.Match) (router.Verdict, error) { return router.Verdict{}, errors.New("pdp down") })
	d, _ = mustRouter(t, rulesJSON, router.Config{Policy: broken}).Route(context.Background(), s)
	if d.Outcome != router.Denied {
		t.Fatalf("policy error must fail closed: %+v", d)
	}
}

func TestPolicyShadowRecordsOnly(t *testing.T) {
	denyAll := pol(func(router.Match) (router.Verdict, error) {
		return router.Verdict{Effect: router.Deny, Reason: "x"}, nil
	})
	s := subj(nil, "source", "inbox", "entity.kind", "url", "entity.value", "https://github.com/a/b")
	d, _ := mustRouter(t, rulesJSON, router.Config{Policy: denyAll, PolicyShadow: true}).Route(context.Background(), s)
	if d.Outcome != router.Routed || d.ShadowVerdicts["gh-links"].Effect != router.Deny {
		t.Fatalf("%+v", d)
	}
}

type esc struct{ called bool }

func (e *esc) Escalate(context.Context, router.Subject, router.Decision) (router.Escalation, error) {
	e.called = true
	return router.Escalation{Stage: "classifier", Ref: "decide/closed-set"}, nil
}

func TestEscalatesOnlyWhenNoRoute(t *testing.T) {
	e := &esc{}
	r := mustRouter(t, rulesJSON, router.Config{Escalator: e})
	d, _ := r.Route(context.Background(), subj(nil, "source", "mail"))
	if !e.called || d.Escalation == nil || d.Escalation.Stage != "classifier" {
		t.Fatalf("%+v", d)
	}
	e.called = false
	r.Route(context.Background(), subj(nil, "source", "inbox", "entity.kind", "url", "entity.value", "https://github.com/a/b"))
	if e.called {
		t.Fatal("escalated despite a route")
	}
	// a suggestion is already a human path; no escalation
	r.Route(context.Background(), subj(f(0.1), "entity.kind", "handoff", "action", "save"))
	if e.called {
		t.Fatal("escalated despite a suggestion")
	}
}

func TestCompileRejects(t *testing.T) {
	bad := map[string]string{
		"no strategy":   `{"version":"v","rules":[]}`,
		"empty when":    `{"version":"v","strategy":"fan_out","rules":[{"id":"a","when":[],"target":"t"}]}`,
		"bad op":        `{"version":"v","strategy":"fan_out","rules":[{"id":"a","when":[{"field":"x","op":"like","value":"1"}],"target":"t"}]}`,
		"dup id":        `{"version":"v","strategy":"fan_out","rules":[{"id":"a","when":[{"field":"x","op":"exists"}],"target":"t"},{"id":"a","when":[{"field":"x","op":"exists"}],"target":"t"}]}`,
		"empty glob":    `{"version":"v","strategy":"fan_out","rules":[{"id":"a","when":[{"field":"x","op":"glob"}],"target":"t"}]}`,
		"confidence >1": `{"version":"v","strategy":"fan_out","rules":[{"id":"a","when":[{"field":"x","op":"exists"}],"target":"t","min_confidence":1.5}]}`,
	}
	for name, js := range bad {
		rs, err := router.ParseRuleSet([]byte(js))
		if err == nil {
			_, err = router.Compile(rs, router.Config{})
		}
		if err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if _, err := router.ParseRuleSet([]byte(`{"version":"v","strategy":"fan_out","rules":[],"typo":1}`)); err == nil {
		t.Error("unknown field accepted")
	}
}

func TestHashTracksContentNotLabel(t *testing.T) {
	a := mustRouter(t, rulesJSON, router.Config{})
	b := mustRouter(t, strings.Replace(rulesJSON, `"target":"github-enricher"`, `"target":"other"`, 1), router.Config{})
	if a.Hash() == b.Hash() {
		t.Fatal("hash should change with rule content")
	}
}

func TestGlobCrossesSlashes(t *testing.T) {
	js := `{"version":"v","strategy":"fan_out","rules":[{"id":"r","when":[{"field":"u","op":"glob","value":"https://github.com/*/issues/?"}],"target":"t","auto":true}]}`
	r := mustRouter(t, js, router.Config{})
	for u, want := range map[string]router.Outcome{
		"https://github.com/o/r/issues/7":  router.Routed,
		"https://github.com/o/r/issues/77": router.NoRoute,
		"https://gitlab.com/o/r/issues/7":  router.NoRoute,
	} {
		if d, _ := r.Route(context.Background(), subj(nil, "u", u)); d.Outcome != want {
			t.Errorf("%s: %s", u, d.Outcome)
		}
	}
}

func TestObligationsAreNotJustAllow(t *testing.T) {
	s := subj(nil, "source", "inbox", "entity.kind", "url", "entity.value", "https://github.com/a/b")
	route := func(o ...router.Obligation) router.Decision {
		p := pol(func(router.Match) (router.Verdict, error) {
			return router.Verdict{Effect: router.Apply, Obligations: o, Reason: "ok"}, nil
		})
		d, _ := mustRouter(t, rulesJSON, router.Config{Policy: p}).Route(context.Background(), s)
		return d
	}

	if d := route(router.Obligation{Kind: router.ObApproval, PolicyID: "p", Reference: "ap-1"}); d.Outcome != router.Suggested || d.Matches[0].Disposition != router.Ask {
		t.Errorf("approval must ask: %+v", d.Matches)
	}
	if d := route(router.Obligation{Kind: router.ObDryRunOnly, PolicyID: "p"}); d.Outcome != router.Routed || !d.Matches[0].DryRun {
		t.Errorf("dry run must be carried: %+v", d.Matches)
	}
	lim := &router.Limit{Count: 2, Window: time.Hour, Unit: "route"}
	if d := route(router.Obligation{Kind: router.ObRateLimit, PolicyID: "p", Reference: "r", Limit: lim}); d.Outcome != router.Routed || d.Matches[0].Obligations[0].Limit.Count != 2 {
		t.Errorf("rate limit must be carried: %+v", d.Matches)
	}
	if d := route(router.Obligation{Kind: router.ObRateLimit, PolicyID: "p"}); d.Outcome != router.Denied {
		t.Errorf("malformed rate limit must deny: %+v", d.Matches)
	}
	if d := route(router.Obligation{Kind: "teleport", PolicyID: "p"}); d.Outcome != router.Denied || !strings.Contains(d.Matches[0].Why, "unknown obligation") {
		t.Errorf("unknown kind must deny: %+v", d.Matches)
	}
}

func TestSnapshotHookRedactsStoredInput(t *testing.T) {
	cfg := router.Config{Snapshot: func(s router.Subject) router.Subject {
		keep := map[string]string{}
		for k, v := range s.Fields {
			if k != "entity.value" {
				keep[k] = v
			}
		}
		return router.Subject{Fields: keep, Confidence: s.Confidence}
	}}
	d, _ := mustRouter(t, rulesJSON, cfg).Route(context.Background(),
		subj(nil, "source", "inbox", "entity.kind", "url", "entity.value", "https://github.com/a/b?token=T"))
	if _, has := d.Input.Fields["entity.value"]; has || d.Input.ID != "s1" || d.Outcome != router.Routed {
		t.Fatalf("%+v", d)
	}
}

func TestDecisionLandsInLedger(t *testing.T) {
	ctx := context.Background()
	store := &ledger.MemStore{}
	r := mustRouter(t, rulesJSON, router.Config{})
	s := subj(f(0.95), "entity.kind", "handoff", "action", "save")

	d, _ := r.Route(ctx, s)
	rec, err := d.LedgerRecord(ledger.DefaultRetention(), ledger.Envelope{SessionID: "sess-1", TurnID: "t-4"}, "", false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	a, dup, err := store.Append(ctx, rec)
	if err != nil || dup {
		t.Fatalf("%v %v", err, dup)
	}
	if a.Output.Label != "notes.write" || a.Disposition != ledger.Applied || a.Version.Hash != r.Hash() ||
		a.Envelope.SessionID != "sess-1" || len(a.Alternatives) != 2 || *a.Output.Confidence != 0.95 {
		t.Fatalf("%+v", a)
	}
	var detail struct {
		Matches []struct {
			RuleID string `json:"rule_id"`
		}
	}
	if json.Unmarshal(a.Output.Detail, &detail) != nil || detail.Matches[0].RuleID != "handoff" {
		t.Fatalf("detail: %s", a.Output.Detail)
	}

	// replay: same rules, same subject -> same row
	d2, _ := r.Route(ctx, s)
	rec2, _ := d2.LedgerRecord(ledger.DefaultRetention(), ledger.Envelope{}, "", false, time.Now())
	if b, dup, _ := store.Append(ctx, rec2); !dup || b.DecisionID != a.DecisionID {
		t.Fatal("replay must not append")
	}

	// changed rules -> new row for the same subject
	r2 := mustRouter(t, strings.Replace(rulesJSON, `"min_confidence":0.8`, `"min_confidence":0.9`, 1), router.Config{})
	d3, _ := r2.Route(ctx, s)
	rec3, _ := d3.LedgerRecord(ledger.DefaultRetention(), ledger.Envelope{}, "", false, time.Now())
	if _, dup, _ := store.Append(ctx, rec3); dup {
		t.Fatal("a changed rule set is a different decision")
	}

	// a human corrects the routing, as a label; the original row stays
	if _, err := store.AppendEvent(ctx, ledger.Event{DecisionID: a.DecisionID, Kind: ledger.EventCorrection,
		Correction: &ledger.Correction{Label: "github-enricher", Actor: "urn:hollis:user:c"}}); err != nil {
		t.Fatal(err)
	}
	e, _ := store.Get(ctx, a.DecisionID)
	if l, c := e.Label(); l != "github-enricher" || !c || e.Record.Output.Label != "notes.write" {
		t.Fatalf("%q %v", l, c)
	}
}

func TestLedgerLabelsAndDispositions(t *testing.T) {
	r := mustRouter(t, rulesJSON, router.Config{})
	ctx := context.Background()
	cases := []struct {
		name  string
		s     router.Subject
		label string
		disp  ledger.Disposition
	}{
		{"no route", subj(nil, "source", "mail"), "no_route", ledger.Rejected},
		{"suggested (low confidence)", subj(f(0.1), "entity.kind", "handoff", "action", "save"), "notes.write", ledger.Suggested},
	}
	for _, c := range cases {
		d, _ := r.Route(ctx, c.s)
		rec, err := d.LedgerRecord(ledger.DefaultRetention(), ledger.Envelope{}, "", false, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := rec.Validate(); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if rec.Output.Label != c.label || rec.Disposition != c.disp {
			t.Errorf("%s: %q %s", c.name, rec.Output.Label, rec.Disposition)
		}
	}
	deny := pol(func(router.Match) (router.Verdict, error) {
		return router.Verdict{Effect: router.Deny, Reason: "x"}, nil
	})
	d, _ := mustRouter(t, rulesJSON, router.Config{Policy: deny}).Route(ctx, subj(f(0.9), "entity.kind", "handoff", "action", "save"))
	rec, _ := d.LedgerRecord(ledger.DefaultRetention(), ledger.Envelope{}, "", false, time.Now())
	if rec.Output.Label != "denied" || rec.Disposition != ledger.Rejected {
		t.Errorf("denied: %q %s", rec.Output.Label, rec.Disposition)
	}
}
