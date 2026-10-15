// Package trust applies explicitly authorized provider trust through host ports.
// Trust grants no sandbox or fabric permission and does not establish readiness.
package trust

import (
	"context"
	"github.com/hollis-labs/substrate/harness/workspace/effects"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
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
	// actual OR uncertain config mutation, including retained staging entries
	// and errors after replacement.
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
func lock(c effects.PreflightContext, id string, roots ...string) bool {
	for _, l := range c.HeldLocks {
		if l.CanonicalID != id || !absolute(l.Namespace) {
			continue
		}
		protected := true
		for _, root := range roots {
			if under(root, l.Namespace) {
				protected = false
				break
			}
		}
		if protected {
			return true
		}
	}
	return false
}
func binding(r Request, c effects.PreflightContext) bool {
	for _, v := range []string{r.Header.Version, r.Header.OperationID, r.Header.InputDigest, string(r.Mechanism), r.AuthorizationID, r.AuthorizationVersion, r.CandidateRootID, r.Config.ID, r.Config.Path, r.Config.AllowedBase, r.Config.Owner, r.Config.Provenance, r.Config.MutationIdentity, r.Target.LogicalPath, r.Target.CanonicalParent, r.Target.AllowedBase, r.Target.Provenance} {
		if !utf8.ValidString(v) || strings.ContainsRune(v, 0) {
			return false
		}
	}
	return r.Header.Valid() && r.Header == c.Header && r.AuthorizationID != "" && r.AuthorizationVersion != "" && r.CandidateRootID != "" && r.Config.ID != "" && r.Config.Owner != "" && r.Config.Provenance != "" && absolute(r.Config.Path) && absolute(r.Config.AllowedBase) && under(r.Config.AllowedBase, r.Config.Path) && r.Config.MutationIdentity == r.Config.Path && r.Target.Stable && r.Target.Provenance != "" && absolute(r.Target.LogicalPath) && absolute(r.Target.CanonicalParent) && absolute(r.Target.AllowedBase) && under(r.Target.AllowedBase, r.Target.CanonicalParent) && r.Target.LogicalPath != r.Target.CanonicalParent && lock(c, r.Config.MutationIdentity, r.Config.Path, r.Target.CanonicalParent) && lock(c, r.Target.CanonicalParent, r.Config.Path, r.Target.CanonicalParent) && c.Validate != nil
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
	out := result(r, effects.Prepared, "preflight_complete")
	out.Evidence.Trust[0].Target = observed.Target
	out.Evidence.Trust[0].Present = observed.Present
	return Prepared{request: r, valid: true}, out
}

const DefaultCleanupTimeout = 5 * time.Second

func cleanupContext(ctx context.Context, c effects.ApplyContext) (context.Context, context.CancelFunc) {
	budget := c.CleanupTimeout
	if budget <= 0 || budget > DefaultCleanupTimeout {
		budget = DefaultCleanupTimeout
	}
	return context.WithTimeout(context.WithoutCancel(ctx), budget)
}

func Apply(ctx context.Context, prepared Prepared, c effects.ApplyContext, p Port) (out effects.Result) {
	r := prepared.request
	if !prepared.valid {
		return result(r, effects.Refused, "invalid_prepared")
	}
	_, out = Preflight(ctx, r, c.PreflightContext, p)
	if out.Outcome != effects.Prepared && out.Outcome != effects.Omitted {
		return out
	}
	if c.Receipts == nil || c.ArtifactRootID != r.CandidateRootID || c.ArtifactGeneration == "" {
		return result(r, effects.Refused, "missing_apply_evidence")
	}
	out.Evidence.Trust[0].Target = filepath.Join(r.Target.CanonicalParent, filepath.Base(r.Target.LogicalPath))
	obligation := func(code string) {
		for _, old := range out.Obligations {
			if old.RootID == r.Config.ID && old.Code == code {
				return
			}
		}
		out.Obligations = append(out.Obligations, effects.Obligation{RootID: r.Config.ID, Code: code})
	}
	terminal := func(o effects.Outcome, phase effects.Phase, code string) {
		out.Outcome, out.Code = o, code
		out.Evidence.Outcome, out.Evidence.Phase = o, phase
		out.Evidence.Trust[0].Outcome = o
	}
	abort := func(o effects.Outcome, code string) effects.Result {
		terminal(o, effects.AbortedPhase, code)
		cleanupCtx, cancel := cleanupContext(ctx, c)
		defer cancel()
		if c.Receipts.Record(cleanupCtx, out.Evidence.Clone()) != nil {
			obligation("receipt_pending")
		}
		if cleanupCtx.Err() != nil {
			obligation("cleanup_deadline")
		}
		return out
	}
	if out.Outcome == effects.Omitted {
		out.Evidence.Phase = effects.CompletePhase
		if c.Receipts.Record(ctx, out.Evidence.Clone()) != nil {
			return abort(effects.Refused, "receipt_failed_before_mutation")
		}
		return out
	}
	terminal(effects.Pending, effects.IntentPhase, "")
	if c.Receipts.Record(ctx, out.Evidence.Clone()) != nil {
		return abort(effects.Refused, "receipt_failed_before_mutation")
	}
	// Receipt callbacks may cancel or revoke the host grant. Do not acquire the
	// provider lock until authority is refreshed after the durable intent.
	if ctx.Err() != nil || c.Validate(ctx) != nil {
		return abort(effects.Refused, "authority_refused")
	}
	session, err := p.Begin(ctx, r)
	if session == nil {
		return abort(effects.Conflict, "config_lock_unavailable")
	}
	partial := func(code string) {
		terminal(effects.Partial, effects.InterruptedPhase, code)
		obligation("recovery_required")
	}
	defer func() {
		// Host sessions must close promptly. No goroutine preemption can safely
		// interrupt a callback which may still own the shared configuration lock.
		if session.Close() != nil {
			partial("config_lock_retained")
		}
		cleanupCtx, cancel := cleanupContext(ctx, c)
		defer cancel()
		if c.Receipts.Record(cleanupCtx, out.Evidence.Clone()) != nil {
			partial("receipt_failed")
			if cleanupCtx.Err() != nil || c.Receipts.Record(cleanupCtx, out.Evidence.Clone()) != nil {
				obligation("receipt_pending")
			}
		}
		if cleanupCtx.Err() != nil {
			obligation("cleanup_deadline")
		}
	}()
	if err != nil {
		partial("config_lock_uncertain")
		return out
	}
	if ctx.Err() != nil || c.Validate(ctx) != nil {
		terminal(effects.Refused, effects.AbortedPhase, "authority_refused")
		return out
	}
	observed, mutated, err := session.Apply(ctx, r, c.Validate)
	out.Evidence.Trust[0].Present = observed.Present
	if err != nil || !observed.Present || observed.Target != out.Evidence.Trust[0].Target {
		if mutated {
			partial("config_update_uncertain")
		} else {
			terminal(effects.Conflict, effects.AbortedPhase, "config_changed")
		}
		return out
	}
	if ctx.Err() != nil || c.Validate(ctx) != nil {
		if mutated {
			partial("authority_changed")
		} else {
			terminal(effects.Refused, effects.AbortedPhase, "authority_changed")
		}
		return out
	}
	outcome := effects.AlreadyPresent
	if mutated {
		outcome = effects.Applied
	}
	terminal(outcome, effects.CompletePhase, "trust_complete")
	return out
}

// Inspect checks trusted, bound receipts without replaying or undoing shared
// trust. Actual or uncertain mutation retains Partial on divergence.
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
	omitted := !r.Required && e.Phase == effects.CompletePhase && e.Outcome == effects.Omitted && entry.Outcome == effects.Omitted && !entry.Present
	aborted := e.Phase == effects.AbortedPhase && (e.Outcome == effects.Refused || e.Outcome == effects.Conflict) && entry.Outcome == e.Outcome
	complete := e.Phase == effects.CompletePhase && (e.Outcome == effects.Applied || e.Outcome == effects.AlreadyPresent) && entry.Outcome == e.Outcome && entry.Present
	intent := e.Phase == effects.IntentPhase && e.Outcome == effects.Pending && entry.Outcome == effects.Pending
	interrupted := e.Phase == effects.InterruptedPhase && e.Outcome == effects.Partial && entry.Outcome == effects.Partial
	if (!omitted && !aborted && !complete && !intent && !interrupted) || (r.Mechanism != ClaudeProjects && !omitted && !aborted) {
		return result(r, effects.Refused, "evidence_refused")
	}
	// Pending intent cannot rule out a crash after replacement. Completed
	// AlreadyPresent and terminal aborts attest no mutation by this operation.
	possibleMutation := intent || interrupted || (complete && e.Outcome == effects.Applied)
	if len(e.Attachments) != 0 {
		out := result(r, effects.Refused, "evidence_shape_refused")
		out.Evidence = e.Clone()
		out.Evidence.Attachments = nil
		if possibleMutation {
			out.Obligations = []effects.Obligation{{RootID: r.Config.ID, Code: "recovery_required"}}
		}
		return out
	}
	unavailable := func() effects.Result {
		out := effects.Result{Outcome: effects.Refused, Code: "inspection_unavailable", Evidence: e.Clone()}
		if possibleMutation {
			out.Outcome = effects.Partial
			out.Obligations = []effects.Obligation{{RootID: r.Config.ID, Code: "recovery_required"}}
		}
		return out
	}
	if ctx.Err() != nil || c.Validate(ctx) != nil {
		return unavailable()
	}
	if omitted {
		return effects.Result{Outcome: effects.Omitted, Code: "inspection_complete", Evidence: e.Clone()}
	}
	if aborted {
		return effects.Result{Outcome: e.Outcome, Code: "inspection_aborted", Evidence: e.Clone()}
	}
	if p == nil || !p.Supported(r.Mechanism) {
		return unavailable()
	}
	observed, err := p.Observe(ctx, r)
	if err != nil || observed.Target != target {
		return unavailable()
	}
	out := effects.Result{Outcome: effects.Partial, Code: "inspection_complete", Evidence: e.Clone(), Obligations: []effects.Obligation{{RootID: r.Config.ID, Code: "recovery_required"}}}
	state := effects.Before
	if observed.Present {
		if entry.Present {
			state = effects.IntendedAfter
		} else {
			state = effects.Divergent
		}
	} else if entry.Present {
		state = effects.Missing
	}
	out.Inspections = []effects.Inspection{{Destination: target, State: state}}
	if complete && observed.Present {
		out.Outcome = e.Outcome
		out.Obligations = nil
	} else if !possibleMutation && (complete || state == effects.Divergent || state == effects.Missing) {
		out.Outcome = effects.Conflict
	}
	return out
}
