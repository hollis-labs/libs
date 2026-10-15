package usageledger

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func measured(n int64) Component  { return Component{Tokens: n, Provenance: ProvenanceMeasured} }
func estimated(n int64) Component { return Component{Tokens: n, Provenance: ProvenanceEstimated} }

func TestProvenance_Valid(t *testing.T) {
	for _, p := range []Provenance{ProvenanceMeasured, ProvenanceEstimated, ProvenanceUnknown} {
		if !p.Valid() {
			t.Errorf("%q should be valid", p)
		}
	}
	for _, p := range []Provenance{"", "measurd", "Measured", "UNKNOWN", " unknown"} {
		if p.Valid() {
			t.Errorf("%q should be invalid", p)
		}
	}
}

func TestNewUsage_DefaultsUnknown(t *testing.T) {
	u := NewUsage()
	for _, c := range u.core() {
		if c.Tokens != 0 || c.Provenance != ProvenanceUnknown {
			t.Errorf("%s = %+v, want {0 unknown}", c.name, c.Component)
		}
	}
	if u.Dims != nil {
		t.Errorf("Dims = %v, want nil", u.Dims)
	}
	if err := u.Validate(); err != nil {
		t.Errorf("NewUsage().Validate() = %v", err)
	}
	if u.TotalTokens() != 0 || u.TotalProvenance() != ProvenanceUnknown {
		t.Errorf("total = %d/%s, want 0/unknown", u.TotalTokens(), u.TotalProvenance())
	}
}

func TestUsage_Validate_RejectsBareLiteral(t *testing.T) {
	err := Usage{}.Validate()
	if err == nil {
		t.Fatal("bare Usage{} must fail Validate")
	}
	for _, name := range []string{"uncached_input_tokens", "cache_read_tokens", "cache_write_tokens", "output_tokens", "reasoning_tokens"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not name %s: %v", name, err)
		}
	}
}

func TestUsage_Validate_RejectsUnknownWithNonzeroTokens(t *testing.T) {
	fields := map[string]func(*Usage, Component){
		"uncached_input_tokens": func(u *Usage, c Component) { u.UncachedInputTokens = c },
		"cache_read_tokens":     func(u *Usage, c Component) { u.CacheReadTokens = c },
		"cache_write_tokens":    func(u *Usage, c Component) { u.CacheWriteTokens = c },
		"output_tokens":         func(u *Usage, c Component) { u.OutputTokens = c },
		"reasoning_tokens":      func(u *Usage, c Component) { u.ReasoningTokens = c },
	}
	for name, set := range fields {
		u := NewUsage()
		set(&u, Component{Tokens: 5, Provenance: ProvenanceUnknown})
		err := u.Validate()
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: err = %v, want error naming the field", name, err)
		}
	}
	u := NewUsage()
	u.SetDim("x", Component{Tokens: 1, Provenance: ProvenanceUnknown})
	if u.Validate() == nil {
		t.Error("unknown dim with tokens must fail")
	}
}

func TestUsage_Validate_RejectsNegativeTokensAndBadDims(t *testing.T) {
	u := NewUsage()
	u.OutputTokens = measured(-1)
	if u.Validate() == nil {
		t.Error("negative tokens must fail")
	}
	u = NewUsage()
	u.SetDim("", measured(1))
	if u.Validate() == nil {
		t.Error("empty dims key must fail")
	}
	u = NewUsage()
	u.SetDim("tool_input", Component{Tokens: 1})
	if u.Validate() == nil {
		t.Error("dim with empty provenance must fail")
	}
	u = NewUsage()
	u.SetDim("tool_input", measured(7))
	if err := u.Validate(); err != nil {
		t.Errorf("valid dim rejected: %v", err)
	}
}

func TestUsage_Validate_RejectsDimsCollision(t *testing.T) {
	for _, key := range []string{"output_tokens", "OUTPUT_TOKENS", "Reasoning_Tokens", "uncached_input_tokens", "cache_read_tokens", "cache_write_tokens"} {
		u := NewUsage()
		u.SetDim(key, measured(1))
		err := u.Validate()
		if err == nil || !strings.Contains(err.Error(), "collides") {
			t.Errorf("key %q: err = %v, want collision error", key, err)
		}
	}
}

// The Nanite regression (apps/nanite/internal/store/usage.go: total_tokens =
// input + output, while cache tokens are stored in their own columns).
func TestUsage_TotalTokens_SumsAllFiveAndDims(t *testing.T) {
	u := NewUsage()
	u.UncachedInputTokens = measured(1)
	u.CacheReadTokens = measured(10)
	u.CacheWriteTokens = measured(100)
	u.OutputTokens = measured(1000)
	u.ReasoningTokens = estimated(10000)
	u.SetDim("tool_input", measured(100000))
	u.SetDim("audio", estimated(1000000))
	if got, want := u.TotalTokens(), int64(1111111); got != want {
		t.Fatalf("TotalTokens() = %d, want %d", got, want)
	}

	// The defect on the naive approach: a stored total computed as
	// input+output omits every other component and diverges from the parts.
	naiveStored := u.UncachedInputTokens.Tokens + u.OutputTokens.Tokens
	if naiveStored == u.TotalTokens() {
		t.Fatal("naive input+output total unexpectedly equals the disjoint sum")
	}
	// The derived total cannot drift: mutate a component, the total follows.
	u.CacheReadTokens = measured(11)
	if got, want := u.TotalTokens(), int64(1111112); got != want {
		t.Fatalf("after mutation TotalTokens() = %d, want %d", got, want)
	}
	if naiveStored != 1001 {
		t.Fatalf("naive stored total = %d, fixture assumption broken", naiveStored)
	}
}

func TestUsage_UnreportedIsNotMeasuredZero(t *testing.T) {
	// Tether's Anthropic adapter fills four of five fields and leaves
	// reasoning at Go's zero value, indistinguishable from "used 0".
	type naiveUsage struct{ In, Out, CacheRead, CacheWrite, Reasoning int }
	unreported, usedZero := naiveUsage{In: 1, Out: 2}, naiveUsage{In: 1, Out: 2, Reasoning: 0}
	if unreported != usedZero {
		t.Fatal("fixture: naive shape should be unable to tell the cases apart")
	}

	a := NewUsage() // reasoning never reported
	a.UncachedInputTokens, a.OutputTokens = measured(1), measured(2)
	a.CacheReadTokens, a.CacheWriteTokens = measured(0), measured(0)
	b := a
	b.ReasoningTokens = measured(0) // provider reported zero reasoning
	if a.ReasoningTokens == b.ReasoningTokens {
		t.Error("ledger must distinguish unreported from measured zero")
	}
	if a.TotalProvenance() != ProvenanceUnknown || b.TotalProvenance() != ProvenanceMeasured {
		t.Errorf("provenance = %s/%s, want unknown/measured", a.TotalProvenance(), b.TotalProvenance())
	}
	if a.TotalTokens() != b.TotalTokens() {
		t.Error("totals should match; only provenance differs")
	}
}

func TestUsage_TotalProvenance_WorstOf(t *testing.T) {
	all := func() Usage {
		u := NewUsage()
		u.UncachedInputTokens, u.CacheReadTokens, u.CacheWriteTokens = measured(1), measured(1), measured(1)
		u.OutputTokens, u.ReasoningTokens = measured(1), measured(1)
		return u
	}
	u := all()
	if got := u.TotalProvenance(); got != ProvenanceMeasured {
		t.Errorf("all measured: %s", got)
	}
	u.CacheReadTokens = estimated(1)
	if got := u.TotalProvenance(); got != ProvenanceEstimated {
		t.Errorf("one estimated: %s", got)
	}
	u = all()
	u.ReasoningTokens = unknownComponent() // the live Tether case
	if got := u.TotalProvenance(); got != ProvenanceUnknown {
		t.Errorf("reasoning unknown: %s", got)
	}
	u = all()
	u.SetDim("x", estimated(1))
	if got := u.TotalProvenance(); got != ProvenanceEstimated {
		t.Errorf("estimated dim: %s", got)
	}
	u.SetDim("y", unknownComponent())
	if got := u.TotalProvenance(); got != ProvenanceUnknown {
		t.Errorf("unknown dim: %s", got)
	}
	// A bare literal must never look measured.
	if got := (Usage{}).TotalProvenance(); got != ProvenanceUnknown {
		t.Errorf("bare literal: %s, want unknown", got)
	}
}

func TestRow_PriceNilByDefault(t *testing.T) {
	if (Row{}).Price != nil {
		t.Error("zero Row must have nil Price")
	}
	r := Row{Provider: "p", Usage: NewUsage()}
	if r.Provider == "" || r.Usage.OutputTokens.Provenance != ProvenanceUnknown || r.Price != nil {
		t.Error("Price must only be set by the caller")
	}
}

func TestRow_JSONRoundTrip(t *testing.T) {
	ts := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

	bare := Row{SessionID: "s", Provider: "p", Model: "m", Usage: NewUsage(), RecordedAt: ts}
	data, err := json.Marshal(bare)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"dims"`) || strings.Contains(string(data), `"price"`) || strings.Contains(string(data), `"message_id"`) {
		t.Errorf("omitempty not honored: %s", data)
	}
	var back Row
	if err = json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(bare, back) {
		t.Errorf("bare round trip mismatch:\n%+v\n%+v", bare, back)
	}

	u := NewUsage()
	u.UncachedInputTokens = measured(3)
	u.ReasoningTokens = estimated(4)
	u.SetDim("tool_input", measured(5))
	full := Row{
		SessionID: "s", MessageID: "m1", Provider: "p", Model: "m", Usage: u,
		Price:      &PriceSnapshot{InputPerMillion: 3, OutputPerMillion: 15, CacheReadPerMillion: 0.3},
		RecordedAt: ts,
	}
	data, err = json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"dims":{"tool_input"`, `"price":{"input_per_million":3`, `"cache_read_per_million":0.3`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing %s in %s", want, data)
		}
	}
	if strings.Contains(string(data), "cache_write_per_million") || strings.Contains(string(data), "reasoning_per_million") {
		t.Errorf("zero optional rates should be omitted: %s", data)
	}
	back = Row{}
	if err = json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(full, back) {
		t.Errorf("full round trip mismatch:\n%+v\n%+v", full, back)
	}
	if back.Usage.Validate() != nil {
		t.Error("round-tripped usage should validate")
	}
}

// Usage values are plain data; concurrent reads of one value are safe.
func TestUsage_ConcurrentReads_RaceSafe(t *testing.T) {
	u := NewUsage()
	u.OutputTokens = measured(2)
	u.SetDim("a", measured(1))
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 200 {
				if u.TotalTokens() != 3 || u.Validate() != nil || u.TotalProvenance() != ProvenanceUnknown {
					t.Error("unexpected result")
					return
				}
			}
		})
	}
	wg.Wait()
}
