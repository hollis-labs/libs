package chatstream

import (
	"encoding/json"
	"errors"
	"fmt"
)

// UsageScope says what a usage report covers.
type UsageScope string

// The usage scopes.
const (
	// UsageCumulative: totals so far for the run; a later report replaces an
	// earlier one (Anthropic's message_start then message_delta).
	UsageCumulative UsageScope = "cumulative"
	// UsageFinal: the run's totals, reported once at the end (OpenAI's last
	// chunk, a CLI's result event).
	UsageFinal UsageScope = "final"
	// UsageDelta: tokens used since the previous report; reports add up.
	UsageDelta UsageScope = "delta"
)

// Usage is token usage as DISJOINT components: no token is counted in more
// than one field, so Total is a plain sum and cannot double-count.
//
// Providers disagree on what their numbers include. Anthropic's input_tokens
// excludes cache reads and writes; OpenAI's prompt_tokens includes cached
// tokens, and its completion_tokens includes reasoning tokens. A consumer
// that adds "input plus cache" to both double-counts the OpenAI shape
// (Hadron's llmprovider bridge does). Decoders therefore convert to these
// components once, with UsageFromExclusiveInput and UsageFromInclusive, and
// nothing downstream needs to know which provider a number came from.
type Usage struct {
	Scope UsageScope `json:"scope,omitempty"`
	// UncachedInput is prompt tokens that were neither read from nor written
	// to a cache.
	UncachedInput int `json:"uncached_input"`
	// CacheRead is prompt tokens served from a cache.
	CacheRead int `json:"cache_read,omitempty"`
	// CacheWrite is prompt tokens written to a cache.
	CacheWrite int `json:"cache_write,omitempty"`
	// Output is generated tokens other than reasoning tokens.
	Output int `json:"output"`
	// Reasoning is generated reasoning ("thinking") tokens, when the provider
	// reports them separately; otherwise they are inside Output.
	Reasoning int `json:"reasoning,omitempty"`
	// Extra holds provider counters that are not tokens of the run's own
	// text (server tool calls, audio tokens, ...). It is never part of Total.
	Extra map[string]int `json:"extra,omitempty"`
}

// Total is every token counted: the sum of the five components. It is derived,
// never stored.
func (u Usage) Total() int {
	return u.UncachedInput + u.CacheRead + u.CacheWrite + u.Output + u.Reasoning
}

// Input is all prompt tokens: uncached plus cache reads and writes.
func (u Usage) Input() int { return u.UncachedInput + u.CacheRead + u.CacheWrite }

// Generated is all generated tokens: Output plus Reasoning.
func (u Usage) Generated() int { return u.Output + u.Reasoning }

// IsZero reports whether every component (and Extra) is zero.
func (u Usage) IsZero() bool {
	if u.UncachedInput != 0 || u.CacheRead != 0 || u.CacheWrite != 0 || u.Output != 0 || u.Reasoning != 0 {
		return false
	}
	for _, v := range u.Extra {
		if v != 0 {
			return false
		}
	}
	return true
}

// Valid reports whether no component is negative.
func (u Usage) Valid() error {
	for name, v := range map[string]int{
		"uncached_input": u.UncachedInput, "cache_read": u.CacheRead, "cache_write": u.CacheWrite,
		"output": u.Output, "reasoning": u.Reasoning,
	} {
		if v < 0 {
			return fmt.Errorf("chatstream: usage %s is negative (%d)", name, v)
		}
	}
	return nil
}

// Add returns the component-wise sum of u and v, with u's Scope. It is how
// UsageDelta reports accumulate.
func (u Usage) Add(v Usage) Usage {
	out := Usage{
		Scope:         u.Scope,
		UncachedInput: u.UncachedInput + v.UncachedInput,
		CacheRead:     u.CacheRead + v.CacheRead,
		CacheWrite:    u.CacheWrite + v.CacheWrite,
		Output:        u.Output + v.Output,
		Reasoning:     u.Reasoning + v.Reasoning,
		Extra:         mergeExtra(u.Extra, v.Extra, 1),
	}
	return out
}

// Sub returns u minus prev, component-wise, with Scope UsageDelta: the
// conversion from a cumulative report to the change since the last one (what
// Codex's cumulative usage needs). It errors when any component went down,
// which a cumulative counter must not do.
func (u Usage) Sub(prev Usage) (Usage, error) {
	d := Usage{
		Scope:         UsageDelta,
		UncachedInput: u.UncachedInput - prev.UncachedInput,
		CacheRead:     u.CacheRead - prev.CacheRead,
		CacheWrite:    u.CacheWrite - prev.CacheWrite,
		Output:        u.Output - prev.Output,
		Reasoning:     u.Reasoning - prev.Reasoning,
		Extra:         mergeExtra(u.Extra, prev.Extra, -1),
	}
	if err := d.Valid(); err != nil {
		return Usage{}, fmt.Errorf("chatstream: cumulative usage went backwards: %w", err)
	}
	return d, nil
}

func mergeExtra(a, b map[string]int, sign int) map[string]int {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := make(map[string]int, len(a)+len(b))
	for k, v := range a {
		out[k] += v
	}
	for k, v := range b {
		out[k] += sign * v
	}
	return out
}

// UsageFromExclusiveInput builds a Usage from a provider whose input count
// EXCLUDES cache tokens (Anthropic: input_tokens, cache_creation_input_tokens,
// cache_read_input_tokens, output_tokens). Each number is already a disjoint
// component; reasoning is not reported separately.
func UsageFromExclusiveInput(scope UsageScope, uncachedInput, cacheRead, cacheWrite, output int) (Usage, error) {
	u := Usage{Scope: scope, UncachedInput: uncachedInput, CacheRead: cacheRead, CacheWrite: cacheWrite, Output: output}
	return u, u.Valid()
}

// ErrUsageInconsistent is returned when a provider's subset counts exceed the
// totals they are supposed to be part of.
var ErrUsageInconsistent = errors.New("chatstream: usage subsets exceed their totals")

// UsageFromInclusive builds a Usage from a provider whose totals INCLUDE their
// subsets (OpenAI: prompt_tokens includes cached_tokens; completion_tokens
// includes reasoning_tokens). promptTotal and completionTotal are the
// provider's totals; cacheRead, cacheWrite and reasoning are the subsets it
// reports inside them. It returns ErrUsageInconsistent when a subset is larger
// than its total, which would otherwise yield a negative component.
func UsageFromInclusive(scope UsageScope, promptTotal, cacheRead, cacheWrite, completionTotal, reasoning int) (Usage, error) {
	u := Usage{
		Scope:         scope,
		UncachedInput: promptTotal - cacheRead - cacheWrite,
		CacheRead:     cacheRead,
		CacheWrite:    cacheWrite,
		Output:        completionTotal - reasoning,
		Reasoning:     reasoning,
	}
	if u.UncachedInput < 0 || u.Output < 0 {
		return Usage{}, fmt.Errorf("%w: prompt %d (cache read %d, write %d), completion %d (reasoning %d)",
			ErrUsageInconsistent, promptTotal, cacheRead, cacheWrite, completionTotal, reasoning)
	}
	return u, u.Valid()
}

// MarshalJSON adds "total" for readers that want it. It is derived: decoding
// ignores it, so a stored total can never disagree with the components.
func (u Usage) MarshalJSON() ([]byte, error) {
	type plain Usage
	return json.Marshal(struct {
		plain
		Total int `json:"total"`
	}{plain(u), u.Total()})
}
