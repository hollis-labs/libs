package hitlrouting

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/hollis-labs/libs/core/util/router"
)

const sample = `{
  "version": "2026-10-23.1",
  "rules": [
    {"id": "prod-shell", "priority": 10,
     "match": {"kind": "approval", "tool": "shell", "namespace": "prod/*"},
     "sink": "ops-console", "completion": "wait", "delivery": "await"},
    {"id": "forms", "match": {"kind": ["form", "survey"]},
     "sink": "tachyon", "completion": "async", "delivery": "durable_message"},
    {"id": "from-cerberus", "match": {"source": "cerberus"},
     "sink": "cerberus-queue", "completion": "notify", "delivery": "durable_message"},
    {"id": "off", "disabled": true, "match": {"kind": "approval"},
     "sink": "never", "completion": "wait", "delivery": "await"}
  ],
  "catch_all": {"sink": "tachyon", "completion": "async", "delivery": "durable_message"}
}`

func compiled(t *testing.T, doc string) *Router {
	t.Helper()
	p, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	r, err := Compile(p, router.Config{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return r
}

func TestResolve(t *testing.T) {
	r := compiled(t, sample)
	for _, tc := range []struct {
		name  string
		s     Subject
		rule  string
		sink  string
		comp  Completion
		deliv Delivery
	}{
		{"all named fields match, glob namespace", Subject{Kind: "approval", Tool: "shell", Namespace: "prod/eu/db"}, "prod-shell", "ops-console", Wait, Await},
		{"a field the request lacks never matches", Subject{Kind: "approval", Tool: "shell"}, "catch_all", "tachyon", Async, DurableMessage},
		{"namespace outside the glob", Subject{Kind: "approval", Tool: "shell", Namespace: "staging/x"}, "catch_all", "tachyon", Async, DurableMessage},
		{"any-of list, first member", Subject{Kind: "form"}, "forms", "tachyon", Async, DurableMessage},
		{"any-of list, second member", Subject{Kind: "survey", Source: "x"}, "forms", "tachyon", Async, DurableMessage},
		{"match on source alone, in file order after forms", Subject{Kind: "approval", Source: "cerberus"}, "from-cerberus", "cerberus-queue", Notify, DurableMessage},
		{"a disabled rule is skipped", Subject{Kind: "approval"}, "catch_all", "tachyon", Async, DurableMessage},
		{"nothing else matched: the catch-all", Subject{Kind: "something-new"}, "catch_all", "tachyon", Async, DurableMessage},
		{"exact values are case sensitive", Subject{Kind: "Form"}, "catch_all", "tachyon", Async, DurableMessage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.Resolve(context.Background(), "req-1", tc.s)
			if err != nil {
				t.Fatal(err)
			}
			if got.RuleID != tc.rule || got.Sink != tc.sink || got.Completion != tc.comp || got.Delivery != tc.deliv {
				t.Fatalf("got rule=%s sink=%s completion=%s delivery=%s; want %s %s %s %s", got.RuleID, got.Sink, got.Completion, got.Delivery, tc.rule, tc.sink, tc.comp, tc.deliv)
			}
			if got.Decision.SubjectID != "req-1" || got.Decision.Outcome != router.Routed || got.Decision.RuleHash != r.Hash() {
				t.Fatalf("decision record = %+v", got.Decision)
			}
		})
	}
}

func TestPriorityBeatsFileOrderAndTheCatchAllIsLast(t *testing.T) {
	r := compiled(t, `{"version":"v","rules":[
	  {"id":"low","match":{"kind":"approval"},"sink":"a","completion":"wait","delivery":"await"},
	  {"id":"high","priority":5,"match":{"kind":"approval"},"sink":"b","completion":"wait","delivery":"await"},
	  {"id":"negative","priority":-100,"match":{"kind":"approval"},"sink":"c","completion":"wait","delivery":"await"}],
	  "catch_all":{"sink":"z","completion":"notify","delivery":"durable_message"}}`)
	got, err := r.Resolve(context.Background(), "x", Subject{Kind: "approval"})
	if err != nil || got.RuleID != "high" {
		t.Fatalf("got %+v err %v, want the high-priority rule", got, err)
	}
	// Even a deeply negative rule priority stays above the catch-all.
	r2 := compiled(t, `{"version":"v","rules":[{"id":"negative","priority":-100,"match":{"kind":"approval"},"sink":"c","completion":"wait","delivery":"await"}],
	  "catch_all":{"sink":"z","completion":"notify","delivery":"durable_message"}}`)
	if got, err = r2.Resolve(context.Background(), "x", Subject{Kind: "approval"}); err != nil || got.RuleID != "negative" {
		t.Fatalf("got %+v err %v, want negative before the catch-all", got, err)
	}
}

func TestEveryRequestWithAKindGetsASink(t *testing.T) {
	r := compiled(t, `{"version":"v","catch_all":{"sink":"only","completion":"notify","delivery":"await"}}`)
	for _, s := range []Subject{{Kind: "a"}, {Kind: "b", Source: "s", Tool: "t", Namespace: "n"}, {Kind: "weird kind/with*chars"}} {
		got, err := r.Resolve(context.Background(), "x", s)
		if err != nil || got.Sink != "only" || got.RuleID != "catch_all" {
			t.Fatalf("subject %+v: %+v %v", s, got, err)
		}
	}
	if _, err := r.Resolve(context.Background(), "x", Subject{}); err == nil || !strings.Contains(err.Error(), "kind is required") {
		t.Fatalf("a request without a kind: %v", err)
	}
}

func TestParseRejects(t *testing.T) {
	rule := func(extra string) string {
		return `{"version":"v","rules":[{"id":"r","match":{"kind":"k"},"sink":"s","completion":"wait","delivery":"await"` + extra + `}],"catch_all":{"sink":"s","completion":"wait","delivery":"await"}}`
	}
	for name, tc := range map[string]struct{ doc, want string }{
		"no catch-all":                    {`{"version":"v","rules":[]}`, "catch_all: sink is required"},
		"catch-all with a bad completion": {`{"version":"v","catch_all":{"sink":"s","completion":"later","delivery":"await"}}`, `completion "later"`},
		"catch-all with a bad delivery":   {`{"version":"v","catch_all":{"sink":"s","completion":"wait","delivery":"email"}}`, `delivery "email"`},
		"no version":                      {`{"catch_all":{"sink":"s","completion":"wait","delivery":"await"}}`, "version is required"},
		"unknown top-level field":         {`{"version":"v","catch_all":{"sink":"s","completion":"wait","delivery":"await"},"extra":1}`, "unknown field"},
		"unknown rule field":              {rule(`,"surprise":true`), "unknown field"},
		"unknown match field":             {`{"version":"v","rules":[{"id":"r","match":{"colour":"red"},"sink":"s","completion":"wait","delivery":"await"}],"catch_all":{"sink":"s","completion":"wait","delivery":"await"}}`, "unknown field"},
		"trailing data":                   {rule("") + ` {}`, "after the profile"},
		"empty match":                     {`{"version":"v","rules":[{"id":"r","match":{},"sink":"s","completion":"wait","delivery":"await"}],"catch_all":{"sink":"s","completion":"wait","delivery":"await"}}`, "names no field"},
		"duplicate id":                    {`{"version":"v","rules":[` + strings.Repeat(`{"id":"r","match":{"kind":"k"},"sink":"s","completion":"wait","delivery":"await"},`, 2)[:len(strings.Repeat(`{"id":"r","match":{"kind":"k"},"sink":"s","completion":"wait","delivery":"await"},`, 2))-1] + `],"catch_all":{"sink":"s","completion":"wait","delivery":"await"}}`, "duplicate id"},
		"reserved id":                     {`{"version":"v","rules":[{"id":"catch_all","match":{"kind":"k"},"sink":"s","completion":"wait","delivery":"await"}],"catch_all":{"sink":"s","completion":"wait","delivery":"await"}}`, "reserved"},
		"missing id":                      {`{"version":"v","rules":[{"match":{"kind":"k"},"sink":"s","completion":"wait","delivery":"await"}],"catch_all":{"sink":"s","completion":"wait","delivery":"await"}}`, "id is required"},
		"empty sink":                      {`{"version":"v","rules":[{"id":"r","match":{"kind":"k"},"sink":"","completion":"wait","delivery":"await"}],"catch_all":{"sink":"s","completion":"wait","delivery":"await"}}`, "sink is required"},
		"sink with whitespace":            {`{"version":"v","rules":[{"id":"r","match":{"kind":"k"},"sink":"a b","completion":"wait","delivery":"await"}],"catch_all":{"sink":"s","completion":"wait","delivery":"await"}}`, "sink is required"},
		"bad completion":                  {`{"version":"v","rules":[{"id":"r","match":{"kind":"k"},"sink":"s","completion":"blocking","delivery":"await"}],"catch_all":{"sink":"s","completion":"wait","delivery":"await"}}`, `completion "blocking"`},
		"empty completion":                {`{"version":"v","rules":[{"id":"r","match":{"kind":"k"},"sink":"s","delivery":"await"}],"catch_all":{"sink":"s","completion":"wait","delivery":"await"}}`, `completion ""`},
		"empty match value":               {`{"version":"v","rules":[{"id":"r","match":{"kind":""},"sink":"s","completion":"wait","delivery":"await"}],"catch_all":{"sink":"s","completion":"wait","delivery":"await"}}`, "empty value"},
		"glob inside a list":              {`{"version":"v","rules":[{"id":"r","match":{"kind":["a","b*"]},"sink":"s","completion":"wait","delivery":"await"}],"catch_all":{"sink":"s","completion":"wait","delivery":"await"}}`, "not a list member"},
		"match value of the wrong type":   {`{"version":"v","rules":[{"id":"r","match":{"kind":7},"sink":"s","completion":"wait","delivery":"await"}],"catch_all":{"sink":"s","completion":"wait","delivery":"await"}}`, "string or an array"},
		"priority at the catch-all floor": {rule(`,"priority":-2147483648`), "priority must be greater"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tc.doc))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
	// Every problem is reported, not just the first.
	_, err := Parse([]byte(`{"version":"","rules":[{"id":"","match":{},"sink":"","completion":"x","delivery":"y"}],"catch_all":{"sink":"","completion":"x","delivery":"y"}}`))
	if err == nil || strings.Count(err.Error(), "\n") < 5 {
		t.Fatalf("expected many joined errors, got %v", err)
	}
}

func TestHashIsContentNotOrderOfKeys(t *testing.T) {
	a := compiled(t, sample)
	b := compiled(t, sample)
	if a.Hash() == "" || a.Hash() != b.Hash() {
		t.Fatalf("hashes %q %q", a.Hash(), b.Hash())
	}
	c := compiled(t, strings.Replace(sample, `"ops-console"`, `"ops-console-2"`, 1))
	if a.Hash() == c.Hash() {
		t.Fatal("a different sink must change the hash")
	}
}

type denyAll struct{}

func (denyAll) Evaluate(context.Context, router.Subject, router.Match) (router.Verdict, error) {
	return router.Verdict{Effect: router.Deny, Reason: "test"}, nil
}

func TestAPolicyThatDeniesMeansNotRouted(t *testing.T) {
	p, _ := Parse([]byte(sample))
	r, err := Compile(p, router.Config{Policy: denyAll{}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Resolve(context.Background(), "x", Subject{Kind: "approval"})
	var nr *NotRoutedError
	if !errors.Is(err, ErrNotRouted) || !errors.As(err, &nr) || nr.Outcome != router.Denied || got.Sink != "" || got.Decision.Outcome != router.Denied {
		t.Fatalf("got %+v err %v, want a NotRoutedError(denied) with the decision attached", got, err)
	}
}

func TestResolveIsSafeForConcurrentUse(t *testing.T) {
	r := compiled(t, sample)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got, err := r.Resolve(context.Background(), "x", Subject{Kind: "form"}); err != nil || got.RuleID != "forms" {
				t.Errorf("got %+v %v", got, err)
			}
		}()
	}
	wg.Wait()
}

func TestValuesRoundTrip(t *testing.T) {
	p, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	b, err := jsonMarshal(p)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(b)
	if err != nil {
		t.Fatalf("re-parse of %s: %v", b, err)
	}
	if again.Rules[1].Match.Kind[1] != "survey" || len(again.Rules[0].Match.Kind) != 1 {
		t.Fatalf("round trip lost values: %+v", again.Rules)
	}
}
