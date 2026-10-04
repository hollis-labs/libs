// Package trust applies explicitly authorized provider trust through host ports.
// Trust grants no sandbox or fabric permission and does not establish readiness.
package trust

import (
	"context"
	"github.com/hollis-labs/substrate/harness/workspace/effects"
	"path/filepath"
	"strings"
)

type Mechanism string

const (
	ClaudeProjects        Mechanism = "claude_projects"
	CodexProjects         Mechanism = "codex_projects"
	AntigravityWorkspaces Mechanism = "antigravity_workspaces"
)

type Target struct {
	LogicalPath, CanonicalParent, AllowedBase, Provenance string
	// Stable attests that this is the final runtime cwd, never a candidate.
	Stable bool
}
type Request struct {
	Header                                                 effects.Header
	Mechanism                                              Mechanism
	Required                                               bool
	AuthorizationID, AuthorizationVersion, CandidateRootID string
	Config                                                 effects.RootInput
	Target                                                 Target
}
type Observation struct {
	Target  string
	Present bool
}
type Port interface {
	Supported(Mechanism) bool
	// Observe is strictly observational, including when the final cwd is absent.
	Observe(context.Context, Request) (Observation, error)
	// Begin may acquire the declared provider config lock after durable intent.
	// Errors must clean up their own lock; uncertain cleanup is represented by a
	// nonnil Session so the caller can retain a recovery obligation.
	Begin(context.Context, Request) (Session, error)
}
type Session interface {
	// Apply reobserves config under its lock, preserves unrelated fields, and
	// refreshes host authority immediately before replacement. The bool means
	// actual OR uncertain config mutation, including errors after replacement.
	Apply(context.Context, Request, func(context.Context) error) (Observation, bool, error)
	Close() error
}
type Prepared struct {
	request Request
	valid   bool
}

func absolute(p string) bool {
	return filepath.IsAbs(p) && filepath.Clean(p) == p && !strings.ContainsRune(p, 0)
}
func under(base, p string) bool {
	rel, e := filepath.Rel(base, p)
	return e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
func lock(c effects.PreflightContext, id string) bool {
	for _, l := range c.HeldLocks {
		if l.CanonicalID == id && absolute(l.Namespace) && !under(id, l.Namespace) {
			return true
		}
	}
	return false
}
func binding(r Request, c effects.PreflightContext) bool {
	return r.Header.Valid() && r.Header == c.Header && r.AuthorizationID != "" && r.AuthorizationVersion != "" && r.CandidateRootID != "" && r.Config.ID != "" && r.Config.Owner != "" && r.Config.Provenance != "" && absolute(r.Config.Path) && absolute(r.Config.AllowedBase) && under(r.Config.AllowedBase, r.Config.Path) && r.Config.MutationIdentity == r.Config.Path && r.Target.Stable && r.Target.Provenance != "" && absolute(r.Target.LogicalPath) && absolute(r.Target.CanonicalParent) && absolute(r.Target.AllowedBase) && under(r.Target.AllowedBase, r.Target.CanonicalParent) && r.Target.LogicalPath != r.Target.CanonicalParent && lock(c, r.Config.MutationIdentity) && lock(c, r.Target.CanonicalParent) && c.Validate != nil
}
func result(r Request, o effects.Outcome, code string) effects.Result {
	return effects.Result{Outcome: o, Code: code, Evidence: effects.Evidence{Header: r.Header, Kind: effects.Trust, RootID: r.Config.ID, Phase: effects.PreflightPhase, Outcome: o, Trust: []effects.TrustEvidence{{Mechanism: string(r.Mechanism), ConfigRootID: r.Config.ID, AuthorizationID: r.AuthorizationID, AuthorizationVersion: r.AuthorizationVersion, Outcome: o}}}}
}
func Preflight(ctx context.Context, r Request, c effects.PreflightContext, p Port) (Prepared, effects.Result) {
	if !binding(r, c) {
		return Prepared{}, result(r, effects.Refused, "binding_refused")
	}
	if ctx.Err() != nil || c.Validate(ctx) != nil {
		return Prepared{}, result(r, effects.Refused, "authority_refused")
	}
	// No generic writer may invent support for an unknown mechanism.
	if r.Mechanism != ClaudeProjects || p == nil || !p.Supported(r.Mechanism) {
		o := effects.Unsupported
		if !r.Required {
			o = effects.Omitted
		}
		return Prepared{request: r, valid: !r.Required}, result(r, o, "mechanism_unsupported")
	}
	observed, err := p.Observe(ctx, r)
	if err != nil || observed.Target != filepath.Join(r.Target.CanonicalParent, filepath.Base(r.Target.LogicalPath)) {
		return Prepared{}, result(r, effects.Refused, "observation_refused")
	}
	out := result(r, effects.AlreadyPresent, "preflight_complete")
	out.Evidence.Trust[0].Target = observed.Target
	out.Evidence.Trust[0].Present = observed.Present
	return Prepared{request: r, valid: true}, out
}
func Apply(ctx context.Context, prepared Prepared, c effects.ApplyContext, p Port) (out effects.Result) {
	r := prepared.request
	if !prepared.valid {
		return result(r, effects.Refused, "invalid_prepared")
	}
	_, out = Preflight(ctx, r, c.PreflightContext, p)
	if out.Code != "preflight_complete" && out.Outcome != effects.Omitted {
		return out
	}
	if c.Receipts == nil || c.ArtifactRootID != r.CandidateRootID || c.ArtifactGeneration == "" {
		return result(r, effects.Refused, "missing_apply_evidence")
	}
	if out.Outcome == effects.Omitted {
		out.Evidence.Trust[0].Target = filepath.Join(r.Target.CanonicalParent, filepath.Base(r.Target.LogicalPath))
		out.Evidence.Phase = effects.CompletePhase
		if c.Receipts.Record(ctx, out.Evidence.Clone()) != nil {
			return result(r, effects.Refused, "receipt_failed_before_mutation")
		}
		return out
	}
	out.Evidence.Phase = effects.IntentPhase
	if c.Receipts.Record(ctx, out.Evidence.Clone()) != nil {
		return result(r, effects.Refused, "receipt_failed_before_mutation")
	}
	session, err := p.Begin(ctx, r)
	if session == nil {
		return result(r, effects.Conflict, "config_lock_unavailable")
	}
	partial := func(code string) {
		out.Outcome = effects.Partial
		out.Code = code
		out.Evidence.Outcome = effects.Partial
		out.Evidence.Phase = effects.InterruptedPhase
		out.Evidence.Trust[0].Outcome = effects.Partial
		out.Obligations = append(out.Obligations, effects.Obligation{RootID: r.Config.ID, Code: "recovery_required"})
	}
	defer func() {
		if session.Close() != nil {
			partial("config_lock_retained")
		}
		if c.Receipts.Record(context.WithoutCancel(ctx), out.Evidence.Clone()) != nil {
			partial("receipt_failed")
			if c.Receipts.Record(context.WithoutCancel(ctx), out.Evidence.Clone()) != nil {
				out.Obligations = append(out.Obligations, effects.Obligation{RootID: r.Config.ID, Code: "receipt_pending"})
			}
		}
	}()
	if err != nil {
		partial("config_lock_uncertain")
		return out
	}
	if ctx.Err() != nil || c.Validate(ctx) != nil {
		out.Outcome = effects.Refused
		out.Code = "authority_refused"
		out.Evidence.Outcome = out.Outcome
		out.Evidence.Phase = effects.InterruptedPhase
		return out
	}
	observed, mutated, err := session.Apply(ctx, r, c.Validate)
	if mutated {
		out.Outcome = effects.Applied
	} else {
		out.Outcome = effects.AlreadyPresent
	}
	out.Evidence.Trust[0].Present = observed.Present
	if err != nil || !observed.Present || observed.Target != out.Evidence.Trust[0].Target {
		if mutated {
			partial("config_update_uncertain")
		} else {
			out.Outcome = effects.Conflict
			out.Code = "config_changed"
			out.Evidence.Outcome = out.Outcome
			out.Evidence.Phase = effects.InterruptedPhase
		}
		return out
	}
	if ctx.Err() != nil || c.Validate(ctx) != nil {
		partial("authority_changed")
		return out
	}
	out.Code = "trust_complete"
	out.Evidence.Outcome = out.Outcome
	out.Evidence.Trust[0].Outcome = out.Outcome
	out.Evidence.Phase = effects.CompletePhase
	return out
}

// Inspect checks trusted, bound receipts without replaying or undoing a shared
// trust effect. An interrupted operation stays Partial even if trust is visible.
func Inspect(ctx context.Context, prepared Prepared, c effects.PreflightContext, p Port, e effects.Evidence) effects.Result {
	r := prepared.request
	if !prepared.valid || !binding(r, c) || e.Header != r.Header || e.Kind != effects.Trust || e.RootID != r.Config.ID || len(e.Trust) != 1 || len(e.Links) != 0 {
		return result(r, effects.Refused, "evidence_refused")
	}
	entry := e.Trust[0]
	target := filepath.Join(r.Target.CanonicalParent, filepath.Base(r.Target.LogicalPath))
	if entry.Mechanism != string(r.Mechanism) || entry.Target != target || entry.ConfigRootID != r.Config.ID || entry.AuthorizationID != r.AuthorizationID || entry.AuthorizationVersion != r.AuthorizationVersion {
		return result(r, effects.Refused, "evidence_refused")
	}
	if !r.Required && r.Mechanism != ClaudeProjects && e.Phase == effects.CompletePhase && e.Outcome == effects.Omitted && entry.Outcome == effects.Omitted && !entry.Present {
		return effects.Result{Outcome: effects.Omitted, Code: "inspection_complete", Evidence: e.Clone()}
	}
	complete := e.Phase == effects.CompletePhase && (e.Outcome == effects.Applied || e.Outcome == effects.AlreadyPresent) && entry.Outcome == e.Outcome && entry.Present
	interrupted := (e.Phase == effects.InterruptedPhase || e.Phase == effects.IntentPhase) && (e.Outcome == effects.Partial || e.Outcome == effects.Conflict || e.Outcome == effects.Refused || e.Outcome == effects.AlreadyPresent)
	if !complete && !interrupted {
		return result(r, effects.Refused, "evidence_refused")
	}
	out := effects.Result{Outcome: effects.Partial, Code: "inspection_complete", Evidence: e.Clone(), Obligations: []effects.Obligation{{RootID: r.Config.ID, Code: "recovery_required"}}}
	if ctx.Err() != nil || c.Validate(ctx) != nil || p == nil || !p.Supported(r.Mechanism) {
		out.Code = "inspection_unavailable"
		return out
	}
	observed, err := p.Observe(ctx, r)
	if err != nil || observed.Target != target {
		out.Code = "inspection_unavailable"
		return out
	}
	state := effects.Before
	if observed.Present {
		if entry.Present {
			state = effects.IntendedAfter
		} else {
			state = effects.Divergent
		}
	} else if complete {
		state = effects.Missing
	}
	out.Inspections = []effects.Inspection{{Destination: target, State: state}}
	if complete && observed.Present {
		out.Outcome = e.Outcome
		out.Obligations = nil
	} else if complete || state == effects.Divergent {
		out.Outcome = effects.Conflict
	}
	return out
}
