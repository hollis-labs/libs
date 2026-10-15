package pricesource

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"sync/atomic"
	"time"

	costcalc "github.com/hollis-labs/substrate/llm-core/costcalc"
	"github.com/hollis-labs/substrate/llm-core/modelsdev"
	usageledger "github.com/hollis-labs/substrate/llm-core/usageledger"
)

// SourceModelsDev is the Source label of snapshots read from a models.dev
// catalog.
const SourceModelsDev = "models.dev"

// Catalog adapts a costcalc.Catalog (for example a *modelsdev.Client) as a
// costcalc.PriceSource. Its snapshots carry Name as Source and AsOf() as AsOf.
// A models.dev rate the catalog omits reads as 0 and is not marked unknown:
// the catalog cannot tell an absent optional rate from a free one.
type Catalog struct {
	// Catalog is the model catalog to read. Required.
	Catalog costcalc.Catalog
	// Name is the Source label; empty means SourceModelsDev.
	Name string
	// AsOf, when set, reports when the catalog's data was fetched.
	AsOf func() time.Time
}

var _ costcalc.PriceSource = Catalog{}

// FromModelsDev returns a Catalog source over a models.dev client, labelled
// SourceModelsDev, whose AsOf is the client's LastFetchedAt.
func FromModelsDev(c *modelsdev.Client) Catalog {
	return Catalog{Catalog: c, Name: SourceModelsDev, AsOf: c.LastFetchedAt}
}

// Snapshot implements costcalc.PriceSource.
func (c Catalog) Snapshot(providerID, modelID string) (usageledger.PriceSnapshot, bool) {
	snap, ok := costcalc.SnapshotPrice(c.Catalog, providerID, modelID)
	if !ok {
		return usageledger.PriceSnapshot{}, false
	}
	snap.Source = c.Name
	if snap.Source == "" {
		snap.Source = SourceModelsDev
	}
	if c.AsOf != nil {
		snap.AsOf = c.AsOf()
	}
	return snap, true
}

// AnyProvider is the Entry.Provider value that matches every provider. An
// exact provider entry for the same model wins over it.
const AnyProvider = "*"

// Entry is one model's rates in a Table, in USD per million tokens. A nil
// rate is unknown and is marked in the snapshot's UnknownRates; a rate of 0 is
// free.
type Entry struct {
	Provider   string   `json:"provider"`
	Model      string   `json:"model"`
	Input      *float64 `json:"input_per_million,omitempty"`
	Output     *float64 `json:"output_per_million,omitempty"`
	CacheWrite *float64 `json:"cache_write_per_million,omitempty"`
	CacheRead  *float64 `json:"cache_read_per_million,omitempty"`
	Reasoning  *float64 `json:"reasoning_per_million,omitempty"`
}

// Rate returns a pointer to v, for building an Entry.
func Rate(v float64) *float64 { return &v }

func (e Entry) key() string { return e.Provider + "\x00" + e.Model }

func (e Entry) snapshot() usageledger.PriceSnapshot {
	var s usageledger.PriceSnapshot
	set := func(r *float64, dst *float64, unknown *bool) {
		if r == nil {
			*unknown = true
			return
		}
		*dst = *r
	}
	set(e.Input, &s.InputPerMillion, &s.UnknownRates.UncachedInput)
	set(e.Output, &s.OutputPerMillion, &s.UnknownRates.Output)
	set(e.CacheWrite, &s.CacheWritePerMillion, &s.UnknownRates.CacheWrite)
	set(e.CacheRead, &s.CacheReadPerMillion, &s.UnknownRates.CacheRead)
	set(e.Reasoning, &s.ReasoningPerMillion, &s.UnknownRates.Reasoning)
	return s
}

func (e Entry) validate() error {
	var errs []error
	if e.Provider == "" {
		errs = append(errs, errors.New("empty provider (use \"*\" for any provider)"))
	}
	if e.Model == "" {
		errs = append(errs, errors.New("empty model"))
	}
	for name, r := range map[string]*float64{
		"input": e.Input, "output": e.Output, "cache_write": e.CacheWrite,
		"cache_read": e.CacheRead, "reasoning": e.Reasoning,
	} {
		if r != nil && (math.IsNaN(*r) || math.IsInf(*r, 0) || *r < 0) {
			errs = append(errs, fmt.Errorf("%s rate %v (must be finite and non-negative)", name, *r))
		}
	}
	return errors.Join(errs...)
}

// Table is an immutable price table: override rates, a parsed LiteLLM-style
// table, or a frozen copy of a catalog. It is safe for concurrent use. Build
// one with NewTable, ParseTable, ParseLiteLLM, FromCatalog or LoadFile.
type Table struct {
	name    string
	asOf    time.Time
	entries []Entry
	index   map[string]int
}

var _ costcalc.PriceSource = (*Table)(nil)

// NewTable validates entries and returns a table labelled name, whose
// snapshots carry name as Source and asOf as AsOf. Two entries for the same
// provider and model, an empty provider or model, or a negative or
// non-finite rate is an error.
func NewTable(name string, asOf time.Time, entries ...Entry) (*Table, error) {
	t := &Table{name: name, asOf: asOf, index: make(map[string]int, len(entries))}
	var errs []error
	for i, e := range entries {
		if err := e.validate(); err != nil {
			errs = append(errs, fmt.Errorf("pricesource: entry %d (%s/%s): %w", i, e.Provider, e.Model, err))
			continue
		}
		if _, dup := t.index[e.key()]; dup {
			errs = append(errs, fmt.Errorf("pricesource: entry %d: duplicate %s/%s", i, e.Provider, e.Model))
			continue
		}
		t.index[e.key()] = len(t.entries)
		t.entries = append(t.entries, e)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return t, nil
}

// Name returns the table's Source label.
func (t *Table) Name() string { return t.name }

// AsOf returns when the table's data was fetched or published (zero if not
// recorded).
func (t *Table) AsOf() time.Time { return t.asOf }

// Len returns the number of entries.
func (t *Table) Len() int { return len(t.entries) }

// Entries returns a copy of the entries, sorted by provider then model.
func (t *Table) Entries() []Entry {
	out := append([]Entry(nil), t.entries...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// Snapshot implements costcalc.PriceSource. It looks up the exact provider
// and model first, then an AnyProvider entry for the model.
func (t *Table) Snapshot(providerID, modelID string) (usageledger.PriceSnapshot, bool) {
	if t == nil {
		return usageledger.PriceSnapshot{}, false
	}
	i, ok := t.index[providerID+"\x00"+modelID]
	if !ok {
		i, ok = t.index[AnyProvider+"\x00"+modelID]
	}
	if !ok {
		return usageledger.PriceSnapshot{}, false
	}
	s := t.entries[i].snapshot()
	s.Source = t.name
	s.AsOf = t.asOf
	return s, true
}

// FromCatalog freezes a model catalog listing (for example
// modelsdev.Client.List) into a table labelled name. Each model's five rates
// are copied as known, the same way costcalc.PriceSnapshotFromPricing reads
// them. Use it with FileCache to keep a catalog's prices available offline.
func FromCatalog(name string, asOf time.Time, models []modelsdev.ModelRef) (*Table, error) {
	entries := make([]Entry, 0, len(models))
	for _, m := range models {
		entries = append(entries, Entry{
			Provider:   m.ProviderID,
			Model:      m.ID,
			Input:      Rate(m.Cost.Input),
			Output:     Rate(m.Cost.Output),
			CacheWrite: Rate(m.Cost.CacheWrite),
			CacheRead:  Rate(m.Cost.CacheRead),
			Reasoning:  Rate(m.Cost.Reasoning),
		})
	}
	return NewTable(name, asOf, entries...)
}

// Chain returns a source that asks each source in order and returns the
// first snapshot found. Put override tables before the catalog. A nil source
// in the list is skipped.
func Chain(sources ...costcalc.PriceSource) costcalc.PriceSource {
	return chain(append([]costcalc.PriceSource(nil), sources...))
}

type chain []costcalc.PriceSource

func (c chain) Snapshot(providerID, modelID string) (usageledger.PriceSnapshot, bool) {
	for _, s := range c {
		if s == nil {
			continue
		}
		if snap, ok := s.Snapshot(providerID, modelID); ok {
			return snap, true
		}
	}
	return usageledger.PriceSnapshot{}, false
}

// Live is a source whose underlying source can be replaced while readers use
// it, for a table that is refreshed in the background. The zero value has no
// source and finds nothing. It is safe for concurrent use.
type Live struct {
	v atomic.Pointer[holder]
}

type holder struct{ src costcalc.PriceSource }

var _ costcalc.PriceSource = (*Live)(nil)

// Store replaces the underlying source. A nil source makes Live find nothing.
func (l *Live) Store(src costcalc.PriceSource) { l.v.Store(&holder{src: src}) }

// Snapshot implements costcalc.PriceSource.
func (l *Live) Snapshot(providerID, modelID string) (usageledger.PriceSnapshot, bool) {
	h := l.v.Load()
	if h == nil || h.src == nil {
		return usageledger.PriceSnapshot{}, false
	}
	return h.src.Snapshot(providerID, modelID)
}
