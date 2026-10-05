package materialize

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
)

// InstalledOriginalContext carries bounded comparison data, never an issued
// descriptor capability. Unknown expected issuer or visibility bindings stay
// unknown. JSON, caller construction and complete-looking metadata confer no
// admission, filesystem support or authority.
type InstalledOriginalContext struct {
	SchemaVersion, OperationID, InputDigest string
	Identity                                InstalledOriginalIdentity
	Fence                                   InstalledOriginalFence
	Target, Control, Lock                   InstalledOriginalRoot
	Grants                                  []InstalledOriginalGrant
	Authorizations                          []InstalledOriginalAuthorization
	Files                                   []InstalledOriginalFile
	ObservedAt, ExpiresAt                   time.Time
	Expected                                InstalledExpectedIssuer
}
type InstalledOriginalIdentity struct {
	AgentURN, EncodedKey, Instance, Session, Assignment, DefinitionRevision string
	SemanticDigest, ArtifactDigest, DependencyDigest                        artifact.Digest
}
type InstalledOriginalFence struct{ ID, Path, Revision, Provenance string }
type InstalledOriginalRoot struct {
	ID, Path, AllowedBase, Owner, Provenance                  string
	Identity, Volume, CapabilityRevision, ObservationRevision string
	Metadata                                                  InstalledMetadata
}
type InstalledOriginalGrant struct {
	ID, Version, Path string
	WholeFile         bool
	KeyPaths          [][]string
}
type InstalledOriginalAuthorization struct{ Kind, RootID, AuthorizationID, Version string }
type InstalledOriginalFile struct {
	Path, Identity string
	Exists         bool
	Kind           artifact.EntryKind
	Mode           uint32
	Digest         artifact.Digest
	Metadata       InstalledMetadata
	Parents        []InstalledDirectoryChange
}

// InstalledExpectedIssuer records upstream expectations only. It is deliberately
// not inferred from GOOS, UID, a capability label, a hash or a callback result.
type InstalledExpectedIssuer struct {
	Issuer, Schema, Implementation, Backend              string
	OS, Kernel, Filesystem, Mount, Volume                string
	SecurityContext, UserNamespace, CredentialVisibility string
}

const installedContextLimit = 1 << 20
const installedContextItems = 4096

// All provenance-bearing values below are private. There is no production
// issuer, constructor, option setter or decoder. Package tests construct
// explicitly SYNTHETIC issued envelopes to test the consumer independently of
// public native Unsupported paths. A future issuer requires its own review.
type installedIssuerSeal struct{ binding InstalledExpectedIssuer }
type installedCoverage struct{ allNamespaces, acl, flags, inheritance bool }
type installedFileAttestation struct {
	path, identity, parentIdentity, revision string
	metadata, creation                       InstalledMetadata
	coverage                                 installedCoverage
	parents                                  []installedParentAttestation
}
type installedParentAttestation struct {
	path, identity, revision string
	metadata                 InstalledMetadata
	coverage                 installedCoverage
}
type installedStageCapability struct {
	parentIdentity, revision string
	creation                 InstalledMetadata
	coverage                 installedCoverage
}
type issuedInstalledAttestation struct {
	issuer                 *installedIssuerSeal
	original               InstalledOriginalContext
	requestSeal            [32]byte
	roots                  [3]InstalledMetadata
	rootCoverage           [3]installedCoverage
	files                  []installedFileAttestation
	stage                  installedStageCapability
	revision               string
	observedAt, validUntil time.Time
	integrity              [32]byte
}
type installedOriginalLedger struct {
	request  Request
	original InstalledOriginalContext
	seal     [32]byte
}
type installedMetadataAdmission struct {
	ledger installedOriginalLedger
	issued issuedInstalledAttestation
	seal   [32]byte
}

func installedBoundedContext(c *InstalledOriginalContext) bool {
	if c == nil {
		return false
	}
	items, size := 0, 0
	var walk func(reflect.Value) bool
	walk = func(v reflect.Value) bool {
		switch v.Kind() {
		case reflect.String:
			s := v.String()
			size += len(s)
			return len(s) <= 4096 && size <= installedContextLimit && utf8.ValidString(s) && !strings.ContainsAny(s, "\x00\r\n")
		case reflect.Struct:
			if v.Type() == reflect.TypeOf(time.Time{}) {
				return true
			}
			for i := 0; i < v.NumField(); i++ {
				if !walk(v.Field(i)) {
					return false
				}
			}
		case reflect.Slice, reflect.Array:
			items += v.Len()
			if v.Len() > installedContextItems || items > installedContextItems*16 {
				return false
			}
			for i := 0; i < v.Len(); i++ {
				if !walk(v.Index(i)) {
					return false
				}
			}
		}
		return true
	}
	if !walk(reflect.ValueOf(*c)) {
		return false
	}
	b, err := json.Marshal(c)
	return err == nil && len(b) <= installedContextLimit
}
func installedCloneRequest(r Request) (Request, error) {
	if !installedBoundedContext(r.InstalledOriginalContext) {
		return Request{}, ErrUnsupportedOperation
	}
	b, err := json.Marshal(r)
	if err != nil {
		return Request{}, ErrUnsafeTarget
	}
	var out Request
	if err = json.Unmarshal(b, &out); err != nil {
		return Request{}, ErrUnsafeTarget
	}
	out.Artifacts.Entries = artifact.CloneEntries(r.Artifacts.Entries)
	return out, nil
}
func installedRequestSeal(r Request) [32]byte {
	b, _ := json.Marshal(r)
	return sha256.Sum256(append([]byte("workspace.installed.original.v1\x00"), b...))
}
func freezeInstalledOriginal(r Request) (installedOriginalLedger, error) {
	frozen, err := installedCloneRequest(r)
	if err != nil {
		return installedOriginalLedger{}, err
	}
	c := frozen.InstalledOriginalContext
	if frozen.Operation != OperationInstall || frozen.Installed == nil || c.SchemaVersion != "workspace.v1" || c.OperationID == "" || c.InputDigest != frozen.Generation || !installedInputDigest(c.InputDigest) || c.Identity.AgentURN == "" || c.Identity.EncodedKey == "" || c.Identity.DefinitionRevision == "" || !installedDigestKnown(c.Identity.SemanticDigest) || !installedDigestKnown(c.Identity.ArtifactDigest) || !installedDigestKnown(c.Identity.DependencyDigest) || c.Fence.ID == "" || c.Fence.Revision == "" || !installedWindow(c.ObservedAt, c.ExpiresAt) {
		return installedOriginalLedger{}, ErrUnsupportedOperation
	}
	roots := []InstalledOriginalRoot{c.Target, c.Control, c.Lock}
	for i, root := range roots {
		if root.ID == "" || root.Owner == "" || root.Provenance == "" || root.Identity == "" || root.Volume == "" || root.CapabilityRevision == "" || root.ObservationRevision == "" || !allowedInstalledMetadata(root.Metadata, true) || root.Metadata.Volume != root.Volume || !filepath.IsAbs(root.Path) || filepath.Clean(root.Path) != root.Path || !filepath.IsAbs(root.AllowedBase) || filepath.Clean(root.AllowedBase) != root.AllowedBase || !installedWithin(root.AllowedBase, root.Path) {
			return installedOriginalLedger{}, ErrUnsupportedOperation
		}
		for _, prior := range roots[:i] {
			if root.ID == prior.ID || root.Identity == prior.Identity || installedWithin(root.Path, prior.Path) || installedWithin(prior.Path, root.Path) {
				return installedOriginalLedger{}, ErrConflict
			}
		}
	}
	if c.Target.Metadata != frozen.Installed.Capabilities.RootMetadata || c.Target.Path != frozen.TargetRoot || c.Target.Identity != frozen.Installed.TargetIdentity || c.Control.Identity != frozen.Installed.ControlIdentity || c.Target.Volume != frozen.Installed.Capabilities.Volume || c.Target.ObservationRevision != frozen.Installed.Capabilities.ObservationRevision || c.Target.CapabilityRevision != frozen.Installed.Capabilities.Revision {
		return installedOriginalLedger{}, ErrConflict
	}
	if !frozen.Installed.Capabilities.Valid() || frozen.Installed.CaseMode != frozen.Installed.Capabilities.CaseMode {
		return installedOriginalLedger{}, ErrUnsupportedOperation
	}
	if frozen.Installed.Stage.Identity != "" || frozen.Installed.Stage.Phase != "" && frozen.Installed.Stage.Phase != InstalledPrepared {
		return installedOriginalLedger{}, ErrConflict
	}
	for _, d := range frozen.Installed.Directories {
		if d.Created || d.Phase != "" && d.Phase != InstalledPrepared {
			return installedOriginalLedger{}, ErrConflict
		}
	}
	grants := map[string]InstalledOriginalGrant{}
	for _, g := range c.Grants {
		if g.ID == "" || g.Version == "" || artifact.ValidateRelPath(g.Path) != nil || g.WholeFile == (len(g.KeyPaths) > 0) {
			return installedOriginalLedger{}, ErrConflict
		}
		if _, ok := grants[g.Path]; ok {
			return installedOriginalLedger{}, ErrConflict
		}
		grants[g.Path] = g
		for _, key := range g.KeyPaths {
			if len(key) == 0 || len(key) > 32 {
				return installedOriginalLedger{}, ErrConflict
			}
			for _, part := range key {
				if part == "" {
					return installedOriginalLedger{}, ErrConflict
				}
			}
		}
		found := false
		for _, a := range c.Authorizations {
			if a.Kind == "installed_artifacts" && a.RootID == c.Target.ID && a.AuthorizationID == g.ID && a.Version == g.Version {
				found = true
			}
		}
		if !found {
			return installedOriginalLedger{}, ErrConflict
		}
	}
	if len(grants) != len(frozen.Installed.Files) || len(c.Files) != len(frozen.Installed.Files) {
		return installedOriginalLedger{}, ErrConflict
	}
	for _, f := range frozen.Installed.Files {
		g, ok := grants[f.Path]
		if !ok || f.Phase != InstalledPrepared || f.AfterMetadata != expectedInstalledMetadata(frozen.Installed.Capabilities.Creation, f.AfterMode) || g.ID != f.GrantID || g.Version != f.GrantVersion || !installedKeyGrantsMatch(g, f) {
			return installedOriginalLedger{}, ErrConflict
		}
		found := false
		for _, b := range c.Files {
			if b.Path == f.Path {
				if found || b.Exists && b.Kind != artifact.EntryFile || !b.Exists && (b.Identity != "" || b.Metadata != (InstalledMetadata{}) || b.Mode != 0 || b.Digest != (artifact.Digest{})) || b.Identity != f.BeforeIdentity || b.Exists != f.BeforeExists || b.Mode != f.BeforeMode || b.Digest != f.Before || b.Metadata != f.BeforeMetadata {
					return installedOriginalLedger{}, ErrConflict
				}
				found = true
			}
		}
		if !found {
			return installedOriginalLedger{}, ErrConflict
		}
		for _, before := range c.Files {
			if before.Path == f.Path {
				parts := strings.Split(f.Path, "/")
				if len(before.Parents) != len(parts)-1 {
					return installedOriginalLedger{}, ErrConflict
				}
				missing := false
				for i, p := range before.Parents {
					if p.Path != strings.Join(parts[:i+1], "/") || missing && p.Exists {
						return installedOriginalLedger{}, ErrConflict
					}
					if !p.Exists {
						missing = true
					}
				}
				if missing && before.Exists {
					return installedOriginalLedger{}, ErrConflict
				}
				for _, p := range before.Parents {
					if artifact.ValidateRelPath(p.Path) != nil || !strings.HasPrefix(f.Path, p.Path+"/") || p.Created {
						return installedOriginalLedger{}, ErrConflict
					}
					matched := false
					for _, d := range frozen.Installed.Directories {
						if d.Path == p.Path && d.Exists == p.Exists && d.Identity == p.Identity && d.Mode == p.Mode && d.Metadata == p.Metadata {
							matched = true
						}
					}
					if !matched {
						return installedOriginalLedger{}, ErrConflict
					}
				}
			}
		}
		found = false
		for _, e := range frozen.Artifacts.Entries {
			if e.Path == f.Path && e.Kind == artifact.EntryFile {
				if found || artifact.DigestBytes(e.Bytes) != f.After || uint32(modeFor(e)) != f.AfterMode {
					return installedOriginalLedger{}, ErrConflict
				}
				found = true
			}
		}
		if !found {
			return installedOriginalLedger{}, ErrConflict
		}
	}
	return installedOriginalLedger{request: frozen, original: *frozen.InstalledOriginalContext, seal: installedRequestSeal(frozen)}, nil
}
func installedWithin(base, child string) bool {
	rel, err := filepath.Rel(base, child)
	return err == nil && (rel == "." || filepath.IsLocal(rel))
}
func installedInputDigest(s string) bool {
	if !strings.HasPrefix(s, "sha256:") {
		return false
	}
	b, e := hex.DecodeString(strings.TrimPrefix(s, "sha256:"))
	return e == nil && len(b) == 32 && "sha256:"+hex.EncodeToString(b) == s
}
func installedWindow(at, until time.Time) bool {
	return !at.IsZero() && until.After(at) && until.Sub(at) <= 5*time.Minute
}
func installedExpectedKnown(e InstalledExpectedIssuer) bool {
	v := reflect.ValueOf(e)
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).String() == "" {
			return false
		}
	}
	return true
}

// No backend issued-envelope exists. Freezing comparison inputs is useful for
// negative evidence, but cannot change the public Unsupported outcome.
func admitInstalledOriginal(ctx context.Context, req Request) (installedOriginalLedger, error) {
	if ctx == nil {
		return installedOriginalLedger{}, ErrUnsupportedOperation
	}
	if err := ctx.Err(); err != nil {
		return installedOriginalLedger{}, err
	}
	ledger, err := freezeInstalledOriginal(req)
	if err != nil {
		return installedOriginalLedger{}, err
	}
	return ledger, ErrUnsupportedOperation
}
func cloneIssuedInstalled(e issuedInstalledAttestation) issuedInstalledAttestation {
	b, _ := json.Marshal(e.original)
	_ = json.Unmarshal(b, &e.original)
	e.files = slices.Clone(e.files)
	for i := range e.files {
		e.files[i].parents = slices.Clone(e.files[i].parents)
	}
	if e.issuer != nil {
		issuer := *e.issuer
		e.issuer = &issuer
	}
	return e
}
func installedAttestationIntegrity(e issuedInstalledAttestation) [32]byte {
	// Explicit private projection: public marshaling cannot recover provenance.
	type file struct {
		Path, Identity, Parent, Revision string
		Metadata, Creation               InstalledMetadata
		All, ACL, Flags, Inheritance     bool
	}
	fs := make([]file, len(e.files))
	for i, f := range e.files {
		fs[i] = file{f.path, f.identity, f.parentIdentity, f.revision, f.metadata, f.creation, f.coverage.allNamespaces, f.coverage.acl, f.coverage.flags, f.coverage.inheritance}
	}
	b, _ := json.Marshal(struct {
		Original                        InstalledOriginalContext
		Request                         [32]byte
		Roots                           [3]InstalledMetadata
		Files                           []file
		Parent, StageRevision, Revision string
		Creation                        InstalledMetadata
		All, ACL, Flags, Inheritance    bool
		At, Until                       time.Time
	}{e.original, e.requestSeal, e.roots, fs, e.stage.parentIdentity, e.stage.revision, e.revision, e.stage.creation, e.stage.coverage.allNamespaces, e.stage.coverage.acl, e.stage.coverage.flags, e.stage.coverage.inheritance, e.observedAt, e.validUntil})
	for _, f := range e.files {
		for _, p := range f.parents {
			parent, _ := json.Marshal(struct {
				Path, Identity, Revision string
				Metadata                 InstalledMetadata
			}{p.path, p.identity, p.revision, p.metadata})
			b = append(b, parent...)
			b = append(b, installedCoverageBytes(p.coverage)...)
		}
	}
	for _, coverage := range e.rootCoverage {
		b = append(b, installedCoverageBytes(coverage)...)
	}
	return sha256.Sum256(append([]byte("workspace.installed.attestation.v1\x00"), b...))
}
func installedCoverageComplete(c installedCoverage) bool {
	return c.allNamespaces && c.acl && c.flags && c.inheritance
}
func verifyInstalledAttestation(ctx context.Context, ledger installedOriginalLedger, issued *issuedInstalledAttestation, current InstalledOriginalContext, now time.Time) (*installedMetadataAdmission, error) {
	if ctx == nil {
		return nil, ErrUnsupportedOperation
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ledger.request.InstalledOriginalContext == nil || ledger.seal == ([32]byte{}) || ledger.seal != installedRequestSeal(ledger.request) || !reflect.DeepEqual(ledger.original, *ledger.request.InstalledOriginalContext) || !reflect.DeepEqual(current, ledger.original) || !installedExpectedKnown(ledger.original.Expected) {
		return nil, ErrUnsupportedOperation
	}
	if issued == nil || issued.issuer == nil || issued.issuer.binding != ledger.original.Expected || !reflect.DeepEqual(issued.original, ledger.original) || issued.requestSeal != ledger.seal || issued.integrity != installedAttestationIntegrity(*issued) || issued.revision != ledger.original.Target.ObservationRevision || !installedWindow(issued.observedAt, issued.validUntil) || issued.observedAt.Before(ledger.original.ObservedAt) || issued.validUntil.After(ledger.original.ExpiresAt) || now.Before(issued.observedAt) || !now.Before(issued.validUntil) || now.Before(ledger.original.ObservedAt) || !now.Before(ledger.original.ExpiresAt) {
		return nil, ErrUnsupportedOperation
	}
	expected := ledger.original.Expected
	if expected.Volume != ledger.original.Target.Volume {
		return nil, ErrUnsupportedOperation
	}
	for i, r := range []InstalledOriginalRoot{ledger.original.Target, ledger.original.Control, ledger.original.Lock} {
		if !installedCoverageComplete(issued.rootCoverage[i]) || issued.roots[i] != r.Metadata || !allowedInstalledMetadata(issued.roots[i], true) || issued.roots[i].Volume != r.Volume {
			return nil, ErrUnsupportedOperation
		}
	}
	if len(issued.files) != len(ledger.request.Installed.Files) {
		return nil, ErrUnsupportedOperation
	}
	for i, f := range issued.files {
		want := ledger.request.Installed.Files[i]
		before := ledger.original.Files[i]
		if f.path != want.Path || before.Path != want.Path || f.identity != want.BeforeIdentity || f.revision != ledger.original.Target.ObservationRevision || !installedCoverageComplete(f.coverage) || f.creation != ledger.request.Installed.Capabilities.Creation || before.Exists && (f.metadata != want.BeforeMetadata || !f.metadata.PreservableFile(f.creation, want.AfterMode)) || !before.Exists && f.metadata != (InstalledMetadata{}) {
			return nil, ErrUnsupportedOperation
		}
		if len(f.parents) != len(before.Parents) {
			return nil, ErrUnsupportedOperation
		}
		parent := ledger.original.Target.Identity
		for j, p := range before.Parents {
			observed := f.parents[j]
			if observed.path != p.Path || observed.identity != p.Identity || observed.metadata != p.Metadata || observed.revision != ledger.original.Target.ObservationRevision || !installedCoverageComplete(observed.coverage) {
				return nil, ErrUnsupportedOperation
			}
			if p.Exists {
				if !allowedInstalledMetadata(p.Metadata, true) || p.Identity == "" || p.Mode != p.Metadata.FullMode || p.Metadata.Volume != ledger.original.Target.Volume {
					return nil, ErrUnsupportedOperation
				}
				parent = p.Identity
			}
		}
		if f.parentIdentity != parent || parent == "" {
			return nil, ErrUnsupportedOperation
		}
	}
	if issued.stage.parentIdentity != ledger.original.Target.Identity || issued.stage.revision != ledger.original.Target.CapabilityRevision || !installedCoverageComplete(issued.stage.coverage) || issued.stage.creation != ledger.request.Installed.Capabilities.Creation {
		return nil, ErrUnsupportedOperation
	}
	owned := cloneIssuedInstalled(*issued)
	detached, err := freezeInstalledOriginal(ledger.request)
	if err != nil {
		return nil, err
	}
	return &installedMetadataAdmission{ledger: detached, issued: owned, seal: owned.integrity}, nil
}

// Each use must supply fresh observed comparison data AFTER external callbacks.
// The original ledger/window is never reminted or renewed from that data.
func revalidateInstalledAdmission(ctx context.Context, a *installedMetadataAdmission, current InstalledOriginalContext, now time.Time) error {
	if a == nil || a.seal != a.issued.integrity {
		return ErrUnsupportedOperation
	}
	_, err := verifyInstalledAttestation(ctx, a.ledger, &a.issued, current, now)
	return err
}

// Postcreation provenance is separate from the pre-mkdir inheritance bound.
// No production issuer exists; public InstalledTemp values cannot issue it.
type issuedInstalledStageObservation struct {
	issuer                     *installedIssuerSeal
	requestSeal, admissionSeal [32]byte
	stage                      InstalledTemp
	parentIdentity, revision   string
	observedAt, validUntil     time.Time
	coverage                   installedCoverage
	integrity                  [32]byte
}

func verifyInstalledStageObservation(ctx context.Context, a *installedMetadataAdmission, current InstalledOriginalContext, proof *issuedInstalledStageObservation, stage InstalledTemp, now time.Time) error {
	if err := revalidateInstalledAdmission(ctx, a, current, now); err != nil {
		return err
	}
	if proof == nil || proof.integrity != installedStageIntegrity(*proof) || proof.issuer == nil || proof.issuer.binding != a.ledger.original.Expected || proof.requestSeal != a.ledger.seal || proof.admissionSeal != a.seal || proof.stage != stage || proof.parentIdentity != a.ledger.original.Target.Identity || proof.revision == "" || !installedCoverageComplete(proof.coverage) || !installedWindow(proof.observedAt, proof.validUntil) || proof.observedAt.Before(a.issued.observedAt) || proof.validUntil.After(a.issued.validUntil) || now.Before(proof.observedAt) || !now.Before(proof.validUntil) {
		return ErrUnsupportedOperation
	}
	if stage.Path == "" || strings.Contains(stage.Path, "/") || !strings.HasPrefix(stage.Path, ".installed-stage-") || stage.Identity == "" || stage.Phase != InstalledIntent || stage.Metadata != expectedInstalledDirectoryMetadata(a.issued.stage.creation, 0700) {
		return ErrUnsupportedOperation
	}
	return nil
}
func installedDigestKnown(d artifact.Digest) bool {
	return d.Algorithm == "sha256" && installedInputDigest("sha256:"+d.Hex)
}
func installedKeyGrantsMatch(g InstalledOriginalGrant, f InstalledFileChange) bool {
	if g.WholeFile {
		return len(f.KeysBefore) == 0 && len(f.KeysAfter) == 0
	}
	if len(g.KeyPaths) != len(f.KeysBefore) || len(g.KeyPaths) != len(f.KeysAfter) {
		return false
	}
	for i, p := range g.KeyPaths {
		if !slices.Equal(p, []string(f.KeysBefore[i].Path)) || !slices.Equal(p, []string(f.KeysAfter[i].Path)) || !f.KeysAfter[i].Exists {
			return false
		}
	}
	return true
}

func installedCoverageBytes(c installedCoverage) []byte {
	b := []byte{0, 0, 0, 0}
	for i, v := range []bool{c.allNamespaces, c.acl, c.flags, c.inheritance} {
		if v {
			b[i] = 1
		}
	}
	return b
}

func installedStageIntegrity(p issuedInstalledStageObservation) [32]byte {
	b, _ := json.Marshal(struct {
		Request, Admission [32]byte
		Stage              InstalledTemp
		Parent, Revision   string
		At, Until          time.Time
	}{p.requestSeal, p.admissionSeal, p.stage, p.parentIdentity, p.revision, p.observedAt, p.validUntil})
	b = append(b, installedCoverageBytes(p.coverage)...)
	return sha256.Sum256(append([]byte("workspace.installed.stage.v1\x00"), b...))
}
