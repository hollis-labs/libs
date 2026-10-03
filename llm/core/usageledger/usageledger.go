package usageledger

import (
	"errors"
	"fmt"
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
type PriceSnapshot struct {
	InputPerMillion      float64 `json:"input_per_million"`
	OutputPerMillion     float64 `json:"output_per_million"`
	CacheWritePerMillion float64 `json:"cache_write_per_million,omitempty"`
	CacheReadPerMillion  float64 `json:"cache_read_per_million,omitempty"`
	ReasoningPerMillion  float64 `json:"reasoning_per_million,omitempty"`
}

// Row is one durable usage-ledger record: identity, the disjoint Usage, and
// an optional price snapshot. There is deliberately no cost field.
type Row struct {
	SessionID  string         `json:"session_id"`
	MessageID  string         `json:"message_id,omitempty"`
	Provider   string         `json:"provider"`
	Model      string         `json:"model"`
	Usage      Usage          `json:"usage"`
	Price      *PriceSnapshot `json:"price,omitempty"` // nil = not priced
	RecordedAt time.Time      `json:"recorded_at"`
}
