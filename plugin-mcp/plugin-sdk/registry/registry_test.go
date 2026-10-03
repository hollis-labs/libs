package registry

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
)

func validResponse() Response {
	r := NewResponse("host-epoch", 2)
	r.Plugins["notes"] = Plugin{OwnerGeneration: "1", BundleURL: "/notes.js", BundleVersion: BundleDigest([]byte("export const Panel = {};")), Runtime: []Runtime{{Name: "react", Min: "19.0.0", Max: "19.9.9"}}}
	r.Kinds["panel"] = KindDescriptor{1, json.RawMessage(`{}`), []Representation{Component}, []string{"rail"}, []string{}}
	r.Regions["rail"] = RegionDescriptor{[]string{"panel"}, []Representation{Component}, json.RawMessage(`{}`), "priority-ascending"}
	_ = r.Set(Contribution{OwnerID: "notes", OwnerGeneration: "1", LocalKey: "main", Kind: "panel", SchemaVersion: 1, Required: true, Representation: Component, Metadata: json.RawMessage(`{"title":"Notes"}`), Component: &ComponentRef{"Panel", "rail"}})
	return r
}
func policyFor(r Response) AdmissionPolicy {
	return AdmissionPolicy{Kinds: r.Kinds, Regions: r.Regions}
}
func TestProtocolLockedAt2(t *testing.T) {
	if Protocol != 2 {
		t.Fatal("registry protocol must be 2")
	}
}
func TestWireRoundtrip(t *testing.T) {
	want := validResponse()
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got Response
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatal("roundtrip lost fields")
	}
	if err := got.Validate(); err != nil {
		t.Fatal(err)
	}
}
func TestInsertionNeverOverwrites(t *testing.T) {
	r := validResponse()
	c := r.Contributions["panel"]["notes/main"]
	c.Component = &ComponentRef{"Other", "rail"}
	if !errors.Is(r.Set(c), ErrCollision) {
		t.Fatal("duplicate accepted")
	}
	if r.Contributions["panel"]["notes/main"].Component.Export != "Panel" {
		t.Fatal("overwritten")
	}
}
func TestOptionalUnknownAndRequiredAtomicFailure(t *testing.T) {
	r := validResponse()
	c := r.Contributions["panel"]["notes/main"]
	c.Kind = "plugin.notes.future"
	c.LocalKey = "future"
	c.Required = false
	_ = r.Set(c)
	p, err := r.Plan(policyFor(r))
	if err != nil || len(p.Accepted) != 1 || p.Refusals[0].Reason != "unsupported-kind" {
		t.Fatalf("optional: %+v %v", p, err)
	}
	c.Required = true
	r.Contributions[c.Kind][c.Key()] = c
	p, err = r.Plan(policyFor(r))
	if !errors.Is(err, ErrRequired) || !p.Refusals[0].Required {
		t.Fatalf("required: %+v %v", p, err)
	}
}
func TestKindRegionAndMetadataOptIn(t *testing.T) {
	cases := []struct {
		name, reason string
		policy       func(Response) AdmissionPolicy
	}{
		{"kind", "unsupported-kind", func(r Response) AdmissionPolicy { p := policyFor(r); p.Kinds = map[string]KindDescriptor{}; return p }},
		{"region", "unsupported-region", func(r Response) AdmissionPolicy {
			p := policyFor(r)
			p.Regions = map[string]RegionDescriptor{}
			return p
		}},
		{"reserved", "reserved", func(r Response) AdmissionPolicy {
			p := policyFor(r)
			p.Reserved = func(_, key string) bool { return key == "notes/main" }
			return p
		}},
		{"schema", "invalid-metadata", func(r Response) AdmissionPolicy {
			p := policyFor(r)
			p.ValidateMetadata = func(_, raw json.RawMessage) error { return errors.New("bad schema") }
			return p
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := validResponse()
			p, err := r.Plan(tc.policy(r))
			if !errors.Is(err, ErrRequired) || p.Refusals[0].Reason != tc.reason {
				t.Fatalf("%+v %v", p, err)
			}
		})
	}
}
func TestRepresentationsAndOwnership(t *testing.T) {
	r := validResponse()
	c := r.Contributions["panel"]["notes/main"]
	c.OwnerGeneration = "old"
	r.Contributions[c.Kind][c.Key()] = c
	if !errors.Is(r.Validate(), ErrInvalidContribution) {
		t.Fatal("stale generation accepted")
	}
	r = validResponse()
	c = r.Contributions["panel"]["notes/main"]
	c.Declarative = json.RawMessage(`{}`)
	r.Contributions[c.Kind][c.Key()] = c
	if !errors.Is(r.Validate(), ErrInvalidContribution) {
		t.Fatal("two representations accepted")
	}
	r = NewResponse("epoch", 2)
	r.Plugins["p"] = Plugin{OwnerGeneration: "1"}
	for _, c := range []Contribution{
		{OwnerID: "p", OwnerGeneration: "1", LocalKey: "nav", Kind: "nav.item", SchemaVersion: 1, Representation: Declarative, Metadata: json.RawMessage(`{}`), Declarative: json.RawMessage(`{"label":"Hi"}`)},
		{OwnerID: "p", OwnerGeneration: "1", LocalKey: "tool", Kind: "mcp.tool", SchemaVersion: 1, Representation: Handler, Metadata: json.RawMessage(`{}`), Handler: &HandlerRef{ID: "notes.read"}},
	} {
		if err := r.Set(c); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
}
func TestRuntimeAndIntegrity(t *testing.T) {
	p := validResponse().Plugins["notes"]
	bytes := []byte("export const Panel = {};")
	if err := VerifyBundle(p, bytes, map[string]string{"react": "19.3.0"}, false); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(VerifyBundle(p, []byte("changed"), map[string]string{"react": "19.3.0"}, false), ErrIntegrity) {
		t.Fatal("digest mismatch accepted")
	}
	for _, version := range []string{"20.0.0", "19.0.0-rc.1", "not-a-version", ""} {
		if !errors.Is(CheckRuntimes(p.Runtime, map[string]string{"react": version}, false), ErrRuntime) {
			t.Fatalf("accepted %q", version)
		}
	}
	if err := CheckRuntimes([]Runtime{{Name: "r", Min: "0.2.0", Max: "0.2.9"}}, map[string]string{"r": "0.2.9"}, false); err != nil {
		t.Fatal(err)
	}
	if err := CheckRuntimes([]Runtime{{Name: "r", Min: "1.0.0-alpha.2", Max: "1.0.0-alpha.10"}}, map[string]string{"r": "1.0.0-alpha.9"}, true); err != nil {
		t.Fatal(err)
	}
}
func TestCatalogRevokeBeforeReplaceAndStaleFence(t *testing.T) {
	r := validResponse()
	catalog := NewCatalog(r.HostInstance)
	notifications := 0
	unsubscribe := catalog.Subscribe(func() { notifications++; _ = catalog.Snapshot() })
	defer unsubscribe()
	if _, err := catalog.Activate(r, policyFor(r)); err != nil {
		t.Fatal(err)
	}
	next := clone(r)
	next.Revision = 4
	p := next.Plugins["notes"]
	p.OwnerGeneration = "2"
	next.Plugins["notes"] = p
	c := next.Contributions["panel"]["notes/main"]
	c.OwnerGeneration = "2"
	next.Contributions[c.Kind][c.Key()] = c
	if _, err := catalog.Activate(next, policyFor(next)); !errors.Is(err, ErrNeedsRevocation) {
		t.Fatal("replacement without revoke")
	}
	if err := catalog.Revoke("notes", "1"); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Snapshot().Contributions) != 0 {
		t.Fatal("revoked view still listed")
	}
	if _, err := catalog.Activate(next, policyFor(next)); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Revoke("notes", "1"); !errors.Is(err, ErrStale) {
		t.Fatal("stale revoke accepted")
	}
	stale := clone(r)
	stale.Revision = 6
	if _, err := catalog.Activate(stale, policyFor(stale)); !errors.Is(err, ErrRevoked) {
		t.Fatal("revoked generation restored")
	}
	if notifications != 3 {
		t.Fatalf("notifications: %d", notifications)
	}
	snapshot := catalog.Snapshot()
	delete(snapshot.Plugins, "notes")
	if len(catalog.Snapshot().Plugins) == 0 {
		t.Fatal("snapshot mutation changed live state")
	}
}
func TestScopeFencesAndContinuesCleanup(t *testing.T) {
	scope, err := NewScope("epoch", "p", "1")
	if err != nil {
		t.Fatal(err)
	}
	var order []int
	for i := range 3 {
		_ = scope.Add(func(context.Context) error {
			order = append(order, i)
			if i == 1 {
				panic("fixture")
			}
			return nil
		})
	}
	ctx, err := scope.Admit()
	if err != nil {
		t.Fatal(err)
	}
	if err := scope.Dispose(context.Background()); err == nil {
		t.Fatal("panic disappeared")
	}
	if !reflect.DeepEqual(order, []int{2, 1, 0}) {
		t.Fatalf("order: %v", order)
	}
	if ctx.Err() == nil || scope.Active() {
		t.Fatal("scope not revoked")
	}
	if _, err := scope.Admit(); !errors.Is(err, ErrRevoked) {
		t.Fatal("dispatch admitted")
	}
	if err := scope.Add(func(context.Context) error { return nil }); !errors.Is(err, ErrRevoked) {
		t.Fatal("late registration admitted")
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() { _ = scope.Dispose(context.Background()) })
	}
	wg.Wait()
	if !reflect.DeepEqual(order, []int{2, 1, 0}) {
		t.Fatal("disposal repeated")
	}
}
func TestDuplicateJSONKeysRefused(t *testing.T) {
	var r Response
	if err := json.Unmarshal([]byte(`{"protocol":2,"protocol":2}`), &r); !errors.Is(err, ErrCollision) {
		t.Fatalf("duplicate: %v", err)
	}
}

func TestCatalogWithdrawalRequiresRevoke(t *testing.T) {
	r := validResponse()
	catalog := NewCatalog(r.HostInstance)
	if _, err := catalog.Activate(r, policyFor(r)); err != nil {
		t.Fatal(err)
	}
	empty := NewResponse(r.HostInstance, 3)
	if _, err := catalog.Activate(empty, policyFor(empty)); !errors.Is(err, ErrNeedsRevocation) {
		t.Fatalf("withdrawal bypassed revoke: %v", err)
	}
	if len(catalog.Snapshot().Plugins) != 1 {
		t.Fatal("failed withdrawal changed catalog")
	}
	if err := catalog.Revoke("notes", "1"); err != nil {
		t.Fatal(err)
	}
	empty.Revision = 4
	if _, err := catalog.Activate(empty, policyFor(empty)); err != nil {
		t.Fatal(err)
	}
	r.Revision = 5
	if _, err := catalog.Activate(r, policyFor(r)); !errors.Is(err, ErrRevoked) {
		t.Fatalf("withdrawn generation restored: %v", err)
	}
}

func TestConcurrentCatalogActivationAndSnapshots(t *testing.T) {
	catalog := NewCatalog("epoch")
	var wg sync.WaitGroup
	for revision := uint64(2); revision < 50; revision++ {
		wg.Go(func() {
			r := NewResponse("epoch", revision)
			_, err := catalog.Activate(r, policyFor(r))
			if err != nil && !errors.Is(err, ErrStale) {
				t.Error(err)
			}
			snapshot := catalog.Snapshot()
			if err := snapshot.Validate(); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if catalog.Snapshot().Revision != 49 {
		t.Fatal("newer revision lost")
	}
}

func TestConcurrentDisposeWaitsForSingleCleanup(t *testing.T) {
	scope, _ := NewScope("epoch", "notes", "1")
	entered, release := make(chan struct{}), make(chan struct{})
	calls := 0
	_ = scope.Add(func(context.Context) error { calls++; close(entered); <-release; return errors.New("cleanup failed") })
	results := make(chan error, 2)
	go func() { results <- scope.Dispose(context.Background()) }()
	<-entered
	if _, err := scope.Admit(); !errors.Is(err, ErrRevoked) {
		t.Fatal("call admitted during cleanup")
	}
	go func() { results <- scope.Dispose(context.Background()) }()
	close(release)
	first, second := <-results, <-results
	if first == nil || second == nil || first.Error() != second.Error() || calls != 1 {
		t.Fatal("disposal was not single/idempotent")
	}
}

func TestRevokePreflightGenerationPreventsLateActivation(t *testing.T) {
	r := validResponse()
	catalog := NewCatalog(r.HostInstance)
	if err := catalog.Revoke("notes", "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Activate(r, policyFor(r)); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked pending owner activated: %v", err)
	}
}

func TestRequiredFailurePreservesServingCatalog(t *testing.T) {
	r := validResponse()
	catalog := NewCatalog(r.HostInstance)
	if _, err := catalog.Activate(r, policyFor(r)); err != nil {
		t.Fatal(err)
	}
	candidate := clone(r)
	candidate.Revision++
	policy := policyFor(candidate)
	policy.Kinds = map[string]KindDescriptor{}
	if _, err := catalog.Activate(candidate, policy); !errors.Is(err, ErrRequired) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(catalog.Snapshot(), r) {
		t.Fatal("preflight failure replaced current catalog")
	}
}

func TestCoreAndForeignNamespacesRefused(t *testing.T) {
	for _, owner := range []string{"core", "notes"} {
		r := NewResponse("epoch", 2)
		r.Plugins[owner] = Plugin{OwnerGeneration: "1"}
		kind := "plugin.someone_else.nav"
		r.Kinds[kind] = KindDescriptor{1, json.RawMessage(`{}`), []Representation{Declarative}, []string{}, []string{}}
		_ = r.Set(Contribution{OwnerID: owner, OwnerGeneration: "1", LocalKey: "nav", Kind: kind, SchemaVersion: 1, Required: true, Representation: Declarative, Metadata: json.RawMessage(`{}`), Declarative: json.RawMessage(`{}`)})
		plan, err := r.Plan(policyFor(r))
		if !errors.Is(err, ErrRequired) || plan.Refusals[0].Reason != "reserved" {
			t.Fatalf("namespace accepted: %+v %v", plan, err)
		}
	}
}

func TestPublicBindingCollisionAndNestedDuplicateJSON(t *testing.T) {
	r := validResponse()
	c := r.Contributions["panel"]["notes/main"]
	c.PublicBinding = "shared"
	r.Contributions[c.Kind][c.Key()] = c
	c.LocalKey = "other"
	_ = r.Set(c)
	if !errors.Is(r.Validate(), ErrCollision) {
		t.Fatal("public binding collision accepted")
	}
	raw := []byte(`{"protocol":2,"metadata":{"nested":1,"nested":2}}`)
	if !errors.Is(json.Unmarshal(raw, &r), ErrCollision) {
		t.Fatal("nested duplicate key accepted")
	}
}
