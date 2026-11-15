package sdk

import (
	"context"
	"fmt"

	"github.com/hollis-labs/libs/mesh/core/agentdef"
	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
	"github.com/hollis-labs/libs/security/policy"
)

// PDPAdapter composes the shared policy evaluator with host-authenticated fact
// projection and immutable policy/obligation references. The host still verifies
// policy and obligations at atomic admission; PDP Allow is not an execution grant.
type PDPAdapter struct {
	PDP     policy.PDP
	Check   func(context.Context, agentdef.Definition, runtimecontract.Binding, runtimecontract.EnvironmentRef) error
	Project func(context.Context, Operation) (policy.Request, PolicyResult, error)
}

func (a PDPAdapter) CheckDefinition(ctx context.Context, d agentdef.Definition, b runtimecontract.Binding, e runtimecontract.EnvironmentRef) error {
	if a.PDP == nil || a.Check == nil || a.Project == nil {
		return fmt.Errorf("%w: incomplete PDP adapter", ErrInvalid)
	}
	return a.Check(ctx, d, b, e)
}
func (a PDPAdapter) Evaluate(ctx context.Context, op Operation) (PolicyResult, error) {
	if a.PDP == nil || a.Project == nil {
		return PolicyResult{}, fmt.Errorf("%w: incomplete PDP adapter", ErrInvalid)
	}
	request, result, err := a.Project(ctx, op)
	if err != nil {
		return PolicyResult{}, err
	}
	if err = request.Validate(); err != nil {
		return PolicyResult{}, err
	}
	decision, err := a.PDP.Evaluate(ctx, request)
	if err != nil {
		return PolicyResult{}, err
	}
	if err = decision.Validate(); err != nil {
		return PolicyResult{}, err
	}
	result.Decision = decision
	return result, nil
}
