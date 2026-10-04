package workspace

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"path/filepath"
	"slices"

	layout "github.com/hollis-labs/substrate/harness/adapters/layout/plan"
	"github.com/hollis-labs/substrate/harness/sandbox"
	"github.com/hollis-labs/substrate/harness/workspace/materialize"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
)

type ActionKind string

const (
	EnsureDirectoryAction ActionKind = "ensure_owned_directory"
	TreeAction            ActionKind = "managed_tree"
	DeferredAction        ActionKind = "deferred_effect"
)

// Action describes an explicit apply step. Requests are observations-dependent;
// Materialize must validate disk and authority again before using one.
type Action struct {
	Kind                 ActionKind
	Root                 RootRef
	RootMode             fs.FileMode
	CanonicalPath        string
	Grant                EffectGrant
	Request              materialize.Request
	RequiredCapabilities []Capability
}

// PlannedWorkspace is immutable through its public API. A zero value is not a
// plan, and none of its fields constitute a launch-ready capability.
type PlannedWorkspace struct {
	spec              Spec
	content           []renderSnapshot
	renderRoots       map[layout.Root]string
	resources         Resources
	observed          Observations
	digest            string
	roots             []RootRef
	actions           []Action
	bindings          []LaunchBinding
	access            []AccessRef
	locks             []LockKey
	diagnostics       []Diagnostic
	renderDiagnostics []RenderDiagnostic
	effectInputs      EffectInputs
	valid             bool
}

func (p PlannedWorkspace) Valid() bool { return p.valid }

func (p PlannedWorkspace) EffectInputs() EffectInputs { return copyRecord(p.effectInputs) }

func (p PlannedWorkspace) Digest() string            { return p.digest }
func (p PlannedWorkspace) Roots() []RootRef          { return slices.Clone(p.roots) }
func (p PlannedWorkspace) LockKeys() []LockKey       { return slices.Clone(p.locks) }
func (p PlannedWorkspace) Diagnostics() []Diagnostic { return slices.Clone(p.diagnostics) }
func (p PlannedWorkspace) RenderDiagnostics() []RenderDiagnostic {
	return slices.Clone(p.renderDiagnostics)
}
func (p PlannedWorkspace) Access() []AccessRef { return copyRecord(p.access) }
func (p PlannedWorkspace) Bindings() []LaunchBinding {
	out := copyRecord(p.bindings)
	if out == nil {
		out = []LaunchBinding{}
	}
	for i := range out {
		if out[i].Environment == nil {
			out[i].Environment = map[string]string{}
		}
		if out[i].RPCProject == nil {
			out[i].RPCProject = map[string]string{}
		}
		if out[i].Argv == nil {
			out[i].Argv = []string{}
		}
	}
	return out
}
func (p PlannedWorkspace) Actions() []Action {
	out := copyRecord(p.actions)
	if out == nil {
		out = []Action{}
	}
	for i := range out {
		out[i].Request.Artifacts.Entries = artifact.CloneEntries(p.actions[i].Request.Artifacts.Entries)
		if out[i].Request.Artifacts.Entries == nil {
			out[i].Request.Artifacts.Entries = []artifact.Entry{}
		}
		if out[i].Request.Selection.Groups == nil {
			out[i].Request.Selection.Groups = []string{}
		}
		if out[i].Request.Selection.EntryIDs == nil {
			out[i].Request.Selection.EntryIDs = []string{}
		}
		if out[i].RequiredCapabilities == nil {
			out[i].RequiredCapabilities = []Capability{}
		}
	}
	return out
}

// Plan performs no I/O, clock reads, ID generation or engine calls. Physical
// root identities and disk evidence come only from explicit observations;
// successful planning promises neither apply success nor launch readiness.
func Plan(spec Spec, content ResolvedContent, resources Resources, observed Observations) (PlannedWorkspace, error) {
	if err := validateFrozenValues(spec, content, resources, observed); err != nil {
		return PlannedWorkspace{}, err
	}
	if err := spec.Validate(); err != nil {
		return PlannedWorkspace{}, err
	}
	if spec.Operation == Recover || spec.Operation == Retire {
		return PlannedWorkspace{}, refuse(CodeOperationDeferred, "operation", Unsupported)
	}
	if observed.At.IsZero() || !observed.ExpiresAt.After(observed.At) || observed.ExpiresAt.Sub(observed.At) > MaxObservationWindow || observed.FenceVersion != spec.Identity.Fence.Revision {
		return PlannedWorkspace{}, refuse(CodeInvalidObservationWindow, "observations", Conflict)
	}
	if _, err := json.Marshal(struct {
		Spec      Spec
		Content   ResolvedContent
		Resources Resources
		Observed  Observations
	}{spec, content, resources, observed}); err != nil {
		return PlannedWorkspace{}, refuse(CodeInvalidFrozenInput, "spec", Conflict)
	}
	spec, resources = canonicalInputs(spec, resources)
	p := PlannedWorkspace{spec: spec, resources: resources, observed: copyRecord(observed), renderRoots: copyRecord(content.Roots)}
	p.observed.At = p.observed.At.UTC()
	p.observed.ExpiresAt = p.observed.ExpiresAt.UTC()
	for _, c := range append(slices.Clone(resources.Capabilities), observed.Capabilities...) {
		if !validCapability(c) {
			return PlannedWorkspace{}, refuse(CodeUnsupportedCapability, "capabilities", Unsupported)
		}
	}
	var err error
	p.roots, err = resolvedRoots(spec, resources)
	if err != nil {
		return PlannedWorkspace{}, err
	}
	obs := map[string]RootObservation{}
	for _, o := range p.observed.Roots {
		if _, dup := obs[o.RootID]; dup {
			return PlannedWorkspace{}, refuse(CodeDuplicateRootObservation, "roots", Conflict)
		}
		obs[o.RootID] = o
	}
	for _, credential := range spec.Credentials {
		if _, ok := findRoot(p.roots, credential.DestinationRootID); !ok {
			return PlannedWorkspace{}, refuse(CodeUnknownCredentialRoot, "credentials", Conflict)
		}
	}
	physical := map[string]string{}
	canonicalBases := map[string]string{}
	for _, rootID := range spec.Cleanup.OwnedRoots {
		if _, ok := findRoot(p.roots, rootID); !ok {
			return PlannedWorkspace{}, refuse(CodeInvalidCleanupReference, "cleanup", Conflict)
		}
	}
	for rootID := range spec.Cleanup.ExpectedGenerations {
		if _, ok := findRoot(p.roots, rootID); !ok {
			return PlannedWorkspace{}, refuse(CodeInvalidCleanupReference, "cleanup", Conflict)
		}
	}
	for _, cwd := range []string{spec.CWD.Child, spec.CWD.ProtocolProject} {
		if cwd != "" {
			found := false
			for _, root := range p.roots {
				if root.Path == cwd {
					found = true
				}
			}
			if !found {
				return PlannedWorkspace{}, refuse(CodeUnresolvedCwd, "cwd", Conflict)
			}
		}
	}
	for _, access := range spec.ExtraDirs {
		r, ok := findRoot(p.roots, access.Resource.ID)
		if !ok || r.Path != access.Resource.Path || r.Provenance != access.Resource.Provenance {
			return PlannedWorkspace{}, refuse(CodeUnresolvedAccessResource, "access", Conflict)
		}
	}
	for _, r := range p.roots {
		o, ok := obs[r.ID]
		if !ok || o.Uncertainty != "" || !cleanAbsolute(o.CanonicalPath) || !cleanAbsolute(o.CanonicalBase) || !within(o.CanonicalBase, o.CanonicalPath) {
			return PlannedWorkspace{}, refuse(CodeUnknownCanonicalRoot, "roots", Unsupported)
		}
		if o.DeclaredPath != r.Path {
			return PlannedWorkspace{}, refuse(CodeObservationRootMismatch, "roots", Conflict)
		}
		declaredRel, _ := filepath.Rel(r.AllowedBase, r.Path)
		canonicalRel, _ := filepath.Rel(o.CanonicalBase, o.CanonicalPath)
		if filepath.Dir(o.CanonicalBase) == o.CanonicalBase || o.CanonicalBase == o.CanonicalPath || declaredRel != canonicalRel {
			return PlannedWorkspace{}, refuse(CodeObservationBaseMismatch, "roots", Conflict)
		}
		if prior, ok := canonicalBases[r.AllowedBase]; ok && prior != o.CanonicalBase {
			return PlannedWorkspace{}, refuse(CodeAmbiguousCanonicalBase, "roots", Conflict)
		}
		canonicalBases[r.AllowedBase] = o.CanonicalBase
		if o.Owner != r.Owner {
			return PlannedWorkspace{}, refuse(CodeRootOwnerMismatch, "roots", Conflict)
		}
		if id, ok := physical[o.CanonicalPath]; ok && id != r.ID {
			return PlannedWorkspace{}, refuse(CodeAmbiguousRootAlias, "roots", Conflict)
		}
		physical[o.CanonicalPath] = r.ID
		if o.Exists && o.Empty && o.Manifest != nil {
			return PlannedWorkspace{}, refuse(CodeInconsistentRootObservation, "roots", Conflict)
		}
		if !o.Exists && (o.Empty || o.Manifest != nil || len(o.Disk) > 0) {
			return PlannedWorkspace{}, refuse(CodeInconsistentRootObservation, "roots", Conflict)
		}
		if o.Exists && !o.Directory {
			return PlannedWorkspace{}, refuse(CodeRootNotDirectory, "roots", Conflict)
		}
		if o.Manifest != nil {
			if err := ValidateManagedManifest(*o.Manifest, credentialDestinations(spec, r.ID)); err != nil {
				return PlannedWorkspace{}, err
			}
		}
	}
	if spec.Boot.ExpectedGeneration != "" {
		current := obs[spec.Boot.Current.ID]
		if current.Manifest == nil || current.Manifest.Generation != spec.Boot.ExpectedGeneration {
			return PlannedWorkspace{}, refuse(CodeStaleCurrentGeneration, "boot", Conflict)
		}
	}
	if err := resources.LockRoot.Validate(); err != nil {
		return PlannedWorkspace{}, err
	}
	lockObservation, ok := obs[resources.LockRoot.ID]
	if !ok || resources.LockRoot.Path != resources.LockNamespace || lockObservation.DeclaredPath != resources.LockRoot.Path || lockObservation.Owner != resources.LockRoot.Owner {
		return PlannedWorkspace{}, refuse(CodeUnknownLockNamespace, "locks", Unsupported)
	}
	declaredLockRel, _ := filepath.Rel(resources.LockRoot.AllowedBase, resources.LockRoot.Path)
	canonicalLockRel, _ := filepath.Rel(lockObservation.CanonicalBase, lockObservation.CanonicalPath)
	if !cleanAbsolute(lockObservation.CanonicalBase) || filepath.Dir(lockObservation.CanonicalBase) == lockObservation.CanonicalBase || lockObservation.CanonicalPath == lockObservation.CanonicalBase || declaredLockRel != canonicalLockRel {
		return PlannedWorkspace{}, refuse(CodeObservationBaseMismatch, "locks", Conflict)
	}
	if base, ok := canonicalBases[resources.LockRoot.AllowedBase]; ok && base != lockObservation.CanonicalBase {
		return PlannedWorkspace{}, refuse(CodeAmbiguousCanonicalBase, "locks", Conflict)
	}
	for _, cap := range append(slices.Clone(spec.Sandbox.RequiredCapabilities), spec.Cleanup.RequiredProofs...) {
		if !slices.Contains(resources.Capabilities, cap) || !slices.Contains(observed.Capabilities, cap) {
			return PlannedWorkspace{}, refuse(CodeRequiredCapabilityUnavailable, "capabilities", Unsupported)
		}
	}
	// One lock covers the stable identity boot parent and all sibling candidates,
	// so changing a generation path never changes the cooperating lock identity.
	current, candidate, parent := obs[spec.Boot.Current.ID], obs[spec.Boot.Candidate.ID], obs[spec.Boot.IdentityRoot.ID]
	if filepath.Dir(current.CanonicalPath) != parent.CanonicalPath || filepath.Dir(candidate.CanonicalPath) != parent.CanonicalPath || current.CanonicalPath == candidate.CanonicalPath {
		return PlannedWorkspace{}, refuse(CodeInvalidBootSiblings, "boot", Conflict)
	}
	mutationRoots := []RootRef{spec.Home.Root, spec.Boot.IdentityRoot}
	lockObs := slices.Clone(p.observed.Roots)
	for _, s := range spec.Scratch {
		mutationRoots = append(mutationRoots, s.Root)
	}
	// Complete effect roots are sorted outer-first, then covered once.
	effectRoots := []RootRef{}
	seenEffect := map[string]bool{}
	for _, g := range spec.Effects {
		key := string(g.Kind) + "\x00" + g.RootID
		if seenEffect[key] {
			return PlannedWorkspace{}, refuse(CodeDuplicateEffectIntent, "effects", Conflict)
		}
		seenEffect[key] = true
		if !slices.Contains(resources.Grants, g) {
			return PlannedWorkspace{}, refuse(CodeMissingEffectGrant, "effects", Conflict)
		}
		r, ok := findRoot(p.roots, g.RootID)
		if !ok {
			return PlannedWorkspace{}, refuse(CodeMissingEffectRoot, "effects", Conflict)
		}
		if g.Kind != ArtifactEffect && g.Kind != DirectoryEffect {
			effectRoots = append(effectRoots, r)
			p.diagnostics = append(p.diagnostics, Diagnostic{Code: "host_effects_pending", RootID: r.ID, Concern: string(g.Kind), Status: Unsupported})
		}
	}
	slices.SortFunc(effectRoots, func(a, b RootRef) int { return cmp.Compare(obs[a.ID].CanonicalPath+"/", obs[b.ID].CanonicalPath+"/") })
	for _, r := range effectRoots {
		covered := false
		for _, m := range mutationRoots {
			if m.Owner == r.Owner && within(obs[m.ID].CanonicalPath, obs[r.ID].CanonicalPath) {
				covered = true
				break
			}
		}
		if !covered {
			mutationRoots = append(mutationRoots, r)
		}
	}
	if err := repositoryInputs(p, obs); err != nil {
		return PlannedWorkspace{}, err
	}
	p.locks, err = OrderedLockKeys(resources.LockNamespace, mutationRoots, lockObs)
	if err != nil {
		return PlannedWorkspace{}, err
	}
	p.locks, err = repositoryLockKeys(p)
	if err != nil {
		return PlannedWorkspace{}, err
	}
	if spec.Operation != Install {
		p.actions = append(p.actions, Action{Kind: EnsureDirectoryAction, Root: spec.Home.Root, RootMode: 0700, RequiredCapabilities: []Capability{CanonicalRoots, MutationLocks}}, Action{Kind: EnsureDirectoryAction, Root: spec.Boot.IdentityRoot, RootMode: 0700, RequiredCapabilities: []Capability{CanonicalRoots, MutationLocks}})
	}
	for _, s := range spec.Scratch {
		p.actions = append(p.actions, Action{Kind: EnsureDirectoryAction, Root: s.Root, RootMode: 0700, RequiredCapabilities: []Capability{CanonicalRoots, MutationLocks}})
	}
	p.access = copyRecord(spec.ExtraDirs)
	p.access = append(p.access, AccessRef{Resource: ResourceRef{ID: spec.Home.Root.ID, Path: spec.Home.Root.Path, Provenance: spec.Home.Root.Provenance}, Access: []sandbox.AccessKind{sandbox.AccessWrite}, Purpose: "identity home"})
	for _, s := range spec.Scratch {
		p.access = append(p.access, AccessRef{Resource: ResourceRef{ID: s.Root.ID, Path: s.Root.Path, Provenance: s.Root.Provenance}, Access: []sandbox.AccessKind{sandbox.AccessWrite}, Purpose: "session scratch"})
	}
	for _, r := range spec.ProviderState {
		p.access = append(p.access, AccessRef{Resource: r, Access: []sandbox.AccessKind{sandbox.AccessRead}, Purpose: "provider state"})
	}
	seenTargets := map[string]bool{}
	for _, root := range []RootRef{spec.Boot.IdentityRoot, spec.Boot.Candidate} {
		p.access = append(p.access, AccessRef{Resource: ResourceRef{ID: root.ID, Path: root.Path, Provenance: root.Provenance}, Access: []sandbox.AccessKind{sandbox.AccessWrite}, Purpose: "boot preparation"})
	}
	p.access = append(p.access, AccessRef{Resource: ResourceRef{ID: spec.Boot.Current.ID, Path: spec.Boot.Current.Path, Provenance: spec.Boot.Current.Provenance}, Access: []sandbox.AccessKind{sandbox.AccessRead}, Purpose: "current generation inspection"})
	for _, credential := range spec.Credentials {
		p.access = append(p.access, AccessRef{Resource: credential.Source, Access: []sandbox.AccessKind{sandbox.AccessSourceRead}, Purpose: "credential binding source"})
	}
	for _, rendered := range content.Rendered {
		r, err := snapshotRendered(rendered)
		if err != nil {
			return PlannedWorkspace{}, err
		}
		root := spec.Boot.Candidate
		if r.Root == layout.RootHome {
			root = spec.Home.Root
			if spec.Operation != Install {
				return PlannedWorkspace{}, refuse(CodeInstalledOperationRequired, "render", Unsupported)
			}
		} else if r.Root != layout.RootBoot {
			return PlannedWorkspace{}, refuse(CodeUnsupportedRenderRoot, "render", Unsupported)
		}
		if spec.Operation == Install && r.Root != layout.RootHome {
			return PlannedWorkspace{}, refuse(CodeInstalledRenderRequired, "render", Unsupported)
		}
		if seenTargets[root.ID] {
			return PlannedWorkspace{}, refuse(CodeDuplicateRenderTarget, "render", Conflict)
		}
		seenTargets[root.ID] = true
		if err := validateRenderRoots(r, p.renderRoots, root, p.roots); err != nil {
			return PlannedWorkspace{}, err
		}
		if r.Root == layout.RootBoot && r.RootMode != 0700 {
			return PlannedWorkspace{}, refuse(CodeUnsafeBootRootMode, "render", Conflict)
		}
		if r.Root == layout.RootHome && r.RootMode != 0 {
			return PlannedWorkspace{}, refuse(CodeInstalledRootModeChange, "render", Conflict)
		}
		destinations := credentialDestinations(spec, root.ID)
		for _, prep := range r.Preparations {
			if prep.Destination != "" {
				destinations = append(destinations, prep.Destination)
			}
		}
		for _, e := range r.Effects {
			destinations = append(destinations, e.Path)
			p.diagnostics = append(p.diagnostics, Diagnostic{Code: "credential_effect_pending", RootID: root.ID, Concern: string(e.Field), Status: Unsupported})
		}
		if err := validateManagedEntries(r.Tree.Entries, destinations); err != nil {
			return PlannedWorkspace{}, err
		}
		entries := r.Tree.Entries
		p.content = append(p.content, r)
		p.bindings = append(p.bindings, copyRecord(r.Binding))
		p.renderDiagnostics = append(p.renderDiagnostics, r.Diagnostics...)
		for _, prep := range r.Preparations {
			p.diagnostics = append(p.diagnostics, Diagnostic{Code: "preparation_pending", RootID: root.ID, Concern: prep.Kind, Status: Unsupported})
		}
		if len(entries) == 0 {
			continue
		} // No engine request or manifest for an empty tree.
		if r.Root == layout.RootHome {
			p.actions = append(p.actions, Action{Kind: DeferredAction, Root: root, Request: materialize.Request{Artifacts: r.Tree}, RequiredCapabilities: []Capability{InstalledMerge}})
			p.diagnostics = append(p.diagnostics, Diagnostic{Code: "installed_apply_pending", RootID: root.ID, Status: Unsupported})
			continue
		}
		o := obs[root.ID]
		request := materialize.Request{TargetRoot: o.CanonicalPath, Artifacts: r.Tree, Operation: materialize.OperationCreate, ExistingTarget: materialize.ExistingTargetRefuse, Selection: copyRecord(spec.Boot.Selection), Reconcile: materialize.ReconcilePolicy{Conflict: materialize.ConflictReport}, Roots: materialize.TargetRoots{BootRoot: o.CanonicalPath, StateRoot: obs[spec.Home.Root.ID].CanonicalPath}}
		if o.Exists {
			if o.Empty && o.Manifest == nil {
				request.ExistingTarget = materialize.ExistingTargetAllowEmpty
			} else {
				if o.Manifest == nil {
					return PlannedWorkspace{}, refuse(CodeMissingCommittedManifest, "candidate", Conflict)
				}
				if spec.Boot.CandidateGeneration == "" || o.Manifest.Generation != spec.Boot.CandidateGeneration {
					return PlannedWorkspace{}, refuse(CodeStaleCandidateGeneration, "candidate", Conflict)
				}
				request.Operation = materialize.OperationReconcile
				request.CurrentManifest = copyRecord(o.Manifest)
				request.ExpectedGeneration = spec.Boot.CandidateGeneration
			}
		}
		p.actions = append(p.actions, Action{Kind: TreeAction, Root: root, RootMode: r.RootMode, Request: request, RequiredCapabilities: []Capability{CanonicalRoots, MutationLocks}})
	}
	for _, credential := range spec.Credentials {
		// Optional requests still need an explicit outcome; omission cannot look
		// like a handled link. No handler has earned completion at this stage.
		p.diagnostics = append(p.diagnostics, Diagnostic{Code: "credential_effect_pending", RootID: credential.DestinationRootID, Concern: credential.Concern, Status: Unsupported})
	}
	if len(spec.Repos) > 0 || len(spec.Trust) > 0 || spec.Sandbox.Policy.Mode == sandbox.ConfinementRequired {
		p.diagnostics = append(p.diagnostics, Diagnostic{Code: "host_effects_pending", Concern: "effects", Status: Unsupported})
	}
	if spec.Operation == Install {
		p.diagnostics = append(p.diagnostics, Diagnostic{Code: "installed_apply_pending", Concern: "installed", Status: Unsupported})
	}
	p.diagnostics = append(p.diagnostics, Diagnostic{Code: string(LaunchReservationPending), Concern: "launch", Status: Partial})
	for i := range p.actions {
		action := &p.actions[i]
		action.CanonicalPath = obs[action.Root.ID].CanonicalPath
		kind := DirectoryEffect
		if action.Kind == TreeAction || (action.Kind == DeferredAction && len(action.Request.Artifacts.Entries) > 0) {
			kind = ArtifactEffect
		} else if action.Kind != EnsureDirectoryAction {
			continue
		}
		for _, g := range spec.Effects {
			if g.Kind == kind && g.RootID == action.Root.ID {
				action.Grant = g
			}
		}
		if !hasGrant(spec.Effects, resources.Grants, kind, action.Root.ID) {
			return PlannedWorkspace{}, refuse(CodeMissingEffectGrant, "effects", Conflict)
		}
		if action.Kind == DeferredAction {
			continue
		}
		for _, capability := range action.RequiredCapabilities {
			if !slices.Contains(resources.Capabilities, capability) || !slices.Contains(observed.Capabilities, capability) {
				return PlannedWorkspace{}, refuse(CodeRequiredCapabilityUnavailable, "capabilities", Unsupported)
			}
		}
	}
	for i := range p.access {
		if root, ok := findRoot(p.roots, p.access[i].Resource.ID); ok && p.access[i].Resource.Path == root.Path {
			p.access[i].Resource.Path = obs[root.ID].CanonicalPath
		}
	}
	slices.SortFunc(p.actions, func(a, b Action) int {
		if n := cmp.Compare(a.CanonicalPath, b.CanonicalPath); n != 0 {
			return n
		}
		return cmp.Compare(a.Kind, b.Kind)
	})
	// Set-valued resources are canonicalized; argv and other ordered inputs are
	// retained in order. Evidence timestamps/disk snapshots are not desired input.
	p.resources.Roots = slices.Clone(p.roots)
	slices.Sort(p.resources.Capabilities)
	slices.SortFunc(p.resources.Grants, compareEffectGrants)
	canonicalRoots := make([]RootObservation, 0, len(p.roots))
	for _, root := range p.roots {
		o := obs[root.ID]
		canonicalRoots = append(canonicalRoots, RootObservation{RootID: o.RootID, DeclaredPath: o.DeclaredPath, CanonicalPath: o.CanonicalPath, CanonicalBase: o.CanonicalBase, Owner: o.Owner})
	}
	encoded, err := json.Marshal(struct {
		Spec           Spec
		Content        []renderSnapshot
		RenderRoots    map[layout.Root]string
		Resources      Resources
		Locks          []LockKey
		CanonicalRoots []RootObservation
	}{p.spec, p.content, p.renderRoots, p.resources, p.locks, canonicalRoots})
	if err != nil {
		return PlannedWorkspace{}, refuse(CodeInvalidFrozenInput, "spec", Conflict)
	}
	encoded = append([]byte(DigestVersion+"\x00"), encoded...)
	sum := sha256.Sum256(encoded)
	p.digest = hex.EncodeToString(sum[:])
	if err := freezeEffects(&p, obs); err != nil {
		return PlannedWorkspace{}, err
	}
	for _, receipt := range observed.Receipts {
		if receipt.OperationID == spec.OperationID {
			if receipt.SchemaVersion != SchemaVersion || receipt.IdentityKey != spec.Identity.EncodedKey {
				return PlannedWorkspace{}, refuse(CodeInvalidOperationReceipt, "receipt", Conflict)
			}
			if receipt.InputDigest != p.digest {
				return PlannedWorkspace{}, refuse(CodeOperationIdReused, "receipt", Conflict)
			}
		}
	}
	for i := range p.actions {
		if p.actions[i].Kind == TreeAction {
			p.actions[i].Request.Generation = p.digest
		}
	}
	p.valid = true
	return p, nil
}

func resolvedRoots(spec Spec, resources Resources) ([]RootRef, error) {
	all := slices.Clone(resources.Roots)
	required := []RootRef{spec.Home.Root, spec.Boot.IdentityRoot, spec.Boot.Current, spec.Boot.Candidate}
	for _, s := range spec.Scratch {
		required = append(required, s.Root)
	}
	for _, repo := range spec.Repos {
		required = append(required, repo.DesiredRoot)
	}
	byID := map[string]RootRef{}
	for _, r := range all {
		if err := r.Validate(); err != nil {
			return nil, err
		}
		if prior, ok := byID[r.ID]; ok && prior != r {
			return nil, refuse(CodeAmbiguousRootReference, "roots", Conflict)
		}
		byID[r.ID] = r
	}
	for _, r := range required {
		if host, ok := byID[r.ID]; !ok || host != r {
			return nil, refuse(CodeMissingHostRoot, "roots", Conflict)
		}
	}
	out := make([]RootRef, 0, len(byID))
	for _, r := range byID {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b RootRef) int { return cmp.Compare(a.ID, b.ID) })
	if _, ok := byID[spec.CWD.RootID]; !ok {
		return nil, refuse(CodeUnknownCwdRoot, "cwd", Conflict)
	}
	return out, nil
}

func findRoot(roots []RootRef, id string) (RootRef, bool) {
	for _, r := range roots {
		if r.ID == id {
			return r, true
		}
	}
	return RootRef{}, false
}
func credentialDestinations(spec Spec, rootID string) []string {
	var out []string
	for _, c := range spec.Credentials {
		if c.DestinationRootID == rootID {
			out = append(out, c.Destination)
		}
	}
	return out
}

// copyRecord is confined to concrete value records containing JSON-compatible
// fields. Artifact byte slices are copied separately to preserve empty files.
func copyRecord[T any](in T) T {
	data, err := json.Marshal(in)
	if err != nil {
		panic("workspace: non-value record")
	}
	var out T
	if json.Unmarshal(data, &out) != nil {
		panic("workspace: invalid value record")
	}
	return out
}

func hasGrant(requested, authorized []EffectGrant, kind EffectKind, rootID string) bool {
	for _, intent := range requested {
		if intent.Kind == kind && intent.RootID == rootID {
			for _, grant := range authorized {
				if grant == intent {
					return true
				}
			}
		}
	}
	return false
}
