package usageledger

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Provenance records how one usage component's token count was obtained.
//
// It has no valid zero value on purpose: an unset Provenance ("") is invalid
// and Usage.Validate rejects it, so a component can never silently read as
// "measured 0" just because nothing was set on it.
type Provenance string

const (
	// ProvenanceMeasured means the provider reported this figure for this call.
	ProvenanceMeasured Provenance = "measured"
	// ProvenanceEstimated means the figure was derived (tokenizer count,
	// ratio backfill, and so on).
	ProvenanceEstimated Provenance = "estimated"
	// ProvenanceUnknown means the figure was neither reported nor estimated.
	// Tokens MUST be 0 for an unknown component.
	ProvenanceUnknown Provenance = "unknown"
)

// Valid reports whether p is one of the three defined provenance values.
func (p Provenance) Valid() bool {
	switch p {
	case ProvenanceMeasured, ProvenanceEstimated, ProvenanceUnknown:
		return true
	}
	return false
}

// rank orders provenance by how much trust it deserves: lower is better.
// An invalid provenance ranks as unknown, so a component nobody filled in
// can never make a total look better than it is.
func (p Provenance) rank() int {
	switch p {
	case ProvenanceMeasured:
		return 0
	case ProvenanceEstimated:
		return 1
	case ProvenanceUnknown:
		return 2
	}
	return 2
}

// Component is one disjoint usage dimension: a token count plus how it was
// obtained. Tokens is always 0 when Provenance is ProvenanceUnknown, and
// Validate enforces that both ways (unknown implies zero; non-zero implies
// not unknown).
type Component struct {
	Tokens     int64      `json:"tokens"`
	Provenance Provenance `json:"provenance"`
}

func unknownComponent() Component { return Component{Provenance: ProvenanceUnknown} }

// Usage is the fixed disjoint core for one LLM call's token accounting, plus
// Dims for provider-specific components the core does not name yet.
//
// The five core fields line up one to one with the five per-million-token
// rates a model catalog prices (input, output, cache write, cache read,
// reasoning). The components are disjoint: a token is counted in exactly one
// of them, so the total is their sum.
type Usage struct {
	UncachedInputTokens Component            `json:"uncached_input_tokens"`
	CacheReadTokens     Component            `json:"cache_read_tokens"`
	CacheWriteTokens    Component            `json:"cache_write_tokens"`
	OutputTokens        Component            `json:"output_tokens"`
	ReasoningTokens     Component            `json:"reasoning_tokens"`
	Dims                map[string]Component `json:"dims,omitempty"`
}

// NewUsage returns a Usage with every core component set to
// ProvenanceUnknown and 0 tokens. A component the caller never touches reads
// as "unknown", never as "measured 0". Building a Usage by bare struct
// literal instead is caught by Validate.
func NewUsage() Usage {
	return Usage{
		UncachedInputTokens: unknownComponent(),
		CacheReadTokens:     unknownComponent(),
		CacheWriteTokens:    unknownComponent(),
		OutputTokens:        unknownComponent(),
		ReasoningTokens:     unknownComponent(),
	}
}

// SetDim lazily allocates Dims and sets one extension component. Use it
// rather than assigning into u.Dims directly, which panics on a nil map.
func (u *Usage) SetDim(name string, c Component) {
	if u.Dims == nil {
		u.Dims = make(map[string]Component)
	}
	u.Dims[name] = c
}

// TotalTokens is derived, never stored: the sum of the five core components
// plus every Dims entry. There is nowhere else to write a total, so it cannot
// drift from the components it summarizes.
//
// Treat it as a floor, not a fact, whenever TotalProvenance is not
// ProvenanceMeasured: an unknown component contributes 0 by construction.
func (u Usage) TotalTokens() int64 {
	total := u.UncachedInputTokens.Tokens + u.CacheReadTokens.Tokens +
		u.CacheWriteTokens.Tokens + u.OutputTokens.Tokens + u.ReasoningTokens.Tokens
	for _, c := range u.Dims {
		total += c.Tokens
	}
	return total
}

// TotalProvenance reports the worst provenance across every component that
// feeds TotalTokens: unknown beats estimated beats measured. A component
// whose Provenance is invalid (for example an unset field in a bare struct
// literal) counts as unknown.
func (u Usage) TotalProvenance() Provenance {
	worst := ProvenanceMeasured
	consider := func(p Provenance) {
		if p.rank() > worst.rank() {
			worst = ProvenanceUnknown
			if p.rank() == 1 {
				worst = ProvenanceEstimated
			}
		}
	}
	for _, c := range u.core() {
		consider(c.Provenance)
	}
	for _, c := range u.Dims {
		consider(c.Provenance)
	}
	return worst
}

type namedComponent struct {
	name string // JSON tag of the core field
	Component
}

func (u Usage) core() []namedComponent {
	return []namedComponent{
		{"uncached_input_tokens", u.UncachedInputTokens},
		{"cache_read_tokens", u.CacheReadTokens},
		{"cache_write_tokens", u.CacheWriteTokens},
		{"output_tokens", u.OutputTokens},
		{"reasoning_tokens", u.ReasoningTokens},
	}
}

func validateComponent(label string, c Component) error {
	if !c.Provenance.Valid() {
		return fmt.Errorf("usageledger: %s: invalid provenance %q", label, string(c.Provenance))
	}
	if c.Tokens < 0 {
		return fmt.Errorf("usageledger: %s: negative tokens %d", label, c.Tokens)
	}
	if c.Provenance == ProvenanceUnknown && c.Tokens != 0 {
		return fmt.Errorf("usageledger: %s: provenance unknown but tokens is %d (must be 0)", label, c.Tokens)
	}
	return nil
}

// Validate rejects a Usage that is not internally consistent. Every core
// component and every Dims entry must have a valid Provenance and
// non-negative Tokens, and an unknown component must have 0 tokens. A Dims
// key that is empty or that equals (case-insensitively) a core field's JSON
// tag is rejected, so TotalTokens can never double-count. All problems are
// reported, joined; the message names each offending field.
func (u Usage) Validate() error {
	var errs []error
	for _, c := range u.core() {
		if err := validateComponent(c.name, c.Component); err != nil {
			errs = append(errs, err)
		}
	}
	keys := make([]string, 0, len(u.Dims))
	for k := range u.Dims {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k == "" {
			errs = append(errs, errors.New("usageledger: dims: empty key"))
			continue
		}
		collides := false
		for _, c := range u.core() {
			if strings.EqualFold(k, c.name) {
				errs = append(errs, fmt.Errorf("usageledger: dims[%q]: key collides with core field %q", k, c.name))
				collides = true
				break
			}
		}
		if collides {
			continue
		}
		if err := validateComponent(fmt.Sprintf("dims[%q]", k), u.Dims[k]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// PriceSnapshot carries the per-million-token rates in effect when a Row was
// recorded. It mirrors the five rates of a model catalog's pricing type by
// value, not by import, and is inert data: this package never multiplies it
// against a Usage.
//
// Source, AsOf and UnknownRates describe where the rates came from. They are
// optional and omitted from JSON when empty, so a snapshot written before they
// existed decodes and re-encodes unchanged.
type PriceSnapshot struct {
	InputPerMillion      float64 `json:"input_per_million"`
	OutputPerMillion     float64 `json:"output_per_million"`
	CacheWritePerMillion float64 `json:"cache_write_per_million,omitempty"`
	CacheReadPerMillion  float64 `json:"cache_read_per_million,omitempty"`
	ReasoningPerMillion  float64 `json:"reasoning_per_million,omitempty"`

	// Source names the price table the rates were read from (for example
	// "models.dev" or the name of an override table). Empty means not recorded.
	Source string `json:"source,omitempty"`
	// AsOf is when the source's data was fetched or published. Zero means not
	// recorded.
	AsOf time.Time `json:"as_of,omitzero"`
	// UnknownRates marks the rates the source did not know. A marked rate
	// field is 0 and means "unknown", not "free". An unmarked rate of 0 is
	// free, or the source cannot tell the two apart.
	UnknownRates UnknownRates `json:"unknown_rates,omitzero"`
}

// UnknownRates flags, per core component, a rate the price source did not
// know. Each field's JSON name is the Usage JSON tag of the component it
// covers. The zero value means every rate is known. It is a struct of flags
// rather than a list so that PriceSnapshot stays comparable with ==.
type UnknownRates struct {
	UncachedInput bool `json:"uncached_input_tokens,omitempty"`
	CacheRead     bool `json:"cache_read_tokens,omitempty"`
	CacheWrite    bool `json:"cache_write_tokens,omitempty"`
	Output        bool `json:"output_tokens,omitempty"`
	Reasoning     bool `json:"reasoning_tokens,omitempty"`
}

// Any reports whether at least one rate is marked unknown.
func (u UnknownRates) Any() bool { return u != UnknownRates{} }

// RateKnown reports whether the snapshot knows the rate for the core
// component named by its Usage JSON tag (for example "cache_read_tokens").
// It returns false for a component marked in UnknownRates and for a name
// that is not a core component.
func (p PriceSnapshot) RateKnown(component string) bool {
	u := p.UnknownRates
	switch component {
	case "uncached_input_tokens":
		return !u.UncachedInput
	case "cache_read_tokens":
		return !u.CacheRead
	case "cache_write_tokens":
		return !u.CacheWrite
	case "output_tokens":
		return !u.Output
	case "reasoning_tokens":
		return !u.Reasoning
	}
	return false
}

// coreComponentNames are the Usage JSON tags of the five core components, in
// field order.
var coreComponentNames = []string{
	"uncached_input_tokens",
	"cache_read_tokens",
	"cache_write_tokens",
	"output_tokens",
	"reasoning_tokens",
}

// CoreComponentNames returns the Usage JSON tags of the five core components,
// in field order. RateKnown and the JSON names of UnknownRates use them. The
// slice is a fresh copy on every call.
func CoreComponentNames() []string {
	return append([]string(nil), coreComponentNames...)
}

// rateFor returns the snapshot's rate for the core component named by its
// Usage JSON tag.
func (p PriceSnapshot) rateFor(component string) float64 {
	switch component {
	case "uncached_input_tokens":
		return p.InputPerMillion
	case "cache_read_tokens":
		return p.CacheReadPerMillion
	case "cache_write_tokens":
		return p.CacheWritePerMillion
	case "output_tokens":
		return p.OutputPerMillion
	case "reasoning_tokens":
		return p.ReasoningPerMillion
	}
	return 0
}

// Validate rejects a snapshot with a negative or non-finite rate, or with a
// rate marked unknown that carries a non-zero value. All problems are
// reported, joined.
func (p PriceSnapshot) Validate() error {
	var errs []error
	for _, n := range coreComponentNames {
		r := p.rateFor(n)
		if math.IsNaN(r) || math.IsInf(r, 0) || r < 0 {
			errs = append(errs, fmt.Errorf("usageledger: price: rate for %s is %v (must be finite and non-negative)", n, r))
			continue
		}
		if !p.RateKnown(n) && r != 0 {
			errs = append(errs, fmt.Errorf("usageledger: price: rate for %s is marked unknown but set to %v (must be 0)", n, r))
		}
	}
	return errors.Join(errs...)
}

// CostKind says what a cost derived from a Row stands for. It is a label on
// the row, not a cost: the package still carries no cost field.
//
// The zero value ("") is CostKindUnspecified, which is what every row recorded
// before the field existed decodes to. Only CostKindAPIBilled is a bill; the
// other kinds are estimates or equivalents and must not be presented as money
// owed.
type CostKind string

const (
	// CostKindUnspecified means the row does not say. Treat its cost as an
	// estimate, never as a bill.
	CostKindUnspecified CostKind = ""
	// CostKindAPIBilled means the provider bills this call by the token at
	// the recorded rates: the cost is what was (or will be) charged.
	CostKindAPIBilled CostKind = "api_billed"
	// CostKindAPIEstimated means the call is billed per token, but the figure
	// is computed locally (from estimated tokens or a rate table) rather than
	// taken from the provider's bill.
	CostKindAPIEstimated CostKind = "api_estimated"
	// CostKindSubscriptionEquivalent means the call ran under a flat-rate
	// subscription. The figure is what the same tokens would cost at API
	// rates; nobody is charged it.
	CostKindSubscriptionEquivalent CostKind = "subscription_equivalent"
	// CostKindLocalCompute means the model ran on hardware the caller owns.
	// Any figure is an attributed compute cost, not a provider charge.
	CostKindLocalCompute CostKind = "local_compute"
)

// Valid reports whether k is one of the defined kinds, including
// CostKindUnspecified.
func (k CostKind) Valid() bool {
	switch k {
	case CostKindUnspecified, CostKindAPIBilled, CostKindAPIEstimated,
		CostKindSubscriptionEquivalent, CostKindLocalCompute:
		return true
	}
	return false
}

// IsBill reports whether a cost of this kind is money a provider charges. It
// is true only for CostKindAPIBilled.
func (k CostKind) IsBill() bool { return k == CostKindAPIBilled }

// Row is one durable usage-ledger record: identity, the disjoint Usage, an
// optional price snapshot and the kind of cost it supports. There is
// deliberately no cost field.
type Row struct {
	SessionID  string         `json:"session_id"`
	MessageID  string         `json:"message_id,omitempty"`
	Provider   string         `json:"provider"`
	Model      string         `json:"model"`
	Usage      Usage          `json:"usage"`
	Price      *PriceSnapshot `json:"price,omitempty"` // nil = not priced
	CostKind   CostKind       `json:"cost_kind,omitempty"`
	RecordedAt time.Time      `json:"recorded_at"`
}

// Validate checks the row's Usage, its CostKind and, when present, its Price.
// It does not check identity fields. All problems are reported, joined.
func (r Row) Validate() error {
	var errs []error
	if err := r.Usage.Validate(); err != nil {
		errs = append(errs, err)
	}
	if !r.CostKind.Valid() {
		errs = append(errs, fmt.Errorf("usageledger: cost_kind: invalid value %q", string(r.CostKind)))
	}
	if r.Price != nil {
		if err := r.Price.Validate(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
