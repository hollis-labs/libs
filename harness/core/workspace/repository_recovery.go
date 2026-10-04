package workspace

import (
	"context"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/hollis-labs/substrate/harness/workspace/effects"
	"github.com/hollis-labs/substrate/harness/workspace/repositories"
)

// Only nonsecret attachment evidence for this original request is acceptable.
// Retirement proofs and sibling-kind payloads cannot enter the attachment seam.
func repositoryEvidenceBound(r repositories.Request, e effects.Evidence) bool {
	if !r.Header.Valid() || e.Header != r.Header || e.Kind != effects.RepositoryAttachment || e.RootID != r.Base.ID || len(e.Attachments) != 1 || len(e.Links) != 0 || len(e.Trust) != 0 {
		return false
	}
	a := e.Attachments[0]
	if a.Outcome != e.Outcome {
		return false
	}
	switch e.Phase {
	case effects.IntentPhase:
		if e.Outcome != effects.Pending || a.Created || a.Head != "" {
			return false
		}
	case effects.InterruptedPhase:
		if e.Outcome != effects.Partial {
			return false
		}
	case effects.AbortedPhase:
		if e.Outcome != effects.Refused || a.Created {
			return false
		}
	case effects.CompletePhase:
		decoded, err := hex.DecodeString(a.Head)
		if err != nil || len(decoded) != 20 && len(decoded) != 32 || strings.ToLower(a.Head) != a.Head {
			return false
		}
		if !(e.Outcome == effects.Applied && !r.Existing && a.Created || e.Outcome == effects.AlreadyPresent && r.Existing && !a.Created) {
			return false
		}
	default:
		return false
	}
	if a.OriginHeader != (effects.Header{}) || a.ShippedProofID != "" || a.ShippedProofRevision != "" || a.UnusedProofID != "" || a.UnusedProofRevision != "" || a.RetirementProvenance != "" || a.SafetyComplete {
		return false
	}
	return a.Owner == r.Base.Owner && a.UserWriteAuthorizationID == r.UserWriteAuthorizationID && a.UserWriteAuthorizationVersion == r.UserWriteAuthorizationVersion && a.ReadonlyProofID == r.Readonly.ID && a.ReadonlyProofRevision == r.Readonly.Revision && a.ReadonlyProvenance == r.Readonly.Provenance && a.Mode == string(r.Mode) && a.Ownership == string(r.Ownership) && a.Path == r.Path && a.SourcePath == r.Source.Path && a.CommonPath == r.Common.Path && a.RepositoryID == r.RepositoryID && a.SourceIdentity == r.SourceIdentity && a.CommonIdentity == r.CommonIdentity && a.BaseCommit == r.BaseCommit && a.AuthorizationID == r.AuthorizationID && a.AuthorizationVersion == r.AuthorizationVersion && (r.Existing && r.Branch == "" || a.Branch == r.Branch)
}

func sameRepositoryRequest(a, b repositories.Request) bool {
	a.Header = effects.Header{}
	b.Header = effects.Header{}
	return a == b
}

// Inspection uses original bound requests and headers with fresh authority and
// the complete current locks. Completed attachments are observed, never created
// again. Uncertain receipts remain a stop before any new mutation.
func inspectRepositoryRecovery(ctx context.Context, p PlannedWorkspace, ports Ports, result *ApplyResult) (map[string]bool, error) {
	recovered := map[string]bool{}
	type key struct {
		Header effects.Header
		Path   string
	}
	seen := map[key]bool{}
	// Records contain successive evidence. The latest terminal record for an
	// operation supersedes its intent, but obligations from older attempts remain.
	for i := len(result.Receipt.EffectEvidence) - 1; i >= 0; i-- {
		e := result.Receipt.EffectEvidence[i]
		if e.Kind == effects.RepositoryRetirement {
			return recovered, refuse("repository_recovery_binding", "repositories", Conflict)
		}
		if e.Kind != effects.RepositoryAttachment {
			if len(e.Attachments) > 0 {
				return recovered, refuse("repository_recovery_binding", "repositories", Conflict)
			}
			continue
		}
		if len(e.Attachments) != 1 {
			return recovered, refuse("repository_recovery_binding", "repositories", Conflict)
		}
		k := key{e.Header, e.Attachments[0].Path}
		if seen[k] {
			continue
		}
		seen[k] = true
		var original repositories.Request
		found := false
		for _, r := range result.Receipt.RepositoryRequests {
			if repositoryEvidenceBound(r, e) {
				if found && original != r {
					return recovered, refuse("repository_recovery_binding", "repositories", Conflict)
				}
				original = r
				found = true
			}
		}
		matched := false
		for _, r := range p.effectInputs.Repositories {
			if sameRepositoryRequest(original, r) {
				matched = true
			}
		}
		if !found || !matched {
			return recovered, refuse("repository_recovery_binding", "repositories", Conflict)
		}
		c := effectContext(ctx, p, ports)
		c.Header = original.Header
		out := repositories.InspectResume(ctx, original, c, ports.Repositories, e)
		for _, o := range out.Obligations {
			appendObligation(result, Obligation{Kind: RecoveryInspectionRequired, RootID: o.RootID, Code: o.Code})
		}
		if !effectSuccess(out.Outcome) {
			if root, ok := findRoot(p.roots, original.Base.ID); ok {
				result.Retained = appendRoot(result.Retained, root)
			}
			for _, repo := range p.spec.Repos {
				if rootCanonicalPath(p, repo.DesiredRoot.ID) == original.Path {
					result.Retained = appendRoot(result.Retained, repo.DesiredRoot)
				}
			}
			result.Receipt.Obligations = slices.Clone(result.Obligations)
			return recovered, refuse(out.Code, "repositories", effectStatus(out.Outcome))
		}
		// Only proved completed attachments skip creation. Other original attempts
		// require an explicit recovery decision and cannot be silently replayed.
		if out.Outcome == effects.AlreadyPresent {
			recovered[original.Path] = true
		}
	}
	return recovered, nil
}
