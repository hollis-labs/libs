package workspace

// Regression cases for planning guards, frozen state and action contents.
// Fixtures model explicit host ownership and canonical observations.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	layout "github.com/hollis-labs/substrate/harness/adapters/layout/plan"
	"github.com/hollis-labs/substrate/harness/sandbox"
	"github.com/hollis-labs/substrate/harness/workspace/bootkey"
	"github.com/hollis-labs/substrate/harness/workspace/materialize"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
	"github.com/hollis-labs/substrate/harness/workspace/render"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
)

// ---------------------------------------------------------------- fixtures

func fixtureBootPath(t *testing.T) string {
	t.Helper()
	key, err := bootkey.Encode("urn:fixture:agent:one")
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join("/fixture", key)
}

func fixtureRoot(id string) RootRef {
	base := filepath.Join(string(filepath.Separator), "fixture")
	return RootRef{ID: id, Path: filepath.Join(base, id), AllowedBase: base, Owner: "fixture-owner", Provenance: "fixture"}
}

func fixtureSpec(t *testing.T) Spec {
	t.Helper()
	key, err := bootkey.Encode("urn:fixture:agent:one")
	if err != nil {
		t.Fatal(err)
	}
	d := artifact.DigestBytes([]byte("fixture"))
	boot := fixtureRoot("boot")
	boot.Path = filepath.Join(boot.AllowedBase, key)
	current, candidate := fixtureRoot("current"), fixtureRoot("candidate")
	current.Path = filepath.Join(boot.Path, "current")
	candidate.Path = filepath.Join(boot.Path, "candidate")
	return Spec{
		SchemaVersion: SchemaVersion, OperationID: "fixture-operation", Operation: Prepare,
		Identity: IdentitySpec{AgentURN: "urn:fixture:agent:one", EncodedKey: key, Session: "fixture-session", DefinitionRevision: "fixture-revision", SemanticDigest: d, ArtifactDigest: d, DependencyDigest: d, Fence: ResourceRef{ID: "fixture-fence", Revision: "1"}},
		Home:     HomeSpec{Root: fixtureRoot("home"), Layout: FullHome, Continuity: Durable, Retention: Keep},
		Boot:     BootSpec{IdentityRoot: boot, Current: current, Candidate: candidate, Retention: RetainForRecovery},
		CWD:      CWDSpec{RootID: "home", Relative: "."},
		Sandbox:  SandboxSpec{Policy: sandbox.ResolvedAccessPolicy{Mode: sandbox.ConfinementDisabled}},
		Cleanup:  CleanupPolicy{Retention: Keep},
	}
}

func fixtureTree(rel string) artifact.Tree {
	return artifact.Tree{Entries: []artifact.Entry{{Path: rel, Kind: artifact.EntryFile, Mode: 0644, Bytes: []byte("fixture"), Ownership: artifact.Ownership{EntryID: "fixture-entry", GroupID: "fixture-group"}, Provenance: artifact.Provenance{Source: "fixture"}}}}
}

func fixturePlanInputs(t *testing.T) (Spec, ResolvedContent, Resources, Observations) {
	t.Helper()
	s := fixtureSpec(t)
	s.Effects = []EffectGrant{
		{Kind: DirectoryEffect, RootID: s.Home.Root.ID, AuthorizationID: "fixture-authority", Version: "1"},
		{Kind: DirectoryEffect, RootID: s.Boot.IdentityRoot.ID, AuthorizationID: "fixture-authority", Version: "1"},
		{Kind: ArtifactEffect, RootID: s.Boot.Candidate.ID, AuthorizationID: "fixture-authority", Version: "1"},
	}
	r := Resources{Roots: []RootRef{s.Home.Root, s.Boot.IdentityRoot, s.Boot.Current, s.Boot.Candidate}, LockNamespace: filepath.Join(s.Home.Root.AllowedBase, "locks"), Capabilities: []Capability{CanonicalRoots, MutationLocks}}
	r.LockRoot = fixtureRoot("locks")
	r.Grants = slices.Clone(s.Effects)
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	o := Observations{At: at, ExpiresAt: at.Add(time.Minute), FenceVersion: s.Identity.Fence.Revision, Capabilities: slices.Clone(r.Capabilities)}
	for _, ref := range r.Roots {
		o.Roots = append(o.Roots, RootObservation{RootID: ref.ID, DeclaredPath: ref.Path, CanonicalPath: ref.Path, CanonicalBase: ref.AllowedBase, Owner: ref.Owner})
	}
	o.Roots = append(o.Roots, RootObservation{RootID: r.LockRoot.ID, DeclaredPath: r.LockRoot.Path, CanonicalPath: r.LockRoot.Path, CanonicalBase: r.LockRoot.AllowedBase, Owner: r.LockRoot.Owner, Exists: true, Directory: true})
	rendered := render.Result{
		Provider: runtimes.Claude, Layer: layout.Boot, Mode: runtimes.ModeSubprocessPerTurn, Root: layout.RootBoot, RootMode: 0700,
		Tree:        fixtureTree("AGENTS.md"),
		Binding:     render.Binding{Argv: []string{"--add-dir", s.Home.Root.Path}, Environment: map[string]string{}, CWD: s.Boot.Candidate.Path},
		Diagnostics: []render.Diagnostic{{Code: "fixture_omission", Class: render.ClassOmission, Reason: "fixture optional omission"}},
	}
	c := ResolvedContent{Rendered: []render.Result{rendered}, Roots: map[layout.Root]string{layout.RootBoot: s.Boot.Candidate.Path, layout.RootProject: s.Home.Root.Path}}
	return s, c, r, o
}

// fixtureRichInputs adds scratch space, selection, access references, a declared
// credential, provider state and an existing candidate with a manifest.
func fixtureRichInputs(t *testing.T) (Spec, ResolvedContent, Resources, Observations) {
	t.Helper()
	s, c, r, o := fixturePlanInputs(t)
	s.ExtraDirs = []AccessRef{{Resource: ResourceRef{ID: "x", Path: "/fixture/x", Provenance: "fixture"}, Access: []sandbox.AccessKind{sandbox.AccessRead, sandbox.AccessWrite}, Purpose: "p"}}
	x := fixtureRoot("x")
	r.Roots = append(r.Roots, x)
	o.Roots = append(o.Roots, RootObservation{RootID: x.ID, DeclaredPath: x.Path, CanonicalPath: x.Path, CanonicalBase: x.AllowedBase, Owner: x.Owner})
	s.Boot.Selection = materialize.Selection{Groups: []string{"g1", "g2"}, EntryIDs: []string{"e1"}}
	sc := fixtureRoot("scratch")
	s.Scratch = []ScratchSpec{{Root: sc, Session: s.Identity.Session, Environment: map[string]string{"B": "2", "A": "1", "C": "3"}, Retention: Keep}}
	s.ProviderState = []ResourceRef{{ID: "ps", Path: "/fixture/ps"}}
	s.Credentials = []CredentialSpec{{Source: ResourceRef{ID: "cs", Path: "/fixture/cs"}, Authorization: ResourceRef{ID: "auth"}, DestinationRootID: "candidate", Destination: "private/token", Concern: "fixture", Required: true, Access: []sandbox.AccessKind{sandbox.AccessSourceRead}}}
	s.Effects = append(s.Effects, EffectGrant{Kind: DirectoryEffect, RootID: sc.ID, AuthorizationID: "fixture-authority", Version: "1"})
	r.Grants = slices.Clone(s.Effects)
	r.Roots = append(r.Roots, sc)
	o.Roots = append(o.Roots, RootObservation{RootID: sc.ID, DeclaredPath: sc.Path, CanonicalPath: sc.Path, CanonicalBase: sc.AllowedBase, Owner: sc.Owner})
	cand := fixtureObservation(&o, s.Boot.Candidate.ID)
	cand.Exists, cand.Directory = true, true
	cand.Manifest = &materialize.Manifest{Generation: "gen", Entries: []materialize.ManifestEntry{{Path: "AGENTS.md", Kind: artifact.EntryFile}}}
	s.Boot.CandidateGeneration = "gen"
	c.Rendered[0].Provider = runtimes.Codex
	c.Rendered[0].Binding.Environment = map[string]string{"CODEX_HOME": s.Boot.Candidate.Path}
	c.Rendered[0].Binding.Argv = []string{"--cd", s.Home.Root.Path}
	c.Rendered[0].Binding.Posture = &layout.PostureReference{Provider: runtimes.Codex, Mapper: "adapters/registry.Descriptor.PostureFor", Posture: "default"}
	c.Rendered[0].Preparations = []render.Preparation{{Provider: runtimes.Codex, Kind: render.PreparationCredentialAvailability}}
	return s, c, r, o
}

// fixtureInstallInputs turns the default inputs into an installed-layer operation
// with a single granted home tree.
func fixtureInstallInputs(t *testing.T) (Spec, ResolvedContent, Resources, Observations) {
	t.Helper()
	s, c, r, o := fixturePlanInputs(t)
	s.Operation = Install
	g := EffectGrant{Kind: ArtifactEffect, RootID: s.Home.Root.ID, AuthorizationID: "fixture-authority", Version: "1"}
	s.Effects = append(s.Effects, g)
	r.Grants = append(r.Grants, g)
	c.Rendered[0] = render.Result{Provider: runtimes.Claude, Layer: layout.Installed, Mode: layout.InstallMode, Root: layout.RootHome, Tree: fixtureTree("fixture.txt")}
	c.Roots = map[layout.Root]string{layout.RootHome: s.Home.Root.Path}
	return s, c, r, o
}

func fixturePlanned(t *testing.T, s Spec, c ResolvedContent, r Resources, o Observations) PlannedWorkspace {
	t.Helper()
	p, err := Plan(s, c, r, o)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func fixtureTreeAction(t *testing.T, p PlannedWorkspace) Action {
	t.Helper()
	for _, a := range p.Actions() {
		if a.Kind == TreeAction {
			return a
		}
	}
	t.Fatal("no managed-tree action")
	return Action{}
}

func fixtureObservation(o *Observations, id string) *RootObservation {
	for i := range o.Roots {
		if o.Roots[i].RootID == id {
			return &o.Roots[i]
		}
	}
	panic("fixture root observation absent")
}

// fixtureExpect asserts a typed refusal code; an empty code asserts acceptance.
func fixtureExpect(t *testing.T, name string, err error, code string) {
	t.Helper()
	if code == "" {
		if err != nil {
			t.Errorf("%s: unexpected refusal %v", name, err)
		}
		return
	}
	if err == nil {
		t.Errorf("%s: accepted, want %s", name, code)
		return
	}
	var got *Refusal
	if !errors.As(err, &got) || got.Code != code {
		t.Errorf("%s: got %v want %s", name, err, code)
	}
}

type (
	fixturePlanMutation func(*Spec, *ResolvedContent, *Resources, *Observations)
	fixtureCase         struct {
		name, code string
		change     fixturePlanMutation
	}
)

func fixtureRun(t *testing.T, cases []fixtureCase) {
	t.Helper()
	for _, tc := range cases {
		s, c, r, o := fixturePlanInputs(t)
		tc.change(&s, &c, &r, &o)
		_, err := Plan(s, c, r, o)
		fixtureExpect(t, tc.name, err, tc.code)
	}
}

// -------------------------------------------------------- group 1: Plan flow

func TestPlanningPlanRefusals(t *testing.T) {
	cases := []fixtureCase{
		{"plan validates the spec", "unsupported_schema", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) { s.SchemaVersion = "fixture-future" }},
		{"recover is deferred", "operation_deferred", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) { s.Operation = Recover }},
		{"retire is deferred", "operation_deferred", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) { s.Operation = Retire }},
		{"zero observation time", "invalid_observation_window", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			o.At = time.Time{}
			o.ExpiresAt = time.Unix(100, 0)
		}},
		{"fence version mismatch", "invalid_observation_window", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) { o.FenceVersion = "fixture-other" }},
		{"time outside the encodable range", "invalid_frozen_input", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			o.At = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			o.ExpiresAt = o.At.Add(time.Minute)
		}},
		{"expected current generation with no current", "stale_current_generation", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) { s.Boot.ExpectedGeneration = "g1" }},
		{"expected current generation mismatch", "stale_current_generation", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			s.Boot.ExpectedGeneration = "g1"
			cur := fixtureObservation(o, s.Boot.Current.ID)
			cur.Exists, cur.Directory = true, true
			cur.Manifest = &materialize.Manifest{Generation: "g2"}
		}},
		{"candidate is not a canonical sibling", "observation_base_mismatch", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			fixtureObservation(o, s.Boot.Candidate.ID).CanonicalPath = "/fixture/elsewhere/candidate"
		}},
		{"current is not a canonical sibling", "observation_base_mismatch", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			fixtureObservation(o, s.Boot.Current.ID).CanonicalPath = "/fixture/elsewhere/current"
		}},
		{"unknown cwd root", "unknown_cwd_root", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) { s.CWD.RootID = "fixture-absent" }},
		{"effect names an unknown root", "missing_effect_root", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			s.Effects = append(s.Effects, EffectGrant{Kind: TrustEffect, RootID: "fixture-ghost", AuthorizationID: "a", Version: "1"})
			r.Grants = slices.Clone(s.Effects)
		}},
		{"unclean canonical path", "unknown_canonical_root", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			fixtureObservation(o, s.Boot.Candidate.ID).CanonicalPath = filepath.Join(fixtureBootPath(t), "candidate") + "/"
		}},
		{"relative canonical base", "unknown_canonical_root", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			fixtureObservation(o, s.Boot.Candidate.ID).CanonicalBase = "fixture"
		}},
		{"unclean canonical base", "unknown_canonical_root", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			fixtureObservation(o, s.Boot.Candidate.ID).CanonicalBase = "/fixture/"
		}},
		{"canonical path outside its base", "unknown_canonical_root", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			fixtureObservation(o, s.Boot.Candidate.ID).CanonicalBase = "/fixture-other"
		}},
		{"uncertain observation", "unknown_canonical_root", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			fixtureObservation(o, s.Boot.Candidate.ID).Uncertainty = "fixture uncertainty"
		}},
		{"owner mismatch", "root_owner_mismatch", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			fixtureObservation(o, s.Boot.Candidate.ID).Owner = "fixture-other-owner"
		}},
		{"absent root claims to be empty", "inconsistent_root_observation", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			fixtureObservation(o, s.Boot.Candidate.ID).Empty = true
		}},
		{"absent root claims a manifest", "inconsistent_root_observation", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			fixtureObservation(o, s.Boot.Candidate.ID).Manifest = &materialize.Manifest{Generation: "g"}
		}},
		{"absent root claims disk entries", "inconsistent_root_observation", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			fixtureObservation(o, s.Boot.Candidate.ID).Disk = []materialize.ManifestEntry{{Path: "x"}}
		}},
		{"existing root is not a directory", "root_not_directory", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			fixtureObservation(o, s.Boot.Candidate.ID).Exists = true
		}},
		{"duplicate observation", "duplicate_root_observation", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			o.Roots = append(o.Roots, o.Roots[0])
		}},
		{"manifest claims a declared credential destination", "credential_owned_invalid", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			s.Credentials = []CredentialSpec{{Source: ResourceRef{ID: "cs"}, Authorization: ResourceRef{ID: "a"}, DestinationRootID: "candidate", Destination: "private/token", Concern: "c"}}
			cand := fixtureObservation(o, s.Boot.Candidate.ID)
			cand.Exists, cand.Directory = true, true
			cand.Manifest = &materialize.Manifest{Generation: "g", Entries: []materialize.ManifestEntry{{Path: "private/token", Kind: artifact.EntryFile}}}
		}},
		{"boot root mode too open", "unsafe_boot_root_mode", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) { c.Rendered[0].RootMode = 0755 }},
		{"boot root mode too closed", "unsafe_boot_root_mode", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) { c.Rendered[0].RootMode = 0600 }},
		{"declared credential collides with the tree", "credential_artifact_collision", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			s.Credentials = []CredentialSpec{{Source: ResourceRef{ID: "cs"}, Authorization: ResourceRef{ID: "a"}, DestinationRootID: "candidate", Destination: "AGENTS.md", Concern: "c"}}
		}},
		{"rendered credential effect collides with the tree", "unsupported_render_effect", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			c.Rendered[0].Effects = []layout.Row{{Form: layout.Link, CredentialPolicy: layout.LinkOnlyNeverWrite, Path: "AGENTS.md"}}
		}},
		{"candidate generation differs", "stale_candidate_generation", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			cand := fixtureObservation(o, s.Boot.Candidate.ID)
			cand.Exists, cand.Directory = true, true
			cand.Manifest = &materialize.Manifest{Generation: "g"}
			s.Boot.CandidateGeneration = "fixture-other"
		}},
		{"candidate generation unauthorized", "stale_candidate_generation", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			cand := fixtureObservation(o, s.Boot.Candidate.ID)
			cand.Exists, cand.Directory = true, true
			cand.Manifest = &materialize.Manifest{Generation: ""}
		}},
		{"grant version differs", "missing_effect_grant", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			r.Grants = slices.Clone(r.Grants)
			for i := range r.Grants {
				r.Grants[i].Version = "2"
			}
		}},
		{"grant authorization differs", "missing_effect_grant", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			r.Grants = slices.Clone(r.Grants)
			for i := range r.Grants {
				r.Grants[i].AuthorizationID = "fixture-other"
			}
		}},
		{"grant kind differs", "missing_effect_grant", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			r.Grants = slices.Clone(r.Grants)
			for i := range r.Grants {
				if r.Grants[i].Kind == DirectoryEffect {
					r.Grants[i].Kind = ArtifactEffect
				}
			}
		}},
		{"intent kind differs", "missing_effect_grant", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			for i := range s.Effects {
				if s.Effects[i].Kind == DirectoryEffect && s.Effects[i].RootID == s.Home.Root.ID {
					s.Effects[i].Kind = ArtifactEffect
				}
			}
			r.Grants = slices.Clone(s.Effects)
		}},
		{"intent root differs", "missing_effect_grant", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			var kept []EffectGrant
			for _, e := range s.Effects {
				if !(e.Kind == DirectoryEffect && e.RootID == s.Home.Root.ID) {
					kept = append(kept, e)
				}
			}
			s.Effects = kept
			r.Grants = slices.Clone(kept)
		}},
		{"capability only on the resource side", "required_capability_unavailable", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) { o.Capabilities = nil }},
		{"capability only on the observation side", "required_capability_unavailable", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) { r.Capabilities = nil }},
		{"unknown resource capability", "unsupported_capability", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			r.Capabilities = append(r.Capabilities, "fixture-future")
		}},
		{"unknown observed capability", "unsupported_capability", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			o.Capabilities = append(o.Capabilities, "fixture-future")
		}},
		{"receipt schema differs", "invalid_operation_receipt", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			o.Receipts = []Receipt{{SchemaVersion: "fixture-other", OperationID: s.OperationID, IdentityKey: s.Identity.EncodedKey}}
		}},
		{"receipt identity key differs", "invalid_operation_receipt", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			o.Receipts = []Receipt{{SchemaVersion: SchemaVersion, OperationID: s.OperationID, IdentityKey: "fixture-other"}}
		}},
		{"installed render under a boot operation", "installed_operation_required", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			c.Rendered[0] = render.Result{Provider: runtimes.Claude, Layer: layout.Installed, Mode: layout.InstallMode, Root: layout.RootHome, Tree: fixtureTree("fixture.txt")}
		}},
		{"boot render under an install operation", "installed_render_required", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) { s.Operation = Install }},
		{"installed render changes the root mode", "installed_root_mode_change", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			s.Operation = Install
			c.Rendered[0] = render.Result{Provider: runtimes.Claude, Layer: layout.Installed, Mode: layout.InstallMode, Root: layout.RootHome, RootMode: 0700, Tree: fixtureTree("fixture.txt")}
			c.Roots = map[layout.Root]string{layout.RootHome: s.Home.Root.Path}
		}},
		{"installed tree without an artifact grant", "missing_effect_grant", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			s.Operation = Install
			c.Rendered[0] = render.Result{Provider: runtimes.Claude, Layer: layout.Installed, Mode: layout.InstallMode, Root: layout.RootHome, Tree: fixtureTree("fixture.txt")}
			c.Roots = map[layout.Root]string{layout.RootHome: s.Home.Root.Path}
		}},
		{"boot layer with a home root", "invalid_render_context", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			s.Operation = Install
			c.Rendered[0].Root = layout.RootHome
			c.Rendered[0].RootMode = 0
			c.Roots = map[layout.Root]string{layout.RootHome: s.Home.Root.Path}
		}},
		{"installed layer with a boot root", "invalid_render_context", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			c.Rendered[0].Layer = layout.Installed
			c.Rendered[0].Mode = layout.InstallMode
		}},
		{"owner mismatch for a root not listed in the resources", "missing_host_root", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			r.Roots = nil
			fixtureObservation(o, s.Boot.Candidate.ID).Owner = "fixture-other-owner"
		}},
		{"owner mismatch for the current root only listed in the spec", "missing_host_root", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			r.Roots = nil
			fixtureObservation(o, s.Boot.Current.ID).Owner = "fixture-other-owner"
		}},
		{"invalid extra resource root", "missing_root_ownership", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			bad := fixtureRoot("extra")
			bad.Provenance = ""
			r.Roots = append(r.Roots, bad)
		}},
		{"conflicting duplicate root reference", "ambiguous_root_reference", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			dup := s.Home.Root
			dup.Provenance = "fixture-different"
			r.Roots = append(r.Roots, dup)
		}},
	}
	fixtureRun(t, cases)
}

// -------------------------------------------- group 2: renderer integration

func TestPlanningRenderAdapterRefusals(t *testing.T) {
	cases := []fixtureCase{
		{"diagnostic without a code", "invalid_render_diagnostic", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			c.Rendered[0].Diagnostics[0].Code = ""
		}},
		{"diagnostic names another provider", "invalid_render_diagnostic", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			c.Rendered[0].Diagnostics[0].Provider = runtimes.Codex
		}},
		{"diagnostic names another mode", "invalid_render_diagnostic", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			c.Rendered[0].Diagnostics[0].Mode = runtimes.ModeHTTPSSE
		}},
		{"entry with a symlink mode", "unsafe_render_entry", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			c.Rendered[0].Tree.Entries[0].Mode = fs.ModeSymlink | 0777
		}},
		{"entry with a renderer-unsafe path", "unsafe_render_entry", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			c.Rendered[0].Tree.Entries[0].Path = ".git/config"
		}},
		{"duplicate entries", "invalid_artifact_tree", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			c.Rendered[0].Tree.Entries = append(c.Rendered[0].Tree.Entries, c.Rendered[0].Tree.Entries[0])
		}},
		{"forged note on a separate MCP document", "invalid_render_ownership", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			c.Rendered[0].Tree.Entries[0].Path = ".mcp.json"
			c.Rendered[0].Tree.Entries[0].Provenance.Note = "forged"
		}},
		{"forged note on a case variant of a document path", "invalid_render_ownership", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			c.Rendered[0].Tree.Entries[0].Path = ".CLAUDE/SETTINGS.JSON"
			c.Rendered[0].Tree.Entries[0].Provenance.Note = "forged"
		}},
		{"plugin document with a forged note", "invalid_render_ownership", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			c.Rendered[0].Provider = runtimes.Antigravity
			c.Rendered[0].Tree.Entries[0].Path = ".agents/plugins/tether/plugin.json"
			c.Rendered[0].Tree.Entries[0].Provenance.Note = "forged"
		}},
		{"effect row is not a link", "unsupported_render_effect", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			c.Rendered[0].Effects = []layout.Row{{Form: layout.File, CredentialPolicy: layout.LinkOnlyNeverWrite, Path: "auth.json"}}
		}},
		{"effect row without the never-write policy", "unsupported_render_effect", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			c.Rendered[0].Effects = []layout.Row{{Form: layout.Link, Path: "auth.json"}}
		}},
		{"effect row with an unsafe path", "unsupported_render_effect", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			c.Rendered[0].Effects = []layout.Row{{Form: layout.Link, CredentialPolicy: layout.LinkOnlyNeverWrite, Path: "../escape"}}
		}},
		{"effect row with launch argv", "unsupported_render_effect", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			c.Rendered[0].Effects = []layout.Row{{Form: layout.Link, CredentialPolicy: layout.LinkOnlyNeverWrite, Path: "auth.json", Locator: layout.Locator{Argv: []string{"--x"}}}}
		}},
		{"effect row with launch environment", "unsupported_render_effect", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			c.Rendered[0].Effects = []layout.Row{{Form: layout.Link, CredentialPolicy: layout.LinkOnlyNeverWrite, Path: "auth.json", Locator: layout.Locator{Env: map[string]layout.Root{"FIXTURE": layout.RootBoot}}}}
		}},
		{"effect row with a launch cwd", "unsupported_render_effect", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			c.Rendered[0].Effects = []layout.Row{{Form: layout.Link, CredentialPolicy: layout.LinkOnlyNeverWrite, Path: "auth.json", Locator: layout.Locator{CWD: layout.RootBoot}}}
		}},
		{"effect row with an RPC project", "unsupported_render_effect", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			c.Rendered[0].Effects = []layout.Row{{Form: layout.Link, CredentialPolicy: layout.LinkOnlyNeverWrite, Path: "auth.json", Locator: layout.Locator{RPCProject: "thread.cwd"}}}
		}},
		{"effect row with before-resume placement", "unsupported_render_effect", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			c.Rendered[0].Effects = []layout.Row{{Form: layout.Link, CredentialPolicy: layout.LinkOnlyNeverWrite, Path: "auth.json", Locator: layout.Locator{BeforeResume: true}}}
		}},
		{"credential link without the never-write policy", "unsupported_preparation", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			c.Rendered[0].Preparations = []render.Preparation{{Provider: runtimes.Claude, Kind: render.PreparationCredentialLink, Destination: "x.json"}}
		}},
		{"credential link with an unsafe destination", "unsupported_preparation", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			c.Rendered[0].Preparations = []render.Preparation{{Provider: runtimes.Claude, Kind: render.PreparationCredentialLink, Destination: "../x", Policy: layout.LinkOnlyNeverWrite}}
		}},
		{"availability check with a destination", "unsupported_preparation", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			c.Rendered[0].Preparations = []render.Preparation{{Provider: runtimes.Claude, Kind: render.PreparationCredentialAvailability, Destination: "x.json"}}
		}},
		{"unknown preparation kind for the matching provider", "unsupported_preparation", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			c.Rendered[0].Preparations = []render.Preparation{{Provider: runtimes.Claude, Kind: "fixture-future"}}
		}},
	}
	fixtureRun(t, cases)
	// An informational diagnostic is accepted alongside an omission.
	s, c, r, o := fixturePlanInputs(t)
	c.Rendered[0].Diagnostics[0].Class = render.ClassInformational
	_, err := Plan(s, c, r, o)
	fixtureExpect(t, "informational diagnostic", err, "")
}

// ------------------------------------------- group 3: plan content and shape

func TestPlanningPlanContent(t *testing.T) {
	s, c, r, o := fixtureRichInputs(t)
	p, err := Plan(s, c, r, o)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, k := range p.LockKeys() {
		ids = append(ids, k.CanonicalID)
	}
	if want := []string{"/fixture/home", "/fixture/scratch", fixtureBootPath(t)}; !slices.Equal(ids, want) {
		t.Errorf("lock set = %v want %v", ids, want)
	}

	type access struct{ path, kinds, purpose string }
	var gotAccess []access
	for _, a := range p.Access() {
		var kinds []string
		for _, k := range a.Access {
			kinds = append(kinds, string(k))
		}
		gotAccess = append(gotAccess, access{a.Resource.Path, strings.Join(kinds, ","), a.Purpose})
	}
	wantAccess := []access{
		{"/fixture/x", "read,write", "p"},
		{"/fixture/home", "write", "identity home"},
		{"/fixture/scratch", "write", "session scratch"},
		{"/fixture/ps", "read", "provider state"},
		{fixtureBootPath(t), "write", "boot preparation"},
		{filepath.Join(fixtureBootPath(t), "candidate"), "write", "boot preparation"},
		{filepath.Join(fixtureBootPath(t), "current"), "read", "current generation inspection"},
		{"/fixture/cs", "source-read", "credential binding source"},
	}
	if !reflect.DeepEqual(gotAccess, wantAccess) {
		t.Errorf("access = %v\nwant %v", gotAccess, wantAccess)
	}

	type step struct {
		kind ActionKind
		root string
		mode fs.FileMode
		caps string
	}
	var gotSteps []step
	for _, a := range p.Actions() {
		caps := []string{}
		for _, c := range a.RequiredCapabilities {
			caps = append(caps, string(c))
		}
		gotSteps = append(gotSteps, step{a.Kind, a.Root.Path, a.RootMode, strings.Join(caps, ",")})
	}
	wantSteps := []step{
		{EnsureDirectoryAction, fixtureBootPath(t), 0700, "canonical_roots,mutation_locks"},
		{TreeAction, filepath.Join(fixtureBootPath(t), "candidate"), 0700, "canonical_roots,mutation_locks"},
		{EnsureDirectoryAction, "/fixture/home", 0700, "canonical_roots,mutation_locks"},
		{EnsureDirectoryAction, "/fixture/scratch", 0700, "canonical_roots,mutation_locks"},
	}
	slices.SortFunc(wantSteps, func(a, b step) int { return strings.Compare(a.root, b.root) })
	if !reflect.DeepEqual(gotSteps, wantSteps) {
		t.Errorf("actions = %v\nwant %v", gotSteps, wantSteps)
	}

	req := fixtureTreeAction(t, p).Request
	if req.TargetRoot != filepath.Join(fixtureBootPath(t), "candidate") || req.Roots.BootRoot != filepath.Join(fixtureBootPath(t), "candidate") || req.Roots.StateRoot != "/fixture/home" || req.ExistingTarget != materialize.ExistingTargetRefuse {
		t.Errorf("reconcile request = %+v", req)
	}
	if !reflect.DeepEqual(req.Selection, s.Boot.Selection) || req.CurrentManifest == nil || req.CurrentManifest.Generation != "gen" || req.Reconcile.Conflict != materialize.ConflictReport {
		t.Errorf("reconcile policy = %+v", req)
	}

	codes := map[string]Status{}
	for _, d := range p.Diagnostics() {
		codes[d.Code] = d.Status
	}
	if codes["launch_reservation_pending"] != Partial {
		t.Errorf("launch reservation diagnostic missing: %v", codes)
	}
	if codes["credential_effect_pending"] != Unsupported {
		t.Errorf("required credential diagnostic missing: %v", codes)
	}

	// Create path for an absent candidate.
	s2, c2, r2, o2 := fixturePlanInputs(t)
	a2 := fixtureTreeAction(t, fixturePlanned(t, s2, c2, r2, o2))
	if a2.Request.TargetRoot != filepath.Join(fixtureBootPath(t), "candidate") || a2.Request.ExistingTarget != materialize.ExistingTargetRefuse ||
		a2.Request.Roots.BootRoot != filepath.Join(fixtureBootPath(t), "candidate") || a2.Request.Roots.StateRoot != "/fixture/home" || a2.RootMode != 0700 ||
		!slices.Equal(a2.RequiredCapabilities, []Capability{CanonicalRoots, MutationLocks}) {
		t.Errorf("create action = %+v", a2)
	}

	// Empty roots cannot simultaneously claim a committed manifest.
	s3, c3, r3, o3 := fixturePlanInputs(t)
	cand := fixtureObservation(&o3, s3.Boot.Candidate.ID)
	cand.Exists, cand.Directory, cand.Empty = true, true, true
	cand.Manifest = &materialize.Manifest{Generation: "g"}
	s3.Boot.CandidateGeneration = "g"
	_, err = Plan(s3, c3, r3, o3)
	fixtureExpect(t, "empty observation with manifest", err, "inconsistent_root_observation")

	// Unbound repository requests refuse; other deferred host effects remain visible.
	sr, cr, rr, or := fixturePlanInputs(t)
	sr.Repos = []RepoSpec{{ID: "r", Source: ResourceRef{ID: "s"}, DesiredRoot: sr.Home.Root, Mode: Worktree, Retention: Keep}}
	_, repoErr := Plan(sr, cr, rr, or)
	fixtureExpect(t, "unbound repository", repoErr, "repository_input_binding")
	for name, mutate := range map[string]func(*Spec){
		"trust":    func(s *Spec) { s.Trust = []TrustSpec{{Mechanism: "m"}} },
		"confined": func(s *Spec) { s.Sandbox.Policy.Mode = sandbox.ConfinementRequired },
	} {
		s, c, r, o := fixturePlanInputs(t)
		mutate(&s)
		found := false
		for _, d := range fixturePlanned(t, s, c, r, o).Diagnostics() {
			if d.Code == "host_effects_pending" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: host_effects_pending missing", name)
		}
	}

	// Installed output is deferred, reported per root and in general, and needs
	// the merge capability.
	si, ci, ri, oi := fixtureInstallInputs(t)
	pi := fixturePlanned(t, si, ci, ri, oi)
	var perRoot, general bool
	for _, d := range pi.Diagnostics() {
		if d.Code == "installed_apply_pending" && d.RootID == si.Home.Root.ID {
			perRoot = true
		}
		if d.Code == "installed_apply_pending" && d.Concern == "installed" {
			general = true
		}
	}
	if !perRoot || !general {
		t.Errorf("installed diagnostics: per-root=%v general=%v", perRoot, general)
	}
	for _, a := range pi.Actions() {
		if a.Kind == DeferredAction && !slices.Equal(a.RequiredCapabilities, []Capability{InstalledMerge}) {
			t.Errorf("deferred capabilities = %v", a.RequiredCapabilities)
		}
	}
}

func TestPlanningDeferredActionCarriesNoEngineFields(t *testing.T) {
	s, c, r, o := fixtureInstallInputs(t)
	found := false
	for _, a := range fixturePlanned(t, s, c, r, o).Actions() {
		switch a.Kind {
		case DeferredAction:
			found = true
			if a.Request.Generation != "" || a.Request.TargetRoot != "" || a.Request.Operation != "" {
				t.Errorf("deferred action carries engine fields: %+v", a.Request)
			}
		case EnsureDirectoryAction:
			t.Errorf("install planned a directory action for %s", a.Root.Path)
		}
	}
	if !found {
		t.Fatal("no deferred action")
	}
}

func TestPlanningSpecRootsRequireHostRoster(t *testing.T) {
	s, c, r, o := fixtureRichInputs(t)
	r.Roots = nil
	_, err := Plan(s, c, r, o)
	fixtureExpect(t, "missing roster", err, "missing_host_root")
}

// -------------------------------------------------------- group 4: the digest

func TestPlanningDigestComposition(t *testing.T) {
	s, c, r, o := fixturePlanInputs(t)
	base := fixturePlanned(t, s, c, r, o).Digest()
	differs := func(name string, f fixturePlanMutation) {
		t.Helper()
		s, c, r, o := fixturePlanInputs(t)
		f(&s, &c, &r, &o)
		p, err := Plan(s, c, r, o)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			return
		}
		if p.Digest() == base {
			t.Errorf("%s: digest unchanged", name)
		}
	}
	differs("spec", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) { s.Home.Layout = LightHome })
	differs("render roots", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
		c.Roots[layout.RootHome] = s.Home.Root.Path
	})
	differs("resources", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
		r.Grants = append(r.Grants, EffectGrant{Kind: TrustEffect, RootID: "home", AuthorizationID: "a", Version: "1"})
	})
	differs("lock identity", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
		for i := range o.Roots {
			o.Roots[i].CanonicalBase = "/physical/fixture"
			o.Roots[i].CanonicalPath = strings.Replace(o.Roots[i].DeclaredPath, "/fixture", "/physical/fixture", 1)
		}
	})

	// A spec-only change must be visible to the operation reuse guard.
	s2, c2, r2, o2 := fixturePlanInputs(t)
	p := fixturePlanned(t, s2, c2, r2, o2)
	s2.Home.Layout = LightHome
	o2.Receipts = []Receipt{{SchemaVersion: SchemaVersion, OperationID: s2.OperationID, IdentityKey: s2.Identity.EncodedKey, InputDigest: p.Digest()}}
	_, err := Plan(s2, c2, r2, o2)
	fixtureExpect(t, "reuse guard sees a spec change", err, "operation_id_reused")

	// Grant order and entry order are canonicalized.
	s3, c3, r3, o3 := fixturePlanInputs(t)
	d1 := fixturePlanned(t, s3, c3, r3, o3).Digest()
	slices.Reverse(r3.Grants)
	if d2 := fixturePlanned(t, s3, c3, r3, o3).Digest(); d1 != d2 {
		t.Error("grant order leaked into the digest")
	}
	s4, c4, r4, o4 := fixturePlanInputs(t)
	entry := fixtureTree("b.txt").Entries[0]
	entry.Ownership.EntryID = "second"
	c4.Rendered[0].Tree.Entries = append(c4.Rendered[0].Tree.Entries, entry)
	d1 = fixturePlanned(t, s4, c4, r4, o4).Digest()
	slices.Reverse(c4.Rendered[0].Tree.Entries)
	if d2 := fixturePlanned(t, s4, c4, r4, o4).Digest(); d1 != d2 {
		t.Error("entry order leaked into the digest")
	}
}

// ------------------------------------------------ group 5: lock set and order

func TestPlanningLockCoverageAndCanonicalization(t *testing.T) {
	keys := func(p PlannedWorkspace) []string {
		var out []string
		for _, k := range p.LockKeys() {
			out = append(out, k.CanonicalID)
		}
		return out
	}
	s, c, r, o := fixturePlanInputs(t)
	base := keys(fixturePlanned(t, s, c, r, o))
	add := func(r *Resources, o *Observations, ref RootRef) {
		r.Roots = append(r.Roots, ref)
		o.Roots = append(o.Roots, RootObservation{RootID: ref.ID, DeclaredPath: ref.Path, CanonicalPath: ref.Path, CanonicalBase: ref.AllowedBase, Owner: ref.Owner})
	}

	// A non-artifact effect on a separate root joins the lock set.
	extra := fixtureRoot("extra")
	add(&r, &o, extra)
	s.Effects = append(s.Effects, EffectGrant{Kind: TrustEffect, RootID: extra.ID, AuthorizationID: "a", Version: "1"})
	r.Grants = slices.Clone(s.Effects)
	if got, want := keys(fixturePlanned(t, s, c, r, o)), slices.Sorted(slices.Values(append(slices.Clone(base), extra.Path))); !slices.Equal(got, want) {
		t.Errorf("trust root lock = %v want %v", got, want)
	}

	// An effect root inside the home root with the same owner is covered by it.
	s, c, r, o = fixturePlanInputs(t)
	inner := fixtureRoot("inhome")
	inner.Path = s.Home.Root.Path + "/in"
	add(&r, &o, inner)
	s.Effects = append(s.Effects, EffectGrant{Kind: TrustEffect, RootID: inner.ID, AuthorizationID: "a", Version: "1"})
	r.Grants = slices.Clone(s.Effects)
	if got := keys(fixturePlanned(t, s, c, r, o)); !slices.Equal(got, base) {
		t.Errorf("covered trust root changed the lock set: %v vs %v", got, base)
	}

	// Directory and artifact grants never add locks of their own.
	s, c, r, o = fixturePlanInputs(t)
	other := fixtureRoot("extra2")
	add(&r, &o, other)
	for _, k := range []EffectKind{DirectoryEffect, ArtifactEffect} {
		s.Effects = append(s.Effects, EffectGrant{Kind: k, RootID: other.ID, AuthorizationID: "fixture-authority", Version: "1"})
	}
	r.Grants = slices.Clone(s.Effects)
	if got := keys(fixturePlanned(t, s, c, r, o)); !slices.Equal(got, base) {
		t.Errorf("directory or artifact grant added a lock: %v vs %v", got, base)
	}

	// A rendered credential effect is reported against the target root.
	s, c, r, o = fixturePlanInputs(t)
	c.Rendered[0].Effects = []layout.Row{{Form: layout.Link, CredentialPolicy: layout.LinkOnlyNeverWrite, Path: "auth.json", Field: layout.Credentials}}
	found := false
	for _, d := range fixturePlanned(t, s, c, r, o).Diagnostics() {
		if d.Code == "credential_effect_pending" && d.Concern == string(layout.Credentials) && d.RootID == s.Boot.Candidate.ID {
			found = true
		}
	}
	if !found {
		t.Error("rendered credential effect diagnostic missing")
	}
}

func TestPlanningLockKeys(t *testing.T) {
	a, b := fixtureRoot("z"), fixtureRoot("a")
	ns := filepath.Join(a.AllowedBase, "locks")
	observe := func() []RootObservation {
		return []RootObservation{
			{RootID: a.ID, DeclaredPath: a.Path, CanonicalPath: a.Path, CanonicalBase: a.AllowedBase, Owner: a.Owner},
			{RootID: b.ID, DeclaredPath: b.Path, CanonicalPath: b.Path, CanonicalBase: b.AllowedBase, Owner: b.Owner},
			{RootID: "locks", DeclaredPath: ns, CanonicalPath: ns, CanonicalBase: a.AllowedBase, Owner: a.Owner, Exists: true, Directory: true},
		}
	}
	_, err := OrderedLockKeys("relative/ns", []RootRef{a}, observe())
	fixtureExpect(t, "relative namespace", err, "unsafe_lock_namespace")
	dup := append(observe(), observe()[0])
	_, err = OrderedLockKeys(ns, []RootRef{a}, dup)
	fixtureExpect(t, "duplicate observation", err, "duplicate_root_observation")
	bad := a
	bad.Provenance = ""
	_, err = OrderedLockKeys(ns, []RootRef{bad}, observe())
	fixtureExpect(t, "invalid root", err, "missing_root_ownership")
	for name, mutate := range map[string]func(*RootObservation){
		"unclean canonical path":  func(o *RootObservation) { o.CanonicalPath += "/" },
		"relative canonical base": func(o *RootObservation) { o.CanonicalBase = "fixture" },
		"unclean canonical base":  func(o *RootObservation) { o.CanonicalBase = "/fixture/" },
		"path outside its base":   func(o *RootObservation) { o.CanonicalBase = "/fixture-other" },
		"uncertain":               func(o *RootObservation) { o.Uncertainty = "fixture uncertainty" },
	} {
		o := observe()
		mutate(&o[0])
		_, err = OrderedLockKeys(ns, []RootRef{a}, o)
		fixtureExpect(t, name, err, "unknown_canonical_root")
	}
	o := observe()
	o[0].Owner = "fixture-other-owner"
	_, err = OrderedLockKeys(ns, []RootRef{a}, o)
	fixtureExpect(t, "owner", err, "root_owner_mismatch")

	_, err = OrderedLockKeys(filepath.Join(a.Path, "locks"), []RootRef{a}, append(observe()[:2:2], RootObservation{RootID: "locks", DeclaredPath: filepath.Join(a.Path, "locks"), CanonicalPath: filepath.Join(a.Path, "locks"), CanonicalBase: a.AllowedBase, Owner: a.Owner, Exists: true, Directory: true}))
	fixtureExpect(t, "namespace below a root", err, "lock_namespace_inside_root")
	_, err = OrderedLockKeys(a.AllowedBase, []RootRef{a}, append(observe()[:2:2], RootObservation{RootID: "locks", DeclaredPath: a.AllowedBase, CanonicalPath: a.AllowedBase, CanonicalBase: "/", Owner: a.Owner, Exists: true, Directory: true}))
	fixtureExpect(t, "namespace above a root", err, "lock_namespace_inside_root")

	inner := fixtureRoot("inner")
	inner.Path = filepath.Join(a.Path, "inner")
	nested := append(observe(), RootObservation{RootID: inner.ID, DeclaredPath: inner.Path, CanonicalPath: inner.Path, CanonicalBase: inner.AllowedBase, Owner: inner.Owner})
	_, err = OrderedLockKeys(ns, []RootRef{a, inner}, nested)
	fixtureExpect(t, "overlap, outer first", err, "overlapping_mutation_roots")
	_, err = OrderedLockKeys(ns, []RootRef{inner, a}, nested)
	fixtureExpect(t, "overlap, inner first", err, "overlapping_mutation_roots")

	o = observe()
	o[0].CanonicalPath = filepath.Join(a.AllowedBase, "canonical-z")
	keys, err := OrderedLockKeys(ns, []RootRef{a}, o)
	if err != nil || len(keys) != 1 || keys[0].CanonicalID != o[0].CanonicalPath {
		t.Errorf("key must use the canonical path: %v %v", keys, err)
	}
}

// --------------------------------------------------- group 6: spec validation

func TestPlanningSpecValidation(t *testing.T) {
	mod := func(name, code string, f func(*Spec)) {
		t.Helper()
		s := fixtureSpec(t)
		f(&s)
		fixtureExpect(t, name, s.Validate(), code)
	}
	mod("operation id", "missing_operation_id", func(s *Spec) { s.OperationID = "" })
	mod("agent identity", "missing_identity_pin", func(s *Spec) { s.Identity.AgentURN = "" })
	mod("definition revision", "missing_identity_pin", func(s *Spec) { s.Identity.DefinitionRevision = "" })
	mod("session", "missing_identity_pin", func(s *Spec) { s.Identity.Session = "" })
	mod("fence id", "missing_identity_pin", func(s *Spec) { s.Identity.Fence.ID = "" })
	mod("fence revision", "missing_identity_pin", func(s *Spec) { s.Identity.Fence.Revision = "" })
	mod("digest empty", "missing_input_digest", func(s *Spec) { s.Identity.SemanticDigest = artifact.Digest{} })
	mod("digest algorithm", "unsupported_input_digest", func(s *Spec) { s.Identity.SemanticDigest.Algorithm = "fixture-md5" })
	mod("digest upper case", "unsupported_input_digest", func(s *Spec) { s.Identity.SemanticDigest.Hex = strings.ToUpper(s.Identity.SemanticDigest.Hex) })
	mod("digest too short", "unsupported_input_digest", func(s *Spec) { s.Identity.SemanticDigest.Hex = "00112233445566778899aabbccddeeff" })
	mod("digest odd length", "unsupported_input_digest", func(s *Spec) { s.Identity.SemanticDigest.Hex = strings.Repeat("0", 65) })
	mod("digest not hex", "unsupported_input_digest", func(s *Spec) { s.Identity.SemanticDigest.Hex = strings.Repeat("z", 64) })
	mod("artifact digest", "unsupported_input_digest", func(s *Spec) { s.Identity.ArtifactDigest.Hex = "bad" })
	mod("dependency digest", "unsupported_input_digest", func(s *Spec) { s.Identity.DependencyDigest.Hex = "bad" })
	mod("ephemeral continuity accepted", "", func(s *Spec) { s.Home.Continuity = Ephemeral })
	mod("light layout accepted", "", func(s *Spec) { s.Home.Layout = LightHome })
	mod("layout unknown", "unsupported_home_layout", func(s *Spec) { s.Home.Layout = "fixture-future" })
	mod("home retention", "unsupported_retention", func(s *Spec) { s.Home.Retention = "fixture-future" })
	mod("boot retention", "unsupported_retention", func(s *Spec) { s.Boot.Retention = "fixture-future" })
	mod("cleanup retention", "unsupported_retention", func(s *Spec) { s.Cleanup.Retention = "fixture-future" })
	mod("retire-when-unused accepted", "", func(s *Spec) { s.Cleanup.Retention = RetireWhenUnused })
	mod("identity root invalid", "missing_root_ownership", func(s *Spec) { s.Boot.IdentityRoot.Provenance = "" })
	mod("current invalid", "missing_root_ownership", func(s *Spec) { s.Boot.Current.Provenance = "" })
	mod("candidate invalid", "missing_root_ownership", func(s *Spec) { s.Boot.Candidate.Provenance = "" })
	mod("current is not a child", "invalid_boot_siblings", func(s *Spec) { s.Boot.Current.Path = "/fixture/elsewhere/current" })
	mod("candidate is not a child", "invalid_boot_siblings", func(s *Spec) { s.Boot.Candidate.Path = "/fixture/candidate-elsewhere" })
	mod("candidate owner differs", "invalid_boot_siblings", func(s *Spec) { s.Boot.Candidate.Owner = "fixture-other-owner" })
	mod("current owner differs", "invalid_boot_siblings", func(s *Spec) { s.Boot.Current.Owner = "fixture-other-owner" })
	mod("required confinement accepted", "", func(s *Spec) { s.Sandbox.Policy.Mode = sandbox.ConfinementRequired })
	mod("cwd root", "missing_cwd_root", func(s *Spec) { s.CWD.RootID = "" })
	mod("cwd dot accepted", "", func(s *Spec) { s.CWD.Relative = "." })

	scratch := func(f func(*ScratchSpec)) func(*Spec) {
		return func(s *Spec) {
			sc := ScratchSpec{Root: fixtureRoot("sc"), Session: s.Identity.Session, Retention: Keep}
			f(&sc)
			s.Scratch = []ScratchSpec{sc}
		}
	}
	mod("scratch session", "invalid_scratch", scratch(func(sc *ScratchSpec) { sc.Session = "fixture-other" }))
	mod("scratch quota", "invalid_scratch", scratch(func(sc *ScratchSpec) { sc.QuotaBytes = -1 }))
	mod("scratch zero quota accepted", "", scratch(func(sc *ScratchSpec) {}))
	mod("scratch retention", "invalid_scratch", scratch(func(sc *ScratchSpec) { sc.Retention = "fixture-future" }))
	mod("scratch root", "missing_root_ownership", scratch(func(sc *ScratchSpec) { sc.Root.Owner = "" }))

	repo := func(f func(*RepoSpec)) func(*Spec) {
		return func(s *Spec) {
			rp := RepoSpec{ID: "r", Source: ResourceRef{ID: "s"}, DesiredRoot: fixtureRoot("repo"), Mode: Worktree, Retention: Keep}
			f(&rp)
			s.Repos = []RepoSpec{rp}
		}
	}
	for _, m := range []RepoMode{Worktree, Checkout, Readonly} {
		mod("repository mode accepted: "+string(m), "", repo(func(rp *RepoSpec) { rp.Mode = m }))
	}
	mod("repository mode unknown", "unsupported_repository_mode", repo(func(rp *RepoSpec) { rp.Mode = "fixture-future" }))
	mod("repository root", "missing_root_ownership", repo(func(rp *RepoSpec) { rp.DesiredRoot.Owner = "" }))
	mod("repository retention", "invalid_repository", repo(func(rp *RepoSpec) { rp.Retention = "fixture-future" }))
	mod("repository id", "invalid_repository", repo(func(rp *RepoSpec) { rp.ID = "" }))
	mod("repository source", "invalid_repository", repo(func(rp *RepoSpec) { rp.Source.ID = "" }))

	credential := func(f func(*CredentialSpec)) func(*Spec) {
		return func(s *Spec) {
			cr := CredentialSpec{Source: ResourceRef{ID: "s"}, Authorization: ResourceRef{ID: "a"}, DestinationRootID: "d", Destination: "x/y", Concern: "c"}
			f(&cr)
			s.Credentials = []CredentialSpec{cr}
		}
	}
	mod("credential accepted", "", credential(func(cr *CredentialSpec) {}))
	mod("credential source", "invalid_credential_reference", credential(func(cr *CredentialSpec) { cr.Source.ID = "" }))
	mod("credential authorization", "invalid_credential_reference", credential(func(cr *CredentialSpec) { cr.Authorization.ID = "" }))
	mod("credential destination root", "invalid_credential_reference", credential(func(cr *CredentialSpec) { cr.DestinationRootID = "" }))
	mod("credential concern", "invalid_credential_reference", credential(func(cr *CredentialSpec) { cr.Concern = "" }))
	mod("credential destination", "invalid_credential_reference", credential(func(cr *CredentialSpec) { cr.Destination = "../x" }))
	mod("credential access", "unsupported_access", credential(func(cr *CredentialSpec) { cr.Access = []sandbox.AccessKind{"fixture-future"} }))

	mod("extra directory access", "unsupported_access", func(s *Spec) {
		s.ExtraDirs = []AccessRef{{Resource: ResourceRef{ID: "x", Path: "/fixture/x", Provenance: "fixture"}, Access: []sandbox.AccessKind{"fixture-future"}}}
	})
	for _, k := range []sandbox.AccessKind{sandbox.AccessRead, sandbox.AccessWrite, sandbox.AccessDeny, sandbox.AccessSourceRead, sandbox.AccessRuntimeRead, sandbox.AccessProtect} {
		mod("access accepted: "+string(k), "", func(s *Spec) {
			s.ExtraDirs = []AccessRef{{Resource: ResourceRef{ID: "x", Path: "/fixture/x", Provenance: "fixture"}, Access: []sandbox.AccessKind{k}}}
		})
	}

	grant := func(f func(*EffectGrant)) func(*Spec) {
		return func(s *Spec) {
			g := EffectGrant{Kind: DirectoryEffect, RootID: "r", AuthorizationID: "a", Version: "1"}
			f(&g)
			s.Effects = []EffectGrant{g}
		}
	}
	mod("grant root", "invalid_effect_grant", grant(func(g *EffectGrant) { g.RootID = "" }))
	mod("grant authorization", "invalid_effect_grant", grant(func(g *EffectGrant) { g.AuthorizationID = "" }))
	mod("grant version", "invalid_effect_grant", grant(func(g *EffectGrant) { g.Version = "" }))
	mod("grant kind unknown", "invalid_effect_grant", grant(func(g *EffectGrant) { g.Kind = "fixture-future" }))
	for _, k := range []EffectKind{DirectoryEffect, ArtifactEffect, CredentialLinkEffect, TrustEffect, RepositoryEffect} {
		mod("grant kind accepted: "+string(k), "", grant(func(g *EffectGrant) { g.Kind = k }))
	}
	for _, cp := range []Capability{CanonicalRoots, MutationLocks, UseReservation, CredentialLinks, TrustHandling, RepositoryAttachments, SandboxConfinement, BootPublication, InstalledMerge} {
		mod("capability accepted: "+string(cp), "", func(s *Spec) { s.Sandbox.RequiredCapabilities = []Capability{cp} })
	}
	mod("sandbox capability unknown", "unsupported_capability", func(s *Spec) { s.Sandbox.RequiredCapabilities = []Capability{"fixture-future"} })
	mod("cleanup proof unknown", "unsupported_capability", func(s *Spec) { s.Cleanup.RequiredProofs = []Capability{"fixture-future"} })
	mod("reconcile policy empty accepted", "", func(s *Spec) { s.Boot.Reconcile.Conflict = "" })
	mod("reconcile report accepted", "", func(s *Spec) { s.Boot.Reconcile.Conflict = materialize.ConflictReport })

	// Root references.
	ref := func(f func(*RootRef)) error {
		r := fixtureRoot("x")
		f(&r)
		return r.Validate()
	}
	fixtureExpect(t, "root id", ref(func(r *RootRef) { r.ID = "" }), "missing_root_ownership")
	fixtureExpect(t, "root owner", ref(func(r *RootRef) { r.Owner = "" }), "missing_root_ownership")
	fixtureExpect(t, "root provenance", ref(func(r *RootRef) { r.Provenance = "" }), "missing_root_ownership")
	fixtureExpect(t, "root relative path", ref(func(r *RootRef) { r.Path = "fixture/x" }), "unsafe_root")
	fixtureExpect(t, "root relative base", ref(func(r *RootRef) { r.AllowedBase = "fixture" }), "unsafe_root")
	fixtureExpect(t, "root unclean path", ref(func(r *RootRef) { r.Path = "/fixture/x/" }), "unsafe_root")
	fixtureExpect(t, "root unclean base", ref(func(r *RootRef) { r.AllowedBase = "/fixture/" }), "unsafe_root")
	fixtureExpect(t, "root NUL", ref(func(r *RootRef) { r.Path = "/fixture/x\x00y" }), "unsafe_root")
	fixtureExpect(t, "root CR", ref(func(r *RootRef) { r.Path = "/fixture/x\ry" }), "unsafe_root")
	fixtureExpect(t, "root LF", ref(func(r *RootRef) { r.Path = "/fixture/x\ny" }), "unsafe_root")
	fixtureExpect(t, "root above its base", ref(func(r *RootRef) { r.AllowedBase = "/fixture/b"; r.Path = "/fixture" }), "unsafe_root")
	fixtureExpect(t, "filesystem root", ref(func(r *RootRef) { r.AllowedBase = "/"; r.Path = "/" }), "protected_filesystem_root")
	fixtureExpect(t, "root equal to its base", ref(func(r *RootRef) { r.AllowedBase = "/fixture/x" }), "unsafe_allowed_base")
}

// ----------------------------------------- group 7: managed tree and manifest

func TestPlanningManagedTreeAndManifest(t *testing.T) {
	fixtureExpect(t, "colon in path", ValidateManagedTree(fixtureTree("a:b"), nil), "unsafe_artifact_path")
	fixtureExpect(t, "control character in path", ValidateManagedTree(fixtureTree("a\x01b"), nil), "unsafe_artifact_path")
	for name, mutate := range map[string]func(*artifact.Entry){
		"entry id": func(e *artifact.Entry) { e.Ownership.EntryID = "" },
		"group id": func(e *artifact.Entry) { e.Ownership.GroupID = "" },
		"source":   func(e *artifact.Entry) { e.Provenance.Source = "" },
	} {
		tr := fixtureTree("AGENTS.md")
		mutate(&tr.Entries[0])
		fixtureExpect(t, name, ValidateManagedTree(tr, nil), "missing_artifact_ownership")
	}
	dup := fixtureTree("AGENTS.md")
	dup.Entries = append(dup.Entries, dup.Entries[0])
	fixtureExpect(t, "duplicate entries", ValidateManagedTree(dup, nil), "invalid_artifact_tree")

	directory := artifact.Tree{Entries: []artifact.Entry{{Path: "private", Kind: artifact.EntryDirectory, Mode: 0755, Ownership: artifact.Ownership{EntryID: "e", GroupID: "g"}, Provenance: artifact.Provenance{Source: "s"}}}}
	fixtureExpect(t, "directory above a destination accepted", ValidateManagedTree(directory, []string{"private/token"}), "")
	fixtureExpect(t, "entry below a destination", ValidateManagedTree(fixtureTree("private/token/child"), []string{"private/token"}), "credential_artifact_collision")
	fixtureExpect(t, "entry case variant", ValidateManagedTree(fixtureTree("PRIVATE/TOKEN"), []string{"private/token"}), "credential_artifact_collision")
	fixtureExpect(t, "destination case variant", ValidateManagedTree(fixtureTree("private/token"), []string{"PRIVATE/Token"}), "credential_artifact_collision")
	fixtureExpect(t, "file above a destination", ValidateManagedTree(fixtureTree("private"), []string{"private/token"}), "credential_artifact_collision")

	fixtureExpect(t, ".materialize child", ValidateManagedTree(fixtureTree(".materialize/x"), nil), "reserved_artifact_path")
	reservedDirectory := artifact.Tree{Entries: []artifact.Entry{{Path: ".materialize", Kind: artifact.EntryDirectory, Mode: 0755, Ownership: artifact.Ownership{EntryID: "e", GroupID: "g"}, Provenance: artifact.Provenance{Source: "s"}}}}
	fixtureExpect(t, ".materialize itself", ValidateManagedTree(reservedDirectory, nil), "reserved_artifact_path")

	manifest := func(path string, kind artifact.EntryKind, dests ...string) error {
		return ValidateManagedManifest(materialize.Manifest{Entries: []materialize.ManifestEntry{{Path: path, Kind: kind}}}, dests)
	}
	fixtureExpect(t, "manifest path escape", manifest("../x", artifact.EntryFile), "credential_owned_invalid")
	fixtureExpect(t, "manifest declared destination", manifest("private/token", artifact.EntryFile, "private/token"), "credential_owned_invalid")
	fixtureExpect(t, "manifest control file", manifest(".materialize/manifest.json", artifact.EntryFile), "credential_owned_invalid")
	fixtureExpect(t, "manifest control directory", manifest(".materialize", artifact.EntryDirectory), "credential_owned_invalid")
	fixtureExpect(t, "manifest ordinary entry accepted", manifest("AGENTS.md", artifact.EntryFile), "")
}

// ---------------------------------------- group 8: input and output isolation

func TestPlanningAliasing(t *testing.T) {
	s, c, r, o := fixtureRichInputs(t)
	p := fixturePlanned(t, s, c, r, o)
	snap := func() string {
		b, err := json.Marshal([]any{p.Digest(), p.Actions(), p.LockKeys(), p.Diagnostics(), p.Access(), p.Bindings(), p.Roots(), p.RenderDiagnostics()})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	before := snap()

	s.Effects[0].Version = "changed"
	s.ExtraDirs[0].Access[0] = sandbox.AccessDeny
	s.Boot.Selection.Groups[0] = "changed"
	s.Scratch[0].Environment["A"] = "changed"
	r.Roots[0].Owner = "changed"
	r.Grants[0].Version = "changed"
	o.Roots[0].Owner = "changed"
	for i := range o.Roots {
		if o.Roots[i].Manifest != nil {
			o.Roots[i].Manifest.Generation = "changed"
			o.Roots[i].Manifest.Entries[0].Path = "changed"
		}
	}
	c.Rendered[0].Binding.Posture.Mapper = "changed"
	c.Rendered[0].Binding.Environment["CODEX_HOME"] = "changed"
	c.Roots[layout.RootBoot] = "/changed"
	if snap() != before {
		t.Fatal("caller mutation reached the plan")
	}

	for name, mutate := range map[string]func(){
		"action selection": func() {
			a := p.Actions()
			for i := range a {
				a[i].Request.Selection.Groups = append(a[i].Request.Selection.Groups, "changed")
				if len(a[i].Request.Selection.Groups) > 0 {
					a[i].Request.Selection.Groups[0] = "changed"
				}
			}
		},
		"action manifest": func() {
			a := p.Actions()
			for i := range a {
				if a[i].Request.CurrentManifest != nil {
					a[i].Request.CurrentManifest.Entries[0].Path = "changed"
				}
			}
		},
		"action capabilities": func() {
			a := p.Actions()
			for i := range a {
				if len(a[i].RequiredCapabilities) > 0 {
					a[i].RequiredCapabilities[0] = "changed"
				}
			}
		},
		"action entry bytes": func() {
			a := p.Actions()
			for i := range a {
				for j := range a[i].Request.Artifacts.Entries {
					if len(a[i].Request.Artifacts.Entries[j].Bytes) > 0 {
						a[i].Request.Artifacts.Entries[j].Bytes[0] = 'Z'
					}
				}
			}
		},
		"bindings": func() {
			b := p.Bindings()
			b[0].Posture.Mapper = "changed"
			b[0].Environment["CODEX_HOME"] = "changed"
			b[0].Argv[0] = "changed"
		},
		"access": func() {
			ac := p.Access()
			for i := range ac {
				if len(ac[i].Access) > 0 {
					ac[i].Access[0] = "changed"
				}
			}
		},
		"lock keys":          func() { p.LockKeys()[0].CanonicalID = "changed" },
		"roots":              func() { p.Roots()[0].ID = "changed" },
		"diagnostics":        func() { p.Diagnostics()[0].Code = "changed" },
		"render diagnostics": func() { p.RenderDiagnostics()[0].Code = "changed" },
	} {
		mutate()
		if snap() != before {
			t.Fatalf("%s: accessor result aliases plan state", name)
		}
	}
}

// ------------------------------------------ group 9: frozen state, white-box

func TestPlanningFrozenStateIsIndependentOfCallerInputs(t *testing.T) {
	s, c, r, o := fixtureRichInputs(t)
	p := fixturePlanned(t, s, c, r, o)
	rs, rc, rr, ro := fixtureRichInputs(t)
	ref := fixturePlanned(t, rs, rc, rr, ro)

	s.Effects[0].Version = "changed"
	s.Boot.Selection.Groups[0] = "changed"
	s.Scratch[0].Environment["A"] = "changed"
	s.ExtraDirs[0].Access[0] = sandbox.AccessDeny
	r.Grants[0].Version = "changed"
	r.Roots[0].Owner = "changed"
	r.Capabilities[0] = "changed"
	o.Roots[0].CanonicalPath = "/changed"
	o.Capabilities[0] = "changed"
	for i := range o.Roots {
		if o.Roots[i].Manifest != nil {
			o.Roots[i].Manifest.Generation = "changed"
			o.Roots[i].Manifest.Entries[0].Path = "changed"
		}
	}
	c.Roots[layout.RootBoot] = "/changed"
	c.Rendered[0].Binding.Environment["CODEX_HOME"] = "changed"
	c.Rendered[0].Binding.Argv[0] = "changed"
	c.Rendered[0].Tree.Entries[0].Bytes[0] = 'Q'

	if !reflect.DeepEqual(p.spec, ref.spec) {
		t.Error("frozen spec changed with the caller's value")
	}
	if !reflect.DeepEqual(p.resources, ref.resources) {
		t.Error("frozen resources changed with the caller's value")
	}
	if !reflect.DeepEqual(p.observed, ref.observed) {
		t.Error("frozen observations changed with the caller's value")
	}
	if !reflect.DeepEqual(p.renderRoots, ref.renderRoots) {
		t.Error("frozen render roots changed with the caller's value")
	}
	if !reflect.DeepEqual(p.content, ref.content) {
		t.Error("frozen render content changed with the caller's value")
	}
}

func TestPlanningInternalStatesAreNotShared(t *testing.T) {
	s, c, r, o := fixtureRichInputs(t)
	p := fixturePlanned(t, s, c, r, o)

	var action *Action
	for i := range p.actions {
		if p.actions[i].Kind == TreeAction {
			action = &p.actions[i]
		}
	}
	var observed RootObservation
	for _, ob := range p.observed.Roots {
		if ob.RootID == s.Boot.Candidate.ID {
			observed = ob
		}
	}
	if action == nil || action.Request.CurrentManifest == nil || observed.Manifest == nil {
		t.Fatal("reconcile fixture is incomplete")
	}
	if action.Request.CurrentManifest == observed.Manifest {
		t.Fatal("request manifest is the observed manifest")
	}
	action.Request.CurrentManifest.Generation = "changed"
	if observed.Manifest.Generation != "gen" {
		t.Error("request manifest aliases the observation")
	}

	if len(p.bindings) != 1 || len(p.content) != 1 {
		t.Fatalf("bindings=%d content=%d", len(p.bindings), len(p.content))
	}
	p.bindings[0].Environment["CODEX_HOME"] = "changed"
	p.bindings[0].Argv[0] = "changed"
	if p.content[0].Binding.Environment["CODEX_HOME"] == "changed" || p.content[0].Binding.Argv[0] == "changed" {
		t.Error("binding copy aliases the frozen render snapshot")
	}
}

func TestPlanningValidityFlag(t *testing.T) {
	var zero PlannedWorkspace
	if zero.valid {
		t.Fatal("zero value is marked valid")
	}
	s, c, r, o := fixturePlanInputs(t)
	if !fixturePlanned(t, s, c, r, o).valid {
		t.Fatal("successful plan is not marked valid")
	}
}

func TestPlanningArtifactsCompleteNeedsEveryTerm(t *testing.T) {
	complete := func() ApplyResult {
		r := ApplyResult{Status: Partial, Receipt: Receipt{Phase: ArtifactsCommitted}, artifactsComplete: true}
		r.artifactSeal, _ = resultSeal(r)
		return r
	}
	if !complete().ArtifactsComplete() {
		t.Fatal("earned completion not reported")
	}
	for name, mutate := range map[string]func(*ApplyResult){
		"no private proof":   func(r *ApplyResult) { r.artifactsComplete = false },
		"ready status":       func(r *ApplyResult) { r.Status = Ready },
		"conflict status":    func(r *ApplyResult) { r.Status = Conflict },
		"unsupported status": func(r *ApplyResult) { r.Status = Unsupported },
		"empty status":       func(r *ApplyResult) { r.Status = "" },
		"planned phase":      func(r *ApplyResult) { r.Receipt.Phase = Planned },
		"interrupted phase":  func(r *ApplyResult) { r.Receipt.Phase = Interrupted },
		"empty phase":        func(r *ApplyResult) { r.Receipt.Phase = "" },
	} {
		r := complete()
		mutate(&r)
		if r.ArtifactsComplete() {
			t.Errorf("%s: completion reported", name)
		}
	}
}

// ---------------------------------------------------- group 10: other checks

func TestPlanningAdditionalRefusals(t *testing.T) {
	fixtureRun(t, []fixtureCase{
		{"effect root inside home with another owner", "overlapping_mutation_roots", func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			in := fixtureRoot("inhome")
			in.Path = s.Home.Root.Path + "/in"
			in.Owner = "fixture-other-owner"
			r.Roots = append(r.Roots, in)
			o.Roots = append(o.Roots, RootObservation{RootID: in.ID, DeclaredPath: in.Path, CanonicalPath: in.Path, CanonicalBase: in.AllowedBase, Owner: in.Owner})
			s.Effects = append(s.Effects, EffectGrant{Kind: TrustEffect, RootID: in.ID, AuthorizationID: "a", Version: "1"})
			r.Grants = slices.Clone(s.Effects)
		}},
	})

	sp := fixtureSpec(t)
	sp.Effects = []EffectGrant{{Kind: "fixture-future", RootID: "r", AuthorizationID: "a", Version: "1"}}
	fixtureExpect(t, "unknown effect kind", sp.Validate(), "invalid_effect_grant")

	// The lock helper rejects an unclean canonical base on its own.
	a := fixtureRoot("z")
	_, err := OrderedLockKeys(filepath.Join(a.AllowedBase, "locks"), []RootRef{a}, []RootObservation{{RootID: a.ID, DeclaredPath: a.Path, CanonicalPath: a.Path, CanonicalBase: "/fixture/", Owner: a.Owner}, {RootID: "locks", DeclaredPath: "/fixture/locks", CanonicalPath: "/fixture/locks", CanonicalBase: "/fixture", Owner: a.Owner, Exists: true, Directory: true}})
	fixtureExpect(t, "lock helper unclean base", err, "unknown_canonical_root")
}

// ------------------------------------------- group 11: general properties

func TestPlanningDeterminism(t *testing.T) {
	encode := func(p PlannedWorkspace) string {
		b, err := json.Marshal([]any{p.Digest(), p.Actions(), p.LockKeys(), p.Diagnostics(), p.Access(), p.Bindings(), p.Roots(), p.RenderDiagnostics()})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	s, c, r, o := fixtureRichInputs(t)
	first := encode(fixturePlanned(t, s, c, r, o))
	for i := 0; i < 200; i++ {
		s, c, r, o := fixtureRichInputs(t)
		if got := encode(fixturePlanned(t, s, c, r, o)); got != first {
			t.Fatalf("run %d produced a different plan", i)
		}
	}
}

func TestPlanningInputImmutability(t *testing.T) {
	s, c, r, o := fixtureRichInputs(t)
	s0, c0, r0, o0 := fixtureRichInputs(t)
	_ = fixturePlanned(t, s, c, r, o)
	if !reflect.DeepEqual(s, s0) || !reflect.DeepEqual(c, c0) || !reflect.DeepEqual(r, r0) || !reflect.DeepEqual(o, o0) {
		t.Fatal("Plan mutated one of its inputs")
	}
}

func TestPlanningZeroValues(t *testing.T) {
	var p PlannedWorkspace
	if p.Digest() != "" || len(p.Actions()) != 0 || len(p.Roots()) != 0 || len(p.LockKeys()) != 0 || len(p.Diagnostics()) != 0 ||
		len(p.RenderDiagnostics()) != 0 || len(p.Access()) != 0 || len(p.Bindings()) != 0 {
		t.Fatal("zero plan is not empty")
	}
	if _, err := Plan(Spec{}, ResolvedContent{}, Resources{}, Observations{}); err == nil {
		t.Fatal("zero inputs planned")
	}
	// Without rendered content only the directory preparation steps remain.
	s, c, r, o := fixturePlanInputs(t)
	c.Rendered, c.Roots = nil, nil
	if got := len(fixturePlanned(t, s, c, r, o).Actions()); got != 2 {
		t.Fatalf("actions without content = %d", got)
	}
}

func TestPlanningCanonicalAccessKeepsDeclaredSpec(t *testing.T) {
	s, c, r, o := fixtureRichInputs(t)
	ref := s.ExtraDirs[0]
	s.ExtraDirs = make([]AccessRef, 9)
	for i := range s.ExtraDirs {
		s.ExtraDirs[i] = ref
	}
	for i := range o.Roots {
		o.Roots[i].CanonicalBase = "/physical/fixture"
		o.Roots[i].CanonicalPath = strings.Replace(o.Roots[i].DeclaredPath, "/fixture", "/physical/fixture", 1)
	}
	p := fixturePlanned(t, s, c, r, o)
	if p.spec.ExtraDirs[0].Resource.Path != "/fixture/x" || p.Access()[0].Resource.Path != "/physical/fixture/x" {
		t.Fatal("canonical access rewrote declared semantic input")
	}
}

func TestPlanningAdditionalSpecFieldValidation(t *testing.T) {
	cases := []struct {
		name, code string
		change     func(*Spec)
	}{
		{"digest algorithm absent", "missing_input_digest", func(s *Spec) { s.Identity.SemanticDigest.Algorithm = "" }},
		{"digest hex absent", "missing_input_digest", func(s *Spec) { s.Identity.SemanticDigest.Hex = "" }},
		{"row id empty", "invalid_row_reference", func(s *Spec) { s.Boot.RowIDs = []string{""} }},
		{"row id control", "invalid_row_reference", func(s *Spec) { s.Boot.RowIDs = []string{"row\x1b"} }},
		{"cwd child relative", "unsafe_cwd", func(s *Spec) { s.CWD.Child = "relative" }},
		{"cwd protocol relative", "unsafe_cwd", func(s *Spec) { s.CWD.ProtocolProject = "relative" }},
		{"cleanup owned empty", "invalid_cleanup_reference", func(s *Spec) { s.Cleanup.OwnedRoots = []string{""} }},
		{"cleanup owned control", "invalid_cleanup_reference", func(s *Spec) { s.Cleanup.OwnedRoots = []string{"root\x1b"} }},
		{"cleanup id empty", "invalid_cleanup_reference", func(s *Spec) { s.Cleanup.ExpectedGenerations = map[string]string{"": "generation"} }},
		{"cleanup id control", "invalid_cleanup_reference", func(s *Spec) { s.Cleanup.ExpectedGenerations = map[string]string{"root\x1b": "generation"} }},
		{"cleanup generation empty", "invalid_cleanup_reference", func(s *Spec) { s.Cleanup.ExpectedGenerations = map[string]string{"home": ""} }},
		{"cleanup generation control", "invalid_cleanup_reference", func(s *Spec) { s.Cleanup.ExpectedGenerations = map[string]string{"home": "generation\x1b"} }},
		{"scratch assignment mismatch", "invalid_scratch", func(s *Spec) { s.Scratch[0].Assignment = "other" }},
		{"scratch environment name", "invalid_scratch_environment", func(s *Spec) { s.Scratch[0].Environment = map[string]string{"a": "value"} }},
		{"scratch credential environment", "invalid_scratch_environment", func(s *Spec) { s.Scratch[0].Environment = map[string]string{"SECRET": "value"} }},
		{"scratch environment value", "invalid_scratch_environment", func(s *Spec) { s.Scratch[0].Environment = map[string]string{"A": "value\x1b"} }},
		{"access path", "invalid_access_resource", func(s *Spec) { s.ExtraDirs[0].Resource.Path = "relative" }},
		{"access id", "invalid_access_resource", func(s *Spec) { s.ExtraDirs[0].Resource.ID = "" }},
		{"access provenance", "invalid_access_resource", func(s *Spec) { s.ExtraDirs[0].Resource.Provenance = "" }},
		{"credential writable source", "invalid_credential_access", func(s *Spec) { s.Credentials[0].Access = []sandbox.AccessKind{sandbox.AccessWrite} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, _ := fixtureRichInputs(t)
			tc.change(&s)
			fixtureExpect(t, tc.name, s.Validate(), tc.code)
		})
	}
	s, c, r, o := fixtureRichInputs(t)
	s.Boot.RowIDs = []string{"fixture-row"}
	s.CWD.Child = s.Home.Root.Path
	s.CWD.ProtocolProject = s.Boot.Current.Path
	s.Cleanup.OwnedRoots = []string{s.Home.Root.ID}
	s.Cleanup.ExpectedGenerations = map[string]string{s.Home.Root.ID: "generation"}
	_ = fixturePlanned(t, s, c, r, o)
}

func TestPlanningPathTextBoundaries(t *testing.T) {
	for _, r := range []rune{0, 9, 27, 31, 127, 0x202a, 0x202b, 0x202c, 0x202d, 0x202e, 0x2066, 0x2067, 0x2068, 0x2069} {
		ref := fixtureRoot("x")
		ref.Path += string(r)
		fixtureExpect(t, "unsafe path text", ref.Validate(), "unsafe_root")
		if safeValueText("value" + string(r)) {
			t.Fatalf("unsafe metadata rune %x accepted", r)
		}
	}
	for _, r := range []rune{32, 126, 128, 0x2029, 0x202f, 0x2065, 0x206a} {
		ref := fixtureRoot("x")
		ref.Path += string(r)
		fixtureExpect(t, "safe path boundary", ref.Validate(), "")
		if !safeValueText("value" + string(r)) {
			t.Fatalf("safe metadata rune %x rejected", r)
		}
	}
	if cleanAbsolute("/"+strings.Repeat("x", render.MaxPathBytes)) || cleanAbsolute("/fixture/"+strings.Repeat("x/", render.MaxTreeDepth)+"x") || cleanAbsolute("/fixture/"+string([]byte{255})) {
		t.Fatal("unbounded or invalid root text accepted")
	}
}

func TestPlanningDirectoryModeContract(t *testing.T) {
	for _, mode := range []fs.FileMode{0, 0755, 0644, 0777} {
		e := fixtureTree("directory").Entries[0]
		e.Kind = artifact.EntryDirectory
		e.Bytes = nil
		e.Mode = mode
		code := ""
		if mode != 0 && mode != 0755 {
			code = "unsafe_artifact_mode"
		}
		fixtureExpect(t, "directory mode", ValidateManagedTree(artifact.Tree{Entries: []artifact.Entry{e}}, nil), code)
	}
}

func TestPlanningCredentialEnvironmentNames(t *testing.T) {
	for _, name := range []string{"TOKEN", "SECRET", "PASSWORD", "API_KEY", "CREDENTIAL", "OPENAI_API_KEY"} {
		s, _, _, _ := fixtureRichInputs(t)
		s.Scratch[0].Environment = map[string]string{name: "fixture"}
		fixtureExpect(t, name, s.Validate(), "invalid_scratch_environment")
	}
}

func TestPlanningBindingGuards(t *testing.T) {
	for name, change := range map[string]fixturePlanMutation{
		"empty environment key and value": func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			c.Rendered[0].Binding.Environment = map[string]string{"": ""}
		},
		"unknown empty environment": func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			c.Rendered[0].Binding.Environment = map[string]string{"FIXTURE_ROOT": ""}
		},
		"unknown environment": func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			c.Rendered[0].Binding.Environment = map[string]string{"FIXTURE_ROOT": s.Boot.Candidate.Path}
		},
		"empty environment": func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			c.Rendered[0].Provider = runtimes.Codex
			c.Rendered[0].Binding.Argv = nil
			c.Rendered[0].Binding.Environment = map[string]string{"CODEX_HOME": ""}
		},
		"resume position": func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			c.Rendered[0].Binding.BeforeResume = true
		},
		"posture provider": func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			c.Rendered[0].Binding.Posture = &layout.PostureReference{Provider: runtimes.Codex, Mapper: "adapters/registry.Descriptor.PostureFor", Posture: "default"}
		},
		"posture mapper": func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			c.Rendered[0].Binding.Posture = &layout.PostureReference{Provider: runtimes.Claude, Mapper: "other", Posture: "default"}
		},
		"posture mode": func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			c.Rendered[0].Binding.Posture = &layout.PostureReference{Provider: runtimes.Claude, Mapper: "adapters/registry.Descriptor.PostureFor", Posture: "unknown"}
		},
		"empty root expansion": func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			delete(c.Roots, layout.RootProject)
			c.Rendered[0].Binding.Argv = []string{"--add-dir", ""}
		},
		"exclusive MCP on another provider": func(s *Spec, c *ResolvedContent, r *Resources, o *Observations) {
			c.Rendered[0].Provider = runtimes.Codex
			c.Rendered[0].Binding.Argv = []string{"--strict-mcp-config"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, c, r, o := fixturePlanInputs(t)
			change(&s, &c, &r, &o)
			_, err := Plan(s, c, r, o)
			fixtureExpect(t, name, err, "invalid_render_binding")
		})
	}
	for name, project := range map[string]map[string]string{
		"unknown empty parameter": {"other": ""},
		"agent without directory": {"agent": "fixture"},
		"directory without agent": {"directory": "/fixture/home"},
		"invalid agent":           {"directory": "/fixture/home", "agent": "../fixture"},
		"directory retarget":      {"directory": "/fixture/other", "agent": "fixture"},
		"unknown parameter":       {"directory": "/fixture/home", "agent": "fixture", "other": "fixture"},
	} {
		t.Run(name, func(t *testing.T) {
			s, c, r, o := fixturePlanInputs(t)
			c.Rendered[0].Provider = runtimes.OpenCode
			c.Rendered[0].Mode = runtimes.ModeHTTPSSE
			c.Rendered[0].Binding = render.Binding{CWD: s.Home.Root.Path, RPCProject: project}
			_, err := Plan(s, c, r, o)
			fixtureExpect(t, name, err, "invalid_render_binding")
		})
	}
	s, c, r, o := fixturePlanInputs(t)
	c.Rendered[0].Provider = runtimes.OpenCode
	c.Rendered[0].Binding = render.Binding{CWD: s.Home.Root.Path, Argv: []string{"run", "--agent", "fixture", "--dir", s.Home.Root.Path, "--agent", "fixture"}}
	_ = fixturePlanned(t, s, c, r, o)
}

func TestPlanningFrozenValueLimits(t *testing.T) {
	entry := fixtureTree("binary.dat").Entries[0]
	entry.Bytes = make([]byte, 65536)
	fixtureExpect(t, "bounded binary payload", ValidateManagedTree(artifact.Tree{Entries: []artifact.Entry{entry}}, nil), "")
	leaves := make([]string, MaxCollectionItems)
	for i := range leaves {
		leaves[i] = strings.Repeat("x", render.MaxPathBytes)
	}
	nested := [][]string{leaves, leaves, leaves, leaves, leaves}
	fixtureExpect(t, "aggregate textual bytes", validateFrozenValues(nested), "input_limit")
	leaves = make([]string, MaxCollectionItems)
	nested = make([][]string, 17)
	for i := range nested {
		nested[i] = leaves
	}
	fixtureExpect(t, "aggregate collection items", validateFrozenValues(nested), "input_limit")
	fixtureExpect(t, "oversized text", validateFrozenValues(strings.Repeat("x", MaxFrozenBytes+1)), "input_limit")
	fixtureExpect(t, "invalid map value", validateFrozenValues(map[string]string{"key": string([]byte{255})}), "invalid_utf8")
	fixtureExpect(t, "invalid pointer field", validateFrozenValues(&ResourceRef{Revision: string([]byte{255})}), "invalid_utf8")
	fixtureExpect(t, "oversized binary", validateFrozenValues(make([]byte, MaxFrozenBytes+1)), "input_limit")
	inner := map[string]string{}
	for i := 0; i < MaxCollectionItems; i++ {
		inner[fmt.Sprint(i)] = ""
	}
	outer := map[string]map[string]string{}
	for i := 0; i < 17; i++ {
		outer[fmt.Sprint(i)] = inner
	}
	fixtureExpect(t, "aggregate map items", validateFrozenValues(outer), "input_limit")
}

func TestPlanningPublicValidatorsCheckTextBeforeCopy(t *testing.T) {
	s := fixtureSpec(t)
	s.Identity.DefinitionRevision = string([]byte{255})
	fixtureExpect(t, "spec text", s.Validate(), "invalid_utf8")
	r := fixtureRoot("x")
	r.Provenance = string([]byte{255})
	fixtureExpect(t, "root text", r.Validate(), "invalid_utf8")
	tree := fixtureTree("file")
	tree.Entries[0].Provenance.Note = string([]byte{255})
	fixtureExpect(t, "tree metadata", ValidateManagedTree(tree, nil), "invalid_utf8")
}

func TestPlanningDirectoryMetadataAndNativePaths(t *testing.T) {
	e := fixtureTree("directory").Entries[0]
	e.Kind = artifact.EntryDirectory
	e.Bytes = nil
	e.Mode = 0755
	e.Digest = artifact.DigestBytes(nil)
	fixtureExpect(t, "directory digest has no byte meaning", ValidateManagedTree(artifact.Tree{Entries: []artifact.Entry{e}}, nil), "artifact_digest_mismatch")
	s, c, r, o := fixturePlanInputs(t)
	e.Path = ".claude/settings.json"
	e.Digest = artifact.Digest{}
	c.Rendered[0].Tree = artifact.Tree{Entries: []artifact.Entry{e}}
	_, err := Plan(s, c, r, o)
	fixtureExpect(t, "native document cannot be a directory", err, "unsafe_render_entry")
	keys, err := OrderedLockKeys(r.LockNamespace, nil, []RootObservation{*fixtureObservation(&o, r.LockRoot.ID)})
	if err != nil || keys == nil || len(keys) != 0 {
		t.Fatalf("empty lock set = %#v, %v", keys, err)
	}
}
