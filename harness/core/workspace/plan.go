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
	valid             bool
}

func (p PlannedWorkspace) Digest() string            { return p.digest }
func (p PlannedWorkspace) Roots() []RootRef          { return slices.Clone(p.roots) }
func (p PlannedWorkspace) LockKeys() []LockKey       { return slices.Clone(p.locks) }
func (p PlannedWorkspace) Diagnostics() []Diagnostic { return slices.Clone(p.diagnostics) }
func (p PlannedWorkspace) RenderDiagnostics() []RenderDiagnostic {
	return slices.Clone(p.renderDiagnostics)
}
func (p PlannedWorkspace) Access() []AccessRef       { return copyRecord(p.access) }
func (p PlannedWorkspace) Bindings() []LaunchBinding { return copyRecord(p.bindings) }
func (p PlannedWorkspace) Actions() []Action {
	out := copyRecord(p.actions)
	for i := range out {
		out[i].Request.Artifacts.Entries = artifact.CloneEntries(p.actions[i].Request.Artifacts.Entries)
	}
	return out
}

// Plan performs no I/O, clock reads, ID generation or engine calls. Physical
// root identities and disk evidence come only from explicit observations;
// successful planning promises neither apply success nor launch readiness.
func Plan(spec Spec, content ResolvedContent, resources Resources, observed Observations) (PlannedWorkspace, error) {
	if err := spec.Validate(); err != nil {
		return PlannedWorkspace{}, err
	}
	if spec.Operation == Recover || spec.Operation == Retire {
		return PlannedWorkspace{}, refuse("operation_deferred", "operation", Unsupported)
	}
	if observed.At.IsZero() || !observed.ExpiresAt.After(observed.At) || observed.FenceVersion != spec.Identity.Fence.Revision {
		return PlannedWorkspace{}, refuse("invalid_observation_window", "observations", Conflict)
	}
	if _, err := json.Marshal(struct {
		Spec      Spec
		Content   ResolvedContent
		Resources Resources
		Observed  Observations
	}{spec, content, resources, observed}); err != nil {
		return PlannedWorkspace{}, refuse("invalid_frozen_input", "spec", Conflict)
	}
	p := PlannedWorkspace{spec: copyRecord(spec), resources: copyRecord(resources), observed: copyRecord(observed), renderRoots: copyRecord(content.Roots)}
	p.observed.At = p.observed.At.UTC()
	p.observed.ExpiresAt = p.observed.ExpiresAt.UTC()
	for _, c := range append(slices.Clone(resources.Capabilities), observed.Capabilities...) {
		if !validCapability(c) {
			return PlannedWorkspace{}, refuse("unsupported_capability", "capabilities", Unsupported)
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
			return PlannedWorkspace{}, refuse("duplicate_root_observation", "roots", Conflict)
		}
		obs[o.RootID] = o
	}
	physical := map[string]string{}
	for _, r := range p.roots {
		o, ok := obs[r.ID]
		if !ok || o.Uncertainty != "" || !cleanAbsolute(o.CanonicalPath) || !cleanAbsolute(o.CanonicalBase) || !within(o.CanonicalBase, o.CanonicalPath) {
			return PlannedWorkspace{}, refuse("unknown_canonical_root", "roots", Unsupported)
		}
		if o.Owner != r.Owner {
			return PlannedWorkspace{}, refuse("root_owner_mismatch", "roots", Conflict)
		}
		if id, ok := physical[o.CanonicalPath]; ok && id != r.ID {
			return PlannedWorkspace{}, refuse("ambiguous_root_alias", "roots", Conflict)
		}
		physical[o.CanonicalPath] = r.ID
		if !o.Exists && (o.Empty || o.Manifest != nil || len(o.Disk) > 0) {
			return PlannedWorkspace{}, refuse("inconsistent_root_observation", "roots", Conflict)
		}
		if o.Exists && !o.Directory {
			return PlannedWorkspace{}, refuse("root_not_directory", "roots", Conflict)
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
			return PlannedWorkspace{}, refuse("stale_current_generation", "boot", Conflict)
		}
	}
	// One lock covers the stable identity boot parent and all sibling candidates,
	// so changing a generation path never changes the cooperating lock identity.
	current, candidate, parent := obs[spec.Boot.Current.ID], obs[spec.Boot.Candidate.ID], obs[spec.Boot.IdentityRoot.ID]
	if filepath.Dir(current.CanonicalPath) != parent.CanonicalPath || filepath.Dir(candidate.CanonicalPath) != parent.CanonicalPath || current.CanonicalPath == candidate.CanonicalPath {
		return PlannedWorkspace{}, refuse("invalid_boot_siblings", "boot", Conflict)
	}
	mutationRoots := []RootRef{spec.Home.Root, spec.Boot.IdentityRoot}
	lockObs := slices.Clone(p.observed.Roots)
	for _, s := range spec.Scratch {
		mutationRoots = append(mutationRoots, s.Root)
	}
	// Declared shared mutation resources join the same complete lock set even
	// while their effect handlers are deferred. No late lock acquisition.
	for _, g := range spec.Effects {
		r, ok := findRoot(p.roots, g.RootID)
		if !ok {
			return PlannedWorkspace{}, refuse("missing_effect_root", "effects", Conflict)
		}
		if g.Kind == ArtifactEffect || g.Kind == DirectoryEffect {
			continue
		}
		covered := false
		for _, m := range mutationRoots {
			if m.Owner == r.Owner && within(obs[m.ID].CanonicalPath, obs[r.ID].CanonicalPath) {
				covered = true
			}
		}
		if !covered {
			mutationRoots = append(mutationRoots, r)
		}
	}
	p.locks, err = OrderedLockKeys(resources.LockNamespace, mutationRoots, lockObs)
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
				return PlannedWorkspace{}, refuse("installed_operation_required", "render", Unsupported)
			}
		} else if r.Root != layout.RootBoot {
			return PlannedWorkspace{}, refuse("unsupported_render_root", "render", Unsupported)
		}
		if spec.Operation == Install && r.Root != layout.RootHome {
			return PlannedWorkspace{}, refuse("installed_render_required", "render", Unsupported)
		}
		if seenTargets[root.ID] {
			return PlannedWorkspace{}, refuse("duplicate_render_target", "render", Conflict)
		}
		seenTargets[root.ID] = true
		if err := validateRenderRoots(r, p.renderRoots, root, p.roots); err != nil {
			return PlannedWorkspace{}, err
		}
		if r.Root == layout.RootBoot && r.RootMode != 0700 {
			return PlannedWorkspace{}, refuse("unsafe_boot_root_mode", "render", Conflict)
		}
		if r.Root == layout.RootHome && r.RootMode != 0 {
			return PlannedWorkspace{}, refuse("installed_root_mode_change", "render", Conflict)
		}
		destinations := credentialDestinations(spec, root.ID)
		for _, e := range r.Effects {
			destinations = append(destinations, e.Path)
			p.diagnostics = append(p.diagnostics, Diagnostic{Code: "credential_effect_pending", RootID: root.ID, Concern: string(e.Field), Status: Unsupported})
		}
		if err := ValidateManagedTree(r.Tree, destinations); err != nil {
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
		request := materialize.Request{TargetRoot: root.Path, Artifacts: r.Tree, Operation: materialize.OperationCreate, ExistingTarget: materialize.ExistingTargetRefuse, Selection: copyRecord(spec.Boot.Selection), Reconcile: materialize.ReconcilePolicy{Conflict: materialize.ConflictReport}, Roots: materialize.TargetRoots{BootRoot: root.Path, StateRoot: spec.Home.Root.Path}}
		if o.Exists {
			if o.Empty && o.Manifest == nil {
				request.ExistingTarget = materialize.ExistingTargetAllowEmpty
			} else {
				if o.Manifest == nil {
					return PlannedWorkspace{}, refuse("missing_committed_manifest", "candidate", Conflict)
				}
				if spec.Boot.CandidateGeneration == "" || o.Manifest.Generation != spec.Boot.CandidateGeneration {
					return PlannedWorkspace{}, refuse("stale_candidate_generation", "candidate", Conflict)
				}
				request.Operation = materialize.OperationReconcile
				request.CurrentManifest = copyRecord(o.Manifest)
				request.ExpectedGeneration = spec.Boot.CandidateGeneration
			}
		}
		p.actions = append(p.actions, Action{Kind: TreeAction, Root: root, RootMode: r.RootMode, Request: request, RequiredCapabilities: []Capability{CanonicalRoots, MutationLocks}})
	}
	for _, credential := range spec.Credentials {
		if credential.Required {
			p.diagnostics = append(p.diagnostics, Diagnostic{Code: "credential_effect_pending", RootID: credential.DestinationRootID, Concern: credential.Concern, Status: Unsupported})
		}
	}
	if len(spec.Repos) > 0 || len(spec.Trust) > 0 || spec.Sandbox.Policy.Mode == sandbox.ConfinementRequired {
		p.diagnostics = append(p.diagnostics, Diagnostic{Code: "host_effects_pending", Concern: "effects", Status: Unsupported})
	}
	if spec.Operation == Install {
		p.diagnostics = append(p.diagnostics, Diagnostic{Code: "installed_apply_pending", Concern: "installed", Status: Unsupported})
	}
	p.diagnostics = append(p.diagnostics, Diagnostic{Code: string(LaunchReservationPending), Concern: "launch", Status: Partial})
	for _, action := range p.actions {
		kind := DirectoryEffect
		if action.Kind == TreeAction || (action.Kind == DeferredAction && len(action.Request.Artifacts.Entries) > 0) {
			kind = ArtifactEffect
		} else if action.Kind != EnsureDirectoryAction {
			continue
		}
		if !hasGrant(spec.Effects, resources.Grants, kind, action.Root.ID) {
			return PlannedWorkspace{}, refuse("missing_effect_grant", "effects", Conflict)
		}
		if action.Kind == DeferredAction {
			continue
		}
		for _, capability := range action.RequiredCapabilities {
			if !slices.Contains(resources.Capabilities, capability) || !slices.Contains(observed.Capabilities, capability) {
				return PlannedWorkspace{}, refuse("required_capability_unavailable", "capabilities", Unsupported)
			}
		}
	}
	slices.SortFunc(p.actions, func(a, b Action) int {
		if n := cmp.Compare(a.Root.Path, b.Root.Path); n != 0 {
			return n
		}
		return cmp.Compare(a.Kind, b.Kind)
	})
	// Set-valued resources are canonicalized; argv and other ordered inputs are
	// retained in order. Evidence timestamps/disk snapshots are not desired input.
	p.resources.Roots = slices.Clone(p.roots)
	slices.Sort(p.resources.Capabilities)
	slices.SortFunc(p.resources.Grants, func(a, b EffectGrant) int {
		return cmp.Compare(string(a.Kind)+"\x00"+a.RootID+"\x00"+a.AuthorizationID+"\x00"+a.Version, string(b.Kind)+"\x00"+b.RootID+"\x00"+b.AuthorizationID+"\x00"+b.Version)
	})
	encoded, err := json.Marshal(struct {
		Spec        Spec
		Content     []renderSnapshot
		RenderRoots map[layout.Root]string
		Resources   Resources
		Locks       []LockKey
	}{p.spec, p.content, p.renderRoots, p.resources, p.locks})
	if err != nil {
		return PlannedWorkspace{}, refuse("invalid_frozen_input", "spec", Conflict)
	}
	sum := sha256.Sum256(encoded)
	p.digest = hex.EncodeToString(sum[:])
	for _, receipt := range observed.Receipts {
		if receipt.OperationID == spec.OperationID {
			if receipt.SchemaVersion != SchemaVersion || receipt.IdentityKey != spec.Identity.EncodedKey {
				return PlannedWorkspace{}, refuse("invalid_operation_receipt", "receipt", Conflict)
			}
			if receipt.InputDigest != p.digest {
				return PlannedWorkspace{}, refuse("operation_id_reused", "receipt", Conflict)
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
	all := append(slices.Clone(resources.Roots), spec.Home.Root, spec.Boot.IdentityRoot, spec.Boot.Current, spec.Boot.Candidate)
	for _, s := range spec.Scratch {
		all = append(all, s.Root)
	}
	byID := map[string]RootRef{}
	for _, r := range all {
		if err := r.Validate(); err != nil {
			return nil, err
		}
		if prior, ok := byID[r.ID]; ok && prior != r {
			return nil, refuse("ambiguous_root_reference", "roots", Conflict)
		}
		byID[r.ID] = r
	}
	out := make([]RootRef, 0, len(byID))
	for _, r := range byID {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b RootRef) int { return cmp.Compare(a.ID, b.ID) })
	if _, ok := byID[spec.CWD.RootID]; !ok {
		return nil, refuse("unknown_cwd_root", "cwd", Conflict)
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
