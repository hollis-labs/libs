package workspace

import (
	"path/filepath"
	"slices"
)

// Publication returns a detached desired request, never a custody capability or
// reservation. Nil preserves the artifact-only operation and digest protocol.
func (p PlannedWorkspace) Publication() *PublicationSpec { return copyRecord(p.spec.Publication) }

func validatePublicationSpec(s Spec) error {
	p := s.Publication
	if p == nil {
		for _, g := range s.Effects {
			if g.Kind == PublicationEffect || g.Kind == PinCreationEffect {
				return refuse("publication_request_required", "publication", Conflict)
			}
		}
		return nil
	}
	if s.Operation != Prepare && s.Operation != Resume {
		return refuse("publication_operation_unsupported", "publication", Unsupported)
	}
	if p.JournalID == "" || p.ReservationID == "" || !safeValueText(p.JournalID) || !safeValueText(p.ReservationID) {
		return refuse("publication_missing_binding", "publication", Conflict)
	}
	for _, root := range []RootRef{p.Control, p.Aside} {
		if err := root.Validate(); err != nil {
			return err
		}
	}
	if filepath.Dir(p.Aside.Path) != s.Boot.IdentityRoot.Path || p.Aside.Owner != s.Boot.IdentityRoot.Owner || p.Aside.Path == s.Boot.Current.Path || p.Aside.Path == s.Boot.Candidate.Path || p.Aside.ID == s.Boot.IdentityRoot.ID || p.Aside.ID == s.Boot.Current.ID || p.Aside.ID == s.Boot.Candidate.ID {
		return refuse("publication_aside_invalid", "publication", Conflict)
	}
	for _, grant := range []struct {
		value EffectGrant
		kind  EffectKind
	}{{p.Authorization, PublicationEffect}, {p.PinCreationAuthorization, PinCreationEffect}} {
		g := grant.value
		if g.Kind != grant.kind || g.RootID != s.Boot.IdentityRoot.ID || g.AuthorizationID == "" || g.Version == "" || !slices.Contains(s.Effects, g) {
			return refuse("publication_grant_mismatch", "publication", Conflict)
		}
	}
	return nil
}

func planPublication(p PlannedWorkspace, observations map[string]RootObservation) error {
	r := p.spec.Publication
	if r == nil {
		return nil
	}
	if r.Control != p.resources.LockRoot || r.Control.Path != p.resources.LockNamespace {
		return refuse("publication_control_mismatch", "publication", Conflict)
	}
	control := observations[r.Control.ID]
	if !control.Exists || !control.Directory || control.CanonicalPath != r.Control.Path {
		return refuse("publication_control_unknown", "publication", Unsupported)
	}
	for _, root := range p.roots {
		if root.ID == r.Control.ID || within(root.Path, r.Control.Path) || within(r.Control.Path, root.Path) {
			return refuse("publication_control_overlap", "publication", Conflict)
		}
	}
	for _, root := range []RootRef{p.spec.Boot.IdentityRoot, p.spec.Boot.Current, p.spec.Boot.Candidate, r.Aside} {
		o := observations[root.ID]
		if o.CanonicalPath != root.Path {
			return refuse("publication_noncanonical_layout", "publication", Conflict)
		}
	}
	parent, current, aside := observations[p.spec.Boot.IdentityRoot.ID], observations[p.spec.Boot.Current.ID], observations[r.Aside.ID]
	if !parent.Exists || !parent.Directory {
		return refuse("publication_parent_unknown", "publication", Unsupported)
	}
	if aside.Exists {
		return refuse("publication_aside_occupied", "publication", Conflict)
	}
	if current.Exists && (p.spec.Boot.ExpectedGeneration == "" || current.Manifest == nil || current.Manifest.Generation != p.spec.Boot.ExpectedGeneration) || !current.Exists && p.spec.Boot.ExpectedGeneration != "" {
		return refuse("publication_current_unpinned", "publication", Conflict)
	}
	return nil
}

// No complete native metadata admission producer exists yet. Explicit
// publication must refuse before even an artifact-only mutation, receipt or
// native reservation. Preserve already trusted obligations without inspection,
// replay, callbacks or refreshing frozen authority into success.
func unavailablePublication(p PlannedWorkspace) (ApplyResult, error) {
	result := ApplyResult{Status: Unsupported, Diagnostics: p.Diagnostics(), Receipt: Receipt{SchemaVersion: SchemaVersion, OperationID: p.spec.OperationID, InputDigest: p.digest, IdentityKey: p.spec.Identity.EncodedKey, Identity: p.spec.Identity, Phase: Planned}}
	if err := carryRecovery(&result, p, p.observed); err != nil {
		result.Status = Conflict
		return result, err
	}
	if len(result.Retained) > 0 || len(result.Obligations) > 0 {
		result.Status = Partial
	}
	return result, refuse("publication_metadata_unavailable", "publication", Unsupported)
}
