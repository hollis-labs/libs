package repositories

import (
	"context"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hollis-labs/substrate/harness/workspace/effects"
)

type Mode string

const (
	Worktree Mode = "worktree"
	Checkout Mode = "checkout"
	Readonly Mode = "readonly"
)

type Ownership string

const (
	Owned     Ownership = "owned"
	UserOwned Ownership = "user_owned"
)

// ReadonlyEvidence is a trusted host's sandbox enforcement proof, never chmod.
// Validate must refresh its authority and revision with all other grants.
type ReadonlyEvidence struct {
	Path, CommonPath, ID, Revision, Provenance string
	Enforced                                   bool
}
type Request struct {
	Header                                                                 effects.Header
	Mode                                                                   Mode
	Ownership                                                              Ownership
	Existing                                                               bool
	Source, Common, Base                                                   effects.RootInput
	Path, RepositoryID, SourceIdentity, CommonIdentity, Branch, BaseCommit string
	AuthorizationID, AuthorizationVersion, CandidateRootID                 string
	UserWriteAuthorizationID, UserWriteAuthorizationVersion                string
	Readonly                                                               ReadonlyEvidence
}
type Observation struct {
	Exists                                                                       bool
	Path, CommonPath, RepositoryID, SourceIdentity, CommonIdentity, Branch, Head string
	Dirty, Locked                                                                bool
}

// Safety is complete only after tracked, untracked, ignored, unknown and Git
// lock state have all been inspected. Any error or incomplete proof retains.
type Safety struct {
	Complete, Dirty, Untracked, Ignored, Unknown, Locked bool
	Head                                                 string
	Unreachable, Ahead                                   int
}
type Port interface {
	Supported(Request) bool
	Source(context.Context, Request) (Observation, error)
	ResolveBase(context.Context, Request) (string, error)
	BranchAvailable(context.Context, Request) error
	Observe(context.Context, Request) (Observation, error)
	// Create never fabricates parents, fetches, resets, cleans or falls back to
	// detached state. The bool means actual OR uncertain mutation. It refreshes
	// authority immediately before the Git action and rechecks root custody.
	Create(context.Context, Request, func(context.Context) error) (Observation, bool, error)
	Safety(context.Context, Request, effects.AttachmentEvidence) (Safety, error)
	// Remove performs only conditional, non-forced Git removal; no folder
	// deletion or prune. The bool includes uncertain removal; errors retain.
	Remove(context.Context, Request, effects.AttachmentEvidence, func(context.Context) error) (bool, error)
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
func commit(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
func binding(r Request, c effects.PreflightContext) bool {
	if !r.Header.Valid() || r.Header != c.Header || c.Validate == nil || r.AuthorizationID == "" || r.AuthorizationVersion == "" || r.CandidateRootID == "" || r.RepositoryID == "" || r.SourceIdentity == "" || r.CommonIdentity == "" || !absolute(r.Path) || filepath.Dir(r.Path) != r.Base.Path {
		return false
	}
	roots := []effects.RootInput{r.Source, r.Common, r.Base}
	for _, v := range []string{string(r.Mode), string(r.Ownership), r.Path, r.RepositoryID, r.SourceIdentity, r.CommonIdentity, r.Branch, r.BaseCommit, r.AuthorizationID, r.AuthorizationVersion, r.CandidateRootID, r.UserWriteAuthorizationID, r.UserWriteAuthorizationVersion} {
		if !utf8.ValidString(v) || strings.ContainsRune(v, 0) {
			return false
		}
	}
	for _, root := range roots {
		if root.ID == "" || root.Owner == "" || root.Provenance == "" || !absolute(root.Path) || !absolute(root.AllowedBase) || !under(root.AllowedBase, root.Path) || root.MutationIdentity != root.Path {
			return false
		}
		found := false
		for _, l := range c.HeldLocks {
			if l.CanonicalID != root.Path || !absolute(l.Namespace) {
				continue
			}
			outside := true
			for _, other := range roots {
				if under(other.Path, l.Namespace) {
					outside = false
				}
			}
			if outside {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	if !r.Existing && (under(r.Path, r.Source.Path) || under(r.Path, r.Common.Path) || under(r.Source.Path, r.Path) || under(r.Common.Path, r.Path)) {
		return false
	}
	switch r.Mode {
	case Worktree:
		return !r.Existing && r.Ownership == Owned && r.Base.PrivateCustody && ValidBranch(r.Branch) && commit(r.BaseCommit)
	case Checkout:
		if r.Existing {
			return r.Ownership == UserOwned && r.UserWriteAuthorizationID != "" && r.UserWriteAuthorizationVersion != "" && (r.Branch == "" || ValidBranch(r.Branch))
		}
		return r.Ownership == Owned && r.Base.PrivateCustody && ValidBranch(r.Branch) && commit(r.BaseCommit)
	case Readonly:
		return r.Existing && r.Ownership == UserOwned && (r.Branch == "" || ValidBranch(r.Branch)) && r.Readonly.Enforced && r.Readonly.Path == r.Path && r.Readonly.CommonPath == r.Common.Path && r.Readonly.ID != "" && r.Readonly.Revision != "" && r.Readonly.Provenance != ""
	}
	return false
}
func sourceMatches(r Request, o Observation) bool {
	return o.Exists && o.Path == r.Source.Path && o.CommonPath == r.Common.Path && o.RepositoryID == r.RepositoryID && o.SourceIdentity == r.SourceIdentity && o.CommonIdentity == r.CommonIdentity && commit(o.Head)
}
func matches(r Request, o Observation, resume bool) bool {
	if !o.Exists || o.Path != r.Path || o.CommonPath != r.Common.Path || o.RepositoryID != r.RepositoryID || o.SourceIdentity != r.SourceIdentity || o.CommonIdentity != r.CommonIdentity || !commit(o.Head) {
		return false
	}
	if !r.Existing {
		return o.Branch == r.Branch && (resume || o.Head == r.BaseCommit)
	}
	return r.Branch == "" || o.Branch == r.Branch
}
func entry(r Request, o effects.Outcome) effects.AttachmentEvidence {
	return effects.AttachmentEvidence{Owner: r.Base.Owner, UserWriteAuthorizationID: r.UserWriteAuthorizationID, UserWriteAuthorizationVersion: r.UserWriteAuthorizationVersion, ReadonlyProofID: r.Readonly.ID, ReadonlyProofRevision: r.Readonly.Revision, ReadonlyProvenance: r.Readonly.Provenance, Mode: string(r.Mode), Ownership: string(r.Ownership), Path: r.Path, SourcePath: r.Source.Path, CommonPath: r.Common.Path, RepositoryID: r.RepositoryID, SourceIdentity: r.SourceIdentity, CommonIdentity: r.CommonIdentity, Branch: r.Branch, BaseCommit: r.BaseCommit, AuthorizationID: r.AuthorizationID, AuthorizationVersion: r.AuthorizationVersion, Outcome: o}
}
func result(r Request, o effects.Outcome, code string) effects.Result {
	return effects.Result{Outcome: o, Code: code, Evidence: effects.Evidence{Header: r.Header, Kind: effects.RepositoryAttachment, RootID: r.Base.ID, Phase: effects.PreflightPhase, Outcome: o, Attachments: []effects.AttachmentEvidence{entry(r, o)}}}
}
func precheck(ctx context.Context, r Request, c effects.PreflightContext, p Port) (Observation, string) {
	if ctx.Err() != nil || c.Validate(ctx) != nil || ctx.Err() != nil {
		return Observation{}, "authority_refused"
	}
	s, e := p.Source(ctx, r)
	if e != nil || !sourceMatches(r, s) {
		return Observation{}, "source_changed"
	}
	if !r.Existing {
		base, e := p.ResolveBase(ctx, r)
		if e != nil || base != r.BaseCommit {
			return Observation{}, "base_unavailable"
		}
		if p.BranchAvailable(ctx, r) != nil {
			return Observation{}, "branch_conflict"
		}
	}
	o, e := p.Observe(ctx, r)
	if e != nil {
		return o, "observation_unavailable"
	}
	if r.Existing {
		if !matches(r, o, true) {
			return o, "attachment_conflict"
		}
	} else if o.Exists {
		return o, "attachment_conflict"
	}
	return o, ""
}
func Preflight(ctx context.Context, r Request, c effects.PreflightContext, p Port) (Prepared, effects.Result) {
	if !binding(r, c) {
		return Prepared{}, result(r, effects.Refused, "binding_refused")
	}
	if p == nil || !p.Supported(r) {
		return Prepared{}, result(r, effects.Unsupported, "mode_unsupported")
	}
	if _, code := precheck(ctx, r, c, p); code != "" {
		return Prepared{}, result(r, effects.Refused, code)
	}
	return Prepared{request: r, valid: true}, result(r, effects.Prepared, "preflight_complete")
}
func cleanup(ctx context.Context, c effects.ApplyContext) (context.Context, context.CancelFunc) {
	d := 5 * time.Second
	if c.CleanupTimeout > 0 && c.CleanupTimeout < d {
		d = c.CleanupTimeout
	}
	return context.WithTimeout(context.WithoutCancel(ctx), d)
}
func finish(ctx context.Context, c effects.ApplyContext, out effects.Result, o effects.Outcome, phase effects.Phase, code string, uncertain bool) effects.Result {
	out.Outcome = o
	out.Code = code
	out.Evidence.Outcome = o
	out.Evidence.Phase = phase
	out.Evidence.Attachments[0].Outcome = o
	if out.Evidence.Kind == effects.RepositoryRetirement && o != effects.Removed {
		out.Obligations = append(out.Obligations, effects.Obligation{RootID: out.Evidence.RootID, Code: "retain_attachment"})
	}
	if uncertain {
		out.Obligations = append(out.Obligations, effects.Obligation{RootID: out.Evidence.RootID, Code: "recovery_required"})
	}
	final, cancel := cleanup(ctx, c)
	defer cancel()
	if c.Receipts.Record(final, out.Evidence.Clone()) != nil || final.Err() != nil {
		out.Obligations = append(out.Obligations, effects.Obligation{RootID: out.Evidence.RootID, Code: "receipt_pending"})
		if uncertain || phase == effects.CompletePhase {
			out.Outcome = effects.Partial
			out.Evidence.Outcome = effects.Partial
			out.Evidence.Phase = effects.InterruptedPhase
			out.Evidence.Attachments[0].Outcome = effects.Partial
			out.Obligations = append(out.Obligations, effects.Obligation{RootID: out.Evidence.RootID, Code: "recovery_required"})
		}
	}
	return out
}
func Apply(ctx context.Context, prepared Prepared, c effects.ApplyContext, p Port) effects.Result {
	r := prepared.request
	out := result(r, effects.Refused, "binding_refused")
	if !prepared.valid || !binding(r, c.PreflightContext) || c.ArtifactRootID != r.CandidateRootID || c.ArtifactGeneration == "" || c.Receipts == nil {
		return out
	}
	if p == nil || !p.Supported(r) {
		return result(r, effects.Unsupported, "mode_unsupported")
	}
	if _, code := precheck(ctx, r, c.PreflightContext, p); code != "" {
		return finish(ctx, c, out, effects.Refused, effects.AbortedPhase, code, false)
	}
	out = result(r, effects.Pending, "attachment_intent")
	out.Evidence.Phase = effects.IntentPhase
	if c.Receipts.Record(ctx, out.Evidence.Clone()) != nil {
		return finish(ctx, c, out, effects.Refused, effects.AbortedPhase, "intent_failed", false)
	}
	o, code := precheck(ctx, r, c.PreflightContext, p)
	if code != "" {
		return finish(ctx, c, out, effects.Refused, effects.AbortedPhase, code, false)
	}
	if r.Existing {
		out.Evidence.Attachments[0].Head = o.Head
		out.Evidence.Attachments[0].Branch = o.Branch
		return finish(ctx, c, out, effects.AlreadyPresent, effects.CompletePhase, "attachment_complete", false)
	}
	o, mutated, e := p.Create(ctx, r, c.Validate)
	if e != nil {
		if mutated {
			return finish(ctx, c, out, effects.Partial, effects.InterruptedPhase, "create_uncertain", true)
		}
		return finish(ctx, c, out, effects.Refused, effects.AbortedPhase, "create_refused", false)
	}
	if !mutated || !matches(r, o, false) {
		return finish(ctx, c, out, effects.Partial, effects.InterruptedPhase, "created_state_unproved", true)
	}
	out.Evidence.Attachments[0].Created = true
	out.Evidence.Attachments[0].Head = o.Head
	if ctx.Err() != nil || c.Validate(ctx) != nil || ctx.Err() != nil {
		return finish(ctx, c, out, effects.Partial, effects.InterruptedPhase, "authority_lost", true)
	}
	verified, e := p.Observe(ctx, r)
	if e != nil || !matches(r, verified, false) {
		return finish(ctx, c, out, effects.Partial, effects.InterruptedPhase, "created_state_changed", true)
	}
	return finish(ctx, c, out, effects.Applied, effects.CompletePhase, "attachment_complete", false)
}
func receiptBound(r Request, e effects.Evidence) bool {
	if e.Header != r.Header || e.Kind != effects.RepositoryAttachment || e.RootID != r.Base.ID || len(e.Attachments) != 1 || len(e.Links) != 0 || len(e.Trust) != 0 {
		return false
	}
	a := e.Attachments[0]
	want := entry(r, a.Outcome)
	return a.OriginHeader == want.OriginHeader && a.ShippedProofID == "" && a.ShippedProofRevision == "" && a.UnusedProofID == "" && a.UnusedProofRevision == "" && a.RetirementProvenance == "" && !a.SafetyComplete && a.Owner == want.Owner && a.UserWriteAuthorizationID == want.UserWriteAuthorizationID && a.UserWriteAuthorizationVersion == want.UserWriteAuthorizationVersion && a.ReadonlyProofID == want.ReadonlyProofID && a.ReadonlyProofRevision == want.ReadonlyProofRevision && a.ReadonlyProvenance == want.ReadonlyProvenance && a.Mode == want.Mode && a.Ownership == want.Ownership && a.Path == want.Path && a.SourcePath == want.SourcePath && a.CommonPath == want.CommonPath && a.RepositoryID == want.RepositoryID && a.SourceIdentity == want.SourceIdentity && a.CommonIdentity == want.CommonIdentity && a.BaseCommit == want.BaseCommit && a.AuthorizationID == want.AuthorizationID && a.AuthorizationVersion == want.AuthorizationVersion && (r.Existing && r.Branch == "" || a.Branch == want.Branch)
}

// InspectResume never re-evaluates a branch template or resets to the base.
// A completed owned attachment can advance HEAD or contain legitimate dirty work.
func InspectResume(ctx context.Context, r Request, c effects.PreflightContext, p Port, e effects.Evidence) effects.Result {
	if !binding(r, c) || !receiptBound(r, e) {
		return result(r, effects.Refused, "evidence_refused")
	}
	a := e.Attachments[0]
	complete := e.Phase == effects.CompletePhase && e.Outcome == a.Outcome && (a.Outcome == effects.Applied || a.Outcome == effects.AlreadyPresent) && commit(a.Head) && ((a.Outcome == effects.Applied && !r.Existing && a.Created) || (a.Outcome == effects.AlreadyPresent && r.Existing && !a.Created))
	uncertain := (e.Phase == effects.IntentPhase && e.Outcome == effects.Pending && a.Outcome == effects.Pending) || (e.Phase == effects.InterruptedPhase && e.Outcome == effects.Partial && a.Outcome == effects.Partial)
	aborted := e.Phase == effects.AbortedPhase && e.Outcome == effects.Refused && a.Outcome == effects.Refused && !a.Created
	if !complete && !uncertain && !aborted {
		return result(r, effects.Refused, "evidence_refused")
	}
	out := effects.Result{Outcome: effects.Partial, Code: "inspection_complete", Evidence: e.Clone(), Obligations: []effects.Obligation{{RootID: r.Base.ID, Code: "recovery_required"}}}
	if ctx.Err() != nil || c.Validate(ctx) != nil || ctx.Err() != nil || p == nil || !p.Supported(r) {
		return out
	}
	if aborted {
		out.Outcome = effects.Refused
		out.Obligations = nil
		return out
	}
	o, err := p.Observe(ctx, r)
	if err != nil {
		return out
	}
	state := effects.Missing
	if o.Exists {
		state = effects.Divergent
		if matches(r, o, true) && o.Branch == a.Branch {
			state = effects.IntendedAfter
		}
	}
	out.Inspections = []effects.Inspection{{Destination: r.Path, State: state}}
	if complete {
		out.Obligations = nil
		if state == effects.IntendedAfter {
			out.Outcome = effects.AlreadyPresent
		} else {
			out.Outcome = effects.Conflict
			out.Obligations = []effects.Obligation{{RootID: r.Base.ID, Code: "retain_attachment"}}
		}
	}
	return out
}
