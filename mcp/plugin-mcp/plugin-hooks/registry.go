package pluginhooks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"
)

// Invocation holds private payload bytes and per-call metadata for a handler.
// Execution code supplies a separate copy to every handler.
type Invocation struct {
	ID, Hook, Owner, Generation, Registration string
	Payload                                   json.RawMessage
	Metadata                                  map[string]string
}
type ActionFunc func(context.Context, Invocation) error
type FilterFunc func(context.Context, Invocation) (json.RawMessage, error)

// Options distinguishes omitted priority/timeout from explicit zero.
type Options struct {
	Priority     *int
	Once         bool
	Timeout      *time.Duration
	OnError      ErrorPolicy
	View         string
	SchemaDigest string
}

// ResolvedOptions is the effective policy retained for introspection.
type ResolvedOptions struct {
	Priority int
	Once     bool
	Timeout  time.Duration
	OnError  ErrorPolicy
	View     string
}

// Handle is an opaque registry-specific identity. Its zero value removes nothing.
// Handles are comparable; never reconstruct them from registration names.
type Handle struct {
	registry   *Registry
	id         uint64
	generation string
}

// Registration is a detached metadata snapshot, without executable functions.
type Registration struct {
	Handle                        Handle
	Owner, Generation, Hook, Name string
	Sequence                      uint64
	Options                       ResolvedOptions
	Warnings                      []RegistrationWarning
}

// ScopeConfig is supplied by the trusted host, not by a plugin. Hooks is an
// explicit allowlist; nil permits no hooks. Remote indicates a transport scope.
type ScopeConfig struct {
	Owner, Generation string
	Hooks             []string
	Remote            bool
}
type scopeKey struct{ owner, generation string }
type scopeState struct {
	key      scopeKey
	remote   bool
	allowed  map[string]bool
	disposed bool
	active   int
	changed  chan struct{}
}
type entry struct {
	registration     Registration
	scope            *scopeState
	action           ActionFunc
	filter           FilterFunc
	removed, claimed bool
	active           map[uint64]context.CancelFunc
	removalContext   context.Context
	cancelRemoval    context.CancelFunc
}

// Registry owns immutable declarations and synchronized registration lifecycle.
// Its zero value is not usable; construct it with NewRegistry.
type Registry struct {
	mu                     sync.Mutex
	catalogVersion         string
	hostInstance           string
	definitions            map[string]Definition
	scopes                 map[scopeKey]*scopeState
	entries                map[uint64]*entry
	sequence, callSequence uint64
	engine                 *Engine
}

// NewRegistry installs a private catalog copy. Engine supplies dispatch capability.
func NewRegistry(c Catalog) (*Registry, error) {
	if c.Version == "" {
		return nil, fmt.Errorf("%w: catalog version required", ErrInvalidDefinition)
	}
	var epoch [16]byte
	if _, err := rand.Read(epoch[:]); err != nil {
		return nil, fmt.Errorf("create registry epoch: %w", err)
	}
	r := &Registry{hostInstance: hex.EncodeToString(epoch[:]), catalogVersion: c.Version, definitions: map[string]Definition{}, scopes: map[scopeKey]*scopeState{}, entries: map[uint64]*entry{}}
	for _, d := range c.Definitions {
		d = copyDefinition(d)
		if err := validateDefinition(d); err != nil {
			return nil, err
		}
		if _, ok := r.definitions[d.Name]; ok {
			return nil, fmt.Errorf("%w: %s", ErrDuplicate, d.Name)
		}
		r.definitions[d.Name] = d
	}
	return r, nil
}

// Catalog returns a detached copy of the host declarations.
func (r *Registry) Catalog() Catalog {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := Catalog{Version: r.catalogVersion}
	for _, d := range r.definitions {
		c.Definitions = append(c.Definitions, copyDefinition(d))
	}
	sort.Slice(c.Definitions, func(i, j int) bool { return c.Definitions[i].Name < c.Definitions[j].Name })
	return c
}

// Scope is a capability for one owner generation. Only the host creates scopes.
type Scope struct {
	registry *Registry
	state    *scopeState
}

// NewScope permanently reserves this owner/generation pair, including after
// disposal. Reload must supply a fresh generation. No implicit core owner exists.
func (r *Registry) NewScope(c ScopeConfig) (*Scope, error) {
	if c.Owner == "" || c.Generation == "" {
		return nil, fmt.Errorf("%w: owner and generation required", ErrUnauthorized)
	}
	allowed := map[string]bool{}
	for _, n := range c.Hooks {
		d, ok := r.definitions[n]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrUnknownHook, n)
		}
		if c.Remote && !*d.RemoteOK {
			return nil, fmt.Errorf("%w: remote %s", ErrUnauthorized, n)
		}
		allowed[n] = true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	k := scopeKey{c.Owner, c.Generation}
	if _, ok := r.scopes[k]; ok {
		return nil, ErrDuplicate
	}
	state := &scopeState{key: k, remote: c.Remote, allowed: allowed, changed: make(chan struct{})}
	r.scopes[k] = state
	return &Scope{r, state}, nil
}
func resolve(d Definition, o Options) (ResolvedOptions, error) {
	v := ResolvedOptions{Priority: 10, Once: o.Once, Timeout: d.HandlerTimeout, OnError: d.OnErrorDefault, View: o.View}
	if o.Priority != nil {
		v.Priority = *o.Priority
	}
	if o.Timeout != nil {
		v.Timeout = *o.Timeout
	}
	if o.OnError != "" {
		v.OnError = o.OnError
	}
	if v.Timeout <= 0 || v.Timeout > d.HandlerTimeout || !slices.Contains(d.AllowedOnError, v.OnError) {
		return v, ErrInvalidOptions
	}
	if v.View != "" {
		if _, ok := d.Views[v.View]; !ok {
			return v, ErrInvalidOptions
		}
	}
	if d.RequiredView != "" && v.View != d.RequiredView {
		return v, ErrUnauthorized
	}
	if o.SchemaDigest != "" && o.SchemaDigest != d.SchemaDigest {
		return v, ErrInvalidOptions
	}
	return v, nil
}
func (s *Scope) AddAction(hook, name string, o Options, f ActionFunc) (Handle, error) {
	return s.add(hook, name, o, f, nil, Action)
}
func (s *Scope) AddFilter(hook, name string, o Options, f FilterFunc) (Handle, error) {
	return s.add(hook, name, o, nil, f, Filter)
}
func (s *Scope) add(hook, name string, o Options, a ActionFunc, f FilterFunc, kind Kind) (Handle, error) {
	r := s.registry
	r.mu.Lock()
	defer r.mu.Unlock()
	policy, err := s.validateRegistrationLocked(hook, name, kind, o)
	if err != nil {
		return Handle{}, err
	}
	if a == nil && f == nil {
		return Handle{}, fmt.Errorf("%w: %s: handler required", ErrInvalidOptions, hook)
	}
	d := r.definitions[hook]
	count := 0
	for _, e := range r.entries {
		if e.scope == s.state && !e.removed && e.registration.Name == name {
			return Handle{}, ErrDuplicate
		}
		if e.registration.Hook == hook && !e.removed && !e.claimed {
			count++
		}
	}
	if count >= d.MaxHandlers {
		return Handle{}, ErrUnavailable
	}
	r.sequence++
	handle := Handle{r, r.sequence, s.state.key.generation}
	reg := Registration{Handle: handle, Owner: s.state.key.owner, Generation: s.state.key.generation, Hook: hook, Name: name, Sequence: r.sequence, Options: policy.Options, Warnings: policy.Warnings}
	removalContext, cancelRemoval := context.WithCancel(context.Background()) //nolint:gosec // Cancellation belongs to removeLocked; it is retained on the registration.
	r.entries[handle.id] = &entry{removalContext: removalContext, cancelRemoval: cancelRemoval, registration: reg, scope: s.state, action: a, filter: f, active: map[uint64]context.CancelFunc{}}
	return handle, nil
}

// Registrations lists eligible registrations by priority and sequence.
// This order describes execution only within an individual hook.
func (s *Scope) Registrations() []Registration {
	r := s.registry
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Registration
	for _, e := range r.entries {
		if e.scope == s.state && !e.removed && !e.claimed {
			reg := e.registration
			reg.Warnings = slices.Clone(reg.Warnings)
			out = append(out, reg)
		}
	}
	sortRegistrations(out)
	return out
}
func sortRegistrations(v []Registration) {
	sort.Slice(v, func(i, j int) bool {
		if v[i].Options.Priority == v[j].Options.Priority {
			return v[i].Sequence < v[j].Sequence
		}
		return v[i].Options.Priority < v[j].Options.Priority
	})
}

// Remove removes only a handle belonging to this scope. It is idempotent.
func (s *Scope) Remove(h Handle) {
	r := s.registry
	r.mu.Lock()
	var cancels []context.CancelFunc
	if e := r.entries[h.id]; h.registry == r && e != nil && e.scope == s.state && e.registration.Handle == h {
		cancels = r.removeLocked(e)
	}
	r.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// Remove is the trusted host's registry-wide removal operation.
func (r *Registry) Remove(h Handle) {
	r.mu.Lock()
	var cancels []context.CancelFunc
	if e := r.entries[h.id]; h.registry == r && e != nil && e.registration.Handle == h {
		cancels = r.removeLocked(e)
	}
	r.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}
func (r *Registry) removeLocked(e *entry) []context.CancelFunc {
	e.removed = true
	if len(e.active) == 0 {
		delete(r.entries, e.registration.Handle.id)
	}
	c := []context.CancelFunc{e.cancelRemoval}
	for _, cancel := range e.active {
		c = append(c, cancel)
	}
	return c
}

// RemoveByPlugin sweeps matching registrations, without closing the generation.
// Use Dispose to unload and permanently prohibit new registrations/starts.
func (r *Registry) RemoveByPlugin(owner, generation string) {
	r.mu.Lock()
	var c []context.CancelFunc
	for _, e := range r.entries {
		if e.scope.key == (scopeKey{owner, generation}) {
			c = append(c, r.removeLocked(e)...)
		}
	}
	r.mu.Unlock()
	for _, cancel := range c {
		cancel()
	}
}

// Dispose atomically closes this generation and cancels in-flight work, then
// waits for cooperative completion only until ctx ends. Calling again can wait
// again; a timeout never reopens the generation.
func (s *Scope) Dispose(ctx context.Context) error {
	r := s.registry
	// Read the immutable engine pointer and clock outside lifecycle locks. Retry
	// only if an engine was installed concurrently while reading the clock.
	var engine *Engine
	var now time.Time
	for {
		r.mu.Lock()
		engine = r.engine
		r.mu.Unlock()
		if engine != nil {
			now = engine.config.Clock.Now()
		}
		r.mu.Lock()
		if engine == r.engine {
			break
		}
		r.mu.Unlock()
	}
	s.state.disposed = true
	var event *BreakerEvent
	if engine != nil {
		event = engine.disposeBreakerLocked(s.state.key, now)
	}
	var c []context.CancelFunc
	for _, e := range r.entries {
		if e.scope == s.state {
			c = append(c, r.removeLocked(e)...)
		}
	}
	r.mu.Unlock()
	if engine != nil {
		engine.breakerEvent(event)
	}
	for _, cancel := range c {
		cancel()
	}
	for {
		r.mu.Lock()
		active := s.state.active
		changed := s.state.changed
		r.mu.Unlock()
		if active == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

// snapshot isolates additions; start rechecks removals and claims under the lock.
func (r *Registry) snapshot(hook string) []*entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*entry
	for _, e := range r.entries {
		if e.registration.Hook == hook && !e.removed && !e.claimed {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].registration, out[j].registration
		if a.Options.Priority == b.Options.Priority {
			return a.Sequence < b.Sequence
		}
		return a.Options.Priority < b.Options.Priority
	})
	return out
}

// lease is the lifecycle half of an invocation; execution acquires capacity
// BEFORE start and retains capacity until finish, even after a caller timeout.
type lease struct {
	registry *Registry
	entry    *entry
	id       uint64
	ctx      context.Context
	cancel   context.CancelFunc
}

func (r *Registry) start(ctx context.Context, e *entry) (*lease, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e.removed || e.claimed || e.scope.disposed {
		return nil, ErrUnavailable
	}
	callCtx, cancel := context.WithCancel(ctx)
	r.callSequence++
	id := r.callSequence
	e.active[id] = cancel
	e.scope.active++
	if e.registration.Options.Once {
		e.claimed = true
	}
	return &lease{r, e, id, callCtx, cancel}, nil
}

// finish returns false after explicit removal/disposal, but not once retirement.
func (l *lease) finish() bool {
	r := l.registry
	r.mu.Lock()
	_, active := l.entry.active[l.id]
	if active {
		delete(l.entry.active, l.id)
		l.entry.scope.active--
		if (l.entry.removed || l.entry.claimed) && len(l.entry.active) == 0 {
			delete(r.entries, l.entry.registration.Handle.id)
		}
		close(l.entry.scope.changed)
		l.entry.scope.changed = make(chan struct{})
	}
	valid := !l.entry.removed && !l.entry.scope.disposed
	r.mu.Unlock()
	l.cancel()
	return valid
}

// RegistrationWarning is returned without logging, so hosts choose their
// operator surface. It describes deprecation and never redirects registration.
type RegistrationWarning struct {
	Hook        string      `json:"hook"`
	Deprecation Deprecation `json:"deprecation"`
}

// RegistrationValidation contains resolved policy and registration warnings.
type RegistrationValidation struct {
	Options  ResolvedOptions
	Warnings []RegistrationWarning
}

// ValidateRegistration preflights a manifest declaration against this scope's
// catalog, kind, allowlist and options. It does not reserve a name or capacity;
// AddAction/AddFilter recheck policy, duplicates and capacity atomically.
func (s *Scope) ValidateRegistration(hook, name string, kind Kind, o Options) (RegistrationValidation, error) {
	s.registry.mu.Lock()
	defer s.registry.mu.Unlock()
	return s.validateRegistrationLocked(hook, name, kind, o)
}
func (s *Scope) validateRegistrationLocked(hook, name string, kind Kind, o Options) (RegistrationValidation, error) {
	var v RegistrationValidation
	fail := func(err error) (RegistrationValidation, error) { return v, fmt.Errorf("%w: %s", err, hook) }
	if s.state.disposed {
		return fail(ErrDisposed)
	}
	d, ok := s.registry.definitions[hook]
	if !ok {
		return fail(ErrUnknownHook)
	}
	if !s.state.allowed[hook] {
		return fail(ErrUnauthorized)
	}
	if name == "" || d.Kind != kind {
		return fail(ErrInvalidOptions)
	}
	options, err := resolve(d, o)
	if err != nil {
		return fail(err)
	}
	v.Options = options
	if d.Deprecated != nil {
		v.Warnings = []RegistrationWarning{{Hook: hook, Deprecation: *d.Deprecated}}
	}
	return v, nil
}

// HostInstance is the random registry-process epoch used in tracing and host
// bindings. A new Registry gets a new epoch; it is not an authentication token.
func (r *Registry) HostInstance() string { return r.hostInstance }
