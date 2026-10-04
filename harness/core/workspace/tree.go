package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"slices"

	"github.com/hollis-labs/substrate/harness/workspace/materialize"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
)

// TreeRequest is an explicitly authorized artifact-only request. It carries no
// enrollment, continuity or launch-readiness claim. Its root must be inactive.
type TreeRequest struct {
	OperationID            string
	Root                   RootRef
	RootMode               fs.FileMode
	Tree                   artifact.Tree
	Resources              Resources
	Observed               Observations
	ExpectedGeneration     string
	Selection              materialize.Selection
	CredentialDestinations []string
}

// ApplyTree is the narrow legacy preparation entry. It preserves the same
// authority, lock, manifest and receipt boundary as Materialize without making
// up an identity or choosing a temporary root for its caller.
func ApplyTree(ctx context.Context, input TreeRequest, ports Ports) (ApplyResult, error) {
	p, err := planTree(input)
	if err != nil {
		var r *Refusal
		result := ApplyResult{Status: Conflict}
		if errors.As(err, &r) {
			result.Status = r.Status
		}
		return result, err
	}
	return apply(ctx, p, ports)
}
func planTree(input TreeRequest) (PlannedWorkspace, error) {
	if err := validateFrozenValues(input); err != nil {
		return PlannedWorkspace{}, err
	}
	input = copyRecord(input)
	_, input.Resources = canonicalInputs(Spec{}, input.Resources)
	if input.OperationID == "" {
		return PlannedWorkspace{}, refuse(CodeMissingOperationId, "spec", Conflict)
	}
	if err := input.Root.Validate(); err != nil {
		return PlannedWorkspace{}, err
	}
	if input.RootMode != 0700 {
		return PlannedWorkspace{}, refuse(CodeUnsafeBootRootMode, "artifacts", Conflict)
	}
	if err := input.Resources.LockRoot.Validate(); err != nil {
		return PlannedWorkspace{}, err
	}
	if input.Resources.LockRoot.Path != input.Resources.LockNamespace {
		return PlannedWorkspace{}, refuse(CodeUnknownLockNamespace, "locks", Unsupported)
	}
	for _, ref := range []RootRef{input.Root, input.Resources.LockRoot} {
		var observation *RootObservation
		for i := range input.Observed.Roots {
			if input.Observed.Roots[i].RootID == ref.ID {
				observation = &input.Observed.Roots[i]
			}
		}
		if observation == nil || observation.DeclaredPath != ref.Path || observation.Owner != ref.Owner {
			return PlannedWorkspace{}, refuse(CodeObservationRootMismatch, "roots", Conflict)
		}
		declaredRel, _ := filepath.Rel(ref.AllowedBase, ref.Path)
		canonicalRel, _ := filepath.Rel(observation.CanonicalBase, observation.CanonicalPath)
		if !cleanAbsolute(observation.CanonicalBase) || filepath.Dir(observation.CanonicalBase) == observation.CanonicalBase || declaredRel != canonicalRel {
			return PlannedWorkspace{}, refuse(CodeObservationBaseMismatch, "roots", Conflict)
		}
		if observation.Exists && (!observation.Directory || observation.Empty && observation.Manifest != nil) || !observation.Exists && (observation.Empty || observation.Manifest != nil || len(observation.Disk) > 0) {
			return PlannedWorkspace{}, refuse(CodeInconsistentRootObservation, "roots", Conflict)
		}
		if observation.Manifest != nil {
			if err := ValidateManagedManifest(*observation.Manifest, input.CredentialDestinations); err != nil {
				return PlannedWorkspace{}, err
			}
		}
	}
	for _, entry := range input.Tree.Entries {
		if entry.Mode&^entry.Mode.Perm() != 0 {
			return PlannedWorkspace{}, refuse(CodeUnsafeArtifactMode, "artifacts", Conflict)
		}
	}
	if err := ValidateManagedTree(input.Tree, input.CredentialDestinations); err != nil {
		return PlannedWorkspace{}, err
	}
	if _, err := json.Marshal(input); err != nil {
		return PlannedWorkspace{}, refuse(CodeInvalidFrozenInput, "artifacts", Conflict)
	}
	rootFound := false
	for _, r := range input.Resources.Roots {
		if r.ID == input.Root.ID {
			if r != input.Root {
				return PlannedWorkspace{}, refuse(CodeAmbiguousRootReference, "roots", Conflict)
			}
			rootFound = true
		}
	}
	if !rootFound {
		return PlannedWorkspace{}, refuse(CodeMissingRootResource, "roots", Conflict)
	}
	if len(input.Resources.Roots) != 1 {
		return PlannedWorkspace{}, refuse(CodeArtifactOnlyExtraRoots, "roots", Conflict)
	}
	// Artifact-only callers explicitly authorize the sole managed-tree effect.
	var grants []EffectGrant
	for _, g := range input.Resources.Grants {
		if g.Kind == ArtifactEffect && g.RootID == input.Root.ID && g.AuthorizationID != "" && g.Version != "" {
			grants = append(grants, g)
		}
	}
	if len(input.Tree.Entries) > 0 && len(grants) != 1 {
		return PlannedWorkspace{}, refuse(CodeMissingEffectGrant, "effects", Conflict)
	}
	keys, err := OrderedLockKeys(input.Resources.LockNamespace, []RootRef{input.Root}, input.Observed.Roots)
	if err != nil {
		return PlannedWorkspace{}, err
	}
	if input.Observed.At.IsZero() || !input.Observed.ExpiresAt.After(input.Observed.At) {
		return PlannedWorkspace{}, refuse(CodeInvalidObservationWindow, "observations", Conflict)
	}
	for _, cap := range []Capability{CanonicalRoots, MutationLocks} {
		if !slices.Contains(input.Resources.Capabilities, cap) || !slices.Contains(input.Observed.Capabilities, cap) {
			return PlannedWorkspace{}, refuse(CodeRequiredCapabilityUnavailable, "capabilities", Unsupported)
		}
	}
	p := PlannedWorkspace{spec: Spec{SchemaVersion: SchemaVersion, OperationID: input.OperationID, Operation: Prepare, Effects: grants}, resources: copyRecord(input.Resources), observed: copyRecord(input.Observed), roots: []RootRef{input.Root}, locks: keys, valid: true}
	// Explicit exclusions enter both the digest and the concrete apply boundary.
	for _, dest := range input.CredentialDestinations {
		p.spec.Credentials = append(p.spec.Credentials, CredentialSpec{DestinationRootID: input.Root.ID, Destination: dest})
	}
	digestInput := input
	digestInput.Observed = Observations{}
	data, err := json.Marshal(struct {
		Input TreeRequest
		Locks []LockKey
	}{digestInput, keys})
	if err != nil {
		return PlannedWorkspace{}, err
	}
	sum := sha256.Sum256(append([]byte("workspace.tree.input.v1\x00"), data...))
	p.digest = hex.EncodeToString(sum[:])
	if len(input.Tree.Entries) == 0 {
		return p, nil
	}
	request := materialize.Request{Operation: materialize.OperationCreate, TargetRoot: input.Root.Path, Artifacts: artifact.Tree{Entries: artifact.CloneEntries(input.Tree.Entries)}, ExistingTarget: materialize.ExistingTargetRefuse, Generation: p.digest, Selection: copyRecord(input.Selection), Reconcile: materialize.ReconcilePolicy{Conflict: materialize.ConflictReport}}
	var observed *RootObservation
	for i := range input.Observed.Roots {
		if input.Observed.Roots[i].RootID == input.Root.ID {
			observed = &input.Observed.Roots[i]
		}
	}
	if observed == nil {
		return PlannedWorkspace{}, refuse(CodeMissingRootObservation, "roots", Conflict)
	}
	request.TargetRoot = observed.CanonicalPath
	if observed.Exists {
		if observed.Empty && observed.Manifest == nil {
			request.ExistingTarget = materialize.ExistingTargetAllowEmpty
		} else {
			if observed.Manifest == nil {
				return PlannedWorkspace{}, refuse(CodeMissingCommittedManifest, "manifest", Conflict)
			}
			if input.ExpectedGeneration == "" || observed.Manifest.Generation != input.ExpectedGeneration {
				return PlannedWorkspace{}, refuse(CodeStaleCandidateGeneration, "artifacts", Conflict)
			}
			request.Operation = materialize.OperationReconcile
			request.CurrentManifest = copyRecord(observed.Manifest)
			request.ExpectedGeneration = input.ExpectedGeneration
		}
	}
	p.actions = []Action{{Kind: TreeAction, Root: input.Root, CanonicalPath: observed.CanonicalPath, Grant: grants[0], RootMode: input.RootMode, Request: request, RequiredCapabilities: []Capability{CanonicalRoots, MutationLocks}}}
	return p, nil
}
