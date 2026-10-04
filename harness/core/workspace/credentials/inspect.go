package credentials

import (
	"context"
	"path/filepath"

	"github.com/hollis-labs/substrate/harness/workspace/effects"
)

// Inspect checks binding on trusted host evidence and observes an inactive
// candidate. It never replays an effect, adopts an unrecorded link, or removes
// divergent paths. Interrupted evidence remains non-complete even when all
// recorded links match; the caller's recovery protocol owns any next step.
func Inspect(ctx context.Context, g Group, e effects.Evidence, c effects.PreflightContext, p LinkPort) effects.Result {
	g = freeze(g)
	if code := binding(g, c); code != "" {
		return result(g, effects.Refused, code)
	}
	if e.Header != g.Header || e.Kind != effects.CredentialLinks || e.RootID != g.Candidate.ID || len(e.Links) != len(g.Bindings) {
		return result(g, effects.Refused, "evidence_binding_refused")
	}
	complete := e.Phase == effects.CompletePhase
	switch e.Phase {
	case effects.CompletePhase, effects.IntentPhase, effects.LinkIntentPhase, effects.LinkCreatedPhase, effects.InterruptedPhase:
	default:
		return result(g, effects.Refused, "evidence_binding_refused")
	}
	if complete && (e.Outcome != effects.Applied && e.Outcome != effects.AlreadyPresent) {
		return result(g, effects.Refused, "evidence_binding_refused")
	}
	if e.Outcome != effects.Applied && e.Outcome != effects.AlreadyPresent && e.Outcome != effects.Partial && e.Outcome != effects.Conflict {
		return result(g, effects.Refused, "evidence_binding_refused")
	}
	for n, b := range g.Bindings {
		entry := e.Links[n]
		if entry.Destination != b.Destination || entry.AuthorizationID != b.AuthorizationID || entry.AuthorizationVersion != b.AuthorizationVersion {
			return result(g, effects.Refused, "evidence_binding_refused")
		}
		want := filepath.Join(g.Home.LogicalPath, filepath.FromSlash(b.Source))
		if entry.Outcome != effects.Omitted && (entry.Source != want || entry.Target != want) {
			return result(g, effects.Refused, "evidence_binding_refused")
		}
		switch entry.Outcome {
		case effects.Omitted:
			if complete && (b.Required || entry.Created || entry.LinkIdentity != "") {
				return result(g, effects.Refused, "evidence_binding_refused")
			}
			if !entry.Created && (b.Required || entry.Source != "" || entry.Target != "" || entry.LinkIdentity != "") {
				return result(g, effects.Refused, "evidence_binding_refused")
			}
		case effects.Pending:
			if complete || entry.Created || entry.LinkIdentity != "" {
				return result(g, effects.Refused, "evidence_binding_refused")
			}
		case effects.Applied:
			if !entry.Created || entry.LinkIdentity == "" || entry.ParentIdentity == "" {
				return result(g, effects.Refused, "evidence_binding_refused")
			}
		case effects.AlreadyPresent:
			if entry.Created || entry.LinkIdentity == "" || entry.ParentIdentity == "" {
				return result(g, effects.Refused, "evidence_binding_refused")
			}
		case effects.Partial:
			if complete || !entry.Created {
				return result(g, effects.Refused, "evidence_binding_refused")
			}
		default:
			return result(g, effects.Refused, "evidence_binding_refused")
		}
		if complete && e.Outcome == effects.AlreadyPresent && entry.Created {
			return result(g, effects.Refused, "evidence_binding_refused")
		}
	}
	if p == nil || !p.Supported() {
		return result(g, effects.Unsupported, "platform_unsupported")
	}
	if ctx.Err() != nil || c.Validate(ctx) != nil {
		return result(g, effects.Refused, "authority_refused")
	}
	r := result(g, effects.AlreadyPresent, "inspected_complete")
	r.Evidence = e.Clone()
	if !complete {
		r.Outcome = effects.Partial
		r.Code = "inspected_interrupted"
	}
	for n, b := range g.Bindings {
		entry := e.Links[n]
		d, err := p.Destination(ctx, g.Candidate, b.Destination)
		state := effects.Divergent
		if err == nil {
			if !d.Exists {
				if entry.Outcome == effects.Pending || entry.Outcome == effects.Omitted {
					state = effects.Before
				} else {
					state = effects.Missing
				}
			} else if entry.Outcome != effects.Pending && entry.Outcome != effects.Omitted && d.IsLink && entry.LinkIdentity != "" && d.Target == entry.Target && d.Identity == entry.LinkIdentity && d.ParentIdentity == entry.ParentIdentity {
				state = effects.IntendedAfter
			}
		}
		r.Inspections = append(r.Inspections, effects.Inspection{Destination: b.Destination, State: state})
		if state == effects.Divergent {
			r.Outcome = effects.Conflict
			r.Code = "evidence_diverged"
		} else if state == effects.Missing && r.Outcome != effects.Conflict {
			r.Outcome = effects.Partial
			r.Code = "evidence_missing"
		}
		if entry.Outcome != effects.Omitted {
			_, omit, code := inspectSource(ctx, g, b, p)
			if (code != "" || omit) && r.Outcome != effects.Conflict {
				r.Outcome = effects.Partial
				r.Code = "source_changed"
			}
		}
	}
	if r.Outcome == effects.Partial || r.Outcome == effects.Conflict {
		r.Obligations = append(r.Obligations, effects.Obligation{RootID: g.Candidate.ID, Code: "recovery_required"})
	}
	return r
}
