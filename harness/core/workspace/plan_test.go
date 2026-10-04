package workspace_test

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	claude "github.com/hollis-labs/substrate/harness/adapters/claude/nativefiles"
	layout "github.com/hollis-labs/substrate/harness/adapters/layout/plan"
	"github.com/hollis-labs/substrate/harness/workspace"
	"github.com/hollis-labs/substrate/harness/workspace/materialize"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
	"github.com/hollis-labs/substrate/harness/workspace/render"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
)

func planInputs(t *testing.T) (workspace.Spec, workspace.ResolvedContent, workspace.Resources, workspace.Observations) {
	s := spec(t)
	s.Effects = []workspace.EffectGrant{{Kind: workspace.DirectoryEffect, RootID: s.Home.Root.ID, AuthorizationID: "fixture-authority", Version: "1"}, {Kind: workspace.DirectoryEffect, RootID: s.Boot.IdentityRoot.ID, AuthorizationID: "fixture-authority", Version: "1"}, {Kind: workspace.ArtifactEffect, RootID: s.Boot.Candidate.ID, AuthorizationID: "fixture-authority", Version: "1"}}
	resources := workspace.Resources{Roots: []workspace.RootRef{s.Home.Root, s.Boot.IdentityRoot, s.Boot.Current, s.Boot.Candidate}, LockNamespace: filepath.Join(s.Home.Root.AllowedBase, "locks"), Capabilities: []workspace.Capability{workspace.CanonicalRoots, workspace.MutationLocks}}
	resources.Grants = slices.Clone(s.Effects)
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	observed := workspace.Observations{At: at, ExpiresAt: at.Add(time.Minute), FenceVersion: s.Identity.Fence.Revision, Capabilities: slices.Clone(resources.Capabilities)}
	for _, r := range resources.Roots {
		observed.Roots = append(observed.Roots, workspace.RootObservation{RootID: r.ID, CanonicalPath: r.Path, CanonicalBase: r.AllowedBase, Owner: r.Owner})
	}
	rendered := render.Result{Root: layout.RootBoot, RootMode: 0700, Tree: tree("AGENTS.md"), Binding: render.Binding{Argv: []string{"--one", "two"}, Environment: map[string]string{"FIXTURE_ROOT": s.Boot.Candidate.Path}, BeforeResume: true}, Diagnostics: []render.Diagnostic{{Code: "fixture_omission", Reason: "fixture optional omission"}}}
	return s, workspace.ResolvedContent{Rendered: []render.Result{rendered}}, resources, observed
}
func planned(t *testing.T, s workspace.Spec, c workspace.ResolvedContent, r workspace.Resources, o workspace.Observations) workspace.PlannedWorkspace {
	t.Helper()
	p, err := workspace.Plan(s, c, r, o)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func treeAction(t *testing.T, p workspace.PlannedWorkspace) workspace.Action {
	t.Helper()
	for _, a := range p.Actions() {
		if a.Kind == workspace.TreeAction {
			return a
		}
	}
	t.Fatal("no managed-tree action")
	return workspace.Action{}
}
func rootObservation(o *workspace.Observations, id string) *workspace.RootObservation {
	for i := range o.Roots {
		if o.Roots[i].RootID == id {
			return &o.Roots[i]
		}
	}
	panic("fixture root absent")
}

func TestPlanIsFrozenAndKeepsOrderedBindings(t *testing.T) {
	s, c, r, o := planInputs(t)
	empty := tree("empty.txt").Entries[0]
	empty.Bytes = []byte{}
	c.Rendered[0].Tree.Entries = append(c.Rendered[0].Tree.Entries, empty)
	p := planned(t, s, c, r, o)
	before := treeAction(t, p)
	bindings := p.Bindings()
	bindings[0].Argv[0] = "changed"
	bindings[0].Environment["FIXTURE_ROOT"] = "changed"
	actions := p.Actions()
	for i := range actions {
		if actions[i].Kind == workspace.TreeAction {
			actions[i].Request.Artifacts.Entries[0].Bytes[0] = 'X'
		}
	}
	c.Rendered[0].Tree.Entries[0].Bytes[0] = 'Y'
	c.Rendered[0].Binding.Argv[0] = "input changed"
	roots := p.Roots()
	roots[0].Owner = "changed"
	if !reflect.DeepEqual(treeAction(t, p), before) {
		t.Fatal("mutation escaped a frozen tree")
	}
	if got := p.Bindings()[0]; got.Argv[0] != "--one" || got.Environment["FIXTURE_ROOT"] != s.Boot.Candidate.Path || !got.BeforeResume {
		t.Fatal("binding order or copy changed")
	}
	for _, e := range before.Request.Artifacts.Entries {
		if e.Path == "empty.txt" && e.Bytes == nil {
			t.Fatal("empty file became unresolved")
		}
	}
	if p.RenderDiagnostics()[0].Code != "fixture_omission" {
		t.Fatal("renderer omission lost")
	}
	for _, d := range p.Diagnostics() {
		if d.Status == workspace.Ready {
			t.Fatal("plan issued ready")
		}
	}
	if before.Request.Generation != p.Digest() || before.Request.Operation != materialize.OperationCreate {
		t.Fatal("create generation not bound to frozen input")
	}
}

func TestDesiredDigestExcludesSnapshotTimeButIncludesBytesAndArgv(t *testing.T) {
	s, c, r, o := planInputs(t)
	p := planned(t, s, c, r, o)
	o.At = o.At.Add(time.Hour)
	o.ExpiresAt = o.ExpiresAt.Add(time.Hour)
	slices.Reverse(r.Roots)
	slices.Reverse(o.Roots)
	slices.Reverse(r.Capabilities)
	if other := planned(t, s, c, r, o); other.Digest() != p.Digest() || !reflect.DeepEqual(other.LockKeys(), p.LockKeys()) {
		t.Fatal("set order or evidence time changed desired digest")
	}
	slices.Reverse(c.Rendered[0].Binding.Argv)
	if other := planned(t, s, c, r, o); other.Digest() == p.Digest() {
		t.Fatal("argv order missing from digest")
	}
	slices.Reverse(c.Rendered[0].Binding.Argv)
	c.Rendered[0].Tree.Entries[0].Bytes = []byte("different")
	if other := planned(t, s, c, r, o); other.Digest() == p.Digest() {
		t.Fatal("bytes missing from digest")
	}
	o.Receipts = []workspace.Receipt{{SchemaVersion: workspace.SchemaVersion, OperationID: s.OperationID, IdentityKey: s.Identity.EncodedKey, InputDigest: p.Digest()}}
	_, err := workspace.Plan(s, c, r, o)
	refusal(t, err, "operation_id_reused")
}

func TestPlanCreateOwnedEmptyAndExplicitCandidateRefresh(t *testing.T) {
	s, c, r, o := planInputs(t)
	observed := rootObservation(&o, s.Boot.Candidate.ID)
	observed.Exists = true
	observed.Directory = true
	_, err := workspace.Plan(s, c, r, o)
	refusal(t, err, "missing_committed_manifest")
	observed.Empty = true
	p := planned(t, s, c, r, o)
	if treeAction(t, p).Request.ExistingTarget != materialize.ExistingTargetAllowEmpty {
		t.Fatal("owned empty target not explicit")
	}
	observed.Empty = false
	observed.Manifest = &materialize.Manifest{Generation: "fixture-generation", Entries: []materialize.ManifestEntry{{Path: "AGENTS.md", Kind: artifact.EntryFile}}}
	_, err = workspace.Plan(s, c, r, o)
	refusal(t, err, "stale_candidate_generation")
	s.Boot.CandidateGeneration = "fixture-generation"
	a := treeAction(t, planned(t, s, c, r, o))
	if a.Request.Operation != materialize.OperationReconcile || a.Request.ExpectedGeneration != s.Boot.CandidateGeneration || a.Request.Reconcile.Conflict != materialize.ConflictReport {
		t.Fatal("owned refresh policy lost")
	}
	observed.Manifest.Entries = append(observed.Manifest.Entries, materialize.ManifestEntry{Path: "auth.json", Kind: artifact.EntryFile})
	_, err = workspace.Plan(s, c, r, o)
	refusal(t, err, "credential_owned_invalid")
}

func TestPlanEmptyTreeHasNoEngineRequestAndBootParentLockIsStable(t *testing.T) {
	s, c, r, o := planInputs(t)
	c.Rendered[0].Tree = artifact.Tree{}
	p := planned(t, s, c, r, o)
	for _, a := range p.Actions() {
		if a.Kind == workspace.TreeAction || a.Kind == workspace.DeferredAction {
			t.Fatal("empty tree reached engine")
		}
	}
	before := p.LockKeys()
	s.Boot.Candidate.Path = filepath.Join(s.Boot.IdentityRoot.Path, "other-candidate")
	for i := range r.Roots {
		if r.Roots[i].ID == s.Boot.Candidate.ID {
			r.Roots[i] = s.Boot.Candidate
		}
	}
	rootObservation(&o, s.Boot.Candidate.ID).CanonicalPath = s.Boot.Candidate.Path
	if !reflect.DeepEqual(planned(t, s, c, r, o).LockKeys(), before) {
		t.Fatal("candidate generation changed mutation lock")
	}
}

func TestPlanRejectsUncertainRootsAndUnknownRendererEffects(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		change     func(*workspace.Spec, *workspace.ResolvedContent, *workspace.Resources, *workspace.Observations)
	}{
		{"window", "invalid_observation_window", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			o.ExpiresAt = o.At
		}},
		{"authority", "missing_effect_grant", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			r.Grants = nil
		}},
		{"capability", "required_capability_unavailable", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			o.Capabilities = nil
		}},
		{"unknown", "unknown_canonical_root", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			o.Roots = nil
		}},
		{"alias", "ambiguous_root_alias", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			rootObservation(o, s.Home.Root.ID).CanonicalPath = s.Boot.Current.Path
		}},
		{"credential", "reserved_artifact_path", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			c.Rendered[0].Tree = tree("auth.json")
		}},
		{"effect", "unsupported_render_effect", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			c.Rendered[0].Effects = []layout.Row{{Form: layout.File, Path: "auth.json"}}
		}},
		{"preparation", "unsupported_preparation", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			c.Rendered[0].Preparations = []render.Preparation{{Kind: "unknown"}}
		}},
		{"duplicate", "duplicate_render_target", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			c.Rendered = append(c.Rendered, c.Rendered[0])
		}},
		{"partial digest", "artifact_digest_mismatch", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			c.Rendered[0].Tree.Entries[0].Digest = artifact.Digest{Hex: "invalid"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, c, r, o := planInputs(t)
			tc.change(&s, &c, &r, &o)
			_, err := workspace.Plan(s, c, r, o)
			refusal(t, err, tc.code)
		})
	}
}

func TestRealRenderOwnershipAndInstalledApplyDeferral(t *testing.T) {
	s, _, r, o := planInputs(t)
	resolution, err := layout.Resolve(layout.Request{Key: layout.Key{Provider: runtimes.Claude, Layer: layout.Boot, Mode: runtimes.ModeSubprocessPerTurn, Field: layout.Instructions}, Requirement: layout.Required})
	if err != nil {
		t.Fatal(err)
	}
	settings, err := layout.Resolve(layout.Request{Key: layout.Key{Provider: runtimes.Claude, Layer: layout.Boot, Mode: runtimes.ModeSubprocessPerTurn, Field: layout.Settings}, Requirement: layout.Required})
	if err != nil {
		t.Fatal(err)
	}
	out, err := render.Render(render.Request{Provider: runtimes.Claude, Layer: layout.Boot, Mode: runtimes.ModeSubprocessPerTurn, Agent: "fixture", Roots: map[layout.Root]string{layout.RootBoot: s.Boot.Candidate.Path, layout.RootProject: s.Home.Root.Path}, Native: render.NativeInputs{Claude: claude.SettingsInput{APIKeyHelper: "fixture-helper"}}, Inputs: []render.Input{{Resolved: settings}, {Resolved: resolution, Content: render.Content{Body: []byte("fixture instructions"), Pin: render.Pin{Source: "fixture", Revision: "fixture-revision"}}}}, Credentials: render.CredentialAvailable})
	if err != nil {
		t.Fatal(err)
	}
	p := planned(t, s, workspace.ResolvedContent{Rendered: []render.Result{out}}, r, o)
	a := treeAction(t, p)
	ownershipFound := false
	for _, entry := range out.Tree.Entries {
		if entry.Path == ".claude/settings.json" {
			var metadata render.DocumentOwnership
			if json.Unmarshal([]byte(entry.Provenance.Note), &metadata) != nil || metadata.Schema != "native-key-ownership.v1" {
				t.Fatal("native ownership schema absent")
			}
			ownershipFound = true
		}
		found := false
		for _, applied := range a.Request.Artifacts.Entries {
			if applied.Path == entry.Path {
				found = true
				if applied.Ownership != entry.Ownership || applied.Provenance != entry.Provenance || applied.Mode != entry.Mode {
					t.Fatal("render ownership/provenance/mode altered")
				}
			}
		}
		if !found {
			t.Fatal("render entry lost")
		}
	}
	if !ownershipFound {
		t.Fatal("native ownership case not exercised")
	}
	s.Operation = workspace.Install
	homeGrant := workspace.EffectGrant{Kind: workspace.ArtifactEffect, RootID: s.Home.Root.ID, AuthorizationID: "fixture-authority", Version: "1"}
	s.Effects = append(s.Effects, homeGrant)
	r.Grants = append(r.Grants, homeGrant)
	settings, err = layout.Resolve(layout.Request{Key: layout.Key{Provider: runtimes.Claude, Layer: layout.Installed, Mode: layout.InstallMode, Field: layout.Settings}, Requirement: layout.Required})
	if err != nil {
		t.Fatal(err)
	}
	out, err = render.Render(render.Request{Provider: runtimes.Claude, Layer: layout.Installed, Mode: layout.InstallMode, Agent: "fixture", DefinitionName: "fixture-definition", Roots: map[layout.Root]string{layout.RootHome: s.Home.Root.Path}, Inputs: []render.Input{{Resolved: settings}}, Native: render.NativeInputs{Claude: claude.SettingsInput{APIKeyHelper: "fixture-helper"}}, Credentials: render.CredentialAvailable})
	if err != nil {
		t.Fatal(err)
	}
	p = planned(t, s, workspace.ResolvedContent{Rendered: []render.Result{out}}, r, o)
	deferred := false
	for _, action := range p.Actions() {
		if action.Kind == workspace.DeferredAction {
			deferred = true
		}
		if action.Kind == workspace.TreeAction {
			t.Fatal("installed output became regular replacement")
		}
	}
	if !deferred {
		t.Fatal("installed merge requirement lost")
	}
}
