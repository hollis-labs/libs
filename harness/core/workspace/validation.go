package workspace

import (
	"encoding/hex"
	"fmt"
	"path"
	"path/filepath"
	"strings"

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
	if s.SchemaVersion != SchemaVersion {
		return refuse("unsupported_schema", "spec", Unsupported)
	}
	if s.OperationID == "" {
		return refuse("missing_operation_id", "spec", Conflict)
	}
	switch s.Operation {
	case Prepare, Resume, Install, Recover, Retire:
	default:
		return refuse("unsupported_operation", "operation", Unsupported)
	}
	if s.Identity.AgentURN == "" || s.Identity.DefinitionRevision == "" || s.Identity.Session == "" || s.Identity.Fence.ID == "" || s.Identity.Fence.Revision == "" {
		return refuse("missing_identity_pin", "identity", Conflict)
	}
	key, err := bootkey.Encode(s.Identity.AgentURN)
	if err != nil || key != s.Identity.EncodedKey {
		return refuse("identity_key_mismatch", "identity", Conflict)
	}
	for _, d := range []artifact.Digest{s.Identity.SemanticDigest, s.Identity.ArtifactDigest, s.Identity.DependencyDigest} {
		if d.Algorithm == "" || d.Hex == "" {
			return refuse("missing_input_digest", "identity", Conflict)
		}
		decoded, err := hex.DecodeString(d.Hex)
		if d.Algorithm != "sha256" || err != nil || len(decoded) != 32 || strings.ToLower(d.Hex) != d.Hex {
			return refuse("unsupported_input_digest", "identity", Unsupported)
		}
	}
	if s.Home.Continuity != Durable && s.Home.Continuity != Ephemeral {
		return refuse("unsupported_continuity", "home", Unsupported)
	}
	if s.Home.Layout != FullHome && s.Home.Layout != LightHome {
		return refuse("unsupported_home_layout", "home", Unsupported)
	}
	if !validRetention(s.Home.Retention) || !validRetention(s.Boot.Retention) || !validRetention(s.Cleanup.Retention) {
		return refuse("unsupported_retention", "retention", Unsupported)
	}
	for _, r := range []RootRef{s.Home.Root, s.Boot.Current, s.Boot.Candidate} {
		if err := r.Validate(); err != nil {
			return err
		}
	}
	if s.Boot.Candidate.Path == s.Boot.Current.Path {
		return refuse("candidate_is_current", "boot", Conflict)
	}
	if s.Boot.ExpectedGeneration == "" && s.Operation == Resume {
		return refuse("missing_expected_generation", "boot", Conflict)
	}
	if s.Sandbox.Policy.Mode != sandbox.ConfinementRequired && s.Sandbox.Policy.Mode != sandbox.ConfinementDisabled {
		return refuse("unsupported_confinement", "sandbox", Unsupported)
	}
	if s.CWD.Relative != "" && s.CWD.Relative != "." {
		if err := artifact.ValidateRelPath(s.CWD.Relative); err != nil {
			return refuse("unsafe_cwd", "cwd", Conflict)
		}
	}
	if s.CWD.RootID == "" {
		return refuse("missing_cwd_root", "cwd", Conflict)
	}
	for _, scratch := range s.Scratch {
		if err := scratch.Root.Validate(); err != nil {
			return err
		}
		if scratch.Session != s.Identity.Session || scratch.QuotaBytes < 0 || !validRetention(scratch.Retention) {
			return refuse("invalid_scratch", "scratch", Conflict)
		}
	}
	for _, repo := range s.Repos {
		if repo.Mode != Worktree && repo.Mode != Checkout && repo.Mode != Readonly {
			return refuse("unsupported_repository_mode", "repository", Unsupported)
		}
		if err := repo.DesiredRoot.Validate(); err != nil {
			return err
		}
		if !validRetention(repo.Retention) || repo.ID == "" || repo.Source.ID == "" {
			return refuse("invalid_repository", "repository", Conflict)
		}
	}
	for _, c := range s.Credentials {
		if c.Source.ID == "" || c.Authorization.ID == "" || c.DestinationRootID == "" || c.Concern == "" || artifact.ValidateRelPath(c.Destination) != nil {
			return refuse("invalid_credential_reference", "credentials", Conflict)
		}
		if err := validateAccess(c.Access); err != nil {
			return err
		}
	}
	for _, access := range s.ExtraDirs {
		if err := validateAccess(access.Access); err != nil {
			return err
		}
	}
	for _, grant := range s.Effects {
		if !validEffect(grant.Kind) || grant.RootID == "" || grant.AuthorizationID == "" || grant.Version == "" {
			return refuse("invalid_effect_grant", "effects", Conflict)
		}
	}
	for _, c := range append(append([]Capability{}, s.Sandbox.RequiredCapabilities...), s.Cleanup.RequiredProofs...) {
		if !validCapability(c) {
			return refuse("unsupported_capability", "capabilities", Unsupported)
		}
	}
	if s.Boot.Reconcile.Conflict != "" && s.Boot.Reconcile.Conflict != materialize.ConflictReport {
		return refuse("overwrite_not_authorized", "reconcile", Conflict)
	}
	if s.Boot.Reconcile.RemoveOwned {
		return refuse("retirement_deferred", "reconcile", Unsupported)
	}
	return nil
}

func validateAccess(access []sandbox.AccessKind) error {
	for _, kind := range access {
		switch kind {
		case sandbox.AccessRead, sandbox.AccessWrite, sandbox.AccessDeny, sandbox.AccessSourceRead, sandbox.AccessRuntimeRead, sandbox.AccessProtect:
		default:
			return refuse("unsupported_access", "access", Unsupported)
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
	if r.ID == "" || r.Owner == "" || r.Provenance == "" {
		return refuse("missing_root_ownership", "root", Conflict)
	}
	if !cleanAbsolute(r.Path) || !cleanAbsolute(r.AllowedBase) || !within(r.AllowedBase, r.Path) {
		return refuse("unsafe_root", "root", Conflict)
	}
	if filepath.Dir(r.Path) == r.Path {
		return refuse("protected_filesystem_root", "root", Conflict)
	}
	return nil
}
func cleanAbsolute(p string) bool {
	return filepath.IsAbs(p) && filepath.Clean(p) == p && !strings.ContainsAny(p, "\x00\r\n")
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
			return refuse("invalid_credential_destination", "credentials", Conflict)
		}
	}
	entries, err := artifact.Normalize(tree.Entries)
	if err != nil {
		return refuse("invalid_artifact_tree", "artifacts", Conflict)
	}
	for _, e := range entries {
		folded := strings.ToLower(e.Path)
		if credentialPath(e.Path) || folded == ".materialize" || strings.HasPrefix(folded, ".materialize/") {
			return refuse("reserved_artifact_path", "artifacts", Conflict)
		}
		for _, dest := range credentialDestinations {
			if artifact.ValidateRelPath(dest) != nil {
				return refuse("invalid_credential_destination", "credentials", Conflict)
			}
			if credentialCollision(e.Path, dest, e.Kind) {
				return refuse("credential_artifact_collision", "credentials", Conflict)
			}
		}
		if e.ContentRef != nil || (e.Kind == artifact.EntryFile && e.Bytes == nil) {
			return refuse("unresolved_artifact", "artifacts", Conflict)
		}
		if e.Ownership.EntryID == "" || e.Ownership.GroupID == "" || e.Provenance.Source == "" {
			return refuse("missing_artifact_ownership", "artifacts", Conflict)
		}
		if e.Kind == artifact.EntryFile && e.Digest != (artifact.Digest{}) && e.Digest != artifact.DigestBytes(e.Bytes) {
			return refuse("artifact_digest_mismatch", "artifacts", Conflict)
		}
	}
	return nil
}

// ValidateManagedManifest prevents legacy credential claims from entering an
// apply or removal plan. It does not bootstrap ownership from desired content.
func ValidateManagedManifest(manifest materialize.Manifest, credentialDestinations []string) error {
	for _, dest := range credentialDestinations {
		if artifact.ValidateRelPath(dest) != nil {
			return refuse("invalid_credential_destination", "credentials", Conflict)
		}
	}
	for _, e := range manifest.Entries {
		folded := strings.ToLower(e.Path)
		if artifact.ValidateRelPath(e.Path) != nil || credentialPath(e.Path) || folded == ".materialize" || strings.HasPrefix(folded, ".materialize/") {
			return refuse("credential_owned_invalid", "manifest", Conflict)
		}
		for _, dest := range credentialDestinations {
			if artifact.ValidateRelPath(dest) != nil {
				return refuse("invalid_credential_destination", "credentials", Conflict)
			}
			if credentialCollision(e.Path, dest, e.Kind) {
				return refuse("credential_owned_invalid", "manifest", Conflict)
			}
		}
	}
	return nil
}

func credentialCollision(entry, destination string, kind artifact.EntryKind) bool {
	// Conservatively reserve case variants on every host, including hosts whose
	// filesystem compares names without case. Ancestor directories may contain
	// links; an ancestor regular file would block the binding destination.
	e, d := strings.ToLower(entry), strings.ToLower(destination)
	return e == d || strings.HasPrefix(e, d+"/") || (kind == artifact.EntryFile && strings.HasPrefix(d, e+"/"))
}

func credentialPath(p string) bool {
	// Check every component so a directory cannot conceal a protected basename.
	for _, part := range strings.Split(p, "/") {
		switch strings.ToLower(path.Base(part)) {
		case "auth.json", ".credentials.json", "oauth_creds.json":
			return true
		}
	}
	return false
}
