package hosttest

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/hollis-labs/plugin-sdk/capability"
	"github.com/hollis-labs/plugin-sdk/capability/host"
)

// Adapter opens a fresh isolated host fixture for each probe. Open must use the
// host's real policy/binding/credential/route adapters. The supplied observer
// instruments the backend and audit sink; it does not decide authorization.
type Adapter interface {
	Open(context.Context, Fixture, *Observer) (Instance, error)
}

// Fixture is trusted test configuration, never plugin input. Hosts provision
// the canonical resource identifiers here in their own test stores.
type Fixture struct {
	Runtime         capability.RuntimeIdentity
	Grants          capability.GrantSet
	Catalog         []capability.Descriptor
	Overrides       []capability.Descriptor // Attempts to overwrite catalog ownership.
	Policy          capability.Scope
	CallerPolicy    *capability.Scope
	Caller          *host.Subject
	UnsafeInstall   bool
	Gate            *Gate
	AfterCommit     bool
	FailAfterCommit bool
	AuditFailure    bool
	Secret          string
	InitIdentity    json.RawMessage
}

// Attempt is an untrusted request. Direct reaches the raw host/* dispatcher
// without an SDK client helper. Bridge reaches the non-plugin loopback bridge.
// ClaimedCaller and EffectHint must not manufacture permission.
type Attempt struct {
	Call                     host.Call
	BindingID                string
	Credential               string
	Direct, Bridge           bool
	ClaimedCaller            string
	EffectHint               capability.Effect
	Origin                   string
	Redirect, Proxy          bool
	BodyBytes, ResponseBytes int64
}

// Reply retains the application error's outer code/data as observed on the
// actual entry point. A denied application call returns Failure, not Go error.
// Go errors mean the adapter/transport itself failed and cannot prove refusal.
type Reply struct {
	RPCCode int
	Data    json.RawMessage `json:"-"` // Received error.data bytes, or serialization of a local pre-send refusal.
	Failure *capability.RPCErrorData
	Tools   []string
}

// Change is a trusted host control operation used between or during calls.
// It must act on the real ledger/lifecycle/policy, not alter a canned reply.
type Change struct {
	Kind  string
	Value string
}

// Instance is the host fixture behind its actual entry points. Activate runs
// handshake/planning before activation. Artifacts returns captured log,
// registry and browser output, not a synthesized redaction report. Access
// returns the host-issued connection binding and a non-plugin credential.
// Methods must honor ctx, be concurrent-safe and return promptly. Close must
// release blocked workers and all fixture resources even after a failed probe.
type Instance interface {
	Descriptors(context.Context) ([]capability.Descriptor, error)
	Activate(context.Context, json.RawMessage, []host.Request) (capability.GrantSet, []host.PlanNotice, error)
	Access() (bindingID, credential string)
	Invoke(context.Context, Attempt) (Reply, error)
	Change(context.Context, Change) error
	Artifacts(context.Context) (map[string]string, error)
	Close() error
}

// Gate pauses real work between admission and commit (or after a definite
// commit). Wait intentionally retains work after cancellation until Release,
// allowing the suite to detect premature release of a running reservation.
type Gate struct {
	Entered                            chan struct{}
	Cancelled                          chan struct{}
	proceed                            chan struct{}
	enterOnce, cancelOnce, releaseOnce sync.Once
}

func NewGate() *Gate {
	return &Gate{Entered: make(chan struct{}), Cancelled: make(chan struct{}), proceed: make(chan struct{})}
}
func (g *Gate) Wait(ctx context.Context) {
	g.enterOnce.Do(func() { close(g.Entered) })
	select {
	case <-g.proceed:
		return
	case <-ctx.Done():
		g.cancelOnce.Do(func() { close(g.Cancelled) })
	}
	<-g.proceed
}
func (g *Gate) Release() {
	if g != nil {
		g.releaseOnce.Do(func() { close(g.proceed) })
	}
}

// Observation is measured by instrumentation at the actual backend, budget,
// activation and audit boundaries. Attempt or reply counts cannot substitute
// for backend effects: a denial reply after a mutation still fails.
type Observation struct {
	Executions         int
	ProxyRequests      int
	Activations        int
	Reserved, Released int
	Audits             []host.AuditEvent
	Calls              []host.Call
	DeliveredCallers   []host.Subject
}

type Observer struct {
	mu    sync.Mutex
	state Observation
}

func (o *Observer) Execute(c host.Call) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.state.Executions++
	c.Dimensions = cloneMap(c.Dimensions)
	c.Usage = cloneMap(c.Usage)
	o.state.Calls = append(o.state.Calls, c)
}
func (o *Observer) Activate() { o.mu.Lock(); defer o.mu.Unlock(); o.state.Activations++ }
func (o *Observer) Reserve()  { o.mu.Lock(); defer o.mu.Unlock(); o.state.Reserved++ }
func (o *Observer) Release()  { o.mu.Lock(); defer o.mu.Unlock(); o.state.Released++ }
func (o *Observer) Audit(e host.AuditEvent) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if e.InitiatingCaller != nil {
		copy := *e.InitiatingCaller
		e.InitiatingCaller = &copy
	}
	o.state.Audits = append(o.state.Audits, e)
}
func (o *Observer) Snapshot() Observation {
	o.mu.Lock()
	defer o.mu.Unlock()
	s := o.state
	s.DeliveredCallers = append([]host.Subject(nil), s.DeliveredCallers...)
	s.Audits = append([]host.AuditEvent(nil), s.Audits...)
	s.Calls = append([]host.Call(nil), s.Calls...)
	for i := range s.Calls {
		s.Calls[i].Dimensions = cloneMap(s.Calls[i].Dimensions)
		s.Calls[i].Usage = cloneMap(s.Calls[i].Usage)
	}
	for i := range s.Audits {
		if s.Audits[i].InitiatingCaller != nil {
			copy := *s.Audits[i].InitiatingCaller
			s.Audits[i].InitiatingCaller = &copy
		}
	}
	return s
}
func cloneMap[K comparable, V any](m map[K]V) map[K]V {
	out := make(map[K]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Each fresh fixture's grants use wall-clock time. Hosts may install their own
// clock and advance it for the "expire" control, without sleeping the suite.
func fixtureTimes() (string, string) {
	n := time.Now().UTC()
	return n.Add(-time.Minute).Format(time.RFC3339Nano), n.Add(time.Minute).Format(time.RFC3339Nano)
}

// ReceivedCaller records the verified identity observed at the actual fixture
// plugin receiver, independently of host-side audit metadata.
func (o *Observer) ReceivedCaller(caller host.Subject) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.state.DeliveredCallers = append(o.state.DeliveredCallers, caller)
}

// ProxyRequest instruments an attempted proxy hop, independently of its result.
func (o *Observer) ProxyRequest() { o.mu.Lock(); defer o.mu.Unlock(); o.state.ProxyRequests++ }
