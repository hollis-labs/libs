package teams

import (
	"fmt"
	"math"
	"strings"

	"github.com/hollis-labs/substrate/mesh"
)

func validPermission(p Permission) bool {
	switch p {
	case MaySpawn, MayMessage, MayDelegate, MayHandoff, MayAssign, MayApprove, MaySignalPhase, MayAdmin, MayNotReview:
		return true
	}
	return false
}
func validName(s string) bool {
	return strings.TrimSpace(s) == s && s != "" && s != Self && s != "all" && !strings.ContainsAny(s, "@* /:\\")
}
func Validate(t Team) error {
	bad := func(s string) error { return fmt.Errorf("team %q: %s", t.Name, s) }
	if t.ID == "" || strings.TrimSpace(t.Name) == "" || t.Version == 0 {
		return bad("id, name and version are required")
	}
	if t.Authority.Mode != "" && t.Authority.Mode != Strict && t.Authority.Mode != DevOpen {
		return bad("invalid authority mode")
	}
	if len(t.Slots) == 0 {
		return bad("slots are required")
	}
	names := map[string]bool{}
	identities := map[mesh.URN]bool{}
	for _, s := range t.Slots {
		if !validName(s.Name) || names[s.Name] {
			return bad("invalid or duplicate slot " + s.Name)
		}
		names[s.Name] = true
		if s.Min < 0 || s.Max < 1 || s.Min > s.Max || (s.Required && s.Min < 1) {
			return bad("invalid min/max/required for " + s.Name)
		}
		switch s.Resolution {
		case Durable:
			if s.Identity == "" || (mesh.Actor{URN: s.Identity, Kind: mesh.ActorAgent}).Validate() != nil || s.Max != 1 || identities[s.Identity] {
				return bad("durable requires a unique agent identity and max=1")
			}
			identities[s.Identity] = true
		case Pool:
			if s.Identity != "" || s.Pool == "" || len(s.Identities) < s.Max {
				return bad("pool requires a pool name and distinct enrolled identity references for every member")
			}
		case Fresh:
			if s.Identity != "" || s.Pool != "" || s.Definition.ID == "" || s.Definition.Revision == "" {
				return bad("fresh requires a pinned definition reference")
			}
		default:
			return bad("invalid resolution")
		}
		switch s.Activation {
		case Singleton, FreshPerWake:
			if s.Max != 1 {
				return bad("non-concurrent slot requires max=1")
			}
		case Concurrent:
			if s.Resolution != Pool {
				return bad("concurrent activation requires a worker pool of distinct enrolled identities")
			}
		default:
			return bad("invalid activation")
		}
		if s.Definition.ID != "" || s.Definition.Revision != "" || s.Definition.Digest != "" {
			if strings.TrimSpace(s.Definition.ID) == "" || strings.TrimSpace(s.Definition.Revision) == "" {
				return bad("definition reference requires id and pinned revision")
			}
		}
		if s.Resolution != Pool && len(s.Identities) != 0 {
			return bad("identity sets belong to worker pools")
		}
		for _, actor := range s.Identities {
			if (mesh.Actor{URN: actor, Kind: mesh.ActorAgent}).Validate() != nil || identities[actor] {
				return bad("pool identities must be distinct enrolled agent references across slots")
			}
			identities[actor] = true
		}
		switch s.Dispatch {
		case "", IdleFirst, RoundRobin, First:
		default:
			return bad("invalid dispatch")
		}
	}
	if len(t.Phases) != 1 {
		return fmt.Errorf("%w: exactly one flex phase is supported", ErrUnsupported)
	}
	p := t.Phases[0]
	if p.Kind != "flex" {
		return fmt.Errorf("%w: gate execution is deferred", ErrUnsupported)
	}
	if p.ID == "" || len(p.ActiveSlots) == 0 || p.ExitTrigger.Kind == "" || len(p.ExitTrigger.Spec) == 0 {
		return bad("flex requires id, active slots and exit trigger")
	}
	seen := map[string]bool{}
	for _, s := range p.ActiveSlots {
		if !names[s] || seen[s] {
			return bad("unknown or duplicate active slot " + s)
		}
		seen[s] = true
	}
	for _, s := range []string{p.OwnerSlot, p.ExitTrigger.AuthorizedSlot, p.ApproverSlot, t.Routing.CoordinatorSlot} {
		if s != "" && !names[s] {
			return bad("unknown slot reference " + s)
		}
	}
	if !p.ExitTrigger.Auto && p.OwnerSlot == "" && t.Routing.CoordinatorSlot == "" && p.ExitTrigger.AuthorizedSlot == "" {
		return bad("phase needs owner/coordinator/authorized slot or auto")
	}
	for _, g := range t.Authority.Grants {
		if !validPermission(g.Verb) || (g.FromSlot == "") == (g.FromActor == "") || (g.ToSlot == "") == (g.ToActor == "") {
			return bad("invalid authority grant")
		}
		if g.FromSlot != "" && !names[g.FromSlot] {
			return bad("unknown grant source")
		}
		if g.ToSlot != "" && g.ToSlot != Self && !names[g.ToSlot] {
			return bad("unknown grant target")
		}
	}
	rules := map[string]bool{}
	for _, r := range t.Routing.Rules {
		if r.Name == "" || rules[r.Name] || !names[r.TargetSlot] || len(r.Phrases) == 0 {
			return bad("invalid routing rule")
		}
		rules[r.Name] = true
		for _, phrase := range r.Phrases {
			if strings.TrimSpace(phrase) == "" {
				return bad("empty routing phrase")
			}
		}
	}
	if err := validatePartialLimits(t.Policy.Spawn); err != nil {
		return err
	}
	switch string(t.Policy.History) {
	case "", "full", "summary", "filtered", "none":
	default:
		return bad("invalid history policy")
	}
	return nil
}
func validatePartialLimits(l mesh.Limits) error {
	if l.MaxDepth < 0 || l.MaxChildren < 0 || l.FanOut < 0 || l.Budget < 0 || math.IsNaN(l.Budget) || math.IsInf(l.Budget, 0) || l.Timeout < 0 || l.MaxRounds < 0 || l.MaxStalls < 0 {
		return fmt.Errorf("spawn: invalid limit")
	}
	return nil
}

// CompileTeam is pure. It retains slot names so member churn doesn't require
// recompilation. The host translates this value into its own workflow AST.
func CompileTeam(t Team, phases []Phase) (WorkflowDefinition, error) {
	t.Phases = phases
	if err := Validate(t); err != nil {
		return WorkflowDefinition{}, err
	}
	p := clone(phases[0])
	return WorkflowDefinition{Name: t.Name, TeamID: t.ID, TeamVersion: t.Version, Steps: []WorkflowStep{{ID: p.ID, Kind: p.Kind, ActiveSlots: p.ActiveSlots, ExitTrigger: p.ExitTrigger, OwnerSlot: p.OwnerSlot, ApproverSlot: p.ApproverSlot}}}, nil
}

type WorkflowDefinition struct {
	Name        string
	TeamID      string
	TeamVersion uint64
	Steps       []WorkflowStep
}
type WorkflowStep struct {
	ID, Kind                string
	ActiveSlots             []string
	ExitTrigger             Trigger
	OwnerSlot, ApproverSlot string
}
