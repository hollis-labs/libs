package workspace

import (
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace/effects"
	"github.com/hollis-labs/substrate/harness/workspace/repositories"
)

func clonePlanInputs(t *testing.T, requirement repositories.CloneRequirement) (Spec, ResolvedContent, Resources, Observations) {
	t.Helper()
	s, c, r, o := repositoryPlanInputs(t)
	s.Repos[0].Mode = Clone
	s.Repos[0].Clone = requirement
	s.EffectInputs.Repositories[0].Mode = repositories.Clone
	s.EffectInputs.Repositories[0].Clone = repositories.CloneIntent{Requirement: requirement}
	return s, c, r, o
}

func TestClonePlanAcceptsFrozenIntent(t *testing.T) {
	for _, requirement := range []repositories.CloneRequirement{repositories.CloneRequired, repositories.ClonePreferred} {
		s, c, r, o := clonePlanInputs(t, requirement)
		p, err := Plan(s, c, r, o)
		if err != nil {
			t.Fatal(requirement, err)
		}
		if got := p.EffectInputs().Repositories[0].Clone; got.Requirement != requirement || got.Method != "" {
			t.Fatal("clone intent not frozen", got)
		}
	}
}

func TestClonePlanFallbackNeedsItsOwnSharedMetadataGrant(t *testing.T) {
	s, c, r, o := clonePlanInputs(t, repositories.ClonePreferred)
	in := &s.EffectInputs.Repositories[0]
	in.Clone.FallbackAuthorizationID, in.Clone.FallbackAuthorizationVersion = "fallback-authority", "1"
	if _, err := Plan(s, c, r, o); err == nil {
		t.Fatal("fallback accepted without a granted effect")
	}
	fallback := EffectGrant{Kind: RepositoryEffect, RootID: in.Common.ID, AuthorizationID: "fallback-authority", Version: "1"}
	s.Effects = append(s.Effects, fallback)
	if _, err := Plan(s, c, r, o); err == nil {
		t.Fatal("fallback accepted without a host grant")
	}
	r.Grants = append(r.Grants, fallback)
	if _, err := Plan(s, c, r, o); err != nil {
		t.Fatal(err)
	}
}

func TestClonePlanRejectsInconsistentIntent(t *testing.T) {
	for name, change := range map[string]func(*Spec){
		"missing requirement": func(s *Spec) {
			s.Repos[0].Clone = ""
			s.EffectInputs.Repositories[0].Clone = repositories.CloneIntent{}
		},
		"unknown requirement": func(s *Spec) {
			s.Repos[0].Clone = "eventually"
			s.EffectInputs.Repositories[0].Clone.Requirement = "eventually"
		},
		"requirement on worktree": func(s *Spec) { s.Repos[0].Mode = Worktree; s.EffectInputs.Repositories[0].Mode = repositories.Worktree },
		"spec and input differ":   func(s *Spec) { s.EffectInputs.Repositories[0].Clone.Requirement = repositories.ClonePreferred },
		"preselected method":      func(s *Spec) { s.EffectInputs.Repositories[0].Clone.Method = repositories.Reflink },
		"preselected fallback":    func(s *Spec) { s.EffectInputs.Repositories[0].Clone.FallbackReason = "chosen" },
		"existing clone":          func(s *Spec) { s.Repos[0].Existing = &AttachmentReceipt{} },
	} {
		s, c, r, o := clonePlanInputs(t, repositories.CloneRequired)
		change(&s)
		if _, err := Plan(s, c, r, o); err == nil {
			t.Fatal("inconsistent clone intent accepted", name)
		}
	}
}

func TestCloneRecoveryEvidenceBindsSelectedConstruction(t *testing.T) {
	s, c, r, o := clonePlanInputs(t, repositories.ClonePreferred)
	in := &s.EffectInputs.Repositories[0]
	in.Clone.FallbackAuthorizationID, in.Clone.FallbackAuthorizationVersion = "fallback-authority", "1"
	fallback := EffectGrant{Kind: RepositoryEffect, RootID: in.Common.ID, AuthorizationID: "fallback-authority", Version: "1"}
	s.Effects = append(s.Effects, fallback)
	r.Grants = append(r.Grants, fallback)
	p, err := Plan(s, c, r, o)
	if err != nil {
		t.Fatal(err)
	}
	req := p.EffectInputs().Repositories[0]
	evidence := func(mode repositories.Mode, method repositories.CloneMethod, reason, grant string) effects.Evidence {
		a := effects.AttachmentEvidence{Owner: req.Base.Owner, Mode: string(mode), Ownership: string(req.Ownership), Path: req.Path, SourcePath: req.Source.Path, CommonPath: req.Common.Path, RepositoryID: req.RepositoryID, SourceIdentity: req.SourceIdentity, CommonIdentity: req.CommonIdentity, Branch: req.Branch, BaseCommit: req.BaseCommit, Head: req.BaseCommit, AuthorizationID: req.AuthorizationID, AuthorizationVersion: req.AuthorizationVersion, Created: true, Outcome: effects.Applied, RequestedMode: string(repositories.Clone), Method: string(method), FallbackReason: reason}
		if grant != "" {
			a.FallbackAuthorizationID, a.FallbackAuthorizationVersion = grant, "1"
		}
		return effects.Evidence{Header: req.Header, Kind: effects.RepositoryAttachment, RootID: req.Base.ID, Phase: effects.CompletePhase, Outcome: effects.Applied, Attachments: []effects.AttachmentEvidence{a}}
	}
	if !repositoryEvidenceBound(req, evidence(repositories.Clone, repositories.Reflink, "", "")) {
		t.Fatal("copy-on-write clone receipt refused")
	}
	if !repositoryEvidenceBound(req, evidence(repositories.Worktree, repositories.NoClone, "reflink_unsupported", "fallback-authority")) {
		t.Fatal("authorized fallback receipt refused")
	}
	for name, e := range map[string]effects.Evidence{
		"fallback without grant": evidence(repositories.Worktree, repositories.NoClone, "reflink_unsupported", ""),
		"fallback other grant":   evidence(repositories.Worktree, repositories.NoClone, "reflink_unsupported", "other"),
		"clone with reason":      evidence(repositories.Clone, repositories.Reflink, "reason", ""),
		"clone without method":   evidence(repositories.Clone, repositories.NoClone, "", ""),
	} {
		if repositoryEvidenceBound(req, e) {
			t.Fatal("forged clone receipt bound", name)
		}
	}
}
