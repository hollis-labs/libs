package agentlaunch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/hollis-labs/substrate/harness/workspace"
	"github.com/hollis-labs/substrate/harness/workspace/materialize"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
)

// ArtifactAuthority is explicit host authorization for one inactive artifact
// operation. Close releases host-port resources after apply releases all locks.
type ArtifactAuthority struct {
	// Explicit host attestations; neither paths nor AutoPlant flags grant custody.
	Inactive, PrivateCustody bool
	Input                    workspace.TreeRequest
	Ports                    workspace.Ports
	Close                    func() error
}

// ArtifactAuthorizer resolves authority and fresh observations without granting
// it from a pathname. No default implementation discovers a home or identity.
type ArtifactAuthorizer func(context.Context, string) (ArtifactAuthority, error)

type ArtifactMaterializationRequest struct {
	TargetRoot                     string
	Roots                          ExecutionRoots
	Artifacts                      artifact.Tree
	Operation                      materialize.Operation
	Generation, ExpectedGeneration string
	Selection                      materialize.Selection
	Reconcile                      materialize.ReconcilePolicy
	Authorize                      ArtifactAuthorizer
}

// ValidateArtifactAuthority checks explicit host inputs before a caller renders
// provider content. Apply refreshes authority and observations under all locks.
func ValidateArtifactAuthority(ctx context.Context, a ArtifactAuthority, target string) error {
	if ctx == nil {
		return &workspace.Refusal{Code: "missing_artifact_authority", Concern: "authority", Status: workspace.Unsupported}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !a.Inactive || !a.PrivateCustody || a.Input.Root.Path != target || a.Input.OperationID == "" || a.Ports.Clock == nil || a.Ports.Host == nil || a.Ports.Locks == nil || a.Ports.Observations == nil || a.Ports.ReceiptStore == nil {
		return &workspace.Refusal{Code: "invalid_artifact_authority", Concern: "authority", Status: workspace.Unsupported}
	}
	if err := workspace.ValidateTreeAuthority(a.Input); err != nil {
		return errors.Join(&workspace.Refusal{Code: "invalid_artifact_authority", Concern: "authority", Status: workspace.Unsupported}, err)
	}
	now := a.Ports.Clock.Now()
	if now.Before(a.Input.Observed.At) || !now.Before(a.Input.Observed.ExpiresAt) {
		return &workspace.Refusal{Code: "expired_artifact_authority", Concern: "observations", Status: workspace.Unsupported}
	}
	// Read-only preflight prevents rendering for a statically unsafe candidate.
	// Apply repeats physical custody checks under the complete mutation locks.
	if info, err := os.Lstat(target); err == nil {
		if !info.IsDir() || info.Mode().Perm() != 0700 || info.Mode()&(os.ModeSymlink|os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return &workspace.Refusal{Code: "invalid_artifact_custody", Concern: "roots", Status: workspace.Unsupported}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.Join(&workspace.Refusal{Code: "invalid_artifact_custody", Concern: "roots", Status: workspace.Unsupported}, err)
	}
	if err := a.Ports.Host.Validate(ctx, workspace.Spec{SchemaVersion: workspace.SchemaVersion, OperationID: a.Input.OperationID, Operation: workspace.Prepare}, a.Input.Resources); err != nil {
		return errors.Join(&workspace.Refusal{Code: "invalid_artifact_authority", Concern: "authority", Status: workspace.Unsupported}, err)
	}
	return ctx.Err()
}

// MaterializeArtifactsResult returns root evidence and retained obligations on
// failures as well as successes. It never compensates or removes a root.
func MaterializeArtifactsResult(ctx context.Context, req ArtifactMaterializationRequest) (result workspace.ApplyResult, err error) {
	defer func() {
		if err != nil && result.Status == "" {
			result.Status = workspace.Conflict
			var refusal *workspace.Refusal
			if errors.As(err, &refusal) {
				result.Status = refusal.Status
			}
		}
	}()
	if ctx == nil || req.Authorize == nil {
		return result, &workspace.Refusal{Code: "missing_artifact_authority", Concern: "authority", Status: workspace.Unsupported}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !filepath.IsAbs(req.TargetRoot) || filepath.Clean(req.TargetRoot) != req.TargetRoot {
		return result, &workspace.Refusal{Code: "unresolved_artifact_root", Concern: "roots", Status: workspace.Conflict}
	}
	if req.Reconcile.RemoveOwned || (req.Reconcile.Conflict != "" && req.Reconcile.Conflict != materialize.ConflictReport) {
		return result, &workspace.Refusal{Code: "legacy_reconcile_policy_unsupported", Concern: "artifacts", Status: workspace.Unsupported}
	}
	authority, err := req.Authorize(ctx, req.TargetRoot)
	if err != nil {
		return result, err
	}
	if authority.Close != nil {
		defer func() {
			closeErr := authority.Close()
			if closeErr != nil {
				err = errors.Join(err, closeErr)
				result.Status = workspace.Partial
				result.Diagnostics = append(result.Diagnostics, workspace.Diagnostic{Code: "artifact_authority_close_failed", Concern: "ports", Status: workspace.Partial})
				if len(result.Handles) > 0 {
					result.Retained = append(result.Retained, authority.Input.Root)
					result.Obligations = append(result.Obligations, workspace.Obligation{Kind: workspace.RecoveryInspectionRequired, RootID: authority.Input.Root.ID})
				}
			}
		}()
	}
	if err := ValidateArtifactAuthority(ctx, authority, req.TargetRoot); err != nil {
		return result, err
	}
	input := authority.Input
	input.Tree = req.Artifacts
	input.Tree.Entries = artifact.CloneEntries(req.Artifacts.Entries)
	// Stable metadata names this adapter's desired entries, never prior disk content.
	for i := range input.Tree.Entries {
		e := &input.Tree.Entries[i]
		if e.Ownership.EntryID == "" && e.Ownership.GroupID == "" {
			e.Ownership = artifact.Ownership{EntryID: "agentlaunch.artifacts:" + e.Path, GroupID: "agentlaunch.artifacts"}
		}
		if e.Provenance.Source == "" {
			e.Provenance.Source = "agentlaunch.artifacts"
		}
	}
	input.Generation = req.Generation
	input.Operation = req.Operation
	input.TargetRoots = materializeRoots(req.Roots)
	input.Selection = req.Selection
	if req.ExpectedGeneration != "" {
		input.ExpectedGeneration = req.ExpectedGeneration
	}
	result, err = workspace.ApplyTree(ctx, input, authority.Ports)
	if err == nil && !result.ArtifactsComplete() {
		err = &workspace.Refusal{Code: "artifact_completion_unproved", Concern: "artifacts", Status: workspace.Partial}
	}
	return result, err
}

func MaterializeArtifacts(ctx context.Context, req ArtifactMaterializationRequest) (*materialize.Handle, error) {
	result, err := MaterializeArtifactsResult(ctx, req)
	if len(result.Handles) == 0 {
		return nil, err
	}
	handle := slices.Clone(result.Handles)[len(result.Handles)-1]
	return &handle, err
}

func materializeRoots(roots ExecutionRoots) materialize.TargetRoots {
	return materialize.TargetRoots{
		ProjectRoot: roots.ProjectRoot,
		BootRoot:    roots.BootRoot,
		StateRoot:   roots.StateRoot,
		ScratchRoot: roots.ScratchRoot,
	}
}
