package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

var (
	ErrStale           = errors.New("registry: stale catalog or host instance")
	ErrNeedsRevocation = errors.New("registry: revoke old generation before replacement")
	ErrRevoked         = errors.New("registry: scope revoked")
)

// Catalog publishes complete validated snapshots. It never owns a plugin process.
// Generation replacements require explicit Revoke followed by host disposal.
type Catalog struct {
	mu        sync.RWMutex
	response  Response
	listeners map[uint64]func()
	next      uint64
	revoked   map[string]bool
}

func NewCatalog(hostInstance string) *Catalog {
	return &Catalog{response: NewResponse(hostInstance, 1), listeners: map[uint64]func(){}, revoked: map[string]bool{}}
}
func clone(r Response) Response {
	out, _ := cloneChecked(r)
	return out
}
func cloneChecked(r Response) (Response, error) {
	raw, err := json.Marshal(r)
	if err != nil {
		return Response{}, ErrInvalidContribution
	}
	var out Response
	if err := json.Unmarshal(raw, &out); err != nil {
		return Response{}, err
	}
	return out, nil
}
func (c *Catalog) Snapshot() Response { c.mu.RLock(); defer c.mu.RUnlock(); return clone(c.response) }
func (c *Catalog) Subscribe(fn func()) func() {
	c.mu.Lock()
	c.next++
	id := c.next
	c.listeners[id] = fn
	c.mu.Unlock()
	return func() { c.mu.Lock(); delete(c.listeners, id); c.mu.Unlock() }
}
func (c *Catalog) callbacks() []func() {
	out := make([]func(), 0, len(c.listeners))
	for _, fn := range c.listeners {
		out = append(out, fn)
	}
	return out
}
func notify(callbacks []func()) {
	for _, fn := range callbacks {
		func() { defer func() { _ = recover() }(); fn() }()
	}
}
func (c *Catalog) Activate(response Response, policy AdmissionPolicy) (Plan, error) {
	candidate, err := cloneChecked(response)
	if err != nil {
		return Plan{}, err
	}
	plan, err := candidate.Plan(policy)
	if err != nil {
		return plan, err
	}
	candidate.Contributions = map[string]map[string]Contribution{}
	for _, entry := range plan.Accepted {
		_ = candidate.Set(entry)
	}
	candidate.Refusals = plan.Refusals
	c.mu.Lock()
	if candidate.HostInstance != c.response.HostInstance || candidate.Revision <= c.response.Revision {
		c.mu.Unlock()
		return plan, ErrStale
	}
	for id := range c.response.Plugins {
		if _, exists := candidate.Plugins[id]; !exists {
			c.mu.Unlock()
			return plan, ErrNeedsRevocation
		}
	}
	for id, p := range candidate.Plugins {
		if c.revoked[id+"\x00"+p.OwnerGeneration] {
			c.mu.Unlock()
			return plan, ErrRevoked
		}
		if old, ok := c.response.Plugins[id]; ok && (old.OwnerGeneration != p.OwnerGeneration || old.BundleVersion != p.BundleVersion || old.BundleURL != p.BundleURL) {
			c.mu.Unlock()
			return plan, ErrNeedsRevocation
		}
	}
	c.response = candidate
	callbacks := c.callbacks()
	c.mu.Unlock()
	notify(callbacks)
	return plan, nil
}

// Revoke affects only the named generation and publishes its absence immediately.
func (c *Catalog) Revoke(owner, generation string) error {
	if !name.MatchString(owner) || generation == "" {
		return ErrInvalidContribution
	}
	c.mu.Lock()
	p, exists := c.response.Plugins[owner]
	if !exists {
		refusals := removeRefusals(c.response.Refusals, owner, generation)
		changed := len(refusals) != len(c.response.Refusals)
		if changed && c.response.Revision == MaxRevision {
			c.mu.Unlock()
			return ErrStale
		}
		c.revoked[owner+"\x00"+generation] = true
		if changed {
			c.response.Refusals = refusals
			c.response.Revision++
			callbacks := c.callbacks()
			c.mu.Unlock()
			notify(callbacks)
		} else {
			c.mu.Unlock()
		}
		return nil
	}
	if p.OwnerGeneration != generation {
		c.mu.Unlock()
		return ErrStale
	}
	if c.response.Revision == MaxRevision {
		c.mu.Unlock()
		return ErrStale
	}
	c.revoked[owner+"\x00"+generation] = true
	delete(c.response.Plugins, owner)
	for kind, entries := range c.response.Contributions {
		for key, entry := range entries {
			if entry.OwnerID == owner && entry.OwnerGeneration == generation {
				delete(entries, key)
			}
		}
		if len(entries) == 0 {
			delete(c.response.Contributions, kind)
		}
	}
	c.response.Refusals = removeRefusals(c.response.Refusals, owner, generation)
	c.response.Revision++
	callbacks := c.callbacks()
	c.mu.Unlock()
	notify(callbacks)
	return nil
}

func removeRefusals(entries []Refusal, owner, generation string) []Refusal {
	out := make([]Refusal, 0, len(entries))
	for _, refusal := range entries {
		if refusal.OwnerID != owner || refusal.OwnerGeneration != generation {
			out = append(out, refusal)
		}
	}
	return out
}

// Scope fences admission before reverse-order cleanup. Callbacks must cooperate
// with context cancellation; this cannot forcibly stop arbitrary in-process code.
type Scope struct {
	HostInstance, OwnerID, OwnerGeneration string
	mu                                     sync.Mutex
	ctx                                    context.Context
	cancel                                 context.CancelFunc
	revoked, disposing                     bool
	disposers                              []func(context.Context) error
	done                                   chan struct{}
	report                                 error
}

func NewScope(hostInstance, owner, generation string) (*Scope, error) {
	if hostInstance == "" || !name.MatchString(owner) || generation == "" {
		return nil, ErrInvalidContribution
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Scope{HostInstance: hostInstance, OwnerID: owner, OwnerGeneration: generation, ctx: ctx, cancel: cancel, done: make(chan struct{})}, nil
}
func (s *Scope) Context() context.Context { return s.ctx }
func (s *Scope) Active() bool             { s.mu.Lock(); defer s.mu.Unlock(); return !s.revoked }
func (s *Scope) Add(disposer func(context.Context) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked {
		return ErrRevoked
	}
	if disposer == nil {
		return ErrInvalidContribution
	}
	s.disposers = append(s.disposers, disposer)
	return nil
}
func (s *Scope) Revoke() { s.mu.Lock(); s.revoked = true; s.cancel(); s.mu.Unlock() }
func (s *Scope) Dispose(ctx context.Context) error {
	s.Revoke()
	s.mu.Lock()
	if s.disposing {
		done := s.done
		s.mu.Unlock()
		select {
		case <-done:
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.report
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.disposing = true
	callbacks := s.disposers
	s.disposers = nil
	s.mu.Unlock()
	var failures []error
	for i := len(callbacks) - 1; i >= 0; i-- {
		if err := callDisposer(ctx, callbacks[i]); err != nil {
			failures = append(failures, err)
		}
	}
	s.mu.Lock()
	s.report = errors.Join(failures...)
	close(s.done)
	report := s.report
	s.mu.Unlock()
	return report
}
func callDisposer(ctx context.Context, fn func(context.Context) error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("registry: disposer panic: %v", p)
		}
	}()
	return fn(ctx)
}

// Admit returns a cancelled-on-revoke context for a call admitted under this generation.
func (s *Scope) Admit() (context.Context, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked {
		return nil, ErrRevoked
	}
	return s.ctx, nil
}
