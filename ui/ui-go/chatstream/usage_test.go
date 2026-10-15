package chatstream

import (
	"encoding/json"
	"errors"
	"math/rand"
	"testing"
)

// The provider shapes disagree about what their numbers include. Whatever the
// shape, the disjoint components must add up to what the provider billed:
// prompt + completion, once each.
func TestUsageTotalNeverDoubleCounts(t *testing.T) {
	r := rand.New(rand.NewSource(1)) //nolint:gosec // a fixed seed: the property test must be reproducible
	for i := 0; i < 2000; i++ {
		// OpenAI shape: prompt_tokens includes cached, completion includes reasoning.
		prompt := r.Intn(5000)
		cached := r.Intn(prompt + 1)
		completion := r.Intn(3000)
		reasoning := r.Intn(completion + 1)
		u, err := UsageFromInclusive(UsageFinal, prompt, cached, 0, completion, reasoning)
		if err != nil {
			t.Fatalf("inclusive %d/%d/%d/%d: %v", prompt, cached, completion, reasoning, err)
		}
		if got, want := u.Total(), prompt+completion; got != want {
			t.Fatalf("inclusive: Total = %d, provider billed %d (prompt %d cached %d completion %d reasoning %d)",
				got, want, prompt, cached, completion, reasoning)
		}
		if u.Input() != prompt || u.Generated() != completion {
			t.Fatalf("inclusive: Input %d Generated %d, want %d %d", u.Input(), u.Generated(), prompt, completion)
		}

		// Anthropic shape: input_tokens excludes cache; the three add up to the prompt.
		in, read, write, out := r.Intn(4000), r.Intn(4000), r.Intn(4000), r.Intn(3000)
		a, err := UsageFromExclusiveInput(UsageCumulative, in, read, write, out)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := a.Total(), in+read+write+out; got != want {
			t.Fatalf("exclusive: Total = %d, want %d", got, want)
		}
	}
}

// The same real-world prompt, reported both ways, gives the same components.
func TestUsageShapesConverge(t *testing.T) {
	// 1000-token prompt of which 600 came from cache; 200 generated.
	openai, err := UsageFromInclusive(UsageFinal, 1000, 600, 0, 200, 0)
	if err != nil {
		t.Fatal(err)
	}
	anthropic, err := UsageFromExclusiveInput(UsageFinal, 400, 600, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	if openai.UncachedInput != anthropic.UncachedInput || openai.Total() != anthropic.Total() {
		t.Fatalf("openai %+v vs anthropic %+v", openai, anthropic)
	}
	// Summing input plus cache on the OpenAI numbers is the double count the
	// disjoint type exists to prevent.
	if naive := 1000 + 600 + 200; naive == openai.Total() {
		t.Fatal("test is vacuous: naive sum equals the total")
	}
}

func TestUsageFromInclusiveRejectsSubsetsLargerThanTotals(t *testing.T) {
	for _, tc := range []struct{ prompt, cr, cw, comp, reason int }{
		{100, 101, 0, 10, 0},
		{100, 60, 50, 10, 0},
		{100, 0, 0, 10, 11},
	} {
		if _, err := UsageFromInclusive(UsageFinal, tc.prompt, tc.cr, tc.cw, tc.comp, tc.reason); !errors.Is(err, ErrUsageInconsistent) {
			t.Errorf("%+v: err = %v", tc, err)
		}
	}
	if _, err := UsageFromExclusiveInput(UsageFinal, -1, 0, 0, 0); err == nil {
		t.Error("a negative component must be rejected")
	}
}

func TestUsageExtraIsNeverInTotal(t *testing.T) {
	u := Usage{UncachedInput: 1, Output: 2, Extra: map[string]int{"server_tool_calls": 100}}
	if u.Total() != 3 {
		t.Fatalf("Total = %d", u.Total())
	}
	if (Usage{Extra: map[string]int{"x": 5}}).IsZero() {
		t.Error("extra counters make a usage non-zero")
	}
	if !(Usage{}).IsZero() {
		t.Error("the zero usage is zero")
	}
}

func TestUsageAddAndSub(t *testing.T) {
	a := Usage{Scope: UsageCumulative, UncachedInput: 10, CacheRead: 5, Output: 7, Extra: map[string]int{"x": 1}}
	b := Usage{UncachedInput: 4, CacheRead: 5, Output: 3, Reasoning: 2, Extra: map[string]int{"x": 2, "y": 1}}
	sum := a.Add(b)
	if sum.UncachedInput != 14 || sum.CacheRead != 10 || sum.Output != 10 || sum.Reasoning != 2 || sum.Extra["x"] != 3 || sum.Extra["y"] != 1 || sum.Scope != UsageCumulative {
		t.Fatalf("Add = %+v", sum)
	}
	d, err := sum.Sub(a)
	if err != nil {
		t.Fatal(err)
	}
	if d.Scope != UsageDelta || d.UncachedInput != 4 || d.CacheRead != 5 || d.Output != 3 || d.Reasoning != 2 {
		t.Fatalf("Sub = %+v", d)
	}
	// a cumulative counter that goes down is a decoding bug, not a delta
	if _, err := a.Sub(sum); err == nil {
		t.Error("Sub must refuse a cumulative usage that went backwards")
	}
	if a.Extra["x"] != 1 {
		t.Error("Add must not modify its receiver's Extra")
	}
}

func TestUsageJSONCarriesDerivedTotalAndIgnoresIt(t *testing.T) {
	u := Usage{Scope: UsageFinal, UncachedInput: 3, CacheRead: 4, Output: 5, Reasoning: 1}
	raw, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["total"] != float64(13) {
		t.Fatalf("total = %v in %s", m["total"], raw)
	}
	var back Usage
	if err := json.Unmarshal([]byte(`{"scope":"final","uncached_input":3,"cache_read":4,"output":5,"reasoning":1,"total":999}`), &back); err != nil {
		t.Fatal(err)
	}
	if back.Total() != 13 {
		t.Fatalf("a stored total must not override the components: Total = %d", back.Total())
	}
}
