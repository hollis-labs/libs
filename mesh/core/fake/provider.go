// Package fake implements a deterministic, in-memory mesh for host tests.
// It launches no processes and provides no persistence or production trust policy.
// Fake cursor spellings expose sequence counts; production hosts must mint
// opaque log-scoped cursors. Provider-side slot dispatch picks a deterministic member. Hosts that resolve
// and authorize routing themselves send to the retained recipient URNs directly.
package fake

import (
	"context"
	"encoding/json"
	"fmt"
	mesh "github.com/hollis-labs/substrate/mesh"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

const CapabilityURI = "urn:hollis-labs:mesh:mvp/v1"

var verbs = []mesh.Verb{mesh.AgentLaunch, mesh.AgentStop, mesh.AgentResume, mesh.AgentStatus, mesh.TeamForm, mesh.TeamDissolve, mesh.MemberAdd, mesh.MemberRemove, mesh.MemberJoin, mesh.MemberLeave, mesh.RoleAssign, mesh.Assign, mesh.Delegate, mesh.MessageSend, mesh.MessageAddress, mesh.Reply, mesh.Steer, mesh.Interrupt, mesh.RequestInput, mesh.RequestApproval, mesh.Approve, mesh.Cancel, mesh.ReportResult}

type cached struct {
	Request  mesh.Request
	Response mesh.Response
}
type Provider struct {
	mu               sync.Mutex
	receipts         map[mesh.URN]mesh.AssignmentReceipt
	followGrants     map[mesh.URN]bool
	cursors          []string
	base             uint64
	changed          chan struct{}
	queueAssignments bool
	definitions      map[mesh.URN]mesh.DefinitionRef
	agents           map[mesh.URN]mesh.InstanceView
	agentOwners      map[mesh.URN]mesh.Actor
	tasks            map[mesh.URN]mesh.Task
	teams            map[mesh.URN]mesh.Team
	messages         map[string]mesh.Message
	approvals        map[mesh.URN]map[mesh.URN]bool
	cache            map[string]cached
	events           []mesh.Event
	next             uint64
	limits           mesh.Limits
}

func New() *Provider {
	return &Provider{agentOwners: map[mesh.URN]mesh.Actor{}, definitions: map[mesh.URN]mesh.DefinitionRef{}, receipts: map[mesh.URN]mesh.AssignmentReceipt{}, followGrants: map[mesh.URN]bool{}, cursors: []string{"fake-bookmark-0"}, changed: make(chan struct{}), agents: map[mesh.URN]mesh.InstanceView{}, tasks: map[mesh.URN]mesh.Task{}, teams: map[mesh.URN]mesh.Team{}, messages: map[string]mesh.Message{}, approvals: map[mesh.URN]map[mesh.URN]bool{}, cache: map[string]cached{}, limits: mesh.Limits{MaxDepth: 8, MaxChildren: 16, FanOut: 16, Budget: 100, Timeout: time.Hour}}
}
func clone[T any](v T) T { b, _ := json.Marshal(v); var out T; _ = json.Unmarshal(b, &out); return out }
func (p *Provider) Describe(ctx context.Context) (mesh.Descriptor, error) {
	if err := ctx.Err(); err != nil {
		return mesh.Descriptor{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.descriptor(), nil
}
func (p *Provider) descriptor() mesh.Descriptor {
	return mesh.Descriptor{Provider: "msg://service/fake/provider", Capabilities: []mesh.Capability{{URI: mesh.SpawnCapabilityURI, Verbs: []mesh.Verb{mesh.AgentLaunch}, Modes: []string{"required_limits", "parent_links", "cancel.cascade"}}, {URI: mesh.DispatchCapabilityURI, Verbs: []mesh.Verb{mesh.Assign, mesh.TaskLookup, mesh.EventFollow, mesh.ReportResult}, Modes: []string{"assign.actor-scoped", "assign.team-single", "follow.authorized", "replay.gap", "result.versioned"}}, {URI: CapabilityURI, Verbs: append([]mesh.Verb(nil), verbs...), Modes: []string{"delivery.at_idle", "cancel.cascade", "history.summary", "history.full", "history.filtered", "history.none"}}}, TaskStates: []mesh.TaskState{mesh.TaskSubmitted, mesh.TaskWorking, mesh.TaskInputRequired, mesh.TaskCompleted, mesh.TaskCanceled}, SessionStates: []mesh.SessionState{mesh.SessionRunning, mesh.SessionPaused, mesh.SessionEnded}, DefaultLimits: p.limits}
}

// GrantApproval grants this exact actor authority over this task. Actor kind grants nothing.
func (p *Provider) GrantApproval(task, actor mesh.URN) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.approvals[task] == nil {
		p.approvals[task] = map[mesh.URN]bool{}
	}
	p.approvals[task][actor] = true
}
func (p *Provider) Events() []mesh.Event { p.mu.Lock(); defer p.mu.Unlock(); return clone(p.events) }
func (p *Provider) id(kind string) mesh.URN {
	p.next++
	return mesh.URN(fmt.Sprintf("msg://%s/fake/%d", kind, p.next))
}
func (p *Provider) Invoke(ctx context.Context, r mesh.Request) (mesh.Response, error) {
	if err := ctx.Err(); err != nil {
		return mesh.Response{}, err
	}
	if err := r.Actor.Validate(); err != nil {
		return mesh.Response{}, mesh.NewError(mesh.ErrorInvalid, err.Error())
	}
	if len(r.OnBehalfOf) > 0 {
		return mesh.Response{}, mesh.NewError(mesh.ErrorUnsupported, "on_behalf_of grants are not implemented")
	}
	if len(r.Body) > 0 && !json.Valid(r.Body) {
		return mesh.Response{}, mesh.NewError(mesh.ErrorInvalid, "body is not JSON")
	}
	if r.Delivery != "" && r.Delivery != mesh.DeliveryAtIdle {
		return mesh.Response{}, mesh.NewError(mesh.ErrorUnsupported, "unsupported delivery policy")
	}
	if r.Delivery != "" && r.Verb != mesh.MessageSend && r.Verb != mesh.MessageAddress && r.Verb != mesh.Reply && r.Verb != mesh.ReportResult {
		return mesh.Response{}, mesh.NewError(mesh.ErrorUnsupported, "delivery policy requires a message or result")
	}
	if !r.History.Effective().Valid() {
		return mesh.Response{}, mesh.NewError(mesh.ErrorInvalid, "invalid history policy")
	}
	if _, err := json.Marshal(r); err != nil {
		return mesh.Response{}, mesh.NewError(mesh.ErrorInvalid, "request cannot be encoded: "+err.Error())
	}
	if r.Verb == mesh.EventFollow {
		return p.follow(ctx, clone(r))
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return mesh.Response{}, err
	}
	r = clone(r)
	if r.Verb == mesh.TaskLookup {
		return p.lookup(r)
	}
	key := string(r.Actor.URN) + "\x00" + r.IdempotencyKey
	if r.IdempotencyKey != "" {
		if c, ok := p.cache[key]; ok {
			cachedRequest := c.Request
			incomingRequest := r
			if r.Verb == mesh.Assign || r.Verb == mesh.Delegate {
				cachedRequest.CorrelationID = ""
				incomingRequest.CorrelationID = ""
			}
			if !reflect.DeepEqual(cachedRequest, incomingRequest) {
				return mesh.Response{}, mesh.NewError(mesh.ErrorConflict, "idempotency key reused with different command")
			}
			out := clone(c.Response)
			if out.Receipt != nil {
				receipt := p.receipts[out.Receipt.TaskURN]
				receipt.State = p.tasks[receipt.TaskURN].State
				out.Receipt = &receipt
				task := p.tasks[receipt.TaskURN]
				out.Task = &task
			}
			return clone(out), nil
		}
	}
	supported := false
	for _, v := range verbs {
		supported = supported || v == r.Verb
	}
	if !supported {
		return mesh.Response{}, mesh.NewError(mesh.ErrorUnsupported, string(r.Verb))
	}
	// Commit only successful commands: errors must not leave partial state or events.
	backup := struct {
		Agents   map[mesh.URN]mesh.InstanceView
		Tasks    map[mesh.URN]mesh.Task
		Teams    map[mesh.URN]mesh.Team
		Messages map[string]mesh.Message
	}{clone(p.agents), clone(p.tasks), clone(p.teams), clone(p.messages)}
	before := p.next
	out, err := p.apply(r)
	if err != nil {
		p.agents = backup.Agents
		p.tasks = backup.Tasks
		p.teams = backup.Teams
		p.messages = backup.Messages
		p.next = before
		return mesh.Response{}, err
	}
	out.Events = []mesh.Event{p.emit(r, out)}
	if r.IdempotencyKey != "" {
		p.cache[key] = cached{r, clone(out)}
	}
	return clone(out), nil
}
func missing() error { return mesh.NewError(mesh.ErrorNotFound, "resource not found") }
func conflict() error {
	return mesh.NewError(mesh.ErrorConflict, "operation is invalid in current state")
}
func (p *Provider) apply(r mesh.Request) (mesh.Response, error) {
	var out mesh.Response
	switch r.Verb {
	case mesh.AgentLaunch:
		limits := r.Limits
		defaults := p.limits
		if parent, ok := p.agents[r.Parent]; ok {
			defaults = parent.Limits
		}
		if limits.MaxDepth == 0 {
			limits.MaxDepth = defaults.MaxDepth
		}
		if limits.MaxChildren == 0 {
			limits.MaxChildren = defaults.MaxChildren
		}
		if limits.FanOut == 0 {
			limits.FanOut = defaults.FanOut
		}
		if limits.Budget == 0 {
			limits.Budget = defaults.Budget
		}
		if limits.Timeout == 0 {
			limits.Timeout = defaults.Timeout
		}
		if limits.MaxRounds == 0 {
			limits.MaxRounds = defaults.MaxRounds
		}
		if limits.MaxStalls == 0 {
			limits.MaxStalls = defaults.MaxStalls
		}
		if err := limits.Validate(); err != nil {
			return out, mesh.NewError(mesh.ErrorInvalid, err.Error())
		}
		if r.Parent != "" {
			parent, ok := p.agents[r.Parent]
			if !ok {
				return out, missing()
			}
			if parent.SessionState != mesh.SessionRunning {
				return out, conflict()
			}
			depth := 1
			for a := parent; a.Parent != ""; a = p.agents[a.Parent] {
				depth++
			}
			children, active := 0, 0
			spent := 0.0
			for _, a := range p.agents {
				if a.Parent == r.Parent {
					children++
					spent += a.Limits.Budget
					if a.SessionState != mesh.SessionEnded {
						active++
					}
				}
			}
			if depth > parent.Limits.MaxDepth || children >= parent.Limits.MaxChildren || active >= parent.Limits.FanOut || spent+limits.Budget > parent.Limits.Budget {
				return out, mesh.NewError(mesh.ErrorLimit, "spawn limit exceeded")
			}
			if limits.MaxDepth > parent.Limits.MaxDepth || limits.MaxChildren > parent.Limits.MaxChildren || limits.FanOut > parent.Limits.FanOut || limits.Budget > parent.Limits.Budget || limits.Timeout > parent.Limits.Timeout {
				if r.Limits == (mesh.Limits{}) {
					limits = parent.Limits
				} else {
					return out, mesh.NewError(mesh.ErrorLimit, "child limits exceed parent")
				}
			}
		}
		id := r.Target
		if id == "" {
			id = p.id("agent")
		}
		if err := (mesh.Actor{URN: id, Kind: mesh.ActorAgent}).Validate(); err != nil {
			return out, mesh.NewError(mesh.ErrorInvalid, "invalid agent address")
		}
		if _, ok := p.agents[id]; ok {
			return out, conflict()
		}
		a := mesh.InstanceView{URN: id, SessionURN: p.id("session"), Parent: r.Parent, SessionState: mesh.SessionRunning, Limits: limits}
		p.agents[id] = a
		p.agentOwners[id] = r.Actor
		out.Instance = &a
	case mesh.AgentStatus, mesh.AgentStop, mesh.AgentResume, mesh.Steer, mesh.Interrupt:
		a, ok := p.agents[r.Target]
		if !ok {
			return out, missing()
		}
		switch r.Verb {
		case mesh.AgentStop:
			if a.SessionState != mesh.SessionRunning {
				return out, conflict()
			}
			a.SessionState = mesh.SessionPaused
		case mesh.AgentResume:
			if a.SessionState != mesh.SessionPaused {
				return out, conflict()
			}
			a.SessionState = mesh.SessionRunning
		case mesh.Steer, mesh.Interrupt:
			if a.SessionState != mesh.SessionRunning {
				return out, conflict()
			}
			if r.Verb == mesh.Interrupt {
				a.SessionState = mesh.SessionPaused
			} else {
				m := mesh.Message{ID: string(p.id("message")), Sender: r.Actor, Recipients: []mesh.URN{a.URN}, Body: r.Body}
				p.messages[m.ID] = m
				out.Message = &m
			}
		}
		p.agents[a.URN] = a
		out.Instance = &a
	case mesh.TeamForm:
		id := r.Target
		if id == "" {
			id = p.id("team")
		}
		if err := id.Validate(); err != nil {
			return out, err
		}
		if _, ok := p.teams[id]; ok {
			return out, conflict()
		}
		t := mesh.Team{URN: id, Members: map[mesh.URN]mesh.Member{}, RosterVersion: 1}
		p.teams[id] = t
		out.Team = &t
	case mesh.TeamDissolve:
		t, ok := p.teams[r.Target]
		if !ok {
			return out, missing()
		}
		delete(p.teams, r.Target)
		out.Team = &t
	case mesh.MemberAdd, mesh.MemberJoin, mesh.MemberRemove, mesh.MemberLeave, mesh.RoleAssign:
		t, ok := p.teams[r.Team]
		if !ok {
			return out, missing()
		}
		member := r.Target
		if r.Verb == mesh.MemberJoin || r.Verb == mesh.MemberLeave {
			member = r.Actor.URN
		}
		if err := member.Validate(); err != nil {
			return out, err
		}
		_, exists := t.Members[member]
		switch r.Verb {
		case mesh.MemberAdd, mesh.MemberJoin:
			if exists {
				return out, conflict()
			}
			if strings.TrimSpace(r.Slot) == "" || strings.ContainsAny(r.Slot, "@* /:\\") {
				return out, mesh.NewError(mesh.ErrorInvalid, "invalid member slot")
			}
			p.next++
			t.Members[member] = mesh.Member{ID: fmt.Sprintf("member-%d", p.next), Slot: r.Slot, Role: r.Role}
		case mesh.MemberRemove, mesh.MemberLeave:
			if !exists {
				return out, missing()
			}
			delete(t.Members, member)
		case mesh.RoleAssign:
			if !exists {
				return out, missing()
			}
			m := t.Members[member]
			m.Role = r.Role
			t.Members[member] = m
		}
		t.RosterVersion++
		p.teams[t.URN] = t
		out.Team = &t
	case mesh.Assign, mesh.Delegate:
		if r.Verb == mesh.Assign && r.IdempotencyKey == "" {
			return out, mesh.NewError(mesh.ErrorInvalid, "assign requires caller-scoped intent key")
		}
		if r.Team == "" && (r.Address != "" || r.Slot != "") {
			return out, mesh.NewError(mesh.ErrorInvalid, "assignment address/slot requires team scope")
		}
		target := r.Target
		var roster *mesh.RosterProvenance
		if r.Team != "" {
			var err error
			target, roster, err = p.selectMember(r)
			if err != nil {
				return out, err
			}
		}
		a, ok := p.agents[target]
		if !ok {
			return out, missing()
		}
		if a.SessionState != mesh.SessionRunning {
			return out, mesh.NewDispatchError(mesh.ErrorConflict, mesh.DiagnosticTargetBusy, string(target), "wait for the explicit target")
		}
		if err := p.checkAdmission(r, target); err != nil {
			return out, err
		}
		if err := assignmentLimits(r.Limits, a.Limits); err != nil {
			return out, err
		}
		if r.Parent != "" {
			parent, ok := p.tasks[r.Parent]
			if !ok || !taskVisible(parent, r.Actor) {
				return out, missing()
			}
			if parent.State.Terminal() {
				return out, conflict()
			}
		}
		t := mesh.Task{ID: p.id("task"), Agent: a.URN, Caller: r.Actor, Parent: r.Parent, State: mesh.TaskWorking, History: r.History.Effective()}
		if p.queueAssignments {
			t.State = mesh.TaskSubmitted
		}
		p.tasks[t.ID] = t
		out.Receipt = ptr(p.admit(r, t, roster))
		out.Task = &t
	case mesh.RequestInput, mesh.RequestApproval, mesh.Approve, mesh.ReportResult:
		t, ok := p.tasks[r.Target]
		if !ok || (!taskVisible(t, r.Actor) && !(r.Verb == mesh.Approve && p.approvals[t.ID][r.Actor.URN])) {
			return out, missing()
		}
		if t.State.Terminal() {
			return out, conflict()
		}
		switch r.Verb {
		case mesh.RequestInput, mesh.RequestApproval:
			t.State = mesh.TaskInputRequired
		case mesh.Approve:
			if t.State != mesh.TaskInputRequired {
				return out, conflict()
			}
			if !p.approvals[t.ID][r.Actor.URN] {
				return out, mesh.NewError(mesh.ErrorDenied, "actor has no approval grant")
			}
			t.State = mesh.TaskWorking
		case mesh.ReportResult:
			if r.Actor.URN != t.Agent {
				return out, mesh.NewError(mesh.ErrorDenied, "only the assigned actor may report its result")
			}
			if p.receipts[t.ID].Delivery == mesh.DeliveryPending {
				return out, conflict()
			}
			if r.Result == nil {
				return out, mesh.NewError(mesh.ErrorInvalid, "versioned result required")
			}
			if err := r.Result.Validate(); err != nil {
				return out, err
			}
			t.State = mesh.TaskCompleted
			receipt := p.receipts[t.ID]
			receipt.Result = ptr(clone(*r.Result))
			t.Result = receipt.Result.Content
			receipt.State = t.State
			p.receipts[t.ID] = receipt
			m := mesh.Message{ID: string(p.id("message")), Sender: r.Actor, Recipients: []mesh.URN{t.Caller.URN}, Body: t.Result, InReplyTo: string(t.ID), Delivery: mesh.DeliveryAtIdle}
			p.messages[m.ID] = m
			out.Message = &m
		}
		p.tasks[t.ID] = t
		out.Task = &t
	case mesh.Cancel:
		if t, ok := p.tasks[r.Target]; ok {
			if !taskVisible(t, r.Actor) {
				return out, missing()
			}
			if t.State.Terminal() {
				return out, conflict()
			}
			p.cancelTask(t.ID, r.Cascade)
			t = p.tasks[t.ID]
			out.Task = &t
		} else if a, ok := p.agents[r.Target]; ok {
			if p.agentOwners[a.URN] != r.Actor && a.URN != r.Actor.URN {
				return out, missing()
			}
			if a.SessionState == mesh.SessionEnded {
				return out, conflict()
			}
			p.cancelAgent(a.URN, r.Cascade)
			a = p.agents[a.URN]
			out.Instance = &a
		} else {
			return out, missing()
		}
	case mesh.MessageSend, mesh.MessageAddress, mesh.Reply:
		recipients := []mesh.URN{}
		version := uint64(0)
		if r.Verb == mesh.Reply {
			m, ok := p.messages[r.InReplyTo]
			if !ok {
				return out, missing()
			}
			allowed := false
			for _, recipient := range m.Recipients {
				allowed = allowed || recipient == r.Actor.URN
			}
			if !allowed {
				return out, mesh.NewError(mesh.ErrorDenied, "only a recipient may reply")
			}
			recipients = []mesh.URN{m.Sender.URN}
		} else if r.Verb == mesh.MessageSend {
			if err := r.Target.Validate(); err != nil {
				return out, err
			}
			recipients = []mesh.URN{r.Target}
		} else {
			t, ok := p.teams[r.Team]
			if !ok {
				return out, missing()
			}
			version = t.RosterVersion
			address := r.Address
			if address == "" {
				return out, mesh.NewError(mesh.ErrorInvalid, "missing address")
			}
			for member, membership := range t.Members {
				if address == string(member) || address == "@"+membership.ID || address == "@all" || address == "@"+membership.Slot || address == "@"+membership.Slot+"*" {
					recipients = append(recipients, member)
				}
			}
			sort.Slice(recipients, func(i, j int) bool { return recipients[i] < recipients[j] })
			if len(recipients) == 0 {
				return out, missing()
			}
			if address != "@all" && !strings.HasSuffix(address, "*") {
				recipients = recipients[:1]
			}
		}
		m := mesh.Message{ID: string(p.id("message")), Sender: r.Actor, Recipients: recipients, Body: r.Body, InReplyTo: r.InReplyTo, RosterVersion: version, Delivery: r.Delivery}
		p.messages[m.ID] = m
		out.Message = &m
	}
	return out, nil
}
func (p *Provider) cancelTask(id mesh.URN, cascade bool) {
	t := p.tasks[id]
	if !t.State.Terminal() {
		t.State = mesh.TaskCanceled
		p.tasks[id] = t
	}
	if cascade {
		for _, child := range p.tasks {
			if child.Parent == id {
				p.cancelTask(child.ID, true)
			}
		}
	}
}
func (p *Provider) cancelAgent(id mesh.URN, cascade bool) {
	a := p.agents[id]
	a.SessionState = mesh.SessionEnded
	p.agents[id] = a
	for _, t := range p.tasks {
		if t.Agent == id {
			p.cancelTask(t.ID, cascade)
		}
	}
	if cascade {
		for _, child := range p.agents {
			if child.Parent == id {
				p.cancelAgent(child.URN, true)
			}
		}
	}
}

// Task returns an isolated snapshot for host assertions.
func (p *Provider) Task(id mesh.URN) (mesh.Task, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	t, ok := p.tasks[id]
	return clone(t), ok
}

var _ mesh.Provider = (*Provider)(nil)

func ptr[T any](v T) *T { return &v }

// emit is called while holding the provider lock.
func (p *Provider) emit(r mesh.Request, out mesh.Response) mesh.Event {
	p.next++
	subject := r.Target
	if out.Instance != nil {
		subject = out.Instance.URN
	}
	if out.Task != nil {
		subject = out.Task.ID
	}
	if out.Team != nil {
		subject = out.Team.URN
	}
	if out.Message != nil && out.Task == nil {
		subject = mesh.URN(out.Message.ID)
	}
	if subject == "" {
		subject = r.Actor.URN
	}
	payload, _ := json.Marshal(out)
	event := mesh.Event{SchemaVersion: "1", ID: fmt.Sprint(p.next), Kind: string(r.Verb), Time: time.Now().UTC(), App: "fake", Source: mesh.EventSource{Channel: "fake", Confidence: 1}, Actor: r.Actor, Subject: subject, CorrelationID: r.CorrelationID, InReplyTo: r.InReplyTo, Generation: 1, SourceSequence: p.next, Cursor: p.bookmark(p.base + uint64(len(p.events)) + 1), ContentType: "application/json", PayloadSchema: "urn:hollis-labs:mesh:response/v1", IdempotencyKey: r.IdempotencyKey, Visibility: "private", Payload: payload}
	p.events = append(p.events, event)
	p.cursors = append(p.cursors, event.Cursor)
	p.wake()
	return event
}
