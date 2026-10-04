package workspace_test

import (
	"errors"
	"fmt"
	layout "github.com/hollis-labs/substrate/harness/adapters/layout/plan"
	"github.com/hollis-labs/substrate/harness/workspace"
	"github.com/hollis-labs/substrate/harness/workspace/materialize"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
	"github.com/hollis-labs/substrate/harness/workspace/render"
	"path/filepath"
	"strings"
	"testing"
)

func TestFrozenInputBoundsAndText(t *testing.T) {
	cases := []struct {
		name, code string
		change     func(*workspace.Spec, *workspace.ResolvedContent, *workspace.Resources, *workspace.Observations)
	}{
		{"invalid observation text", "invalid_utf8", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			o.FenceVersion = string([]byte{255})
		}},
		{"invalid map key", "invalid_utf8", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			c.Rendered[0].Binding.Environment = map[string]string{string([]byte{255}): "value"}
		}},
		{"total content bytes", "input_limit", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			c.Rendered[0].Tree.Entries[0].Bytes = make([]byte, workspace.MaxFrozenBytes+1)
		}},
		{"grant count", "input_limit", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			r.Grants = make([]workspace.EffectGrant, workspace.MaxCollectionItems+1)
		}},
		{"root count", "input_limit", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			r.Roots = make([]workspace.RootRef, workspace.MaxCollectionItems+1)
		}},
		{"scratch count", "input_limit", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			s.Scratch = make([]workspace.ScratchSpec, workspace.MaxCollectionItems+1)
		}},
		{"access count", "input_limit", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			s.ExtraDirs = make([]workspace.AccessRef, workspace.MaxCollectionItems+1)
		}},
		{"map count", "input_limit", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			s.Cleanup.ExpectedGenerations = map[string]string{}
			for i := 0; i <= workspace.MaxCollectionItems; i++ {
				s.Cleanup.ExpectedGenerations[fmt.Sprint(i)] = "generation"
			}
		}},
		{"validity horizon", "invalid_observation_window", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			o.ExpiresAt = o.At.Add(workspace.MaxObservationWindow + 1)
		}},
		{"lock base retarget", "ambiguous_canonical_base", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			obs := rootObservation(o, r.LockRoot.ID)
			obs.CanonicalBase = "/elsewhere"
			obs.CanonicalPath = "/elsewhere/locks"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, c, r, o := planInputs(t)
			tc.change(&s, &c, &r, &o)
			_, err := workspace.Plan(s, c, r, o)
			var refused *workspace.Refusal
			if !errors.As(err, &refused) || refused.Code != tc.code {
				t.Fatalf("got %v want %s", err, tc.code)
			}
		})
	}
}

func TestReviewedSecurityRefusals(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*workspace.Spec, *workspace.ResolvedContent, *workspace.Resources, *workspace.Observations)
	}{
		{"observation unrelated", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			rootObservation(o, s.Home.Root.ID).CanonicalPath = filepath.Join(s.Home.Root.AllowedBase, "unrelated")
		}},
		{"observation unrelated declared", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			rootObservation(o, s.Home.Root.ID).DeclaredPath = filepath.Join(s.Home.Root.AllowedBase, "unrelated")
		}},
		{"missing host root", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			r.Roots = r.Roots[1:]
		}},
		{"canonical global base", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			rootObservation(o, s.Home.Root.ID).CanonicalBase = string(filepath.Separator)
		}},
		{"canonical base is root", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			rootObservation(o, s.Home.Root.ID).CanonicalBase = s.Home.Root.Path
		}},
		{"access traversal", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			s.ExtraDirs = []workspace.AccessRef{{Resource: workspace.ResourceRef{ID: "fixture-extra", Path: "../../etc\x00/x", Provenance: "fixture"}}}
		}},
		{"cwd retarget", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			c.Rendered[0].Binding.CWD = filepath.Join(s.Home.Root.AllowedBase, "elsewhere")
		}},
		{"environment retarget", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			c.Rendered[0].Binding.Environment = map[string]string{"CODEX_HOME": "/elsewhere"}
		}},
		{"credential environment", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			c.Rendered[0].Binding.Environment = map[string]string{"OPENAI_API_KEY": "fixture"}
		}},
		{"rpc retarget", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			c.Rendered[0].Binding.RPCProject = map[string]string{"thread.cwd": "/elsewhere"}
		}},
		{"argv retarget", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			c.Rendered[0].Binding.Argv = []string{"--add-dir", "/elsewhere"}
		}},
		{"credential link to artifact", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			c.Rendered[0].Preparations = []render.Preparation{{Provider: c.Rendered[0].Provider, Kind: render.PreparationCredentialLink, Destination: "AGENTS.md", Policy: layout.LinkOnlyNeverWrite}}
		}},
		{"credential root typo", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			s.Credentials = []workspace.CredentialSpec{{Source: workspace.ResourceRef{ID: "fixture-source"}, DestinationRootID: "typo", Destination: "auth.json", Concern: "fixture", Authorization: workspace.ResourceRef{ID: "fixture-auth"}}}
		}},
		{"invalid utf8 metadata", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			s.Identity.Assignment = string([]byte{255})
		}},
		{"invalid utf8 resource", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			r.ProviderHomes = []workspace.ResourceRef{{ID: string([]byte{254})}}
		}},
		{"invalid utf8 observation", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			o.Roots[0].Uncertainty = string([]byte{254})
			o.Roots = append(o.Roots, workspace.RootObservation{RootID: string([]byte{255})})
		}},
		{"tree entry bound", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			c.Rendered[0].Tree.Entries = nil
			for i := 0; i <= render.MaxTreeEntries; i++ {
				entry := tree(fmt.Sprintf("entry-%d", i)).Entries[0]
				entry.Ownership.EntryID = fmt.Sprintf("fixture-%d", i)
				c.Rendered[0].Tree.Entries = append(c.Rendered[0].Tree.Entries, entry)
			}
		}},
		{"credential list bound", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			for i := 0; i <= render.MaxTreeEntries; i++ {
				s.Credentials = append(s.Credentials, workspace.CredentialSpec{Source: workspace.ResourceRef{ID: "fixture"}, Authorization: workspace.ResourceRef{ID: "fixture"}, DestinationRootID: s.Boot.Candidate.ID, Destination: "auth.json", Concern: "fixture"})
			}
		}},
		{"namespace bound", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			r.LockNamespace = "/" + strings.Repeat("x", render.MaxPathBytes)
		}},
		{"second ungranted artifact", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			s.Effects = append(s.Effects, workspace.EffectGrant{Kind: workspace.ArtifactEffect, RootID: s.Boot.Candidate.ID, AuthorizationID: "ungranted", Version: "2"})
		}},
		{"ungranted trust", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			s.Effects = append(s.Effects, workspace.EffectGrant{Kind: workspace.TrustEffect, RootID: s.Home.Root.ID, AuthorizationID: "ungranted", Version: "2"})
		}},
		{"session traversal", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			s.Identity.Session = "../../x/y"
		}},
		{"instance traversal", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			s.Identity.Instance = "../../x/y"
		}},
		{"assignment traversal", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			s.Identity.Assignment = "../../x/y"
		}},
		{"duplicate entry IDs", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			c.Rendered[0].Tree.Entries = append(c.Rendered[0].Tree.Entries, tree("other.md").Entries[0])
		}},
		{"case alias", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			entry := tree("agents.MD").Entries[0]
			entry.Ownership.EntryID = "fixture-other"
			c.Rendered[0].Tree.Entries = append(c.Rendered[0].Tree.Entries, entry)
		}},
		{"writable by others", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			c.Rendered[0].Tree.Entries[0].Mode = 0777
		}},
		{"root control text", func(s *workspace.Spec, c *workspace.ResolvedContent, r *workspace.Resources, o *workspace.Observations) {
			s.Home.Root.Path += "\t"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, c, r, o := planInputs(t)
			tc.mutate(&s, &c, &r, &o)
			if _, err := workspace.Plan(s, c, r, o); err == nil {
				t.Fatal("unsafe reviewed input accepted")
			}
		})
	}
}
func TestCredentialFoldedDestinationCollision(t *testing.T) {
	e := tree("private/tokenſ")
	if err := workspace.ValidateManagedTree(e, []string{"private/tokens"}); err == nil {
		t.Fatal("Unicode case alias escaped exclusion")
	}
	m := materializeManifestForCredential()
	if err := workspace.ValidateManagedManifest(m, []string{"private/tokens"}); err == nil {
		t.Fatal("manifest Unicode alias escaped exclusion")
	}
}

func materializeManifestForCredential() materialize.Manifest {
	return materialize.Manifest{Entries: []materialize.ManifestEntry{{Path: "private/tokenſ", Kind: artifact.EntryFile}}}
}

func TestAdditionalContractRefusals(t *testing.T) {
	for _, kind := range []string{"required capability", "duplicate effects", "identity basename", "current basename", "empty manifest"} {
		t.Run(kind, func(t *testing.T) {
			s, c, r, o := planInputs(t)
			switch kind {
			case "required capability":
				s.Sandbox.RequiredCapabilities = []workspace.Capability{workspace.UseReservation}
			case "duplicate effects":
				s.Effects = append(s.Effects, s.Effects[0])
			case "identity basename":
				s.Boot.IdentityRoot.Path = filepath.Join(s.Boot.IdentityRoot.AllowedBase, "not-key")
				s.Boot.Current.Path = filepath.Join(s.Boot.IdentityRoot.Path, "current")
				s.Boot.Candidate.Path = filepath.Join(s.Boot.IdentityRoot.Path, "candidate")
				for i := range r.Roots {
					switch r.Roots[i].ID {
					case s.Boot.IdentityRoot.ID:
						r.Roots[i] = s.Boot.IdentityRoot
					case s.Boot.Current.ID:
						r.Roots[i] = s.Boot.Current
					case s.Boot.Candidate.ID:
						r.Roots[i] = s.Boot.Candidate
					}
					o.Roots[i].DeclaredPath = r.Roots[i].Path
					o.Roots[i].CanonicalPath = r.Roots[i].Path
				}
				c.Roots[layout.RootBoot] = s.Boot.Candidate.Path
				c.Rendered[0].Binding.CWD = s.Boot.Candidate.Path
			case "current basename":
				s.Boot.Current.Path = filepath.Join(s.Boot.IdentityRoot.Path, "not-current")
				for i := range r.Roots {
					if r.Roots[i].ID == s.Boot.Current.ID {
						r.Roots[i] = s.Boot.Current
						rootObservation(&o, s.Boot.Current.ID).DeclaredPath = s.Boot.Current.Path
						rootObservation(&o, s.Boot.Current.ID).CanonicalPath = s.Boot.Current.Path
					}
				}
			case "empty manifest":
				obs := rootObservation(&o, s.Boot.Candidate.ID)
				obs.Exists = true
				obs.Directory = true
				obs.Empty = true
				obs.Manifest = &materialize.Manifest{Generation: "fixture"}
				s.Boot.CandidateGeneration = "fixture"
			}
			if _, err := workspace.Plan(s, c, r, o); err == nil {
				t.Fatal("unsafe contract input accepted")
			}
		})
	}
}
func TestEffectCoverageOrderIndependent(t *testing.T) {
	s, c, r, o := planInputs(t)
	outer := root("shared")
	inner := root("nested")
	inner.Path = filepath.Join(outer.Path, "nested")
	for _, ref := range []workspace.RootRef{outer, inner} {
		r.Roots = append(r.Roots, ref)
		o.Roots = append(o.Roots, workspace.RootObservation{RootID: ref.ID, DeclaredPath: ref.Path, CanonicalPath: ref.Path, CanonicalBase: ref.AllowedBase, Owner: ref.Owner})
		g := workspace.EffectGrant{Kind: workspace.TrustEffect, RootID: ref.ID, AuthorizationID: "fixture", Version: "1"}
		s.Effects = append(s.Effects, g)
		r.Grants = append(r.Grants, g)
	}
	p, err := workspace.Plan(s, c, r, o)
	if err != nil {
		t.Fatal(err)
	}
	s.Effects[len(s.Effects)-1], s.Effects[len(s.Effects)-2] = s.Effects[len(s.Effects)-2], s.Effects[len(s.Effects)-1]
	other, err := workspace.Plan(s, c, r, o)
	if err != nil || other.Digest() != p.Digest() {
		t.Fatal("effect order changed acceptance or digest", err)
	}
}
