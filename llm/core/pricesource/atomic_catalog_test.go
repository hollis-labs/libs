package pricesource_test

import (
	"github.com/hollis-labs/libs/llm/core/modelsdev"
	"github.com/hollis-labs/libs/llm/core/pricesource"
	"testing"
	"time"
)

type atomicCatalog struct{}

func (atomicCatalog) Get(string, string) (modelsdev.Model, bool) { panic("non-atomic lookup") }
func (atomicCatalog) GetWithFetchedAt(string, string) (modelsdev.Model, time.Time, bool) {
	return modelsdev.Model{Cost: modelsdev.Pricing{Input: 3}}, asOf, true
}
func TestCatalogUsesAtomicPriceProvenance(t *testing.T) {
	src := pricesource.Catalog{Catalog: atomicCatalog{}, AsOf: func() time.Time { panic("timestamp from a different snapshot") }}
	snap, ok := src.Snapshot("p", "m")
	if !ok || snap.InputPerMillion != 3 || !snap.AsOf.Equal(asOf) {
		t.Fatalf("snapshot: %+v", snap)
	}
}
