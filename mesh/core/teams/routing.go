package teams

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/hollis-labs/substrate/mesh"
)

type AddressRequest struct {
	RunID   string
	Actor   mesh.URN // Authenticated by host.
	Address string
	Body    string
	Verb    mesh.Verb
	Kind    mesh.ActorKind // Address filter only.
}
type Route struct {
	TeamID        string
	TeamVersion   uint64
	Sender        Member
	Verb          mesh.Verb
	RunID         string
	RosterVersion uint64
	Address       string
	Rule          string
	Recipients    []Member
}
type Delivery struct {
	IdempotencyKey string
	From           mesh.URN
	Recipient      Member
	Body           string
	Verb           mesh.Verb
	History        mesh.HistoryPolicy
	Route          Route // Message -> rule -> snapshotted member provenance.
}

// Router resolves addresses; MessageSender is the actual transport. Lazy
// uses the library's bounded, authorized Spawn path.
// It runs once only for a never-resolved slot; stopped slots fail loudly.
type LazyProvisioning struct {
	Provisioner MemberProvisioner
	Trust       TrustResolver
	Approvals   ApprovalEmitter
	Defaults    mesh.Limits
	Limits      mesh.Limits
}
type Router struct {
	Roster  RosterStore
	Sender  MessageSender
	Lazy    *LazyProvisioning
	mu      sync.Mutex
	cursors map[string]uint64
}

func permissionFor(verb mesh.Verb) (Permission, error) {
	switch verb {
	case mesh.MessageAddress, mesh.MessageSend, mesh.MessageBroadcast, mesh.Reply:
		return MayMessage, nil
	case mesh.Assign:
		return MayAssign, nil
	case mesh.Delegate:
		return MayDelegate, nil
	case mesh.Handoff:
		return MayHandoff, nil
	default:
		return "", ErrUnsupported
	}
}
func (router *Router) Resolve(ctx context.Context, t Team, req AddressRequest) (Route, error) {
	if router.Roster == nil || req.RunID == "" {
		return Route{}, fmt.Errorf("routing: incomplete host/request")
	}
	if err := Validate(t); err != nil {
		return Route{}, err
	}
	permission, err := permissionFor(req.Verb)
	if err != nil {
		return Route{}, err
	}
	roster, err := router.Roster.Snapshot(ctx, req.RunID)
	if err != nil {
		return Route{}, err
	}
	from, err := roster.member(req.Actor)
	if err != nil {
		return Route{}, err
	}
	address, rule, err := routingAddress(t, req)
	if err != nil {
		return Route{}, err
	}
	slotName := strings.TrimSuffix(strings.TrimPrefix(address, "@"), "*")
	slot, isSlot := t.slot(slotName)
	isSlot = isSlot && strings.HasPrefix(address, "@")
	all := address == "@all" || strings.HasSuffix(address, "*")
	work := req.Verb == mesh.Assign || req.Verb == mesh.Delegate || req.Verb == mesh.Handoff
	if work && all {
		return Route{}, fmt.Errorf("routing: work verbs require one recipient")
	}
	matches := func(r Roster) []Member {
		var out []Member
		for _, m := range r.Members {
			if m.Status != "active" || (req.Kind != "" && req.Kind != m.Kind) {
				continue
			}
			if address == "@all" || (isSlot && m.Slot == slotName) || address == "@"+m.ID || address == string(m.Actor) {
				out = append(out, m)
			}
		}
		return out
	}
	if req.Kind != "" && req.Kind != mesh.ActorAgent && req.Kind != mesh.ActorUser && req.Kind != mesh.ActorService {
		return Route{}, fmt.Errorf("routing: invalid actor filter")
	}
	recipients := matches(roster)
	if len(recipients) == 0 && isSlot && router.Lazy != nil {
		previouslyResolved := false
		for _, m := range roster.Members {
			if m.Slot == slotName {
				previouslyResolved = true
			}
		}
		if !previouslyResolved {
			// Reject unauthorized addressing before any lazy provisioning side effect.
			prospective := Member{Slot: slotName, Actor: "msg://agent/prospective/new", Status: "active"}
			if err := Authorize(t, from, prospective, permission, ""); err != nil {
				return Route{}, err
			}
			lazy := router.Lazy
			if _, err := Spawn(ctx, t, router.Roster, lazy.Provisioner, lazy.Trust, lazy.Approvals, req.RunID, req.Actor, slotName, "lazy/"+slotName, lazy.Limits, lazy.Defaults); err != nil {
				return Route{}, fmt.Errorf("%w: lazy %s: %w", ErrUnavailable, slotName, err)
			}
			roster, err = router.Roster.Snapshot(ctx, req.RunID)
			if err != nil {
				return Route{}, err
			}
			from, err = roster.member(req.Actor)
			if err != nil {
				return Route{}, err
			}
			recipients = matches(roster)
		}
	}
	if len(recipients) == 0 {
		return Route{}, fmt.Errorf("%w: %s", ErrUnavailable, address)
	}
	sort.Slice(recipients, func(i, j int) bool { return recipients[i].ID < recipients[j].ID })
	if !all {
		candidates := recipients
		if isSlot && (slot.Dispatch == "" || slot.Dispatch == IdleFirst) {
			var idle []Member
			for _, m := range candidates {
				if m.Idle {
					idle = append(idle, m)
				}
			}
			if len(idle) > 0 {
				candidates = idle
			}
		}
		index := 0
		if isSlot && slot.Dispatch != First {
			router.mu.Lock()
			if router.cursors == nil {
				router.cursors = map[string]uint64{}
			}
			key := req.RunID + "\x00" + slotName
			index = int(router.cursors[key] % uint64(len(candidates)))
			router.cursors[key]++
			router.mu.Unlock()
		}
		recipients = []Member{candidates[index]}
	}
	// Authorize the complete fanout before sending anything.
	for _, to := range recipients {
		if err := Authorize(t, from, to, permission, ""); err != nil {
			return Route{}, err
		}
	}
	return Route{TeamID: t.ID, TeamVersion: t.Version, Sender: clone(from), Verb: req.Verb, RunID: req.RunID, RosterVersion: roster.Version, Address: address, Rule: rule, Recipients: clone(recipients)}, nil
}

// Send resolves once and retains the same roster snapshot across fanout.
// Partial failure is returned with provenance; unavailable members never
// silently fall back to the coordinator. Retry keys are per recipient.
func (router *Router) Send(ctx context.Context, t Team, req AddressRequest, key string) (Route, error) {
	if router.Sender == nil || key == "" || req.Body == "" {
		return Route{}, fmt.Errorf("routing: sender, body and key required")
	}
	route, err := router.Resolve(ctx, t, req)
	if err != nil {
		return route, err
	}
	return route, router.SendResolved(ctx, t, req, route, key)
}

// SendResolved retries a host-retained plan without selecting new recipients.
// The host must retain the original trusted request and plan together.
func (router *Router) SendResolved(ctx context.Context, t Team, req AddressRequest, route Route, key string) error {
	if router.Roster == nil || router.Sender == nil || key == "" || req.Body == "" || req.Actor == "" || route.RunID != req.RunID || route.RosterVersion == 0 || len(route.Recipients) == 0 {
		return fmt.Errorf("routing: invalid retained delivery")
	}
	permission, err := permissionFor(req.Verb)
	if err != nil {
		return err
	}
	if route.TeamID != t.ID || route.TeamVersion != t.Version || route.Sender.Actor != req.Actor || route.Verb != req.Verb {
		return ErrConflict
	}
	snapshot, err := router.Roster.SnapshotAt(ctx, route.RunID, route.RosterVersion)
	if err != nil {
		return err
	}
	from, err := snapshot.member(req.Actor)
	if err != nil {
		return err
	}

	address, rule, err := routingAddress(t, req)
	if err != nil {
		return err
	}
	if route.Address != address || route.Rule != rule {
		return ErrConflict
	}
	slotName := strings.TrimSuffix(strings.TrimPrefix(address, "@"), "*")
	_, isSlot := t.slot(slotName)
	isSlot = isSlot && strings.HasPrefix(address, "@")
	all := address == "@all" || strings.HasSuffix(address, "*")
	var expected []Member
	for _, m := range snapshot.Members {
		if m.Status != "active" || (req.Kind != "" && m.Kind != req.Kind) {
			continue
		}
		if address == "@all" || (isSlot && m.Slot == slotName) || address == "@"+m.ID || address == string(m.Actor) {
			expected = append(expected, m)
		}
	}
	if all && len(expected) != len(route.Recipients) {
		return ErrConflict
	}

	recipients := make([]Member, 0, len(route.Recipients))
	seen := map[string]bool{}
	for _, claimed := range route.Recipients {
		actual, err := snapshot.member(claimed.Actor)
		if err != nil {
			return err
		}
		if actual.ID != claimed.ID || seen[actual.ID] {
			return ErrConflict
		}
		seen[actual.ID] = true
		if err = Authorize(t, from, actual, permission, ""); err != nil {
			return err
		}
		matches := false
		for _, m := range expected {
			if m.ID == actual.ID {
				matches = true
			}
		}
		if !matches {
			return ErrConflict
		}
		if !reflect.DeepEqual(actual, claimed) {
			return ErrConflict
		}
		recipients = append(recipients, actual)
	}
	if !reflect.DeepEqual(from, route.Sender) {
		return ErrConflict
	}
	if (req.Verb == mesh.Assign || req.Verb == mesh.Delegate || req.Verb == mesh.Handoff) && len(recipients) != 1 {
		return fmt.Errorf("routing: work requires one recipient")
	}
	// Bind the key to content and the actual immutable roster plan before
	// transport delivery. Even a key-only transport cannot mask changed input.
	encoded, err := json.Marshal(struct {
		Request AddressRequest
		Route   Route
		History mesh.HistoryPolicy
	}{req, route, t.Policy.History.Effective()})
	if err != nil {
		return err
	}
	if err = router.Sender.BindMessage(ctx, stableID(req.RunID, "send", key), stableID(string(encoded))); err != nil {
		return err
	}
	var failures []error
	for _, m := range recipients {
		err := router.Sender.SendMessage(ctx, Delivery{IdempotencyKey: stableID(req.RunID, key, m.ID), From: from.Actor, Recipient: clone(m), Body: req.Body, Verb: req.Verb, History: t.Policy.History.Effective(), Route: clone(route)})
		if err != nil {
			failures = append(failures, fmt.Errorf("recipient %s: %w", m.ID, err))
		}
	}
	return errors.Join(failures...)
}

func routingAddress(t Team, req AddressRequest) (string, string, error) {
	address, rule := req.Address, "explicit"
	if address == "" {
		rules := append([]RoutingRule(nil), t.Routing.Rules...)
		sort.SliceStable(rules, func(i, j int) bool { return rules[i].Priority > rules[j].Priority })
		for _, r := range rules {
			for _, phrase := range r.Phrases {
				if strings.Contains(strings.ToLower(req.Body), strings.ToLower(phrase)) {
					address = "@" + r.TargetSlot
					rule = r.Name
					break
				}
			}
			if address != "" {
				break
			}
		}
		if address == "" {
			if t.Routing.CoordinatorSlot == "" {
				return "", "", ErrUnavailable
			}
			address = "@" + t.Routing.CoordinatorSlot
			rule = "coordinator"
		}
	}

	return address, rule, nil
}
