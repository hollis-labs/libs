// Package effects defines neutral host-effect evidence and the receipt view
// provided by the workspace owner. Evidence comes from a trusted host; binding
// checks do not provide cryptographic authenticity or launch readiness.
package effects

import "context"

const SchemaVersion = "effects.v1"

type Kind string

const CredentialLinks Kind = "credential_links"
const Trust Kind = "trust"

type Phase string

const (
	PreflightPhase   Phase = "preflight"
	IntentPhase      Phase = "intent"
	LinkIntentPhase  Phase = "link_intent"
	LinkCreatedPhase Phase = "link_created"
	CompletePhase    Phase = "complete"
	InterruptedPhase Phase = "interrupted"
)

type Outcome string

const (
	Applied        Outcome = "applied"
	AlreadyPresent Outcome = "already_present"
	Refused        Outcome = "refused"
	Unsupported    Outcome = "unsupported"
	Partial        Outcome = "partial"
	Conflict       Outcome = "conflict"
	Omitted        Outcome = "omitted"
	Pending        Outcome = "pending"
)

type Header struct {
	Version, OperationID, InputDigest string
}

func (h Header) Valid() bool {
	return h.Version == SchemaVersion && h.OperationID != "" && h.InputDigest != ""
}

type RootInput struct {
	ID, Path, AllowedBase, Owner, Provenance string
	// MutationIdentity is the stable canonical parent covered by HeldLocks.
	MutationIdentity string
	// These are explicit trusted host attestations, never inferred authority.
	Inactive, PrivateCustody bool
}

type LockIdentity struct{ Namespace, CanonicalID string }

type LinkEvidence struct {
	Source, Destination, Target, ParentIdentity, LinkIdentity string
	AuthorizationID, AuthorizationVersion                     string
	Created                                                   bool
	Outcome                                                   Outcome
}

type Evidence struct {
	Header  Header
	Kind    Kind
	RootID  string
	Phase   Phase
	Outcome Outcome
	Links   []LinkEvidence
	Trust   []TrustEvidence
}

// TrustEvidence contains only the authorized target and nonsecret trust state.
type TrustEvidence struct {
	Mechanism, Target, ConfigRootID, AuthorizationID, AuthorizationVersion string
	Present                                                                bool
	Outcome                                                                Outcome
}

func (e Evidence) Clone() Evidence {
	e.Links = append([]LinkEvidence(nil), e.Links...)
	e.Trust = append([]TrustEvidence(nil), e.Trust...)
	return e
}

type Obligation struct{ RootID, Code string }

type ObservationState string

const (
	Before        ObservationState = "before"
	IntendedAfter ObservationState = "intended_after"
	Missing       ObservationState = "missing"
	Divergent     ObservationState = "divergent"
)

type Inspection struct {
	Destination string
	State       ObservationState
}
type Result struct {
	Outcome     Outcome
	Code        string
	Evidence    Evidence
	Obligations []Obligation
	Inspections []Inspection
}

// ReceiptSink is a narrow view over the caller's single operation receipt
// store. Leaves never own another persistence path. Record failures matter.
type ReceiptSink interface {
	Record(context.Context, Evidence) error
}

type PreflightContext struct {
	Header    Header
	HeldLocks []LockIdentity
	// Validate refreshes host authority/fence evidence; it must not mutate.
	Validate func(context.Context) error
}

type ApplyContext struct {
	PreflightContext
	// ArtifactGeneration binds this call to the verified inactive candidate.
	ArtifactGeneration string
	ArtifactRootID     string
	Receipts           ReceiptSink
}
