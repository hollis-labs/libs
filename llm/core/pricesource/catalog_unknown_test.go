package pricesource_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/libs/llm/core/costcalc"
	"github.com/hollis-labs/libs/llm/core/modelsdev"
	"github.com/hollis-labs/libs/llm/core/pricesource"
	"github.com/hollis-labs/libs/llm/core/usageledger"
)

func TestCatalogAvailabilitySurvivesAdaptersAndOfflineTable(t *testing.T) {
	raw, err := os.ReadFile("../modelsdev/testdata/catalog-current.json")
	if err != nil {
		t.Fatal(err)
	}
	var providers map[string]modelsdev.Provider
	if err := json.Unmarshal(raw, &providers); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	data, err := json.Marshal(struct {
		Version   int               `json:"schema_version"`
		FetchedAt any               `json:"fetched_at"`
		Data      modelsdev.Catalog `json:"data"`
	}{1, asOf, modelsdev.Catalog{Providers: providers}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "catalog.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	client := modelsdev.New(modelsdev.WithCacheDir(dir), modelsdev.WithLegacyCacheSource("https://models.dev/api.json?type=all"))
	src := pricesource.FromModelsDev(client)
	snap, found := src.Snapshot("relay", "offering")
	if !found || snap.Source != pricesource.SourceModelsDev || !snap.AsOf.Equal(asOf) {
		t.Fatalf("source: %+v found=%v", snap, found)
	}
	wantUnknown := usageledger.UnknownRates{CacheWrite: true, Reasoning: true}
	if snap.UnknownRates != wantUnknown || snap.InputPerMillion != 0 || snap.OutputPerMillion != 2 {
		t.Fatalf("availability: %+v", snap)
	}
	frozen, err := pricesource.FromCatalog("frozen", asOf, client.List())
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "prices.json")
	if err := pricesource.SaveFile(filename, frozen); err != nil {
		t.Fatal(err)
	}
	offline, err := pricesource.LoadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []costcalc.PriceSource{src, frozen, offline} {
		snap, found := s.Snapshot("relay", "offering")
		if !found || snap.UnknownRates != wantUnknown || !snap.AsOf.Equal(asOf) {
			t.Fatalf("lost metadata: %+v", snap)
		}
		if err := snap.Validate(); err != nil {
			t.Fatal(err)
		}
		c := costcalc.Price(usage(), snap)
		if !c.Partial() || c.UnpricedTokens != 500000 || c.Total() != 0.5 {
			t.Fatalf("partial cost: %+v total=%v", c, c.Total())
		}
		unknown, found := s.Snapshot("relay", "unknown")
		if !found || unknown.UnknownRates != (usageledger.UnknownRates{UncachedInput: true, Output: true, CacheRead: true, CacheWrite: true, Reasoning: true}) {
			t.Fatalf("unknown price: %+v", unknown)
		}
		tiered, found := s.Snapshot("relay", "tiered")
		if !found || tiered.UnknownRates != unknown.UnknownRates {
			t.Fatalf("guessed tier: %+v", tiered)
		}
		if _, found := s.Snapshot("relay", "missing"); found {
			t.Fatal("missing model found")
		}
	}
}
