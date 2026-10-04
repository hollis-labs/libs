package hosttest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hollis-labs/plugin-sdk/capability"
	"github.com/hollis-labs/plugin-sdk/capability/host"
)

// These variants corrupt one observed boundary at a time. They run the named
// probe directly so unrelated failures cannot hide a missing assertion.
type reviewAdapter struct{ mode string }
type reviewInstance struct {
	Instance
	mode     string
	observer *Observer
	fixture  Fixture
	calls    *atomic.Uint64
}

func (a reviewAdapter) Open(ctx context.Context, f Fixture, o *Observer) (Instance, error) {
	if len(f.Overrides) > 0 {
		if a.mode == "override wrong name" {
			return nil, &capability.Error{Code: capability.UnsupportedCapability, Capability: "host.example.wrong", EffectState: capability.NotStarted}
		}
		if a.mode == "override effect" {
			o.Execute(host.Call{})
			return nil, &capability.Error{Code: capability.UnsupportedCapability, Capability: f.Overrides[0].Name, EffectState: capability.NotStarted}
		}
	}
	ref := referenceAdapter{}
	if a.mode == "unsupported undeclared support" {
		// Same fixture grants, but the dispatcher actually installs every shared route.
		f.Catalog = capability.SharedDescriptors()
	}
	i, err := ref.Open(ctx, f, o)
	if err != nil {
		return i, err
	}
	if a.mode == "unsupported undeclared support" {
		r := i.(*referenceInstance)
		for _, d := range f.Catalog {
			for _, op := range d.Operations {
				effect := d.EffectCeiling
				if d.Name == capability.StorageWrite {
					effect = capability.Write
				}
				r.nativeEffects[op] = effect
			}
		}
	}
	return reviewInstance{i, a.mode, o, f, new(atomic.Uint64)}, nil
}
func (i reviewInstance) Activate(ctx context.Context, raw json.RawMessage, reqs []host.Request) (capability.GrantSet, []host.PlanNotice, error) {
	g, n, err := i.Instance.Activate(ctx, raw, reqs)
	if len(n) > 0 {
		if i.mode == "optional wrong code" {
			n[0].Code = capability.Conflict
		}
		if i.mode == "narrowing wrong grant" {
			n[0].GrantID = "different-grant"
		}
	}
	return g, n, err
}
func (i reviewInstance) Invoke(ctx context.Context, c Attempt) (Reply, error) {
	callIndex := i.calls.Add(1)
	if i.mode == "bridge credentials ignored" && c.Bridge {
		r := i.Instance.(*referenceInstance)
		_, err := r.credentials.Verify(c.Credential, host.Subject{Kind: host.SessionClient, ID: "session-client"}, i.fixture.Runtime, "loopback")
		if err != nil {
			i.observer.Execute(c.Call)
			return Reply{}, nil
		}
	}
	if i.mode == "session cancels plugin" && c.Bridge && c.Call.Operation == "host/mcp/cancel_call" {
		r := i.Instance.(*referenceInstance)
		r.mu.Lock()
		for key, cancel := range r.requests {
			if key.id == c.CancelID {
				cancel()
			}
		}
		r.mu.Unlock()
	}
	r, err := i.Instance.Invoke(ctx, c)
	switch i.mode {
	case "bridge credential wrong code":
		if c.Bridge && r.Failure != nil && r.Failure.Code == capability.Unauthenticated {
			r.Failure.Code = capability.ScopeDenied
			r.Data, _ = json.Marshal(r.Failure)
		}
	case "bridge credential effects":
		if c.Bridge && r.Failure != nil && r.Failure.Code == capability.Unauthenticated {
			i.observer.Execute(c.Call)
		}
	case "bridge credential reservation":
		if c.Bridge && r.Failure != nil && r.Failure.Code == capability.Unauthenticated {
			i.observer.Reserve()
		}
	case "redirect reservation":
		if c.Redirect {
			i.observer.Reserve()
		}
	case "audit actor kind":
		i.observer.mu.Lock()
		for idx := range i.observer.state.Audits {
			i.observer.state.Audits[idx].Actor.Kind = string(host.SessionClient)
		}
		i.observer.mu.Unlock()
	case "withdrawal reservation":
		if callIndex == 1 && i.fixture.Gate != nil && r.Failure != nil {
			i.observer.Reserve()
		}
	case "withdrawal state":
		if callIndex == 1 && i.fixture.Gate != nil && r.Failure != nil {
			r.Failure.EffectState = capability.NotCommitted
			r.Data, _ = json.Marshal(r.Failure)
		}
	case "unknown reservation":
		if r.Failure != nil && r.Failure.Code == capability.UnknownOutcome {
			i.observer.Reserve()
		}
	case "output no measurement":
		if c.Bridge && i.fixture.OutputBytes > 1000 {
			r.WireBytes = 0
		}
	case "output oversized payload":
		if c.Bridge && i.fixture.OutputBytes > 1000 {
			r.Payload = string(make([]byte, 1001))
		}
	case "diagnostic wire token":
		if i.fixture.FailAfterCommit {
			_, token := i.Access()
			r.Wire = json.RawMessage(`{"token":"` + token + `"}`)
		}
	case "discovery reservation":
		if c.Call.Operation == "host/mcp/list_tools" && i.fixture.CallerPolicy != nil {
			i.observer.Reserve()
		}
	case "diagnostic read state":
		if i.fixture.FailAfterCommit && r.Failure != nil && r.Failure.Code == capability.InternalError {
			r.Failure.EffectState = capability.NotStarted
			r.Data, _ = json.Marshal(r.Failure)
		}
	case "unsupported internal":
		if r.Failure != nil {
			r.Failure.Code = capability.InternalError
			r.Data, _ = json.Marshal(r.Failure)
		}
	case "success wire token":
		if r.Failure == nil {
			_, token := i.Access()
			r.Wire = json.RawMessage(`{"token":"` + token + `"}`)
		}
	case "success data token":
		if r.Failure == nil {
			_, token := i.Access()
			r.Data = json.RawMessage(`{"token":"` + token + `"}`)
		}
	case "success payload token":
		if r.Failure == nil {
			_, token := i.Access()
			r.Payload = token
		}
	case "success encoded token":
		if r.Failure == nil {
			_, token := i.Access()
			r.Payload = base64.StdEncoding.EncodeToString([]byte("Authorization: Bearer " + token + " tail"))
		}
	}
	return r, err
}

func findProbe(t *testing.T, name string) probe {
	t.Helper()
	supported := capability.SharedDescriptors()
	f, _ := basic(workflowName)
	supported = append(supported, f.Catalog[0])
	for _, p := range probes(supported) {
		if p.name == name {
			return p
		}
	}
	t.Fatalf("missing probe %q", name)
	return probe{}
}
func TestReviewBoundaryMutants(t *testing.T) {
	cases := map[string]string{
		"redirect reservation":     "bridge redirect",
		"override wrong name":      "shared descriptor cannot be overwritten",
		"override effect":          "shared descriptor cannot be overwritten",
		"audit actor kind":         "delegated identity reaches audit",
		"withdrawal reservation":   "stop before commit",
		"withdrawal state":         "stop before commit",
		"unknown reservation":      "unknown write outcome never retries",
		"output no measurement":    "bridge output",
		"output oversized payload": "bridge output",
		"diagnostic wire token":    "secret diagnostic output",
		"optional wrong code":      "unknown request optional=true",
		"narrowing wrong grant":    "planning wider than policy",
		"discovery reservation":    "discovery intersects caller permission",
		"session cancels plugin":   "bridge cancel_call cannot cancel plugin",
		"success wire token":       "bridge admitted host/mcp/call_tool",
		"success data token":       "bridge admitted host/mcp/call_tool",
		"success payload token":    "bridge admitted host/mcp/call_tool",
		"success encoded token":    "bridge admitted host/mcp/call_tool",
	}
	for mode, name := range cases {
		t.Run(mode, func(t *testing.T) {
			p := findProbe(t, name)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := runProbe(ctx, reviewAdapter{mode}, p.run); err == nil {
				t.Fatalf("broken adapter passed %s/%s", p.requirement, p.name)
			}
		})
	}
	// The read diagnostic has a distinct valid error code and effect-state rule.
	t.Run("diagnostic read state", func(t *testing.T) {
		f, _ := basic(capability.ReadonlyQuery)
		p := secretDiagnosticProbe(f.Catalog[0])
		if err := runProbe(context.Background(), reviewAdapter{"diagnostic read state"}, p.run); err == nil {
			t.Fatal("read diagnostic state passed")
		}
	})
}
func TestBridgeCredentialRefusals(t *testing.T) {
	for _, problem := range []string{"absent", "garbage", "plugin binding", "replaced generation", "stopped owner"} {
		t.Run(problem, func(t *testing.T) {
			p := bridgeCredentialProbe(problem)
			if err := runProbe(context.Background(), referenceAdapter{}, p.run); err != nil {
				t.Fatalf("reference: %s", safeReason(err))
			}
			for _, mode := range []string{"bridge credentials ignored", "bridge credential wrong code", "bridge credential effects", "bridge credential reservation"} {
				if err := runProbe(context.Background(), reviewAdapter{mode}, p.run); err == nil {
					t.Fatalf("%s passed", mode)
				}
			}
		})
	}
}
func TestUnsupportedValidGrantAndRefusal(t *testing.T) {
	f, _ := basic(capability.ReadonlyQuery)
	for _, p := range unsupportedProbes(f.Catalog) {
		t.Run(p.name, func(t *testing.T) {
			for _, mode := range []string{"unsupported undeclared support", "unsupported internal"} {
				if err := runProbe(context.Background(), reviewAdapter{mode}, p.run); err == nil {
					t.Fatalf("%s passed", mode)
				}
			}
		})
	}
}
