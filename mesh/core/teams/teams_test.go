package teams_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/substrate/mesh"
	"github.com/hollis-labs/substrate/mesh/teams"
	"github.com/hollis-labs/substrate/mesh/teams/memory"
)

var ctx = context.Background()

func limits() mesh.Limits {
	return mesh.Limits{MaxDepth: 3, MaxChildren: 8, FanOut: 8, Budget: 100, Timeout: time.Minute}
}
func team() teams.Team {
	return teams.Team{ID: "feature", Name: "Feature development", Version: 1,
		Slots: []teams.Slot{
			{Name: "owner", Definition: mesh.DefinitionRef{ID: "lead", Revision: "r1"}, Role: "lead", Resolution: teams.Fresh, Activation: teams.Singleton, Required: true, Min: 1, Max: 1},
			{Name: "engineer", Role: "engineer", Resolution: teams.Pool, Pool: "workers", Identities: []mesh.URN{"msg://agent/memory/worker-1", "msg://agent/memory/worker-2", "msg://agent/memory/worker-3", "msg://agent/memory/worker-4"}, Activation: teams.Concurrent, Min: 2, Max: 4},
			{Name: "reviewer", Definition: mesh.DefinitionRef{ID: "reviewer", Revision: "r1"}, Role: "reviewer", Resolution: teams.Fresh, Activation: teams.Singleton, Required: true, Min: 1, Max: 1},
			{Name: "architect", Resolution: teams.Durable, Identity: "msg://agent/local/architect", Activation: teams.Singleton, Max: 1},
		},
		Phases: []teams.Phase{{ID: "work", Kind: "flex", ActiveSlots: []string{"owner", "engineer", "reviewer", "architect"}, OwnerSlot: "owner", ApproverSlot: "reviewer", ExitTrigger: teams.Trigger{Kind: "event", Spec: map[string]string{"event": "ready"}}}},
		Authority: teams.Authority{Mode: teams.Strict, Grants: []teams.Grant{
			{FromSlot: "owner", Verb: teams.MayMessage, ToSlot: "engineer"},
			{FromSlot: "owner", Verb: teams.MayMessage, ToSlot: "architect"},
			{FromSlot: "owner", Verb: teams.MaySpawn, ToSlot: "engineer"},
			{FromSlot: "owner", Verb: teams.MaySpawn, ToSlot: "architect"},
			{FromSlot: "owner", Verb: teams.MayAssign, ToSlot: "engineer"},
			{FromSlot: "owner", Verb: teams.MayDelegate, ToSlot: "engineer"},
			{FromSlot: "owner", Verb: teams.MaySignalPhase, ToSlot: teams.Self},
			{FromSlot: "owner", Verb: teams.MayAdmin, ToSlot: "engineer"},
			{FromSlot: "reviewer", Verb: teams.MayApprove, ToSlot: "engineer"},
			{FromSlot: "reviewer", Verb: teams.MayNotReview, ToSlot: teams.Self},
		}},
		Routing: teams.Routing{CoordinatorSlot: "owner", Rules: []teams.RoutingRule{{Name: "architecture", Phrases: []string{"architecture?"}, TargetSlot: "architect", Priority: 10}}},
		Policy:  teams.Policy{Spawn: limits()},
	}
}
func putDefinition(h *memory.Host, d teams.Team) error {
	for _, slot := range d.Slots {
		ids := slot.Identities
		if slot.Identity != "" {
			ids = append(ids, slot.Identity)
		}
		for _, id := range ids {
			if err := h.EnrollIdentity(ctx, id); err != nil {
				return err
			}
		}
	}
	return h.PutDefinition(ctx, d)
}
func launcher(h *memory.Host) *teams.Launcher {
	return &teams.Launcher{Definitions: h, Roster: h, Ledger: h, Provisioner: h, Workflows: h, Routing: h, Clock: h, IDs: h, Defaults: limits()}
}
func launch(t *testing.T) (teams.Team, *memory.Host, *teams.Launcher, teams.TeamRun, teams.Roster) {
	t.Helper()
	definition := team()
	h := memory.New()
	h.SpawnCapabilities["owner"] = true
	if err := putDefinition(h, definition); err != nil {
		t.Fatal(err)
	}
	l := launcher(h)
	run, err := l.Launch(ctx, teams.LaunchRequest{Key: "test", TeamID: definition.ID, Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	r, err := h.Snapshot(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return definition, h, l, run, r
}
func slotMember(t *testing.T, r teams.Roster, slot string) teams.Member {
	t.Helper()
	for _, m := range r.Members {
		if m.Slot == slot {
			return m
		}
	}
	t.Fatal("missing slot " + slot)
	return teams.Member{}
}
func TestCompilerAndValidation(t *testing.T) {
	d := team()
	compiled, err := teams.CompileTeam(d, d.Phases)
	if err != nil {
		t.Fatal(err)
	}
	d.Phases[0].ActiveSlots[0] = "changed"
	d.Phases[0].ExitTrigger.Spec["event"] = "mutated"
	if compiled.Steps[0].ActiveSlots[0] != "owner" || compiled.Steps[0].ExitTrigger.Spec["event"] != "ready" {
		t.Fatal("compiler retained aliases")
	}
	cases := map[string]func(*teams.Team){
		"gate":               func(d *teams.Team) { d.Phases[0].Kind = "gate" },
		"two phases":         func(d *teams.Team) { d.Phases = append(d.Phases, d.Phases[0]) },
		"duplicate slot":     func(d *teams.Team) { d.Slots = append(d.Slots, d.Slots[0]) },
		"reserved slot":      func(d *teams.Team) { d.Slots[0].Name = "all" },
		"missing pool":       func(d *teams.Team) { d.Slots[1].Pool = "" },
		"durable multi":      func(d *teams.Team) { d.Slots[3].Max = 2 },
		"negative minimum":   func(d *teams.Team) { d.Slots[1].Min = -1 },
		"required zero":      func(d *teams.Team) { d.Slots[0].Min = 0 },
		"unknown active":     func(d *teams.Team) { d.Phases[0].ActiveSlots = []string{"missing"} },
		"empty trigger":      func(d *teams.Team) { d.Phases[0].ExitTrigger.Spec = nil },
		"unknown approver":   func(d *teams.Team) { d.Phases[0].ApproverSlot = "human" },
		"invalid permission": func(d *teams.Team) { d.Authority.Grants[0].Verb = "grant-all" },
		"unknown target":     func(d *teams.Team) { d.Routing.Rules[0].TargetSlot = "missing" },
		"empty phrase":       func(d *teams.Team) { d.Routing.Rules[0].Phrases = []string{""} },
		"negative limit":     func(d *teams.Team) { d.Policy.Spawn.MaxChildren = -1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			d := team()
			mutate(&d)
			if err := teams.Validate(d); err == nil {
				t.Fatal("accepted invalid definition")
			}
		})
	}
}
func TestAuthorityEveryVerbAndActorKinds(t *testing.T) {
	d := team()
	from := teams.Member{ID: "a", Slot: "owner", Actor: "msg://agent/local/owner", Kind: mesh.ActorAgent, Status: "active"}
	to := teams.Member{ID: "b", Slot: "engineer", Actor: "msg://user/local/operator", Kind: mesh.ActorUser, Status: "active"}
	for _, verb := range []teams.Permission{teams.MaySpawn, teams.MayMessage, teams.MayDelegate, teams.MayHandoff, teams.MayAssign, teams.MayApprove, teams.MaySignalPhase, teams.MayAdmin} {
		d.Authority.Grants = nil
		if err := teams.Authorize(d, from, to, verb, ""); !errors.Is(err, teams.ErrDenied) {
			t.Fatalf("%s did not deny", verb)
		}
		d.Authority.Grants = []teams.Grant{{FromActor: from.Actor, Verb: verb, ToActor: to.Actor}}
		if err := teams.Authorize(d, from, to, verb, ""); err != nil {
			t.Fatalf("%s: %v", verb, err)
		}
	}
	d.Authority.Mode = teams.DevOpen
	d.Authority.Grants = nil
	if err := teams.Authorize(d, from, to, teams.MayMessage, ""); err != nil {
		t.Fatal(err)
	}
	if err := teams.Authorize(d, from, to, teams.MayApprove, ""); !errors.Is(err, teams.ErrDenied) {
		t.Fatal("dev approvals bypassed explicit grants")
	}
	d.Authority.Grants = []teams.Grant{{FromSlot: "owner", Verb: teams.MayMessage, ToSlot: teams.Self}}
	if err := teams.Authorize(d, from, to, teams.MayMessage, ""); !errors.Is(err, teams.ErrDenied) {
		t.Fatal("self granted peers")
	}
	if err := teams.Authorize(d, from, from, teams.MayMessage, ""); err != nil {
		t.Fatal(err)
	}
}
func TestApprovalHardGateAndAuthorExclusion(t *testing.T) {
	d, _, _, _, r := launch(t)
	reviewer := slotMember(t, r, "reviewer")
	author := slotMember(t, r, "engineer")
	phase := d.Phases[0]
	phase.ApproverSlot = "reviewer"
	if err := teams.CheckApproval(d, r, phase, string(reviewer.Actor), string(author.Actor), string(author.Actor)); err != nil {
		t.Fatal(err)
	}
	if err := teams.CheckApproval(d, r, phase, string(author.Actor), string(author.Actor), string(author.Actor)); !errors.Is(err, teams.ErrDenied) {
		t.Fatal("wrong slot approval accepted")
	}
	// A reviewer who authored engineer work may not approve it.
	if err := teams.CheckApproval(d, r, phase, string(reviewer.Actor), string(author.Actor), string(reviewer.Actor)); !errors.Is(err, teams.ErrDenied) {
		t.Fatal("author approved own work")
	}
	reviewer.Kind = mesh.ActorUser
	reviewer.Actor = "msg://user/local/operator"
	for i := range r.Members {
		if r.Members[i].ID == reviewer.ID {
			r.Members[i] = reviewer
		}
	}
	d.Authority.Grants = nil
	if err := teams.CheckApproval(d, r, phase, string(reviewer.Actor), string(author.Actor), string(author.Actor)); !errors.Is(err, teams.ErrDenied) {
		t.Fatal("user kind bypassed grant")
	}
}
func TestRoutingOneAllURNProvenanceAndSnapshot(t *testing.T) {
	d, h, _, run, r := launch(t)
	owner := slotMember(t, r, "owner")
	engineer := slotMember(t, r, "engineer")
	router := &teams.Router{Roster: h, Sender: h}
	req := teams.AddressRequest{RunID: run.ID, Actor: owner.Actor, Address: "@engineer", Body: "work", Verb: mesh.MessageAddress}
	first, err := router.Resolve(ctx, d, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := router.Resolve(ctx, d, req)
	if err != nil || first.Recipients[0].ID == second.Recipients[0].ID {
		t.Fatal("round robin did not rotate", err)
	}
	req.Address = "@engineer*"
	route, err := router.Send(ctx, d, req, "fanout")
	if err != nil || len(route.Recipients) != 2 {
		t.Fatal(route, err)
	}
	for _, delivery := range h.Deliveries() {
		if delivery.Route.RosterVersion != r.Version || delivery.Route.Rule != "explicit" || delivery.History != mesh.HistorySummary {
			t.Fatal("missing snapshot/provenance/history")
		}
	}
	for _, address := range []string{"@" + engineer.ID, string(engineer.Actor)} {
		req.Address = address
		route, err = router.Resolve(ctx, d, req)
		if err != nil || route.Recipients[0].ID != engineer.ID {
			t.Fatal(address, err)
		}
	}
	req.Address = "@engineer*"
	req.Verb = mesh.Assign
	if _, err := router.Resolve(ctx, d, req); err == nil {
		t.Fatal("work broadcast allowed")
	}
	req.Verb = mesh.MessageAddress
	req.Address = "@all"
	if _, err := router.Send(ctx, d, req, "denied-all"); !errors.Is(err, teams.ErrDenied) {
		t.Fatal("fanout not checked in full", err)
	}
	if len(h.Deliveries()) != 2 {
		t.Fatal("unauthorized send had side effect")
	}
	req.Address = "@engineer"
	req.Kind = mesh.ActorUser
	if _, err := router.Resolve(ctx, d, req); !errors.Is(err, teams.ErrUnavailable) {
		t.Fatal("kind filter ignored")
	}
}
func TestRoutingIdleFirstFailureAndLazyOnce(t *testing.T) {
	d, h, _, run, r := launch(t)
	owner := slotMember(t, r, "owner")
	req := teams.AddressRequest{RunID: run.ID, Actor: owner.Actor, Address: "@engineer", Body: "architecture?", Verb: mesh.MessageAddress}
	router := &teams.Router{Roster: h, Sender: h}
	var idle string
	if err := h.Mutate(ctx, run.ID, func(r *teams.Roster) error {
		first := true
		for i := range r.Members {
			if r.Members[i].Slot == "engineer" {
				r.Members[i].Idle = first
				if first {
					idle = r.Members[i].ID
				}
				first = false
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	route, err := router.Resolve(ctx, d, req)
	if err != nil || route.Recipients[0].ID != idle {
		t.Fatal("idle member not preferred", err)
	}
	lazyCalls := 0
	h.Before = func(op, key string) error {
		if op == "provision" {
			lazyCalls++
		}
		return nil
	}
	router.Lazy = &teams.LazyProvisioning{Provisioner: h, Trust: h, Approvals: h, Defaults: limits(), Limits: mesh.Limits{Budget: 1}}
	req.Address = ""
	route, err = router.Resolve(ctx, d, req)
	if err != nil || route.Rule != "architecture" || route.Recipients[0].Slot != "architect" {
		t.Fatal(route, err)
	}
	req.Address = "@architect"
	if _, err = router.Resolve(ctx, d, req); err != nil {
		t.Fatal(err)
	}
	if lazyCalls != 1 {
		t.Fatal("lazy member duplicated")
	}
	if err = h.Mutate(ctx, run.ID, func(r *teams.Roster) error {
		for i := range r.Members {
			if r.Members[i].Slot == "architect" {
				r.Members[i].Status = "stopped"
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = router.Resolve(ctx, d, req); !errors.Is(err, teams.ErrUnavailable) || lazyCalls != 1 {
		t.Fatal("stopped target silently rerouted or rewoken", err)
	}
}
func TestSendKeepsSnapshotAcrossRosterChange(t *testing.T) {
	d, h, _, run, r := launch(t)
	owner := slotMember(t, r, "owner")
	router := &teams.Router{Roster: h, Sender: h}
	changed := false
	h.Before = func(op, key string) error {
		if op == "send" && !changed {
			changed = true
			return h.Mutate(ctx, run.ID, func(r *teams.Roster) error {
				for i := range r.Members {
					if r.Members[i].Slot == "engineer" {
						r.Members[i].Status = "stopped"
					}
				}
				return nil
			})
		}
		return nil
	}
	route, err := router.Send(ctx, d, teams.AddressRequest{RunID: run.ID, Actor: owner.Actor, Address: "@engineer*", Body: "frozen roster", Verb: mesh.MessageAddress}, "snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if route.RosterVersion != r.Version || len(h.Deliveries()) != 2 {
		t.Fatal("fanout used changed roster")
	}
	h.Before = func(op, key string) error {
		if op == "send" {
			return errors.New("mailbox unavailable")
		}
		return nil
	}
	// Resolve a new active recipient to exercise transport failure.
	if err = h.Mutate(ctx, run.ID, func(r *teams.Roster) error {
		for i := range r.Members {
			if r.Members[i].Slot == "engineer" {
				r.Members[i].Status = "active"
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = router.Send(ctx, d, teams.AddressRequest{RunID: run.ID, Actor: owner.Actor, Address: "@engineer", Body: "fail loud", Verb: mesh.MessageAddress}, "fail"); err == nil {
		t.Fatal("send failure suppressed")
	}
}
func TestLaunchCrashRecoveryAtEveryBoundary(t *testing.T) {
	for _, boundary := range []string{"provision", "ledger_planning", "ledger_prepared", "workflow", "ledger_launched", "roster", "ledger_members_ready", "routing", "ledger_routing_ready"} {
		t.Run(boundary, func(t *testing.T) {
			d := team()
			h := memory.New()
			if err := putDefinition(h, d); err != nil {
				t.Fatal(err)
			}
			failed := false
			h.After = func(op, key string) error {
				if op == boundary && !failed {
					failed = true
					return errors.New("crash after side effect")
				}
				return nil
			}
			l := launcher(h)
			req := teams.LaunchRequest{Key: "recovery", TeamID: d.ID, Version: 1}
			if _, err := l.Launch(ctx, req); err == nil {
				t.Fatal("fault not exercised")
			}
			h.After = nil
			// New launcher, same persisted host; no process-local lock/state survives.
			l = launcher(h)
			report, err := l.Reconcile(ctx, "", 16)
			if err != nil || len(report.Failures) != 0 {
				t.Fatal(report, err)
			}
			run, err := l.Launch(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			r, err := h.Snapshot(ctx, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(h.Provisioned()) != 4 || len(r.Members) != 4 {
				t.Fatal("recovery duplicated/lost members")
			}
			record, err := h.GetLaunch(ctx, req.Key)
			if err != nil || record.State != teams.RoutingReady {
				t.Fatal(record.State, err)
			}
			req.Counts = map[string]int{"engineer": 3}
			if _, err = l.Launch(ctx, req); !errors.Is(err, teams.ErrConflict) {
				t.Fatal("same key accepted changed request", err)
			}
		})
	}
}
func TestConcurrentLaunchAndDetachedSnapshots(t *testing.T) {
	d := team()
	h := memory.New()
	h.SpawnCapabilities["owner"] = true
	if err := putDefinition(h, d); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := launcher(h).Launch(ctx, teams.LaunchRequest{Key: "concurrent", TeamID: d.ID, Version: 1})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	r, err := h.Snapshot(ctx, "run-concurrent")
	if err != nil || len(r.Members) != 4 || len(h.Provisioned()) != 4 {
		t.Fatal("concurrent launch duplicated members", err)
	}
	version := r.Version
	r.Members[0].Status = "mutated"
	again, _ := h.Snapshot(ctx, r.RunID)
	if again.Members[0].Status != "active" {
		t.Fatal("snapshot aliased")
	}
	if _, err = launcher(h).Launch(ctx, teams.LaunchRequest{Key: "concurrent", TeamID: d.ID, Version: 1}); err != nil {
		t.Fatal(err)
	}
	again, _ = h.Snapshot(ctx, r.RunID)
	if again.Version != version {
		t.Fatal("idempotent replay changed roster version")
	}
}
func TestReconcileRotatesPastPersistentFailure(t *testing.T) {
	d := team()
	d.Slots[1] = teams.Slot{Name: "engineer", Definition: mesh.DefinitionRef{ID: "worker", Revision: "r1"}, Resolution: teams.Fresh, Activation: teams.Singleton, Min: 1, Max: 1}
	h := memory.New()
	h.SpawnCapabilities["owner"] = true
	if err := putDefinition(h, d); err != nil {
		t.Fatal(err)
	}
	h.Before = func(op, key string) error {
		if op == "routing" {
			return errors.New("offline")
		}
		return nil
	}
	l := launcher(h)
	for _, key := range []string{"a", "b", "c"} {
		if _, err := l.Launch(ctx, teams.LaunchRequest{Key: key, TeamID: d.ID, Version: 1}); err == nil {
			t.Fatal("routing fault not hit")
		}
	}
	h.Before = func(op, key string) error {
		if op == "routing" && key == "run-a" {
			return errors.New("persistent failure")
		}
		return nil
	}
	cursor := ""
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		report, err := l.Reconcile(ctx, cursor, 1)
		if err != nil {
			t.Fatal(err)
		}
		cursor = report.Next
		seen[cursor] = true
	}
	for _, key := range []string{"b", "c"} {
		record, err := h.GetLaunch(ctx, key)
		if err != nil || record.State != teams.RoutingReady {
			t.Fatal("later launch not repaired", key, record.State, err)
		}
	}
	broken, _ := h.GetLaunch(ctx, "a")
	if broken.State != teams.MembersReady {
		t.Fatal("persistent failure unexpectedly completed")
	}
	if !seen["a"] || !seen["b"] || !seen["c"] {
		t.Fatal("persistent failure starved later records", seen)
	}
}
func TestSpawnLimitsAuthorityTrustAndConcurrentCap(t *testing.T) {
	d, h, _, run, r := launch(t)
	owner := slotMember(t, r, "owner")
	effective, err := teams.ResolveLimits(mesh.Limits{MaxChildren: 2}, mesh.Limits{MaxDepth: 2}, limits())
	if err != nil || effective.MaxDepth != 2 || effective.MaxChildren != 2 || effective.Timeout != time.Minute {
		t.Fatal(effective, err)
	}
	if _, err = teams.ResolveLimits(mesh.Limits{}, mesh.Limits{}, mesh.Limits{}); err == nil {
		t.Fatal("unlimited spawn accepted")
	}
	if err = h.Mutate(ctx, run.ID, func(r *teams.Roster) error {
		for i := range r.Members {
			if r.Members[i].ID == owner.ID {
				r.Members[i].Budget = 20
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h.Trust = teams.TrustApproval
	if _, err = teams.Spawn(ctx, d, h, h, h, h, run.ID, owner.Actor, "engineer", "approval", mesh.Limits{Budget: 1}, limits()); !errors.Is(err, teams.ErrDenied) || len(h.Approvals) != 1 {
		t.Fatal("trust approval hook bypassed", err)
	}
	h.Trust = teams.TrustAllow
	var wg sync.WaitGroup
	out := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := teams.Spawn(ctx, d, h, h, h, h, run.ID, owner.Actor, "engineer", string(rune('a'+i)), mesh.Limits{Budget: 1}, limits())
			out <- err
		}(i)
	}
	wg.Wait()
	close(out)
	success := 0
	for err := range out {
		if err == nil {
			success++
		}
	}
	if success != 2 {
		t.Fatal("concurrent slot cap not enforced", success)
	}
	r, _ = h.Snapshot(ctx, run.ID)
	var spawned teams.Member
	for _, m := range r.Members {
		if m.Parent == owner.ID {
			spawned = m
			break
		}
	}
	d.Slots[1].Max = 5
	if _, err = teams.Spawn(ctx, d, h, h, h, h, run.ID, spawned.Actor, "engineer", "unauthorized", mesh.Limits{Budget: 1}, limits()); !errors.Is(err, teams.ErrDenied) {
		t.Fatal("lineage granted spawn", err)
	}
	parent := owner
	parent.Budget = 20
	parent.SpawnCapable = false
	if err = teams.CheckSpawn(d, r, parent, d.Slots[1], limits()); !errors.Is(err, teams.ErrDenied) {
		t.Fatal("missing capability accepted")
	}
	parent.SpawnCapable = true
	for name, l := range map[string]mesh.Limits{"fanout": {MaxDepth: 3, MaxChildren: 8, FanOut: 1, Budget: 1, Timeout: time.Minute}, "children": {MaxDepth: 3, MaxChildren: 1, FanOut: 8, Budget: 1, Timeout: time.Minute}, "budget": {MaxDepth: 3, MaxChildren: 8, FanOut: 8, Budget: 100, Timeout: time.Minute}} {
		boundedParent := parent
		boundedParent.Limits.MaxChildren = l.MaxChildren
		boundedParent.Limits.FanOut = l.FanOut
		if err := teams.CheckSpawn(d, r, boundedParent, d.Slots[1], l); err == nil {
			t.Fatal(name + " limit bypassed")
		}
	}
}
func TestSpawnRecoversLostAckAndRejectsChangedKey(t *testing.T) {
	d, h, _, run, r := launch(t)
	owner := slotMember(t, r, "owner")
	failed := false
	h.After = func(op, key string) error {
		if op == "provision" && !failed {
			failed = true
			return errors.New("lost ack")
		}
		return nil
	}
	req := mesh.Limits{Budget: 1}
	if _, err := teams.Spawn(ctx, d, h, h, h, h, run.ID, owner.Actor, "engineer", "recover", req, limits()); err == nil {
		t.Fatal("fault not hit")
	}
	h.After = nil
	m, err := teams.Spawn(ctx, d, h, h, h, h, run.ID, owner.Actor, "engineer", "recover", req, limits())
	if err != nil {
		t.Fatal(err)
	}
	replay, err := teams.Spawn(ctx, d, h, h, h, h, run.ID, owner.Actor, "engineer", "recover", req, limits())
	if err != nil || m.ID != replay.ID {
		t.Fatal("spawn duplicated", err)
	}
	req.Timeout = 30 * time.Second
	if _, err = teams.Spawn(ctx, d, h, h, h, h, run.ID, owner.Actor, "engineer", "recover", req, limits()); !errors.Is(err, teams.ErrConflict) {
		t.Fatal("changed spawn key accepted", err)
	}
}
func TestMemberMinimumCascadeAndRunEnd(t *testing.T) {
	d, h, _, run, r := launch(t)
	owner := slotMember(t, r, "owner")
	engineer := slotMember(t, r, "engineer")
	if err := teams.RemoveMember(ctx, d, h, h, run.ID, owner.Actor, engineer.ID); err == nil {
		t.Fatal("removed member below min")
	}
	child, err := teams.Spawn(ctx, d, h, h, h, h, run.ID, owner.Actor, "engineer", "child", mesh.Limits{Budget: 1}, limits())
	if err != nil {
		t.Fatal(err)
	}
	if err = teams.RemoveMember(ctx, d, h, h, run.ID, owner.Actor, child.ID); err != nil {
		t.Fatal(err)
	}
	r, _ = h.Snapshot(ctx, run.ID)
	plan, err := teams.Cascade(r, owner.ID, true)
	if err != nil || len(plan) != 2 || plan[0].ID != child.ID || plan[1].ID != owner.ID {
		t.Fatal("cascade not post-order", plan, err)
	}
	if err = teams.CancelMembers(ctx, d, h, h, run.ID, owner.Actor, engineer.ID, true); err != nil {
		t.Fatal(err)
	}
	r, _ = h.Snapshot(ctx, run.ID)
	seen := []string{}
	h.Before = func(op, key string) error {
		if op == "retired" || op == "released" {
			seen = append(seen, op+":"+key)
		}
		return nil
	}
	if err = teams.EndRun(ctx, run.ID, h, h, h); err != nil {
		t.Fatal(err)
	}
	released := false
	for _, s := range seen {
		if strings.HasPrefix(s, "released:") {
			released = true
		}
	}
	if !released {
		t.Fatal("pool identities were killed at end")
	}
	cycle := teams.Roster{Members: []teams.Member{{ID: "a", Parent: "b"}, {ID: "b", Parent: "a"}}}
	if _, err = teams.Cascade(cycle, "a", true); err == nil {
		t.Fatal("cycle ignored")
	}
}
func TestPhaseClosureRaceAndAuthorization(t *testing.T) {
	d, h, _, run, r := launch(t)
	owner := slotMember(t, r, "owner")
	engineer := slotMember(t, r, "engineer")
	p := d.Phases[0]
	h.TriggerFired = true
	if _, err := teams.Signal(ctx, d, r, p, engineer.Actor, h, h); !errors.Is(err, teams.ErrDenied) {
		t.Fatal("unauthorized signal")
	}
	// Auto allows either engineer only after an explicit may_signal_phase grant.
	d.Authority.Grants = append(d.Authority.Grants, teams.Grant{FromSlot: "engineer", Verb: teams.MaySignalPhase, ToSlot: teams.Self})
	p.ExitTrigger.Auto = true
	d.Phases[0] = p
	var wg sync.WaitGroup
	out := make(chan teams.SignalResolution, 3)
	for _, m := range r.Members {
		if m.Slot != "engineer" && m.ID != owner.ID {
			continue
		}
		wg.Add(1)
		go func(m teams.Member) {
			defer wg.Done()
			_, err := teams.Signal(ctx, d, r, p, m.Actor, h, h)
			if err != nil {
				t.Error(err)
				return
			}
			result, err := teams.Advance(ctx, d, r, p, m.Actor, h)
			if err != nil {
				t.Error(err)
			}
			out <- result
		}(m)
	}
	wg.Wait()
	close(out)
	var first *teams.SignalResolution
	for result := range out {
		if first == nil {
			first = &result
		} else if !reflect.DeepEqual(*first, result) {
			t.Fatal("phase resolved more than once")
		}
	}
	if first == nil || first.RunID != run.ID || first.RosterVersion != r.Version {
		t.Fatal("missing signal snapshot")
	}
}

func TestPendingSpawnReservesQuotaAndCanBeReconciledOrCanceled(t *testing.T) {
	d, h, _, run, r := launch(t)
	owner := slotMember(t, r, "owner")
	d.Slots[1].Max = 3
	h.After = func(op, key string) error {
		if op == "provision" {
			return errors.New("lost acknowledgement")
		}
		return nil
	}
	if _, err := teams.Spawn(ctx, d, h, h, h, h, run.ID, owner.Actor, "engineer", "pending", mesh.Limits{Budget: 1}, limits()); err == nil {
		t.Fatal("fault not hit")
	}
	r, _ = h.Snapshot(ctx, run.ID)
	var pending teams.Member
	for _, m := range r.Members {
		if m.Status == "provisioning" {
			pending = m
		}
	}
	if pending.Intent == nil {
		t.Fatal("reservation not durable")
	}
	if _, err := teams.Spawn(ctx, d, h, h, h, h, run.ID, owner.Actor, "engineer", "other", mesh.Limits{Budget: 1}, limits()); err == nil {
		t.Fatal("another key spent pending quota")
	}
	h.After = nil
	if err := teams.ReconcileMembers(ctx, h, h, run.ID); err != nil {
		t.Fatal(err)
	}
	r, _ = h.Snapshot(ctx, run.ID)
	for _, m := range r.Members {
		if m.ID == pending.ID && m.Status != "active" {
			t.Fatal("pending provision not recovered")
		}
	}
	// A second pending intent canceled at the host must never be re-provisioned.
	d.Slots[1].Max = 4
	h.Before = func(op, key string) error {
		if op == "provision" {
			return errors.New("before external provision")
		}
		return nil
	}
	if _, err := teams.Spawn(ctx, d, h, h, h, h, run.ID, owner.Actor, "engineer", "cancel-pending", mesh.Limits{Budget: 1}, limits()); err == nil {
		t.Fatal("fault not hit")
	}
	h.Before = nil
	r, _ = h.Snapshot(ctx, run.ID)
	for _, m := range r.Members {
		if m.Status == "provisioning" {
			pending = m
		}
	}
	if err := teams.CancelMembers(ctx, d, h, h, run.ID, owner.Actor, pending.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Provision(ctx, *pending.Intent); !errors.Is(err, teams.ErrDenied) {
		t.Fatal("canceled intent was provisioned", err)
	}
	if err := teams.ReconcileMembers(ctx, h, h, run.ID); err != nil {
		t.Fatal(err)
	}
}

func TestRetainedDeliveryRetryDoesNotResolveANewRoster(t *testing.T) {
	d, h, _, run, r := launch(t)
	owner := slotMember(t, r, "owner")
	router := &teams.Router{Roster: h, Sender: h}
	req := teams.AddressRequest{RunID: run.ID, Actor: owner.Actor, Address: "@engineer*", Body: "same logical send", Verb: mesh.MessageAddress}
	route, err := router.Send(ctx, d, req, "replay")
	if err != nil {
		t.Fatal(err)
	}
	if err = h.Mutate(ctx, run.ID, func(r *teams.Roster) error {
		for i := range r.Members {
			if r.Members[i].Slot == "engineer" {
				r.Members[i].Status = "stopped"
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = router.SendResolved(ctx, d, req, route, "replay"); err != nil {
		t.Fatal(err)
	}
	if len(h.Deliveries()) != 2 {
		t.Fatal("retry duplicated deliveries")
	}
	req.Body = "changed"
	if err = router.SendResolved(ctx, d, req, route, "replay"); !errors.Is(err, teams.ErrConflict) {
		t.Fatal("changed retry accepted", err)
	}
}
func TestSpawnDepthAndMissingAncestors(t *testing.T) {
	d, _, _, _, r := launch(t)
	owner := slotMember(t, r, "owner")
	ancestor := teams.Member{ID: "root", Status: "active"}
	owner.Parent = ancestor.ID
	owner.Budget = 100
	r.Members = append(r.Members, ancestor)
	l := limits()
	l.MaxDepth = 1
	owner.Limits.MaxDepth = 1
	l.Budget = 1
	if err := teams.CheckSpawn(d, r, owner, d.Slots[1], l); err == nil {
		t.Fatal("depth limit bypassed")
	}
	l.MaxDepth = 3
	owner.Limits.MaxDepth = 3
	owner.Parent = "missing"
	if err := teams.CheckSpawn(d, r, owner, d.Slots[1], l); err == nil {
		t.Fatal("missing ancestor ignored")
	}
}

func TestRequestCannotWidenPolicyOrInheritedParentCeiling(t *testing.T) {
	for name, request := range map[string]mesh.Limits{
		"children": {MaxChildren: 1000}, "depth": {MaxDepth: 9}, "fanout": {FanOut: 1000}, "budget": {Budget: 1000}, "timeout": {Timeout: time.Hour},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := teams.ResolveLimits(request, limits(), limits()); !errors.Is(err, teams.ErrDenied) {
				t.Fatal("request widened policy", err)
			}
		})
	}
	d, h, _, run, r := launch(t)
	owner := slotMember(t, r, "owner")
	d.Policy.Spawn.MaxChildren = 1
	if _, err := teams.Spawn(ctx, d, h, h, h, h, run.ID, owner.Actor, "engineer", "first-cap", mesh.Limits{Budget: 1}, limits()); err != nil {
		t.Fatal(err)
	}
	if _, err := teams.Spawn(ctx, d, h, h, h, h, run.ID, owner.Actor, "engineer", "second-cap", mesh.Limits{MaxChildren: 1000, Budget: 1}, limits()); !errors.Is(err, teams.ErrDenied) {
		t.Fatal("request widened team child cap", err)
	}
	r, _ = h.Snapshot(ctx, run.ID)
	var child teams.Member
	for _, m := range r.Members {
		if m.Parent == owner.ID {
			child = m
		}
	}
	if child.Limits.MaxChildren != 1 || child.Limits.Budget != 1 {
		t.Fatal("child lost inherited ceiling")
	}
	if err := h.Mutate(ctx, run.ID, func(r *teams.Roster) error {
		for i := range r.Members {
			if r.Members[i].ID == child.ID {
				r.Members[i].SpawnCapable = true
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	d.Authority.Grants = append(d.Authority.Grants, teams.Grant{FromSlot: "engineer", Verb: teams.MaySpawn, ToSlot: "architect"})
	if _, err := teams.Spawn(ctx, d, h, h, h, h, run.ID, child.Actor, "architect", "grandchild", mesh.Limits{Budget: 2}, limits()); !errors.Is(err, teams.ErrDenied) {
		t.Fatal("grandchild widened parent budget", err)
	}
}
func TestSelfApprovalAlwaysDeniedAndEndedAuthorsMayBeReviewed(t *testing.T) {
	d, h, _, run, r := launch(t)
	reviewer := slotMember(t, r, "reviewer")
	worker := slotMember(t, r, "engineer")
	p := d.Phases[0]
	p.ApproverSlot = "reviewer"
	d.Authority.Grants = []teams.Grant{{FromSlot: "reviewer", Verb: teams.MayApprove, ToSlot: "engineer"}, {FromSlot: "reviewer", Verb: teams.MayApprove, ToSlot: "reviewer"}}
	if err := teams.CheckApproval(d, r, p, string(reviewer.Actor), string(worker.Actor), string(reviewer.Actor)); !errors.Is(err, teams.ErrDenied) {
		t.Fatal("self author approved without negative grant", err)
	}
	if err := teams.CheckApproval(d, r, p, string(reviewer.Actor), string(reviewer.Actor), string(reviewer.Actor)); !errors.Is(err, teams.ErrDenied) {
		t.Fatal("self target approved", err)
	}
	if err := h.Mutate(ctx, run.ID, func(r *teams.Roster) error {
		for i := range r.Members {
			if r.Members[i].ID == worker.ID {
				r.Members[i].Status = "released"
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r, _ = h.Snapshot(ctx, run.ID)
	if err := teams.CheckApproval(d, r, p, string(reviewer.Actor), string(worker.Actor), string(worker.Actor)); err != nil {
		t.Fatal("ended author cannot be reviewed", err)
	}
	if err := teams.CheckApproval(d, r, p, string(reviewer.Actor), string(worker.Actor), "msg://agent/local/unknown"); !errors.Is(err, teams.ErrDenied) {
		t.Fatal("unknown author accepted", err)
	}
}
func TestDevOpenIsSlotWideButNeverGrantsAdministration(t *testing.T) {
	d, h, _, run, r := launch(t)
	owner := slotMember(t, r, "owner")
	worker := slotMember(t, r, "engineer")
	d.Authority = teams.Authority{}
	if err := teams.Authorize(d, worker, owner, teams.MayMessage, ""); err != nil {
		t.Fatal(err)
	}
	d.Authority.Grants = []teams.Grant{{FromSlot: "engineer", Verb: teams.MayMessage, ToSlot: "owner"}}
	if err := teams.Authorize(d, worker, owner, teams.MayDelegate, ""); !errors.Is(err, teams.ErrDenied) {
		t.Fatal("governed slot got unspecified verb", err)
	}
	d.Authority = teams.Authority{}
	if err := teams.CancelMembers(ctx, d, h, h, run.ID, worker.Actor, owner.ID, true); !errors.Is(err, teams.ErrDenied) {
		t.Fatal("dev-open granted roster administration", err)
	}
	if err := teams.RemoveMember(ctx, d, h, h, run.ID, worker.Actor, owner.ID); !errors.Is(err, teams.ErrDenied) {
		t.Fatal("unauthorized removal not denied", err)
	}
	if err := h.Mutate(ctx, run.ID, func(r *teams.Roster) error {
		for i := range r.Members {
			if r.Members[i].ID == worker.ID {
				r.Members[i].Governance = teams.Admin
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := teams.CancelMembers(ctx, d, h, h, run.ID, worker.Actor, owner.ID, false); err != nil {
		t.Fatal("run admin cannot govern roster", err)
	}
}
func TestMembersSignalButOnlyOwnerAdvancesNonAutoPhase(t *testing.T) {
	d, h, _, _, r := launch(t)
	owner := slotMember(t, r, "owner")
	worker := slotMember(t, r, "engineer")
	p := d.Phases[0]
	h.TriggerFired = true
	// Give the worker its signal permission so owner gating is tested alone.
	d.Authority.Grants = append(d.Authority.Grants, teams.Grant{FromSlot: "engineer", Verb: teams.MaySignalPhase, ToSlot: teams.Self})
	if _, err := teams.Signal(ctx, d, r, p, worker.Actor, h, h); err != nil {
		t.Fatal("permitted worker could not signal", err)
	}
	if _, err := h.GetSignal(ctx, r.RunID, p.ID); !errors.Is(err, teams.ErrNotFound) {
		t.Fatal("signal closed phase", err)
	}
	if _, err := teams.Advance(ctx, d, r, p, worker.Actor, h); !errors.Is(err, teams.ErrDenied) {
		t.Fatal("worker advanced owner phase", err)
	}
	result, err := teams.Advance(ctx, d, r, p, owner.Actor, h)
	if err != nil || result.Actor != owner.Actor || result.Signaler != worker.Actor {
		t.Fatal("owner did not advance worker signal", result, err)
	}
	// Independently isolate the permission gate: the owner slot alone is insufficient.
	d.Authority.Grants = nil
	if _, err := teams.Signal(ctx, d, r, p, owner.Actor, h, h); !errors.Is(err, teams.ErrDenied) {
		t.Fatal("owner slot bypassed signal grant", err)
	}
}
func TestCallerBuiltRouteCannotForgeAuthorityOrSnapshot(t *testing.T) {
	d, h, _, run, r := launch(t)
	reviewer := slotMember(t, r, "reviewer")
	worker := slotMember(t, r, "engineer")
	router := &teams.Router{Roster: h, Sender: h}
	req := teams.AddressRequest{RunID: run.ID, Actor: reviewer.Actor, Address: "@engineer", Body: "forged", Verb: mesh.MessageAddress}
	if _, err := router.Resolve(ctx, d, req); !errors.Is(err, teams.ErrDenied) {
		t.Fatal("fixture unexpectedly authorized")
	}
	claimed := reviewer
	claimed.Slot = "owner"
	route := teams.Route{RunID: run.ID, RosterVersion: r.Version, TeamID: d.ID, TeamVersion: d.Version, Sender: claimed, Address: req.Address, Rule: "explicit", Verb: req.Verb, Recipients: []teams.Member{worker}}
	if err := router.SendResolved(ctx, d, req, route, "forged"); !errors.Is(err, teams.ErrDenied) {
		t.Fatal("caller built route delivered", err)
	}
	owner := slotMember(t, r, "owner")
	req.Actor = owner.Actor
	route.Sender = owner
	route.RosterVersion = 9999
	if err := router.SendResolved(ctx, d, req, route, "fake-version"); !errors.Is(err, teams.ErrNotFound) {
		t.Fatal("unknown retained snapshot accepted", err)
	}
	if len(h.Deliveries()) != 0 {
		t.Fatal("forged route had transport effects")
	}
}

type keyOnlySender struct {
	*memory.Host
	mu   sync.Mutex
	keys map[string]bool
	body string
}

func (s *keyOnlySender) SendMessage(ctx context.Context, d teams.Delivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keys[d.IdempotencyKey] {
		return nil
	}
	s.keys[d.IdempotencyKey] = true
	s.body = d.Body
	return nil
}
func TestChangedSendBodyRejectedBeforeKeyOnlyTransport(t *testing.T) {
	d, h, _, run, r := launch(t)
	owner := slotMember(t, r, "owner")
	sender := &keyOnlySender{Host: h, keys: map[string]bool{}}
	router := &teams.Router{Roster: h, Sender: sender}
	req := teams.AddressRequest{RunID: run.ID, Actor: owner.Actor, Address: "@engineer", Body: "original", Verb: mesh.MessageAddress}
	route, err := router.Send(ctx, d, req, "same")
	if err != nil {
		t.Fatal(err)
	}
	req.Body = "changed"
	if err = router.SendResolved(ctx, d, req, route, "same"); !errors.Is(err, teams.ErrConflict) {
		t.Fatal("key-only host hid changed body", err)
	}
	if sender.body != "original" {
		t.Fatal("changed body reached transport")
	}
}
func TestDuplicateDurableIdentityRejectedBeforeProvisioning(t *testing.T) {
	d := team()
	duplicate := d.Slots[3]
	duplicate.Name = "other-architect"
	d.Slots = append(d.Slots, duplicate)
	if err := teams.Validate(d); err == nil {
		t.Fatal("duplicate durable identity accepted")
	}
	bad := team()
	bad.Slots[3].Identity = "https://example.com/actor"
	if err := teams.Validate(bad); err == nil {
		t.Fatal("nonactor durable identity accepted")
	}
}

type callbackTrust struct {
	resolve func(context.Context, teams.Member, teams.Slot) (teams.TrustDecision, error)
}

func (t callbackTrust) ResolveTrust(ctx context.Context, m teams.Member, s teams.Slot) (teams.TrustDecision, error) {
	return t.resolve(ctx, m, s)
}
func TestTrustAndLazyCanReenterRosterAndRouter(t *testing.T) {
	d, h, _, run, r := launch(t)
	owner := slotMember(t, r, "owner")
	router := &teams.Router{Roster: h, Sender: h}
	called := false
	trust := callbackTrust{resolve: func(ctx context.Context, m teams.Member, s teams.Slot) (teams.TrustDecision, error) {
		if _, err := h.Snapshot(ctx, run.ID); err != nil {
			return "", err
		}
		_, err := router.Resolve(ctx, d, teams.AddressRequest{RunID: run.ID, Actor: owner.Actor, Address: "@engineer", Verb: mesh.MessageAddress})
		called = true
		return teams.TrustAllow, err
	}}
	router.Lazy = &teams.LazyProvisioning{Provisioner: h, Trust: trust, Approvals: h, Defaults: limits(), Limits: mesh.Limits{Budget: 1}}
	done := make(chan error, 1)
	go func() {
		_, err := router.Resolve(ctx, d, teams.AddressRequest{RunID: run.ID, Actor: owner.Actor, Address: "@architect", Verb: mesh.MessageAddress})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("host callback deadlocked under a roster/router lock")
	}
	if !called {
		t.Fatal("lazy trust hook not exercised")
	}
}
func TestUnauthorizedLazyDoesNotReachProvisioning(t *testing.T) {
	d, h, _, run, r := launch(t)
	worker := slotMember(t, r, "engineer")
	h.Before = func(op, key string) error {
		if op == "provision" || op == "trust" {
			t.Error("unauthorized lazy reached a host callback")
		}
		return nil
	}
	router := &teams.Router{Roster: h, Sender: h, Lazy: &teams.LazyProvisioning{Provisioner: h, Trust: h, Approvals: h, Defaults: limits(), Limits: mesh.Limits{Budget: 1}}}
	if _, err := router.Resolve(ctx, d, teams.AddressRequest{RunID: run.ID, Actor: worker.Actor, Address: "@architect", Verb: mesh.MessageAddress}); !errors.Is(err, teams.ErrDenied) {
		t.Fatal("unauthorized lazy not denied", err)
	}
}
func TestFailedTerminationIsNonRoutableAndReconciled(t *testing.T) {
	d, h, _, run, r := launch(t)
	owner := slotMember(t, r, "owner")
	if err := h.Mutate(ctx, run.ID, func(r *teams.Roster) error {
		for i := range r.Members {
			if r.Members[i].ID == owner.ID {
				r.Members[i].Governance = teams.Owner
			} else {
				r.Members[i].Parent = owner.ID
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stoppedOne := false
	h.Before = func(op, key string) error {
		if op == "released" || op == "retired" {
			snapshot, err := h.Snapshot(ctx, run.ID)
			if err != nil {
				return err
			}
			for _, m := range snapshot.Members {
				if m.ID == key && (m.Status == "active" || m.Status == "provisioning") {
					t.Error("external termination ran before routing was fenced")
				}
			}
			if stoppedOne {
				return errors.New("termination transport offline")
			}
			stoppedOne = true
		}
		return nil
	}
	if err := teams.CancelMembers(ctx, d, h, h, run.ID, owner.Actor, owner.ID, true); err == nil {
		t.Fatal("termination fault not hit")
	}
	r, _ = h.Snapshot(ctx, run.ID)
	for _, m := range r.Members {
		if m.Status == "active" || m.Status == "provisioning" {
			t.Fatal("failed termination left a routable member")
		}
	}
	h.Before = nil
	if err := teams.ReconcileMembers(ctx, h, h, run.ID); err != nil {
		t.Fatal(err)
	}
	r, _ = h.Snapshot(ctx, run.ID)
	for _, m := range r.Members {
		expected := "stopped"
		if m.Resolution != teams.Fresh {
			expected = "released"
		}
		if m.Status != expected {
			t.Fatal("termination not repaired", m.Status, expected)
		}
	}
}

type pausedProvisioner struct {
	*memory.Host
	started        chan struct{}
	resume         chan struct{}
	cleanedSession string
}

func (p *pausedProvisioner) Provision(ctx context.Context, req teams.ProvisionRequest) (teams.Member, error) {
	m, err := p.Host.Provision(ctx, req)
	if err != nil {
		return m, err
	}
	close(p.started)
	select {
	case <-p.resume:
	case <-ctx.Done():
		return teams.Member{}, ctx.Err()
	}
	return m, nil
}
func (p *pausedProvisioner) Release(ctx context.Context, key string, m teams.Member) error {
	if m.Status == "active" {
		p.cleanedSession = m.SessionID
	}
	return p.Host.Release(ctx, key, m)
}
func TestCancelAndEndRunRaceWithProvisionCompensatesLiveSession(t *testing.T) {
	for _, end := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "end-run"}[end], func(t *testing.T) {
			d, h, _, run, r := launch(t)
			owner := slotMember(t, r, "owner")
			p := &pausedProvisioner{Host: h, started: make(chan struct{}), resume: make(chan struct{})}
			done := make(chan error, 1)
			go func() {
				_, err := teams.Spawn(ctx, d, h, p, h, h, run.ID, owner.Actor, "engineer", "racing", mesh.Limits{Budget: 1}, limits())
				done <- err
			}()
			<-p.started
			r, _ = h.Snapshot(ctx, run.ID)
			var reserved teams.Member
			for _, m := range r.Members {
				if m.Status == "provisioning" {
					reserved = m
				}
			}
			var err error
			if end {
				err = teams.EndRun(ctx, run.ID, h, p, h)
			} else {
				err = teams.CancelMembers(ctx, d, h, p, run.ID, owner.Actor, reserved.ID, false)
			}
			if err != nil {
				t.Fatal(err)
			}
			close(p.resume)
			if err = <-done; !errors.Is(err, teams.ErrConflict) {
				t.Fatal("racing commit was not fenced", err)
			}
			if p.cleanedSession == "" {
				t.Fatal("live session from lost acknowledgement was not compensated")
			}
			r, _ = h.Snapshot(ctx, run.ID)
			for _, m := range r.Members {
				if m.ID == reserved.ID && m.Status != "released" {
					t.Fatal("canceled member became active")
				}
			}
		})
	}
}
func TestTerminalProvisionFailureFreesQuotaAndReplayConflicts(t *testing.T) {
	d, h, _, run, r := launch(t)
	owner := slotMember(t, r, "owner")
	d.Policy.Spawn.MaxChildren = 1
	h.Before = func(op, key string) error {
		if op == "provision" {
			return teams.ErrProvisionFailed
		}
		return nil
	}
	if _, err := teams.Spawn(ctx, d, h, h, h, h, run.ID, owner.Actor, "engineer", "permanent", mesh.Limits{Budget: 1}, limits()); !errors.Is(err, teams.ErrProvisionFailed) {
		t.Fatal("terminal error not surfaced", err)
	}
	r, _ = h.Snapshot(ctx, run.ID)
	found := false
	for _, m := range r.Members {
		if m.Status == "failed" {
			found = true
		}
	}
	if !found {
		t.Fatal("failed intent not terminal")
	}
	h.Before = nil
	if _, err := teams.Spawn(ctx, d, h, h, h, h, run.ID, owner.Actor, "engineer", "permanent", mesh.Limits{Budget: 1}, limits()); !errors.Is(err, teams.ErrConflict) {
		t.Fatal("failed key replayed as success", err)
	}
	if _, err := teams.Spawn(ctx, d, h, h, h, h, run.ID, owner.Actor, "engineer", "replacement", mesh.Limits{Budget: 1}, limits()); err != nil {
		t.Fatal("failed reservation retained quota", err)
	}
	if err := teams.ReconcileMembers(ctx, h, h, run.ID); err != nil {
		t.Fatal("terminal intent retried forever", err)
	}
}
func TestStoppedSpawnReplayReturnsConflict(t *testing.T) {
	d, h, _, run, r := launch(t)
	owner := slotMember(t, r, "owner")
	child, err := teams.Spawn(ctx, d, h, h, h, h, run.ID, owner.Actor, "engineer", "ended", mesh.Limits{Budget: 1}, limits())
	if err != nil {
		t.Fatal(err)
	}
	if err = teams.RemoveMember(ctx, d, h, h, run.ID, owner.Actor, child.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = teams.Spawn(ctx, d, h, h, h, h, run.ID, owner.Actor, "engineer", "ended", mesh.Limits{Budget: 1}, limits()); !errors.Is(err, teams.ErrConflict) {
		t.Fatal("ended spawn replay succeeded", err)
	}
}

func TestRetainedSendMayCompleteAfterSenderRemoval(t *testing.T) {
	d, h, _, run, r := launch(t)
	owner := slotMember(t, r, "owner")
	router := &teams.Router{Roster: h, Sender: h}
	req := teams.AddressRequest{RunID: run.ID, Actor: owner.Actor, Address: "@engineer", Body: "in flight", Verb: mesh.MessageAddress}
	plan, err := router.Resolve(ctx, d, req)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.Mutate(ctx, run.ID, func(r *teams.Roster) error {
		for i := range r.Members {
			if r.Members[i].ID == owner.ID {
				r.Members[i].Status = "stopped"
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = router.Resolve(ctx, d, req); !errors.Is(err, teams.ErrDenied) {
		t.Fatal("removed sender started a new send", err)
	}
	if err = router.SendResolved(ctx, d, req, plan, "authorized-before-removal"); err != nil {
		t.Fatal("authorized in-flight send was discarded", err)
	}
}

func TestUnspecifiedChildBudgetUsesRemainingAllocationAndCanReplay(t *testing.T) {
	d, h, _, run, r := launch(t)
	owner := slotMember(t, r, "owner")
	first, err := teams.Spawn(ctx, d, h, h, h, h, run.ID, owner.Actor, "engineer", "allocated", mesh.Limits{Budget: 1}, limits())
	if err != nil {
		t.Fatal(err)
	}
	child, err := teams.Spawn(ctx, d, h, h, h, h, run.ID, owner.Actor, "engineer", "remaining", mesh.Limits{}, limits())
	if err != nil {
		t.Fatal(err)
	}
	if child.Budget != owner.Budget-first.Budget || child.Limits.Budget != child.Budget {
		t.Fatal("child did not inherit remaining allocation")
	}
	replay, err := teams.Spawn(ctx, d, h, h, h, h, run.ID, owner.Actor, "engineer", "remaining", mesh.Limits{}, limits())
	if err != nil || replay.ID != child.ID {
		t.Fatal("fully allocated parent prevented idempotent replay", err)
	}
}

func TestTightChildSubtreeLimitsDoNotConsumeParentCeiling(t *testing.T) {
	d, h, _, run, r := launch(t)
	owner := slotMember(t, r, "owner")
	childLimits := mesh.Limits{FanOut: 1, MaxChildren: 1, Budget: 1}
	first, err := teams.Spawn(ctx, d, h, h, h, h, run.ID, owner.Actor, "engineer", "tight-a", childLimits, limits())
	if err != nil {
		t.Fatal(err)
	}
	second, err := teams.Spawn(ctx, d, h, h, h, h, run.ID, owner.Actor, "engineer", "tight-b", childLimits, limits())
	if err != nil {
		t.Fatal("child subtree limit incorrectly blocked its sibling", err)
	}
	if first.Limits.FanOut != 1 || second.Limits.MaxChildren != 1 {
		t.Fatal("child subtree ceiling was lost")
	}
}

type conflictingProvisioner struct {
	*memory.Host
	actor mesh.URN
}

func (p conflictingProvisioner) Provision(ctx context.Context, req teams.ProvisionRequest) (teams.Member, error) {
	m, err := p.Host.Provision(ctx, req)
	if err != nil {
		return m, err
	}
	m.Actor = p.actor
	return m, nil
}
func TestConflictingProvisionedActorIsTerminalAndReleasesQuota(t *testing.T) {
	d, h, _, run, r := launch(t)
	owner := slotMember(t, r, "owner")
	existing := slotMember(t, r, "engineer")
	d.Policy.Spawn.MaxChildren = 1
	p := conflictingProvisioner{Host: h, actor: existing.Actor}
	if _, err := teams.Spawn(ctx, d, h, p, h, h, run.ID, owner.Actor, "engineer", "duplicate-actor", mesh.Limits{Budget: 1}, limits()); !errors.Is(err, teams.ErrProvisionFailed) {
		t.Fatal("conflicting actor not rejected", err)
	}
	r, _ = h.Snapshot(ctx, run.ID)
	failed := false
	for _, m := range r.Members {
		if m.Parent == owner.ID {
			if m.Status != "failed" {
				t.Fatal("compensated conflict still reserves quota", m.Status)
			}
			failed = true
		}
		if m.ID == existing.ID && m.Status != "active" {
			t.Fatal("compensation stopped the original member")
		}
	}
	if !failed {
		t.Fatal("conflicting reservation disappeared instead of becoming terminal")
	}
	if err := teams.ReconcileMembers(ctx, h, p, run.ID); err != nil {
		t.Fatal("tombstoned conflicting intent retried forever", err)
	}
	if _, err := teams.Spawn(ctx, d, h, h, h, h, run.ID, owner.Actor, "engineer", "after-conflict", mesh.Limits{Budget: 1}, limits()); err != nil {
		t.Fatal("compensated conflict retained parent quota", err)
	}
}

type privilegedProvisioner struct{ *memory.Host }

func (p privilegedProvisioner) Provision(ctx context.Context, req teams.ProvisionRequest) (teams.Member, error) {
	m, err := p.Host.Provision(ctx, req)
	m.Governance = teams.Admin
	return m, err
}
func TestProviderCannotGrantGovernanceToLaunchedOrSpawnedMembers(t *testing.T) {
	d := team()
	h := memory.New()
	h.SpawnCapabilities["owner"] = true
	if err := putDefinition(h, d); err != nil {
		t.Fatal(err)
	}
	p := privilegedProvisioner{h}
	l := launcher(h)
	l.Provisioner = p
	run, err := l.Launch(ctx, teams.LaunchRequest{Key: "governance", TeamID: d.ID, Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := h.Snapshot(ctx, run.ID)
	for _, m := range r.Members {
		if m.Governance != teams.MemberRole {
			t.Fatal("provider elevated launched member", m.Governance)
		}
	}
	owner := slotMember(t, r, "owner")
	reviewer := slotMember(t, r, "reviewer")
	child, err := teams.Spawn(ctx, d, h, p, h, h, run.ID, owner.Actor, "engineer", "unprivileged-child", mesh.Limits{Budget: 1}, limits())
	if err != nil {
		t.Fatal(err)
	}
	if child.Governance != teams.MemberRole {
		t.Fatal("provider elevated spawned child", child.Governance)
	}
	if err := teams.CancelMembers(ctx, d, h, p, run.ID, child.Actor, reviewer.ID, false); !errors.Is(err, teams.ErrDenied) {
		t.Fatal("provider-granted administration bypassed authority", err)
	}
}

type recordingTrigger struct{ received teams.Trigger }

func (e *recordingTrigger) Evaluate(ctx context.Context, trigger teams.Trigger, m teams.Member) (bool, error) {
	e.received = trigger
	return true, nil
}
func TestPhaseInputsCannotOverrideAuthoredSignalAdvanceOrApprovalPolicy(t *testing.T) {
	d, h, _, _, r := launch(t)
	owner := slotMember(t, r, "owner")
	worker := slotMember(t, r, "engineer")
	reviewer := slotMember(t, r, "reviewer")
	d.Authority.Grants = append(d.Authority.Grants, teams.Grant{FromSlot: "engineer", Verb: teams.MaySignalPhase, ToSlot: teams.Self}, teams.Grant{FromSlot: "engineer", Verb: teams.MayApprove, ToSlot: "owner"}, teams.Grant{FromSlot: "reviewer", Verb: teams.MaySignalPhase, ToSlot: teams.Self})
	d.Phases[0].ActiveSlots = []string{"owner", "engineer"}
	forged := teams.Phase{ID: d.Phases[0].ID, Kind: "flex", OwnerSlot: "engineer", ApproverSlot: "engineer", ActiveSlots: []string{"reviewer", "engineer"}, ExitTrigger: teams.Trigger{Auto: true, Kind: "always", Spec: map[string]string{"override": "true"}}}
	eval := &recordingTrigger{}
	if _, err := teams.Signal(ctx, d, r, forged, worker.Actor, h, eval); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(eval.received, d.Phases[0].ExitTrigger) {
		t.Fatal("caller supplied trigger reached evaluator")
	}
	if _, err := teams.Signal(ctx, d, r, forged, reviewer.Actor, h, eval); !errors.Is(err, teams.ErrDenied) {
		t.Fatal("caller supplied active slots bypassed authored participation", err)
	}
	if _, err := teams.Advance(ctx, d, r, forged, worker.Actor, h); !errors.Is(err, teams.ErrDenied) {
		t.Fatal("caller enabled Auto or changed owner", err)
	}
	if _, err := teams.Advance(ctx, d, r, forged, owner.Actor, h); err != nil {
		t.Fatal("authored owner could not advance", err)
	}
	if err := teams.CheckApproval(d, r, forged, string(worker.Actor), string(owner.Actor), string(owner.Actor)); !errors.Is(err, teams.ErrDenied) {
		t.Fatal("caller changed approver slot", err)
	}
	if err := teams.CheckApproval(d, r, forged, string(reviewer.Actor), string(worker.Actor), string(worker.Actor)); err != nil {
		t.Fatal("authored approver rejected by caller fields", err)
	}
	forged.ID = "not-authored"
	if _, err := teams.Signal(ctx, d, r, forged, owner.Actor, h, eval); !errors.Is(err, teams.ErrNotFound) {
		t.Fatal("unknown phase signal accepted", err)
	}
	if _, err := teams.Advance(ctx, d, r, forged, owner.Actor, h); !errors.Is(err, teams.ErrNotFound) {
		t.Fatal("unknown phase advance accepted", err)
	}
	if err := teams.CheckApproval(d, r, forged, string(reviewer.Actor), string(worker.Actor), string(worker.Actor)); !errors.Is(err, teams.ErrNotFound) {
		t.Fatal("unknown phase approval accepted", err)
	}
}
func TestSpawnGrantAloneCannotLazilyResolveAMessageTarget(t *testing.T) {
	d, h, _, run, r := launch(t)
	owner := slotMember(t, r, "owner")
	d.Authority.Grants = []teams.Grant{{FromSlot: "owner", Verb: teams.MaySpawn, ToSlot: "architect"}}
	h.Before = func(op, key string) error {
		if op == "provision" {
			t.Error("may_spawn bypassed missing may_message before lazy provisioning")
		}
		return nil
	}
	router := &teams.Router{Roster: h, Sender: h, Lazy: &teams.LazyProvisioning{Provisioner: h, Trust: h, Approvals: h, Defaults: limits(), Limits: mesh.Limits{Budget: 1}}}
	if _, err := router.Resolve(ctx, d, teams.AddressRequest{RunID: run.ID, Actor: owner.Actor, Address: "@architect", Verb: mesh.MessageAddress}); !errors.Is(err, teams.ErrDenied) {
		t.Fatal("lazy message accepted without may_message", err)
	}
}

func TestDefinitionAndIdentityValidation(t *testing.T) {
	for name, change := range map[string]func(*teams.Team){
		"unpinned":         func(d *teams.Team) { d.Slots[0].Definition.Revision = "" },
		"duplicate":        func(d *teams.Team) { d.Slots[1].Identities[1] = d.Slots[1].Identities[0] },
		"capacity":         func(d *teams.Team) { d.Slots[1].Identities = d.Slots[1].Identities[:2] },
		"concurrent fresh": func(d *teams.Team) { d.Slots[0].Activation = teams.Concurrent },
		"cross slot":       func(d *teams.Team) { d.Slots[3].Identity = d.Slots[1].Identities[0] },
	} {
		t.Run(name, func(t *testing.T) {
			d := team()
			change(&d)
			if teams.Validate(d) == nil {
				t.Fatal("invalid identity model accepted")
			}
		})
	}
}
func TestFreshEnrollmentRetiredAndPoolLeaseExclusive(t *testing.T) {
	d, h, _, run, r := launch(t)
	fresh := slotMember(t, r, "owner")
	stable := slotMember(t, r, "engineer")
	if !fresh.Enrolled || !fresh.Ephemeral || !h.Enrolled(fresh.Actor) || stable.Ephemeral {
		t.Fatal("lifecycle attestation incorrect")
	}
	req := teams.ProvisionRequest{IdempotencyKey: "overlap", MemberID: "overlap", Slot: d.Slots[1], Identity: stable.Actor, Limits: limits()}
	if _, err := h.Provision(ctx, req); !errors.Is(err, teams.ErrProvisionFailed) {
		t.Fatal("overlapping pool session permitted", err)
	}
	if err := teams.EndRun(ctx, run.ID, h, h, h); err != nil {
		t.Fatal(err)
	}
	if h.Enrolled(fresh.Actor) || !h.Enrolled(stable.Actor) {
		t.Fatal("fresh retirement or stable continuity incorrect")
	}
}
func TestInitialAbortLostAcknowledgementAndCleanupRetry(t *testing.T) {
	d := team()
	h := memory.New()
	if err := putDefinition(h, d); err != nil {
		t.Fatal(err)
	}
	l := launcher(h)
	l.MaxAttempts = 1
	h.After = func(op, key string) error {
		if op == "provision" {
			return errors.New("lost ack")
		}
		return nil
	}
	h.Before = func(op, key string) error {
		if op == "retired" {
			return errors.New("cleanup offline")
		}
		return nil
	}
	if _, err := l.Launch(ctx, teams.LaunchRequest{Key: "abort", TeamID: d.ID, Version: 1}); !errors.Is(err, teams.ErrLaunchFailed) {
		t.Fatal(err)
	}
	rec, _ := h.GetLaunch(ctx, "abort")
	if rec.State != teams.Aborting || rec.Run.Status != mesh.TaskFailed {
		t.Fatal("failure not journaled", rec.State)
	}
	h.Before = nil
	h.After = nil
	if _, err := l.Reconcile(ctx, "", 10); err != nil {
		t.Fatal(err)
	}
	rec, _ = h.GetLaunch(ctx, "abort")
	if rec.State != teams.Failed {
		t.Fatal("cleanup not terminal", rec.State)
	}
	for _, m := range h.Provisioned() {
		if m.Status == "active" || h.Enrolled(m.Actor) {
			t.Fatal("lost-ack resource leaked", m)
		}
	}
	pending, err := h.Pending(ctx, "", 10)
	if err != nil || len(pending) != 0 {
		t.Fatal("failed launch remains pending", pending, err)
	}
	if _, err := h.Provision(ctx, rec.Intents[0].Request); !errors.Is(err, teams.ErrDenied) {
		t.Fatal("late provision resurrected", err)
	}
	if _, err := h.LaunchWorkflow(ctx, "abort", rec.Definition); !errors.Is(err, teams.ErrLaunchFailed) {
		t.Fatal("late workflow resurrected", err)
	}
}
func TestInitialLaunchRetryAndTimeoutBounded(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(map[bool]string{false: "attempts", true: "timeout"}[timeout], func(t *testing.T) {
			d := team()
			h := memory.New()
			if err := putDefinition(h, d); err != nil {
				t.Fatal(err)
			}
			l := launcher(h)
			h.Before = func(op, key string) error {
				if op == "routing" {
					return errors.New("offline")
				}
				return nil
			}
			_, err := l.Launch(ctx, teams.LaunchRequest{Key: "bounded", TeamID: d.ID, Version: 1})
			if err == nil {
				t.Fatal("fault not hit")
			}
			if timeout {
				h.NowTime = h.NowTime.Add(2 * time.Minute)
			}
			for i := 0; i < 3; i++ {
				if _, err := l.Reconcile(ctx, "", 10); err != nil {
					t.Fatal(err)
				}
			}
			rec, _ := h.GetLaunch(ctx, "bounded")
			if rec.State != teams.Failed || rec.Run.Status != mesh.TaskFailed {
				t.Fatal("retry budget not terminal", rec.State)
			}
			r, _ := h.Snapshot(ctx, rec.Run.ID)
			for _, m := range r.Members {
				if m.Status == "active" {
					t.Fatal("failed run routable")
				}
			}
			for _, m := range h.Provisioned() {
				if m.Status == "active" {
					t.Fatal("failed run resource active")
				}
			}
		})
	}
}
func TestSpawnCeilingsInclusive(t *testing.T) {
	d := team()
	d.Authority = teams.Authority{Mode: teams.DevOpen}
	cap := limits()
	cap.MaxChildren = 2
	cap.MaxDepth = 2
	d.Policy.Spawn = cap
	parent := teams.Member{ID: "parent", Actor: "msg://agent/test/parent", Slot: "owner", Status: "active", SpawnCapable: true, Budget: 100, Limits: cap}
	target := d.Slots[1]
	target.Max = 20
	req := cap
	req.Budget = 1
	r := teams.Roster{Members: []teams.Member{parent, {ID: "one", Parent: parent.ID, Slot: "engineer", Status: "active", Budget: 1}}}
	if err := teams.CheckSpawn(d, r, parent, target, req); err != nil {
		t.Fatal("child at ceiling denied", err)
	}
	r.Members = append(r.Members, teams.Member{ID: "two", Parent: parent.ID, Slot: "engineer", Status: "active", Budget: 1})
	if err := teams.CheckSpawn(d, r, parent, target, req); err == nil {
		t.Fatal("child ceiling+1 accepted")
	}
	root := parent
	root.ID = "root"
	parent.Parent = root.ID
	r.Members = []teams.Member{root, parent}
	if err := teams.CheckSpawn(d, r, parent, target, req); err != nil {
		t.Fatal("depth at ceiling denied", err)
	}
	ancestor := root
	ancestor.ID = "ancestor"
	root.Parent = ancestor.ID
	r.Members = []teams.Member{ancestor, root, parent}
	if err := teams.CheckSpawn(d, r, parent, target, req); err == nil {
		t.Fatal("depth ceiling+1 accepted")
	}
}

type projectionConflictStore struct{ *memory.Host }

func (s projectionConflictStore) Mutate(c context.Context, id string, fn func(*teams.Roster) error) error {
	return s.Host.Mutate(c, id, func(r *teams.Roster) error {
		pending := map[string]bool{}
		for _, m := range r.Members {
			pending[m.ID] = m.Status == "provisioning"
		}
		if err := fn(r); err != nil {
			return err
		}
		for _, m := range r.Members {
			if pending[m.ID] && m.Status == "active" {
				return teams.ErrConflict
			}
		}
		return nil
	})
}
func TestProjectionConflictReleasesReservation(t *testing.T) {
	d, h, _, run, r := launch(t)
	owner := slotMember(t, r, "owner")
	d.Policy.Spawn.MaxChildren = 1
	store := projectionConflictStore{h}
	if _, err := teams.Spawn(ctx, d, store, h, h, h, run.ID, owner.Actor, "engineer", "projection-conflict", mesh.Limits{Budget: 1}, limits()); !errors.Is(err, teams.ErrConflict) {
		t.Fatal(err)
	}
	r, _ = h.Snapshot(ctx, run.ID)
	found := false
	for _, m := range r.Members {
		if m.Parent == owner.ID {
			found = true
			if m.Status != "failed" {
				t.Fatal("conflict retained quota", m.Status)
			}
		}
	}
	if !found {
		t.Fatal("missing terminal reservation")
	}
	if _, err := teams.Spawn(ctx, d, h, h, h, h, run.ID, owner.Actor, "engineer", "replacement", mesh.Limits{Budget: 1}, limits()); err != nil {
		t.Fatal("quota was not released", err)
	}
}

type unenrolledProvisioner struct{ *memory.Host }

func (p unenrolledProvisioner) Provision(c context.Context, req teams.ProvisionRequest) (teams.Member, error) {
	m, err := p.Host.Provision(c, req)
	m.Enrolled = false
	return m, err
}
func TestInitialInvalidEnrollmentAborts(t *testing.T) {
	d := team()
	h := memory.New()
	if err := putDefinition(h, d); err != nil {
		t.Fatal(err)
	}
	l := launcher(h)
	l.Provisioner = unenrolledProvisioner{h}
	if _, err := l.Launch(ctx, teams.LaunchRequest{Key: "invalid", TeamID: d.ID, Version: 1}); !errors.Is(err, teams.ErrLaunchFailed) {
		t.Fatal("unenrolled actor accepted", err)
	}
	rec, _ := h.GetLaunch(ctx, "invalid")
	if rec.State != teams.Failed {
		t.Fatal("invalid actor wedged launch", rec.State)
	}
	for _, m := range h.Provisioned() {
		if m.Status == "active" || h.Enrolled(m.Actor) {
			t.Fatal("invalid result leaked resource")
		}
	}
}

func TestLaunchContextInterruptionIsRetryable(t *testing.T) {
	for _, fault := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(fault.Error(), func(t *testing.T) {
			d := team()
			h := memory.New()
			if err := putDefinition(h, d); err != nil {
				t.Fatal(err)
			}
			l := launcher(h)
			calls := 0
			h.Before = func(op, key string) error {
				if op == "provision" {
					calls++
					if calls == 2 {
						return fault
					}
				}
				return nil
			}
			if _, err := l.Launch(ctx, teams.LaunchRequest{Key: "interrupted", TeamID: d.ID, Version: 1}); !errors.Is(err, fault) {
				t.Fatal(err)
			}
			rec, _ := h.GetLaunch(ctx, "interrupted")
			if rec.State != teams.Planning || rec.Attempts != 1 {
				t.Fatal("context interruption aborted launch", rec.State)
			}
			for _, m := range h.Provisioned() {
				if m.Status != "active" {
					t.Fatal("interruption cleaned member")
				}
			}
			h.Before = nil
			if _, err := l.Launch(ctx, teams.LaunchRequest{Key: "interrupted", TeamID: d.ID, Version: 1}); err != nil {
				t.Fatal("retry failed", err)
			}
		})
	}
}
func TestAbortFencesAndTerminatesSpawnedMembers(t *testing.T) {
	d := team()
	h := memory.New()
	h.SpawnCapabilities["owner"] = true
	if err := putDefinition(h, d); err != nil {
		t.Fatal(err)
	}
	l := launcher(h)
	h.Before = func(op, key string) error {
		if op == "routing" {
			return errors.New("offline")
		}
		return nil
	}
	run, err := l.Launch(ctx, teams.LaunchRequest{Key: "descendants", TeamID: d.ID, Version: 1})
	if err == nil {
		t.Fatal("fault not hit")
	}
	r, _ := h.Snapshot(ctx, run.ID)
	owner := slotMember(t, r, "owner")
	child, err := teams.Spawn(ctx, d, h, h, h, h, run.ID, owner.Actor, "engineer", "during-launch", mesh.Limits{Budget: 1}, limits())
	if err != nil {
		t.Fatal(err)
	}
	h.Before = func(op, key string) error {
		if op == "retired" || op == "released" {
			snapshot, e := h.Snapshot(ctx, run.ID)
			if e != nil {
				t.Fatal(e)
			}
			for _, m := range snapshot.Members {
				if m.Status == "active" || m.Status == "provisioning" {
					t.Fatal("cleanup before full non-routable fence", m.ID)
				}
			}
		}
		return nil
	}
	h.NowTime = h.NowTime.Add(2 * time.Minute)
	if _, err := l.Reconcile(ctx, "", 10); err != nil {
		t.Fatal(err)
	}
	r, _ = h.Snapshot(ctx, run.ID)
	found := false
	for _, m := range r.Members {
		if m.Status != "stopped" && m.Status != "released" {
			t.Fatal("abort left member nonterminal", m.ID, m.Status)
		}
		if m.ID == child.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("spawned member missing")
	}
	for _, m := range h.Provisioned() {
		if m.Status == "active" {
			t.Fatal("abort left active resource", m.ID)
		}
	}
}

type stubCheckingProvisioner struct {
	*memory.Host
	t *testing.T
}

func (p stubCheckingProvisioner) Release(c context.Context, key string, m teams.Member) error {
	acquired := false
	for _, actual := range p.Host.Provisioned() {
		if actual.ID == m.ID {
			acquired = true
		}
	}
	if !acquired && m.Actor != "" {
		p.t.Fatal("never-acquired stub named an actor", m.Actor)
	}
	return p.Host.Release(c, key, m)
}
func TestAbortUnacquiredIntentStubHasNoActor(t *testing.T) {
	d := team()
	h := memory.New()
	if err := putDefinition(h, d); err != nil {
		t.Fatal(err)
	}
	l := launcher(h)
	l.Provisioner = stubCheckingProvisioner{h, t}
	h.Before = func(op, key string) error {
		if op == "provision" {
			return teams.ErrProvisionFailed
		}
		return nil
	}
	if _, err := l.Launch(ctx, teams.LaunchRequest{Key: "unacquired", TeamID: d.ID, Version: 1, Counts: map[string]int{"architect": 1}}); !errors.Is(err, teams.ErrLaunchFailed) {
		t.Fatal(err)
	}
}
func TestFreshResolutionRejectsStableFields(t *testing.T) {
	for _, change := range []func(*teams.Team){func(d *teams.Team) { d.Slots[0].Identity = d.Slots[3].Identity }, func(d *teams.Team) { d.Slots[0].Pool = "workers" }, func(d *teams.Team) { d.Slots[1].Identity = d.Slots[3].Identity }} {
		d := team()
		change(&d)
		if teams.Validate(d) == nil {
			t.Fatal("resolution accepted incompatible identity field")
		}
	}
}
func TestFreshActorCannotUseReservedOrHistoricalIdentity(t *testing.T) {
	for _, historical := range []bool{false, true} {
		t.Run(map[bool]string{false: "declared", true: "historical"}[historical], func(t *testing.T) {
			d := team()
			d.Slots[1] = teams.Slot{Name: "engineer", Resolution: teams.Fresh, Activation: teams.Singleton, Definition: mesh.DefinitionRef{ID: "worker", Revision: "r1"}, Max: 1}
			h := memory.New()
			h.SpawnCapabilities["owner"] = true
			if err := putDefinition(h, d); err != nil {
				t.Fatal(err)
			}
			run, err := launcher(h).Launch(ctx, teams.LaunchRequest{Key: "fresh", TeamID: d.ID, Version: 1})
			if err != nil {
				t.Fatal(err)
			}
			r, _ := h.Snapshot(ctx, run.ID)
			owner := slotMember(t, r, "owner")
			actor := d.Slots[3].Identity
			if historical {
				actor = "msg://agent/test/retired"
				if err := h.Mutate(ctx, run.ID, func(r *teams.Roster) error {
					r.Members = append(r.Members, teams.Member{ID: "retired", Actor: actor, Slot: "engineer", Status: "stopped", Resolution: teams.Fresh})
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			p := conflictingProvisioner{h, actor}
			if _, err := teams.Spawn(ctx, d, h, p, h, h, run.ID, owner.Actor, "engineer", "bad-fresh", mesh.Limits{Budget: 1}, limits()); err == nil {
				t.Fatal("fresh identity reuse accepted")
			}
			r, _ = h.Snapshot(ctx, run.ID)
			for _, m := range r.Members {
				if m.Parent == owner.ID && m.Status != "failed" {
					t.Fatal("identity reuse retained quota")
				}
			}
		})
	}
}
func TestFanOutCeilingInclusive(t *testing.T) {
	d := team()
	d.Authority = teams.Authority{Mode: teams.DevOpen}
	cap := limits()
	cap.FanOut = 2
	d.Policy.Spawn = cap
	parent := teams.Member{ID: "root", Actor: "msg://agent/test/root", Slot: "owner", Status: "active", SpawnCapable: true, Budget: 100, Limits: cap}
	target := d.Slots[1]
	target.Max = 10
	req := cap
	req.Budget = 1
	r := teams.Roster{Members: []teams.Member{parent, {ID: "first", Parent: parent.ID, Slot: "engineer", Status: "active", Budget: 1}}}
	if err := teams.CheckSpawn(d, r, parent, target, req); err != nil {
		t.Fatal("fanout at ceiling denied", err)
	}
	r.Members = append(r.Members, teams.Member{ID: "second", Parent: parent.ID, Slot: "engineer", Status: "active", Budget: 1})
	if err := teams.CheckSpawn(d, r, parent, target, req); err == nil {
		t.Fatal("fanout ceiling+1 accepted")
	}
}
