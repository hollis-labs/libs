package teams

import (
	"context"
	"fmt"

	"github.com/hollis-labs/substrate/mesh"
)

func liveMember(m Member) bool { return m.Status == "active" || m.Status == "provisioning" }
func holdsQuota(m Member) bool { return liveMember(m) || terminating(m) }
func terminating(m Member) bool {
	return m.Status == "stopping" || m.Status == "releasing" || m.Status == "failing"
}
func terminationState(m Member) string {
	if m.Resolution == Fresh {
		return "stopping"
	}
	return "releasing"
}

// RemoveMember reserves a non-routable termination before external calls.
// Failed acknowledgements are repaired through ReconcileMembers.
func RemoveMember(ctx context.Context, t Team, store RosterStore, p MemberProvisioner, runID string, actor mesh.URN, memberID string) error {
	if store == nil || p == nil {
		return fmt.Errorf("members: incomplete host")
	}
	var plan []Member
	err := store.Mutate(ctx, runID, func(r *Roster) error {
		from, err := r.member(actor)
		if err != nil {
			return err
		}
		index := -1
		for i, m := range r.Members {
			if m.ID == memberID {
				index = i
				break
			}
		}
		if index < 0 {
			return ErrNotFound
		}
		target := r.Members[index]
		if !liveMember(target) && !terminating(target) {
			return nil
		}
		if err = authorizeAdmin(t, from, authorizationTarget(target)); err != nil {
			return err
		}
		if liveMember(target) {
			slot, ok := t.slot(target.Slot)
			if !ok {
				return ErrNotFound
			}
			count := 0
			for _, m := range r.Members {
				if m.Slot == slot.Name && liveMember(m) {
					count++
				}
			}
			if count-1 < slot.Min {
				return fmt.Errorf("remove: slot minimum would be violated")
			}
			target.Status = terminationState(target)
			r.Members[index] = target
		}
		plan = []Member{clone(target)}
		return nil
	})
	if err != nil {
		return err
	}
	return finishMembers(ctx, store, p, runID, plan)
}

// CancelMembers authorizes the full post-order plan before reserving it.
// Durable/pool sessions are released, retaining their stable identities.
func CancelMembers(ctx context.Context, t Team, store RosterStore, p MemberProvisioner, runID string, actor mesh.URN, root string, cascade bool) error {
	if store == nil || p == nil {
		return fmt.Errorf("members: incomplete host")
	}
	var plan []Member
	err := store.Mutate(ctx, runID, func(r *Roster) error {
		from, err := r.member(actor)
		if err != nil {
			return err
		}
		targets, err := Cascade(*r, root, true)
		if err != nil {
			return err
		}
		if !cascade {
			targets = targets[len(targets)-1:]
		}
		for _, m := range targets {
			if liveMember(m) || terminating(m) {
				if err = authorizeAdmin(t, from, authorizationTarget(m)); err != nil {
					return err
				}
			}
		}
		for _, m := range targets {
			if !liveMember(m) && !terminating(m) {
				continue
			}
			if liveMember(m) {
				m.Status = terminationState(m)
			}
			for i := range r.Members {
				if r.Members[i].ID == m.ID {
					r.Members[i] = clone(m)
				}
			}
			plan = append(plan, clone(m))
		}
		return nil
	})
	if err != nil {
		return err
	}
	return finishMembers(ctx, store, p, runID, plan)
}
func authorizationTarget(m Member) Member {
	if m.Actor == "" {
		m.Actor = "msg://agent/prospective/new"
	}
	m.Status = "active"
	return m
}
func endMember(ctx context.Context, p MemberProvisioner, runID string, m Member) error {
	key := stableID(runID, "termination", m.ID)
	if m.Resolution == Fresh {
		return p.Retire(ctx, key, m)
	}
	return p.Release(ctx, key, m)
}
func finishMembers(ctx context.Context, store RosterStore, p MemberProvisioner, runID string, plan []Member) error {
	for _, m := range plan {
		if err := endMember(ctx, p, runID, m); err != nil {
			return err
		}
		err := store.Mutate(ctx, runID, func(r *Roster) error {
			for i, old := range r.Members {
				if old.ID == m.ID {
					if old.Status != m.Status {
						if !liveMember(old) && !terminating(old) {
							return nil
						}
						return ErrConflict
					}
					status := "stopped"
					if m.Status == "failing" {
						status = "failed"
					} else if m.Resolution != Fresh {
						status = "released"
					}
					r.Members[i].Status = status
					return nil
				}
			}
			return ErrNotFound
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// EndRun is invoked by the trusted workflow host. All membership becomes
// non-routable before stopping/releasing, so recovery cannot start new work.
func EndRun(ctx context.Context, runID string, store RosterStore, p MemberProvisioner, routing RoutingInstaller) error {
	if store == nil || p == nil || routing == nil {
		return fmt.Errorf("end run: incomplete host")
	}
	var plan []Member
	err := store.Mutate(ctx, runID, func(r *Roster) error {
		for i, m := range r.Members {
			if !liveMember(m) && !terminating(m) {
				continue
			}
			if liveMember(m) {
				m.Status = terminationState(m)
				r.Members[i] = clone(m)
			}
			plan = append(plan, clone(m))
		}
		return nil
	})
	if err != nil {
		return err
	}
	plan, err = childrenFirst(plan)
	if err != nil {
		return err
	}
	if err = finishMembers(ctx, store, p, runID, plan); err != nil {
		return err
	}
	return routing.RemoveRouting(ctx, runID)
}

// childrenFirst orders a forest, including descendants whose parents have
// already finished. Cycles fail before any external cleanup call.
func childrenFirst(members []Member) ([]Member, error) {
	state := map[string]int{}
	var out []Member
	var visit func(Member) error
	visit = func(m Member) error {
		if state[m.ID] == 1 {
			return fmt.Errorf("termination: parent cycle")
		}
		if state[m.ID] == 2 {
			return nil
		}
		state[m.ID] = 1
		for _, child := range members {
			if child.Parent == m.ID {
				if err := visit(child); err != nil {
					return err
				}
			}
		}
		state[m.ID] = 2
		out = append(out, m)
		return nil
	}
	for _, m := range members {
		if err := visit(m); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// reconcileTerminations skips only ancestors of failed descendants, allowing
// unrelated branches and later provisioning to make progress on every pass.
func reconcileTerminations(ctx context.Context, store RosterStore, p MemberProvisioner, runID string, plan []Member) []error {
	state := map[string]int{}
	recovered := map[string]bool{}
	var failures []error
	var visit func(Member) bool
	visit = func(m Member) bool {
		if state[m.ID] == 1 {
			failures = append(failures, fmt.Errorf("termination: parent cycle at %s", m.ID))
			return false
		}
		if state[m.ID] == 2 {
			return recovered[m.ID]
		}
		state[m.ID] = 1
		ready := true
		for _, child := range plan {
			if child.Parent == m.ID && !visit(child) {
				ready = false
			}
		}
		if ready {
			if err := finishMembers(ctx, store, p, runID, []Member{m}); err != nil {
				failures = append(failures, err)
				ready = false
			}
		}
		state[m.ID] = 2
		recovered[m.ID] = ready
		return ready
	}
	for _, m := range plan {
		visit(m)
	}
	return failures
}
