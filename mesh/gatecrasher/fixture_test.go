package gatecrasher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/libs/mesh/core/gate"
	"github.com/hollis-labs/libs/mesh/core/gate/gatetest"
	"github.com/hollis-labs/libs/mesh/core/hitl"
	"github.com/hollis-labs/libs/mesh/core/hitl/memstore"
	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
	"github.com/hollis-labs/libs/security/policy"
)

var errCrash = errors.New("simulated process loss")

// imageJournal is a restartable FAKE: each operation reads/writes a serialized
// image. Reconstructing from image loses all wrapper state. It is not production
// persistence and makes no fsync, multi-process or production isolation claims.
type imageJournal struct {
	mu                    sync.Mutex
	image                 []byte
	failReserve, failSwap bool
}

func newJournal() *imageJournal { return &imageJournal{image: []byte(`{}`)} }
func (j *imageJournal) read() map[string]Route {
	var m map[string]Route
	if err := json.Unmarshal(j.image, &m); err != nil {
		panic(err)
	}
	return m
}
func (j *imageJournal) write(m map[string]Route) {
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	j.image = b
}
func (j *imageJournal) restart() *imageJournal {
	j.mu.Lock()
	defer j.mu.Unlock()
	return &imageJournal{image: slices.Clone(j.image)}
}
func (j *imageJournal) Reserve(ctx context.Context, intent Route) (Route, error) {
	if err := ctx.Err(); err != nil {
		return Route{}, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.failReserve {
		j.failReserve = false
		return Route{}, errCrash
	}
	r := intent.Request
	m := j.read()
	if old, ok := m[r.IdempotencyKey]; ok {
		d, _ := r.Digest()
		od, _ := old.Request.Digest()
		if d != od || old.OwnerRef != intent.OwnerRef || old.CallerScope != intent.CallerScope {
			return Route{}, ErrConflict
		}
		return old, nil
	}
	detached, err := r.Snapshot()
	if err != nil {
		return Route{}, err
	}
	route := Route{Version: 1, Request: detached, OwnerRef: intent.OwnerRef, CallerScope: intent.CallerScope}
	m[r.IdempotencyKey] = route
	j.write(m)
	return j.read()[r.IdempotencyKey], nil
}
func (j *imageJournal) Load(ctx context.Context, key string) (Route, error) {
	if err := ctx.Err(); err != nil {
		return Route{}, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	r, ok := j.read()[key]
	if !ok {
		return Route{}, ErrNotFound
	}
	return r, nil
}
func (j *imageJournal) Find(ctx context.Context, t gate.Ticket) (Route, error) {
	if err := ctx.Err(); err != nil {
		return Route{}, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, r := range j.read() {
		if r.Ticket != nil && reflect.DeepEqual(*r.Ticket, t) {
			return r, nil
		}
	}
	return Route{}, ErrNotFound
}
func (j *imageJournal) FindDecision(ctx context.Context, ref string, b runtimecontract.Binding) (Route, error) {
	if err := ctx.Err(); err != nil {
		return Route{}, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, r := range j.read() {
		if r.DecisionRef == ref && r.Request.Binding == b {
			return r, nil
		}
	}
	return Route{}, ErrNotFound
}
func (j *imageJournal) Swap(ctx context.Context, key string, version uint64, r Route) (Route, error) {
	if err := ctx.Err(); err != nil {
		return Route{}, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.failSwap {
		j.failSwap = false
		return Route{}, errCrash
	}
	m := j.read()
	old, ok := m[key]
	if !ok {
		return Route{}, ErrNotFound
	}
	if old.Version != version {
		return Route{}, ErrCAS
	}
	d, _ := r.Request.Digest()
	od, _ := old.Request.Digest()
	if d != od || r.OwnerRef != old.OwnerRef || r.CallerScope != old.CallerScope || r.Version != version || old.Ticket != nil && !reflect.DeepEqual(old.Ticket, r.Ticket) || old.DecisionRef != "" && old.DecisionRef != r.DecisionRef {
		return Route{}, ErrConflict
	}
	r.Version++
	m[key] = r
	j.write(m)
	return j.read()[key], nil
}
func (j *imageJournal) List(ctx context.Context, cursor string, limit int) ([]Route, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	m := j.read()
	keys := []string{}
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	start := 0
	if cursor != "" {
		var err error
		start, err = strconv.Atoi(cursor)
		if err != nil || start < 0 || start > len(keys) {
			return nil, "", gate.ErrInvalid
		}
	}
	end := min(start+limit, len(keys))
	out := []Route{}
	for _, k := range keys[start:end] {
		out = append(out, m[k])
	}
	next := ""
	if end < len(keys) {
		next = strconv.Itoa(end)
	}
	return out, next, nil
}

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) get() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *testClock) advance(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.now = c.now.Add(d) }

type pdpFake struct {
	decision policy.Decision
	err      error
}

func (p *pdpFake) Evaluate(context.Context, policy.Request) (policy.Decision, error) {
	return p.decision, p.err
}

type auditFake struct {
	mu     sync.Mutex
	events []policy.AuditEvent
	fail   bool
}

func (a *auditFake) Record(_ context.Context, e policy.AuditEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fail {
		return errCrash
	}
	a.events = append(a.events, e)
	return nil
}

type policyBinding struct{}

func (policyBinding) Facts(_ context.Context, r gate.Request) (policy.Request, error) {
	return policy.Request{Principal: policy.Principal{URN: "urn:operative:one", Kind: "operative"}, RequestID: r.IdempotencyKey, Scope: r.EnforcementPoint, Action: "tool.execute", Resource: policy.Resource{Type: "tool", ID: r.OperationID}}, nil
}
func (policyBinding) CheckIntent(_ context.Context, r gate.Request, d policy.Decision) error {
	if r.Policy != gatetest.Reference("policy-1") || !reflect.DeepEqual(r.Provenance, []runtimecontract.Reference{gatetest.Reference("rule-1")}) {
		return gate.ErrBinding
	}
	want := []gate.ObligationRef{}
	for _, o := range d.Obligations {
		want = append(want, gate.ObligationRef{Kind: string(o.Kind), Ref: gatetest.Reference(o.Reference)})
	}
	if !slices.Equal(r.Obligations, want) {
		return gate.ErrBinding
	}
	return nil
}

type principalKey struct{}

func human() context.Context {
	return context.WithValue(context.Background(), principalKey{}, "urn:human:alice")
}

type authorityFake struct {
	service *hitl.Service
	deny    bool
}

func (a *authorityFake) Authorize(ctx context.Context, r gate.Request, t gate.Ticket, input ResponseAttempt) (hitl.Participant, error) {
	principal, _ := ctx.Value(principalKey{}).(string)
	if a.deny || principal != "urn:human:alice" || input.Participant.PrincipalRef == string(r.Binding.OperativeID) || r.Policy.IssuerRef != "fake-host" || t.AuthorityRef != "fake-owner" {
		return hitl.Participant{}, gate.ErrAuthority
	}
	// Untrusted participant/proof is not copied into the recorded identity.
	return hitl.Participant{PrincipalRef: principal, Authority: "fake-host-verified", Assurance: "fake-presence"}, nil
}
func (a *authorityFake) Verify(ctx context.Context, r gate.Request, d gate.Decision) error {
	if a.deny || d.AuthorityRef != "fake-owner" || d.Ticket.AuthorityRef != "fake-owner" || r.Policy.IssuerRef != "fake-host" || d.Resolution.Participant.PrincipalRef != "urn:human:alice" || d.Resolution.Participant.Authority != "fake-host-verified" {
		return gate.ErrAuthority
	}
	v, err := a.service.Get(ctx, hitl.GetCommand{ContractVersion: hitl.ContractVersion, ItemID: d.Ticket.Handle.ItemID, Caller: hitl.CallerAssertion{ApplicationID: "fake-app"}})
	if err != nil {
		return err
	}
	o, ok := v.Item.TerminalOutcome.(hitl.Resolved)
	if !ok || !reflect.DeepEqual(o.Resolution, d.Resolution) || o.Resolution.Response.Decision != d.OptionID {
		return gate.ErrAuthority
	}
	return nil
}

type originFake struct {
	mu             sync.Mutex
	live           gate.Request
	clock          *testClock
	cap            gate.Capabilities
	sends          int
	accepted       map[string]bool
	mode           string // ack, lost, sent, crash-before-send
	reconciliation string // auto, unknown, wrong-id
	afterSend      func()
	beforeWrite    func()
	fence          func(gate.Request, gate.Resolution) error
}

func (o *originFake) Capabilities(_ context.Context, _ gate.AdapterScope) (gate.Capabilities, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.cap, nil
}
func (o *originFake) validate(r gate.Request) error {
	a, _ := r.Digest()
	b, _ := o.live.Digest()
	if a != b {
		return gate.ErrBinding
	}
	if !o.clock.get().Before(r.ExpiresAt) {
		return gate.ErrExpired
	}
	return nil
}
func (o *originFake) Validate(_ context.Context, r gate.Request) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.validate(r)
}
func receipt(r gate.Request, res gate.Resolution, s gate.DeliveryStatus) gate.DeliveryReceipt {
	return gate.DeliveryReceipt{DecisionRef: resolutionRef(res), Binding: r.Binding, AdapterID: r.Adapter.AdapterID, NativeRequestID: r.NativeRequestID, Status: s}
}
func (o *originFake) Send(_ context.Context, r gate.Request, res gate.Resolution) (gate.DeliveryReceipt, error) {
	if o.beforeWrite != nil {
		o.beforeWrite()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.validate(r); err != nil {
		return gate.DeliveryReceipt{}, err
	}
	if o.accepted[resolutionRef(res)] {
		return receipt(r, res, gate.DeliveryAcknowledged), nil
	}
	if o.fence != nil {
		if err := o.fence(r, res); err != nil {
			return gate.DeliveryReceipt{}, err
		}
	}
	o.sends++
	if o.mode == "crash-before-send" {
		return gate.DeliveryReceipt{}, errCrash
	}
	o.accepted[resolutionRef(res)] = true
	if o.afterSend != nil {
		o.afterSend()
	}
	if o.mode == "lost" {
		return gate.DeliveryReceipt{}, errCrash
	}
	if o.mode == "sent" {
		return receipt(r, res, gate.DeliverySent), nil
	}
	return receipt(r, res, gate.DeliveryAcknowledged), nil
}
func (o *originFake) Reconcile(_ context.Context, r gate.Request, res gate.Resolution) (gate.DeliveryReceipt, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.reconciliation == "unknown" {
		return receipt(r, res, gate.DeliveryUnknown), nil
	}
	if o.reconciliation == "wrong-id" {
		p := receipt(r, res, gate.DeliveryAcknowledged)
		p.NativeRequestID = gate.NativeRequestID(`"9007199254740993"`)
		return p, nil
	}
	if !o.accepted[resolutionRef(res)] && o.validate(r) != nil {
		return receipt(r, res, gate.DeliveryUnknown), nil
	}
	p := receipt(r, res, gate.DeliveryPending)
	p.EvidenceRef = "fake-native-query:no-reply"
	if o.accepted[resolutionRef(res)] {
		p.Status = gate.DeliveryAcknowledged
		p.EvidenceRef = "fake-native-query:accepted"
	}
	return p, nil
}

type fixture struct {
	c       *Coordinator
	cfg     Config
	clock   *testClock
	journal *imageJournal
	service *hitl.Service
	store   *memstore.Store
	auth    *authorityFake
	origin  *originFake
	pdp     *pdpFake
	audit   *auditFake
	request gate.Request
}

func newFixture() *fixture {
	clock := &testClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	r := gatetest.Request(clock.get())
	store := memstore.New()
	service := hitl.NewService(store, hitl.Options{Clock: clock.get, PollInterval: time.Millisecond})
	f := &fixture{clock: clock, journal: newJournal(), service: service, store: store, request: r}
	f.auth = &authorityFake{service: service}
	f.origin = &originFake{live: r, clock: clock, cap: gatetest.Capabilities(r), accepted: map[string]bool{}}
	f.pdp = &pdpFake{decision: policy.Decision{Effect: policy.Ask, MatchedPolicies: []policy.PolicyMatch{{ID: "rule-1"}}, Obligations: []policy.Obligation{{Kind: policy.Approval, PolicyID: "rule-1", Reference: "approval-rule-1"}}}}
	f.audit = &auditFake{}
	f.cfg = Config{OwnerRef: "fake-owner", CallerScope: "fake-app", Interactions: service, Journal: f.journal, Policy: PolicyPort{Evaluator: policy.Evaluator{PDP: f.pdp, Audit: f.audit}, Options: policy.Options{Mode: policy.Enforce, FailMode: policy.FailClosed}, Binding: policyBinding{}}, Responses: f.auth, Verifier: f.auth, Origin: f.origin, Clock: clock.get, LeaseDuration: time.Second}
	var err error
	f.c, err = New(f.cfg)
	if err != nil {
		panic(err)
	}
	return f
}
func (f *fixture) restart() {
	f.journal = f.journal.restart()
	f.service = hitl.NewService(f.store, hitl.Options{Clock: f.clock.get, PollInterval: time.Millisecond})
	f.auth.service = f.service
	f.cfg.Interactions = f.service
	f.cfg.Journal = f.journal
	var err error
	f.c, err = New(f.cfg)
	if err != nil {
		panic(err)
	}
}
func (f *fixture) ticket(t testing.TB) gate.Ticket {
	t.Helper()
	v, err := f.c.Request(human(), f.request)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func (f *fixture) respond(t testing.TB, ticket gate.Ticket, option string) gate.Resolution {
	t.Helper()
	v, err := f.c.Respond(human(), ticket, ResponseAttempt{OptionID: option, ExpectedRevision: ticket.Handle.Revision})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// fakeExecutor is the local SDK-shaped enforcement boundary, not coordinator
// delivery. Its mutex atomically claims an exact operation once before effect.
// A crash after effect leaves the claim consumed until host reconciliation.
type fakeExecutor struct {
	mu      sync.Mutex
	claimed map[string]bool
	effects int
}

func (e *fakeExecutor) execute(ctx context.Context, r gate.Request, d gate.Decision, live gate.Admission, v gate.AuthorityVerifier) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := gate.ValidateAdmission(ctx, r, d, live, v); err != nil {
		return err
	}
	if e.claimed == nil {
		e.claimed = map[string]bool{}
	}
	key := fmt.Sprintf("%s/%s/%s", d.Resolution.ResolutionID, r.OperationID, r.Binding.AttemptID)
	if e.claimed[key] {
		return gate.ErrDenied
	}
	e.claimed[key] = true
	e.effects++
	return nil
}
func admission(f *fixture) gate.Admission {
	r := f.request
	return gate.Admission{Binding: r.Binding, Adapter: r.Adapter, EnforcementPoint: r.EnforcementPoint, OperationDigest: r.OperationDigest, Now: f.clock.get()}
}
