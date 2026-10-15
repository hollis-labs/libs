package workspace

import (
	"github.com/hollis-labs/substrate/harness/workspace/effects"
	"github.com/hollis-labs/substrate/harness/workspace/repositories"
	"slices"
	"strings"
	"testing"
)

func repositoryPlanInputs(t *testing.T) (Spec, ResolvedContent, Resources, Observations) {
	t.Helper()
	s, c, r, o := fixturePlanInputs(t)
	source := fixtureRoot("repo-source")
	common := fixtureRoot("repo-common")
	common.Path = source.Path + "/.git"
	base := fixtureRoot("repo-base")
	target := fixtureRoot("repo-target")
	target.Path = base.Path + "/attachment"
	for _, ref := range []RootRef{source, common, base, target} {
		r.Roots = append(r.Roots, ref)
		o.Roots = append(o.Roots, RootObservation{RootID: ref.ID, DeclaredPath: ref.Path, CanonicalPath: ref.Path, CanonicalBase: ref.AllowedBase, Owner: ref.Owner, Exists: ref.ID != target.ID, Directory: ref.ID != target.ID})
	}
	grant := EffectGrant{Kind: RepositoryEffect, RootID: base.ID, AuthorizationID: "repo-authority", Version: "1"}
	s.Effects = append(s.Effects, grant)
	r.Grants = append(r.Grants, grant)
	r.Capabilities = append(r.Capabilities, RepositoryAttachments)
	o.Capabilities = append(o.Capabilities, RepositoryAttachments)
	s.Repos = []RepoSpec{{ID: "repo", Source: ResourceRef{ID: source.ID, Path: source.Path, Provenance: source.Provenance}, DesiredRoot: target, Mode: Worktree, BaseCommit: strings.Repeat("a", 40), BranchTemplate: "work/fixture", Retention: Keep}}
	root := func(ref RootRef) effects.RootInput {
		return effects.RootInput{ID: ref.ID, Path: ref.Path, AllowedBase: ref.AllowedBase, Owner: ref.Owner, Provenance: ref.Provenance, MutationIdentity: ref.Path, Inactive: true, PrivateCustody: true}
	}
	s.EffectInputs.Repositories = []repositories.Request{{Mode: repositories.Worktree, Ownership: repositories.Owned, Source: root(source), Common: root(common), Base: root(base), Path: target.Path, RepositoryID: "repo", SourceIdentity: "source-identity", CommonIdentity: "common-identity", Branch: "work/fixture", BaseCommit: s.Repos[0].BaseCommit, AuthorizationID: grant.AuthorizationID, AuthorizationVersion: grant.Version, CandidateRootID: s.Boot.Candidate.ID}}
	return s, c, r, o
}
func TestRepositoryPlanFreezesRequestAndCompleteLocks(t *testing.T) {
	s, c, r, o := repositoryPlanInputs(t)
	p, err := Plan(s, c, r, o)
	if err != nil {
		t.Fatal(err)
	}
	req := p.EffectInputs().Repositories[0]
	if req.Header != effectHeader(p) {
		t.Fatal("repository header unbound")
	}
	for _, root := range []effects.RootInput{req.Source, req.Common, req.Base} {
		if !slices.Contains(p.LockKeys(), LockKey{r.LockNamespace, root.Path}) {
			t.Fatal("missing exact repository lock", root.ID)
		}
	}
	for _, d := range p.Diagnostics() {
		if d.Status == Unsupported {
			t.Fatal("supported repository deferred", d)
		}
	}
	s.EffectInputs.Repositories[0].SourceIdentity = "changed"
	q, err := Plan(s, c, r, o)
	if err != nil || q.Digest() == p.Digest() {
		t.Fatal("identity not digested", err)
	}
	if p.EffectInputs().Repositories[0].SourceIdentity != "source-identity" {
		t.Fatal("mutable repository request")
	}
	detached := p.EffectInputs()
	detached.Repositories[0].Base.Provenance = "edited"
	if p.EffectInputs().Repositories[0].Base.Provenance == "edited" {
		t.Fatal("mutable returned request")
	}
}
func TestRepositoryPlanRejectsUnboundRequests(t *testing.T) {
	for name, change := range map[string]func(*Spec, *Resources, *Observations){
		"missing": func(s *Spec, _ *Resources, _ *Observations) { s.EffectInputs.Repositories = nil },
		"duplicate": func(s *Spec, _ *Resources, _ *Observations) {
			s.EffectInputs.Repositories = append(s.EffectInputs.Repositories, s.EffectInputs.Repositories[0])
		},
		"unknown": func(s *Spec, _ *Resources, _ *Observations) { s.EffectInputs.Repositories[0].RepositoryID = "foreign" },
		"header": func(s *Spec, _ *Resources, _ *Observations) {
			s.EffectInputs.Repositories[0].Header.OperationID = "foreign"
		},
		"source": func(s *Spec, _ *Resources, _ *Observations) {
			s.EffectInputs.Repositories[0].Source.Path = "/fixture/foreign"
		},
		"source-provenance": func(s *Spec, _ *Resources, _ *Observations) {
			s.EffectInputs.Repositories[0].Source.Provenance = "foreign"
		},
		"common-provenance": func(s *Spec, _ *Resources, _ *Observations) {
			s.EffectInputs.Repositories[0].Common.Provenance = "foreign"
		},
		"base-provenance": func(s *Spec, _ *Resources, _ *Observations) {
			s.EffectInputs.Repositories[0].Base.Provenance = "foreign"
		},
		"private-checkout": func(s *Spec, _ *Resources, _ *Observations) {
			s.EffectInputs.Repositories[0].Mode = repositories.Checkout
			s.Repos[0].Mode = Checkout
		},
		"common": func(s *Spec, _ *Resources, _ *Observations) { s.EffectInputs.Repositories[0].Common.ID = "unknown" },
		"base": func(s *Spec, _ *Resources, _ *Observations) {
			s.EffectInputs.Repositories[0].Base.MutationIdentity = s.Home.Root.Path
		},
		"coherent-wrong-parent": func(s *Spec, r *Resources, _ *Observations) {
			s.EffectInputs.Repositories[0].Base = s.EffectInputs.Repositories[0].Source
			for i := range s.Effects {
				if s.Effects[i].Kind == RepositoryEffect {
					s.Effects[i].RootID = s.EffectInputs.Repositories[0].Source.ID
				}
			}
			r.Grants = slices.Clone(s.Effects)
		},
		"parent": func(s *Spec, _ *Resources, _ *Observations) { s.EffectInputs.Repositories[0].Path = s.Home.Root.Path },
		"candidate": func(s *Spec, _ *Resources, _ *Observations) {
			s.EffectInputs.Repositories[0].CandidateRootID = s.Boot.Current.ID
		},
		"grant": func(s *Spec, _ *Resources, _ *Observations) {
			s.EffectInputs.Repositories[0].AuthorizationVersion = "2"
		},
		"commit": func(s *Spec, _ *Resources, _ *Observations) {
			s.EffectInputs.Repositories[0].BaseCommit = strings.Repeat("b", 40)
		},
		"existing": func(s *Spec, _ *Resources, _ *Observations) { s.EffectInputs.Repositories[0].Existing = true },
		"ownership": func(s *Spec, _ *Resources, _ *Observations) {
			s.EffectInputs.Repositories[0].Ownership = repositories.UserOwned
		},
		"custody": func(s *Spec, _ *Resources, _ *Observations) {
			s.EffectInputs.Repositories[0].Base.PrivateCustody = false
		},
		"capability": func(_ *Spec, r *Resources, _ *Observations) {
			r.Capabilities = slices.DeleteFunc(r.Capabilities, func(c Capability) bool { return c == RepositoryAttachments })
		},
		"missing-parent": func(_ *Spec, _ *Resources, o *Observations) { fixtureObservation(o, "repo-base").Exists = false },
		"unmatched":      func(s *Spec, _ *Resources, _ *Observations) { s.Repos = nil },
	} {
		t.Run(name, func(t *testing.T) {
			s, c, r, o := repositoryPlanInputs(t)
			change(&s, &r, &o)
			if _, err := Plan(s, c, r, o); err == nil {
				t.Fatal("unbound repository accepted")
			}
		})
	}
}

func repositoryEvidenceFixture(p PlannedWorkspace) effects.Evidence {
	r := p.EffectInputs().Repositories[0]
	return effects.Evidence{Header: r.Header, Kind: effects.RepositoryAttachment, RootID: r.Base.ID, Phase: effects.IntentPhase, Outcome: effects.Pending, Attachments: []effects.AttachmentEvidence{{Owner: r.Base.Owner, Mode: string(r.Mode), Ownership: string(r.Ownership), Path: r.Path, SourcePath: r.Source.Path, CommonPath: r.Common.Path, RepositoryID: r.RepositoryID, SourceIdentity: r.SourceIdentity, CommonIdentity: r.CommonIdentity, Branch: r.Branch, BaseCommit: r.BaseCommit, AuthorizationID: r.AuthorizationID, AuthorizationVersion: r.AuthorizationVersion, Outcome: effects.Pending}}}
}
func TestRepositorySinkRejectsForeignPayloadsBeforePersistence(t *testing.T) {
	s, c, r, o := repositoryPlanInputs(t)
	p := fixturePlanned(t, s, c, r, o)
	valid := repositoryEvidenceFixture(p)
	for name, change := range map[string]func(*effects.Evidence){
		"links":            func(e *effects.Evidence) { e.Links = []effects.LinkEvidence{{}} },
		"trust":            func(e *effects.Evidence) { e.Trust = []effects.TrustEvidence{{}} },
		"extra-attachment": func(e *effects.Evidence) { e.Attachments = append(e.Attachments, e.Attachments[0]) },
		"root":             func(e *effects.Evidence) { e.RootID = s.Boot.Candidate.ID },
		"grant":            func(e *effects.Evidence) { e.Attachments[0].AuthorizationVersion = "foreign" },
		"header":           func(e *effects.Evidence) { e.Header.InputDigest = "foreign" },
		"retirement":       func(e *effects.Evidence) { e.Attachments[0].ShippedProofID = "foreign" },
		"phase":            func(e *effects.Evidence) { e.Phase = effects.PreflightPhase },
		"outcome":          func(e *effects.Evidence) { e.Attachments[0].Outcome = effects.Applied },
		"sibling":          func(e *effects.Evidence) { e.Kind = effects.CredentialLinks; e.RootID = s.Boot.Candidate.ID },
	} {
		t.Run(name, func(t *testing.T) {
			e := valid.Clone()
			change(&e)
			sink := effectReceiptSink{p: p}
			if sink.known(e) {
				t.Fatal("foreign payload accepted")
			}
		})
	}
	if !(effectReceiptSink{p: p}).known(valid) {
		t.Fatal("valid intent rejected")
	}
}

func TestRepositoryResolvedAttestationsBindDigest(t *testing.T) {
	for name, change := range map[string]func(*Spec){
		"source-identity": func(s *Spec) { s.EffectInputs.Repositories[0].SourceIdentity = "new" },
		"common-identity": func(s *Spec) { s.EffectInputs.Repositories[0].CommonIdentity = "new" },
		"source-custody":  func(s *Spec) { s.EffectInputs.Repositories[0].Source.PrivateCustody = false },
		"base-inactive":   func(s *Spec) { s.EffectInputs.Repositories[0].Base.Inactive = false },
		"branch": func(s *Spec) {
			s.EffectInputs.Repositories[0].Branch = "work/changed"
			s.Repos[0].BranchTemplate = "work/changed"
		},
		"base-commit": func(s *Spec) {
			s.EffectInputs.Repositories[0].BaseCommit = strings.Repeat("b", 40)
			s.Repos[0].BaseCommit = strings.Repeat("b", 40)
		},
		"authority": func(s *Spec) {
			s.EffectInputs.Repositories[0].AuthorizationVersion = "2"
			for i := range s.Effects {
				if s.Effects[i].Kind == RepositoryEffect {
					s.Effects[i].Version = "2"
				}
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, c, r, o := repositoryPlanInputs(t)
			p := fixturePlanned(t, s, c, r, o)
			change(&s)
			r.Grants = slices.Clone(s.Effects)
			q := fixturePlanned(t, s, c, r, o)
			if p.Digest() == q.Digest() {
				t.Fatal("attestation absent from digest")
			}
		})
	}
}
