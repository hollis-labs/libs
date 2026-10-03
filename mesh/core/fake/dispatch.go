package fake

import (
	"context"
	"fmt"
	"sort"
	"strings"

	mesh "github.com/hollis-labs/substrate/mesh"
)

const LogURN mesh.URN = "msg://log/fake/events"

// RestartImage is a fake-only checkpoint. It is not a production persistence or
// credential format. Export and Restore simulate a provider restart in tests.
type RestartImage struct {
	Agents           map[mesh.URN]mesh.InstanceView
	Tasks            map[mesh.URN]mesh.Task
	Teams            map[mesh.URN]mesh.Team
	Messages         map[string]mesh.Message
	Approvals        map[mesh.URN]map[mesh.URN]bool
	Cache            map[string]cached
	Receipts         map[mesh.URN]mesh.AssignmentReceipt
	FollowGrants     map[mesh.URN]bool
	Events           []mesh.Event
	Cursors          []string
	Base             uint64
	Next             uint64
	Limits           mesh.Limits
	QueueAssignments bool
	Definitions      map[mesh.URN]mesh.DefinitionRef
	AgentOwners      map[mesh.URN]mesh.Actor
}

func (p *Provider) Export() RestartImage {
	p.mu.Lock()
	defer p.mu.Unlock()
	return clone(RestartImage{p.agents, p.tasks, p.teams, p.messages, p.approvals, p.cache, p.receipts, p.followGrants, p.events, p.cursors, p.base, p.next, p.limits, p.queueAssignments, p.definitions, p.agentOwners})
}
func Restore(image RestartImage) *Provider {
	s := clone(image)
	p := New()
	p.agents = s.Agents
	p.tasks = s.Tasks
	p.teams = s.Teams
	p.messages = s.Messages
	p.approvals = s.Approvals
	p.cache = s.Cache
	p.receipts = s.Receipts
	p.followGrants = s.FollowGrants
	p.events = s.Events
	p.cursors = s.Cursors
	p.base = s.Base
	p.next = s.Next
	p.limits = s.Limits
	p.queueAssignments = s.QueueAssignments
	p.definitions = s.Definitions
	p.agentOwners = s.AgentOwners
	return p
}

// GrantFollow authorizes log observation, not universal access to private events.
// Private task events remain visible only to caller/assigned actor; other events
// only to their actor. This deliberately small policy is for host tests.
func (p *Provider) GrantFollow(actor mesh.URN) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.followGrants[actor] = true
}
func (p *Provider) RevokeFollow(actor mesh.URN) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.followGrants, actor)
	p.wake()
}
func (p *Provider) wake()                        { close(p.changed); p.changed = make(chan struct{}) }
func (p *Provider) bookmark(index uint64) string { return fmt.Sprintf("fake-bookmark-%x", index) }
func (p *Provider) position() mesh.LogPosition {
	head := p.bookmark(p.base + uint64(len(p.events)))
	return mesh.LogPosition{Log: LogURN, Watermark: head, Head: head, Oldest: p.bookmark(p.base)}
}

// RetainLast simulates retention without discarding assignment keys/receipts.
func (p *Provider) RetainLast(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n < 0 {
		n = 0
	}
	if n >= len(p.events) {
		return
	}
	drop := len(p.events) - n
	p.events = clone(p.events[drop:])
	p.base += uint64(drop)
	p.wake()
}
func (p *Provider) visible(actor mesh.URN, e mesh.Event) bool {
	if e.Visibility == "public" {
		return true
	}
	if e.Actor.URN == actor {
		return true
	}
	t, ok := p.tasks[e.Subject]
	return ok && (t.Caller.URN == actor || t.Agent == actor)
}
func contains[T comparable](items []T, x T) bool {
	for _, v := range items {
		if v == x {
			return true
		}
	}
	return false
}
func (p *Provider) follow(ctx context.Context, r mesh.Request) (mesh.Response, error) {
	q := r.Follow
	if q == nil || q.Log != LogURN || q.Limit < 1 || q.Limit > mesh.MaxReplayPageSize {
		return mesh.Response{}, mesh.NewError(mesh.ErrorInvalid, "invalid follow log or page limit")
	}
	for {
		if err := ctx.Err(); err != nil {
			return mesh.Response{}, err
		}
		p.mu.Lock()
		if !p.followGrants[r.Actor.URN] {
			p.mu.Unlock()
			return mesh.Response{}, mesh.NewError(mesh.ErrorDenied, "follow grant required")
		}
		page := mesh.ReplayPage{Position: p.position(), Next: q.Cursor, Events: []mesh.Event{}}
		index := p.base
		if q.Cursor != "" {
			found := false
			for i, c := range p.cursors {
				if c == q.Cursor {
					index = uint64(i)
					found = true
					break
				}
			}
			if !found {
				p.mu.Unlock()
				return mesh.Response{}, mesh.NewError(mesh.ErrorInvalid, "cursor does not belong to log")
			}
		}
		if index < p.base {
			page.Gap = true
			page.Diagnostic = mesh.DiagnosticReplayGap
			page.Next = q.Cursor
			p.mu.Unlock()
			return mesh.Response{Replay: &page}, nil
		}
		end := p.base + uint64(len(p.events))
		// Bound scanned records, not only visible results, to prevent unbounded work.
		stop := index + uint64(q.Limit)
		if stop > end {
			stop = end
		}
		for ; index < stop; index++ {
			e := p.events[index-p.base]
			if p.visible(r.Actor.URN, e) && (len(q.Subjects) == 0 || contains(q.Subjects, e.Subject)) && (len(q.Kinds) == 0 || contains(q.Kinds, e.Kind)) {
				page.Events = append(page.Events, clone(e))
			}
		}
		page.Next = p.bookmark(index)
		page.HasMore = index < end
		if len(page.Events) > 0 || page.HasMore || !q.Wait {
			p.mu.Unlock()
			return mesh.Response{Replay: &page}, nil
		}
		ch := p.changed
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return mesh.Response{}, ctx.Err()
		case <-ch:
		}
	}
}
func (p *Provider) lookup(r mesh.Request) (mesh.Response, error) {
	q := r.Lookup
	if q == nil || (q.TaskURN == "") == (q.IntentKey == "") {
		return mesh.Response{}, mesh.NewError(mesh.ErrorInvalid, "select task or caller-scoped intent")
	}
	id := q.TaskURN
	if q.IntentKey != "" {
		for task, receipt := range p.receipts {
			if receipt.Caller == r.Actor && receipt.IntentKey == q.IntentKey {
				id = task
				break
			}
		}
	}
	t, ok := p.tasks[id]
	if !ok {
		return mesh.Response{}, missing()
	}
	if !taskVisible(t, r.Actor) {
		return mesh.Response{}, missing()
	}
	receipt, ok := p.receipts[id]
	if !ok {
		return mesh.Response{}, missing()
	}
	receipt.State = t.State
	snapshot := mesh.TaskSnapshot{Receipt: clone(receipt), Task: clone(t), Position: p.position()}
	return mesh.Response{Snapshot: &snapshot}, nil
}
func (p *Provider) admit(r mesh.Request, t mesh.Task, roster *mesh.RosterProvenance) mesh.AssignmentReceipt {
	digest, _ := mesh.AssignmentDigest(r)
	target := r.Target
	if roster != nil {
		target = r.Team
	}
	receipt := mesh.AssignmentReceipt{TaskURN: t.ID, Caller: r.Actor, IntentKey: r.IdempotencyKey, RequestDigest: digest, AcceptedTarget: target, ResolvedMember: t.Agent, Roster: roster, Delivery: mesh.DeliveryDelivered, State: t.State, ActorURN: t.Agent, SessionURN: p.agents[t.Agent].SessionURN}
	if p.queueAssignments {
		receipt.Delivery = mesh.DeliveryPending
		receipt.ActorURN = ""
		receipt.SessionURN = ""
	}
	p.receipts[t.ID] = receipt
	return receipt
}
func (p *Provider) selectMember(r mesh.Request) (mesh.URN, *mesh.RosterProvenance, error) {
	team, ok := p.teams[r.Team]
	if !ok {
		return "", nil, missing()
	}

	if r.Admission != nil && r.Admission.ExpectedProviderRosterVersion != 0 && r.Admission.ExpectedProviderRosterVersion != team.RosterVersion {
		return "", nil, mesh.NewDispatchError(mesh.ErrorConflict, mesh.DiagnosticStaleSnapshot, string(r.Team), "read a current roster and create a new intent")
	}
	if r.Address == "@all" || strings.HasSuffix(r.Address, "*") {
		return "", nil, mesh.NewError(mesh.ErrorInvalid, "assignment cannot broadcast")
	}
	var candidates []mesh.URN
	for id, m := range team.Members {
		addressMatches := r.Address == "" || r.Address == string(id) || r.Address == "@"+m.ID || r.Address == "@"+m.Slot
		slotMatches := r.Slot == "" || r.Slot == m.Slot
		if r.Target != "" && r.Target == id && (!addressMatches || !slotMatches) {
			return "", nil, mesh.NewError(mesh.ErrorInvalid, "conflicting assignment target/address/slot")
		}
		if (r.Target != "" && r.Target == id && addressMatches && slotMatches) || (r.Target == "" && (r.Address != "" || r.Slot != "") && addressMatches && slotMatches) {
			candidates = append(candidates, id)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i] < candidates[j] })
	if len(candidates) == 0 {
		return "", nil, missing()
	}
	// Explicit identities never fall back to another worker. A slot picks one
	// deterministic member, then the receipt retains it across roster changes.
	id := candidates[0]
	provenance := &mesh.RosterProvenance{Team: r.Team, Member: id, Membership: team.Members[id], ProviderVersion: team.RosterVersion}
	return id, provenance, nil
}

func assignmentLimits(l, defaults mesh.Limits) error {
	if l.MaxDepth == 0 {
		l.MaxDepth = defaults.MaxDepth
	}
	if l.MaxChildren == 0 {
		l.MaxChildren = defaults.MaxChildren
	}
	if l.FanOut == 0 {
		l.FanOut = defaults.FanOut
	}
	if l.Budget == 0 {
		l.Budget = defaults.Budget
	}
	if l.Timeout == 0 {
		l.Timeout = defaults.Timeout
	}
	if err := l.Validate(); err != nil {
		return mesh.NewError(mesh.ErrorInvalid, err.Error())
	}
	if l.Budget > defaults.Budget {
		return mesh.NewDispatchError(mesh.ErrorLimit, mesh.DiagnosticBudgetExhausted, "assignment", "authorize a new budget before admission")
	}
	if l.MaxDepth > defaults.MaxDepth || l.MaxChildren > defaults.MaxChildren || l.FanOut > defaults.FanOut || l.Timeout > defaults.Timeout {
		return mesh.NewError(mesh.ErrorLimit, "assignment exceeds target limits")
	}
	return nil
}

// QueueAssignments makes new admission queue mail without claiming work started.
// Existing receipts are unaffected. Host tests call DeliverTask to model delivery.
func (p *Provider) QueueAssignments(queued bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.queueAssignments = queued
}

// DeliverTask models mail reaching the selected member. The retained assignment
// selection is used even if the team roster has changed since admission.
func (p *Provider) DeliverTask(id mesh.URN) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	t, ok := p.tasks[id]
	if !ok {
		return missing()
	}
	receipt := p.receipts[id]
	if receipt.Delivery == mesh.DeliveryDelivered {
		return nil
	}
	if t.State.Terminal() {
		return conflict()
	}
	a, ok := p.agents[t.Agent]
	if !ok || a.SessionState != mesh.SessionRunning {
		return mesh.NewDispatchError(mesh.ErrorConflict, mesh.DiagnosticTargetBusy, string(t.Agent), "retain queued delivery until target is available")
	}
	receipt.Delivery = mesh.DeliveryDelivered
	if t.State == mesh.TaskSubmitted {
		t.State = mesh.TaskWorking
	}
	receipt.State = t.State
	receipt.ActorURN = t.Agent
	receipt.SessionURN = a.SessionURN
	p.tasks[id] = t
	p.receipts[id] = receipt
	p.emit(mesh.Request{Verb: "task.delivered", Actor: mesh.Actor{URN: "msg://service/fake/provider", Kind: mesh.ActorService}, Target: id, IdempotencyKey: "delivery:" + string(id)}, mesh.Response{Task: &t, Receipt: &receipt})
	return nil
}

// SetDefinition supplies the host-verified pin for an enrolled fake worker.
func (p *Provider) SetDefinition(agent mesh.URN, definition mesh.DefinitionRef) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.definitions[agent] = definition
}
func (p *Provider) checkAdmission(r mesh.Request, target mesh.URN) error {
	c := r.Admission
	if c == nil {
		return nil
	}
	if c.HostStoreVersion != "" {
		return mesh.NewDispatchError(mesh.ErrorUnsupported, mesh.DiagnosticUnsupportedRequirement, "host roster", "use a host that verifies its roster-store provenance")
	}
	if c.ExpectedProviderRosterVersion != 0 && r.Team == "" {
		return mesh.NewError(mesh.ErrorInvalid, "roster constraint requires team target")
	}
	if _, err := mesh.NegotiateCapabilities(p.descriptor(), c.Requirements); err != nil {
		return mesh.NewDispatchError(mesh.ErrorUnsupported, mesh.DiagnosticUnsupportedRequirement, "admission", "negotiate exact required capability versions")
	}
	if c.Definition != nil {
		known, ok := p.definitions[target]
		pin := *c.Definition
		if !ok || pin.ID == "" || pin.Revision == "" || known.ID != pin.ID || known.Revision != pin.Revision || (pin.Digest != "" && known.Digest != pin.Digest) {
			return mesh.NewDispatchError(mesh.ErrorInvalid, mesh.DiagnosticInvalidPin, string(target), "resolve and verify the immutable definition pin")
		}
	}
	return nil
}

func taskVisible(t mesh.Task, actor mesh.Actor) bool {
	return t.Caller == actor || t.Agent == actor.URN
}
