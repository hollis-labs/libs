package pricesource_test

import (
	"bytes"
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	costcalc "github.com/hollis-labs/substrate/llm-core/costcalc"
	"github.com/hollis-labs/substrate/llm-core/modelsdev"
	"github.com/hollis-labs/substrate/llm-core/pricesource"
	usageledger "github.com/hollis-labs/substrate/llm-core/usageledger"
)

type fakeCatalog map[string]modelsdev.Model

func (f fakeCatalog) Get(providerID, modelID string) (modelsdev.Model, bool) {
	m, ok := f[providerID+"/"+modelID]
	return m, ok
}

var asOf = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func usage() usageledger.Usage {
	m := usageledger.ProvenanceMeasured
	u := usageledger.NewUsage()
	u.UncachedInputTokens = usageledger.Component{Tokens: 1_000_000, Provenance: m}
	u.CacheReadTokens = usageledger.Component{Tokens: 2_000_000, Provenance: m}
	u.CacheWriteTokens = usageledger.Component{Tokens: 500_000, Provenance: m}
	u.OutputTokens = usageledger.Component{Tokens: 250_000, Provenance: m}
	return u
}

func TestCatalog_LabelsSnapshot(t *testing.T) {
	cat := fakeCatalog{"anthropic/m": {Cost: modelsdev.Pricing{Input: 3, Output: 15, CacheRead: 0.3}}}
	src := pricesource.Catalog{Catalog: cat, AsOf: func() time.Time { return asOf }}
	s, ok := src.Snapshot("anthropic", "m")
	if !ok {
		t.Fatal("not found")
	}
	if s.Source != pricesource.SourceModelsDev || !s.AsOf.Equal(asOf) {
		t.Fatalf("metadata: %+v", s)
	}
	if s.InputPerMillion != 3 || s.CacheReadPerMillion != 0.3 || s.UnknownRates.Any() {
		t.Fatalf("rates: %+v", s)
	}
	if _, ok := src.Snapshot("anthropic", "missing"); ok {
		t.Fatal("missing model found")
	}
}

func TestFromModelsDev_UsesClient(t *testing.T) {
	c := modelsdev.New(modelsdev.WithCacheDir(t.TempDir()))
	src := pricesource.FromModelsDev(c)
	if src.Name != pricesource.SourceModelsDev || src.Catalog == nil || src.AsOf == nil {
		t.Fatalf("got %+v", src)
	}
	// An empty, never-fetched client has no models.
	if _, ok := src.Snapshot("anthropic", "m"); ok {
		t.Fatal("found a model in an empty client")
	}
}

func TestTable_UnknownRatesAreMarked(t *testing.T) {
	tab, err := pricesource.NewTable("override:team", asOf, pricesource.Entry{
		Provider: "anthropic", Model: "m",
		Input: pricesource.Rate(3), Output: pricesource.Rate(15), CacheRead: pricesource.Rate(0.3),
		// CacheWrite and Reasoning unknown.
	})
	if err != nil {
		t.Fatal(err)
	}
	s, ok := tab.Snapshot("anthropic", "m")
	if !ok {
		t.Fatal("not found")
	}
	want := usageledger.UnknownRates{CacheWrite: true, Reasoning: true}
	if s.UnknownRates != want || s.Source != "override:team" || !s.AsOf.Equal(asOf) {
		t.Fatalf("snapshot: %+v", s)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("snapshot invalid: %v", err)
	}
	c := costcalc.PriceFromSource(tab, "anthropic", "m", usage())
	// 1*3 + 2*0.3 + 0.25*15; the 500k cache-write tokens are unpriced.
	if !near(c.Total(), 3+0.6+3.75) || c.UnpricedTokens != 500_000 || !c.Partial() {
		t.Fatalf("cost: %+v total %v", c, c.Total())
	}
}

func TestTable_ZeroRateIsFree(t *testing.T) {
	z := pricesource.Rate(0)
	tab, err := pricesource.NewTable("local", time.Time{}, pricesource.Entry{
		Provider: "ollama", Model: "m", Input: z, Output: z, CacheWrite: z, CacheRead: z, Reasoning: z,
	})
	if err != nil {
		t.Fatal(err)
	}
	c := costcalc.PriceFromSource(tab, "ollama", "m", usage())
	if !c.Priced || c.Total() != 0 || c.Partial() {
		t.Fatalf("got %+v", c)
	}
}

func TestTable_AnyProviderFallback(t *testing.T) {
	tab, err := pricesource.NewTable("t", time.Time{},
		pricesource.Entry{Provider: pricesource.AnyProvider, Model: "m", Input: pricesource.Rate(1), Output: pricesource.Rate(2)},
		pricesource.Entry{Provider: "exact", Model: "m", Input: pricesource.Rate(9), Output: pricesource.Rate(9)},
	)
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := tab.Snapshot("exact", "m"); s.InputPerMillion != 9 {
		t.Fatalf("exact entry lost to wildcard: %+v", s)
	}
	if s, ok := tab.Snapshot("other", "m"); !ok || s.InputPerMillion != 1 {
		t.Fatalf("wildcard not used: %+v %v", s, ok)
	}
	if _, ok := tab.Snapshot("other", "n"); ok {
		t.Fatal("unknown model found")
	}
}

func TestNewTable_Rejects(t *testing.T) {
	cases := map[string][]pricesource.Entry{
		"empty model":    {{Provider: "p"}},
		"empty provider": {{Model: "m"}},
		"negative":       {{Provider: "p", Model: "m", Input: pricesource.Rate(-1)}},
		"nan":            {{Provider: "p", Model: "m", Output: pricesource.Rate(math.NaN())}},
		"duplicate":      {{Provider: "p", Model: "m"}, {Provider: "p", Model: "m"}},
	}
	for name, entries := range cases {
		if _, err := pricesource.NewTable("t", time.Time{}, entries...); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestNilTable_FindsNothing(t *testing.T) {
	var tab *pricesource.Table
	if _, ok := tab.Snapshot("p", "m"); ok {
		t.Fatal("nil table found a model")
	}
}

func TestChain_FirstFoundWins(t *testing.T) {
	override, _ := pricesource.NewTable("override", time.Time{},
		pricesource.Entry{Provider: "anthropic", Model: "m", Input: pricesource.Rate(1), Output: pricesource.Rate(1)})
	cat := pricesource.Catalog{Catalog: fakeCatalog{
		"anthropic/m": {Cost: modelsdev.Pricing{Input: 3, Output: 15}},
		"anthropic/n": {Cost: modelsdev.Pricing{Input: 5, Output: 25}},
	}}
	src := pricesource.Chain(nil, override, cat)
	if s, _ := src.Snapshot("anthropic", "m"); s.Source != "override" || s.InputPerMillion != 1 {
		t.Fatalf("override not preferred: %+v", s)
	}
	if s, _ := src.Snapshot("anthropic", "n"); s.Source != pricesource.SourceModelsDev || s.InputPerMillion != 5 {
		t.Fatalf("catalog fallback: %+v", s)
	}
	if _, ok := src.Snapshot("anthropic", "x"); ok {
		t.Fatal("chain found a model no source has")
	}
}

func TestLive_SwapsSource(t *testing.T) {
	var l pricesource.Live
	if _, ok := l.Snapshot("p", "m"); ok {
		t.Fatal("zero Live found a model")
	}
	a, _ := pricesource.NewTable("a", time.Time{}, pricesource.Entry{Provider: "p", Model: "m", Input: pricesource.Rate(1)})
	b, _ := pricesource.NewTable("b", time.Time{}, pricesource.Entry{Provider: "p", Model: "m", Input: pricesource.Rate(2)})
	l.Store(a)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				if s, ok := l.Snapshot("p", "m"); !ok || (s.Source != "a" && s.Source != "b") {
					t.Errorf("bad snapshot %+v %v", s, ok)
					return
				}
			}
		}()
	}
	l.Store(b)
	wg.Wait()
	if s, _ := l.Snapshot("p", "m"); s.Source != "b" {
		t.Fatalf("after swap: %+v", s)
	}
	l.Store(nil)
	if _, ok := l.Snapshot("p", "m"); ok {
		t.Fatal("Live with nil source found a model")
	}
}

func TestTable_JSONRoundTripKeepsUnknownRates(t *testing.T) {
	tab, err := pricesource.NewTable("override", asOf,
		pricesource.Entry{Provider: "b", Model: "m", Input: pricesource.Rate(0), Output: pricesource.Rate(4)},
		pricesource.Entry{Provider: "a", Model: "m", Input: pricesource.Rate(1)},
	)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := tab.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	back, err := pricesource.ParseTable(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range [][2]string{{"a", "m"}, {"b", "m"}} {
		s1, _ := tab.Snapshot(key[0], key[1])
		s2, ok := back.Snapshot(key[0], key[1])
		if !ok || s1 != s2 {
			t.Fatalf("%v: %+v != %+v", key, s1, s2)
		}
	}
	// Known zero stays known; absent stays unknown.
	s, _ := back.Snapshot("b", "m")
	if s.UnknownRates.UncachedInput || !s.UnknownRates.CacheRead {
		t.Fatalf("unknown rates lost: %+v", s.UnknownRates)
	}
	// Output is deterministic: entries sorted.
	var buf2 bytes.Buffer
	_ = back.WriteJSON(&buf2)
	if buf.String() != buf2.String() {
		t.Fatalf("re-encoded table differs:\n%s\n%s", buf.String(), buf2.String())
	}
	if strings.Index(buf.String(), `"provider": "a"`) > strings.Index(buf.String(), `"provider": "b"`) {
		t.Fatalf("entries not sorted:\n%s", buf.String())
	}
}

func TestParseTable_RejectsUnknownFields(t *testing.T) {
	_, err := pricesource.ParseTable(strings.NewReader(`{"name":"t","entries":[{"provider":"p","model":"m","input":3}]}`))
	if err == nil {
		t.Fatal("a misspelled rate field was accepted (it would silently read as unknown)")
	}
}

const liteLLMSample = `{
  "sample_spec": {"max_tokens": "LEGACY parameter", "input_cost_per_token": 0.0},
  "claude-x": {"litellm_provider": "anthropic", "input_cost_per_token": 3e-06, "output_cost_per_token": 1.5e-05,
               "cache_read_input_token_cost": 3e-07, "cache_creation_input_token_cost": 3.75e-06},
  "openrouter/vendor/model-y": {"litellm_provider": "openrouter", "input_cost_per_token": 1e-06, "output_cost_per_token": 2e-06,
               "output_cost_per_reasoning_token": 4e-06},
  "free-model": {"litellm_provider": "local", "input_cost_per_token": 0, "output_cost_per_token": 0},
  "no-provider": {"input_cost_per_token": 5e-06},
  "image-only": {"litellm_provider": "x", "output_cost_per_image": 0.04},
  "broken": {"litellm_provider": "x", "input_cost_per_token": "free"}
}`

func TestParseLiteLLM(t *testing.T) {
	tab, skipped, err := pricesource.ParseLiteLLM(strings.NewReader(liteLLMSample), "litellm", asOf)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(skipped, ","), "broken,image-only,sample_spec"; got != want {
		t.Fatalf("skipped = %s, want %s", got, want)
	}
	if tab.Len() != 4 {
		t.Fatalf("Len() = %d, want 4", tab.Len())
	}

	s, ok := tab.Snapshot("anthropic", "claude-x")
	if !ok {
		t.Fatal("claude-x not found")
	}
	if !near(s.InputPerMillion, 3) || !near(s.OutputPerMillion, 15) || !near(s.CacheReadPerMillion, 0.3) || !near(s.CacheWritePerMillion, 3.75) {
		t.Fatalf("per-million conversion: %+v", s)
	}
	if s.UnknownRates != (usageledger.UnknownRates{Reasoning: true}) || s.Source != "litellm" {
		t.Fatalf("claude-x metadata: %+v", s)
	}

	s, ok = tab.Snapshot("openrouter", "vendor/model-y")
	if !ok || !near(s.ReasoningPerMillion, 4) || !s.UnknownRates.CacheRead || !s.UnknownRates.CacheWrite {
		t.Fatalf("provider prefix / reasoning: %+v %v", s, ok)
	}

	s, ok = tab.Snapshot("local", "free-model")
	if !ok || s.InputPerMillion != 0 || s.UnknownRates.UncachedInput || s.UnknownRates.Output {
		t.Fatalf("free model must be known-zero: %+v %v", s, ok)
	}

	if s, ok = tab.Snapshot("anyone", "no-provider"); !ok || !near(s.InputPerMillion, 5) {
		t.Fatalf("no-provider entry: %+v %v", s, ok)
	}

	// Pricing through the seam: reasoning tokens are unknown for claude-x.
	u := usage()
	u.ReasoningTokens = usageledger.Component{Tokens: 100, Provenance: usageledger.ProvenanceMeasured}
	c := costcalc.PriceFromSource(tab, "anthropic", "claude-x", u)
	if !c.Partial() || c.UnpricedTokens != 100 {
		t.Fatalf("cost: %+v", c)
	}
}

func TestParseLiteLLM_RejectsNonObject(t *testing.T) {
	if _, _, err := pricesource.ParseLiteLLM(strings.NewReader(`[1,2]`), "x", time.Time{}); err == nil {
		t.Fatal("accepted a JSON array")
	}
}

func TestSaveLoadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "prices.json")
	tab, _ := pricesource.NewTable("t", asOf, pricesource.Entry{Provider: "p", Model: "m", Input: pricesource.Rate(2)})
	if err := pricesource.SaveFile(path, tab); err != nil {
		t.Fatal(err)
	}
	back, err := pricesource.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s1, _ := tab.Snapshot("p", "m")
	s2, _ := back.Snapshot("p", "m")
	if s1 != s2 || back.Name() != "t" || !back.AsOf().Equal(asOf) {
		t.Fatalf("%+v != %+v", s1, s2)
	}
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "*.tmp-*"))
	if len(matches) != 0 {
		t.Fatalf("temp files left behind: %v", matches)
	}
}

func TestFileCache_Refresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.json")
	cache := pricesource.FileCache{Path: path}
	fresh, _ := pricesource.NewTable("fresh", asOf, pricesource.Entry{Provider: "p", Model: "m", Input: pricesource.Rate(1)})
	fetchErr := errors.New("offline")

	// Nothing saved and fetch fails: no table, both errors.
	if got, err := cache.Refresh(context.Background(), func(context.Context) (*pricesource.Table, error) { return nil, fetchErr }); got != nil || !errors.Is(err, fetchErr) {
		t.Fatalf("cold failure: %v %v", got, err)
	}

	// Fetch succeeds: saved and returned.
	got, err := cache.Refresh(context.Background(), func(context.Context) (*pricesource.Table, error) { return fresh, nil })
	if err != nil || got != fresh {
		t.Fatalf("fresh: %v %v", got, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("not saved: %v", err)
	}

	// Fetch fails later: the saved copy is returned with the fetch error.
	got, err = cache.Refresh(context.Background(), func(context.Context) (*pricesource.Table, error) { return nil, fetchErr })
	if got == nil || got.Name() != "fresh" || !errors.Is(err, fetchErr) {
		t.Fatalf("offline fallback: %v %v", got, err)
	}
	if _, ok := got.Snapshot("p", "m"); !ok {
		t.Fatal("offline table lost its entry")
	}
}

func TestFromCatalog(t *testing.T) {
	refs := []modelsdev.ModelRef{
		{ProviderID: "anthropic", ID: "m", Cost: modelsdev.Pricing{Input: 3, Output: 15, CacheRead: 0.3}},
		{ProviderID: "openai", ID: "n", Cost: modelsdev.Pricing{Input: 1, Output: 4}},
	}
	tab, err := pricesource.FromCatalog(pricesource.SourceModelsDev, asOf, refs)
	if err != nil {
		t.Fatal(err)
	}
	s, ok := tab.Snapshot("anthropic", "m")
	want := costcalc.PriceSnapshotFromPricing(refs[0].Cost)
	want.Source, want.AsOf = pricesource.SourceModelsDev, asOf
	if !ok || s != want {
		t.Fatalf("got %+v, want %+v", s, want)
	}
}
