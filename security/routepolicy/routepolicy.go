package routepolicy

import (
	"context"
	"errors"
	"fmt"

	"github.com/hollis-labs/libs/core/util/router"
	"github.com/hollis-labs/libs/security/policy"
)

// Mapper projects a routing candidate to a policy request. It decides what
// reaches the PDP (Request.Context may hold sensitive input; the evaluator never
// audits it). It runs before the evaluator, so a mapping error is a refusal.
type Mapper func(ctx context.Context, s router.Subject, m router.Match) (policy.Request, error)

// Adapter implements router.Policy.
type Adapter struct {
	Evaluator policy.Evaluator
	Options   policy.Options
	Map       Mapper
}

var _ router.Policy = Adapter{}

// Evaluate runs the evaluator and returns a router verdict. It returns an error
// only when it cannot produce one (no Mapper, mapping failure); the router
// treats that as a denial. Evaluator failures come back as a Deny (or, under
// FailOpenWithAudit with a recorded audit, an Apply) verdict carrying the
// failure code.
func (a Adapter) Evaluate(ctx context.Context, s router.Subject, m router.Match) (router.Verdict, error) {
	if a.Map == nil {
		return router.Verdict{}, errors.New("routepolicy: no Mapper")
	}
	req, err := a.Map(ctx, s, m)
	if err != nil {
		return router.Verdict{}, fmt.Errorf("routepolicy: map: %w", err)
	}
	res, _ := a.Evaluator.Evaluate(ctx, req, a.Options) // the error repeats res.Failure; its text may hold private input
	return toVerdict(res), nil
}

func toVerdict(res policy.Result) router.Verdict {
	v := router.Verdict{
		Effect:     disposition(res.Admission),
		Reason:     res.Decision.Reason,
		WouldBlock: res.WouldBlock && res.Admission == policy.Allow,
		Failure:    string(res.Failure),
	}
	for _, p := range res.Decision.MatchedPolicies {
		v.Matched = append(v.Matched, router.PolicyRef{ID: p.ID, Source: p.Source, Reason: p.Reason})
	}
	for _, o := range res.Decision.Obligations {
		ro := router.Obligation{Kind: router.ObligationKind(o.Kind), PolicyID: o.PolicyID, Reference: o.Reference}
		if o.Limit != nil {
			ro.Limit = &router.Limit{Count: o.Limit.Count, Window: o.Limit.Window, Unit: o.Limit.Unit}
		}
		v.Obligations = append(v.Obligations, ro)
	}
	return v
}

// disposition maps an effective admission. Anything unrecognized denies.
func disposition(e policy.Effect) router.Disposition {
	switch e {
	case policy.Allow:
		return router.Apply
	case policy.Ask:
		return router.Ask
	}
	return router.Deny
}

// DefaultMapper builds a request in the shape routing needs: the action is
// "route:<target>", the resource is the target, and the context carries the
// rule id, the subject's fields and its confidence (if known). Principal comes
// from the subject's "principal.urn" and "principal.kind" fields; Scope is fixed.
// Hosts that route untrusted subjects should supply their own Mapper and decide
// which fields reach the PDP.
func DefaultMapper(scope string) Mapper {
	return func(_ context.Context, s router.Subject, m router.Match) (policy.Request, error) {
		urn, kind := s.Fields["principal.urn"], s.Fields["principal.kind"]
		if urn == "" || kind == "" {
			return policy.Request{}, errors.New("subject has no principal")
		}
		fields := make(map[string]any, len(s.Fields))
		for k, v := range s.Fields {
			fields[k] = v
		}
		ctxMap := map[string]any{"rule_id": m.RuleID, "fields": fields}
		if s.Confidence != nil {
			ctxMap["confidence"] = *s.Confidence
		}
		return policy.Request{
			RequestID: s.ID,
			Principal: policy.Principal{URN: urn, Kind: kind},
			Action:    "route:" + m.Target,
			Resource:  policy.Resource{Type: "route-target", ID: m.Target},
			Scope:     scope,
			Context:   ctxMap,
		}, nil
	}
}
