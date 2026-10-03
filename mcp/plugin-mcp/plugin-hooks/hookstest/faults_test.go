package hookstest_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	hooks "github.com/hollis-labs/plugin-hooks"
	"github.com/hollis-labs/plugin-hooks/hookstest"
)

// Deliberately broken host adapters prove the suite observes actual behavior.
// Each isolated child runs one requirement, and must fail it by name. There is
// no assertion about source text, fixture counts or implementation structure.
func TestRejectsBrokenAdapters(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, probe := range []struct{ fault, id string }{{"priority", "R01"}, {"fail-open", "R04"}, {"once", "R08"}, {"stale-handle", "R09"}, {"payload", "R13"}, {"view", "R14"}, {"catalog", "R15"}} {
		t.Run(probe.fault, func(t *testing.T) {
			// #nosec G204 -- The executable is this test binary; arguments are fixed suite requirement IDs.
			cmd := exec.Command(executable, "-test.run=^TestFaultProbe/"+probe.id, "-test.timeout=10s")
			cmd.Env = append(os.Environ(), "HOOKSTEST_TEST_FAULT="+probe.fault)
			output, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "FAIL: TestFaultProbe/"+probe.id) {
				t.Fatalf("broken adapter %s was not rejected: %v\n%s", probe.fault, err, output)
			}
		})
	}
}
func faultFactory(fault string) hookstest.Factory {
	return func(c hooks.Catalog, config hooks.ExecutionConfig) (hookstest.Dispatcher, error) {
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
	return d.Dispatcher.EmitAction(ctx, hook, payload, meta)
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
	}
	return s.Scope.AddAction(hook, name, o, fn)
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
	cmd := exec.Command(executable, "-test.run=^TestFaultProbe/R01", "-test.v", "-test.timeout=10s")
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
	factory := faultFactory(fault)
	if fault == "waive" {
		hookstest.Run(t, factory, hookstest.Waive("R01", "host migration gap for waiver reporting test"))
		return
	}
	hookstest.Run(t, factory)
}
