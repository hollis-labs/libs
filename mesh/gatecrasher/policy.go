package gatecrasher

import (
	"context"
	"slices"

	"github.com/hollis-labs/libs/mesh/core/gate"
	"github.com/hollis-labs/libs/security/policy"
)

// PolicyBinding resolves verified host facts and checks that the request's
// policy reference, ordered provenance and obligation references represent the
// ENTIRE evaluated intent. It must reject stale/forged revisions and dropped or
// altered obligations. Request fields are assertions until this binding checks
// them. It must not derive identity from an arbitrary caller's Request alone.
type PolicyBinding interface {
	Facts(context.Context, gate.Request) (policy.Request, error)
	CheckIntent(context.Context, gate.Request, policy.Decision) error
}

type PolicyPort struct {
	Evaluator policy.Evaluator
	Options   policy.Options
	Binding   PolicyBinding
}

func (p PolicyPort) evaluate(ctx context.Context, r gate.Request) error {
	if p.Binding == nil {
		return ErrPolicy
	}
	facts, err := p.Binding.Facts(ctx, r)
	if err != nil {
		return ErrPolicy
	}
	result, evalErr := p.Evaluator.Evaluate(ctx, facts, p.Options)
	if !result.AuditRecorded {
		return ErrPolicy
	}
	// This human gate has no independent fail-open grant. A configured local
	// allow remains a local policy path; callers must evaluate it independently.
	if evalErr != nil {
		if result.Admission == policy.Allow {
			return ErrLocalPath
		}
		return ErrPolicy
	}
	if err := p.Binding.CheckIntent(ctx, r, result.Decision); err != nil {
		return ErrPolicy
	}
	if result.Decision.Effect == policy.Deny {
		return ErrPolicy
	}
	for _, o := range result.Decision.Obligations {
		if o.Kind != policy.Approval {
			return gate.ErrUnsupported
		}
	}
	if result.Decision.Effect == policy.Allow && !slices.ContainsFunc(result.Decision.Obligations, func(o policy.Obligation) bool { return o.Kind == policy.Approval }) {
		return ErrLocalPath
	}
	if p.Options.Mode != policy.Enforce {
		return ErrLocalPath
	}
	return ctx.Err()
}
