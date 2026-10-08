package boot

import (
	"context"
	"errors"

	"github.com/hollis-labs/substrate/harness/workspace"
)

// Prepare plans before touching host ports, then forwards the complete engine
// result on success and failure. Process projection occurs before any mutation;
// a failed projection cannot leave an unaccounted boot directory.
func Prepare(ctx context.Context, in Input, host HostInputs) (Result, error) {
	out := Result{Description: initialDescription(in, host)}
	if ctx == nil {
		return out, failure(PhasePlan, "missing_context", errors.New("context is required"))
	}
	if err := ctx.Err(); err != nil {
		return out, failure(PhasePlan, "cancelled", err)
	}
	p, err := Plan(in, host)
	out.Description = p.Description()
	if err != nil {
		return out, err
	}
	applied, err := workspace.Materialize(ctx, p.workspace, host.Ports)
	out.Apply = applied.Clone()
	for _, evidence := range out.Apply.Receipt.EffectEvidence {
		out.Description.TrustOutcome = append(out.Description.TrustOutcome, evidence.Trust...)
	}
	if err != nil {
		return out, failure(PhaseMaterialize, errorCode(err, "materialize_failed"), err)
	}
	return out, nil
}
