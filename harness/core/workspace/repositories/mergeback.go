package repositories

import (
	"context"

	"github.com/hollis-labs/substrate/harness/workspace/effects"
)

// MergeBackPort transfers a clone attachment's branch into its source
// repository. It is a separate Git transfer effect: attachment creation,
// capture grants and clone authority never authorize it.
type MergeBackPort interface {
	// TargetRef observes refs/heads/<branch> in the source's common metadata:
	// its full commit ID, empty when absent, and whether any source worktree
	// has the branch checked out. It never mutates.
	TargetRef(ctx context.Context, r Request, branch string) (oid string, checkedOut bool, err error)
	// MergeBack fetches the clone's branch objects into the source without
	// updating any ref, proves head descends from the base and from before,
	// then moves refs/heads/<branch> from exactly before to head (empty before
	// creates only). It never forces, never touches a worktree or index and
	// never fetches from anywhere but the clone. The bool means actual or
	// uncertain mutation. It refreshes authority immediately before the ref
	// update.
	MergeBack(ctx context.Context, r Request, branch, before, head string, validate func(context.Context) error) (bool, error)
}

// MergeBack is an explicitly authorized fast-forward of one source branch to
// the head of a completed copy-on-write clone attachment. ExpectedTarget is the
// exact current source ref, empty when it must be absent.
type MergeBack struct {
	Header                                effects.Header
	Attachment                            Request
	Receipt                               effects.Evidence
	TargetBranch, ExpectedTarget          string
	AuthorizationID, AuthorizationVersion string
}
type PreparedMergeBack struct {
	request MergeBack
	head    string
	valid   bool
}

func mergeBackAttachment(m MergeBack) (MergeBack, bool) {
	if len(m.Receipt.Attachments) != 1 {
		return m, false
	}
	bound, ok := BindSelection(m.Attachment, m.Receipt.Attachments[0])
	m.Attachment = bound
	return m, ok && bound.Mode == Clone
}
func mergeBackBinding(m MergeBack, c effects.PreflightContext) bool {
	old := c
	old.Header = m.Attachment.Header
	e := m.Receipt
	if !m.Header.Valid() || m.Header != c.Header || m.AuthorizationID == "" || m.AuthorizationVersion == "" || !ValidBranch(m.TargetBranch) || m.ExpectedTarget != "" && !commit(m.ExpectedTarget) {
		return false
	}
	if !binding(m.Attachment, old) || !receiptBound(m.Attachment, e) {
		return false
	}
	a := e.Attachments[0]
	return e.Phase == effects.CompletePhase && e.Outcome == effects.Applied && a.Outcome == effects.Applied && a.Created && commit(a.Head)
}
func mergeBackEntry(m MergeBack, head string, o effects.Outcome) effects.AttachmentEvidence {
	a := entry(m.Attachment, o)
	a.OriginHeader = m.Attachment.Header
	a.Created = false
	a.Head = head
	a.AuthorizationID = m.AuthorizationID
	a.AuthorizationVersion = m.AuthorizationVersion
	a.TargetBranch = m.TargetBranch
	a.TargetBefore = m.ExpectedTarget
	return a
}
func mergeBackResult(m MergeBack, head string, o effects.Outcome, code string) effects.Result {
	return effects.Result{Outcome: o, Code: code, Evidence: effects.Evidence{Header: m.Header, Kind: effects.RepositoryMergeBack, RootID: m.Attachment.Common.ID, Phase: effects.PreflightPhase, Outcome: o, Attachments: []effects.AttachmentEvidence{mergeBackEntry(m, head, o)}}}
}

// mergeBackCheck observes the clone head and the exact source target. It
// returns the head to transfer, or a refusal code.
func mergeBackCheck(ctx context.Context, m MergeBack, c effects.PreflightContext, p MergeBackPort, port Port) (string, string) {
	r := m.Attachment
	if ctx.Err() != nil || c.Validate(ctx) != nil || ctx.Err() != nil {
		return "", "authority_refused"
	}
	s, e := port.Source(ctx, r)
	if e != nil || !sourceMatches(r, s) {
		return "", "source_changed"
	}
	o, e := port.Observe(ctx, r)
	if e != nil || !matches(r, o, true) || o.Branch != r.Branch {
		return "", "attachment_conflict"
	}
	target, checkedOut, e := p.TargetRef(ctx, r, m.TargetBranch)
	if e != nil || checkedOut {
		return "", "target_unavailable"
	}
	if target != m.ExpectedTarget {
		return "", "target_changed"
	}
	if ctx.Err() != nil || c.Validate(ctx) != nil || ctx.Err() != nil {
		return "", "authority_refused"
	}
	return o.Head, ""
}
func mergeBackPort(m MergeBack, port Port) (MergeBackPort, bool) {
	p, ok := port.(MergeBackPort)
	return p, ok && port.Supported(m.Attachment)
}

// CheckMergeBack is observational; it validates the completed clone receipt,
// the explicit merge-back authorization and the exact source target.
func CheckMergeBack(ctx context.Context, m MergeBack, c effects.PreflightContext, port Port) (PreparedMergeBack, effects.Result) {
	m, ok := mergeBackAttachment(m)
	if !ok || !mergeBackBinding(m, c) {
		return PreparedMergeBack{}, mergeBackResult(m, "", effects.Refused, "mergeback_binding_refused")
	}
	p, ok := mergeBackPort(m, port)
	if !ok {
		return PreparedMergeBack{}, mergeBackResult(m, "", effects.Unsupported, "mergeback_unsupported")
	}
	head, code := mergeBackCheck(ctx, m, c, p, port)
	if code != "" {
		return PreparedMergeBack{}, mergeBackResult(m, "", effects.Refused, code)
	}
	m.Receipt = m.Receipt.Clone()
	return PreparedMergeBack{request: m, head: head, valid: true}, mergeBackResult(m, head, effects.Prepared, "mergeback_preflight_complete")
}

// ApplyMergeBack records intent, rechecks, then fast-forwards the source ref
// through the port. A moved clone head or source ref refuses; nothing is
// forced, reset or retried.
func ApplyMergeBack(ctx context.Context, ticket PreparedMergeBack, c effects.ApplyContext, port Port) effects.Result {
	m := ticket.request
	out := mergeBackResult(m, ticket.head, effects.Refused, "mergeback_binding_refused")
	if !ticket.valid || !mergeBackBinding(m, c.PreflightContext) || c.Receipts == nil {
		return out
	}
	p, ok := mergeBackPort(m, port)
	if !ok {
		return mergeBackResult(m, ticket.head, effects.Unsupported, "mergeback_unsupported")
	}
	if head, code := mergeBackCheck(ctx, m, c.PreflightContext, p, port); code != "" || head != ticket.head {
		if code == "" {
			code = "attachment_conflict"
		}
		return finish(ctx, c, out, effects.Refused, effects.AbortedPhase, code, false)
	}
	if ticket.head == m.ExpectedTarget {
		return finish(ctx, c, out, effects.AlreadyPresent, effects.CompletePhase, "mergeback_complete", false)
	}
	out = mergeBackResult(m, ticket.head, effects.Pending, "mergeback_intent")
	out.Evidence.Phase = effects.IntentPhase
	if c.Receipts.Record(ctx, out.Evidence.Clone()) != nil {
		return finish(ctx, c, out, effects.Refused, effects.AbortedPhase, "intent_failed", false)
	}
	if head, code := mergeBackCheck(ctx, m, c.PreflightContext, p, port); code != "" || head != ticket.head {
		if code == "" {
			code = "attachment_conflict"
		}
		return finish(ctx, c, out, effects.Refused, effects.AbortedPhase, code, false)
	}
	mutated, e := p.MergeBack(ctx, m.Attachment, m.TargetBranch, m.ExpectedTarget, ticket.head, c.Validate)
	if e != nil {
		if mutated {
			return finish(ctx, c, out, effects.Partial, effects.InterruptedPhase, "mergeback_uncertain", true)
		}
		return finish(ctx, c, out, effects.Refused, effects.AbortedPhase, "mergeback_refused", false)
	}
	target, _, e := p.TargetRef(ctx, m.Attachment, m.TargetBranch)
	if !mutated || e != nil || target != ticket.head || ctx.Err() != nil || c.Validate(ctx) != nil || ctx.Err() != nil {
		return finish(ctx, c, out, effects.Partial, effects.InterruptedPhase, "mergeback_unproved", true)
	}
	return finish(ctx, c, out, effects.Applied, effects.CompletePhase, "mergeback_complete", false)
}
