package hookstest

import (
	"context"
	"errors"
	"sync"

	hooks "github.com/hollis-labs/plugin-hooks"
)

// Harness runs plugin registration callbacks in process against a dispatcher.
// Plugins receive only Scope. The test's host retains dispatch and teardown.
// Close disposes scopes in reverse load order, attempts all cleanup, then closes
// the dispatcher. Handlers and registration callbacks must cooperate with scope
// disposal; this harness cannot kill arbitrary plugin code.
type Harness struct {
	Dispatcher
	mu     sync.Mutex
	scopes []Scope
	closed bool
}

// NewHarness installs a caller-authored catalog. Use NewEngineAdapter as factory
// for plugin unit tests, or the host's Factory for integration tests.
func NewHarness(factory Factory, catalog hooks.Catalog, config hooks.ExecutionConfig) (*Harness, error) {
	if factory == nil {
		return nil, hooks.ErrInvalidOptions
	}
	dispatcher, err := factory(catalog, config)
	if err != nil {
		return nil, err
	}
	if dispatcher == nil {
		return nil, hooks.ErrInvalidOptions
	}
	return &Harness{Dispatcher: dispatcher}, nil
}

// Load creates a host-owned scope and calls register outside harness locks.
// A failed registration is swept before returning, using ctx for cleanup.
// Retain the returned scope to unload this generation early with Dispose.
func (h *Harness) Load(ctx context.Context, config hooks.ScopeConfig, register func(Scope) error) (Scope, error) {
	if register == nil {
		return nil, hooks.ErrInvalidOptions
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, hooks.ErrDisposed
	}
	scope, err := h.NewScope(config)
	if err == nil {
		h.scopes = append(h.scopes, scope)
	}
	h.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if err = register(scope); err != nil {
		return nil, errors.Join(err, scope.Dispose(ctx))
	}
	return scope, nil
}

// Close is safe to repeat. Every call can wait again for timed-out disposal.
func (h *Harness) Close(ctx context.Context) error {
	h.mu.Lock()
	h.closed = true
	scopes := append([]Scope(nil), h.scopes...)
	h.mu.Unlock()
	var errs []error
	for i := len(scopes) - 1; i >= 0; i-- {
		errs = append(errs, scopes[i].Dispose(ctx))
	}
	errs = append(errs, h.Dispatcher.Close(ctx))
	return errors.Join(errs...)
}
