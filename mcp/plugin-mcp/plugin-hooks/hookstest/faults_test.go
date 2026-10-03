package hookstest_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	hooks "github.com/hollis-labs/plugin-hooks"
	"github.com/hollis-labs/plugin-hooks/hookstest"
)

// Deliberately broken host adapters prove the suite observes actual behavior.
// Each isolated child runs one selected requirement and reports failure with
// a dedicated exit code after its cleanup. Crashes and process timeouts are not
// accepted as conformance failures. No assertion inspects source text, counts,
// or implementation structure.
func TestRejectsBrokenAdapters(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, probe := range []struct{ fault, id string }{
		{"priority", "R01"}, {"waterfall-order", "R02"}, {"parallel-outcomes", "R03"},
		{"fail-open", "R04"}, {"bail-open", "R05"}, {"timeout-hidden", "R06"},
		{"panic-siblings", "R07"}, {"once", "R08"}, {"stale-handle", "R09"},
		{"unload-no-sweep", "R10"}, {"unload-late-output", "R10"}, {"depth", "R11"},
		{"capacity", "R12"}, {"payload", "R13"}, {"view", "R14"}, {"catalog", "R15"},
	} {
		t.Run(probe.fault, func(t *testing.T) {
			// #nosec G204 -- The executable is this test binary; arguments are fixed suite requirement IDs.
			cmd := exec.Command(executable, "-test.run=^TestFaultProbe$/"+probe.id, "-test.timeout=10s")
			cmd.Env = append(os.Environ(), "HOOKSTEST_TEST_FAULT="+probe.fault)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != conformanceFailureExit {
				t.Fatalf("broken adapter %s was not rejected: %v\n%s", probe.fault, err, output)
			}
		})
	}
}

// Only an ordinary testing.T failure after Run returns produces this code.
// The process timeout and Go runtime panic paths use different exit codes.
const conformanceFailureExit = 42

func TestFaultProbeReference(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// #nosec G204 -- Re-execute this test binary with fixed requirement arguments.
	cmd := exec.Command(executable, "-test.run=^TestFaultProbe$/R05", "-test.timeout=10s")
	cmd.Env = append(os.Environ(), "HOOKSTEST_TEST_FAULT=reference")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("reference child failed: %v\n%s", err, output)
	}
}

func faultFactory(fault string) hookstest.Factory {
	return func(c hooks.Catalog, config hooks.ExecutionConfig) (hookstest.Dispatcher, error) {
		switch fault {
		case "depth":
			config.MaxDepth = 4 // Ignore the requested depth limit of three.
		case "capacity":
			config.MaxActive, config.MaxActivePerOwner = 2, 2 // Admit a second live lease.
		}
		if fault == "catalog" {
			c = hooks.Catalog{Version: "1"}
		}
		if fault == "view" {
			for i := range c.Definitions {
				c.Definitions[i].Views = map[string][]string{"public": {""}}
				c.Definitions[i].MutablePaths = []string{""}
			}
		}
		d, err := hookstest.NewEngineAdapter(c, config)
		if err != nil {
			return nil, err
		}
		return faultDispatcher{Dispatcher: d, fault: fault}, nil
	}
}

type faultDispatcher struct {
	hookstest.Dispatcher
	fault string
}

func (d faultDispatcher) NewScope(c hooks.ScopeConfig) (hookstest.Scope, error) {
	s, err := d.Dispatcher.NewScope(c)
	if err != nil {
		return nil, err
	}
	return faultScope{Scope: s, fault: d.fault}, nil
}
func (d faultDispatcher) EmitAction(ctx context.Context, hook string, payload json.RawMessage, meta map[string]string) (hooks.DispatchResult, error) {
	if d.fault == "payload" && len(payload) > 0 {
		payload[0] = 'x'
	}
	r, err := d.Dispatcher.EmitAction(ctx, hook, payload, meta)
	switch d.fault {
	case "parallel-outcomes":
		// A dispatcher returning completion order instead of registration order.
		for left, right := 0, len(r.Outcomes)-1; left < right; left, right = left+1, right-1 {
			r.Outcomes[left], r.Outcomes[right] = r.Outcomes[right], r.Outcomes[left]
		}
	case "timeout-hidden":
		if errors.Is(err, context.DeadlineExceeded) {
			err = nil
		}
	}
	return r, err
}

type faultScope struct {
	hookstest.Scope
	fault string
}

func (s faultScope) AddAction(hook, name string, o hooks.Options, fn hooks.ActionFunc) (hookstest.Handle, error) {
	switch s.fault {
	case "priority":
		zero := 0
		o.Priority = &zero
	case "fail-open":
		o.OnError = hooks.Open
	case "once":
		o.Once = false
	case "bail-open":
		original := fn
		fn = func(ctx context.Context, v hooks.Invocation) error {
			err := original(ctx, v)
			if errors.Is(err, hooks.ErrCancelled) || errors.Is(err, hooks.ErrApprovalRequired) {
				return errors.New("veto treated as an ordinary open error")
			}
			return err
		}
	case "panic-siblings":
		o.OnError = hooks.Closed // Recover the panic but kill healthy siblings.
	}
	return s.Scope.AddAction(hook, name, o, fn)
}
func (d faultDispatcher) ApplyFilters(ctx context.Context, hook string, payload json.RawMessage, meta map[string]string) (hooks.DispatchResult, error) {
	r, err := d.Dispatcher.ApplyFilters(ctx, hook, payload, meta)
	if d.fault == "unload-late-output" && err != nil {
		r.Status, r.Value = hooks.Success, json.RawMessage(`{"n":999}`)
		return r, nil //nolint:nilerr // Deliberately broken adapter publishes a canceled result.
	}
	return r, err
}

func (s faultScope) AddFilter(hook, name string, o hooks.Options, fn hooks.FilterFunc) (hookstest.Handle, error) {
	if s.fault == "waterfall-order" {
		zero := 0
		o.Priority = &zero
	}
	return s.Scope.AddFilter(hook, name, o, fn)
}

func (s faultScope) Dispose(ctx context.Context) error {
	if s.fault == "unload-no-sweep" {
		return nil
	}
	return s.Scope.Dispose(ctx)
}

func (s faultScope) Remove(handle hookstest.Handle) {
	if s.fault != "stale-handle" {
		s.Scope.Remove(handle)
	}
}

func TestWaiverReasonReported(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// #nosec G204 -- Re-execute this test binary with fixed arguments to observe testing.T's skip output.
	cmd := exec.Command(executable, "-test.run=^TestFaultProbe$/R01", "-test.v", "-test.timeout=10s")
	cmd.Env = append(os.Environ(), "HOOKSTEST_TEST_FAULT=waive")
	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "WAIVED R01") || !strings.Contains(string(output), "host migration gap for waiver reporting test") || !strings.Contains(string(output), "SKIP: TestFaultProbe/R01") {
		t.Fatalf("waiver was not visibly reported: %v\n%s", err, output)
	}
}

// TestFaultProbe is a child-process entry point. The reference TestConformance
// always runs NewEngineAdapter unconditionally, with zero waivers.
func TestFaultProbe(t *testing.T) {
	fault := os.Getenv("HOOKSTEST_TEST_FAULT")
	if fault == "" {
		return
	}
	t.Cleanup(func() {
		if t.Failed() {
			os.Exit(conformanceFailureExit)
		}
	})
	factory := faultFactory(fault)
	if fault == "waive" {
		hookstest.Run(t, factory, hookstest.Waive("R01", "host migration gap for waiver reporting test"))
		return
	}
	hookstest.Run(t, factory)
}
