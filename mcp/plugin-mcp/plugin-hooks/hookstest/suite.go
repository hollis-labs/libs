package hookstest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	hooks "github.com/hollis-labs/plugin-hooks"
)

type config struct {
	waivers map[string]string
	bound   time.Duration
}

// Option configures Run.
type Option func(*config)

// Waive names a migration gap. Unknown IDs and empty reasons fail the run.
// Every valid waiver is printed as a skipped subtest, never silently omitted.
func Waive(id, reason string) Option { return func(c *config) { c.waivers[id] = reason } }

// WithBound sets the observation and cleanup bound (default three seconds).
// It does not change the explicit timeout and dispatch-budget probes.
func WithBound(bound time.Duration) Option { return func(c *config) { c.bound = bound } }

// Requirement names one stable conformance obligation. IDs are package-local.
type Requirement struct{ ID, Name string }
type requirement struct {
	Requirement
	run func(*env)
}

var requirements = []requirement{
	{Requirement{"R01", "action priorities and stable ties"}, (*env).ordering},
	{Requirement{"R02", "ordered waterfall results"}, (*env).waterfall},
	{Requirement{"R03", "parallel outcomes retain priority order"}, (*env).parallel},
	{Requirement{"R04", "explicit open and closed error policies"}, (*env).policies},
	{Requirement{"R05", "bail permission gates never fail open"}, (*env).bail},
	{Requirement{"R06", "handler timeout and whole dispatch budget"}, (*env).timeouts},
	{Requirement{"R07", "panic isolation at every handler boundary"}, (*env).panics},
	{Requirement{"R08", "concurrent once and canceled admission"}, (*env).once},
	{Requirement{"R09", "foreign and stale handles cannot remove replacements"}, (*env).handles},
	{Requirement{"R10", "unload cancels work and sweeps owner generation"}, (*env).unload},
	{Requirement{"R11", "nested context carries the depth guard"}, (*env).depth},
	{Requirement{"R12", "timed out handlers retain execution capacity"}, (*env).capacity},
	{Requirement{"R13", "ingress handler output and metadata isolation"}, (*env).isolation},
	{Requirement{"R14", "deep views merge only visible mutable fields"}, (*env).views},
	{Requirement{"R15", "catalog validation and registration admission"}, (*env).catalog},
	{Requirement{"R16", "remote policy and latency admission"}, (*env).remoteAdmission},
	{Requirement{"R17", "structured remote results and vetoes"}, (*env).remoteResults},
	{Requirement{"R18", "remote scope binding and output fences"}, (*env).remoteFences},
	{Requirement{"R22", "remote generation breaker accounting"}, (*env).remoteBreaker},
}

// Requirements returns a detached list for host migration reports.
func Requirements() []Requirement {
	out := make([]Requirement, len(requirements))
	for i, r := range requirements {
		out[i] = r.Requirement
	}
	return out
}
func validateConfig(c *config) error {
	if c.bound <= 0 {
		return errors.New("observation bound must be positive")
	}
	for id, reason := range c.waivers {
		known := false
		for _, r := range requirements {
			known = known || r.ID == id
		}
		if !known {
			return fmt.Errorf("unknown requirement %q", id)
		}
		if strings.TrimSpace(reason) == "" {
			return fmt.Errorf("waiver %s needs a reason", id)
		}
	}
	return nil
}

// Run executes the published requirements against fresh dispatchers, one subtest per requirement.
// Factories must not substitute the reference Engine for the host's real path.
func Run(t *testing.T, factory Factory, options ...Option) {
	t.Helper()
	c := &config{waivers: map[string]string{}, bound: 3 * time.Second}
	for _, option := range options {
		if option == nil {
			t.Fatal("hookstest: nil option")
		}
		option(c)
	}
	if factory == nil {
		t.Fatal("hookstest: nil factory")
	}
	if err := validateConfig(c); err != nil {
		t.Fatal("hookstest:", err)
	}
	for _, r := range requirements {
		t.Run(r.ID+"_"+strings.ReplaceAll(r.Name, " ", "_"), func(t *testing.T) {
			if reason, ok := c.waivers[r.ID]; ok {
				t.Skipf("WAIVED %s (%s): %s", r.ID, r.Name, reason)
			}
			r.run(&env{t: t, factory: factory, bound: c.bound})
		})
	}
}

type env struct {
	t       *testing.T
	factory Factory
	bound   time.Duration
}

func (e *env) context() context.Context {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), e.bound)
	e.t.Cleanup(cancel)
	return ctx
}
func definition(name string, kind hooks.Kind) hooks.Definition {
	remote := false
	d := hooks.Definition{Name: name, Kind: kind, Mode: hooks.Sequential, Since: "1.0.0", SchemaDigest: "conformance-v1", RemoteOK: &remote, InputSchema: json.RawMessage(`{}`), ValidateInput: validJSON, Budget: time.Second, HandlerTimeout: 500 * time.Millisecond, OnErrorDefault: hooks.Closed, AllowedOnError: []hooks.ErrorPolicy{hooks.Open, hooks.Closed}, MaxPayloadBytes: 4096, MaxHandlers: 64, MaxParallelism: 32}
	if kind == hooks.Filter {
		d.Mode = hooks.Waterfall
		d.OutputSchema = json.RawMessage(`{}`)
		d.ValidateOutput = validJSON
		d.MutablePaths = []string{""}
	}
	return d
}
func validJSON(raw json.RawMessage) error {
	if !json.Valid(raw) {
		return errors.New("invalid JSON")
	}
	return nil
}
func (e *env) new(config hooks.ExecutionConfig, definitions ...hooks.Definition) Dispatcher {
	e.t.Helper()
	d, err := e.factory(hooks.Catalog{Version: "conformance-v1", Definitions: definitions}, config)
	if err != nil || d == nil {
		e.t.Fatalf("construct dispatcher: %v", err)
	}
	e.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), e.bound)
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- d.Close(ctx) }()
		select {
		case err := <-done:
			if err != nil {
				e.t.Errorf("close: %v", err)
			}
		case <-ctx.Done():
			e.t.Error("dispatcher close exceeded bound")
		}
	})
	return d
}
func (e *env) scope(d Dispatcher, owner, generation string, definitions ...hooks.Definition) Scope {
	e.t.Helper()
	names := make([]string, len(definitions))
	for i, d := range definitions {
		names[i] = d.Name
	}
	s, err := d.NewScope(hooks.ScopeConfig{Owner: owner, Generation: generation, Hooks: names})
	if err != nil || s == nil {
		e.t.Fatalf("create scope: %v", err)
	}
	e.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), e.bound)
		defer cancel()
		if err := s.Dispose(ctx); err != nil {
			e.t.Errorf("dispose: %v", err)
		}
	})
	return s
}
func (e *env) action(s Scope, d hooks.Definition, name string, o hooks.Options, fn hooks.ActionFunc) Handle {
	e.t.Helper()
	h, err := s.AddAction(d.Name, name, o, fn)
	if err != nil {
		e.t.Fatal(err)
	}
	return h
}
func (e *env) filter(s Scope, d hooks.Definition, name string, o hooks.Options, fn hooks.FilterFunc) Handle {
	e.t.Helper()
	h, err := s.AddFilter(d.Name, name, o, fn)
	if err != nil {
		e.t.Fatal(err)
	}
	return h
}
func (e *env) wait(ch <-chan struct{}) {
	e.t.Helper()
	select {
	case <-ch:
	case <-time.After(e.bound):
		e.t.Fatal("handler observation exceeded bound")
	}
}
func (e *env) success(result hooks.DispatchResult, err error, n int) {
	e.t.Helper()
	if err != nil || result.Status != hooks.Success || len(result.Outcomes) != n {
		e.t.Fatalf("want success with %d outcomes; got %+v, %v", n, result, err)
	}
}
func pointer[T any](v T) *T { return &v }
func names(result hooks.DispatchResult) []string {
	out := make([]string, len(result.Outcomes))
	for i, o := range result.Outcomes {
		out[i] = o.Name
	}
	return out
}
func decode(raw json.RawMessage) map[string]any {
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}
