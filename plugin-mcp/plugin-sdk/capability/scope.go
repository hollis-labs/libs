package capability

import (
	"slices"
	"sort"
)

// Scope is a normalized authority envelope, not a wire grant. Each allowlist
// contains exact identifiers, never patterns. A missing/empty list or missing/
// zero budget conveys no authority. Limits are nonnegative ceilings, in units
// defined by the descriptor. Hosts must encode related identifiers (such as
// server/tool pairs) in one dimension rather than creating a Cartesian product.
type Scope struct {
	Allowlists map[string][]string `json:"allowlists"`
	Limits     map[string]int64    `json:"limits"`
}

// Intersect computes requested ∩ supported ∩ approved ∩ policy. All inputs
// must already use the same descriptor version; missing constraints deny.
// The result owns its maps and slices and never aliases an input.
func Intersect(name string, requested, supported, approved, policy Scope) (Scope, error) {
	inputs := []Scope{requested, supported, approved, policy}
	for _, s := range inputs {
		if err := s.Validate(name); err != nil {
			return Scope{}, err
		}
	}
	out := Scope{Allowlists: map[string][]string{}, Limits: map[string]int64{}}
	for key, values := range requested.Allowlists {
		result := []string{}
		for _, v := range values {
			if slices.Contains(supported.Allowlists[key], v) && slices.Contains(approved.Allowlists[key], v) && slices.Contains(policy.Allowlists[key], v) {
				result = append(result, v)
			}
		}
		sort.Strings(result)
		out.Allowlists[key] = slices.Compact(result)
	}
	for key, value := range requested.Limits {
		for _, s := range inputs[1:] {
			value = min(value, s.Limits[key])
		}
		out.Limits[key] = value
	}
	return out, nil
}

// Validate rejects malformed authority. Wildcards require a separate reviewed
// schema; this exact-match envelope deliberately cannot express them.
func (s Scope) Validate(name string) error {
	for k, vs := range s.Allowlists {
		if k == "" {
			return refusal(InvalidRequest, name)
		}
		for _, v := range vs {
			if v == "" || v == "*" {
				return refusal(InvalidRequest, name)
			}
		}
	}
	for k, v := range s.Limits {
		if k == "" || v < 0 {
			return refusal(InvalidRequest, name)
		}
	}
	return nil
}

// CheckNarrowing rejects an attempted widening by capability name. New empty
// dimensions carry no authority; positive new limits/identifiers are denied.
func CheckNarrowing(name string, previous, next Scope) error {
	if err := previous.Validate(name); err != nil {
		return err
	}
	if err := next.Validate(name); err != nil {
		return err
	}
	for k, vs := range next.Allowlists {
		for _, v := range vs {
			if !slices.Contains(previous.Allowlists[k], v) {
				return refusal(ScopeDenied, name)
			}
		}
	}
	for k, v := range next.Limits {
		if v > previous.Limits[k] {
			return refusal(ScopeDenied, name)
		}
	}
	return nil
}

// Allows matches one exact identifier in a dimension.
func (s Scope) Allows(dimension, value string) bool {
	return value != "" && slices.Contains(s.Allowlists[dimension], value)
}

// Within checks a nonnegative amount against an explicitly present ceiling.
func (s Scope) Within(dimension string, amount int64) bool {
	limit, ok := s.Limits[dimension]
	return ok && amount >= 0 && amount <= limit
}
