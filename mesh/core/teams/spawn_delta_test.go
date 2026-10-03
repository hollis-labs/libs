package teams_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/hollis-labs/substrate/mesh"
	"github.com/hollis-labs/substrate/mesh/teams"
	"github.com/hollis-labs/substrate/mesh/teams/memory"
)

func TestPerCallWorkHistoryAndRetryBinding(t *testing.T) {
	for _, verb := range []mesh.Verb{mesh.Delegate, mesh.Handoff} {
		t.Run(string(verb), func(t *testing.T) {
			d, h, _, run, roster := launch(t)
			d.Policy.History = mesh.HistoryFull
			d.Authority.Grants = append(d.Authority.Grants, teams.Grant{FromSlot: "owner", Verb: teams.MayHandoff, ToSlot: "engineer"})
			owner := slotMember(t, roster, "owner")
			router := teams.Router{Roster: h, Sender: h}
			req := teams.AddressRequest{RunID: run.ID, Actor: owner.Actor, Address: "@engineer", Body: "work", Verb: verb, History: mesh.HistoryNone}
			route, err := router.Send(ctx, d, req, "override")
			if err != nil {
				t.Fatal(err)
			}
			delivered := h.Deliveries()[0]
			if delivered.History != mesh.HistoryNone {
				t.Fatal("lost per-call history")
			}
			req.History = mesh.HistorySummary
			if err := router.SendResolved(ctx, d, req, route, "override"); !errors.Is(err, teams.ErrConflict) {
				t.Fatalf("changed history retry accepted: %v", err)
			}
			req.History = "invalid"
			if _, err := router.Send(ctx, d, req, "invalid"); err == nil {
				t.Fatal("invalid history accepted")
			}
			req.History = ""
			if _, err := router.Send(ctx, d, req, "team-default"); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, delivery := range h.Deliveries() {
				found = found || delivery.History == mesh.HistoryFull
			}
			if !found {
				t.Fatal("lost team history default")
			}
		})
	}
}

func TestDelegateResultReturnsToAcceptedSenderAtNextTurn(t *testing.T) {
	d, h, _, run, roster := launch(t)
	owner := slotMember(t, roster, "owner")
	router := teams.Router{Roster: h, Sender: h}
	req := teams.AddressRequest{RunID: run.ID, Actor: owner.Actor, Address: "@engineer", Verb: mesh.Delegate, Body: "produce answer"}
	route, err := router.Send(ctx, d, req, "delegate")
	if err != nil {
		t.Fatal(err)
	}
	original := h.Deliveries()[0]
	worker := route.Recipients[0]
	// The worker has no reverse may_message grant. A delegation authorizes its reply.
	if err := router.ReplyResult(ctx, d, run.ID, original.IdempotencyKey, owner.Actor, "answer"); !errors.Is(err, teams.ErrDenied) {
		t.Fatalf("wrong reporter accepted: %v", err)
	}
	if err := router.ReplyResult(ctx, d, run.ID, "missing", worker.Actor, "answer"); !errors.Is(err, teams.ErrNotFound) {
		t.Fatalf("unknown delegation accepted: %v", err)
	}
	if err := router.ReplyResult(ctx, d, run.ID, original.IdempotencyKey, worker.Actor, "answer"); err != nil {
		t.Fatal(err)
	}
	if err := h.Mutate(ctx, run.ID, func(r *teams.Roster) error {
		for i := range r.Members {
			r.Members[i].Idle = !r.Members[i].Idle
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Mutable roster metadata does not alter the immutable reply plan on retry.
	if err := router.ReplyResult(ctx, d, run.ID, original.IdempotencyKey, worker.Actor, "answer"); err != nil {
		t.Fatal(err)
	}
	var reply teams.Delivery
	for _, delivery := range h.Deliveries() {
		if delivery.Verb == mesh.Reply {
			reply = delivery
		}
	}
	if reply.From != worker.Actor || reply.Recipient.Actor != owner.Actor || reply.InReplyTo != original.IdempotencyKey || reply.Delivery != mesh.DeliveryAtIdle || reply.Body != "answer" {
		t.Fatalf("incorrect result reply: %+v", reply)
	}
	if err := router.ReplyResult(ctx, d, run.ID, original.IdempotencyKey, worker.Actor, "different"); !errors.Is(err, teams.ErrConflict) {
		t.Fatalf("changed result accepted: %v", err)
	}
	req.Body = "second task"
	if _, err := router.Send(ctx, d, req, "second-delegate"); err != nil {
		t.Fatal(err)
	}
	var second teams.Delivery
	for _, delivery := range h.Deliveries() {
		if delivery.Verb == mesh.Delegate && delivery.Body == req.Body {
			second = delivery
		}
	}
	if err := h.Mutate(ctx, run.ID, func(r *teams.Roster) error {
		for i := range r.Members {
			if r.Members[i].ID == owner.ID {
				r.Members[i].SessionID = "replacement"
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := router.ReplyResult(ctx, d, run.ID, second.IdempotencyKey, second.Recipient.Actor, "answer"); !errors.Is(err, teams.ErrConflict) {
		t.Fatalf("reply reached replacement session: %v", err)
	}
	if err := router.ReplyResult(ctx, d, run.ID, original.IdempotencyKey, worker.Actor, "answer"); err != nil {
		t.Fatalf("accepted result replay depended on current session: %v", err)
	}
}

func TestUnacceptedDelegateCannotReturnResult(t *testing.T) {
	d, h, _, run, roster := launch(t)
	router := teams.Router{Roster: h, Sender: h}
	h.Before = func(op, _ string) error {
		if op == "send" {
			return errors.New("transport unavailable")
		}
		return nil
	}
	req := teams.AddressRequest{RunID: run.ID, Actor: slotMember(t, roster, "owner").Actor, Address: "@engineer", Verb: mesh.Delegate, Body: "work"}
	if _, err := router.Send(ctx, d, req, "failed"); err == nil {
		t.Fatal("expected transport failure")
	}
	h.Before = nil
	if err := router.ReplyResult(ctx, d, run.ID, "failed", slotMember(t, roster, "engineer").Actor, "answer"); !errors.Is(err, teams.ErrNotFound) {
		t.Fatalf("unaccepted delegation returned: %v", err)
	}
}

func TestTerminationRecoveryKeepsChildrenBeforeParents(t *testing.T) {
	for _, ending := range []bool{false, true} {
		h := memory.New()
		members := []teams.Member{{ID: "root", Resolution: teams.Fresh}, {ID: "child", Parent: "root", Resolution: teams.Fresh}, {ID: "leaf", Parent: "child", Resolution: teams.Fresh}}
		for i := range members {
			members[i].Status = "stopping"
			if ending {
				members[i].Status = "active"
			}
		}
		if err := h.Mutate(ctx, "run", func(r *teams.Roster) error { r.Members = members; return nil }); err != nil {
			t.Fatal(err)
		}
		var order []string
		fail := true
		h.Before = func(op, key string) error {
			if op == "retired" {
				order = append(order, key)
				if key == "leaf" && fail {
					return errors.New("lost child acknowledgement")
				}
			}
			return nil
		}
		var err error
		if ending {
			err = teams.EndRun(ctx, "run", h, h, h)
		} else {
			err = teams.ReconcileMembers(ctx, h, h, "run")
		}
		if err == nil || !reflect.DeepEqual(order, []string{"leaf"}) {
			t.Fatalf("stopped ancestor before failed child: %v %v", order, err)
		}
		fail = false
		order = nil
		if err := teams.ReconcileMembers(ctx, h, h, "run"); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(order, []string{"leaf", "child", "root"}) {
			t.Fatalf("recovery lost child-first order: %v", order)
		}
	}
}

func TestConcurrentLaunchesSelectSeparatePoolSubsets(t *testing.T) {
	d := team()
	d.Slots[1].Min = 1
	h := memory.New()
	h.SpawnCapabilities["owner"] = true
	if err := putDefinition(h, d); err != nil {
		t.Fatal(err)
	}
	l := launcher(h)
	ids := d.Slots[1].Identities
	requests := []teams.LaunchRequest{
		{Key: "first", TeamID: d.ID, Version: d.Version, PoolIdentities: map[string][]mesh.URN{"engineer": ids[:2]}},
		{Key: "second", TeamID: d.ID, Version: d.Version, PoolIdentities: map[string][]mesh.URN{"engineer": ids[2:]}},
	}
	runs := make([]teams.TeamRun, 2)
	var wg sync.WaitGroup
	for i := range requests {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			run, err := l.Launch(ctx, requests[i])
			if err != nil {
				t.Error(err)
			}
			runs[i] = run
		}(i)
	}
	wg.Wait()
	for i, run := range runs {
		roster, err := h.Snapshot(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		worker := slotMember(t, roster, "engineer")
		if worker.Actor != requests[i].PoolIdentities["engineer"][0] {
			t.Fatal("launch ignored pool override")
		}
		owner := slotMember(t, roster, "owner")
		request := limits()
		request.Budget = 5
		child, err := teams.Spawn(ctx, d, h, h, h, h, run.ID, owner.Actor, "engineer", "spawn", request, limits())
		if err != nil {
			t.Fatal(err)
		}
		if child.Actor != requests[i].PoolIdentities["engineer"][1] {
			t.Fatal("spawn escaped run's identity subset")
		}
	}
	changed := requests[0]
	changed.PoolIdentities = requests[1].PoolIdentities
	if _, err := l.Launch(ctx, changed); !errors.Is(err, teams.ErrConflict) {
		t.Fatalf("changed identity retry accepted: %v", err)
	}
	invalid := requests[0]
	invalid.Key = "invalid"
	// This identity belongs to a different team's enrolled pool. It must
	// not also occur in this team, where duplicate-identity validation would
	// mask a missing slot-membership check.
	outsider := mesh.URN("msg://agent/memory/other-pool-worker")
	foreign := team()
	foreign.ID = "other-pool"
	foreign.Slots[1].Pool = "other-pool"
	foreign.Slots[1].Identities = []mesh.URN{outsider, "msg://agent/memory/other-2", "msg://agent/memory/other-3", "msg://agent/memory/other-4"}
	if err := putDefinition(h, foreign); err != nil {
		t.Fatal(err)
	}
	invalid.PoolIdentities = map[string][]mesh.URN{"engineer": {outsider, ids[1]}}
	if _, err := l.Launch(ctx, invalid); err == nil {
		t.Fatal("identity outside enrolled pool accepted")
	}
}

func TestDelegateResultRecoversLostQueueAcknowledgement(t *testing.T) {
	d, h, _, run, roster := launch(t)
	router := teams.Router{Roster: h, Sender: h}
	req := teams.AddressRequest{RunID: run.ID, Actor: slotMember(t, roster, "owner").Actor, Address: "@engineer", Verb: mesh.Delegate, Body: "work"}
	route, err := router.Send(ctx, d, req, "delegate")
	if err != nil {
		t.Fatal(err)
	}
	original := h.Deliveries()[0]
	h.After = func(op, _ string) error {
		if op == "send" {
			return errors.New("queue committed; acknowledgement lost")
		}
		return nil
	}
	if err := router.ReplyResult(ctx, d, run.ID, original.IdempotencyKey, route.Recipients[0].Actor, "answer"); err == nil {
		t.Fatal("expected lost acknowledgement")
	}
	h.After = nil
	h.Before = func(op, _ string) error {
		if op == "send" {
			t.Error("replayed accepted result through transport")
		}
		return nil
	}
	if err := router.ReplyResult(ctx, d, run.ID, original.IdempotencyKey, route.Recipients[0].Actor, "answer"); err != nil {
		t.Fatal(err)
	}
}

func TestTerminationCycleFailsBeforeExternalCleanup(t *testing.T) {
	h := memory.New()
	if err := h.Mutate(ctx, "run", func(r *teams.Roster) error {
		r.Members = []teams.Member{{ID: "a", Parent: "b", Status: "stopping", Resolution: teams.Fresh}, {ID: "b", Parent: "a", Status: "stopping", Resolution: teams.Fresh}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h.Before = func(op, _ string) error {
		if op == "retired" {
			t.Error("cleanup called for cyclic lineage")
		}
		return nil
	}
	if err := teams.ReconcileMembers(ctx, h, h, "run"); err == nil {
		t.Fatal("cyclic lineage accepted")
	}
}

type borrowedPoolIdentity struct {
	*memory.Host
	actor mesh.URN
}

func (p borrowedPoolIdentity) Provision(c context.Context, req teams.ProvisionRequest) (teams.Member, error) {
	m, err := p.Host.Provision(c, req)
	if err == nil && req.Slot.Name == "owner" && req.Slot.Resolution == teams.Fresh {
		m.Actor = p.actor
	}
	return m, err
}

func TestPoolSubsetPreservesFreshIdentityReservations(t *testing.T) {
	d := team()
	h := memory.New()
	if err := putDefinition(h, d); err != nil {
		t.Fatal(err)
	}
	l := launcher(h)
	// A buggy provisioner returns a durable identity omitted from this run's
	// subset. It remains reserved by the authored pool and cannot become fresh.
	l.Provisioner = borrowedPoolIdentity{Host: h, actor: d.Slots[1].Identities[2]}
	_, err := l.Launch(ctx, teams.LaunchRequest{Key: "borrowed", TeamID: d.ID, Version: d.Version, PoolIdentities: map[string][]mesh.URN{"engineer": d.Slots[1].Identities[:2]}})
	if !errors.Is(err, teams.ErrLaunchFailed) {
		t.Fatalf("fresh identity borrowed from excluded pool: %v", err)
	}
}

func TestReplyRequiresOriginalLiveAssigneeAndActiveDelegation(t *testing.T) {
	for _, change := range []string{"removed", "replacement-member", "replacement-session", "cancelled", "ended"} {
		t.Run(change, func(t *testing.T) {
			d, h, _, run, roster := launch(t)
			router := teams.Router{Roster: h, Sender: h}
			req := teams.AddressRequest{RunID: run.ID, Actor: slotMember(t, roster, "owner").Actor, Address: "@engineer", Verb: mesh.Delegate, Body: "work"}
			route, err := router.Send(ctx, d, req, "delegate")
			if err != nil {
				t.Fatal(err)
			}
			original := h.Deliveries()[0]
			worker := route.Recipients[0]
			if change == "cancelled" || change == "ended" {
				state := mesh.TaskCanceled
				if change == "ended" {
					state = mesh.TaskFailed
				}
				if err := h.EndDelegation(ctx, original.IdempotencyKey, state); err != nil {
					t.Fatal(err)
				}
			} else if change == "removed" {
				d.Slots[1].Min = 1
				if err := teams.RemoveMember(ctx, d, h, h, run.ID, req.Actor, worker.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := h.Mutate(ctx, run.ID, func(r *teams.Roster) error {
					for i := range r.Members {
						if r.Members[i].ID == worker.ID {
							switch change {
							case "removed":
								r.Members[i].Status = "released"
							case "replacement-member":
								r.Members[i].ID = "replacement"
							case "replacement-session":
								r.Members[i].SessionID = "replacement"
							}
						}
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			h.Before = func(op, _ string) error {
				if op == "send" {
					t.Error("invalid result passed library policy and reached transport")
				}
				return nil
			}
			if err := router.ReplyResult(ctx, d, run.ID, original.IdempotencyKey, worker.Actor, "answer"); err == nil {
				t.Fatal("stale or ended delegation accepted a result")
			}
			for _, delivery := range h.Deliveries() {
				if delivery.Verb == mesh.Reply {
					t.Fatal("refused result reached transport")
				}
			}
		})
	}
}

func TestReplyRejectsWrongRunVersionAndNonDelegation(t *testing.T) {
	d, h, _, run, roster := launch(t)
	router := teams.Router{Roster: h, Sender: h}
	actor := slotMember(t, roster, "owner").Actor
	for _, verb := range []mesh.Verb{mesh.Delegate, mesh.MessageAddress} {
		req := teams.AddressRequest{RunID: run.ID, Actor: actor, Address: "@engineer", Verb: verb, Body: string(verb)}
		route, err := router.Send(ctx, d, req, string(verb))
		if err != nil {
			t.Fatal(err)
		}
		var original teams.Delivery
		for _, delivery := range h.Deliveries() {
			if delivery.Verb == verb {
				original = delivery
			}
		}
		if verb != mesh.Delegate {
			if err := router.ReplyResult(ctx, d, run.ID, original.IdempotencyKey, route.Recipients[0].Actor, "answer"); !errors.Is(err, teams.ErrConflict) {
				t.Fatalf("non-delegation accepted: %v", err)
			}
			continue
		}
		if err := router.ReplyResult(ctx, d, "other-run", original.IdempotencyKey, route.Recipients[0].Actor, "answer"); !errors.Is(err, teams.ErrConflict) {
			t.Fatalf("cross-run reply accepted: %v", err)
		}
		other := d
		other.Version++
		if err := router.ReplyResult(ctx, other, run.ID, original.IdempotencyKey, route.Recipients[0].Actor, "answer"); !errors.Is(err, teams.ErrConflict) {
			t.Fatalf("cross-version reply accepted: %v", err)
		}
	}
}

func TestHistoryOnlyNarrowsTeamPolicy(t *testing.T) {
	policies := []mesh.HistoryPolicy{mesh.HistoryNone, mesh.HistorySummary, mesh.HistoryFiltered, mesh.HistoryFull}
	for ceiling, policy := range policies {
		for requested, history := range policies {
			t.Run(string(policy)+"/"+string(history), func(t *testing.T) {
				d, h, _, run, roster := launch(t)
				d.Policy.History = policy
				router := teams.Router{Roster: h, Sender: h}
				req := teams.AddressRequest{RunID: run.ID, Actor: slotMember(t, roster, "owner").Actor, Address: "@engineer", Body: "work", Verb: mesh.Delegate, History: history}
				_, err := router.Send(ctx, d, req, "work")
				if requested > ceiling {
					if !errors.Is(err, teams.ErrDenied) {
						t.Fatalf("widening history accepted: %v", err)
					}
					if len(h.Deliveries()) != 0 {
						t.Fatal("widening history reached transport")
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					if h.Deliveries()[0].History != history {
						t.Fatal("narrow history lost")
					}
				}
			})
		}
	}
}

func TestRecoveryContinuesPastFailedOrCyclicSubtree(t *testing.T) {
	for _, broken := range []string{"failure", "cycle"} {
		t.Run(broken, func(t *testing.T) {
			h := memory.New()
			p := teams.ProvisionRequest{IdempotencyKey: "pending", RunID: "run", MemberID: "pending", Slot: teams.Slot{Name: "pending", Resolution: teams.Fresh, Definition: mesh.DefinitionRef{ID: "worker", Revision: "r1"}}, Limits: limits()}
			members := []teams.Member{{ID: "a", Status: "stopping", Resolution: teams.Fresh}, {ID: "a1", Parent: "a", Status: "stopping", Resolution: teams.Fresh}, {ID: "b", Status: "stopping", Resolution: teams.Fresh}, {ID: "b1", Parent: "b", Status: "stopping", Resolution: teams.Fresh}, {ID: "pending", Slot: "pending", Status: "provisioning", Resolution: teams.Fresh, Limits: limits(), Intent: &p}}
			if broken == "cycle" {
				members[0].Parent = "a1"
			}
			if err := h.Mutate(ctx, "run", func(r *teams.Roster) error { r.Members = members; return nil }); err != nil {
				t.Fatal(err)
			}
			var ended []string
			h.Before = func(op, key string) error {
				if op == "retired" {
					ended = append(ended, key)
					if broken == "failure" && key == "a1" {
						return errors.New("cannot retire a1")
					}
				}
				return nil
			}
			if err := teams.ReconcileMembers(ctx, h, h, "run"); err == nil {
				t.Fatal("missing failed-branch diagnostic")
			}
			expected := []string{"b1", "b"}
			if broken == "failure" {
				expected = append([]string{"a1"}, expected...)
			}
			if !reflect.DeepEqual(ended, expected) {
				t.Fatalf("failed branch blocked unrelated cleanup or stopped ancestor: %v", ended)
			}
			roster, err := h.Snapshot(ctx, "run")
			if err != nil {
				t.Fatal(err)
			}
			if slotMember(t, roster, "pending").Status != "active" {
				t.Fatal("failed termination blocked unrelated provisioning")
			}
		})
	}
}

func TestCancellationBetweenResultValidationAndQueueWins(t *testing.T) {
	d, h, _, run, roster := launch(t)
	router := teams.Router{Roster: h, Sender: h}
	req := teams.AddressRequest{RunID: run.ID, Actor: slotMember(t, roster, "owner").Actor, Address: "@engineer", Verb: mesh.Delegate, Body: "work"}
	route, err := router.Send(ctx, d, req, "delegate")
	if err != nil {
		t.Fatal(err)
	}
	original := h.Deliveries()[0]
	h.Before = func(op, _ string) error {
		if op == "send" {
			return h.EndDelegation(ctx, original.IdempotencyKey, mesh.TaskCanceled)
		}
		return nil
	}
	if err := router.ReplyResult(ctx, d, run.ID, original.IdempotencyKey, route.Recipients[0].Actor, "answer"); !errors.Is(err, teams.ErrConflict) {
		t.Fatalf("racing cancellation lost: %v", err)
	}
	for _, delivery := range h.Deliveries() {
		if delivery.Verb == mesh.Reply {
			t.Fatal("cancelled result accepted by queue")
		}
	}
}
