package usageledger

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

// legacyRowJSON is a Row as written before cost_kind and the snapshot's
// source fields existed.
const legacyRowJSON = `{"session_id":"s","message_id":"m1","provider":"p","model":"m",` +
	`"usage":{"uncached_input_tokens":{"tokens":3,"provenance":"measured"},` +
	`"cache_read_tokens":{"tokens":0,"provenance":"unknown"},` +
	`"cache_write_tokens":{"tokens":0,"provenance":"unknown"},` +
	`"output_tokens":{"tokens":1,"provenance":"measured"},` +
	`"reasoning_tokens":{"tokens":0,"provenance":"unknown"}},` +
	`"price":{"input_per_million":3,"output_per_million":15,"cache_read_per_million":0.3},` +
	`"recorded_at":"2026-09-29T12:00:00Z"}`

func TestRow_LegacyJSONDecodesAndReencodesUnchanged(t *testing.T) {
	var r Row
	if err := json.Unmarshal([]byte(legacyRowJSON), &r); err != nil {
		t.Fatal(err)
	}
	if r.CostKind != CostKindUnspecified {
		t.Fatalf("legacy row CostKind = %q, want unspecified", r.CostKind)
	}
	if r.Price.Source != "" || !r.Price.AsOf.IsZero() || r.Price.UnknownRates.Any() {
		t.Fatalf("legacy snapshot grew metadata: %+v", *r.Price)
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("legacy row fails validation: %v", err)
	}
	out, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != legacyRowJSON {
		t.Fatalf("re-encoded legacy row changed:\n got %s\nwant %s", out, legacyRowJSON)
	}
}

func TestRow_CostKindAndSnapshotMetadataRoundTrip(t *testing.T) {
	u := NewUsage()
	u.UncachedInputTokens = measured(3)
	u.CacheReadTokens = measured(7)
	r := Row{
		SessionID: "s", Provider: "p", Model: "m", Usage: u,
		Price: &PriceSnapshot{
			InputPerMillion: 3, OutputPerMillion: 15,
			Source:       "override:team",
			AsOf:         time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
			UnknownRates: UnknownRates{CacheRead: true},
		},
		CostKind:   CostKindSubscriptionEquivalent,
		RecordedAt: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC),
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"cost_kind":"subscription_equivalent"`, `"source":"override:team"`,
		`"as_of":"2026-10-01T00:00:00Z"`, `"unknown_rates":{"cache_read_tokens":true}`,
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing %s in %s", want, data)
		}
	}
	var back Row
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r, back) {
		t.Fatalf("round trip mismatch:\n%+v\n%+v", r, back)
	}
}

func TestCostKind_ValidAndIsBill(t *testing.T) {
	cases := map[CostKind]bool{
		CostKindUnspecified:            false,
		CostKindAPIBilled:              true,
		CostKindAPIEstimated:           false,
		CostKindSubscriptionEquivalent: false,
		CostKindLocalCompute:           false,
	}
	for k, bill := range cases {
		if !k.Valid() {
			t.Errorf("%q: Valid() = false", k)
		}
		if k.IsBill() != bill {
			t.Errorf("%q: IsBill() = %v, want %v", k, k.IsBill(), bill)
		}
	}
	for _, bad := range []CostKind{"billed", "API_BILLED", "subscription"} {
		if bad.Valid() || bad.IsBill() {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestRow_Validate(t *testing.T) {
	good := Row{Usage: NewUsage(), CostKind: CostKindAPIEstimated, Price: &PriceSnapshot{InputPerMillion: 1}}
	if err := good.Validate(); err != nil {
		t.Fatalf("good row: %v", err)
	}
	if err := (Row{Usage: NewUsage()}).Validate(); err != nil {
		t.Fatalf("row with no price and no kind: %v", err)
	}

	bad := Row{
		Usage:    Usage{},
		CostKind: "bill",
		Price:    &PriceSnapshot{InputPerMillion: -1, CacheReadPerMillion: 2, UnknownRates: UnknownRates{CacheRead: true}},
	}
	err := bad.Validate()
	if err == nil {
		t.Fatal("bad row validated")
	}
	for _, want := range []string{"cost_kind", "uncached_input_tokens", "rate for uncached_input_tokens", "cache_read_tokens is marked unknown"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
}

func TestPriceSnapshot_ValidateRejectsNonFinite(t *testing.T) {
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if err := (PriceSnapshot{OutputPerMillion: v}).Validate(); err == nil {
			t.Errorf("rate %v accepted", v)
		}
	}
}

func TestPriceSnapshot_RateKnown(t *testing.T) {
	p := PriceSnapshot{UnknownRates: UnknownRates{Output: true}}
	for _, n := range CoreComponentNames() {
		if got, want := p.RateKnown(n), n != "output_tokens"; got != want {
			t.Errorf("RateKnown(%q) = %v, want %v", n, got, want)
		}
	}
	if p.RateKnown("tool_input") {
		t.Error("a non-core name reported a known rate")
	}
}

// The UnknownRates JSON names must be the Usage core JSON tags, so a reader
// of the wire format can match them to components.
func TestUnknownRates_JSONNamesMatchUsageTags(t *testing.T) {
	ut := reflect.TypeOf(Usage{})
	rt := reflect.TypeOf(UnknownRates{})
	if rt.NumField() != len(coreComponentNames) {
		t.Fatalf("UnknownRates has %d fields, want %d", rt.NumField(), len(coreComponentNames))
	}
	for i := range rt.NumField() {
		rtag := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
		utag := strings.Split(ut.Field(i).Tag.Get("json"), ",")[0]
		if rtag != utag || rtag != coreComponentNames[i] {
			t.Errorf("field %d: UnknownRates tag %q, Usage tag %q, core name %q", i, rtag, utag, coreComponentNames[i])
		}
	}
}

// PriceSnapshot stays comparable; existing callers compare it with ==.
func TestPriceSnapshot_Comparable(t *testing.T) {
	a := PriceSnapshot{InputPerMillion: 1, UnknownRates: UnknownRates{Reasoning: true}}
	b := a
	if a != b {
		t.Fatal("equal snapshots compare unequal")
	}
	b.UnknownRates.Reasoning = false
	if a == b {
		t.Fatal("snapshots differing in UnknownRates compare equal")
	}
}

func TestCoreComponentNames_IsACopy(t *testing.T) {
	n := CoreComponentNames()
	n[0] = "changed"
	if CoreComponentNames()[0] != "uncached_input_tokens" {
		t.Fatal("CoreComponentNames exposed its backing array")
	}
}
