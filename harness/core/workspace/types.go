package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io/fs"
	"time"

	"github.com/hollis-labs/substrate/harness/interception/permission"
	"github.com/hollis-labs/substrate/harness/sandbox"
	"github.com/hollis-labs/substrate/harness/workspace/credentials"
	"github.com/hollis-labs/substrate/harness/workspace/effects"
	"github.com/hollis-labs/substrate/harness/workspace/install"
	"github.com/hollis-labs/substrate/harness/workspace/materialize"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
	"github.com/hollis-labs/substrate/harness/workspace/repositories"
	"github.com/hollis-labs/substrate/harness/workspace/trust"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
)

const SchemaVersion = "workspace.v1"

type Status string

const (
	Ready       Status = "ready"
	Partial     Status = "partial"
	Conflict    Status = "conflict"
	Unsupported Status = "unsupported"
)

type Operation string

const (
	Prepare Operation = "prepare"
	Resume  Operation = "resume"
	Install Operation = "install"
	Recover Operation = "recover"
	Retire  Operation = "retire"
)

type Continuity string

const (
	Durable   Continuity = "durable"
	Ephemeral Continuity = "ephemeral"
)

type HomeLayout string

const (
	FullHome  HomeLayout = "full"
	LightHome HomeLayout = "light"
)

type RetentionPolicy string

const (
	Keep              RetentionPolicy = "keep"
	RetainForRecovery RetentionPolicy = "retain_for_recovery"
	RetireWhenUnused  RetentionPolicy = "retire_when_unused"
)

// RootRef carries host-resolved ownership, never authority inferred from a path.
// Canonical physical identity is supplied separately in RootObservation.
type RootRef struct{ ID, Path, AllowedBase, Owner, Provenance string }
type ResourceRef struct{ ID, Path, Revision, Provenance string }

type IdentitySpec struct {
	AgentURN, EncodedKey, Instance, Session, Assignment, DefinitionRevision string
	SemanticDigest, ArtifactDigest, DependencyDigest                        artifact.Digest
	Fence                                                                   ResourceRef
}

type HomeSpec struct {
	Root       RootRef
	Layout     HomeLayout
	Continuity Continuity
	Retention  RetentionPolicy
}

type BootSpec struct {
	// IdentityRoot is the explicitly owned stable parent of current/candidates
	// and supplies their shared canonical mutation-lock identity.
	IdentityRoot       RootRef
	Current, Candidate RootRef
	RowIDs             []string
	ExpectedGeneration string
	// CandidateGeneration explicitly authorizes reconcile of an inactive owned
	// candidate. It is distinct from the expected stable current generation.
	CandidateGeneration string
	Selection           materialize.Selection
	Reconcile           materialize.ReconcilePolicy
	Retention           RetentionPolicy
}

type ScratchSpec struct {
	Root                RootRef
	Session, Assignment string
	QuotaBytes          int64
	Environment         map[string]string
	Retention           RetentionPolicy
}

type RepoMode string

const (
	Worktree RepoMode = "worktree"
	Checkout RepoMode = "checkout"
	Readonly RepoMode = "readonly"
)

type RepoSpec struct {
	ID                                  string
	Source                              ResourceRef
	DesiredRoot                         RootRef
	Mode                                RepoMode
	BaseRef, BaseCommit, BranchTemplate string
	Existing                            *AttachmentReceipt
	Retention                           RetentionPolicy
}

// AccessRef reuses the sandbox access vocabulary rather than inventing grants.
type AccessRef struct {
	Resource ResourceRef
	Access   []sandbox.AccessKind
	Purpose  string
}

type CWDSpec struct{ RootID, Relative, Child, ProtocolProject string }
type CredentialSpec struct {
	Source                                  ResourceRef
	DestinationRootID, Destination, Concern string
	Required                                bool
	Authorization                           ResourceRef
	Access                                  []sandbox.AccessKind
}
type TrustSpec struct {
	Mechanism     string
	Targets       []ResourceRef
	Required      bool
	Authorization ResourceRef
}
type SandboxSpec struct {
	Policy               sandbox.ResolvedAccessPolicy
	Profile              permission.ProfileBinding
	RequiredCapabilities []Capability
}

type EffectKind string

const (
	DirectoryEffect      EffectKind = "owned_directory"
	ArtifactEffect       EffectKind = "managed_tree"
	CredentialLinkEffect EffectKind = "credential_link"
	TrustEffect          EffectKind = "trust"
	RepositoryEffect     EffectKind = "repository"
	InstalledEffect      EffectKind = "installed_artifacts"
)

type EffectGrant struct {
	Kind                             EffectKind
	RootID, AuthorizationID, Version string
}
type CleanupPolicy struct {
	Retention           RetentionPolicy
	OwnedRoots          []string
	ExpectedGenerations map[string]string
	RequiredProofs      []Capability
}

// Spec is the resolved semantic input. It does not parse an agent definition.
type InstallSpec struct {
	Target, Control RootRef
	Provider        runtimes.ID
	Grants          []install.Grant
}

type Spec struct {
	Installed                  *InstallSpec `json:",omitempty"`
	SchemaVersion, OperationID string
	Operation                  Operation
	Identity                   IdentitySpec
	Home                       HomeSpec
	Boot                       BootSpec
	ProviderState              []ResourceRef
	Scratch                    []ScratchSpec
	Repos                      []RepoSpec
	ExtraDirs                  []AccessRef
	CWD                        CWDSpec
	Trust                      []TrustSpec
	Credentials                []CredentialSpec
	Sandbox                    SandboxSpec
	Effects                    []EffectGrant
	Cleanup                    CleanupPolicy
	// EffectInputs carries resolved host attestations, never ambient discovery.
	// Headers must be empty on input; Plan binds them to its frozen digest.
	EffectInputs EffectInputs
}

type EffectInputs struct {
	Credentials  []credentials.Group
	Repositories []repositories.Request `json:",omitempty"`
	Trust        []trust.Request
}

type Capability string

const (
	CanonicalRoots        Capability = "canonical_roots"
	MutationLocks         Capability = "mutation_locks"
	UseReservation        Capability = "use_reservation"
	CredentialLinks       Capability = "credential_links"
	TrustHandling         Capability = "trust_handling"
	RepositoryAttachments Capability = "repository_attachments"
	SandboxConfinement    Capability = "sandbox_confinement"
	BootPublication       Capability = "boot_publication"
	InstalledMerge        Capability = "installed_merge"
)

type Resources struct {
	Roots         []RootRef
	ProviderHomes []ResourceRef
	Attachments   []AttachmentReceipt
	Grants        []EffectGrant
	Capabilities  []Capability
	LockNamespace string
	LockRoot      RootRef
	// RecoveryReceipts are explicit trusted earlier-operation evidence. Their
	// retained obligations survive a retry; this input does not authorize replay.
	RecoveryReceipts []Receipt
}

// Observations are snapshots, not proof that an apply will succeed. The host
// must refresh them under locks before applying any filesystem effects.
type Observations struct {
	InstalledFiles    []install.FileSnapshot `json:",omitempty"`
	InstalledCaseMode materialize.CaseMode   `json:",omitempty"`
	At, ExpiresAt     time.Time
	Roots             []RootObservation
	Receipts          []Receipt
	Capabilities      []Capability
	FenceVersion      string
}
type RootObservation struct {
	FileIdentity                                              string `json:",omitempty"`
	RootID, DeclaredPath, CanonicalPath, CanonicalBase, Owner string
	Exists, Empty                                             bool
	Directory                                                 bool
	Manifest                                                  *materialize.Manifest
	Disk                                                      []materialize.ManifestEntry
	Uncertainty                                               string
}

type Phase string

const (
	Planned            Phase = "planned"
	ArtifactsCommitted Phase = "artifacts_committed"
	Interrupted        Phase = "interrupted"
)

type ObligationKind string

const (
	LaunchReservationPending   ObligationKind = "launch_reservation_pending"
	RecoveryInspectionRequired ObligationKind = "recovery_inspection_required"
	EffectPending              ObligationKind = "effect_pending"
)

type Obligation struct {
	Kind   ObligationKind
	RootID string
	Code   string
}
type RootReceipt struct {
	Root       RootRef
	Generation string
	Complete   bool
}
type AttachmentReceipt struct {
	ID, RepositoryID, Path, Branch, BaseCommit, Head string
	Mode                                             RepoMode
}
type EffectReceipt struct {
	Kind   EffectKind
	RootID string
	Status Status
}

// RepositoryOrigin preserves independently trusted originating receipt pins and
// their repository members across aggregate retry receipts. It is host evidence,
// not a cryptographic proof or authority to replay an attachment.
type RepositoryOrigin struct {
	SchemaVersion, OperationID, InputDigest, IdentityKey string
	Requests                                             []repositories.Request
	Evidence                                             []effects.Evidence
}

type Receipt struct {
	Installed                                            *install.Evidence `json:",omitempty"`
	SchemaVersion, OperationID, InputDigest, IdentityKey string
	// Identity records originating pins for new-path recovery. Artifact-only
	// receipts leave it empty and make no enrollment or continuity claim.
	Identity    IdentitySpec
	Phase       Phase
	Roots       []RootReceipt
	Attachments []AttachmentReceipt
	Effects     []EffectReceipt
	// RepositoryRequests preserves original trusted operation bindings for
	// observational recovery; it never authorizes recreation or retirement.
	RepositoryRequests []repositories.Request `json:",omitempty"`
	RepositoryOrigins  []RepositoryOrigin     `json:",omitempty"`
	EffectEvidence     []effects.Evidence
	Obligations        []Obligation
	RecordedAt         time.Time
}

type Diagnostic struct {
	Code, RootID, Concern, Reason string
	Status                        Status
}

// ApplyResult never derives completion from caller-supplied receipt fields.
type ApplyResult struct {
	Status            Status
	Receipt           Receipt
	Handles           []materialize.Handle
	Diagnostics       []Diagnostic
	Retained          []RootRef
	Obligations       []Obligation
	artifactsComplete bool
	artifactSeal      [32]byte
	launchComplete    bool
	launchSeal        [32]byte
}

// Clone detaches public evidence while preserving only proofs already earned
// by this result. It cannot confer completion on a caller-built result.
func (r ApplyResult) Clone() ApplyResult {
	out := copyRecord(r)
	out.artifactsComplete, out.artifactSeal = r.artifactsComplete, r.artifactSeal
	out.launchComplete, out.launchSeal = r.launchComplete, r.launchSeal
	return out
}

// ArtifactsComplete proves only managed-artifact application. It is deliberately
// false for a hand-built result, including one with a committed-looking receipt.
func (r ApplyResult) ArtifactsComplete() bool {
	seal, ok := resultSeal(r)
	return ok && r.artifactsComplete && r.Status == Partial && r.Receipt.Phase == ArtifactsCommitted && r.artifactSeal == seal
}

// LaunchReady requires an earned, unchanged result. Artifact-only application
// never mints this proof; a caller-assigned Ready status cannot supply it.
func (r ApplyResult) LaunchReady() bool {
	seal, ok := resultSeal(r)
	return ok && r.launchComplete && r.Status == Ready && r.launchSeal == seal
}

func resultSeal(r ApplyResult) ([32]byte, bool) {
	encoded, err := json.Marshal(r)
	if err != nil {
		return [32]byte{}, false
	}
	return sha256.Sum256(append([]byte("workspace.apply.result.v1\x00"), encoded...)), true
}

// Ports contains no arbitrary managed-file writer. The concrete materialize
// engine owns that edge. These contracts are host authority and observation.
type Ports struct {
	Clock        Clock
	IDs          IDs
	Host         Host
	Locks        Locks
	Observations Observer
	ReceiptStore ReceiptStore
	Credentials  credentials.LinkPort
	Trust        trust.Port
	Repositories repositories.Port
}
type Clock interface{ Now() time.Time }
type IDs interface {
	NewID(context.Context) (string, error)
}
type Host interface {
	Validate(context.Context, Spec, Resources) error
	EnsureOwnedDirectory(context.Context, RootRef, fs.FileMode) error
}
type Locks interface {
	Acquire(context.Context, LockKey) (HeldLock, error)
}
type HeldLock interface{ Release() error }
type Observer interface {
	Observe(context.Context, Resources) (Observations, error)
}

// ReceiptStore persists control records outside every mutable workspace root.
// Successful Record must durably persist the supplied record before returning.
// Record must reject an operation ID bound to a different input digest. Records
// carry paths, identities and digests; credential bytes never belong here.
type ReceiptStore interface {
	Record(context.Context, Receipt) error
}

// ControlledReceiptStore exposes the same durable store's configured custody.
// Installed operations require this view; it is not a second store or writer.
type ControlledReceiptStore interface {
	ReceiptStore
	ControlRoot() RootRef
}
