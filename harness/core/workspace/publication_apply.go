package workspace

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"

	"github.com/hollis-labs/substrate/harness/workspace/materialize"
	"github.com/hollis-labs/substrate/harness/workspace/publication"
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

// The private adapter is only accounting/validation, never a writer, native
// capability producer or use admission. Public Materialize cannot reach it
// while aggregate native preflight is unavailable. Its sole Record is the
// original root ReceiptStore; no leaf journal store or cleanup callback exists.
type publicationReceiptHost struct {
	plan   PlannedWorkspace
	ports  Ports
	result *ApplyResult
	// The caller owns result. Callbacks may mutate it, so only this detached
	// admitted ledger supplies durable and returned accounting.
	accounting ApplyResult
	control    publication.Root
	last       publication.Journal
}

func newPublicationReceiptHost(p PlannedWorkspace, ports Ports, result *ApplyResult, journal publication.Journal, held []HeldLock) (*publicationReceiptHost, error) {
	if !p.valid || p.spec.Publication == nil || result == nil || !result.ArtifactsComplete() || ports.Host == nil || ports.Observations == nil || ports.Clock == nil || len(held) != len(p.locks) || slices.ContainsFunc(held, func(lock HeldLock) bool { return lock == nil }) {
		return nil, refuse("publication_adapter_admission", "publication", Unsupported)
	}
	admitted := result.Clone()
	// ControlRoot is also a callback. Restore admitted accounting on constructor
	// failure as well as success; never snapshot caller mutations after it.
	defer func() { *result = admitted.Clone() }()
	store, ok := ports.ReceiptStore.(ControlledReceiptStore)
	if !ok || store.ControlRoot() != p.spec.Publication.Control {
		return nil, refuse("publication_control_mismatch", "publication", Conflict)
	}
	j := journal.Clone()
	if j.Validate() != nil || len(j.Events) != 3 || j.Events[2].Phase != publication.ReservationHeld || j.Origin.OperationID != p.spec.OperationID || j.Origin.InputDigest != p.digest || j.Origin.IdentityKey != p.spec.Identity.EncodedKey || j.Origin.AgentURN != p.spec.Identity.AgentURN || j.JournalID != p.spec.Publication.JournalID || j.Use.ReservationID != p.spec.Publication.ReservationID || j.Use.Namespace != p.resources.LockNamespace || j.Use.CanonicalID != p.spec.Boot.IdentityRoot.Path || j.Layout.CandidateGeneration != p.digest || !journalRootMatches(j.Control, p.spec.Publication.Control) || !journalRootMatches(j.Layout.Parent, p.spec.Boot.IdentityRoot) || !journalRootMatches(j.Layout.Current, p.spec.Boot.Current) || !journalRootMatches(j.Layout.Candidate, p.spec.Boot.Candidate) || !journalRootMatches(j.Layout.Aside, p.spec.Publication.Aside) {
		return nil, refuse("publication_adapter_binding", "publication", Conflict)
	}
	if admitted.Receipt.SchemaVersion != SchemaVersion || admitted.Receipt.OperationID != p.spec.OperationID || admitted.Receipt.InputDigest != p.digest || admitted.Receipt.IdentityKey != p.spec.Identity.EncodedKey {
		return nil, refuse("publication_adapter_binding", "publication", Conflict)
	}
	return &publicationReceiptHost{plan: p, ports: ports, result: result, accounting: admitted, control: j.Control, last: j}, nil
}

// Each publication entry returns detached owned accounting, including failed
// callback/Record/cancellation paths. No field is refreshed from caller state.
func (h *publicationReceiptHost) returnAccounting() { *h.result = h.accounting.Clone() }

func journalRootMatches(j publication.Root, r RootRef) bool {
	return j.ID == r.ID && j.Path == r.Path && j.Owner == r.Owner && j.Provenance == r.Provenance
}
func (h *publicationReceiptHost) PublicationControl() publication.Root { return h.control }
func (h *publicationReceiptHost) validateJournal(j publication.Journal) error {
	if j.Validate() != nil || !publicationPinsEqual(j, h.last) {
		return refuse("publication_adapter_binding", "publication", Conflict)
	}
	return nil
}
func publicationPinsEqual(a, b publication.Journal) bool {
	return a.Version == b.Version && a.JournalID == b.JournalID && a.Control == b.Control && a.Origin == b.Origin && a.Layout == b.Layout && a.Use == b.Use
}

func (h *publicationReceiptHost) ValidatePublication(ctx context.Context, req materialize.PublishRequest) error {
	defer h.returnAccounting()
	if ctx == nil {
		return refuse("publication_context_required", "publication", Unsupported)
	}
	if err := h.validateJournal(req.Journal); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := h.ports.Host.Validate(ctx, copyRecord(h.plan.spec), copyRecord(h.plan.resources)); err != nil {
		return err
	}
	live, err := h.ports.Observations.Observe(ctx, copyRecord(h.plan.resources))
	if err != nil {
		return err
	}
	if err = validateLive(h.plan, live, h.ports.Clock.Now()); err != nil {
		return err
	}
	// Re-read actual configured control AFTER all user callbacks, not a cached
	// tuple. The concrete engine follows this with independent native checks.
	store, ok := h.ports.ReceiptStore.(ControlledReceiptStore)
	if !ok || store.ControlRoot() != h.plan.spec.Publication.Control {
		return refuse("publication_control_mismatch", "publication", Conflict)
	}
	return ctx.Err()
}

func (h *publicationReceiptHost) RecordPublication(ctx context.Context, j publication.Journal) error {
	defer h.returnAccounting()
	if err := h.validateJournal(j); err != nil {
		return err
	}
	if len(j.Events) != len(h.last.Events)+1 || !reflect.DeepEqual(j.Events[:len(h.last.Events)], h.last.Events) {
		return refuse("publication_journal_replay", "publication", Conflict)
	}
	if err := h.ValidatePublication(ctx, materialize.PublishRequest{Journal: j}); err != nil {
		return err
	}
	// Invalidate any prior artifact proof before intent accounting. Failures
	// retain all roots and original obligations, never seal completion.
	h.accounting.artifactsComplete = false
	h.accounting.launchComplete = false
	h.accounting.Status = Partial
	h.accounting.Receipt.Phase = Interrupted
	for _, root := range []RootRef{h.plan.spec.Boot.Current, h.plan.spec.Boot.Candidate, h.plan.spec.Publication.Aside} {
		h.accounting.Retained = appendRoot(h.accounting.Retained, root)
	}
	appendObligation(&h.accounting, Obligation{Kind: RecoveryInspectionRequired, RootID: h.plan.spec.Boot.IdentityRoot.ID, Code: "publication_observation_required"})
	owned := j.Clone()
	h.accounting.Receipt.PublicationJournal = &owned
	h.accounting.Receipt.Obligations = slices.Clone(h.accounting.Obligations)
	// No clock callback after the last validation and before this Record; event
	// sequence/origin supplies ordering, timestamps confer no authority.
	if err := h.ports.ReceiptStore.Record(ctx, copyRecord(h.accounting.Receipt)); err != nil {
		return err
	}
	h.last = j.Clone()
	return h.ValidatePublication(ctx, materialize.PublishRequest{Journal: j})
}

func admitPublicationOrigins(r Receipt) ([]PublicationOrigin, error) {
	out := copyRecord(r.PublicationOrigins)
	if r.PublicationJournal != nil {
		out = append(out, PublicationOrigin{SchemaVersion: r.SchemaVersion, OperationID: r.OperationID, InputDigest: r.InputDigest, IdentityKey: r.IdentityKey, Journal: r.PublicationJournal.Clone()})
	}
	operations := map[string]string{}
	for _, origin := range out {
		j := origin.Journal
		if origin.SchemaVersion != SchemaVersion || origin.IdentityKey != r.IdentityKey || j.Validate() != nil || j.Origin.OperationID != origin.OperationID || j.Origin.InputDigest != origin.InputDigest || j.Origin.IdentityKey != origin.IdentityKey {
			return nil, refuse("publication_origin_mismatch", "publication", Conflict)
		}
		if digest, exists := operations[origin.OperationID]; exists && digest != origin.InputDigest {
			return nil, refuse("publication_origin_mismatch", "publication", Conflict)
		}
		operations[origin.OperationID] = origin.InputDigest
	}
	return out, nil
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
