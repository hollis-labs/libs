package credentials

import (
	"context"
	"github.com/hollis-labs/substrate/harness/workspace/effects"
	"path/filepath"
)

// Inspect checks trusted receipt binding and observes an inactive candidate.
// It never replays or compensates a link. Post-mutation problems are Partial;
// Conflict is reserved for evidence that records no actual or uncertain create.
func Inspect(ctx context.Context, g Group, e effects.Evidence, c effects.PreflightContext, p LinkPort) effects.Result {
	g = freeze(g)
	if code := binding(g, c); code != "" {
		return result(g, effects.Refused, code)
	}
	if e.Header != g.Header || e.Kind != effects.CredentialLinks || e.RootID != g.Candidate.ID || len(e.Links) != len(g.Bindings) {
		return result(g, effects.Refused, "evidence_binding_refused")
	}
	complete := e.Phase == effects.CompletePhase
	aborted := e.Phase == effects.AbortedPhase
	inFlight := e.Phase == effects.IntentPhase || e.Phase == effects.LinkIntentPhase || e.Phase == effects.LinkCreatedPhase
	switch {
	case complete:
		if e.Outcome != effects.Applied && e.Outcome != effects.AlreadyPresent {
			return result(g, effects.Refused, "evidence_binding_refused")
		}
	case aborted:
		if e.Outcome != effects.Refused && e.Outcome != effects.Conflict {
			return result(g, effects.Refused, "evidence_binding_refused")
		}
	case inFlight:
		if e.Outcome != effects.Pending {
			return result(g, effects.Refused, "evidence_binding_refused")
		}
	case e.Phase == effects.InterruptedPhase:
		if e.Outcome != effects.Partial {
			return result(g, effects.Refused, "evidence_binding_refused")
		}
	default:
		return result(g, effects.Refused, "evidence_binding_refused")
	}
	mutated := false
	for n, b := range g.Bindings {
		entry := e.Links[n]
		if entry.Destination != b.Destination || entry.AuthorizationID != b.AuthorizationID || entry.AuthorizationVersion != b.AuthorizationVersion {
			return result(g, effects.Refused, "evidence_binding_refused")
		}
		want := filepath.Join(g.Home.LogicalPath, filepath.FromSlash(b.Source))
		if entry.Outcome != effects.Omitted && (entry.Source != want || entry.Target != want) {
			return result(g, effects.Refused, "evidence_binding_refused")
		}
		if entry.Uncertain && entry.Outcome != effects.Partial {
			return result(g, effects.Refused, "evidence_binding_refused")
		}
		switch entry.Outcome {
		case effects.Omitted:
			if b.Required || entry.Created || entry.Uncertain || entry.Source != "" || entry.Target != "" || entry.LinkIdentity != "" {
				return result(g, effects.Refused, "evidence_binding_refused")
			}
		case effects.Pending:
			if complete || entry.Created || entry.Uncertain || entry.LinkIdentity != "" {
				return result(g, effects.Refused, "evidence_binding_refused")
			}
		case effects.Applied:
			if !entry.Created || entry.Uncertain || entry.LinkIdentity == "" || entry.ParentIdentity == "" {
				return result(g, effects.Refused, "evidence_binding_refused")
			}
		case effects.AlreadyPresent:
			if entry.Created || entry.Uncertain || entry.LinkIdentity == "" || entry.ParentIdentity == "" {
				return result(g, effects.Refused, "evidence_binding_refused")
			}
		case effects.Removed:
			if complete || !entry.Created || entry.Uncertain || entry.LinkIdentity == "" || entry.ParentIdentity == "" {
				return result(g, effects.Refused, "evidence_binding_refused")
			}
		case effects.Partial:
			if complete || (!entry.Created && !entry.Uncertain) {
				return result(g, effects.Refused, "evidence_binding_refused")
			}
		default:
			return result(g, effects.Refused, "evidence_binding_refused")
		}
		if complete && e.Outcome == effects.AlreadyPresent && entry.Created {
			return result(g, effects.Refused, "evidence_binding_refused")
		}
		mutated = mutated || entry.Created || entry.Uncertain
	}
	if aborted && mutated {
		return result(g, effects.Refused, "evidence_binding_refused")
	}
	unavailable := func(o effects.Outcome, code string) effects.Result {
		r := result(g, o, code)
		r.Evidence = e.Clone()
		if mutated {
			r.Outcome = effects.Partial
			r.Obligations = []effects.Obligation{{RootID: g.Candidate.ID, Code: "recovery_required"}}
		}
		return r
	}
	if p == nil || !p.Supported() {
		return unavailable(effects.Unsupported, "platform_unsupported")
	}
	if ctx.Err() != nil || c.Validate(ctx) != nil {
		return unavailable(effects.Refused, "authority_refused")
	}
	r := result(g, effects.AlreadyPresent, "inspected_complete")
	r.Evidence = e.Clone()
	if aborted {
		r.Outcome = e.Outcome
		r.Code = "inspected_aborted"
	} else if !complete {
		r.Outcome = effects.Partial
		r.Code = "inspected_interrupted"
	}
	divergent, missing, sourceChanged := false, false, false
	for n, b := range g.Bindings {
		entry := e.Links[n]
		d, err := p.Destination(ctx, g.Candidate, b.Destination)
		state := effects.Divergent
		if err == nil {
			if !d.Exists {
				if entry.Outcome == effects.Pending || entry.Outcome == effects.Omitted || entry.Outcome == effects.Removed {
					state = effects.Before
				} else {
					state = effects.Missing
				}
			} else if !entry.Uncertain && entry.Outcome != effects.Pending && entry.Outcome != effects.Omitted && entry.Outcome != effects.Removed && d.IsLink && entry.LinkIdentity != "" && d.Target == entry.Target && d.Identity == entry.LinkIdentity && d.ParentIdentity == entry.ParentIdentity {
				state = effects.IntendedAfter
			}
		}
		r.Inspections = append(r.Inspections, effects.Inspection{Destination: b.Destination, State: state})
		divergent = divergent || state == effects.Divergent
		missing = missing || state == effects.Missing
		if !aborted && entry.Outcome != effects.Omitted && entry.Outcome != effects.Removed {
			_, omit, code := inspectSource(ctx, g, b, p)
			sourceChanged = sourceChanged || code != "" || omit
		}
	}
	if divergent {
		r.Outcome = effects.Conflict
		if mutated {
			r.Outcome = effects.Partial
		}
		r.Code = "evidence_diverged"
	} else if missing {
		r.Outcome = effects.Partial
		r.Code = "evidence_missing"
	} else if sourceChanged {
		r.Outcome = effects.Partial
		r.Code = "source_changed"
	}
	if r.Outcome == effects.Partial || divergent || missing || sourceChanged {
		r.Obligations = append(r.Obligations, effects.Obligation{RootID: g.Candidate.ID, Code: "recovery_required"})
	}
	return r
}
