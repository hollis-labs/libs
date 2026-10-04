package repositories

import (
	"context"
	"github.com/hollis-labs/substrate/harness/workspace/effects"
)

// Retirement requires separately authorized shipped-HEAD and unused-resource
// proofs. The host refreshes these proofs through Validate under complete locks.
type Retirement struct {
	Header                                             effects.Header
	Attachment                                         Request
	Receipt                                            effects.Evidence
	AcceptedHead, ShippedProofID, ShippedProofRevision string
	UnusedProofID, UnusedProofRevision, Provenance     string
	AuthorizationID, AuthorizationVersion              string
}
type PreparedRetirement struct {
	request Retirement
	valid   bool
}

func retirementBinding(r Retirement, c effects.PreflightContext) bool {
	old := c
	old.Header = r.Attachment.Header
	e := r.Receipt
	return r.Header.Valid() && r.Header == c.Header && binding(r.Attachment, old) && r.AuthorizationID != "" && r.AuthorizationVersion != "" && r.Provenance != "" && r.ShippedProofID != "" && r.ShippedProofRevision != "" && r.UnusedProofID != "" && r.UnusedProofRevision != "" && commit(r.AcceptedHead) && r.Attachment.Mode == Worktree && r.Attachment.Ownership == Owned && !r.Attachment.Existing && receiptBound(r.Attachment, e) && e.Phase == effects.CompletePhase && e.Outcome == effects.Applied && e.Attachments[0].Outcome == effects.Applied && e.Attachments[0].Created && commit(e.Attachments[0].Head)
}
func retirementEntry(r Retirement) effects.AttachmentEvidence {
	a := r.Receipt.Attachments[0]
	a.Head = r.AcceptedHead
	a.OriginHeader = r.Attachment.Header
	a.ShippedProofID = r.ShippedProofID
	a.ShippedProofRevision = r.ShippedProofRevision
	a.UnusedProofID = r.UnusedProofID
	a.UnusedProofRevision = r.UnusedProofRevision
	a.RetirementProvenance = r.Provenance
	a.AuthorizationID = r.AuthorizationID
	a.AuthorizationVersion = r.AuthorizationVersion
	return a
}
func retirementResult(r Retirement, o effects.Outcome, code string) effects.Result {
	out := result(r.Attachment, o, code)
	out.Evidence.Header = r.Header
	out.Evidence.Kind = effects.RepositoryRetirement
	if len(r.Receipt.Attachments) == 1 {
		out.Evidence.Attachments[0] = retirementEntry(r)
		out.Evidence.Attachments[0].Outcome = o
	}
	return out
}
func safe(s Safety, head string) bool {
	return s.Complete && s.Head == head && !s.Dirty && !s.Untracked && !s.Ignored && !s.Unknown && !s.Locked && s.Unreachable == 0 && s.Ahead == 0
}
func retirementCheck(ctx context.Context, r Retirement, c effects.PreflightContext, p Port) bool {
	if ctx.Err() != nil || c.Validate(ctx) != nil || ctx.Err() != nil {
		return false
	}
	source, e := p.Source(ctx, r.Attachment)
	if e != nil || !sourceMatches(r.Attachment, source) {
		return false
	}
	o, e := p.Observe(ctx, r.Attachment)
	if e != nil || !matches(r.Attachment, o, true) || o.Head != r.AcceptedHead || o.Branch != r.Receipt.Attachments[0].Branch {
		return false
	}
	s, e := p.Safety(ctx, r.Attachment, retirementEntry(r))
	return e == nil && safe(s, r.AcceptedHead)
}
func CheckRetirement(ctx context.Context, r Retirement, c effects.PreflightContext, p Port) (PreparedRetirement, effects.Result) {
	if !retirementBinding(r, c) {
		return PreparedRetirement{}, retirementResult(r, effects.Refused, "retirement_binding_refused")
	}
	if p == nil || !p.Supported(r.Attachment) {
		return PreparedRetirement{}, retirementResult(r, effects.Unsupported, "mode_unsupported")
	}
	if !retirementCheck(ctx, r, c, p) {
		out := retirementResult(r, effects.Conflict, "retain_attachment")
		out.Obligations = []effects.Obligation{{RootID: r.Attachment.Base.ID, Code: "retain_attachment"}}
		return PreparedRetirement{}, out
	}
	r.Receipt = r.Receipt.Clone()
	return PreparedRetirement{request: r, valid: true}, retirementResult(r, effects.Prepared, "retirement_preflight_complete")
}

// Retire removes only the operation-owned registered worktree through its port.
// It never performs generic folder cleanup, force removal or branch deletion.
func Retire(ctx context.Context, ticket PreparedRetirement, c effects.ApplyContext, p Port) effects.Result {
	r := ticket.request
	out := retirementResult(r, effects.Refused, "retirement_binding_refused")
	if !ticket.valid || !retirementBinding(r, c.PreflightContext) || c.Receipts == nil {
		return out
	}
	if p == nil || !p.Supported(r.Attachment) {
		return retirementResult(r, effects.Unsupported, "mode_unsupported")
	}
	if !retirementCheck(ctx, r, c.PreflightContext, p) {
		return finish(ctx, c, out, effects.Conflict, effects.AbortedPhase, "retain_attachment", false)
	}
	out = retirementResult(r, effects.Pending, "retirement_intent")
	out.Evidence.Phase = effects.IntentPhase
	if c.Receipts.Record(ctx, out.Evidence.Clone()) != nil {
		return finish(ctx, c, out, effects.Refused, effects.AbortedPhase, "intent_failed", false)
	}
	if !retirementCheck(ctx, r, c.PreflightContext, p) {
		return finish(ctx, c, out, effects.Conflict, effects.AbortedPhase, "retain_attachment", false)
	}
	out.Evidence.Attachments[0].SafetyComplete = true
	removed, e := p.Remove(ctx, r.Attachment, retirementEntry(r), c.Validate)
	if e != nil {
		if removed {
			return finish(ctx, c, out, effects.Partial, effects.InterruptedPhase, "removal_uncertain", true)
		}
		return finish(ctx, c, out, effects.Conflict, effects.AbortedPhase, "retain_attachment", false)
	}
	if !removed {
		return finish(ctx, c, out, effects.Conflict, effects.AbortedPhase, "retain_attachment", false)
	}
	o, e := p.Observe(ctx, r.Attachment)
	if e != nil || o.Exists {
		return finish(ctx, c, out, effects.Partial, effects.InterruptedPhase, "removal_unproved", true)
	}
	return finish(ctx, c, out, effects.Removed, effects.CompletePhase, "retirement_complete", false)
}

// InspectRetirement classifies bound evidence without repeating removal.
// Missing work after interrupted removal does not erase its uncertainty.
func InspectRetirement(ctx context.Context, r Retirement, c effects.PreflightContext, p Port, e effects.Evidence) effects.Result {
	if !retirementBinding(r, c) || e.Header != r.Header || e.Kind != effects.RepositoryRetirement || e.RootID != r.Attachment.Base.ID || len(e.Attachments) != 1 || len(e.Links) != 0 || len(e.Trust) != 0 {
		return retirementResult(r, effects.Refused, "evidence_refused")
	}
	a := e.Attachments[0]
	want := retirementEntry(r)
	want.Outcome = a.Outcome
	want.SafetyComplete = a.SafetyComplete
	if a != want || e.Outcome != a.Outcome {
		return retirementResult(r, effects.Refused, "evidence_refused")
	}
	complete := e.Phase == effects.CompletePhase && e.Outcome == effects.Removed && a.SafetyComplete
	aborted := e.Phase == effects.AbortedPhase && (e.Outcome == effects.Refused || e.Outcome == effects.Conflict)
	uncertain := e.Phase == effects.IntentPhase && e.Outcome == effects.Pending || e.Phase == effects.InterruptedPhase && e.Outcome == effects.Partial
	if !complete && !aborted && !uncertain {
		return retirementResult(r, effects.Refused, "evidence_refused")
	}
	out := effects.Result{Outcome: effects.Partial, Code: "retirement_inspection_complete", Evidence: e.Clone(), Obligations: []effects.Obligation{{RootID: e.RootID, Code: "recovery_required"}, {RootID: e.RootID, Code: "retain_attachment"}}}
	if ctx.Err() != nil || c.Validate(ctx) != nil || ctx.Err() != nil || p == nil || !p.Supported(r.Attachment) {
		return out
	}
	if aborted {
		out.Outcome = e.Outcome
		out.Obligations = out.Obligations[1:]
		return out
	}
	o, err := p.Observe(ctx, r.Attachment)
	if err != nil {
		return out
	}
	state := effects.IntendedAfter
	if o.Exists {
		state = effects.Divergent
	}
	out.Inspections = []effects.Inspection{{Destination: r.Attachment.Path, State: state}}
	if complete && !o.Exists {
		out.Outcome = effects.Removed
		out.Obligations = nil
	}
	return out
}
