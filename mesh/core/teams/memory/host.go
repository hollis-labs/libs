// Package memory provides a detached, concurrency-safe host fake for teams
// contract tests. It is intentionally non-durable and never starts real agents.
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/hollis-labs/substrate/mesh"
	"github.com/hollis-labs/substrate/mesh/teams"
)

type Host struct {
	mu                  sync.Mutex
	rosterMu            sync.Mutex
	leases              sync.Map
	definitions         map[string]teams.Team
	launches            map[string]teams.LaunchRecord
	rosters             map[string]teams.Roster
	rosterHistory       map[string]map[uint64]teams.Roster
	bindings            map[string]string
	phaseSignals        map[string][]teams.PhaseSignalRecord
	signals             map[string]teams.SignalResolution
	provisions          map[string]teams.Member
	requests            map[string]teams.ProvisionRequest
	workflows           map[string]string
	workflowDefinitions map[string]teams.WorkflowDefinition
	routing             map[string]teams.Routing
	delegationStates    map[string]mesh.TaskState
	deliveries          map[string]teams.Delivery
	canceled            map[string]bool
	ended               map[string]string
	enrollments         map[mesh.URN]bool
	actorBindings       map[mesh.URN]string
	failedWorkflows     map[string]string
	next                uint64
	// Set hooks before concurrent use. Before fails before a write; After
	// simulates losing the acknowledgement after a side effect succeeded.
	Before            func(op, key string) error
	After             func(op, key string) error
	SpawnCapabilities map[string]bool
	Trust             teams.TrustDecision
	TriggerFired      bool
	NowTime           time.Time
	Approvals         []string
}

func New() *Host {
	return &Host{
		enrollments: map[mesh.URN]bool{}, actorBindings: map[mesh.URN]string{}, failedWorkflows: map[string]string{}, definitions: map[string]teams.Team{}, launches: map[string]teams.LaunchRecord{}, rosterHistory: map[string]map[uint64]teams.Roster{}, bindings: map[string]string{}, phaseSignals: map[string][]teams.PhaseSignalRecord{}, rosters: map[string]teams.Roster{}, signals: map[string]teams.SignalResolution{}, provisions: map[string]teams.Member{}, requests: map[string]teams.ProvisionRequest{}, workflows: map[string]string{}, workflowDefinitions: map[string]teams.WorkflowDefinition{}, routing: map[string]teams.Routing{}, deliveries: map[string]teams.Delivery{}, delegationStates: map[string]mesh.TaskState{}, canceled: map[string]bool{}, ended: map[string]string{}, SpawnCapabilities: map[string]bool{}, Trust: teams.TrustAllow, NowTime: time.Unix(1700000000, 0).UTC(),
	}
}
func copyValue[T any](v T) T {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	var out T
	if err = json.Unmarshal(b, &out); err != nil {
		panic(err)
	}
	return out
}
func (h *Host) before(ctx context.Context, op, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if h.Before != nil {
		return h.Before(op, key)
	}
	return nil
}
func (h *Host) after(op, key string) error {
	if h.After != nil {
		return h.After(op, key)
	}
	return nil
}
func definitionKey(id string, version uint64) string { return fmt.Sprintf("%s/%d", id, version) }
func (h *Host) GetDefinition(ctx context.Context, id string, version uint64) (teams.Team, error) {
	if err := h.before(ctx, "get_definition", id); err != nil {
		return teams.Team{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	v, ok := h.definitions[definitionKey(id, version)]
	if !ok {
		return v, teams.ErrNotFound
	}
	return copyValue(v), nil
}
func (h *Host) PutDefinition(ctx context.Context, t teams.Team) error {
	if err := teams.Validate(t); err != nil {
		return err
	}
	if err := h.before(ctx, "definition", t.ID); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	key := definitionKey(t.ID, t.Version)
	if old, ok := h.definitions[key]; ok && !reflect.DeepEqual(old, t) {
		return teams.ErrConflict
	}
	h.definitions[key] = copyValue(t)
	return nil
}
func (h *Host) Snapshot(ctx context.Context, runID string) (teams.Roster, error) {
	if err := h.before(ctx, "snapshot", runID); err != nil {
		return teams.Roster{}, err
	}
	h.rosterMu.Lock()
	defer h.rosterMu.Unlock()
	r, ok := h.rosters[runID]
	if !ok {
		return r, teams.ErrNotFound
	}
	return copyValue(r), nil
}
func (h *Host) Mutate(ctx context.Context, runID string, fn func(*teams.Roster) error) error {
	if err := h.before(ctx, "roster", runID); err != nil {
		return err
	}
	err := func() error {
		h.rosterMu.Lock()
		defer h.rosterMu.Unlock()
		old := h.rosters[runID]
		r := copyValue(old)
		r.RunID = runID
		if err := fn(&r); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !reflect.DeepEqual(old.Members, r.Members) || old.RunID == "" {
			r.Version = old.Version + 1
		}
		h.rosters[runID] = copyValue(r)
		if h.rosterHistory[runID] == nil {
			h.rosterHistory[runID] = map[uint64]teams.Roster{}
		}
		h.rosterHistory[runID][r.Version] = copyValue(r)
		return nil
	}()
	if err != nil {
		return err
	}
	return h.after("roster", runID)
}
func (h *Host) WithLease(ctx context.Context, key string, fn func(context.Context) error) error {
	lock, _ := h.leases.LoadOrStore(key, make(chan struct{}, 1))
	ch := lock.(chan struct{})
	select {
	case ch <- struct{}{}:
		defer func() { <-ch }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(ctx)
}
func (h *Host) GetLaunch(ctx context.Context, key string) (teams.LaunchRecord, error) {
	if err := h.before(ctx, "get_launch", key); err != nil {
		return teams.LaunchRecord{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	v, ok := h.launches[key]
	if !ok {
		return v, teams.ErrNotFound
	}
	return copyValue(v), nil
}
func (h *Host) PutLaunch(ctx context.Context, v teams.LaunchRecord) error {
	if err := h.before(ctx, "ledger_"+string(v.State), v.Key); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.launches[v.Key]; ok {
		if old.Digest != v.Digest || old.Team.ID != v.Team.ID {
			return teams.ErrConflict
		}
		rank := map[teams.LaunchState]int{teams.Planning: 1, teams.Prepared: 2, teams.Launched: 3, teams.MembersReady: 4, teams.RoutingReady: 5}
		abort := (v.State == teams.Aborting && old.State != teams.Failed && old.State != teams.RoutingReady) || (old.State == teams.Aborting && v.State == teams.Failed) || (old.State == teams.Failed && v.State == teams.Failed)
		if !abort && (old.State == teams.Aborting || old.State == teams.Failed || rank[v.State] < rank[old.State] || rank[v.State] > rank[old.State]+1) {
			return teams.ErrConflict
		}
	} else if v.State != teams.Planning {
		return teams.ErrConflict
	}
	h.launches[v.Key] = copyValue(v)
	return h.after("ledger_"+string(v.State), v.Key)
}
func (h *Host) Pending(ctx context.Context, after string, limit int) ([]string, error) {
	if err := h.before(ctx, "pending", after); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, fmt.Errorf("positive limit required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var higher, lower []string
	for k, v := range h.launches {
		if v.State == teams.RoutingReady || v.State == teams.Failed {
			continue
		}
		if k > after {
			higher = append(higher, k)
		} else {
			lower = append(lower, k)
		}
	}
	sort.Strings(higher)
	sort.Strings(lower)
	keys := append(higher, lower...)
	if len(keys) > limit {
		keys = keys[:limit]
	}
	return keys, nil
}
func (h *Host) Provision(ctx context.Context, req teams.ProvisionRequest) (teams.Member, error) {
	if err := h.before(ctx, "provision", req.IdempotencyKey); err != nil {
		return teams.Member{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.canceled[req.IdempotencyKey] {
		return teams.Member{}, teams.ErrDenied
	}
	if old, ok := h.requests[req.IdempotencyKey]; ok && !reflect.DeepEqual(old, req) {
		return teams.Member{}, teams.ErrConflict
	}
	if m, ok := h.provisions[req.IdempotencyKey]; ok {
		return copyValue(m), nil
	}
	h.requests[req.IdempotencyKey] = copyValue(req)
	actor := req.Identity
	if req.Slot.Resolution == teams.Fresh {
		actor = mesh.URN("msg://agent/memory/" + req.MemberID)
	}
	if req.Slot.Resolution == teams.Fresh {
		h.enrollments[actor] = true
	}
	if !h.enrollments[actor] {
		return teams.Member{}, teams.ErrProvisionFailed
	}
	if binding := h.actorBindings[actor]; binding != "" && binding != req.IdempotencyKey {
		return teams.Member{}, teams.ErrProvisionFailed
	}
	h.actorBindings[actor] = req.IdempotencyKey
	m := teams.Member{Enrolled: true, Ephemeral: req.Slot.Resolution == teams.Fresh, ID: req.MemberID, Slot: req.Slot.Name, Actor: actor, Kind: mesh.ActorAgent, AgentID: string(actor), SessionID: "session-" + req.MemberID, Status: "active", Governance: teams.MemberRole, Resolution: req.Slot.Resolution, Parent: req.Parent, SpawnCapable: h.SpawnCapabilities[req.Slot.Name], Budget: req.Limits.Budget, Idle: true, JoinedAt: h.NowTime}
	// Pool identities are explicitly enrolled by the test host; a binding
	// prevents two active sessions from sharing the same actor.
	h.provisions[req.IdempotencyKey] = copyValue(m)
	return copyValue(m), h.after("provision", req.IdempotencyKey)
}
func (h *Host) LaunchWorkflow(ctx context.Context, key string, definition teams.WorkflowDefinition) (string, error) {
	if err := h.before(ctx, "workflow", key); err != nil {
		return "", err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, failed := h.failedWorkflows[key]; failed {
		return "", teams.ErrLaunchFailed
	}
	if old, ok := h.workflowDefinitions[key]; ok && !reflect.DeepEqual(old, definition) {
		return "", teams.ErrConflict
	}
	if id, ok := h.workflows[key]; ok {
		return id, nil
	}
	id := "run-" + key
	h.workflows[key] = id
	h.workflowDefinitions[key] = copyValue(definition)
	return id, h.after("workflow", key)
}
func (h *Host) InstallRouting(ctx context.Context, key string, run teams.TeamRun, r teams.Routing) error {
	if err := h.before(ctx, "routing", run.ID); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.routing[run.ID]; ok && !reflect.DeepEqual(old, r) {
		return teams.ErrConflict
	}
	h.routing[run.ID] = copyValue(r)
	return h.after("routing", run.ID)
}
func (h *Host) RemoveRouting(ctx context.Context, runID string) error {
	if err := h.before(ctx, "remove_routing", runID); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.routing, runID)
	return nil
}
func (h *Host) SendMessage(ctx context.Context, d teams.Delivery) error {
	if err := h.before(ctx, "send", d.Recipient.ID); err != nil {
		return err
	}
	h.rosterMu.Lock()
	defer h.rosterMu.Unlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.deliveries[d.IdempotencyKey]; ok {
		if !reflect.DeepEqual(old, d) {
			return teams.ErrConflict
		}
		return h.after("send", d.Recipient.ID)
	}
	if d.Verb == mesh.Reply && d.Route.Verb == mesh.Delegate {
		original, ok := h.deliveries[d.InReplyTo]
		if !ok || original.Verb != mesh.Delegate {
			return teams.ErrNotFound
		}
		state := h.delegationStates[d.InReplyTo]
		if !state.Valid() || state.Terminal() {
			return teams.ErrConflict
		}
		roster := h.rosters[d.Route.RunID]
		for _, wanted := range []teams.Member{original.Route.Sender, original.Recipient} {
			found := false
			for _, live := range roster.Members {
				if live.ID == wanted.ID && live.Actor == wanted.Actor && live.SessionID == wanted.SessionID && live.Status == "active" {
					found = true
				}
			}
			if !found {
				return teams.ErrUnavailable
			}
		}
		h.delegationStates[d.InReplyTo] = mesh.TaskCompleted
	}
	if d.Verb == mesh.Delegate {
		h.delegationStates[d.IdempotencyKey] = mesh.TaskWorking
	}
	h.deliveries[d.IdempotencyKey] = copyValue(d)
	return h.after("send", d.Recipient.ID)
}

func (h *Host) GetSignal(ctx context.Context, runID, phaseID string) (teams.SignalResolution, error) {
	if err := h.before(ctx, "get_signal", runID); err != nil {
		return teams.SignalResolution{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	r, ok := h.signals[runID+"/"+phaseID]
	if !ok {
		return r, teams.ErrNotFound
	}
	return copyValue(r), nil
}
func (h *Host) ResolveSignal(ctx context.Context, r teams.SignalResolution) (teams.SignalResolution, error) {
	if err := h.before(ctx, "signal", r.RunID); err != nil {
		return teams.SignalResolution{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	key := r.RunID + "/" + r.PhaseID
	if old, ok := h.signals[key]; ok {
		return copyValue(old), nil
	}
	h.signals[key] = copyValue(r)
	return copyValue(r), h.after("signal", r.RunID)
}
func (h *Host) Evaluate(ctx context.Context, t teams.Trigger, m teams.Member) (bool, error) {
	if err := h.before(ctx, "trigger", m.ID); err != nil {
		return false, err
	}
	return h.TriggerFired, nil
}
func (h *Host) ResolveTrust(ctx context.Context, m teams.Member, s teams.Slot) (teams.TrustDecision, error) {
	if err := h.before(ctx, "trust", m.ID); err != nil {
		return "", err
	}
	return h.Trust, nil
}
func (h *Host) EmitApproval(ctx context.Context, key string, m teams.Member, s teams.Slot) error {
	if err := h.before(ctx, "approval", key); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, old := range h.Approvals {
		if old == key {
			return nil
		}
	}
	h.Approvals = append(h.Approvals, key)
	return nil
}
func (h *Host) EnrollIdentity(ctx context.Context, actor mesh.URN) error {
	if err := (mesh.Actor{URN: actor, Kind: mesh.ActorAgent}).Validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.enrollments[actor] = true
	return nil
}
func (h *Host) Enrolled(actor mesh.URN) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.enrollments[actor]
}
func (h *Host) FailWorkflow(ctx context.Context, key, reason string) error {
	if err := h.before(ctx, "fail_workflow", key); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failedWorkflows[key] = reason
	return h.after("fail_workflow", key)
}
func (h *Host) Retire(ctx context.Context, key string, m teams.Member) error {
	return h.end(ctx, key, m, "retired")
}
func (h *Host) Release(ctx context.Context, key string, m teams.Member) error {
	return h.end(ctx, key, m, "released")
}
func (h *Host) end(ctx context.Context, key string, m teams.Member, status string) error {
	if err := h.before(ctx, status, m.ID); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.ended[key]; ok && old != status {
		return teams.ErrConflict
	}
	if m.Intent != nil {
		h.canceled[m.Intent.IdempotencyKey] = true
		if provisioned, ok := h.provisions[m.Intent.IdempotencyKey]; ok {
			delete(h.actorBindings, provisioned.Actor)
			if status == "retired" {
				h.enrollments[provisioned.Actor] = false
			}
			provisioned.Status = status
			h.provisions[m.Intent.IdempotencyKey] = provisioned
		}
	}
	h.ended[key] = status
	return h.after(status, m.ID)
}
func (h *Host) Now() time.Time { return h.NowTime }
func (h *Host) NewID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next++
	return fmt.Sprintf("memory-%d", h.next)
}
func (h *Host) Deliveries() []teams.Delivery {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []teams.Delivery
	for _, d := range h.deliveries {
		out = append(out, copyValue(d))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Recipient.ID < out[j].Recipient.ID })
	return out
}
func (h *Host) Provisioned() []teams.Member {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []teams.Member
	for _, m := range h.provisions {
		out = append(out, copyValue(m))
	}
	return out
}

var (
	_ teams.DefinitionStore   = (*Host)(nil)
	_ teams.RosterStore       = (*Host)(nil)
	_ teams.LaunchLedger      = (*Host)(nil)
	_ teams.SignalStore       = (*Host)(nil)
	_ teams.MemberProvisioner = (*Host)(nil)
	_ teams.WorkflowLauncher  = (*Host)(nil)
	_ teams.TriggerEvaluator  = (*Host)(nil)
	_ teams.MessageSender     = (*Host)(nil)
	_ teams.RoutingInstaller  = (*Host)(nil)
	_ teams.TrustResolver     = (*Host)(nil)
	_ teams.ApprovalEmitter   = (*Host)(nil)
	_ teams.Clock             = (*Host)(nil)
	_ teams.IDs               = (*Host)(nil)
)

func (h *Host) SnapshotAt(ctx context.Context, runID string, version uint64) (teams.Roster, error) {
	if err := h.before(ctx, "snapshot_at", runID); err != nil {
		return teams.Roster{}, err
	}
	h.rosterMu.Lock()
	defer h.rosterMu.Unlock()
	r, ok := h.rosterHistory[runID][version]
	if !ok {
		return r, teams.ErrNotFound
	}
	return copyValue(r), nil
}
func (h *Host) BindMessage(ctx context.Context, key, digest string) error {
	if err := h.before(ctx, "bind_message", key); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if prior, ok := h.bindings[key]; ok && prior != digest {
		return teams.ErrConflict
	}
	h.bindings[key] = digest
	return nil
}
func (h *Host) RecordSignal(ctx context.Context, record teams.PhaseSignalRecord) error {
	if err := h.before(ctx, "record_signal", record.RunID); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	key := record.RunID + "/" + record.PhaseID
	for _, old := range h.phaseSignals[key] {
		if old.Actor == record.Actor {
			return nil
		}
	}
	h.phaseSignals[key] = append(h.phaseSignals[key], record)
	return nil
}
func (h *Host) ListSignals(ctx context.Context, runID, phaseID string) ([]teams.PhaseSignalRecord, error) {
	if err := h.before(ctx, "list_signals", runID); err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return copyValue(h.phaseSignals[runID+"/"+phaseID]), nil
}

func (h *Host) GetDelivery(ctx context.Context, key string) (teams.Delivery, error) {
	if err := ctx.Err(); err != nil {
		return teams.Delivery{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	d, ok := h.deliveries[key]
	if !ok {
		return teams.Delivery{}, teams.ErrNotFound
	}
	return copyValue(d), nil
}

// DelegationState returns the test host's current delegation task state.
func (h *Host) DelegationState(ctx context.Context, key string) (mesh.TaskState, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, ok := h.delegationStates[key]
	if !ok {
		return "", teams.ErrNotFound
	}
	return state, nil
}

// EndDelegation simulates a host task ending independently of result delivery.
func (h *Host) EndDelegation(ctx context.Context, key string, state mesh.TaskState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !state.Terminal() {
		return fmt.Errorf("delegation requires a terminal state")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	current, ok := h.delegationStates[key]
	if !ok {
		return teams.ErrNotFound
	}
	if current.Terminal() && current != state {
		return teams.ErrConflict
	}
	h.delegationStates[key] = state
	return nil
}
