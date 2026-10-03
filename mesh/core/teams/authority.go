package teams

import (
	"fmt"

	"github.com/hollis-labs/substrate/mesh"
)

// Authorize requires real roster members. The host supplies authenticated
// actor URNs; callers never supply a claimed source slot. Kind and governance
// don't confer grants. Denials win over positive grants, including dev-open.
func Authorize(t Team, from, to Member, verb Permission, authorActor string) error {
	if t.Authority.Mode != "" && t.Authority.Mode != DevOpen && t.Authority.Mode != Strict {
		return ErrDenied
	}
	if !validPermission(verb) || verb == MayNotReview || from.Actor == "" || to.Actor == "" || from.Status != "active" || (to.Status != "active" && !(verb == MayApprove && (to.Status == "stopped" || to.Status == "released"))) {
		return ErrDenied
	}
	if _, ok := t.slot(from.Slot); !ok {
		return ErrDenied
	}
	if _, ok := t.slot(to.Slot); !ok {
		return ErrDenied
	}
	if verb == MayApprove && (from.Actor == to.Actor || string(from.Actor) == authorActor) {
		return fmt.Errorf("%w: self approval", ErrDenied)
	}
	governed, allowed := false, false
	for _, g := range t.Authority.Grants {
		source := (g.FromActor != "" && g.FromActor == from.Actor) || (g.FromSlot != "" && g.FromSlot == from.Slot)
		if !source {
			continue
		}
		governed = true
		target := (g.ToActor != "" && g.ToActor == to.Actor) || (g.ToSlot == Self && from.Actor == to.Actor) || (g.ToSlot != "" && g.ToSlot != Self && g.ToSlot == to.Slot)
		if verb == MayApprove && g.Verb == MayNotReview && (target || (g.ToSlot == Self && string(from.Actor) == authorActor)) {
			return fmt.Errorf("%w: may_not_review", ErrDenied)
		}
		if g.Verb == verb {
			if target {
				allowed = true
			}
		}
	}
	// Approvals always require an explicit grant, even on development teams.
	if allowed || (verb != MayApprove && t.Authority.Mode != Strict && !governed) {
		return nil
	}
	return fmt.Errorf("%w: %s -> %s (%s)", ErrDenied, from.Slot, to.Slot, verb)
}

// CheckApproval enforces approver_slot and author exclusion before the host
// resolves a wait. A person receives no special bypass.
func CheckApproval(t Team, r Roster, phase Phase, approver, subject string, author string) error {
	authored, err := t.phase(phase.ID)
	if err != nil {
		return err
	}
	phase = authored
	if author == "" || mesh.URN(author).Validate() != nil {
		return ErrDenied
	}
	from, err := r.memberURN(approver)
	if err != nil {
		return err
	}
	if _, err := r.knownMember(mesh.URN(author)); err != nil {
		return err
	}
	to, err := r.knownMember(mesh.URN(subject))
	if err != nil {
		return err
	}
	if phase.ApproverSlot == "" || from.Slot != phase.ApproverSlot {
		return ErrDenied
	}
	return Authorize(t, from, to, MayApprove, author)
}

func (r Roster) memberURN(actor string) (Member, error) { return r.member(mesh.URN(actor)) }

// Roster administration is never dev-open. A run owner/admin may govern its
// membership; ordinary members require an explicit administrative grant.
func authorizeAdmin(t Team, from, to Member) error {
	if from.Governance == Owner || from.Governance == Admin {
		return nil
	}
	t.Authority.Mode = Strict
	return Authorize(t, from, authorizationTarget(to), MayAdmin, "")
}
