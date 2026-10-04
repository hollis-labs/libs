package workspace_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	layout "github.com/hollis-labs/substrate/harness/adapters/layout/plan"
	"github.com/hollis-labs/substrate/harness/sandbox"
	"github.com/hollis-labs/substrate/harness/workspace"
	"github.com/hollis-labs/substrate/harness/workspace/materialize"
)

func relocateObservations(o *workspace.Observations) {
	for i := range o.Roots {
		o.Roots[i].CanonicalBase = "/physical/fixture"
		o.Roots[i].CanonicalPath = strings.Replace(o.Roots[i].DeclaredPath, "/fixture", "/physical/fixture", 1)
	}
}

func TestCanonicalPlanContentAndGrants(t *testing.T) {
	s, c, r, o := planInputs(t)
	scratch, extra := root("scratch"), root("effect")
	s.Scratch = []workspace.ScratchSpec{{Root: scratch, Session: s.Identity.Session, Retention: workspace.Keep}}
	for _, ref := range []workspace.RootRef{scratch, extra} {
		r.Roots = append(r.Roots, ref)
		o.Roots = append(o.Roots, workspace.RootObservation{RootID: ref.ID, DeclaredPath: ref.Path, CanonicalPath: ref.Path, CanonicalBase: ref.AllowedBase, Owner: ref.Owner})
	}
	for _, g := range []workspace.EffectGrant{{Kind: workspace.DirectoryEffect, RootID: scratch.ID, AuthorizationID: "scratch-authority", Version: "2"}, {Kind: workspace.TrustEffect, RootID: extra.ID, AuthorizationID: "trust-authority", Version: "3"}} {
		s.Effects = append(s.Effects, g)
		r.Grants = append(r.Grants, g)
	}
	s.Credentials = []workspace.CredentialSpec{{Source: workspace.ResourceRef{ID: "credential-source", Path: "/fixture/source"}, Authorization: workspace.ResourceRef{ID: "link-authority"}, DestinationRootID: s.Boot.Candidate.ID, Destination: "private/token", Concern: "fixture"}}
	s.Boot.Selection = materialize.Selection{Groups: []string{"fixture-group"}, EntryIDs: []string{"fixture-entry"}}
	relocateObservations(&o)
	p := planned(t, s, c, r, o)
	physical := func(path string) string { return strings.Replace(path, "/fixture", "/physical/fixture", 1) }
	expected := []workspace.LockKey{}
	for _, path := range []string{s.Home.Root.Path, s.Boot.IdentityRoot.Path, scratch.Path, extra.Path} {
		expected = append(expected, workspace.LockKey{Namespace: "/physical/fixture/locks", CanonicalID: physical(path)})
	}
	slices.SortFunc(expected, func(a, b workspace.LockKey) int { return strings.Compare(a.CanonicalID, b.CanonicalID) })
	if !reflect.DeepEqual(p.LockKeys(), expected) {
		t.Fatalf("locks = %#v want %#v", p.LockKeys(), expected)
	}
	a := treeAction(t, p)
	if a.CanonicalPath != physical(s.Boot.Candidate.Path) || a.Request.TargetRoot != a.CanonicalPath || a.Request.Roots.BootRoot != a.CanonicalPath || a.Request.Roots.StateRoot != physical(s.Home.Root.Path) || a.Request.Operation != materialize.OperationCreate || a.Request.ExistingTarget != materialize.ExistingTargetRefuse || a.Request.CurrentManifest != nil || !reflect.DeepEqual(a.Request.Selection, s.Boot.Selection) || a.RootMode != 0700 {
		t.Fatalf("request = %#v", a)
	}
	if a.Grant != (workspace.EffectGrant{Kind: workspace.ArtifactEffect, RootID: s.Boot.Candidate.ID, AuthorizationID: "fixture-authority", Version: "1"}) {
		t.Fatalf("grant = %#v", a.Grant)
	}
	previous := ""
	for _, action := range p.Actions() {
		if action.CanonicalPath < previous {
			t.Fatal("actions not in canonical order")
		}
		previous = action.CanonicalPath
		if action.Kind == workspace.EnsureDirectoryAction && (action.RootMode != 0700 || action.Grant.Kind != workspace.DirectoryEffect || action.Grant.RootID != action.Root.ID) {
			t.Fatalf("directory = %#v", action)
		}
	}
	foundCurrent, foundCredential := false, false
	for _, access := range p.Access() {
		if access.Resource.ID == s.Boot.Current.ID {
			foundCurrent = true
			if !slices.Equal(access.Access, []sandbox.AccessKind{sandbox.AccessRead}) || access.Resource.Path != physical(s.Boot.Current.Path) {
				t.Fatalf("current access = %#v", access)
			}
		}
		if access.Resource.ID == "credential-source" {
			foundCredential = true
			if !slices.Equal(access.Access, []sandbox.AccessKind{sandbox.AccessSourceRead}) {
				t.Fatalf("credential access = %#v", access)
			}
		}
	}
	if !foundCurrent || !foundCredential || !p.Valid() {
		t.Fatal("accepted plan missing required content")
	}
	bindings := p.Bindings()
	bindings[0].Environment["fixture"] = "value"
	bindings[0].RPCProject["fixture"] = "value"
}

func TestPlanDigestEncodingGolden(t *testing.T) {
	s, c, r, o := planInputs(t)
	const expected = "855e7810a83830f9292cb1d240435d63c574e4469ab6362c8b8ddc004736ba1f"
	if got := planned(t, s, c, r, o).Digest(); got != expected {
		t.Fatalf("digest %s (encoding %s)", got, workspace.DigestVersion)
	}
}

func TestDigestIncludesDesiredInputs(t *testing.T) {
	s, c, r, o := planInputs(t)
	base := planned(t, s, c, r, o).Digest()
	cases := map[string]func(*workspace.Spec, *workspace.ResolvedContent, *workspace.Resources, *workspace.Observations){
		"operation": func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			s.OperationID = "other-operation"
		},
		"revision": func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			s.Identity.DefinitionRevision = "other"
		},
		"session": func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			s.Identity.Session = "other"
		},
		"selection": func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			s.Boot.Selection.Groups = []string{"other"}
		},
		"grant": func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			for i := range s.Effects {
				s.Effects[i].Version = "2"
			}
			r.Grants = slices.Clone(s.Effects)
		},
		"provider homes": func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			r.ProviderHomes = []workspace.ResourceRef{{ID: "provider-home", Revision: "2"}}
		},
		"capability": func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			r.Capabilities = append(r.Capabilities, workspace.TrustHandling)
		},
		"render roots": func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			c.Roots[layout.RootHome] = s.Home.Root.Path
		},
		"canonical paths": func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			relocateObservations(o)
		},
		"bytes": func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			c.Rendered[0].Tree.Entries[0].Bytes = []byte("other")
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s, c, r, o := planInputs(t)
			change(&s, &c, &r, &o)
			if planned(t, s, c, r, o).Digest() == base {
				t.Fatal("desired input missing from digest")
			}
		})
	}
}

func TestSetValuedInputsCanonicalized(t *testing.T) {
	s, c, r, o := planInputs(t)
	r.ProviderHomes = []workspace.ResourceRef{{ID: "a"}, {ID: "b"}}
	p := planned(t, s, c, r, o)
	slices.Reverse(r.ProviderHomes)
	r.ProviderHomes = append(r.ProviderHomes, r.ProviderHomes[0])
	slices.Reverse(s.Effects)
	slices.Reverse(r.Grants)
	r.Grants = append(r.Grants, r.Grants[0])
	r.Capabilities = append(r.Capabilities, r.Capabilities[0])
	if q := planned(t, s, c, r, o); q.Digest() != p.Digest() {
		t.Fatal("set ordering or repetition changed digest")
	}
}

func TestNamespaceAliasesUseOneCanonicalKey(t *testing.T) {
	s, c, r, o := planInputs(t)
	p := planned(t, s, c, r, o)
	r.LockRoot.AllowedBase = "/alias"
	r.LockRoot.Path = "/alias/locks"
	r.LockNamespace = r.LockRoot.Path
	rootObservation(&o, r.LockRoot.ID).DeclaredPath = r.LockRoot.Path
	q := planned(t, s, c, r, o)
	if !reflect.DeepEqual(p.LockKeys(), q.LockKeys()) {
		t.Fatal("namespace alias changed cooperating lock keys")
	}
}
