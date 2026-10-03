package integration

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/hollis-labs/substrate/mesh"
	"github.com/hollis-labs/substrate/mesh/fake"
	"github.com/hollis-labs/substrate/mesh/teams"
	"github.com/hollis-labs/substrate/mesh/teams/memory"
)

// The adapter preserves the router's resolved recipients. Re-addressing here
// would select twice and lose the authority-checked roster snapshot.
type providerHost struct {
	*memory.Host
	provider   mesh.Provider
	actor      mesh.Actor
	members    map[string]teams.Member
	deliveries []mesh.Response
}

func (h *providerHost) Provision(ctx context.Context, req teams.ProvisionRequest) (teams.Member, error) {
	parent := mesh.URN("")
	if req.Parent != "" {
		m, ok := h.members[req.Parent]
		if !ok {
			return teams.Member{}, teams.ErrNotFound
		}
		parent = m.Actor
	}
	m, err := h.Host.Provision(ctx, req)
	if err != nil {
		return teams.Member{}, err
	}
	target := m.Actor
	out, err := h.provider.Invoke(ctx, mesh.Request{Verb: mesh.AgentLaunch, Actor: h.actor, Target: target, Parent: parent, Limits: req.Limits, IdempotencyKey: req.IdempotencyKey})
	if err != nil {
		return teams.Member{}, err
	}
	m.SessionID = string(out.Instance.SessionURN)
	m.Limits = req.Limits
	m.Idle = req.Slot.Name != "workers" || req.Identity == "msg://agent/integration/worker-a"
	h.members[m.ID] = m
	return m, nil
}
func (h *providerHost) Stop(ctx context.Context, key string, m teams.Member) error {
	_, err := h.provider.Invoke(ctx, mesh.Request{Verb: mesh.Cancel, Actor: h.actor, Target: m.Actor, IdempotencyKey: key})
	return err
}
func (h *providerHost) SendMessage(ctx context.Context, d teams.Delivery) error {
	body, err := json.Marshal(d.Body)
	if err != nil {
		return err
	}
	out, err := h.provider.Invoke(ctx, mesh.Request{Verb: mesh.MessageSend, Actor: mesh.Actor{URN: d.From, Kind: mesh.ActorAgent}, Target: d.Recipient.Actor, Body: body, IdempotencyKey: d.IdempotencyKey})
	if err == nil {
		h.deliveries = append(h.deliveries, out)
	}
	return err
}
func invoke(t *testing.T, p mesh.Provider, r mesh.Request) mesh.Response {
	t.Helper()
	out, err := p.Invoke(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range out.Events {
		if err := event.Validate(); err != nil {
			t.Fatal(err)
		}
		if event.Actor != r.Actor {
			t.Fatal("event lost authenticated actor")
		}
	}
	return out
}
func recipientURNs(route teams.Route) []mesh.URN {
	out := make([]mesh.URN, 0, len(route.Recipients))
	for _, m := range route.Recipients {
		out = append(out, m.Actor)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
func TestTeamsProviderComposition(t *testing.T) {
	ctx := context.Background()
	p := fake.New()
	actor := mesh.Actor{URN: "msg://service/integration/host", Kind: mesh.ActorService}
	limits := mesh.Limits{MaxDepth: 3, MaxChildren: 8, FanOut: 8, Budget: 90, Timeout: time.Minute}
	pin := mesh.DefinitionRef{ID: "example-worker", Revision: "r1"}
	pool := []mesh.URN{"msg://agent/integration/worker-a", "msg://agent/integration/worker-b", "msg://agent/integration/worker-c", "msg://agent/integration/worker-d"}
	definition := teams.Team{ID: "example", Name: "Example team", Version: 1,
		Slots:     []teams.Slot{{Name: "owner", Role: "coordinator", Definition: pin, Resolution: teams.Fresh, Activation: teams.Singleton, Min: 1, Max: 1}, {Name: "workers", Role: "engineer", Definition: pin, Resolution: teams.Pool, Pool: "workers", Identities: pool, Activation: teams.Concurrent, Min: 2, Max: 4, Dispatch: teams.IdleFirst}},
		Phases:    []teams.Phase{{ID: "work", Kind: "flex", ActiveSlots: []string{"owner", "workers"}, OwnerSlot: "owner", ExitTrigger: teams.Trigger{Kind: "event", Spec: map[string]string{"event": "ready"}}}},
		Authority: teams.Authority{Mode: teams.Strict, Grants: []teams.Grant{{FromSlot: "owner", Verb: teams.MayMessage, ToSlot: "workers"}, {FromSlot: "owner", Verb: teams.MaySpawn, ToSlot: "workers"}}}, Policy: teams.Policy{Spawn: limits}}
	h := &providerHost{Host: memory.New(), provider: p, actor: actor, members: map[string]teams.Member{}}
	h.SpawnCapabilities["owner"] = true
	for _, identity := range pool {
		if err := h.EnrollIdentity(ctx, identity); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.PutDefinition(ctx, definition); err != nil {
		t.Fatal(err)
	}
	launcher := teams.Launcher{Definitions: h, Roster: h, Ledger: h, Provisioner: h, Workflows: h, Routing: h, Clock: h, IDs: h, Defaults: limits}
	run, err := launcher.Launch(ctx, teams.LaunchRequest{Key: "example-launch", TeamID: definition.ID, Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	roster, err := h.Snapshot(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	teamURN := mesh.URN("msg://team/integration/" + run.ID)
	invoke(t, p, mesh.Request{Verb: mesh.TeamForm, Actor: actor, Target: teamURN})
	var owner, worker teams.Member
	for _, m := range roster.Members {
		// Preserve the functional slot separately from its descriptive role.
		role := definition.Slots[0].Role
		if m.Slot == "workers" {
			role = definition.Slots[1].Role
		}
		invoke(t, p, mesh.Request{Verb: mesh.MemberAdd, Actor: actor, Team: teamURN, Target: m.Actor, Slot: m.Slot, Role: role})
		if !m.Enrolled || m.Ephemeral != (m.Slot == "owner") || !h.Enrolled(m.Actor) {
			t.Fatal("provisioning lost enrollment lifetime")
		}
		if m.Slot == "owner" {
			owner = m
		} else {
			worker = m
		}
		a := invoke(t, p, mesh.Request{Verb: mesh.AgentStatus, Actor: actor, Target: m.Actor}).Instance
		if m.SessionID != string(a.SessionURN) || a.SessionURN == a.URN || a.SessionURN.Validate() != nil {
			t.Fatal("provisioning lost provider session identity")
		}
		want := limits
		want.Budget /= float64(len(roster.Members))
		if a.Limits != want {
			t.Fatalf("provision limits differ: %+v / %+v", a.Limits, want)
		}
	}
	router := teams.Router{Roster: h, Sender: h}
	t.Run("address_comparison", func(t *testing.T) {
		for _, address := range []string{"@workers", "@workers*", string(worker.Actor)} {
			req := teams.AddressRequest{RunID: run.ID, Actor: owner.Actor, Address: address, Verb: mesh.MessageAddress, Body: "hello"}
			route, err := router.Resolve(ctx, definition, req)
			if err != nil {
				t.Fatal(err)
			}
			message := invoke(t, p, mesh.Request{Verb: mesh.MessageAddress, Actor: mesh.Actor{URN: owner.Actor, Kind: owner.Kind}, Team: teamURN, Address: address}).Message
			if !reflect.DeepEqual(recipientURNs(route), message.Recipients) {
				t.Fatalf("%s recipients disagree", address)
			}
			if message.RosterVersion == route.RosterVersion {
				t.Fatal("expected independent roster counters to differ")
			}
			t.Logf("roster-version mismatch: teams=%d, provider=%d", route.RosterVersion, message.RosterVersion)
			before := len(h.deliveries)
			if err := router.SendResolved(ctx, definition, req, route, "send-"+address); err != nil {
				t.Fatal(err)
			}
			got := []mesh.URN{}
			for _, delivery := range h.deliveries[before:] {
				got = append(got, delivery.Message.Recipients...)
				for _, event := range delivery.Events {
					if event.Actor.URN != owner.Actor || event.Actor.Kind != owner.Kind {
						t.Fatal("delivery event lost actor attribution")
					}
				}
				if delivery.Message.Sender.URN != owner.Actor {
					t.Fatal("sender attribution lost")
				}
			}
			sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
			if !reflect.DeepEqual(got, recipientURNs(route)) {
				t.Fatal("host sender changed resolved recipients")
			}
		}
	})
	t.Run("denied_send_has_no_provider_effect", func(t *testing.T) {
		before := p.Events()
		_, err := router.Send(ctx, definition, teams.AddressRequest{RunID: run.ID, Actor: worker.Actor, Address: "@owner", Verb: mesh.MessageAddress, Body: "denied"}, "denied")
		if !errors.Is(err, teams.ErrDenied) {
			t.Fatalf("wanted authority denial, got %v", err)
		}
		if !reflect.DeepEqual(before, p.Events()) {
			t.Fatal("authority-denied send reached provider")
		}
	})
	t.Run("parent_lineage_and_cascade", func(t *testing.T) {
		childLimits := limits
		childLimits.Budget = 5
		child, err := teams.Spawn(ctx, definition, h, h, h, h, run.ID, owner.Actor, "workers", "child", childLimits, limits)
		if err != nil {
			t.Fatal(err)
		}
		a := invoke(t, p, mesh.Request{Verb: mesh.AgentStatus, Actor: actor, Target: child.Actor}).Instance
		if a.Parent != owner.Actor || child.Parent != owner.ID || a.Limits != childLimits {
			t.Fatal("member parent/URN translation or limits lost")
		}
		invoke(t, p, mesh.Request{Verb: mesh.MemberAdd, Actor: actor, Team: teamURN, Target: child.Actor, Slot: child.Slot, Role: "engineer"})
		updated, err := h.Snapshot(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := teams.Cascade(updated, owner.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		invoke(t, p, mesh.Request{Verb: mesh.Cancel, Actor: actor, Target: owner.Actor, Cascade: true})
		for _, m := range plan {
			if invoke(t, p, mesh.Request{Verb: mesh.AgentStatus, Actor: actor, Target: m.Actor}).Instance.SessionState != mesh.SessionEnded {
				t.Fatal("provider cascade disagrees with team lineage")
			}
		}
		if invoke(t, p, mesh.Request{Verb: mesh.AgentStatus, Actor: actor, Target: worker.Actor}).Instance.SessionState != mesh.SessionRunning {
			t.Fatal("cascade stopped unrelated team member")
		}
	})
	t.Run("slot_and_role_are_distinct", func(t *testing.T) {
		for _, m := range roster.Members {
			if m.Slot == "workers" {
				out := invoke(t, p, mesh.Request{Verb: mesh.RoleAssign, Actor: actor, Team: teamURN, Target: m.Actor, Role: "reviewer"})
				if out.Team.Members[m.Actor].Slot != "workers" || out.Team.Members[m.Actor].Role != "reviewer" {
					t.Fatal("role assignment changed the slot")
				}
			}
		}
		message := invoke(t, p, mesh.Request{Verb: mesh.MessageAddress, Actor: actor, Team: teamURN, Address: "@workers*"}).Message
		route, err := router.Resolve(ctx, definition, teams.AddressRequest{RunID: run.ID, Actor: owner.Actor, Address: "@workers*", Verb: mesh.MessageAddress})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(message.Recipients, recipientURNs(route)) {
			t.Fatal("distinct role labels altered slot addressing")
		}
		_, err = p.Invoke(ctx, mesh.Request{Verb: mesh.MessageAddress, Actor: actor, Team: teamURN, Address: "@reviewer*"})
		var typed *mesh.Error
		if !errors.As(err, &typed) || typed.Code != mesh.ErrorNotFound {
			t.Fatalf("role label became a slot selector: %v", err)
		}
	})
	t.Run("dispatch_policy", func(t *testing.T) {
		// Role changes leave functional slot dispatch intact.
		for _, m := range roster.Members {
			if m.Slot == "workers" {
				invoke(t, p, mesh.Request{Verb: mesh.RoleAssign, Actor: actor, Team: teamURN, Target: m.Actor, Role: "engineer"})
			}
		}
		roundRobin := definition
		roundRobin.Slots = append([]teams.Slot(nil), definition.Slots...)
		roundRobin.Slots[1].Dispatch = teams.RoundRobin
		// The owner is ended in mesh, but remains a roster actor for this pure check.
		resolver := teams.Router{Roster: h}
		first, err := resolver.Resolve(ctx, roundRobin, teams.AddressRequest{RunID: run.ID, Actor: owner.Actor, Address: "@workers", Verb: mesh.MessageAddress})
		if err != nil {
			t.Fatal(err)
		}
		second, err := resolver.Resolve(ctx, roundRobin, teams.AddressRequest{RunID: run.ID, Actor: owner.Actor, Address: "@workers", Verb: mesh.MessageAddress})
		if err != nil {
			t.Fatal(err)
		}
		if reflect.DeepEqual(recipientURNs(first), recipientURNs(second)) {
			t.Fatal("round-robin did not rotate")
		}
		a := invoke(t, p, mesh.Request{Verb: mesh.MessageAddress, Actor: actor, Team: teamURN, Address: "@workers"}).Message
		b := invoke(t, p, mesh.Request{Verb: mesh.MessageAddress, Actor: actor, Team: teamURN, Address: "@workers"}).Message
		if !reflect.DeepEqual(a.Recipients, b.Recipients) {
			t.Fatal("expected fake's deterministic dispatch")
		}
		t.Log("teams supports round-robin; provider re-addressing repeats its first actor")
	})
}
