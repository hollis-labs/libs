package teams

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hollis-labs/substrate/mesh"
)

// ResolveLimits fills policy from provider defaults; requests may only tighten
// that policy. Zero is unspecified, never unlimited.
func ResolveLimits(request, team, provider mesh.Limits) (mesh.Limits, error) {
	for _, l := range []mesh.Limits{request, team, provider} {
		if err := validatePartialLimits(l); err != nil {
			return mesh.Limits{}, err
		}
	}
	out := provider
	if team.MaxDepth != 0 {
		out.MaxDepth = team.MaxDepth
	}
	if team.MaxChildren != 0 {
		out.MaxChildren = team.MaxChildren
	}
	if team.FanOut != 0 {
		out.FanOut = team.FanOut
	}
	if team.Budget != 0 {
		out.Budget = team.Budget
	}
	if team.Timeout != 0 {
		out.Timeout = team.Timeout
	}
	if team.MaxRounds != 0 {
		out.MaxRounds = team.MaxRounds
	}
	if team.MaxStalls != 0 {
		out.MaxStalls = team.MaxStalls
	}
	if err := out.Validate(); err != nil {
		return mesh.Limits{}, err
	}
	ints := []struct {
		request int
		ceiling *int
	}{{request.MaxDepth, &out.MaxDepth}, {request.MaxChildren, &out.MaxChildren}, {request.FanOut, &out.FanOut}, {request.MaxRounds, &out.MaxRounds}, {request.MaxStalls, &out.MaxStalls}}
	for _, v := range ints {
		if v.request != 0 {
			if *v.ceiling != 0 && v.request > *v.ceiling {
				return mesh.Limits{}, fmt.Errorf("%w: request widens spawn limits", ErrDenied)
			}
			*v.ceiling = v.request
		}
	}
	if request.Budget != 0 {
		if request.Budget > out.Budget {
			return mesh.Limits{}, fmt.Errorf("%w: request widens budget", ErrDenied)
		}
		out.Budget = request.Budget
	}
	if request.Timeout != 0 {
		if request.Timeout > out.Timeout {
			return mesh.Limits{}, fmt.Errorf("%w: request widens timeout", ErrDenied)
		}
		out.Timeout = request.Timeout
	}
	return out, nil
}
func parentCeiling(policy mesh.Limits, parent Member, roster Roster) (mesh.Limits, error) {
	if err := parent.Limits.Validate(); err != nil {
		return mesh.Limits{}, fmt.Errorf("spawn: parent has no bounded ceiling: %w", err)
	}
	out := policy
	out.MaxDepth = min(out.MaxDepth, parent.Limits.MaxDepth)
	out.MaxChildren = min(out.MaxChildren, parent.Limits.MaxChildren)
	out.FanOut = min(out.FanOut, parent.Limits.FanOut)
	remaining := parent.Budget
	for _, child := range roster.Members {
		if child.Parent == parent.ID && child.Status != "failed" {
			remaining -= child.Budget
		}
	}
	out.Budget = min(out.Budget, parent.Limits.Budget, remaining)
	out.Timeout = min(out.Timeout, parent.Limits.Timeout)
	if parent.Limits.MaxRounds > 0 && (out.MaxRounds == 0 || parent.Limits.MaxRounds < out.MaxRounds) {
		out.MaxRounds = parent.Limits.MaxRounds
	}
	if parent.Limits.MaxStalls > 0 && (out.MaxStalls == 0 || parent.Limits.MaxStalls < out.MaxStalls) {
		out.MaxStalls = parent.Limits.MaxStalls
	}
	return out, out.Validate()
}

// CheckSpawn uses every recorded child for lifetime child/budget accounting,
// and only active children for fanout. Parent links never confer capability.
func CheckSpawn(t Team, r Roster, parent Member, target Slot, limits mesh.Limits) error {
	if err := limits.Validate(); err != nil {
		return err
	}
	if !parent.SpawnCapable {
		return ErrDenied
	}
	ceiling, err := ResolveLimits(mesh.Limits{}, t.Policy.Spawn, parent.Limits)
	if err != nil {
		return err
	}
	ceiling, err = parentCeiling(ceiling, parent, r)
	if err != nil {
		return err
	}
	if _, err = ResolveLimits(limits, ceiling, ceiling); err != nil {
		return err
	}
	prospective := Member{ID: "prospective", Slot: target.Name, Actor: "msg://agent/prospective/new", Status: "active"}
	if err := Authorize(t, parent, prospective, MaySpawn, ""); err != nil {
		return err
	}
	byID := map[string]Member{}
	slotCount := 0
	for _, m := range r.Members {
		byID[m.ID] = m
		if m.Slot == target.Name && holdsQuota(m) {
			slotCount++
		}
	}
	if slotCount >= target.Max {
		return fmt.Errorf("spawn: slot maximum reached")
	}
	depth := 1
	at := parent
	seen := map[string]bool{}
	for {
		if seen[at.ID] {
			return fmt.Errorf("spawn: parent cycle")
		}
		seen[at.ID] = true
		if at.Parent == "" {
			break
		}
		var ok bool
		at, ok = byID[at.Parent]
		if !ok {
			return fmt.Errorf("spawn: missing ancestor")
		}
		depth++
	}
	children, active := 0, 0
	spent := 0.0
	for _, m := range r.Members {
		if m.Parent == parent.ID && m.Status != "failed" {
			children++
			spent += m.Budget
			if holdsQuota(m) {
				active++
			}
		}
	}
	if depth > ceiling.MaxDepth || children >= ceiling.MaxChildren || active >= ceiling.FanOut || spent+limits.Budget > parent.Budget {
		return fmt.Errorf("spawn: depth, children, fanout or budget exhausted")
	}
	return nil
}

// Spawn commits a quota reservation and immutable intent before provisioning.
// Trust and approval callbacks run outside the roster transaction. The atomic
// reservation rechecks authority, parent ceilings and quota after callbacks.
func Spawn(ctx context.Context, t Team, store RosterStore, p MemberProvisioner, trust TrustResolver, approvals ApprovalEmitter, runID string, parentActor mesh.URN, target string, key string, request, defaults mesh.Limits) (Member, error) {
	if key == "" || store == nil || p == nil || trust == nil {
		return Member{}, fmt.Errorf("spawn: incomplete host or key")
	}
	slot, ok := t.slot(target)
	if !ok {
		return Member{}, ErrNotFound
	}
	policy, err := ResolveLimits(mesh.Limits{}, t.Policy.Spawn, defaults)
	if err != nil {
		return Member{}, err
	}
	r, err := store.Snapshot(ctx, runID)
	if err != nil {
		return Member{}, err
	}
	parent, err := r.member(parentActor)
	if err != nil {
		return Member{}, err
	}
	encoded, _ := json.Marshal(struct {
		Parent  mesh.URN
		Slot    Slot
		Request mesh.Limits
	}{parentActor, slot, request})
	digest := fmt.Sprintf("%x", sha256.Sum256(encoded))
	id := stableID(runID, "spawn", key)
	var result Member
	replay := func(r Roster) (bool, error) {
		for _, m := range r.Members {
			if m.ID == id {
				if m.ProvisionDigest != digest {
					return true, ErrConflict
				}
				if m.Status != "active" && m.Status != "provisioning" {
					return true, fmt.Errorf("%w: spawn intent is %s", ErrConflict, m.Status)
				}
				result = clone(m)
				return true, nil
			}
		}
		return false, nil
	}
	found, err := replay(r)
	if err != nil {
		return Member{}, err
	}
	if !found {
		ceiling, err := parentCeiling(policy, parent, r)
		if err != nil {
			return Member{}, err
		}
		limits, err := ResolveLimits(request, ceiling, ceiling)
		if err != nil {
			return Member{}, err
		}
		if err = CheckSpawn(t, r, parent, slot, limits); err != nil {
			return Member{}, err
		}
		decision, err := trust.ResolveTrust(ctx, parent, slot)
		if err != nil {
			return Member{}, err
		}
		if decision == TrustApproval {
			if approvals == nil {
				return Member{}, ErrDenied
			}
			if err = approvals.EmitApproval(ctx, id, parent, slot); err != nil {
				return Member{}, err
			}
			return Member{}, fmt.Errorf("%w: spawn needs approval", ErrDenied)
		}
		if decision != TrustAllow {
			return Member{}, ErrDenied
		}
		err = store.Mutate(ctx, runID, func(r *Roster) error {
			if found, err := replay(*r); found || err != nil {
				return err
			}
			current, err := r.member(parentActor)
			if err != nil {
				return err
			}
			if err = CheckSpawn(t, *r, current, slot, limits); err != nil {
				return err
			}
			identity, err := identityForSlot(slot, *r)
			if err != nil {
				return err
			}
			intent := ProvisionRequest{ReservedIdentities: declaredIdentities(t), Identity: identity, IdempotencyKey: id, RunID: runID, MemberID: id, Slot: clone(slot), Parent: current.ID, Limits: limits}
			result = Member{ID: id, Slot: target, Status: "provisioning", Parent: current.ID, Resolution: slot.Resolution, Budget: limits.Budget, Limits: limits, ProvisionDigest: digest, Intent: &intent}
			r.Members = append(r.Members, clone(result))
			return nil
		})
		if err != nil {
			return Member{}, err
		}
	}
	if result.Status == "active" {
		return result, nil
	}
	return completeProvision(ctx, store, p, runID, result)
}
func completeProvision(ctx context.Context, store RosterStore, p MemberProvisioner, runID string, reserved Member) (Member, error) {
	if reserved.Intent == nil {
		return Member{}, fmt.Errorf("spawn: reservation missing intent")
	}
	m, err := p.Provision(ctx, clone(*reserved.Intent))
	if err != nil {
		if errors.Is(err, ErrProvisionFailed) {
			return Member{}, errors.Join(err, failReservation(ctx, store, p, runID, reserved))
		}
		return Member{}, err
	}
	m.ID = reserved.ID
	m.Slot = reserved.Slot
	m.Governance = MemberRole
	m.Parent = reserved.Parent
	m.Resolution = reserved.Resolution
	m.Budget = reserved.Budget
	m.Limits = reserved.Limits
	m.Intent = clone(reserved.Intent)
	m.ProvisionDigest = reserved.ProvisionDigest
	if err = validateProvisioned(m, *reserved.Intent); err != nil {
		return Member{}, errors.Join(ErrProvisionFailed, err, failReservation(ctx, store, p, runID, reserved))
	}
	err = store.Mutate(ctx, runID, func(r *Roster) error {
		index := -1
		for i, old := range r.Members {
			if old.ID == reserved.ID {
				index = i
				if old.ProvisionDigest != reserved.ProvisionDigest {
					return ErrConflict
				}
				if old.Status == "active" {
					m = clone(old)
					return nil
				}
				if old.Status != "provisioning" {
					return ErrConflict
				}
			}
			if old.ID != reserved.ID && old.Actor == m.Actor && (m.Resolution == Fresh || old.Status == "active") {
				return ErrConflict
			}
		}
		if index < 0 {
			return ErrNotFound
		}
		r.Members[index] = clone(m)
		return nil
	})
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
		cleanupCtx := context.WithoutCancel(ctx)
		cleanupErr := releaseOrStop(cleanupCtx, p, runID, m)
		// A cancellation already owns its terminal state. A conflicting actor
		// projection still in provisioning must also become terminal and free
		// quota after cleanup; otherwise its fenced key can never recover.
		failErr := failReservation(cleanupCtx, store, p, runID, reserved)
		err = errors.Join(err, cleanupErr, failErr)
	}
	return m, err
}

func failReservation(ctx context.Context, store RosterStore, p MemberProvisioner, runID string, reserved Member) error {
	var plan []Member
	err := store.Mutate(ctx, runID, func(r *Roster) error {
		for i, m := range r.Members {
			if m.ID == reserved.ID {
				if m.Status == "provisioning" {
					m.Status = "failing"
					r.Members[i] = clone(m)
					plan = []Member{clone(m)}
				}
				return nil
			}
		}
		return ErrNotFound
	})
	if err != nil {
		return err
	}
	return finishMembers(ctx, store, p, runID, plan)
}

// ReconcileMembers repairs provisioning and termination acknowledgements.
// Only the trusted host invokes recovery; transient failures retain quota.
// ErrProvisionFailed fences the intent and frees quota after cleanup succeeds.
func ReconcileMembers(ctx context.Context, store RosterStore, p MemberProvisioner, runID string) error {
	if store == nil || p == nil {
		return fmt.Errorf("reconcile members: incomplete host")
	}
	r, err := store.Snapshot(ctx, runID)
	if err != nil {
		return err
	}
	var failures []error
	for _, m := range r.Members {
		if m.Status == "provisioning" {
			_, err = completeProvision(ctx, store, p, runID, m)
		} else if terminating(m) {
			err = finishMembers(ctx, store, p, runID, []Member{m})
		} else {
			continue
		}
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// Cascade is a pure post-order plan; hosts stop children before parents.
func Cascade(r Roster, root string, includeRoot bool) ([]Member, error) {
	byID := map[string]Member{}
	for _, m := range r.Members {
		byID[m.ID] = m
	}
	if _, ok := byID[root]; !ok {
		return nil, ErrNotFound
	}
	seen := map[string]bool{}
	var out []Member
	var visit func(string) error
	visit = func(id string) error {
		if seen[id] {
			return fmt.Errorf("cascade: parent cycle")
		}
		seen[id] = true
		for _, m := range r.Members {
			if m.Parent == id {
				if err := visit(m.ID); err != nil {
					return err
				}
			}
		}
		if id != root || includeRoot {
			out = append(out, byID[id])
		}
		return nil
	}
	err := visit(root)
	return out, err
}
func validateMember(m Member) error {
	if m.ID == "" || m.Slot == "" || m.Status != "active" {
		return fmt.Errorf("invalid provisioned member")
	}
	if err := (mesh.Actor{URN: m.Actor, Kind: m.Kind}).Validate(); err != nil {
		return err
	}
	if m.Kind == mesh.ActorAgent && (m.AgentID == "" || m.SessionID == "") {
		return fmt.Errorf("agent member needs identity and session")
	}
	return nil
}

func identityForSlot(slot Slot, r Roster) (mesh.URN, error) {
	if slot.Resolution == Durable {
		return slot.Identity, nil
	}
	if slot.Resolution == Fresh {
		return "", nil
	}
	used := map[mesh.URN]bool{}
	for _, m := range r.Members {
		if holdsQuota(m) {
			if m.Actor != "" {
				used[m.Actor] = true
			}
			if m.Intent != nil {
				used[m.Intent.Identity] = true
			}
		}
	}
	for _, actor := range slot.Identities {
		if !used[actor] {
			return actor, nil
		}
	}
	return "", fmt.Errorf("%w: pool identities exhausted", ErrUnavailable)
}
func validateProvisioned(m Member, req ProvisionRequest) error {
	if err := validateMember(m); err != nil {
		return err
	}
	if !m.Enrolled || m.Ephemeral != (req.Slot.Resolution == Fresh) {
		return fmt.Errorf("%w: invalid enrollment lifetime", ErrProvisionFailed)
	}
	if req.Slot.Resolution == Fresh {
		for _, actor := range req.ReservedIdentities {
			if m.Actor == actor {
				return fmt.Errorf("%w: ephemeral identity is reserved", ErrProvisionFailed)
			}
		}
	}
	if req.Identity != "" && m.Actor != req.Identity {
		return fmt.Errorf("%w: provisioned identity differs from enrolled reference", ErrProvisionFailed)
	}
	return nil
}

func declaredIdentities(t Team) []mesh.URN {
	var out []mesh.URN
	for _, s := range t.Slots {
		if s.Identity != "" {
			out = append(out, s.Identity)
		}
		out = append(out, s.Identities...)
	}
	return out
}
