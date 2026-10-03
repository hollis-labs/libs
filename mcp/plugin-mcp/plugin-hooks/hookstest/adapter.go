package hookstest

import (
	"context"
	"encoding/json"

	hooks "github.com/hollis-labs/plugin-hooks"
)

// Handle is a host-owned opaque registration token. Never synthesize a token
// from its name; adapters must preserve identity across removal and reload.
type Handle any

// Scope is the plugin capability for one owner generation. Removal of a foreign
// or stale handle is harmless. Dispose fences admission and sweeps every hook.
type Scope interface {
	AddAction(string, string, hooks.Options, hooks.ActionFunc) (Handle, error)
	AddFilter(string, string, hooks.Options, hooks.FilterFunc) (Handle, error)
	Remove(Handle)
	Dispose(context.Context) error
}

// Dispatcher is the synchronous conformance surface a host adapts. Translate
// native results and errors into the module's statuses and errors.Is sentinels.
// EmitAction and ApplyFilters must preserve the supplied handler context for
// nested calls. Close releases resources within its context budget.
type Dispatcher interface {
	NewScope(hooks.ScopeConfig) (Scope, error)
	EmitAction(context.Context, string, json.RawMessage, map[string]string) (hooks.DispatchResult, error)
	ApplyFilters(context.Context, string, json.RawMessage, map[string]string) (hooks.DispatchResult, error)
	Close(context.Context) error
}

// Factory creates an isolated dispatcher with exactly the supplied catalog and
// execution limits. It must validate the catalog before admitting callbacks.
type Factory func(hooks.Catalog, hooks.ExecutionConfig) (Dispatcher, error)

// NewEngineAdapter is the reference Factory backed by Registry and Engine.
func NewEngineAdapter(c hooks.Catalog, config hooks.ExecutionConfig) (Dispatcher, error) {
	registry, err := hooks.NewRegistry(c)
	if err != nil {
		return nil, err
	}
	engine, err := hooks.NewEngine(registry, config)
	if err != nil {
		return nil, err
	}
	return &engineAdapter{registry: registry, engine: engine}, nil
}

type engineAdapter struct {
	registry *hooks.Registry
	engine   *hooks.Engine
}

func (a *engineAdapter) NewScope(c hooks.ScopeConfig) (Scope, error) {
	s, err := a.registry.NewScope(c)
	if err != nil {
		return nil, err
	}
	return engineScope{s}, nil
}
func (a *engineAdapter) EmitAction(ctx context.Context, name string, payload json.RawMessage, metadata map[string]string) (hooks.DispatchResult, error) {
	return a.engine.EmitAction(ctx, name, payload, metadata)
}
func (a *engineAdapter) ApplyFilters(ctx context.Context, name string, payload json.RawMessage, metadata map[string]string) (hooks.DispatchResult, error) {
	return a.engine.ApplyFilters(ctx, name, payload, metadata)
}
func (a *engineAdapter) Close(ctx context.Context) error { return a.engine.Shutdown(ctx) }

type engineScope struct{ scope *hooks.Scope }

func (s engineScope) AddAction(hook, name string, o hooks.Options, fn hooks.ActionFunc) (Handle, error) {
	return s.scope.AddAction(hook, name, o, fn)
}
func (s engineScope) AddFilter(hook, name string, o hooks.Options, fn hooks.FilterFunc) (Handle, error) {
	return s.scope.AddFilter(hook, name, o, fn)
}
func (s engineScope) Remove(token Handle) {
	if handle, ok := token.(hooks.Handle); ok {
		s.scope.Remove(handle)
	}
}
func (s engineScope) Dispose(ctx context.Context) error { return s.scope.Dispose(ctx) }

// RemoteScope extends the scope capability for a host's transport adapter.
// A factory used with Run must implement it to meet the remote requirements.
type RemoteScope interface {
	Scope
	AddRemoteAction(string, string, hooks.Options, hooks.RemoteRegistration) (Handle, error)
	AddRemoteFilter(string, string, hooks.Options, hooks.RemoteRegistration) (Handle, error)
}

// RemoteDispatcher exposes operator state for remote failure-accounting probes.
type RemoteDispatcher interface {
	Dispatcher
	Breaker(string, string) (hooks.BreakerSnapshot, error)
	ResetBreaker(string, string) error
}

func (a *engineAdapter) Breaker(owner, generation string) (hooks.BreakerSnapshot, error) {
	return a.engine.Breaker(owner, generation)
}
func (a *engineAdapter) ResetBreaker(owner, generation string) error {
	return a.engine.ResetBreaker(owner, generation)
}
func (s engineScope) AddRemoteAction(hook, name string, o hooks.Options, r hooks.RemoteRegistration) (Handle, error) {
	return s.scope.AddRemoteAction(hook, name, o, r)
}
func (s engineScope) AddRemoteFilter(hook, name string, o hooks.Options, r hooks.RemoteRegistration) (Handle, error) {
	return s.scope.AddRemoteFilter(hook, name, o, r)
}
