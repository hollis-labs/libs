// Package conformance compares a PDP with caller-owned, immutable recorded cases.
// It supplies no engine, adapter, policy fixture discovery or authority.
package conformance

import (
	"context"
	"errors"
	"reflect"

	"github.com/hollis-labs/substrate/policy"
)

// Case binds a named host-projected request to a complete expected decision.
// Host owners must retain original source revision and projection provenance.
type Case struct {
	Name     string
	Request  policy.Request
	Expected policy.Decision
}

// Mismatch describes a failed case without emitting its context or PDP error text.
type Mismatch struct {
	Name   string
	Reason string
}

// Check compares ordered policy matches, reasons and every mandatory obligation.
// An empty/invalid corpus refuses rather than reporting vacuous conformance.
// This exercises the supplied PDP only; it does not prove host broker fulfillment,
// audit durability or that a consumer uses the adapter.
func Check(ctx context.Context, pdp policy.PDP, cases []Case) ([]Mismatch, error) {
	if ctx == nil || pdp == nil || len(cases) == 0 {
		return nil, errors.New("policy conformance: context, PDP and recorded cases required")
	}
	names := map[string]bool{}
	for _, c := range cases {
		if c.Name == "" || names[c.Name] || c.Request.Validate() != nil || c.Expected.Validate() != nil {
			return nil, errors.New("policy conformance: invalid recorded case")
		}
		names[c.Name] = true
	}
	var mismatches []Mismatch
	for _, c := range cases {
		if err := ctx.Err(); err != nil {
			return mismatches, err
		}
		got, err := pdp.Evaluate(ctx, c.Request)
		if ctx.Err() != nil {
			return mismatches, ctx.Err()
		}
		switch {
		case err != nil:
			mismatches = append(mismatches, Mismatch{Name: c.Name, Reason: "evaluation failed"})
		case got.Validate() != nil:
			mismatches = append(mismatches, Mismatch{Name: c.Name, Reason: "invalid decision"})
		case !reflect.DeepEqual(got, c.Expected):
			mismatches = append(mismatches, Mismatch{Name: c.Name, Reason: "decision differs"})
		}
	}
	return mismatches, nil
}
