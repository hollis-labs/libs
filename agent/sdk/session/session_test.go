package session

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
)

// fakeHost deliberately uses one mutex for records, leases and delivery. This
// exercises host admission at delivery instead of a Manager-side check-then-act.
// Storage implementations need equivalent transactions, plus real authentication.
type fakeHost struct {
	mu             sync.Mutex
	now            time.Time
	serial         int
	records        map[Scope]Record
	attempts       map[runtimecontract.AttemptID]bool
	leases         map[Scope]Lease
	inputs         map[string]runtimecontract.CommittedCursor
	messages       map[string]runtimecontract.CommittedCursor
	inputContent   map[string]Input
	messageContent map[string]Message
	packs          map[runtimecontract.Binding]Pack
	envs           map[runtimecontract.Binding]runtimecontract.EnvironmentRef
	issued         map[string]runtimecontract.EnvironmentRef
	prepared       []*fakeRuntime
	readyErr       error
	prepareErr     error
	activationErr  bool
	readyHook      func()
	reconcileHook  func(Pack) Pack
	checkpointHook func(Pack) Pack
	messageEntered chan struct{}
	messageRelease chan struct{}
	inputEntered   chan struct{}
	inputRelease   chan struct{}
	effects        int
	closed         int
	checkpoints    int
	verifications  int
}

func newHost() *fakeHost {
	return &fakeHost{now: time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC), records: map[Scope]Record{}, attempts: map[runtimecontract.AttemptID]bool{}, leases: map[Scope]Lease{}, inputs: map[string]runtimecontract.CommittedCursor{}, messages: map[string]runtimecontract.CommittedCursor{}, inputContent: map[string]Input{}, messageContent: map[string]Message{}, packs: map[runtimecontract.Binding]Pack{}, envs: map[runtimecontract.Binding]runtimecontract.EnvironmentRef{}, issued: map[string]runtimecontract.EnvironmentRef{}}
}

func ref(id string, owner runtimecontract.OwnerID) runtimecontract.Reference {
	return runtimecontract.Reference{ID: id, Revision: "r1", Digest: runtimecontract.Digest{Scheme: "sha256", Value: strings.Repeat("a", 64)}, IssuerRef: "fixture-host", OwnerID: owner}
}

func binding(conversation, attempt string, generation runtimecontract.Generation) runtimecontract.Binding {
	return runtimecontract.Binding{OwnerID: "owner", OperativeID: "agent", SeatID: "seat", ConversationID: runtimecontract.ConversationID(conversation), AttemptID: runtimecontract.AttemptID(attempt), Generation: generation}
}

func (h *fakeHost) environment(b runtimecontract.Binding) runtimecontract.EnvironmentRef {
	h.mu.Lock()
	defer h.mu.Unlock()
	e := runtimecontract.EnvironmentRef{Receipt: ref("env-"+string(b.OwnerID), b.OwnerID), Workspace: ref("workspace-"+string(b.ConversationID), b.OwnerID), Lease: ref("host-lease", b.OwnerID), Grants: ref("grants", b.OwnerID), Capabilities: []string{"model", "tool"}, ExpiresAt: h.now.Add(time.Hour)}
	h.issued[e.Workspace.ID] = cloneEnvironment(e)
	return e
}

func (h *fakeHost) manager(t *testing.T) *Manager {
	t.Helper()
	m, err := New(Ports{Conversations: h, Input: h, Runtimes: h, Environments: h, Recoveries: recoveryHost{h}, Reconciler: recoveryHost{h}, Transcripts: transcriptHost{h}})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (h *fakeHost) token() string { h.serial++; return fmt.Sprint(h.serial) }

func (h *fakeHost) Create(_ context.Context, r Record) (Record, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.records[scope(r.Binding)]; ok || h.attempts[r.Binding.AttemptID] {
		return Record{}, ErrConflict
	}
	r.Version = h.token()
	h.records[scope(r.Binding)] = cloneRecord(r)
	h.attempts[r.Binding.AttemptID] = true
	return cloneRecord(r), nil
}

func (h *fakeHost) Load(_ context.Context, s Scope) (Record, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r, ok := h.records[s]
	if !ok {
		return Record{}, ErrNotFound
	}
	return cloneRecord(r), nil
}

func (h *fakeHost) CompareAndSwap(_ context.Context, s Scope, version string, r Record) (Record, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	old, ok := h.records[s]
	if !ok || old.Version != version {
		return Record{}, ErrConflict
	}
	if r.Phase == Active && h.activationErr {
		return Record{}, ErrConflict
	}
	if r.Binding != old.Binding {
		if h.attempts[r.Binding.AttemptID] || scope(r.Binding) != s || r.Binding.SeatID != old.Binding.SeatID || r.Binding.Generation != old.Binding.Generation+1 || r.Parent == nil || *r.Parent != old.Binding || r.Phase != Fenced {
			return Record{}, ErrConflict
		}
		h.attempts[r.Binding.AttemptID] = true
	} else if old.Phase != Fenced || r.Phase != Active {
		return Record{}, ErrConflict
	}
	r.Version = h.token()
	h.records[s] = cloneRecord(r)
	return cloneRecord(r), nil
}

func (h *fakeHost) active(b runtimecontract.Binding) bool {
	r, ok := h.records[scope(b)]
	return ok && r.Binding == b && r.Phase == Active
}

func (h *fakeHost) Admit(_ context.Context, m Message) (runtimecontract.CommittedCursor, error) {
	if h.messageEntered != nil {
		close(h.messageEntered)
		<-h.messageRelease
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.active(m.Binding) {
		return "", ErrStale
	}
	key := fmt.Sprint(m.Binding) + m.ID
	if cursor, ok := h.messages[key]; ok {
		if h.messageContent[key] != m {
			return "", ErrConflict
		}
		return cursor, nil
	}
	cursor := runtimecontract.CommittedCursor("committed-" + h.token())
	h.messages[key] = cursor
	h.messageContent[key] = m
	return cursor, nil
}

func (h *fakeHost) Acquire(_ context.Context, b runtimecontract.Binding, w WriterID) (Lease, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.active(b) {
		return Lease{}, ErrStale
	}
	if old, ok := h.leases[scope(b)]; ok && h.now.Before(old.ExpiresAt) {
		return Lease{}, ErrLease
	}
	l := Lease{ID: LeaseID(h.token()), Binding: b, Writer: w, ExpiresAt: h.now.Add(time.Minute)}
	h.leases[scope(b)] = l
	return l, nil
}

func (h *fakeHost) Renew(_ context.Context, l Lease) (Lease, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	old, ok := h.leases[scope(l.Binding)]
	if !h.active(l.Binding) || !ok || old != l || !h.now.Before(old.ExpiresAt) {
		return Lease{}, ErrLease
	}
	old.ExpiresAt = h.now.Add(time.Minute)
	h.leases[scope(l.Binding)] = old
	return old, nil
}

func (h *fakeHost) Revoke(_ context.Context, b runtimecontract.Binding) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if l, ok := h.leases[scope(b)]; ok && l.Binding == b {
		delete(h.leases, scope(b))
	}
	return nil
}

func (h *fakeHost) Submit(_ context.Context, i Input) (runtimecontract.CommittedCursor, error) {
	if h.inputEntered != nil {
		close(h.inputEntered)
		<-h.inputRelease
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	l, ok := h.leases[scope(i.Lease.Binding)]
	if !h.active(i.Lease.Binding) || !ok || l != i.Lease || !h.now.Before(l.ExpiresAt) {
		return "", ErrLease
	}
	key := fmt.Sprint(l.Binding) + i.ID
	if c, ok := h.inputs[key]; ok {
		if h.inputContent[key].Text != i.Text {
			return "", ErrConflict
		}
		return c, nil
	}
	c := runtimecontract.CommittedCursor("committed-" + h.token())
	h.inputs[key] = c
	h.inputContent[key] = i
	return c, nil
}

func (h *fakeHost) Verify(_ context.Context, b runtimecontract.Binding, e runtimecontract.EnvironmentRef) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.verifications++
	issued, ok := h.issued[e.Workspace.ID]
	if !ok || !reflect.DeepEqual(issued, e) || e.Receipt.OwnerID != b.OwnerID || e.Workspace.ID != "workspace-"+string(b.ConversationID) {
		return ErrInvalid
	}
	if err := e.ValidateAt(h.now, []string{"model", "tool"}); err != nil {
		return err
	}
	h.envs[b] = cloneEnvironment(e)
	return nil
}

type fakeRuntime struct {
	host    *fakeHost
	request PrepareRequest
	closed  bool
}

func (h *fakeHost) Prepare(_ context.Context, p PrepareRequest) (Prepared, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.prepareErr != nil {
		return nil, h.prepareErr
	}
	if h.active(p.Binding) {
		return nil, errors.New("preparation unexpectedly active")
	}
	r := &fakeRuntime{host: h, request: p}
	h.prepared = append(h.prepared, r)
	return r, nil
}
func (r *fakeRuntime) Ready(context.Context) (runtimecontract.Binding, error) {
	h := r.host
	h.mu.Lock()
	hook := h.readyHook
	h.readyHook = nil
	err := h.readyErr
	h.mu.Unlock()
	if hook != nil {
		hook()
	}
	return r.request.Binding, err
}
func (r *fakeRuntime) Close(context.Context) error {
	r.host.mu.Lock()
	defer r.host.mu.Unlock()
	if !r.closed {
		r.closed = true
		r.host.closed++
	}
	return nil
}

type recoveryHost struct{ h *fakeHost }

func (r recoveryHost) Checkpoint(_ context.Context, b runtimecontract.Binding, _ Reason) (Pack, error) {
	h := r.h
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checkpoints++
	p, ok := h.packs[b]
	if !ok {
		e, ok := h.envs[b]
		if !ok {
			return Pack{}, ErrNotFound
		}
		plan, snapshot, pending := ref("plan-"+string(b.ConversationID), b.OwnerID), ref("snapshot", b.OwnerID), ref("pending", b.OwnerID)
		p = Pack{Locator: runtimecontract.RecoveryLocator{Binding: b, Environment: e, Workspace: e.Workspace, Transcript: ref("transcript-"+string(b.ConversationID), b.OwnerID), Plan: &plan, Snapshot: &snapshot, PendingOperations: &pending, CommittedCursor: "durable-cursor", LastCommittedTurn: "turn-1"}}
	}
	if h.checkpointHook != nil {
		p = h.checkpointHook(clonePack(p))
	}
	h.packs[b] = clonePack(p)
	return clonePack(p), nil
}
func (r recoveryHost) Load(_ context.Context, b runtimecontract.Binding) (Pack, error) {
	r.h.mu.Lock()
	defer r.h.mu.Unlock()
	p, ok := r.h.packs[b]
	if !ok {
		return Pack{}, ErrNotFound
	}
	return clonePack(p), nil
}
func (r recoveryHost) Save(_ context.Context, p Pack) error {
	r.h.mu.Lock()
	defer r.h.mu.Unlock()
	r.h.packs[p.Locator.Binding] = clonePack(p)
	return nil
}
func (r recoveryHost) Reconcile(_ context.Context, _ runtimecontract.Binding, p Pack) (Pack, error) {
	r.h.mu.Lock()
	defer r.h.mu.Unlock()
	if r.h.reconcileHook != nil {
		return r.h.reconcileHook(p), nil
	}
	for i := range p.Operations {
		if p.Operations[i].State == Started {
			p.Operations[i].State = Unknown
		}
	}
	for i := range p.Approvals {
		if p.Approvals[i].State == ApprovalPending {
			p.Approvals[i].State = Invalidated
		}
	}
	return p, nil
}

type transcriptHost struct{ h *fakeHost }

func (r transcriptHost) Locate(_ context.Context, s Scope) (runtimecontract.Reference, error) {
	r.h.mu.Lock()
	defer r.h.mu.Unlock()
	if _, ok := r.h.records[s]; !ok {
		return runtimecontract.Reference{}, ErrNotFound
	}
	return ref("transcript-"+string(s.ConversationID), s.OwnerID), nil
}
func (r transcriptHost) Search(ctx context.Context, q SearchQuery) ([]TranscriptMatch, error) {
	rf, err := r.Locate(ctx, q.Scope)
	if err != nil {
		return nil, err
	}
	return []TranscriptMatch{{Reference: rf, Cursor: "durable-cursor", Excerpt: "scoped portable text"}}, nil
}

func openFresh(t *testing.T, h *fakeHost, m *Manager, b runtimecontract.Binding) Record {
	t.Helper()
	r, err := m.Open(context.Background(), OpenRequest{Mode: Fresh, Binding: b, Environment: h.environment(b)})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func nextRequest(h *fakeHost, parent runtimecontract.Binding, attempt string, reason Reason) OpenRequest {
	b := parent
	b.Generation++
	b.AttemptID = runtimecontract.AttemptID(attempt)
	return OpenRequest{Mode: Continue, Binding: b, Parent: &parent, Environment: h.environment(b), Reason: reason}
}

func TestViewersOneWriterAndDuplicateBinding(t *testing.T) {
	ctx := context.Background()
	h := newHost()
	m := h.manager(t)
	b := binding("chat", "a1", 1)
	openFresh(t, h, m, b)
	for range 2 {
		v, err := h.manager(t).View(ctx, scope(b))
		if err != nil || v.Binding != b {
			t.Fatalf("view: %+v %v", v, err)
		}
	}
	l, err := m.AcquireInput(ctx, b, "terminal-one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.AcquireInput(ctx, b, "terminal-two"); !errors.Is(err, ErrLease) {
		t.Fatalf("two writers: %v", err)
	}
	first, err := m.Submit(ctx, Input{ID: "input-1", Lease: l, Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Submit(ctx, Input{ID: "input-1", Lease: l, Text: "hello"})
	if err != nil || first != second {
		t.Fatal("duplicate input not idempotent")
	}
	if _, err = m.Submit(ctx, Input{ID: "input-1", Lease: l, Text: "changed"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed duplicate input: %v", err)
	}
	msg := Message{Binding: b, ID: "event-1", Kind: Event, Reference: ref("event-content", b.OwnerID)}
	msgCursor, err := m.Admit(ctx, msg)
	if err != nil {
		t.Fatal(err)
	}
	if same, err := m.Admit(ctx, msg); err != nil || same != msgCursor {
		t.Fatal("duplicate event not idempotent")
	}
	msg.Reference.ID = "changed"
	if _, err = m.Admit(ctx, msg); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed duplicate event: %v", err)
	}
	if _, err = m.Open(ctx, OpenRequest{Mode: Fresh, Binding: b, Environment: h.environment(b)}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate conversation: %v", err)
	}
	duplicate := binding("other-chat", "a1", 1)
	if _, err = m.Open(ctx, OpenRequest{Mode: Fresh, Binding: duplicate, Environment: h.environment(duplicate)}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate attempt: %v", err)
	}
	h.mu.Lock()
	h.now = h.now.Add(2 * time.Minute)
	h.mu.Unlock()
	if _, err = m.RenewInput(ctx, l); !errors.Is(err, ErrLease) {
		t.Fatalf("expired renew: %v", err)
	}
	newLease, err := m.AcquireInput(ctx, b, "terminal-two")
	if err != nil || newLease.ID == l.ID {
		t.Fatalf("reacquire: %+v %v", newLease, err)
	}
	if _, err = m.Submit(ctx, Input{ID: "late", Lease: l}); !errors.Is(err, ErrLease) {
		t.Fatalf("expired input: %v", err)
	}
}

func TestDelayedOldCallbacksCheckAtDelivery(t *testing.T) {
	for _, kind := range []MessageKind{Event, Approval, "input"} {
		t.Run(string(kind), func(t *testing.T) {
			ctx := context.Background()
			h := newHost()
			m := h.manager(t)
			b := binding("chat", "a1", 1)
			openFresh(t, h, m, b)
			lease, err := m.AcquireInput(ctx, b, "writer")
			if err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			done := make(chan error, 1)
			if kind == "input" {
				h.inputEntered, h.inputRelease = entered, release
				go func() { _, err := m.Submit(ctx, Input{ID: "late-input", Lease: lease, Text: "old"}); done <- err }()
			} else {
				h.messageEntered, h.messageRelease = entered, release
				go func() {
					_, err := m.Admit(ctx, Message{Binding: b, Kind: kind, ID: "late", Reference: ref("receipt", b.OwnerID)})
					done <- err
				}()
			}
			<-entered
			next, err := m.Open(ctx, nextRequest(h, b, "a2", Crash))
			if err != nil {
				t.Fatal(err)
			}
			close(release)
			err = <-done
			if !errors.Is(err, ErrStale) && !errors.Is(err, ErrLease) {
				t.Fatalf("old delivery admitted: %v", err)
			}
			if _, err = m.RenewInput(ctx, lease); !errors.Is(err, ErrLease) {
				t.Fatalf("old lease renewed: %v", err)
			}
			if _, err = m.AcquireInput(ctx, next.Binding, "new-writer"); err != nil {
				t.Fatal(err)
			}
			if len(h.messages) != 0 || len(h.inputs) != 0 {
				t.Fatal("old callback committed")
			}
		})
	}
}

func TestQuotaAndCrashReplacementPreservesPortableHistory(t *testing.T) {
	for _, reason := range []Reason{Quota, Crash} {
		t.Run(string(reason), func(t *testing.T) {
			ctx := context.Background()
			h := newHost()
			m := h.manager(t)
			b := binding("chat", "a1", 1)
			openFresh(t, h, m, b)
			next, err := m.Open(ctx, nextRequest(h, b, "a2", reason))
			if err != nil {
				t.Fatal(err)
			}
			// A new Manager models cold boot: it must read host durable state.
			cold := h.manager(t)
			p, err := cold.Recovery(ctx, next.Binding)
			if err != nil {
				t.Fatal(err)
			}
			if p.Locator.CommittedCursor != "durable-cursor" || p.Locator.LastCommittedTurn != "turn-1" || p.Locator.Plan == nil || p.Locator.Snapshot == nil || p.Locator.PendingOperations == nil || p.Locator.Environment.Lease.ID != "host-lease" {
				t.Fatalf("lost recovery evidence: %+v", p.Locator)
			}
			runtime := h.prepared[len(h.prepared)-1]
			if runtime.request.Recovery == nil || runtime.request.Recovery.Locator.Binding != next.Binding {
				t.Fatal("portable recovery not supplied to runtime")
			}
			p.Locator.Plan.ID = "caller mutation"
			again, err := cold.Recovery(ctx, next.Binding)
			if err != nil || again.Locator.Plan.ID == "caller mutation" {
				t.Fatal("recovery alias leaked")
			}
			if h.effects != 0 {
				t.Fatal("preparation executed effects")
			}
		})
	}
}

func TestReadinessFailureStaysFencedAndExplicitRecoveryAdvances(t *testing.T) {
	ctx := context.Background()
	h := newHost()
	m := h.manager(t)
	b := binding("chat", "a1", 1)
	openFresh(t, h, m, b)
	lease, err := m.AcquireInput(ctx, b, "writer")
	if err != nil {
		t.Fatal(err)
	}
	h.readyErr = errors.New("runtime unavailable")
	failed, err := m.Open(ctx, nextRequest(h, b, "a2", Crash))
	if err == nil || failed.Phase != Fenced || h.closed != 1 {
		t.Fatalf("readiness: %+v %v closed=%d", failed, err, h.closed)
	}
	if _, err = m.Submit(ctx, Input{ID: "old", Lease: lease}); !errors.Is(err, ErrLease) {
		t.Fatalf("old input live: %v", err)
	}
	if _, err = m.AcquireInput(ctx, failed.Binding, "writer"); !errors.Is(err, ErrStale) {
		t.Fatalf("failed candidate writable: %v", err)
	}
	if _, err = m.Open(ctx, nextRequest(h, b, "a2", Crash)); !errors.Is(err, ErrStale) {
		t.Fatalf("implicit failed-attempt replay: %v", err)
	}
	h.readyErr = nil
	recovered, err := h.manager(t).Open(ctx, nextRequest(h, failed.Binding, "a3", Recovery))
	if err != nil || recovered.Phase != Active || recovered.Binding.Generation != 3 {
		t.Fatalf("explicit recovery: %+v %v", recovered, err)
	}
}

func TestCASFailureClosesOnlyCandidate(t *testing.T) {
	ctx := context.Background()
	h := newHost()
	m := h.manager(t)
	b := binding("chat", "a1", 1)
	openFresh(t, h, m, b)
	h.activationErr = true
	failed, err := m.Open(ctx, nextRequest(h, b, "a2", Quota))
	if !errors.Is(err, ErrConflict) || failed.Phase != Fenced || h.closed != 1 {
		t.Fatalf("activation: %+v %v", failed, err)
	}
	view, err := m.View(ctx, scope(b))
	if err != nil || view.Binding != failed.Binding || view.Phase != Fenced {
		t.Fatal("failed CAS unfenced runtime")
	}
	if h.prepared[0].closed {
		t.Fatal("candidate cleanup closed another runtime")
	}
}

func TestConcurrentReplacementHasOneWinner(t *testing.T) {
	h := newHost()
	m := h.manager(t)
	b := binding("chat", "a1", 1)
	openFresh(t, h, m, b)
	requests := []OpenRequest{nextRequest(h, b, "a2", Crash), nextRequest(h, b, "a3", Quota)}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, req := range requests {
		go func() { <-start; _, err := h.manager(t).Open(context.Background(), req); results <- err }()
	}
	close(start)
	winners := 0
	for range 2 {
		err := <-results
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrStale) && !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("replacement winners=%d", winners)
	}
}

func TestCASLoserCannotActivateAfterNewRecovery(t *testing.T) {
	ctx := context.Background()
	h := newHost()
	m := h.manager(t)
	b := binding("chat", "a1", 1)
	openFresh(t, h, m, b)
	req := nextRequest(h, b, "a2", Crash)
	h.readyHook = func() {
		_, err := h.manager(t).Open(ctx, nextRequest(h, req.Binding, "a3", Recovery))
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := m.Open(ctx, req)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("late preparation activated: %v", err)
	}
	current, err := m.View(ctx, scope(b))
	if err != nil || current.Binding.AttemptID != "a3" || current.Phase != Active {
		t.Fatalf("successor damaged: %+v %v", current, err)
	}
	if h.closed != 1 || h.prepared[len(h.prepared)-1].closed {
		t.Fatal("CAS loser closed successor")
	}
}

func TestUncertainEffectAndPendingApprovalNeverReplay(t *testing.T) {
	ctx := context.Background()
	h := newHost()
	m := h.manager(t)
	b := binding("chat", "a1", 1)
	openFresh(t, h, m, b)
	// External service completed, but the runtime crashed before storing success.
	h.effects = 1
	h.checkpointHook = func(p Pack) Pack {
		p.Operations = []Operation{{ID: "charge", Binding: b, Digest: ref("operation", b.OwnerID).Digest, State: Started}}
		p.Approvals = []PendingApproval{{Reference: ref("old-gate-request", b.OwnerID), Binding: b, State: ApprovalPending}}
		return p
	}
	next, err := m.Open(ctx, nextRequest(h, b, "a2", Crash))
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.Recovery(ctx, next.Binding)
	if err != nil {
		t.Fatal(err)
	}
	if p.Operations[0].State != Unknown || p.Approvals[0].State != Invalidated || h.effects != 1 {
		t.Fatalf("effect replayed or uncertainty lost: %+v effects=%d", p, h.effects)
	}
	if _, err = m.Admit(ctx, Message{Binding: b, Kind: Approval, ID: "old-decision", Reference: ref("old-gate-request", b.OwnerID)}); !errors.Is(err, ErrStale) {
		t.Fatalf("old approval accepted: %v", err)
	}
	if h.prepared[len(h.prepared)-1].request.Recovery.Operations[0].State != Unknown {
		t.Fatal("runtime did not receive uncertainty")
	}
}

func TestReconcilerCannotDropUncertainEffectOrChangeHistory(t *testing.T) {
	for _, change := range []string{"drop", "replayable", "history", "pending-approval"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			h := newHost()
			m := h.manager(t)
			b := binding("chat", "a1", 1)
			openFresh(t, h, m, b)
			h.checkpointHook = func(p Pack) Pack {
				p.Operations = []Operation{{ID: "effect", Binding: b, Digest: ref("digest", b.OwnerID).Digest, State: Unknown}}
				p.Approvals = []PendingApproval{{Reference: ref("request", b.OwnerID), Binding: b, State: ApprovalUnknown}}
				return p
			}
			h.reconcileHook = func(p Pack) Pack {
				switch change {
				case "drop":
					p.Operations = nil
				case "replayable":
					p.Operations[0].State = NotStarted
				case "history":
					p.Locator.Plan.ID = "another-plan"
				case "pending-approval":
					p.Approvals[0].State = ApprovalPending
				}
				return p
			}
			failed, err := m.Open(ctx, nextRequest(h, b, "a2", Crash))
			if !errors.Is(err, ErrInvalid) || failed.Phase != Fenced || len(h.prepared) != 1 {
				t.Fatalf("unsafe reconciliation prepared: %+v %v", failed, err)
			}
		})
	}
}

func TestModesAndExplicitParentRejectBeforeHostEffects(t *testing.T) {
	h := newHost()
	m := h.manager(t)
	b := binding("chat", "a1", 1)
	env := h.environment(b)
	for _, mode := range []Mode{Fork, Relaunch, "newest"} {
		_, err := m.Open(context.Background(), OpenRequest{Mode: mode, Binding: b, Environment: env})
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("mode %s: %v", mode, err)
		}
	}
	_, err := m.Open(context.Background(), OpenRequest{Mode: Continue, Binding: b, Environment: env, Reason: Crash})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing parent: %v", err)
	}
	if len(h.records) != 0 || len(h.prepared) != 0 || h.verifications != 0 {
		t.Fatal("unsupported mode touched host")
	}
}

func TestCrossAgentIsolationAndForgedRecovery(t *testing.T) {
	ctx := context.Background()
	h := newHost()
	m := h.manager(t)
	b := binding("chat", "a1", 1)
	openFresh(t, h, m, b)
	other := binding("other", "b1", 1)
	other.OperativeID = "another-agent"
	other.SeatID = "another-seat"
	openFresh(t, h, m, other)
	forged := scope(b)
	forged.OperativeID = other.OperativeID
	if _, err := m.View(ctx, forged); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-agent view: %v", err)
	}
	if _, err := m.SearchTranscript(ctx, SearchQuery{Scope: forged, Text: "secret", Limit: 10}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-agent search: %v", err)
	}
	search, err := m.SearchTranscript(ctx, SearchQuery{Scope: scope(b), Text: "portable", Limit: 5})
	if err != nil || search[0].Reference.ID != "transcript-chat" {
		t.Fatalf("scoped search: %+v %v", search, err)
	}
	request := nextRequest(h, b, "a2", Crash)
	request.Parent = &other
	if _, err = m.Open(ctx, request); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cross-agent parent: %v", err)
	}
	h.checkpointHook = func(p Pack) Pack {
		p.Locator.Binding = other
		p.Locator.Plan = ptrRef(ref("other-plan", "other-owner"))
		return p
	}
	if _, err = m.Open(ctx, nextRequest(h, b, "a2", Crash)); err == nil {
		t.Fatal("forged recovery accepted")
	}
	if len(h.prepared) != 2 {
		t.Fatal("cross-agent recovery reached runtime")
	}
}

func ptrRef(r runtimecontract.Reference) *runtimecontract.Reference { return &r }

func TestEnvironmentVerificationBeforeReservationAndAfterReadiness(t *testing.T) {
	for _, failure := range []string{"forged", "expired", "capability", "expires-during-ready"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			h := newHost()
			m := h.manager(t)
			b := binding("chat", "a1", 1)
			env := h.environment(b)
			switch failure {
			case "forged":
				env.Grants.ID = "forged"
			case "expired":
				h.now = env.ExpiresAt
			case "capability":
				env.Capabilities = nil
				h.issued[env.Workspace.ID] = env
			case "expires-during-ready":
				h.readyHook = func() { h.mu.Lock(); h.now = env.ExpiresAt; h.mu.Unlock() }
			}
			r, err := m.Open(ctx, OpenRequest{Mode: Fresh, Binding: b, Environment: env})
			if err == nil {
				t.Fatal("invalid environment active")
			}
			if failure == "expires-during-ready" {
				if r.Phase != Fenced || h.closed != 1 {
					t.Fatalf("readiness expiry: %+v closed=%d", r, h.closed)
				}
			} else if len(h.records) != 0 || len(h.prepared) != 0 {
				t.Fatal("invalid environment reached reservation")
			}
		})
	}
}

func TestCancelledPreparationClosesCandidateWithoutActivation(t *testing.T) {
	h := newHost()
	m := h.manager(t)
	b := binding("chat", "a1", 1)
	ctx, cancel := context.WithCancel(context.Background())
	h.readyHook = cancel
	r, err := m.Open(ctx, OpenRequest{Mode: Fresh, Binding: b, Environment: h.environment(b)})
	if !errors.Is(err, context.Canceled) || r.Phase != Fenced || h.closed != 1 {
		t.Fatalf("cancelled preparation: %+v %v closed=%d", r, err, h.closed)
	}
	if _, err = m.AcquireInput(context.Background(), b, "writer"); !errors.Is(err, ErrStale) {
		t.Fatalf("cancelled candidate active: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	other := binding("other", "other-attempt", 1)
	_, err = m.Open(ctx, OpenRequest{Mode: Fresh, Binding: other, Environment: h.environment(other)})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = m.View(context.Background(), scope(other)); !errors.Is(err, ErrNotFound) {
		t.Fatal("pre-cancelled request reserved a conversation")
	}
}

func TestPreparationFailureReservesFencedAttempt(t *testing.T) {
	h := newHost()
	m := h.manager(t)
	b := binding("chat", "a1", 1)
	h.prepareErr = errors.New("cannot prepare runtime")
	r, err := m.Open(context.Background(), OpenRequest{Mode: Fresh, Binding: b, Environment: h.environment(b)})
	if err == nil || r.Binding != b || r.Phase != Fenced || len(h.prepared) != 0 {
		t.Fatalf("prepare failure: %+v %v", r, err)
	}
	if _, err = m.AcquireInput(context.Background(), b, "writer"); !errors.Is(err, ErrStale) {
		t.Fatalf("failed preparation active: %v", err)
	}
}
