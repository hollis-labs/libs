package trust

import (
	"context"
	"errors"
	"github.com/hollis-labs/substrate/harness/workspace/effects"
	"testing"
)

type fakePort struct {
	updates             int
	present             bool
	updateErr, closeErr error
}

func (p *fakePort) Supported(m Mechanism) bool { return m == ClaudeProjects }
func (p *fakePort) Observe(context.Context, Request) (Observation, error) {
	return Observation{Target: "/resource/runtime/current", Present: p.present}, nil
}
func (p *fakePort) Begin(context.Context, Request) (Session, error) { return p, nil }
func (p *fakePort) Apply(_ context.Context, _ Request, validate func(context.Context) error) (Observation, bool, error) {
	if err := validate(context.Background()); err != nil {
		return Observation{}, false, err
	}
	if p.present {
		return Observation{Target: "/resource/runtime/current", Present: true}, false, nil
	}
	p.updates++
	p.present = true
	return Observation{Target: "/resource/runtime/current", Present: true}, true, p.updateErr
}
func (p *fakePort) Close() error { return p.closeErr }

type sink struct{ calls, fail int }

func (s *sink) Record(context.Context, effects.Evidence) error {
	s.calls++
	if s.calls == s.fail {
		return errors.New("injected")
	}
	return nil
}
func fixture() (Request, effects.ApplyContext) {
	h := effects.Header{Version: effects.SchemaVersion, OperationID: "operation", InputDigest: "input"}
	r := Request{Header: h, Mechanism: ClaudeProjects, Required: true, AuthorizationID: "grant", AuthorizationVersion: "revision", Config: effects.RootInput{ID: "config", Path: "/resource/provider", AllowedBase: "/resource", Owner: "owner", Provenance: "capture", MutationIdentity: "/resource/provider"}, Target: Target{LogicalPath: "/resource/runtime/current", CanonicalParent: "/resource/runtime", AllowedBase: "/resource/runtime", Provenance: "runtime", Stable: true}, CandidateRootID: "candidate"}
	c := effects.ApplyContext{PreflightContext: effects.PreflightContext{Header: h, HeldLocks: []effects.LockIdentity{{Namespace: "/locks", CanonicalID: "/resource/provider"}, {Namespace: "/locks", CanonicalID: "/resource/runtime"}}, Validate: func(context.Context) error { return nil }}, ArtifactGeneration: "generation", ArtifactRootID: "candidate", Receipts: &sink{}}
	return r, c
}
func TestRequiredUnknownNeverWrites(t *testing.T) {
	r, c := fixture()
	r.Mechanism = CodexProjects
	p := &fakePort{}
	_, got := Preflight(context.Background(), r, c.PreflightContext, p)
	if got.Outcome != effects.Unsupported || p.updates != 0 {
		t.Fatal(got)
	}
}
func TestBindingGuards(t *testing.T) {
	for _, mutate := range []func(*Request, *effects.ApplyContext){func(r *Request, c *effects.ApplyContext) { r.AuthorizationID = "" }, func(r *Request, c *effects.ApplyContext) { r.Target.Stable = false }, func(r *Request, c *effects.ApplyContext) { r.Target.Provenance = "" }, func(r *Request, c *effects.ApplyContext) { c.HeldLocks = c.HeldLocks[:1] }, func(r *Request, c *effects.ApplyContext) { r.Header.InputDigest = "changed" }, func(r *Request, c *effects.ApplyContext) { r.Config.Provenance = "" }} {
		r, c := fixture()
		mutate(&r, &c)
		p := &fakePort{}
		_, got := Preflight(context.Background(), r, c.PreflightContext, p)
		if got.Outcome != effects.Refused || p.updates != 0 {
			t.Fatal(got)
		}
	}
	for _, mutate := range []func(*Request, *effects.ApplyContext){func(r *Request, c *effects.ApplyContext) { r.AuthorizationVersion = "" }, func(r *Request, c *effects.ApplyContext) { r.Config.Owner = "" }, func(r *Request, c *effects.ApplyContext) { r.Config.AllowedBase = "/outside" }, func(r *Request, c *effects.ApplyContext) { r.Config.MutationIdentity = "/outside" }, func(r *Request, c *effects.ApplyContext) { r.Target.AllowedBase = "/outside" }, func(r *Request, c *effects.ApplyContext) { r.CandidateRootID = "" }, func(r *Request, c *effects.ApplyContext) { c.HeldLocks = c.HeldLocks[1:] }, func(r *Request, c *effects.ApplyContext) { c.HeldLocks[0].Namespace = r.Config.Path }, func(r *Request, c *effects.ApplyContext) { c.Validate = nil }} {
		r, c := fixture()
		mutate(&r, &c)
		_, got := Preflight(context.Background(), r, c.PreflightContext, &fakePort{})
		if got.Outcome != effects.Refused {
			t.Fatal(got)
		}
	}
}
func TestApplyFailuresAndIdempotence(t *testing.T) {
	for _, tc := range []struct {
		name                string
		receiptFail         int
		updateErr, closeErr error
		present             bool
		want                effects.Outcome
		updates             int
	}{
		{name: "apply", want: effects.Applied, updates: 1}, {name: "already", present: true, want: effects.AlreadyPresent}, {name: "intent", receiptFail: 1, want: effects.Refused}, {name: "complete", receiptFail: 2, want: effects.Partial, updates: 1}, {name: "after_replace", updateErr: errors.New("injected"), want: effects.Partial, updates: 1}, {name: "close", closeErr: errors.New("injected"), want: effects.Partial, updates: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, c := fixture()
			c.Receipts = &sink{fail: tc.receiptFail}
			p := &fakePort{present: tc.present, updateErr: tc.updateErr, closeErr: tc.closeErr}
			prep, pre := Preflight(context.Background(), r, c.PreflightContext, p)
			if pre.Code != "preflight_complete" {
				t.Fatal(pre)
			}
			got := Apply(context.Background(), prep, c, p)
			if got.Outcome != tc.want || p.updates != tc.updates {
				t.Fatalf("%+v updates %d", got, p.updates)
			}
			if got.Outcome == effects.Partial && len(got.Obligations) == 0 {
				t.Fatal("lost recovery obligation")
			}
		})
	}
}

func TestInspectionBindingAndNoReplay(t *testing.T) {
	r, c := fixture()
	p := &fakePort{}
	prep, _ := Preflight(context.Background(), r, c.PreflightContext, p)
	applied := Apply(context.Background(), prep, c, p)
	got := Inspect(context.Background(), prep, c.PreflightContext, p, applied.Evidence)
	if got.Outcome != effects.Applied || p.updates != 1 {
		t.Fatal(got)
	}
	interrupted := applied.Evidence.Clone()
	interrupted.Phase = effects.InterruptedPhase
	interrupted.Outcome = effects.Partial
	interrupted.Trust[0].Outcome = effects.Partial
	got = Inspect(context.Background(), prep, c.PreflightContext, p, interrupted)
	if got.Outcome != effects.Partial || p.updates != 1 {
		t.Fatal(got)
	}
	intent := interrupted.Clone()
	intent.Phase = effects.IntentPhase
	intent.Outcome = effects.Pending
	intent.Trust[0].Outcome = effects.Pending
	intent.Trust[0].Present = false
	got = Inspect(context.Background(), prep, c.PreflightContext, p, intent)
	if got.Outcome != effects.Partial || got.Inspections[0].State != effects.Divergent {
		t.Fatal(got)
	}
	for _, mutate := range []func(*effects.Evidence){func(e *effects.Evidence) { e.Header.OperationID = "other" }, func(e *effects.Evidence) { e.Header.InputDigest = "other" }, func(e *effects.Evidence) { e.Header.Version = "unknown" }, func(e *effects.Evidence) { e.RootID = "other" }, func(e *effects.Evidence) { e.Trust[0].Target = "/resource/other" }, func(e *effects.Evidence) { e.Trust[0].AuthorizationVersion = "other" }, func(e *effects.Evidence) { e.Trust[0].Present = false }, func(e *effects.Evidence) { e.Phase = "unknown" }} {
		e := applied.Evidence.Clone()
		mutate(&e)
		got = Inspect(context.Background(), prep, c.PreflightContext, p, e)
		if got.Outcome != effects.Refused || p.updates != 1 {
			t.Fatal(got)
		}
	}
	p.present = false
	got = Inspect(context.Background(), prep, c.PreflightContext, p, applied.Evidence)
	if got.Outcome != effects.Partial || got.Inspections[0].State != effects.Missing {
		t.Fatal(got)
	}
}
func TestApplyRequiresArtifactAndRefreshesAuthority(t *testing.T) {
	for _, mutate := range []func(*effects.ApplyContext){func(c *effects.ApplyContext) { c.ArtifactRootID = "other" }, func(c *effects.ApplyContext) { c.ArtifactGeneration = "" }, func(c *effects.ApplyContext) { c.Receipts = nil }, func(c *effects.ApplyContext) {
		c.Validate = func(context.Context) error { return errors.New("revoked") }
	}} {
		r, c := fixture()
		p := &fakePort{}
		prep, _ := Preflight(context.Background(), r, c.PreflightContext, p)
		mutate(&c)
		got := Apply(context.Background(), prep, c, p)
		if got.Outcome != effects.Refused || p.updates != 0 {
			t.Fatal(got)
		}
	}
}
func TestPreparedInputDetached(t *testing.T) {
	r, c := fixture()
	p := &fakePort{}
	prep, _ := Preflight(context.Background(), r, c.PreflightContext, p)
	r.Target.LogicalPath = "/resource/changed"
	got := Apply(context.Background(), prep, c, p)
	if got.Outcome != effects.Applied {
		t.Fatal(got)
	}
}

func TestOptionalUnsupportedHasDurableOmission(t *testing.T) {
	r, c := fixture()
	r.Required = false
	r.Mechanism = AntigravityWorkspaces
	p := &fakePort{}
	rec := &sink{}
	c.Receipts = rec
	prep, pre := Preflight(context.Background(), r, c.PreflightContext, p)
	if pre.Outcome != effects.Omitted {
		t.Fatal(pre)
	}
	got := Apply(context.Background(), prep, c, p)
	if got.Outcome != effects.Omitted || got.Evidence.Phase != effects.CompletePhase || rec.calls == 0 || p.updates != 0 {
		t.Fatal(got, rec.calls)
	}
	inspected := Inspect(context.Background(), prep, c.PreflightContext, p, got.Evidence)
	if inspected.Outcome != effects.Omitted {
		t.Fatal(inspected)
	}
}

func TestInvalidTextRefusedBeforeConfigRead(t *testing.T) {
	for _, mutate := range []func(*Request){func(r *Request) { r.AuthorizationID = string([]byte{255}) }, func(r *Request) { r.Header.OperationID = "invalid\x00" }, func(r *Request) { r.Target.LogicalPath = "/resource/runtime/" + string([]byte{255}) }} {
		r, c := fixture()
		mutate(&r)
		c.Header = r.Header
		_, got := Preflight(context.Background(), r, c.PreflightContext, &fakePort{})
		if got.Outcome != effects.Refused {
			t.Fatal(got)
		}
	}
}
