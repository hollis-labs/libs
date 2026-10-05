package workspace

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	layout "github.com/hollis-labs/substrate/harness/adapters/layout/plan"
	"github.com/hollis-labs/substrate/harness/workspace/effects"
	"github.com/hollis-labs/substrate/harness/workspace/install"
	"github.com/hollis-labs/substrate/harness/workspace/materialize"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
	"github.com/hollis-labs/substrate/harness/workspace/render"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"time"
)

// InstalledDigestVersion isolates installed desired-input encoding from boot plans.
const InstalledDigestVersion = "workspace.installed.input.v1"

func validateInstallSpec(s Spec) error {
	i := s.Installed
	if i == nil {
		return refuse("installed_target_required", "installed", Unsupported)
	}
	if s.Home != (HomeSpec{}) || !reflect.DeepEqual(s.Boot, BootSpec{}) || len(s.Scratch)+len(s.Repos)+len(s.Trust)+len(s.Credentials)+len(s.ProviderState)+len(s.ExtraDirs) > 0 || !reflect.DeepEqual(s.EffectInputs, EffectInputs{}) || !reflect.DeepEqual(s.Cleanup, CleanupPolicy{}) || !reflect.DeepEqual(s.Sandbox, SandboxSpec{}) || s.CWD != (CWDSpec{}) {
		return refuse("installed_side_effects_refused", "installed", Unsupported)
	}
	for _, r := range []RootRef{i.Target, i.Control} {
		if err := r.Validate(); err != nil {
			return err
		}
	}
	if within(i.Target.Path, i.Control.Path) || within(i.Control.Path, i.Target.Path) {
		return refuse("installed_control_overlap", "installed", Conflict)
	}
	if i.Target.ID == i.Control.ID {
		return refuse("installed_ambiguous_roots", "installed", Conflict)
	}
	return nil
}
func installRootInput(r RootRef) effects.RootInput {
	return effects.RootInput{ID: r.ID, Path: r.Path, AllowedBase: r.AllowedBase, Owner: r.Owner, Provenance: r.Provenance, MutationIdentity: r.Path}
}

func planInstalledWorkspace(s Spec, c ResolvedContent, r Resources, o Observations) (PlannedWorkspace, error) {
	if err := validateFrozenValues(s, c, r, o); err != nil {
		return PlannedWorkspace{}, err
	}
	// Transport JSON omits zero bytes: detach explicitly before any record copy.
	raw := artifact.Tree{}
	if len(c.Rendered) == 1 {
		raw.Entries = artifact.CloneEntries(c.Rendered[0].Tree.Entries)
	}
	snapshots := make([]install.FileSnapshot, len(o.InstalledFiles))
	copy(snapshots, o.InstalledFiles)
	for j := range snapshots {
		snapshots[j].Bytes = slices.Clone(snapshots[j].Bytes)
	}
	s = copyRecord(s)
	c = copyRecord(c)
	r = copyRecord(r)
	o = copyRecord(o)
	o.InstalledFiles = snapshots
	if len(c.Rendered) == 1 {
		c.Rendered[0].Tree.Entries = raw.Entries
	}
	s, r = canonicalInputs(s, r)
	slices.SortFunc(s.Installed.Grants, func(a, b install.Grant) int { return cmp.Compare(a.Path, b.Path) })
	for j := range s.Installed.Grants {
		slices.SortFunc(s.Installed.Grants[j].KeyPaths, func(a, b []string) int { return slices.Compare(a, b) })
	}
	slices.SortFunc(r.Roots, func(a, b RootRef) int { return cmp.Compare(a.ID, b.ID) })
	slices.SortFunc(o.Roots, func(a, b RootObservation) int { return cmp.Compare(a.RootID, b.RootID) })
	slices.SortFunc(o.InstalledVolumes, func(a, b materialize.InstalledCapabilities) int { return cmp.Compare(a.RootIdentity, b.RootIdentity) })
	slices.Sort(o.Capabilities)
	o.Capabilities = slices.Compact(o.Capabilities)
	slices.SortFunc(o.InstalledFiles, func(a, b install.FileSnapshot) int { return cmp.Compare(a.Path, b.Path) })
	if len(c.Rendered) == 1 {
		var err error
		c.Rendered[0].Tree.Entries, err = artifact.Normalize(c.Rendered[0].Tree.Entries)
		if err != nil {
			return PlannedWorkspace{}, installRefusal(err)
		}
	}
	if err := s.Validate(); err != nil {
		return PlannedWorkspace{}, err
	}
	if o.At.IsZero() || !o.ExpiresAt.After(o.At) || o.ExpiresAt.Sub(o.At) > MaxObservationWindow || o.FenceVersion != s.Identity.Fence.Revision {
		return PlannedWorkspace{}, refuse(CodeInvalidObservationWindow, "observations", Conflict)
	}
	if len(c.Rendered) != 1 || c.Rendered[0].Provider != s.Installed.Provider || len(c.Roots) != 1 || c.Roots[layout.RootHome] != s.Installed.Target.Path {
		return PlannedWorkspace{}, refuse("installed_render_binding", "installed", Conflict)
	}
	if _, err := snapshotRendered(c.Rendered[0]); err != nil {
		return PlannedWorkspace{}, err
	}
	if err := r.LockRoot.Validate(); err != nil {
		return PlannedWorkspace{}, err
	}
	if r.LockNamespace != r.LockRoot.Path {
		return PlannedWorkspace{}, refuse(CodeUnknownLockNamespace, "locks", Unsupported)
	}
	if err := validateInstalledObservations(s, r, o); err != nil {
		return PlannedWorkspace{}, err
	}
	roots := []RootRef{s.Installed.Target, s.Installed.Control}
	if len(r.Roots) != 2 {
		return PlannedWorkspace{}, refuse("installed_roots_mismatch", "installed", Conflict)
	}
	for _, root := range roots {
		if !slices.Contains(r.Roots, root) {
			return PlannedWorkspace{}, refuse("installed_roots_mismatch", "installed", Conflict)
		}
	}
	if len(r.ProviderHomes)+len(r.Attachments) > 0 {
		return PlannedWorkspace{}, refuse("installed_resources_refused", "installed", Unsupported)
	}
	for _, cap := range []Capability{CanonicalRoots, MutationLocks, InstalledMerge} {
		if !slices.Contains(r.Capabilities, cap) || !slices.Contains(o.Capabilities, cap) {
			return PlannedWorkspace{}, refuse("installed_capability_missing", "installed", Unsupported)
		}
	}
	for _, g := range s.Installed.Grants {
		grant := EffectGrant{Kind: InstalledEffect, RootID: s.Installed.Target.ID, AuthorizationID: g.ID, Version: g.Version}
		if !slices.Contains(s.Effects, grant) || !slices.Contains(r.Grants, grant) {
			return PlannedWorkspace{}, refuse(CodeMissingEffectGrant, "installed", Unsupported)
		}
	}
	controlGrant := false
	for _, g := range s.Effects {
		if !validEffect(g.Kind) || g.AuthorizationID == "" || g.Version == "" || !slices.Contains(r.Grants, g) {
			return PlannedWorkspace{}, refuse(CodeInvalidEffectGrant, "installed", Conflict)
		}
		if g.Kind == DirectoryEffect && g.RootID == s.Installed.Control.ID {
			controlGrant = true
			continue
		}
		if g.Kind != InstalledEffect || g.RootID != s.Installed.Target.ID {
			return PlannedWorkspace{}, refuse("installed_extraneous_grant", "installed", Unsupported)
		}
	}
	if !controlGrant {
		return PlannedWorkspace{}, refuse(CodeMissingEffectGrant, "installed_control", Unsupported)
	}
	locks, err := OrderedLockKeys(r.LockNamespace, roots, o.Roots)
	if err != nil {
		return PlannedWorkspace{}, err
	}
	for _, root := range append(slices.Clone(roots), r.LockRoot) {
		found := false
		for _, obs := range o.Roots {
			if obs.RootID == root.ID {
				if found || obs.DeclaredPath != root.Path || obs.CanonicalPath != root.Path || obs.CanonicalBase != root.AllowedBase || obs.Owner != root.Owner || !obs.Exists || !obs.Directory || obs.Uncertainty != "" || obs.FileIdentity == "" {
					return PlannedWorkspace{}, refuse("installed_root_observation", "installed", Conflict)
				}
				found = true
			}
		}
		if !found {
			return PlannedWorkspace{}, refuse("installed_root_observation", "installed", Conflict)
		}
	}
	previous, err := previousInstalled(s, r)
	if err != nil {
		return PlannedWorkspace{}, err
	}
	request := install.Request{Target: installRootInput(s.Installed.Target), Control: installRootInput(s.Installed.Control), Grants: s.Installed.Grants, Previous: previous, Capabilities: installedTargetCapabilities(o, s.Installed.Target.ID), CaseMode: o.InstalledCaseMode}
	if _, err := install.Prepare(request, c.Rendered[0], o.InstalledFiles); err != nil {
		return PlannedWorkspace{}, installRefusal(err)
	}
	digestObserved := copyRecord(o)
	digestObserved.At = time.Time{}
	digestObserved.ExpiresAt = time.Time{}
	encoded, err := json.Marshal(struct {
		Version   string
		Spec      Spec
		Content   ResolvedContent
		Resources Resources
		Observed  Observations
	}{InstalledDigestVersion, s, c, r, digestObserved})
	if err != nil {
		return PlannedWorkspace{}, err
	}
	sum := sha256.Sum256(encoded)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	request.Header = effects.Header{Version: effects.SchemaVersion, OperationID: s.OperationID, InputDigest: digest}
	prepared, err := install.Prepare(request, c.Rendered[0], o.InstalledFiles)
	if err != nil {
		return PlannedWorkspace{}, installRefusal(err)
	}
	p := PlannedWorkspace{spec: s, resources: r, observed: o, digest: digest, roots: roots, locks: locks, valid: true, installed: &prepared, renderRoots: copyRecord(c.Roots)}
	snapshot, err := snapshotRendered(c.Rendered[0])
	if err != nil {
		return PlannedWorkspace{}, err
	}
	p.content = []renderSnapshot{snapshot}
	publicRequest := prepared.EngineRequest()
	publicRequest.Generation = digest
	for _, obs := range o.Roots {
		if obs.RootID == s.Installed.Target.ID {
			publicRequest.Installed.TargetIdentity = obs.FileIdentity
		}
		if obs.RootID == s.Installed.Control.ID {
			publicRequest.Installed.ControlIdentity = obs.FileIdentity
		}
	}
	publicRequest.InstalledOriginalContext = installedOriginalContext(s, r, o, digest, publicRequest)
	p.actions = []Action{{Kind: InstalledTreeAction, Root: s.Installed.Target, CanonicalPath: s.Installed.Target.Path, Request: publicRequest, RequiredCapabilities: []Capability{CanonicalRoots, MutationLocks, InstalledMerge}}}
	return p, nil
}

// installedOriginalContext transports already-frozen comparison data. No
// trusted upstream issuer or credential visibility input exists here. Expected
// stays unknown; capability labels and hashes never supply provenance.
func installedOriginalContext(s Spec, r Resources, o Observations, digest string, req materialize.Request) *materialize.InstalledOriginalContext {
	c := &materialize.InstalledOriginalContext{SchemaVersion: s.SchemaVersion, OperationID: s.OperationID, InputDigest: digest,
		Identity: materialize.InstalledOriginalIdentity{AgentURN: s.Identity.AgentURN, EncodedKey: s.Identity.EncodedKey, Instance: s.Identity.Instance, Session: s.Identity.Session, Assignment: s.Identity.Assignment, DefinitionRevision: s.Identity.DefinitionRevision, SemanticDigest: s.Identity.SemanticDigest, ArtifactDigest: s.Identity.ArtifactDigest, DependencyDigest: s.Identity.DependencyDigest},
		Fence:    materialize.InstalledOriginalFence{ID: s.Identity.Fence.ID, Path: s.Identity.Fence.Path, Revision: s.Identity.Fence.Revision, Provenance: s.Identity.Fence.Provenance}, ObservedAt: o.At, ExpiresAt: o.ExpiresAt}
	root := func(ref RootRef) materialize.InstalledOriginalRoot {
		cap := installedTargetCapabilities(o, ref.ID)
		return materialize.InstalledOriginalRoot{ID: ref.ID, Path: ref.Path, AllowedBase: ref.AllowedBase, Owner: ref.Owner, Provenance: ref.Provenance, Identity: cap.RootIdentity, Volume: cap.Volume, CapabilityRevision: cap.Revision, ObservationRevision: cap.ObservationRevision, Metadata: cap.RootMetadata}
	}
	c.Target, c.Control, c.Lock = root(s.Installed.Target), root(s.Installed.Control), root(r.LockRoot)
	for _, g := range s.Installed.Grants {
		c.Grants = append(c.Grants, materialize.InstalledOriginalGrant{ID: g.ID, Version: g.Version, Path: g.Path, WholeFile: g.WholeFile, KeyPaths: copyRecord(g.KeyPaths)})
	}
	for _, g := range s.Effects {
		c.Authorizations = append(c.Authorizations, materialize.InstalledOriginalAuthorization{Kind: string(g.Kind), RootID: g.RootID, AuthorizationID: g.AuthorizationID, Version: g.Version})
	}
	for _, f := range req.Installed.Files {
		for _, b := range o.InstalledFiles {
			if b.Path == f.Path {
				c.Files = append(c.Files, materialize.InstalledOriginalFile{Path: b.Path, Identity: b.Identity, Exists: b.Exists, Kind: b.Kind, Mode: b.Mode, Digest: b.Digest, Metadata: b.Metadata, Parents: copyRecord(b.Parents)})
				break
			}
		}
	}
	return c
}

func installRefusal(err error) error {
	status := Conflict
	if errors.Is(err, install.ErrUnsupported) || errors.Is(err, materialize.ErrUnsupportedOperation) {
		status = Unsupported
	}
	return refuse("installed_preflight_refused", "installed", status)
}
func previousInstalled(s Spec, r Resources) (*install.Evidence, error) {
	var previous *install.Evidence
	for _, receipt := range r.RecoveryReceipts {
		if receipt.Installed == nil {
			return nil, refuse("installed_foreign_recovery", "installed", Conflict)
		}
		if len(receipt.Roots) != 2 {
			return nil, refuse("installed_origin_roots", "installed", Conflict)
		}
		for _, root := range []RootRef{s.Installed.Target, s.Installed.Control} {
			found := false
			for _, observed := range receipt.Roots {
				if observed.Root == root && observed.Complete {
					if found {
						return nil, refuse("installed_origin_roots", "installed", Conflict)
					}
					found = true
				}
			}
			if !found {
				return nil, refuse("installed_origin_roots", "installed", Conflict)
			}
		}
		e := receipt.Installed
		if receipt.SchemaVersion != SchemaVersion || receipt.IdentityKey != s.Identity.EncodedKey || receipt.Identity.AgentURN != s.Identity.AgentURN || receipt.Identity.EncodedKey != s.Identity.EncodedKey || receipt.Identity.Session != s.Identity.Session || receipt.Identity.Instance != s.Identity.Instance || receipt.Identity.Assignment != s.Identity.Assignment || receipt.Phase != ArtifactsCommitted || receipt.OperationID == "" || receipt.InputDigest == "" || e.Header != (effects.Header{Version: effects.SchemaVersion, OperationID: receipt.OperationID, InputDigest: receipt.InputDigest}) || e.Generation != receipt.InputDigest || e.TargetID != s.Installed.Target.ID || e.CanonicalTarget != s.Installed.Target.Path || e.ControlID != s.Installed.Control.ID || e.CanonicalControl != s.Installed.Control.Path {
			return nil, refuse("installed_origin_mismatch", "installed", Conflict)
		}
		if previous != nil {
			return nil, refuse("installed_ambiguous_previous", "installed", Conflict)
		}
		previous = copyRecord(e)
	}
	return previous, nil
}
func observeInstallRoot(ref RootRef) (RootObservation, error) {
	if err := ref.Validate(); err != nil {
		return RootObservation{}, err
	}
	base, err := filepath.EvalSymlinks(ref.AllowedBase)
	if err != nil {
		return RootObservation{}, refuse(CodeCanonicalBaseUnavailable, "installed", Unsupported)
	}
	canonical, err := canonicalMissing(ref.Path)
	if err != nil || canonical != ref.Path || base != ref.AllowedBase || !within(base, canonical) {
		return RootObservation{}, refuse(CodeNoncanonicalRoot, "installed", Conflict)
	}
	info, err := os.Lstat(ref.Path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return RootObservation{}, refuse(CodeRootNotDirectory, "installed", Conflict)
	}
	return RootObservation{RootID: ref.ID, DeclaredPath: ref.Path, CanonicalPath: canonical, CanonicalBase: base, Owner: ref.Owner, Exists: true, Directory: true, FileIdentity: materialize.InstalledIdentity(info)}, nil
}
func applyInstalledWorkspace(ctx context.Context, p PlannedWorkspace, ports Ports) (result ApplyResult, err error) {
	result.Status = Partial
	result.Receipt = Receipt{SchemaVersion: SchemaVersion, OperationID: p.spec.OperationID, InputDigest: p.digest, IdentityKey: p.spec.Identity.EncodedKey, Identity: p.spec.Identity, Phase: Planned}
	result.Receipt.Roots = []RootReceipt{{Root: p.spec.Installed.Target}, {Root: p.spec.Installed.Control}}
	// Keep trusted prior obligations and roots on this refused new attempt. Older
	// installed evidence remains bound to its original receipt, never reissued.
	for _, receipt := range p.resources.RecoveryReceipts {
		for _, o := range receipt.Obligations {
			appendObligation(&result, o)
		}
		for _, root := range receipt.Roots {
			result.Retained = appendRoot(result.Retained, root.Root)
		}
	}
	// Until a new attempt earns its own durable evidence, return the admitted
	// originating wrapper intact. A refused/cancelled attempt must be retryable
	// without flattening older Installed.Header under this operation's envelope.
	if len(p.resources.RecoveryReceipts) == 1 {
		result.Receipt = copyRecord(p.resources.RecoveryReceipts[0])
	}
	result.Receipt.Obligations = slices.Clone(result.Obligations)
	if ctx == nil || ports.Host == nil || ports.Locks == nil || ports.Clock == nil || ports.Observations == nil || ports.ReceiptStore == nil {
		return result, refuse(CodeMissingApplyPort, "installed", Unsupported)
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if len(p.actions) != 1 || p.actions[0].Request.InstalledOriginalContext == nil || !reflect.DeepEqual(p.actions[0].Request.InstalledOriginalContext, installedOriginalContext(p.spec, p.resources, p.observed, p.digest, p.actions[0].Request)) {
		result.Status = Unsupported
		return result, refuse("installed_original_context_mismatch", "installed", Unsupported)
	}
	// No trusted issuer exists. Refuse before ControlRoot, locks, Clock, Host,
	// Observe, Record or filesystem work. A DTO cannot enable the native path.
	if p.actions[0].Request.InstalledOriginalContext.Expected == (materialize.InstalledExpectedIssuer{}) {
		result.Status = Unsupported
		return result, refuse("installed_metadata_issuer_unknown", "installed", Unsupported)
	}
	store, ok := ports.ReceiptStore.(ControlledReceiptStore)
	if !ok || store.ControlRoot() != p.spec.Installed.Control {
		return result, refuse("installed_control_store_mismatch", "installed", Unsupported)
	}
	var held []HeldLock
	mutated := false
	var validate func() error
	var operationCancel context.CancelFunc
	defer func() {
		if operationCancel != nil {
			defer operationCancel()
		}
		if err != nil && mutated {
			result.Status = Partial
			result.Receipt.Phase = Interrupted
			for j := range result.Receipt.Roots {
				result.Receipt.Roots[j].Complete = false
			}
			appendObligation(&result, Obligation{Kind: RecoveryInspectionRequired, RootID: p.spec.Installed.Target.ID})
			result.Retained = appendRoot(result.Retained, p.spec.Installed.Target)
			result.Receipt.Obligations = slices.Clone(result.Obligations)
			recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			defer cancel()
			// Do not send recovery evidence through a store whose configured or
			// physical custody was lost. Returned trusted obligations still survive.
			if store.ControlRoot() == p.spec.Installed.Control {
				fresh, ce := observeInstallRoot(p.spec.Installed.Control)
				if ce == nil && fresh.FileIdentity == installedTargetCapabilities(p.observed, p.spec.Installed.Control.ID).RootIdentity {
					cap, ce := materialize.InspectInstalledCapabilities(recordCtx, p.spec.Installed.Control.Path, p.observed.InstalledCaseMode)
					if ce == nil && cap == installedTargetCapabilities(p.observed, p.spec.Installed.Control.ID) {
						err = errors.Join(err, ports.ReceiptStore.Record(recordCtx, copyRecord(result.Receipt)))
					}
				}
			}
		}
		for j := len(held) - 1; j >= 0; j-- {
			if e := held[j].Release(); e != nil {
				err = errors.Join(err, e)
				result.artifactsComplete = false
				if mutated {
					result.Retained = appendRoot(result.Retained, p.spec.Installed.Target)
				}
				appendObligation(&result, Obligation{Kind: RecoveryInspectionRequired, Code: "lock_release_failed"})
			}
		}
		if validate != nil {
			ve := validate()
			if ve == nil && result.artifactsComplete {
				ve = verifyInstalledCompletion(ctx, p.spec.Installed.Target.Path, p.content[0].Tree, p.observed.InstalledCaseMode, result.Receipt.Installed, result.Receipt.Installed.Directories)
			}
			if ve != nil {
				err = errors.Join(err, ve)
				result.artifactsComplete = false
				if mutated {
					result.Status = Partial
					result.Retained = appendRoot(result.Retained, p.spec.Installed.Target)
					result.Receipt.Phase = Interrupted
					for j := range result.Receipt.Roots {
						result.Receipt.Roots[j].Complete = false
					}
				}
				appendObligation(&result, Obligation{Kind: RecoveryInspectionRequired, RootID: p.spec.Installed.Target.ID, Code: "installed_final_custody_lost"})
			}
		}
		result.Receipt.Obligations = slices.Clone(result.Obligations)
		if err != nil {
			result.artifactsComplete = false
			if mutated {
				result.Status = Partial
				result.Receipt.Phase = Interrupted
				result.Retained = appendRoot(result.Retained, p.spec.Installed.Target)
				for j := range result.Receipt.Roots {
					result.Receipt.Roots[j].Complete = false
				}
				appendObligation(&result, Obligation{Kind: RecoveryInspectionRequired, RootID: p.spec.Installed.Target.ID})
				result.Receipt.Obligations = slices.Clone(result.Obligations)
			}
			if !mutated {
				var refusal *Refusal
				if errors.As(err, &refusal) {
					result.Status = refusal.Status
				}
			}
		}
	}()
	for _, key := range p.locks {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		var l HeldLock
		l, err = ports.Locks.Acquire(ctx, key)
		if err != nil {
			return result, err
		}
		if l == nil {
			return result, refuse(CodeInvalidHeldLock, "installed", Unsupported)
		}
		held = append(held, l)
	}
	var live Observations
	validate = func() error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if e := validateInstalledFreshness(p, live, ports.Clock.Now()); e != nil {
			return e
		}
		if store.ControlRoot() != p.spec.Installed.Control {
			return refuse("installed_control_store_mismatch", "installed", Conflict)
		}
		if e := ports.Host.Validate(ctx, copyRecord(p.spec), copyRecord(p.resources)); e != nil {
			return e
		}
		// Host validation is an external callback: it may change the configured
		// store as well as physical custody. Never admit its earlier binding.
		if store.ControlRoot() != p.spec.Installed.Control {
			return refuse("installed_control_store_mismatch", "installed", Conflict)
		}
		if e := ctx.Err(); e != nil {
			return e
		}
		if e := validateInstalledFreshness(p, live, ports.Clock.Now()); e != nil {
			return e
		}
		if store.ControlRoot() != p.spec.Installed.Control {
			return refuse("installed_control_store_mismatch", "installed", Conflict)
		}
		for _, root := range append(slices.Clone(p.roots), p.resources.LockRoot) {
			cap, e := materialize.InspectInstalledCapabilities(ctx, root.Path, p.observed.InstalledCaseMode)
			if e != nil {
				return installRefusal(e)
			}
			if cap != installedTargetCapabilities(p.observed, root.ID) {
				return refuse("installed_volume_changed", "installed", Conflict)
			}
			fresh, e := observeInstallRoot(root)
			if e != nil {
				return e
			}
			matched := false
			for _, original := range p.observed.Roots {
				if original.RootID == root.ID {
					matched = fresh.FileIdentity == original.FileIdentity
				}
			}
			if !matched {
				return refuse("installed_root_identity_changed", "installed", Conflict)
			}
			if root != p.spec.Installed.Target {
				info, e := os.Lstat(root.Path)
				if e != nil || info.Mode().Perm() != 0700 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
					return refuse("installed_control_custody", "installed", Conflict)
				}
			}
		}
		return ctx.Err()
	}
	if err = validate(); err != nil {
		return result, err
	}
	live, err = ports.Observations.Observe(ctx, copyRecord(p.resources))
	if err != nil {
		return result, err
	}
	if err = validateFrozenValues(live); err != nil {
		return result, err
	}
	if err = validate(); err != nil {
		return result, err
	}
	if err = validateInstalledObservations(p.spec, p.resources, live); err != nil {
		return result, err
	}
	for _, root := range append(slices.Clone(p.roots), p.resources.LockRoot) {
		var frozen RootObservation
		for _, original := range p.observed.Roots {
			if original.RootID == root.ID {
				frozen = original
			}
		}
		matched := false
		for _, obs := range live.Roots {
			if obs.RootID == root.ID {
				if matched || obs.DeclaredPath != frozen.DeclaredPath || obs.CanonicalPath != frozen.CanonicalPath || obs.CanonicalBase != frozen.CanonicalBase || obs.Owner != frozen.Owner || !obs.Exists || !obs.Directory || obs.Uncertainty != "" || obs.FileIdentity != frozen.FileIdentity {
					return result, refuse("installed_root_observation", "installed", Conflict)
				}
				matched = true
			}
		}
		if !matched {
			return result, refuse("installed_root_observation", "installed", Conflict)
		}
	}
	now := ports.Clock.Now()
	if live.FenceVersion != p.spec.Identity.Fence.Revision || live.At.After(now) || !live.ExpiresAt.After(now) || live.ExpiresAt.Sub(live.At) > MaxObservationWindow || live.InstalledCaseMode != p.observed.InstalledCaseMode {
		return result, refuse(CodeInvalidObservationWindow, "installed", Conflict)
	}
	remaining := min(p.observed.ExpiresAt.Sub(now), live.ExpiresAt.Sub(now))
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, remaining)
	operationCancel = cancel
	for _, cap := range []Capability{CanonicalRoots, MutationLocks, InstalledMerge} {
		if !slices.Contains(live.Capabilities, cap) {
			return result, refuse("installed_capability_missing", "installed", Unsupported)
		}
	}
	renderValue := render.Result{Provider: p.content[0].Provider, Layer: p.content[0].Layer, Mode: p.content[0].Mode, Variant: p.content[0].Variant, Tree: p.content[0].Tree, Root: p.content[0].Root, RootMode: p.content[0].RootMode, Binding: p.content[0].Binding, Effects: p.content[0].Effects}
	snapshots, err := materialize.InspectInstalled(ctx, p.spec.Installed.Target.Path, renderValue.Tree, live.InstalledCaseMode)
	if err != nil {
		return result, installRefusal(err)
	}
	if len(snapshots) != len(p.observed.InstalledFiles) {
		return result, refuse("installed_observation_changed", "installed", Conflict)
	}
	for j := range snapshots {
		if !install.EquivalentSnapshot(snapshots[j], p.observed.InstalledFiles[j]) {
			return result, refuse("installed_observation_changed", "installed", Conflict)
		}
	}
	previous, err := previousInstalled(p.spec, p.resources)
	if err != nil {
		return result, err
	}
	request := install.Request{Header: effects.Header{Version: effects.SchemaVersion, OperationID: p.spec.OperationID, InputDigest: p.digest}, Target: installRootInput(p.spec.Installed.Target), Control: installRootInput(p.spec.Installed.Control), Grants: p.spec.Installed.Grants, Previous: previous, Capabilities: installedTargetCapabilities(live, p.spec.Installed.Target.ID), CaseMode: live.InstalledCaseMode}
	prepared, err := install.Prepare(request, renderValue, snapshots)
	if err != nil {
		return result, installRefusal(err)
	}
	engineRequest := prepared.EngineRequest()
	engineRequest.Generation = p.digest
	engineRequest.InstalledOriginalContext = copyRecord(p.actions[0].Request.InstalledOriginalContext)
	targetInfo, _ := os.Lstat(p.spec.Installed.Target.Path)
	controlInfo, _ := os.Lstat(p.spec.Installed.Control.Path)
	engineRequest.Installed.TargetIdentity = materialize.InstalledIdentity(targetInfo)
	engineRequest.Installed.ControlIdentity = materialize.InstalledIdentity(controlInfo)
	evidence := prepared.Evidence()
	evidence.Generation = p.digest
	result.Receipt = Receipt{SchemaVersion: SchemaVersion, OperationID: p.spec.OperationID, InputDigest: p.digest, IdentityKey: p.spec.Identity.EncodedKey, Identity: p.spec.Identity, Phase: Planned, Roots: []RootReceipt{{Root: p.spec.Installed.Target}, {Root: p.spec.Installed.Control}}}
	result.Receipt.Installed = &evidence
	for _, receipt := range p.resources.RecoveryReceipts {
		for _, o := range receipt.Obligations {
			appendObligation(&result, o)
		}
	}
	result.Receipt.Obligations = slices.Clone(result.Obligations)
	engine := materialize.NewEngine(materialize.EngineOptions{Now: func() time.Time { return now }, BeforeInstalledCommit: func(cctx context.Context, c materialize.InstalledFileChange) error {
		if e := validate(); e != nil {
			return e
		}
		for j := range result.Receipt.Installed.Files {
			if result.Receipt.Installed.Files[j].Path == c.Path {
				result.Receipt.Installed.Files[j].Phase = materialize.InstalledIntent
				result.Receipt.Installed.Stage = c.Stage
			}
		}
		if e := ports.ReceiptStore.Record(cctx, copyRecord(result.Receipt)); e != nil {
			return e
		}
		return validate()
	}})
	if _, err = engine.Plan(ctx, engineRequest); err != nil {
		return result, installRefusal(err)
	}
	if err = validate(); err != nil {
		return result, err
	}
	result.Receipt.RecordedAt = now.UTC()
	if err = ports.ReceiptStore.Record(ctx, copyRecord(result.Receipt)); err != nil {
		return result, err
	}
	if err = validate(); err != nil {
		return result, err
	}
	// Engine stage creation is itself mutation; account conservatively if an
	// apply fails before returning a usable handle.
	var handle materialize.Handle
	handle, err = engine.Apply(ctx, engineRequest)
	mutated = handle.Mutated
	result.Handles = append(result.Handles, handle)
	for _, rel := range handle.Retained {
		if artifact.ValidateRelPath(rel) == nil {
			root := p.spec.Installed.Target
			root.ID += "/" + rel
			root.Path = filepath.Join(root.Path, rel)
			result.Retained = appendRoot(result.Retained, root)
		}
	}
	result.Receipt.Installed.Directories = copyRecord(handle.InstalledDirectories)
	result.Receipt.Installed.Stage = handle.InstalledStage
	for _, c := range handle.Installed {
		for j := range result.Receipt.Installed.Files {
			if c.Path == result.Receipt.Installed.Files[j].Path {
				result.Receipt.Installed.Files[j] = c
			}
		}
	}
	if err != nil {
		return result, err
	}
	if !handle.Report.Complete {
		return result, refuse(CodeIncompleteEngineResult, "installed", Partial)
	}
	after, err := materialize.InspectInstalled(ctx, p.spec.Installed.Target.Path, engineRequest.Artifacts, live.InstalledCaseMode)
	if err != nil {
		return result, err
	}
	for _, c := range result.Receipt.Installed.Files {
		found := false
		for _, s := range after {
			if s.Path == c.Path && s.Exists && s.Digest == c.After && s.Mode == c.AfterMode && s.Metadata == c.AfterMetadata {
				found = true
			}
		}
		if !found || c.Phase != materialize.InstalledVerified {
			return result, refuse(CodeCommittedDigestMismatch, "installed", Partial)
		}
	}
	if err = validate(); err != nil {
		return result, err
	}
	result.Receipt.Phase = ArtifactsCommitted
	result.Receipt.Roots[0].Complete = true
	result.Receipt.Roots[0].Generation = p.digest
	result.Receipt.Roots[1].Complete = true
	appendObligation(&result, Obligation{Kind: LaunchReservationPending, RootID: p.spec.Installed.Target.ID})
	result.Receipt.Obligations = slices.Clone(result.Obligations)
	if err = ports.ReceiptStore.Record(ctx, copyRecord(result.Receipt)); err != nil {
		return result, err
	}
	// The final durable callback can change custody or committed content too.
	if err = validate(); err != nil {
		return result, err
	}
	final, e := materialize.InspectInstalled(ctx, p.spec.Installed.Target.Path, engineRequest.Artifacts, live.InstalledCaseMode)
	if e != nil {
		return result, e
	}
	for _, c := range result.Receipt.Installed.Files {
		found := false
		for _, snapshot := range final {
			if snapshot.Path == c.Path && snapshot.Exists && snapshot.Digest == c.After && snapshot.Mode == c.AfterMode && snapshot.Identity == c.AfterIdentity && snapshot.Metadata == c.AfterMetadata {
				found = true
			}
		}
		if !found {
			return result, refuse(CodeCommittedDigestMismatch, "installed", Partial)
		}
	}
	if err = verifyInstalledDirectories(final, handle.InstalledDirectories); err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	result.artifactsComplete = true
	result.artifactSeal, _ = resultSeal(result)
	return result, nil
}

func verifyInstalledDirectories(snapshots []materialize.InstalledSnapshot, dirs []materialize.InstalledDirectoryChange) error {
	seen := map[string]materialize.InstalledDirectoryChange{}
	for _, s := range snapshots {
		for _, d := range s.Parents {
			seen[d.Path] = d
		}
		if s.Kind == artifact.EntryDirectory {
			seen[s.Path] = materialize.InstalledDirectoryChange{Path: s.Path, Exists: s.Exists, Identity: s.Identity, Mode: s.Mode, Metadata: s.Metadata}
		}
	}
	for _, d := range dirs {
		actual, ok := seen[d.Path]
		if !ok || actual.Exists != d.Exists || actual.Identity != d.Identity || actual.Mode != d.Mode || actual.Metadata != d.Metadata {
			return refuse("installed_directory_changed", "installed", Partial)
		}
	}
	return nil
}

func validateInstalledObservations(s Spec, r Resources, o Observations) error {
	refs := []RootRef{s.Installed.Target, s.Installed.Control, r.LockRoot}
	if len(o.Roots) != len(refs) {
		return refuse("installed_root_observation", "installed", Conflict)
	}
	if len(o.InstalledVolumes) != len(refs) {
		return refuse("installed_volume_capability_missing", "installed", Unsupported)
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		cap := installedTargetCapabilities(o, ref.ID)
		if !cap.Valid() || cap.CaseMode != o.InstalledCaseMode || seen[cap.RootIdentity] {
			return refuse("installed_volume_capability_missing", "installed", Unsupported)
		}
		seen[cap.RootIdentity] = true
	}

	ids := map[string]bool{}
	physical := map[string]bool{}
	for _, root := range refs {
		if ids[root.ID] {
			return refuse(CodeDuplicateRootObservation, "installed", Conflict)
		}
		ids[root.ID] = true
	}
	for _, obs := range o.Roots {
		if !ids[obs.RootID] || obs.FileIdentity == "" || physical[obs.FileIdentity] {
			return refuse("installed_root_observation", "installed", Conflict)
		}
		delete(ids, obs.RootID)
		physical[obs.FileIdentity] = true
	}
	for _, cap := range append(slices.Clone(r.Capabilities), o.Capabilities...) {
		if !validCapability(cap) {
			return refuse(CodeUnsupportedCapability, "installed", Unsupported)
		}
	}
	for _, receipt := range append(slices.Clone(o.Receipts), r.RecoveryReceipts...) {
		if receipt.OperationID == s.OperationID {
			return refuse("operation_id_reused", "installed", Conflict)
		}
	}
	return nil
}

func validateInstalledFreshness(p PlannedWorkspace, live Observations, now time.Time) error {
	if now.Before(p.observed.At) || !now.Before(p.observed.ExpiresAt) {
		return refuse(CodeInvalidObservationWindow, "installed", Conflict)
	}
	if !live.At.IsZero() && (now.Before(live.At) || !now.Before(live.ExpiresAt) || !live.ExpiresAt.After(live.At) || live.ExpiresAt.Sub(live.At) > MaxObservationWindow || live.FenceVersion != p.spec.Identity.Fence.Revision || live.InstalledCaseMode != p.observed.InstalledCaseMode) {
		return refuse(CodeInvalidObservationWindow, "installed", Conflict)
	}
	return nil
}

func installedTargetCapabilities(o Observations, id string) materialize.InstalledCapabilities {
	var identity string
	for _, r := range o.Roots {
		if r.RootID == id {
			identity = r.FileIdentity
		}
	}
	var found materialize.InstalledCapabilities
	for _, c := range o.InstalledVolumes {
		if c.RootIdentity == identity {
			if found.RootIdentity != "" {
				return materialize.InstalledCapabilities{}
			}
			found = c
		}
	}
	return found
}

func verifyInstalledCompletion(ctx context.Context, target string, tree artifact.Tree, mode materialize.CaseMode, evidence *install.Evidence, dirs []materialize.InstalledDirectoryChange) error {
	if evidence == nil {
		return refuse(CodeCommittedDigestMismatch, "installed", Partial)
	}
	final, err := materialize.InspectInstalled(ctx, target, tree, mode)
	if err != nil {
		return err
	}
	for _, c := range evidence.Files {
		found := false
		for _, s := range final {
			if s.Path == c.Path && s.Exists && s.Identity == c.AfterIdentity && s.Digest == c.After && s.Mode == c.AfterMode && s.Metadata == c.AfterMetadata && c.Phase == materialize.InstalledVerified {
				found = true
			}
		}
		if !found {
			return refuse(CodeCommittedDigestMismatch, "installed", Partial)
		}
	}
	if err := verifyInstalledDirectories(final, dirs); err != nil {
		return err
	}
	return ctx.Err()
}
