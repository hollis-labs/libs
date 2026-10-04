package workspace

import (
	"encoding/hex"
	"fmt"
	"github.com/hollis-labs/substrate/harness/workspace/render"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/hollis-labs/substrate/harness/sandbox"
	"github.com/hollis-labs/substrate/harness/workspace/bootkey"
	"github.com/hollis-labs/substrate/harness/workspace/materialize"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
)

// Refusal is a path- and content-free, machine-readable preparation failure.
type Refusal struct {
	Code    string
	Status  Status
	Concern string
}

func (r *Refusal) Error() string                       { return fmt.Sprintf("workspace: %s (%s)", r.Code, r.Concern) }
func refuse(code, concern string, status Status) error { return &Refusal{code, status, concern} }

// Validate checks syntax and explicit identity inputs only. Enrollment, fences,
// physical paths and authority still require live host validation at apply.
func (s Spec) Validate() error {
	if err := validateFrozenValues(s); err != nil {
		return err
	}
	if s.SchemaVersion != SchemaVersion {
		return refuse(CodeUnsupportedSchema, "spec", Unsupported)
	}
	if s.OperationID == "" {
		return refuse(CodeMissingOperationId, "spec", Conflict)
	}
	switch s.Operation {
	case Prepare, Resume, Install, Recover, Retire:
	default:
		return refuse(CodeUnsupportedOperation, "operation", Unsupported)
	}
	if s.Identity.AgentURN == "" || s.Identity.DefinitionRevision == "" || s.Identity.Session == "" || s.Identity.Fence.ID == "" || s.Identity.Fence.Revision == "" {
		return refuse(CodeMissingIdentityPin, "identity", Conflict)
	}
	for _, component := range []string{s.Identity.Session, s.Identity.Instance, s.Identity.Assignment} {
		if component != "" && bootkey.ValidateComponent(component) != nil {
			return refuse(CodeInvalidIdentityComponent, "identity", Conflict)
		}
	}
	key, err := bootkey.Encode(s.Identity.AgentURN)
	if err != nil || key != s.Identity.EncodedKey {
		return refuse(CodeIdentityKeyMismatch, "identity", Conflict)
	}
	for _, d := range []artifact.Digest{s.Identity.SemanticDigest, s.Identity.ArtifactDigest, s.Identity.DependencyDigest} {
		if d.Algorithm == "" || d.Hex == "" {
			return refuse(CodeMissingInputDigest, "identity", Conflict)
		}
		decoded, err := hex.DecodeString(d.Hex)
		if d.Algorithm != "sha256" || err != nil || len(decoded) != 32 || strings.ToLower(d.Hex) != d.Hex {
			return refuse(CodeUnsupportedInputDigest, "identity", Unsupported)
		}
	}
	if s.Home.Continuity != Durable && s.Home.Continuity != Ephemeral {
		return refuse(CodeUnsupportedContinuity, "home", Unsupported)
	}
	if s.Home.Layout != FullHome && s.Home.Layout != LightHome {
		return refuse(CodeUnsupportedHomeLayout, "home", Unsupported)
	}
	if !validRetention(s.Home.Retention) || !validRetention(s.Boot.Retention) || !validRetention(s.Cleanup.Retention) {
		return refuse(CodeUnsupportedRetention, "retention", Unsupported)
	}
	for _, r := range []RootRef{s.Home.Root, s.Boot.IdentityRoot, s.Boot.Current, s.Boot.Candidate} {
		if err := r.Validate(); err != nil {
			return err
		}
	}
	if filepath.Base(s.Boot.IdentityRoot.Path) != s.Identity.EncodedKey || filepath.Base(s.Boot.Current.Path) != "current" {
		return refuse(CodeIdentityRootMismatch, "identity", Conflict)
	}
	if s.Boot.Candidate.Path == s.Boot.Current.Path {
		return refuse(CodeCandidateIsCurrent, "boot", Conflict)
	}
	for _, r := range []RootRef{s.Boot.Current, s.Boot.Candidate} {
		if filepath.Dir(r.Path) != s.Boot.IdentityRoot.Path || r.Owner != s.Boot.IdentityRoot.Owner {
			return refuse(CodeInvalidBootSiblings, "boot", Conflict)
		}
	}
	if s.Boot.ExpectedGeneration == "" && s.Operation == Resume {
		return refuse(CodeMissingExpectedGeneration, "boot", Conflict)
	}
	if s.Sandbox.Policy.Mode != sandbox.ConfinementRequired && s.Sandbox.Policy.Mode != sandbox.ConfinementDisabled {
		return refuse(CodeUnsupportedConfinement, "sandbox", Unsupported)
	}
	if s.CWD.Relative != "" && s.CWD.Relative != "." {
		if err := validateArtifactPath(s.CWD.Relative); err != nil {
			return refuse(CodeUnsafeCwd, "cwd", Conflict)
		}
	}
	if s.CWD.RootID == "" {
		return refuse(CodeMissingCwdRoot, "cwd", Conflict)
	}
	for _, rowID := range s.Boot.RowIDs {
		if rowID == "" || !safeValueText(rowID) {
			return refuse(CodeInvalidRowReference, "boot", Conflict)
		}
	}
	for _, path := range []string{s.CWD.Child, s.CWD.ProtocolProject} {
		if path != "" && !cleanAbsolute(path) {
			return refuse(CodeUnsafeCwd, "cwd", Conflict)
		}
	}
	for _, rootID := range s.Cleanup.OwnedRoots {
		if rootID == "" || !safeValueText(rootID) {
			return refuse(CodeInvalidCleanupReference, "cleanup", Conflict)
		}
	}
	for rootID, generation := range s.Cleanup.ExpectedGenerations {
		if rootID == "" || generation == "" || !safeValueText(rootID) || !safeValueText(generation) {
			return refuse(CodeInvalidCleanupReference, "cleanup", Conflict)
		}
	}
	for _, scratch := range s.Scratch {
		if err := scratch.Root.Validate(); err != nil {
			return err
		}
		if scratch.Session != s.Identity.Session || scratch.Assignment != s.Identity.Assignment || scratch.QuotaBytes < 0 || !validRetention(scratch.Retention) {
			return refuse(CodeInvalidScratch, "scratch", Conflict)
		}
	}
	for _, scratch := range s.Scratch {
		for name, value := range scratch.Environment {
			if !environmentName.MatchString(name) || credentialEnvironmentName(name) || !safeValueText(value) {
				return refuse(CodeInvalidScratchEnvironment, "scratch", Conflict)
			}
		}
	}
	for _, repo := range s.Repos {
		if repo.Mode != Worktree && repo.Mode != Checkout && repo.Mode != Readonly {
			return refuse(CodeUnsupportedRepositoryMode, "repository", Unsupported)
		}
		if err := repo.DesiredRoot.Validate(); err != nil {
			return err
		}
		if !validRetention(repo.Retention) || repo.ID == "" || repo.Source.ID == "" {
			return refuse(CodeInvalidRepository, "repository", Conflict)
		}
	}
	for _, c := range s.Credentials {
		if c.Source.ID == "" || c.Authorization.ID == "" || c.DestinationRootID == "" || c.Concern == "" || validateArtifactPath(c.Destination) != nil {
			return refuse(CodeInvalidCredentialReference, "credentials", Conflict)
		}
		if err := validateAccess(c.Access); err != nil {
			return err
		}
	}
	for _, c := range s.Credentials {
		for _, kind := range c.Access {
			if kind == sandbox.AccessWrite {
				return refuse(CodeInvalidCredentialAccess, "credentials", Conflict)
			}
		}
	}
	for _, access := range s.ExtraDirs {
		if !cleanAbsolute(access.Resource.Path) || access.Resource.ID == "" || access.Resource.Provenance == "" {
			return refuse(CodeInvalidAccessResource, "access", Conflict)
		}
		if err := validateAccess(access.Access); err != nil {
			return err
		}
	}
	for _, grant := range s.Effects {
		if !validEffect(grant.Kind) || grant.RootID == "" || grant.AuthorizationID == "" || grant.Version == "" {
			return refuse(CodeInvalidEffectGrant, "effects", Conflict)
		}
	}
	for _, c := range append(append([]Capability{}, s.Sandbox.RequiredCapabilities...), s.Cleanup.RequiredProofs...) {
		if !validCapability(c) {
			return refuse(CodeUnsupportedCapability, "capabilities", Unsupported)
		}
	}
	if s.Boot.Reconcile.Conflict != "" && s.Boot.Reconcile.Conflict != materialize.ConflictReport {
		return refuse(CodeOverwriteNotAuthorized, "reconcile", Conflict)
	}
	if s.Boot.Reconcile.RemoveOwned {
		return refuse(CodeRetirementDeferred, "reconcile", Unsupported)
	}
	return nil
}

func validateAccess(access []sandbox.AccessKind) error {
	for _, kind := range access {
		switch kind {
		case sandbox.AccessRead, sandbox.AccessWrite, sandbox.AccessDeny, sandbox.AccessSourceRead, sandbox.AccessRuntimeRead, sandbox.AccessProtect:
		default:
			return refuse(CodeUnsupportedAccess, "access", Unsupported)
		}
	}
	return nil
}

func validEffect(k EffectKind) bool {
	switch k {
	case DirectoryEffect, ArtifactEffect, CredentialLinkEffect, TrustEffect, RepositoryEffect:
		return true
	}
	return false
}

func validCapability(c Capability) bool {
	switch c {
	case CanonicalRoots, MutationLocks, UseReservation, CredentialLinks, TrustHandling, RepositoryAttachments, SandboxConfinement, BootPublication, InstalledMerge:
		return true
	}
	return false
}

func validRetention(p RetentionPolicy) bool {
	return p == Keep || p == RetainForRecovery || p == RetireWhenUnused
}

// Validate is lexical: it performs no filesystem operation. A live observer
// must supply and later revalidate physical canonical roots and allowed bases.
func (r RootRef) Validate() error {
	if err := validateFrozenValues(r); err != nil {
		return err
	}
	if r.ID == "" || r.Owner == "" || r.Provenance == "" {
		return refuse(CodeMissingRootOwnership, "root", Conflict)
	}
	if !cleanAbsolute(r.Path) || !cleanAbsolute(r.AllowedBase) || !within(r.AllowedBase, r.Path) {
		return refuse(CodeUnsafeRoot, "root", Conflict)
	}
	if filepath.Dir(r.AllowedBase) == r.AllowedBase || r.Path == r.AllowedBase {
		return refuse(CodeUnsafeAllowedBase, "root", Conflict)
	}
	if filepath.Dir(r.Path) == r.Path {
		return refuse(CodeProtectedFilesystemRoot, "root", Conflict)
	}
	return nil
}
func cleanAbsolute(p string) bool {
	if !utf8.ValidString(p) || len(p) > render.MaxPathBytes || !filepath.IsAbs(p) || filepath.Clean(p) != p || strings.Count(filepath.ToSlash(p), "/") > render.MaxTreeDepth {
		return false
	}
	for _, r := range p {
		if r < 32 || r == 127 || r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069 {
			return false
		}
	}
	return true
}
func within(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// ValidateManagedTree refuses credential and control slots regardless of the
// claimed owner/concern. Explicit additional credential destinations are also
// excluded, including descendants and ancestors that would remove them.
func ValidateManagedTree(tree artifact.Tree, credentialDestinations []string) error {
	for _, dest := range credentialDestinations {
		if artifact.ValidateRelPath(dest) != nil {
			return refuse(CodeInvalidCredentialDestination, "credentials", Conflict)
		}
	}
	if err := validateFrozenValues(tree, credentialDestinations); err != nil {
		return err
	}
	entries, err := artifact.Normalize(tree.Entries)
	if err != nil {
		return refuse(CodeInvalidArtifactTree, "artifacts", Conflict)
	}
	return validateManagedEntries(entries, credentialDestinations)
}

func validateManagedEntries(entries []artifact.Entry, credentialDestinations []string) error {
	paths := map[string]bool{}
	ids := map[string]bool{}
	for _, e := range entries {
		if paths[foldText(e.Path)] || ids[e.Ownership.EntryID] {
			return refuse(CodeArtifactAliasCollision, "artifacts", Conflict)
		}
		paths[foldText(e.Path)] = true
		ids[e.Ownership.EntryID] = true
		if e.Mode&^e.Mode.Perm() != 0 || e.Kind == artifact.EntryFile && e.Mode.Perm()&0022 != 0 || e.Kind == artifact.EntryDirectory && e.Mode != 0 && e.Mode.Perm() != 0755 {
			return refuse(CodeUnsafeArtifactMode, "artifacts", Conflict)
		}
		folded := strings.ToLower(e.Path)
		if credentialPath(e.Path) || folded == ".materialize" || strings.HasPrefix(folded, ".materialize/") {
			return refuse(CodeReservedArtifactPath, "artifacts", Conflict)
		}
		if validateArtifactPath(e.Path) != nil {
			return refuse(CodeUnsafeArtifactPath, "artifacts", Conflict)
		}
		for _, dest := range credentialDestinations {
			if artifact.ValidateRelPath(dest) != nil {
				return refuse(CodeInvalidCredentialDestination, "credentials", Conflict)
			}
			if credentialCollision(e.Path, dest, e.Kind) {
				return refuse(CodeCredentialArtifactCollision, "credentials", Conflict)
			}
		}
		if e.ContentRef != nil || (e.Kind == artifact.EntryFile && e.Bytes == nil) {
			return refuse(CodeUnresolvedArtifact, "artifacts", Conflict)
		}
		if e.Ownership.EntryID == "" || e.Ownership.GroupID == "" || e.Provenance.Source == "" {
			return refuse(CodeMissingArtifactOwnership, "artifacts", Conflict)
		}
		if e.Kind == artifact.EntryFile && e.Digest != (artifact.Digest{}) && e.Digest != artifact.DigestBytes(e.Bytes) {
			return refuse(CodeArtifactDigestMismatch, "artifacts", Conflict)
		}
	}
	return nil
}

// ValidateManagedManifest prevents legacy credential claims from entering an
// apply or removal plan. It does not bootstrap ownership from desired content.
func ValidateManagedManifest(manifest materialize.Manifest, credentialDestinations []string) error {
	for _, dest := range credentialDestinations {
		if artifact.ValidateRelPath(dest) != nil {
			return refuse(CodeInvalidCredentialDestination, "credentials", Conflict)
		}
	}
	for _, e := range manifest.Entries {
		folded := strings.ToLower(e.Path)
		if artifact.ValidateRelPath(e.Path) != nil || credentialPath(e.Path) || folded == ".materialize" || strings.HasPrefix(folded, ".materialize/") {
			return refuse(CodeCredentialOwnedInvalid, "manifest", Conflict)
		}
		for _, dest := range credentialDestinations {
			if artifact.ValidateRelPath(dest) != nil {
				return refuse(CodeInvalidCredentialDestination, "credentials", Conflict)
			}
			if credentialCollision(e.Path, dest, e.Kind) {
				return refuse(CodeCredentialOwnedInvalid, "manifest", Conflict)
			}
		}
	}
	return nil
}

func credentialCollision(entry, destination string, kind artifact.EntryKind) bool {
	// Conservatively reserve case variants on every host, including hosts whose
	// filesystem compares names without case. Ancestor directories may contain
	// links; an ancestor regular file would block the binding destination.
	e, d := foldText(entry), foldText(destination)
	return e == d || strings.HasPrefix(e, d+"/") || (kind == artifact.EntryFile && strings.HasPrefix(d, e+"/"))
}

func credentialPath(p string) bool { return isCredentialDestination(p) }

var environmentName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

func safeValueText(s string) bool {
	for _, r := range s {
		if r < 32 || r == 127 || r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069 {
			return false
		}
	}
	return utf8.ValidString(s)
}
func credentialEnvironmentName(name string) bool {
	upper := strings.ToUpper(name)
	return isCredentialDestination(name) || strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "API_KEY") || strings.Contains(upper, "CREDENTIAL")
}
