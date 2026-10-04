package workspace

import (
	"path/filepath"
	"slices"

	"github.com/hollis-labs/substrate/harness/sandbox"
	"github.com/hollis-labs/substrate/harness/workspace/credentials"
	"github.com/hollis-labs/substrate/harness/workspace/effects"
)

func effectHeader(p PlannedWorkspace) effects.Header {
	return effects.Header{Version: effects.SchemaVersion, OperationID: p.spec.OperationID, InputDigest: p.digest}
}

func effectRoot(ref RootRef, o RootObservation, identity string) effects.RootInput {
	return effects.RootInput{ID: ref.ID, Path: o.CanonicalPath, AllowedBase: o.CanonicalBase, Owner: ref.Owner, Provenance: ref.Provenance, MutationIdentity: identity, Inactive: true, PrivateCustody: true}
}

// freezeEffects binds explicit resolved inputs to semantic requests and host
// grants. Leaf preflight still refreshes custody/authority under held locks.
func freezeEffects(p *PlannedWorkspace, obs map[string]RootObservation) error {
	in := copyRecord(p.spec.EffectInputs)
	if len(in.Credentials)+len(in.Repositories)+len(in.Trust) > 0 {
		artifactAction := false
		for _, a := range p.actions {
			if a.Kind == TreeAction && a.Root.ID == p.spec.Boot.Candidate.ID {
				artifactAction = true
			}
		}
		if !artifactAction {
			return refuse("missing_effect_artifacts", "effects", Unsupported)
		}
		for _, cap := range []struct {
			enabled    bool
			capability Capability
		}{{len(in.Credentials) > 0, CredentialLinks}, {len(in.Trust) > 0, TrustHandling}, {len(in.Repositories) > 0, RepositoryAttachments}} {
			if cap.enabled && (!slices.Contains(p.resources.Capabilities, cap.capability) || !slices.Contains(p.observed.Capabilities, cap.capability)) {
				return refuse(CodeRequiredCapabilityUnavailable, "effects", Unsupported)
			}
		}
	}
	seen := map[int]bool{}
	for i := range in.Credentials {
		g := &in.Credentials[i]
		candidate := p.spec.Boot.Candidate
		if g.Header != (effects.Header{}) || g.Layer != credentials.BootLayer || g.Candidate != effectRoot(candidate, obs[candidate.ID], obs[p.spec.Boot.IdentityRoot.ID].CanonicalPath) || len(g.Bindings) == 0 {
			return refuse("effect_input_binding", "credentials", Conflict)
		}
		for _, b := range g.Bindings {
			matched := -1
			for n, c := range p.spec.Credentials {
				if c.DestinationRootID == candidate.ID && c.Destination == b.Destination && c.Source.Path == filepath.Join(g.Home.LogicalPath, filepath.FromSlash(b.Source)) && c.Authorization.ID == b.AuthorizationID && c.Authorization.Revision == b.AuthorizationVersion && c.Required == b.Required && b.SourceRead && !b.SourceWrite && slices.Contains(c.Access, sandbox.AccessSourceRead) {
					matched = n
				}
			}
			grant := EffectGrant{Kind: CredentialLinkEffect, RootID: candidate.ID, AuthorizationID: b.AuthorizationID, Version: b.AuthorizationVersion}
			if matched < 0 || seen[matched] || !slices.Contains(p.spec.Effects, grant) || !slices.Contains(p.resources.Grants, grant) {
				return refuse("effect_input_binding", "credentials", Conflict)
			}
			seen[matched] = true
		}
		g.Header = effectHeader(*p)
	}
	if len(in.Credentials) > 0 && len(seen) != len(p.spec.Credentials) {
		return refuse("effect_input_binding", "credentials", Conflict)
	}
	if len(in.Credentials) > 0 {
		for _, rendered := range p.content {
			for _, e := range rendered.Effects {
				matched := false
				for _, g := range in.Credentials {
					for _, b := range g.Bindings {
						if rendered.Root == "boot" && g.Home.Provider == string(rendered.Provider) && b.Destination == e.Path {
							matched = true
						}
					}
				}
				if !matched {
					return refuse("effect_input_binding", "render", Conflict)
				}
			}
			for _, prep := range rendered.Preparations {
				if prep.Kind != "credential-link" {
					continue
				}
				matched := false
				for _, g := range in.Credentials {
					for _, b := range g.Bindings {
						if g.Home.Provider == string(rendered.Provider) && b.Destination == prep.Destination {
							matched = true
						}
					}
				}
				if !matched {
					return refuse("effect_input_binding", "render", Conflict)
				}
			}
		}
	}
	seenTrust := map[int]bool{}
	for i := range in.Trust {
		r := &in.Trust[i]
		config, ok := findRoot(p.roots, r.Config.ID)
		if !ok || r.Header != (effects.Header{}) || r.CandidateRootID != p.spec.Boot.Candidate.ID || r.Config.ID != config.ID || r.Config.Path != obs[config.ID].CanonicalPath || r.Config.AllowedBase != obs[config.ID].CanonicalBase || r.Config.Owner != config.Owner || r.Config.Provenance != config.Provenance || r.Config.MutationIdentity != obs[config.ID].CanonicalPath || !r.Target.Stable || r.Target.LogicalPath != p.spec.Boot.Current.Path || r.Target.CanonicalParent != obs[p.spec.Boot.IdentityRoot.ID].CanonicalPath || r.Target.AllowedBase != obs[p.spec.Boot.IdentityRoot.ID].CanonicalBase || r.Target.Provenance == "" {
			return refuse("effect_input_binding", "trust", Conflict)
		}
		matched := -1
		for n, t := range p.spec.Trust {
			if t.Mechanism != string(r.Mechanism) || t.Required != r.Required || t.Authorization.ID != r.AuthorizationID || t.Authorization.Revision != r.AuthorizationVersion || len(t.Targets) != 1 {
				continue
			}
			target := t.Targets[0]
			if target.ID == p.spec.Boot.Current.ID && target.Path == r.Target.LogicalPath && target.Provenance == r.Target.Provenance {
				matched = n
			}
		}
		grant := EffectGrant{Kind: TrustEffect, RootID: config.ID, AuthorizationID: r.AuthorizationID, Version: r.AuthorizationVersion}
		if matched < 0 || seenTrust[matched] || !slices.Contains(p.spec.Effects, grant) || !slices.Contains(p.resources.Grants, grant) {
			return refuse("effect_input_binding", "trust", Conflict)
		}
		seenTrust[matched] = true
		r.Header = effectHeader(*p)
	}
	if len(in.Trust) > 0 && len(seenTrust) != len(p.spec.Trust) {
		return refuse("effect_input_binding", "trust", Conflict)
	}
	for i := range in.Repositories {
		in.Repositories[i].Header = effectHeader(*p)
	}
	p.effectInputs = in
	for i := range p.diagnostics {
		if handledEffectDiagnostic(*p, p.diagnostics[i]) {
			p.diagnostics[i].Status = Partial
		}
	}
	return nil
}

func handledEffectDiagnostic(p PlannedWorkspace, d Diagnostic) bool {
	switch d.Code {
	case "credential_effect_pending":
		return len(p.effectInputs.Credentials) > 0 && d.RootID == p.spec.Boot.Candidate.ID
	case "preparation_pending":
		return d.Concern == "credential-link" && len(p.effectInputs.Credentials) > 0 && d.RootID == p.spec.Boot.Candidate.ID
	case "host_effects_pending":
		if d.Concern == string(CredentialLinkEffect) {
			return len(p.effectInputs.Credentials) > 0 && d.RootID == p.spec.Boot.Candidate.ID
		}
		if d.Concern == string(TrustEffect) {
			for _, r := range p.effectInputs.Trust {
				if r.Config.ID == d.RootID {
					return true
				}
			}
			return false
		}
		if d.Concern == string(RepositoryEffect) {
			for _, r := range p.effectInputs.Repositories {
				if r.Base.ID == d.RootID || r.Existing && r.Path == rootCanonicalPath(p, d.RootID) {
					return true
				}
			}
			return false
		}
		return d.Concern == "effects" && len(p.spec.Repos) == len(p.effectInputs.Repositories) && len(p.spec.Trust) == len(p.effectInputs.Trust) && p.spec.Sandbox.Policy.Mode == sandbox.ConfinementDisabled
	}
	return false
}

func rootCanonicalPath(p PlannedWorkspace, id string) string {
	for _, o := range p.observed.Roots {
		if o.RootID == id {
			return o.CanonicalPath
		}
	}
	return ""
}
