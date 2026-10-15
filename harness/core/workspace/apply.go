package workspace

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"reflect"
	"slices"
	"time"

	"github.com/hollis-labs/substrate/harness/workspace/materialize"
)

// Materialize prepares inactive artifacts under the full planned lock set.
// Explicit credential, repository and trust groups are preflighted under all locks before
// mutation and dispatched after verified artifact commit. Unknown/deferred
// effects refuse before mutation. It never publishes current, retires roots or
// issues Ready. Every failure returns observed partial accounting, not rollback.
func Materialize(ctx context.Context, p PlannedWorkspace, ports Ports) (ApplyResult, error) {
	if p.installed != nil && p.valid {
		return applyInstalledWorkspace(ctx, p, ports)
	}
	if !p.valid {
		return ApplyResult{Status: Conflict}, refuse(CodeInvalidPlan, "plan", Conflict)
	}
	if p.spec.Publication != nil {
		return unavailablePublication(p)
	}
	if len(p.spec.Sandbox.RequiredCapabilities) > 0 || len(p.spec.Cleanup.RequiredProofs) > 0 {
		return ApplyResult{Status: Unsupported}, refuse(CodeHostProofsPending, "capabilities", Unsupported)
	}
	for _, d := range p.diagnostics {
		if d.Status == Unsupported {
			return ApplyResult{Status: Unsupported, Diagnostics: p.Diagnostics()}, refuse(d.Code, d.Concern, Unsupported)
		}
	}
	return apply(ctx, p, ports)
}
func apply(ctx context.Context, p PlannedWorkspace, ports Ports) (result ApplyResult, err error) {
	result.Status = Partial
	result.Receipt = Receipt{SchemaVersion: SchemaVersion, OperationID: p.spec.OperationID, InputDigest: p.digest, IdentityKey: p.spec.Identity.EncodedKey, Identity: p.spec.Identity, Phase: Planned}
	for _, root := range p.roots {
		result.Receipt.Roots = append(result.Receipt.Roots, RootReceipt{Root: root})
	}
	result.Diagnostics = p.Diagnostics()
	if ctx == nil || ports.Clock == nil || ports.Host == nil || ports.Locks == nil || ports.Observations == nil || ports.ReceiptStore == nil {
		return result, refuse(CodeMissingApplyPort, "ports", Unsupported)
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	var held []HeldLock
	mutated := false
	defer func() {
		for i := len(held) - 1; i >= 0; i-- {
			if releaseErr := held[i].Release(); releaseErr != nil {
				err = errors.Join(err, releaseErr)
				result.artifactsComplete = false
				result.Diagnostics = append(result.Diagnostics, Diagnostic{Code: "lock_release_failed", Concern: "locks", Status: Partial})
				result.Obligations = append(result.Obligations, Obligation{Kind: RecoveryInspectionRequired})
			}
		}
		if err != nil {
			result.artifactsComplete = false
			if !mutated {
				var refusal *Refusal
				if errors.As(err, &refusal) {
					result.Status = refusal.Status
				}
			}
			if mutated {
				result.Status = Partial
				result.Receipt.Phase = Interrupted
				result.Obligations = append(result.Obligations, Obligation{Kind: RecoveryInspectionRequired})
			}
		}
		result.Receipt.Obligations = slices.Clone(result.Obligations)
	}()
	for _, key := range p.locks {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		var lock HeldLock
		lock, err = ports.Locks.Acquire(ctx, key)
		if err != nil {
			return result, err
		}
		if lock == nil {
			return result, refuse(CodeInvalidHeldLock, "locks", Unsupported)
		}
		held = append(held, lock)
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = ports.Host.Validate(ctx, copyRecord(p.spec), copyRecord(p.resources)); err != nil {
		return result, err
	}
	var live Observations
	live, err = ports.Observations.Observe(ctx, copyRecord(p.resources))
	if err != nil {
		return result, err
	}
	if err = validateLive(p, live, ports.Clock.Now()); err != nil {
		return result, err
	}
	// Independently inspect the concrete filesystem. Host observations supply
	// authority, not permission to substitute an invented committed manifest.
	physical := map[string]RootObservation{}
	for _, r := range append(slices.Clone(p.roots), p.resources.LockRoot) {
		var o RootObservation
		o, err = InspectRoot(r)
		if err != nil {
			return result, err
		}
		physical[r.ID] = o
		var expected RootObservation
		for _, e := range live.Roots {
			if e.RootID == r.ID {
				expected = e
			}
		}
		if o.DeclaredPath != expected.DeclaredPath || o.CanonicalPath != expected.CanonicalPath || o.CanonicalBase != expected.CanonicalBase || o.Exists != expected.Exists || o.Directory != expected.Directory || o.Empty != expected.Empty || !reflect.DeepEqual(o.Manifest, expected.Manifest) {
			return result, refuse(CodeLiveDiskMismatch, "roots", Conflict)
		}
	}
	for _, a := range p.actions {
		if a.Kind == TreeAction {
			if err = validateTarget(a, physical[a.Root.ID], credentialDestinations(p.spec, a.Root.ID)); err != nil {
				return result, err
			}
		}
	}
	if p.spec.Boot.ExpectedGeneration != "" {
		current := physical[p.spec.Boot.Current.ID]
		if current.Manifest == nil || current.Manifest.Generation != p.spec.Boot.ExpectedGeneration {
			return result, refuse(CodeStaleCurrentGeneration, "boot", Conflict)
		}
	}
	if err = carryRecovery(&result, p, live); err != nil {
		return result, err
	}
	var prepared preparedEffects
	prepared, err = preflightEffects(ctx, p, ports, &result)
	if err != nil {
		return result, err
	}
	for _, g := range p.effectInputs.Credentials {
		appendObligation(&result, Obligation{Kind: EffectPending, RootID: g.Candidate.ID, Code: "effect_dispatch_pending:" + p.spec.OperationID})
	}
	for _, r := range p.effectInputs.Repositories {
		appendObligation(&result, Obligation{Kind: EffectPending, RootID: r.Base.ID, Code: "effect_dispatch_pending:" + p.spec.OperationID})
		if !slices.Contains(result.Receipt.RepositoryRequests, r) {
			result.Receipt.RepositoryRequests = append(result.Receipt.RepositoryRequests, r)
		}
	}
	for _, r := range p.effectInputs.Trust {
		appendObligation(&result, Obligation{Kind: EffectPending, RootID: r.Config.ID, Code: "effect_dispatch_pending:" + p.spec.OperationID})
	}
	result.Receipt.Obligations = slices.Clone(result.Obligations)
	result.Receipt.RecordedAt = ports.Clock.Now().UTC()
	if err = ports.ReceiptStore.Record(ctx, copyRecord(result.Receipt)); err != nil {
		return result, err
	}
	// Record interrupted state before the first side effect. A killed process
	// leaves explicit inspection obligations even if its final record is absent.
	result.Receipt.Phase = Interrupted
	result.Receipt.Obligations = append(slices.Clone(result.Obligations), Obligation{Kind: RecoveryInspectionRequired})
	if err = ports.ReceiptStore.Record(ctx, copyRecord(result.Receipt)); err != nil {
		return result, err
	}
	defer func() {
		if err != nil && mutated {
			result.Receipt.Phase = Interrupted
			result.Receipt.Obligations = append(slices.Clone(result.Obligations), Obligation{Kind: RecoveryInspectionRequired})
			result.Receipt.RecordedAt = ports.Clock.Now().UTC()
			recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			defer cancel()
			err = errors.Join(err, ports.ReceiptStore.Record(recordCtx, copyRecord(result.Receipt)))
		}
	}()
	for _, a := range p.actions {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		switch a.Kind {
		case EnsureDirectoryAction:
			if !physical[a.Root.ID].Exists {
				result.Receipt.Roots = setRootReceipt(result.Receipt.Roots, RootReceipt{Root: a.Root})
				mutated = true
				result.Retained = appendRoot(result.Retained, a.Root)
				if err = ports.Host.EnsureOwnedDirectory(ctx, a.Root, a.RootMode); err != nil {
					return result, err
				}
			}
			var info fs.FileInfo
			info, err = os.Lstat(a.Root.Path)
			if err != nil {
				return result, err
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return result, refuse(CodeOwnedDirectoryInvalid, "roots", Partial)
			}
			if !physical[a.Root.ID].Exists && (info.Mode().Perm() != a.RootMode.Perm() || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0) {
				return result, refuse(CodeOwnedDirectoryModeMismatch, "roots", Partial)
			}
			result.Receipt.Roots = setRootReceipt(result.Receipt.Roots, RootReceipt{Root: a.Root, Complete: true})
		case TreeAction:
			result.Receipt.Roots = setRootReceipt(result.Receipt.Roots, RootReceipt{Root: a.Root})
			mutated = true
			result.Retained = appendRoot(result.Retained, a.Root)
			var handle materialize.Handle
			request := a.Request
			// The planned manifest is advisory. Only the independently loaded live
			// manifest may instruct the engine's ownership-sensitive reconciliation.
			request.CurrentManifest = copyRecord(physical[a.Root.ID].Manifest)
			handle, err = materialize.NewEngine(materialize.EngineOptions{Now: ports.Clock.Now}).Apply(ctx, request)
			if err != nil {
				return result, err
			}
			var manifest materialize.Manifest
			manifest, err = verifyHandle(a, handle, credentialDestinations(p.spec, a.Root.ID))
			if err != nil {
				return result, err
			}
			result.Handles = append(result.Handles, handle)
			result.Receipt.Roots = setRootReceipt(result.Receipt.Roots, RootReceipt{Root: a.Root, Generation: manifest.Generation, Complete: true})
		default:
			return result, refuse(CodeDeferredApplyAction, "effects", Unsupported)
		}
	}
	result.Obligations = append(result.Obligations, Obligation{Kind: LaunchReservationPending})
	result.Receipt.Phase = ArtifactsCommitted
	result.Receipt.Obligations = slices.Clone(result.Obligations)
	result.Receipt.RecordedAt = ports.Clock.Now().UTC()
	if err = ports.ReceiptStore.Record(ctx, copyRecord(result.Receipt)); err != nil {
		return result, err
	}
	if err = applyEffects(ctx, p, ports, prepared, &result); err != nil {
		return result, err
	}
	if len(p.effectInputs.Credentials)+len(p.effectInputs.Repositories)+len(p.effectInputs.Trust) > 0 {
		result.Obligations = slices.DeleteFunc(result.Obligations, func(o Obligation) bool {
			return o.Kind == EffectPending && o.Code == "effect_dispatch_pending:"+p.spec.OperationID
		})
		result.Receipt.Phase = ArtifactsCommitted
		result.Receipt.Obligations = slices.Clone(result.Obligations)
		result.Receipt.RecordedAt = ports.Clock.Now().UTC()
		if err = ports.ReceiptStore.Record(ctx, copyRecord(result.Receipt)); err != nil {
			return result, err
		}
	}
	result.artifactsComplete = true
	result.artifactSeal, result.artifactsComplete = resultSeal(result)
	return result, nil
}
func appendRoot(roots []RootRef, r RootRef) []RootRef {
	for _, prior := range roots {
		if prior.ID == r.ID {
			return roots
		}
	}
	return append(roots, r)
}
func validateTarget(a Action, o RootObservation, destinations []string) error {
	// Existing roots retain their host-declared permissions. In particular,
	// Create/AllowEmpty must not let the engine chmod a pre-existing root.
	if o.Exists {
		info, err := os.Lstat(a.Root.Path)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode().Perm() != a.RootMode.Perm() || info.Mode()&(os.ModeSymlink|os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return refuse(CodeUnsafeCandidateMode, "candidate", Conflict)
		}
	}
	if o.Manifest != nil {
		if err := ValidateManagedManifest(*o.Manifest, destinations); err != nil {
			return err
		}
	}
	switch a.Request.Operation {
	case materialize.OperationCreate:
		if o.Exists && !(o.Empty && o.Manifest == nil && a.Request.ExistingTarget == materialize.ExistingTargetAllowEmpty) {
			return refuse(CodeCandidateChanged, "candidate", Conflict)
		}
	case materialize.OperationReconcile, materialize.OperationRefresh:
		if err := validateExistingPaths(a); err != nil {
			return err
		}
		if o.Manifest == nil || o.Manifest.Generation != a.Request.ExpectedGeneration || !reflect.DeepEqual(o.Manifest, a.Request.CurrentManifest) {
			return refuse(CodeStaleCandidateGeneration, "candidate", Conflict)
		}
	default:
		return refuse(CodeUnsupportedEngineOperation, "artifacts", Unsupported)
	}
	return nil
}
func validateLive(p PlannedWorkspace, o Observations, now time.Time) error {
	if err := validateFrozenValues(o); err != nil {
		return err
	}
	if now.Before(o.At) || !now.Before(o.ExpiresAt) || now.Before(p.observed.At) || !now.Before(p.observed.ExpiresAt) || !o.ExpiresAt.After(o.At) {
		return refuse(CodeExpiredObservation, "observations", Conflict)
	}
	if p.spec.Identity.AgentURN != "" && o.FenceVersion != p.spec.Identity.Fence.Revision {
		return refuse(CodeStaleIdentityFence, "identity", Conflict)
	}
	seen := map[string]bool{}
	for _, live := range o.Roots {
		if seen[live.RootID] {
			return refuse(CodeDuplicateRootObservation, "roots", Conflict)
		}
		seen[live.RootID] = true
	}
	for _, r := range append(slices.Clone(p.roots), p.resources.LockRoot) {
		var old, live *RootObservation
		for i := range p.observed.Roots {
			if p.observed.Roots[i].RootID == r.ID {
				old = &p.observed.Roots[i]
			}
		}
		for i := range o.Roots {
			if o.Roots[i].RootID == r.ID {
				live = &o.Roots[i]
			}
		}
		if old == nil || live == nil || live.Uncertainty != "" || live.DeclaredPath != r.Path || live.CanonicalPath != old.CanonicalPath || live.CanonicalBase != old.CanonicalBase || live.Owner != r.Owner {
			return refuse(CodeCanonicalRootChanged, "roots", Conflict)
		}
		if r.ID == p.resources.LockRoot.ID && (!live.Exists || !live.Directory) {
			return refuse(CodeUnknownLockNamespace, "locks", Unsupported)
		}
	}
	for _, a := range p.actions {
		for _, c := range a.RequiredCapabilities {
			if !slices.Contains(o.Capabilities, c) {
				return refuse(CodeRequiredCapabilityUnavailable, "capabilities", Unsupported)
			}
		}
	}
	for _, cap := range []struct {
		enabled    bool
		capability Capability
	}{{len(p.effectInputs.Credentials) > 0, CredentialLinks}, {len(p.effectInputs.Trust) > 0, TrustHandling}, {len(p.effectInputs.Repositories) > 0, RepositoryAttachments}} {
		if cap.enabled && !slices.Contains(o.Capabilities, cap.capability) {
			return refuse(CodeRequiredCapabilityUnavailable, "effects", Unsupported)
		}
	}
	for _, r := range o.Receipts {
		if r.OperationID == p.spec.OperationID && (r.SchemaVersion != SchemaVersion || r.IdentityKey != p.spec.Identity.EncodedKey || r.InputDigest != p.digest) {
			return refuse(CodeOperationIdReused, "receipt", Conflict)
		}
	}
	return nil
}

func setRootReceipt(roots []RootReceipt, receipt RootReceipt) []RootReceipt {
	for i := range roots {
		if roots[i].Root.ID == receipt.Root.ID {
			roots[i] = receipt
			return roots
		}
	}
	return append(roots, receipt)
}
