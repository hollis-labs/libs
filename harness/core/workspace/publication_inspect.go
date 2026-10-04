package workspace

import (
	"encoding/hex"
	"github.com/hollis-labs/substrate/harness/workspace/publication"
	"slices"
)

// PublicationInspection is detached observational accounting. It never earns
// artifact completion, Ready, a use reservation, or cleanup permission.
type PublicationInspection struct {
	Status           Status
	Code             string
	Journal          publication.Journal
	OriginalReceipts []Receipt
	Retained         []RootRef
	Obligations      []Obligation
}

// InspectPublicationEvidence admits original host-control evidence BEFORE any
// flattening. No disk read, callback, Record, replay, rename or deletion occurs.
// Actual Recover/Retire must still acquire the complete canonical lock union and
// revalidate current authority/custody. Required absence is initially unavailable.
func InspectPublicationEvidence(scope Scope, operation Operation, raw []byte, original []Receipt) (PublicationInspection, error) {
	out := PublicationInspection{Status: Partial, Code: "publication_inspection_only", Retained: scope.Roots()}
	if !scope.valid || operation != Recover && operation != Retire {
		return out, refuse("invalid_publication_inspection", "publication", Conflict)
	}
	if err := validateFrozenValues(original); err != nil {
		return out, err
	}
	j, err := publication.Decode(raw)
	if err != nil {
		out.Code = "publication_journal_unknown"
		out.Obligations = []Obligation{{Kind: RecoveryInspectionRequired, RootID: scope.spec.Boot.IdentityRoot.ID, Code: out.Code}}
		return out, refuse(out.Code, "publication", Unsupported)
	}
	if j.Origin.IdentityKey != scope.spec.Identity.EncodedKey || j.Origin.AgentURN != scope.spec.Identity.AgentURN || j.Layout.Parent.Path != scope.spec.Boot.IdentityRoot.Path || j.Layout.Parent.ID != scope.spec.Boot.IdentityRoot.ID || j.Layout.Current.Path != scope.spec.Boot.Current.Path || j.Layout.Current.ID != scope.spec.Boot.Current.ID || j.Layout.Candidate.Path != scope.spec.Boot.Candidate.Path || j.Layout.Candidate.ID != scope.spec.Boot.Candidate.ID {
		return out, refuse("publication_scope_mismatch", "publication", Conflict)
	}

	for _, pair := range []struct {
		journal publication.Root
		root    RootRef
	}{{j.Layout.Parent, scope.spec.Boot.IdentityRoot}, {j.Layout.Current, scope.spec.Boot.Current}, {j.Layout.Candidate, scope.spec.Boot.Candidate}} {
		if pair.journal.Owner != pair.root.Owner || pair.journal.Provenance != pair.root.Provenance {
			return out, refuse("publication_scope_mismatch", "publication", Conflict)
		}
	}
	if j.Control.ID != scope.control.ID || j.Control.Path != scope.control.Path || j.Control.Owner != scope.control.Owner || j.Control.Provenance != scope.control.Provenance || j.Use.Namespace != scope.resources.LockNamespace {
		return out, refuse("publication_scope_mismatch", "publication", Conflict)
	}
	aside, found := findRoot(scope.roots, j.Layout.Aside.ID)
	if !found || aside.Path != j.Layout.Aside.Path || aside.Owner != j.Layout.Aside.Owner || aside.Provenance != j.Layout.Aside.Provenance {
		return out, refuse("publication_aside_unenrolled", "publication", Conflict)
	}

	// Preserve each receipt with its own originating pins and repository envelope.
	// An aggregate retry operation must not replace any earlier origin header.
	matched := false
	operationDigests := map[string]string{}
	for _, r := range original {
		bytes, digestErr := hex.DecodeString(r.InputDigest)
		if digestErr != nil || len(bytes) != 32 || hex.EncodeToString(bytes) != r.InputDigest {
			return out, refuse("publication_origin_mismatch", "publication", Conflict)
		}
		if prior, ok := operationDigests[r.OperationID]; ok && prior != r.InputDigest {
			return out, refuse("publication_origin_mismatch", "publication", Conflict)
		}
		operationDigests[r.OperationID] = r.InputDigest
		if r.SchemaVersion != SchemaVersion || r.OperationID == "" || r.InputDigest == "" || r.IdentityKey != j.Origin.IdentityKey {
			return out, refuse("publication_origin_mismatch", "publication", Conflict)
		}
		if _, err := admitRepositoryOrigins(r); err != nil {
			return out, err
		}
		switch r.Phase {
		case Planned, Interrupted, ArtifactsCommitted:
		default:
			return out, refuse("publication_receipt_phase", "publication", Conflict)
		}
		if r.OperationID == j.Origin.OperationID {
			if r.InputDigest != j.Origin.InputDigest {
				return out, refuse("publication_origin_mismatch", "publication", Conflict)
			}
			matched = true
		}
		out.OriginalReceipts = append(out.OriginalReceipts, copyRecord(r))
		for _, o := range r.Obligations {
			if !slices.Contains(out.Obligations, o) {
				out.Obligations = append(out.Obligations, o)
			}
		}
	}
	if !matched {
		return out, refuse("publication_origin_missing", "publication", Conflict)
	}
	out.Journal = j.Clone()
	out.Obligations = append(out.Obligations, Obligation{Kind: RecoveryInspectionRequired, RootID: scope.spec.Boot.IdentityRoot.ID, Code: "publication_observation_required"})
	if operation == Retire {
		out.Status = Unsupported
		out.Code = "complete_absence_unavailable"
		return out, refuse(out.Code, "publication", Unsupported)
	}
	return out, nil
}
