package materialize

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/substrate/harness/workspace/keymerge"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
)

// This fixture is SYNTHETIC PRIVATE CONSUMER evidence. It never calls native
// metadata, obtains an OS capability, or enables the public installed engine.
func syntheticInstalledAdmission(t *testing.T) (Request, installedOriginalLedger, issuedInstalledAttestation, time.Time) {
	t.Helper()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	creation := InstalledMetadata{UID: 1000, GID: 1000, Volume: "synthetic-volume", Complete: true, ACLAbsent: true, XattrsAbsent: true}
	directory := creation
	directory.FullMode = 0755
	directory.Links = 2
	privateDir := directory
	privateDir.FullMode = 0700
	leaf := expectedInstalledMetadata(creation, 0600)
	base := t.TempDir()
	root := func(id string, m InstalledMetadata) InstalledOriginalRoot {
		return InstalledOriginalRoot{ID: id, Path: filepath.Join(base, id), AllowedBase: base, Owner: "synthetic-owner", Provenance: "synthetic-host", Identity: "synthetic-inode-" + id, Volume: creation.Volume, CapabilityRevision: installedMetadataRevision, ObservationRevision: "synthetic-observation-1", Metadata: m}
	}
	before, after := []byte("old"), []byte("new")
	c := &InstalledOriginalContext{SchemaVersion: "workspace.v1", OperationID: "synthetic-operation", InputDigest: "sha256:" + strings.Repeat("a", 64), Identity: InstalledOriginalIdentity{AgentURN: "urn:fixture:agent", EncodedKey: "fixture-key", Instance: "fixture-instance", Session: "fixture-session", Assignment: "fixture-assignment", DefinitionRevision: "fixture-revision", SemanticDigest: artifact.DigestBytes([]byte("semantic")), ArtifactDigest: artifact.DigestBytes([]byte("artifact")), DependencyDigest: artifact.DigestBytes([]byte("dependency"))}, Fence: InstalledOriginalFence{ID: "synthetic-fence", Path: filepath.Join(base, "fence"), Revision: "1", Provenance: "synthetic-host"}, Target: root("target", directory), Control: root("control", privateDir), Lock: root("lock", privateDir), ObservedAt: at, ExpiresAt: at.Add(time.Minute), Expected: InstalledExpectedIssuer{Issuer: "synthetic-issuer", Schema: "synthetic-schema", Implementation: "synthetic-version", Backend: "synthetic-backend", OS: "synthetic-os", Kernel: "synthetic-kernel", Filesystem: "synthetic-fs", Mount: "synthetic-mount", Volume: creation.Volume, SecurityContext: "synthetic-security", UserNamespace: "synthetic-namespace", CredentialVisibility: "synthetic-credentials"}, Grants: []InstalledOriginalGrant{{ID: "file-grant", Version: "1", Path: "fixture.txt", WholeFile: true}}, Authorizations: []InstalledOriginalAuthorization{{Kind: "installed_artifacts", RootID: "target", AuthorizationID: "file-grant", Version: "1"}, {Kind: "directory", RootID: "control", AuthorizationID: "control-grant", Version: "1"}}, Files: []InstalledOriginalFile{{Path: "fixture.txt", Exists: true, Kind: artifact.EntryFile, Identity: "synthetic-file-inode", Mode: 0600, Digest: artifact.DigestBytes(before), Metadata: leaf}}}
	cap := InstalledCapabilities{Volume: creation.Volume, RootIdentity: c.Target.Identity, Revision: installedMetadataRevision, ObservationRevision: c.Target.ObservationRevision, CaseMode: CaseSensitive, Creation: creation, RootMetadata: directory}
	req := Request{InstalledOriginalContext: c, Operation: OperationInstall, TargetRoot: c.Target.Path, Generation: c.InputDigest, Artifacts: artifact.Tree{Entries: []artifact.Entry{{Path: "fixture.txt", Kind: artifact.EntryFile, Mode: 0600, Bytes: after}}}, Installed: &InstalledPolicy{Capabilities: cap, CaseMode: CaseSensitive, TargetIdentity: c.Target.Identity, ControlIdentity: c.Control.Identity, Files: []InstalledFileChange{{Path: "fixture.txt", BeforeExists: true, BeforeIdentity: c.Files[0].Identity, Before: artifact.DigestBytes(before), After: artifact.DigestBytes(after), BeforeMode: 0600, AfterMode: 0600, BeforeMetadata: leaf, AfterMetadata: leaf, GrantID: "file-grant", GrantVersion: "1", Phase: InstalledPrepared}}}}
	ledger, err := freezeInstalledOriginal(req)
	if err != nil {
		t.Fatal(err)
	}
	coverage := installedCoverage{true, true, true, true}
	issued := issuedInstalledAttestation{issuer: &installedIssuerSeal{binding: c.Expected}, original: ledger.original, requestSeal: ledger.seal, roots: [3]InstalledMetadata{directory, privateDir, privateDir}, rootCoverage: [3]installedCoverage{coverage, coverage, coverage}, files: []installedFileAttestation{{path: "fixture.txt", identity: c.Files[0].Identity, parentIdentity: c.Target.Identity, revision: c.Target.ObservationRevision, metadata: leaf, creation: creation, coverage: coverage}}, stage: installedStageCapability{parentIdentity: c.Target.Identity, revision: c.Target.CapabilityRevision, creation: creation, coverage: coverage}, revision: c.Target.ObservationRevision, observedAt: at, validUntil: at.Add(time.Minute)}
	issued = cloneIssuedInstalled(issued)
	issued.integrity = installedAttestationIntegrity(issued)
	if _, err := verifyInstalledAttestation(context.Background(), ledger, &issued, ledger.original, at.Add(time.Second)); err != nil {
		t.Fatalf("synthetic positive failed: %v", err)
	}
	return req, ledger, issued, at.Add(time.Second)
}

func TestMetadataPrivateOriginalBindings(t *testing.T) {
	// Recompute comparison integrity after each adversarial change: rejection
	// must come from the admitted original binding, not merely a stale hash.
	mutations := map[string]func(*InstalledOriginalContext){
		"schema":     func(c *InstalledOriginalContext) { c.SchemaVersion = "foreign" },
		"operation":  func(c *InstalledOriginalContext) { c.OperationID = "foreign" },
		"input":      func(c *InstalledOriginalContext) { c.InputDigest = "sha256:" + strings.Repeat("b", 64) },
		"agent":      func(c *InstalledOriginalContext) { c.Identity.AgentURN = "foreign" },
		"key":        func(c *InstalledOriginalContext) { c.Identity.EncodedKey = "foreign" },
		"instance":   func(c *InstalledOriginalContext) { c.Identity.Instance = "foreign" },
		"session":    func(c *InstalledOriginalContext) { c.Identity.Session = "foreign" },
		"assignment": func(c *InstalledOriginalContext) { c.Identity.Assignment = "foreign" },
		"definition": func(c *InstalledOriginalContext) { c.Identity.DefinitionRevision = "foreign" },
		"semantic":   func(c *InstalledOriginalContext) { c.Identity.SemanticDigest = artifact.DigestBytes([]byte("foreign")) },
		"artifact":   func(c *InstalledOriginalContext) { c.Identity.ArtifactDigest = artifact.DigestBytes([]byte("foreign")) },
		"dependency": func(c *InstalledOriginalContext) {
			c.Identity.DependencyDigest = artifact.DigestBytes([]byte("foreign"))
		},
		"fence":          func(c *InstalledOriginalContext) { c.Fence.ID = "foreign" },
		"fence-revision": func(c *InstalledOriginalContext) { c.Fence.Revision = "foreign" },
		"target-inode":   func(c *InstalledOriginalContext) { c.Target.Identity = "foreign" },
		"target-path":    func(c *InstalledOriginalContext) { c.Target.Path = filepath.Join(c.Target.AllowedBase, "foreign") },
		"control-inode":  func(c *InstalledOriginalContext) { c.Control.Identity = "foreign" },
		"lock-inode":     func(c *InstalledOriginalContext) { c.Lock.Identity = "foreign" },
		"volume":         func(c *InstalledOriginalContext) { c.Target.Volume = "foreign" },
		"observation":    func(c *InstalledOriginalContext) { c.Target.ObservationRevision = "foreign" },
		"capability":     func(c *InstalledOriginalContext) { c.Target.CapabilityRevision = "foreign" },
		"grant":          func(c *InstalledOriginalContext) { c.Grants[0].ID = "foreign" },
		"grant-version":  func(c *InstalledOriginalContext) { c.Grants[0].Version = "foreign" },
		"grant-scope":    func(c *InstalledOriginalContext) { c.Grants[0].Path = "foreign" },
		"whole-vs-key": func(c *InstalledOriginalContext) {
			c.Grants[0].WholeFile = false
			c.Grants[0].KeyPaths = [][]string{{"foreign"}}
		},
		"authority":    func(c *InstalledOriginalContext) { c.Authorizations[0].AuthorizationID = "foreign" },
		"file-inode":   func(c *InstalledOriginalContext) { c.Files[0].Identity = "foreign" },
		"window-renew": func(c *InstalledOriginalContext) { c.ExpiresAt = c.ExpiresAt.Add(time.Minute) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			_, ledger, issued, now := syntheticInstalledAdmission(t)
			mutate(&issued.original)
			issued.integrity = installedAttestationIntegrity(issued)
			if a, err := verifyInstalledAttestation(context.Background(), ledger, &issued, ledger.original, now); err == nil || a != nil {
				t.Fatal("accepted foreign original")
			}
		})
	}
}

func TestMetadataIssuerVisibilityAndCoverage(t *testing.T) {
	for _, name := range []string{"Issuer", "Schema", "Implementation", "Backend", "OS", "Kernel", "Filesystem", "Mount", "Volume", "SecurityContext", "UserNamespace", "CredentialVisibility"} {
		t.Run(name, func(t *testing.T) {
			_, ledger, issued, now := syntheticInstalledAdmission(t)
			reflect.ValueOf(&issued.issuer.binding).Elem().FieldByName(name).SetString("foreign")
			issued.integrity = installedAttestationIntegrity(issued)
			if _, err := verifyInstalledAttestation(context.Background(), ledger, &issued, ledger.original, now); err == nil {
				t.Fatal("accepted wrong issuer visibility binding")
			}
		})
	}
	mutations := map[string]func(*issuedInstalledAttestation){
		"missing-issuer":            func(e *issuedInstalledAttestation) { e.issuer = nil },
		"root-hidden-xattr":         func(e *issuedInstalledAttestation) { e.rootCoverage[0].allNamespaces = false },
		"control-hidden-acl":        func(e *issuedInstalledAttestation) { e.rootCoverage[1].acl = false },
		"lock-flags-unknown":        func(e *issuedInstalledAttestation) { e.rootCoverage[2].flags = false },
		"file-hidden-xattr":         func(e *issuedInstalledAttestation) { e.files[0].coverage.allNamespaces = false },
		"file-acl-unknown":          func(e *issuedInstalledAttestation) { e.files[0].coverage.acl = false },
		"file-flags-unknown":        func(e *issuedInstalledAttestation) { e.files[0].coverage.flags = false },
		"file-inheritance-unknown":  func(e *issuedInstalledAttestation) { e.files[0].coverage.inheritance = false },
		"stage-inheritance-unknown": func(e *issuedInstalledAttestation) { e.stage.coverage.inheritance = false },
		"stage-hidden-xattr":        func(e *issuedInstalledAttestation) { e.stage.coverage.allNamespaces = false },
		"root-uid":                  func(e *issuedInstalledAttestation) { e.roots[0].UID++ },
		"root-gid":                  func(e *issuedInstalledAttestation) { e.roots[0].GID++ },
		"root-mode":                 func(e *issuedInstalledAttestation) { e.roots[0].FullMode = 0700 },
		"root-links":                func(e *issuedInstalledAttestation) { e.roots[0].Links++ },
		"root-flags":                func(e *issuedInstalledAttestation) { e.roots[0].Flags = 1 },
		"root-xattrs":               func(e *issuedInstalledAttestation) { e.roots[0].XattrsAbsent = false },
		"root-acl":                  func(e *issuedInstalledAttestation) { e.roots[0].ACLAbsent = false },
		"root-volume":               func(e *issuedInstalledAttestation) { e.roots[0].Volume = "foreign" },
		"file-uid":                  func(e *issuedInstalledAttestation) { e.files[0].metadata.UID++ },
		"file-gid":                  func(e *issuedInstalledAttestation) { e.files[0].metadata.GID++ },
		"file-mode":                 func(e *issuedInstalledAttestation) { e.files[0].metadata.FullMode = 0644 },
		"file-links":                func(e *issuedInstalledAttestation) { e.files[0].metadata.Links = 2 },
		"file-flags":                func(e *issuedInstalledAttestation) { e.files[0].metadata.Flags = 1 },
		"file-acl":                  func(e *issuedInstalledAttestation) { e.files[0].metadata.ACLAbsent = false },
		"file-xattrs":               func(e *issuedInstalledAttestation) { e.files[0].metadata.XattrsAbsent = false },
		"file-volume":               func(e *issuedInstalledAttestation) { e.files[0].metadata.Volume = "foreign" },
		"file-inode":                func(e *issuedInstalledAttestation) { e.files[0].identity = "foreign" },
		"parent-inode":              func(e *issuedInstalledAttestation) { e.files[0].parentIdentity = "foreign" },
		"file-revision":             func(e *issuedInstalledAttestation) { e.files[0].revision = "foreign" },
		"root-revision":             func(e *issuedInstalledAttestation) { e.revision = "foreign" },
		"stage-parent":              func(e *issuedInstalledAttestation) { e.stage.parentIdentity = "foreign" },
		"stage-revision":            func(e *issuedInstalledAttestation) { e.stage.revision = "foreign" },
		"stage-creation":            func(e *issuedInstalledAttestation) { e.stage.creation.UID++ },
		"file-creation":             func(e *issuedInstalledAttestation) { e.files[0].creation.UID++ },
		"expired":                   func(e *issuedInstalledAttestation) { e.validUntil = e.observedAt.Add(time.Millisecond) },
		"renewed":                   func(e *issuedInstalledAttestation) { e.validUntil = e.validUntil.Add(time.Second) },
		"old-start":                 func(e *issuedInstalledAttestation) { e.observedAt = e.observedAt.Add(-time.Second) },
		"future":                    func(e *issuedInstalledAttestation) { e.observedAt = e.observedAt.Add(10 * time.Second) },
		"request-seal":              func(e *issuedInstalledAttestation) { e.requestSeal = [32]byte{} },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			_, ledger, issued, now := syntheticInstalledAdmission(t)
			mutate(&issued)
			issued.integrity = installedAttestationIntegrity(issued)
			if a, err := verifyInstalledAttestation(context.Background(), ledger, &issued, ledger.original, now); err == nil || a != nil {
				t.Fatal("accepted incomplete or changed descriptor proof")
			}
		})
	}
}

func TestMetadataDetachedPrivateAdmission(t *testing.T) {
	req, ledger, issued, now := syntheticInstalledAdmission(t)
	a, err := verifyInstalledAttestation(context.Background(), ledger, &issued, ledger.original, now)
	if err != nil {
		t.Fatal(err)
	}
	// Caller callbacks can mutate every public backing slice. The privately earned
	// snapshot neither follows that mutation nor allows it to replace current proof.
	req.Artifacts.Entries[0].Bytes[0] = 'x'
	req.Installed.Files[0].GrantID = "foreign"
	req.InstalledOriginalContext.OperationID = "foreign"
	if err := revalidateInstalledAdmission(context.Background(), a, a.ledger.original, now); err != nil {
		t.Fatal("caller alias corrupted private original", err)
	}
	if err := revalidateInstalledAdmission(context.Background(), a, *req.InstalledOriginalContext, now); err == nil {
		t.Fatal("accepted replaced current authority")
	}
	ledger.request.Installed.Files[0].GrantID = "foreign"
	issued.files[0].identity = "foreign"
	issued.issuer.binding.Issuer = "foreign"
	if err := revalidateInstalledAdmission(context.Background(), a, a.ledger.original, now); err != nil {
		t.Fatal("admission aliased input ledger/envelope", err)
	}
	b, _ := json.Marshal(a)
	var decoded installedMetadataAdmission
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := revalidateInstalledAdmission(context.Background(), &decoded, a.ledger.original, now); err == nil {
		t.Fatal("JSON constructed admission")
	}
	var decodedIssuer issuedInstalledAttestation
	b, _ = json.Marshal(a.issued)
	_ = json.Unmarshal(b, &decodedIssuer)
	if _, err := verifyInstalledAttestation(context.Background(), a.ledger, &decodedIssuer, a.ledger.original, now); err == nil {
		t.Fatal("JSON constructed issuer")
	}
	// Data JSON is permitted and detached; a matching public hash and Complete
	// booleans still cannot issue provenance through the production boundary.
	b, _ = json.Marshal(a.ledger.request)
	var roundtrip Request
	_ = json.Unmarshal(b, &roundtrip)
	if _, err := admitInstalledOriginal(context.Background(), roundtrip); !errors.Is(err, ErrUnsupportedOperation) {
		t.Fatal("data-only context enabled production admission", err)
	}
	if _, err := verifyInstalledAttestation(context.Background(), a.ledger, nil, a.ledger.original, now); err == nil {
		t.Fatal("hash/metadata issued missing provenance")
	}
}

func TestMetadataLateAuthorityExpiryCancellation(t *testing.T) {
	_, ledger, issued, now := syntheticInstalledAdmission(t)
	a, err := verifyInstalledAttestation(context.Background(), ledger, &issued, ledger.original, now)
	if err != nil {
		t.Fatal(err)
	}
	current := copySyntheticOriginal(t, ledger.original)
	for _, phase := range []string{"validate", "observe", "clock", "record", "completion"} {
		t.Run(phase, func(t *testing.T) {
			late := copySyntheticOriginal(t, current)
			late.Fence.Revision = "revoked"
			if err := revalidateInstalledAdmission(context.Background(), a, late, now); err == nil {
				t.Fatal("late callback authority admitted")
			}
		})
	}
	if err := revalidateInstalledAdmission(context.Background(), a, current, ledger.original.ExpiresAt); err == nil {
		t.Fatal("expired original admitted")
	}
	refreshed := copySyntheticOriginal(t, current)
	refreshed.ObservedAt = refreshed.ExpiresAt
	refreshed.ExpiresAt = refreshed.ExpiresAt.Add(time.Minute)
	if err := revalidateInstalledAdmission(context.Background(), a, refreshed, refreshed.ObservedAt); err == nil {
		t.Fatal("refreshed expired original into success")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := revalidateInstalledAdmission(ctx, a, current, now); !errors.Is(err, context.Canceled) {
		t.Fatal("lost cancellation", err)
	}
	if _, err := verifyInstalledAttestation(nil, ledger, &issued, current, now); err == nil {
		t.Fatal("nil context admitted")
	}
}
func copySyntheticOriginal(t *testing.T, c InstalledOriginalContext) InstalledOriginalContext {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var out InstalledOriginalContext
	if err = json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMetadataStagePostcreationRequiresSeparatePrivateProof(t *testing.T) {
	_, ledger, issued, now := syntheticInstalledAdmission(t)
	a, err := verifyInstalledAttestation(context.Background(), ledger, &issued, ledger.original, now)
	if err != nil {
		t.Fatal(err)
	}
	stage := InstalledTemp{Path: ".installed-stage-fixture", Identity: "synthetic-stage-inode", Phase: InstalledIntent, Metadata: expectedInstalledDirectoryMetadata(issued.stage.creation, 0700)}
	p := issuedInstalledStageObservation{issuer: issued.issuer, requestSeal: ledger.seal, admissionSeal: a.seal, stage: stage, parentIdentity: ledger.original.Target.Identity, revision: "synthetic-stage-observation", observedAt: issued.observedAt, validUntil: issued.validUntil, coverage: installedCoverage{true, true, true, true}}
	p.integrity = installedStageIntegrity(p)
	if err := verifyInstalledStageObservation(context.Background(), a, ledger.original, &p, stage, now); err != nil {
		t.Fatal("synthetic stage positive", err)
	}
	if err := verifyInstalledStageObservation(context.Background(), a, ledger.original, nil, stage, now); err == nil {
		t.Fatal("inheritance proof replaced postcreation observation")
	}
	for _, name := range []string{"inode", "metadata", "parent", "issuer", "request", "admission", "hidden", "expiry", "revision"} {
		t.Run(name, func(t *testing.T) {
			q := p
			actual := stage
			switch name {
			case "inode":
				actual.Identity = "foreign"
			case "metadata":
				actual.Metadata.FullMode = 0755
			case "parent":
				q.parentIdentity = "foreign"
			case "issuer":
				q.issuer = nil
			case "request":
				q.requestSeal = [32]byte{}
			case "admission":
				q.admissionSeal = [32]byte{}
			case "hidden":
				q.coverage.allNamespaces = false
			case "expiry":
				q.validUntil = now
			case "revision":
				q.revision = ""
			}
			q.integrity = installedStageIntegrity(q)
			if err := verifyInstalledStageObservation(context.Background(), a, ledger.original, &q, actual, now); err == nil {
				t.Fatal("accepted changed stage proof")
			}
		})
	}
	q := p
	q.stage.Identity = "changed"
	if err := verifyInstalledStageObservation(context.Background(), a, ledger.original, &q, q.stage, now); err == nil {
		t.Fatal("changed private stage snapshot without integrity")
	}
}

func TestMetadataOriginalRequestMismatchAndBounds(t *testing.T) {
	cases := map[string]func(*Request){
		"missing":        func(r *Request) { r.InstalledOriginalContext = nil },
		"input":          func(r *Request) { r.Generation = "foreign" },
		"policy-target":  func(r *Request) { r.Installed.TargetIdentity = "foreign" },
		"policy-control": func(r *Request) { r.Installed.ControlIdentity = "foreign" },
		"policy-grant":   func(r *Request) { r.Installed.Files[0].GrantVersion = "foreign" },
		"artifact":       func(r *Request) { r.Artifacts.Entries[0].Bytes = []byte("foreign") },
		"artifact-mode":  func(r *Request) { r.Artifacts.Entries[0].Mode = 0644 },
		"full-vs-keys": func(r *Request) {
			r.Installed.Files[0].KeysAfter = []keymerge.KeyState{{Path: keymerge.KeyPath{"foreign"}, Exists: true}}
		},
		"duplicate": func(r *Request) {
			r.InstalledOriginalContext.Grants = append(r.InstalledOriginalContext.Grants, r.InstalledOriginalContext.Grants[0])
		},
		"authorization":   func(r *Request) { r.InstalledOriginalContext.Authorizations = nil },
		"metadata":        func(r *Request) { r.InstalledOriginalContext.Files[0].Metadata.UID++ },
		"observed-window": func(r *Request) { r.InstalledOriginalContext.ExpiresAt = r.InstalledOriginalContext.ObservedAt },
		"root-overlap":    func(r *Request) { r.InstalledOriginalContext.Lock.Path = r.TargetRoot },
		"root-relative":   func(r *Request) { r.InstalledOriginalContext.Control.Path = "relative" },
		"bounds":          func(r *Request) { r.InstalledOriginalContext.Expected.Issuer = strings.Repeat("x", 4097) },
		"nul":             func(r *Request) { r.InstalledOriginalContext.Expected.Issuer = "x\x00y" },
		"utf8":            func(r *Request) { r.InstalledOriginalContext.Expected.Issuer = string([]byte{255}) },
		"collection":      func(r *Request) { r.InstalledOriginalContext.Grants = make([]InstalledOriginalGrant, 4097) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			req, _, _, _ := syntheticInstalledAdmission(t)
			change(&req)
			if _, err := freezeInstalledOriginal(req); err == nil {
				t.Fatal("accepted foreign/unbounded original request")
			}
		})
	}
}

func TestMetadataProductionNeverIssuesFromPublicContext(t *testing.T) {
	req, ledger, _, _ := syntheticInstalledAdmission(t)
	callbacks := 0
	e := NewEngine(EngineOptions{Now: func() time.Time { callbacks++; return ledger.original.ObservedAt }, BeforeInstalledCommit: func(context.Context, InstalledFileChange) error { callbacks++; return nil }})
	for _, roundtrip := range []bool{false, true} {
		candidate := req
		if roundtrip {
			raw, _ := json.Marshal(req)
			if err := json.Unmarshal(raw, &candidate); err != nil {
				t.Fatal(err)
			}
		}
		plan, err := e.Plan(context.Background(), candidate)
		if !errors.Is(err, ErrUnsupportedOperation) || plan.WillMutate {
			t.Fatalf("public data issued plan: %v", err)
		}
		h, err := e.Apply(context.Background(), candidate)
		if !errors.Is(err, ErrUnsupportedOperation) || h.Mutated || len(h.Retained) > 0 || callbacks != 0 {
			t.Fatalf("public DTO reached callbacks or mutation: %v %+v", err, h)
		}
	}
}

func TestMetadataKeyGrantPathsAreComponents(t *testing.T) {
	req, _, _, _ := syntheticInstalledAdmission(t)
	g := &req.InstalledOriginalContext.Grants[0]
	g.WholeFile = false
	g.KeyPaths = [][]string{{"section", "key"}}
	f := &req.Installed.Files[0]
	f.KeysBefore = []keymerge.KeyState{{Path: keymerge.KeyPath{"section", "key"}}}
	f.KeysAfter = []keymerge.KeyState{{Path: keymerge.KeyPath{"section", "key"}, Exists: true, Digest: "fixture"}}
	if _, err := freezeInstalledOriginal(req); err != nil {
		t.Fatal("exact synthetic component grant refused", err)
	}
	f.KeysAfter[0].Path = keymerge.KeyPath{"section.key"}
	if _, err := freezeInstalledOriginal(req); err == nil {
		t.Fatal("dotted string replaced component authority")
	}
}

func TestMetadataParentsRequireCompleteDescriptorEvidence(t *testing.T) {
	req, _, issued, now := syntheticInstalledAdmission(t)
	parent := InstalledDirectoryChange{Path: "fixture-dir", Identity: "synthetic-parent-inode", Exists: true, Mode: 0755, Metadata: req.InstalledOriginalContext.Target.Metadata}
	req.Artifacts.Entries[0].Path = "fixture-dir/fixture.txt"
	req.Installed.Files[0].Path = req.Artifacts.Entries[0].Path
	req.InstalledOriginalContext.Grants[0].Path = req.Artifacts.Entries[0].Path
	req.InstalledOriginalContext.Files[0].Path = req.Artifacts.Entries[0].Path
	req.InstalledOriginalContext.Files[0].Parents = []InstalledDirectoryChange{parent}
	req.Installed.Directories = []InstalledDirectoryChange{parent}
	ledger, err := freezeInstalledOriginal(req)
	if err != nil {
		t.Fatal(err)
	}
	issued.original = copySyntheticOriginal(t, ledger.original)
	issued.requestSeal = ledger.seal
	issued.files[0].path = req.Artifacts.Entries[0].Path
	issued.files[0].parentIdentity = parent.Identity
	issued.files[0].parents = []installedParentAttestation{{path: parent.Path, identity: parent.Identity, revision: ledger.original.Target.ObservationRevision, metadata: parent.Metadata, coverage: installedCoverage{true, true, true, true}}}
	issued.integrity = installedAttestationIntegrity(issued)
	a, err := verifyInstalledAttestation(context.Background(), ledger, &issued, ledger.original, now)
	if err != nil {
		t.Fatal("synthetic parent positive", err)
	}
	for _, name := range []string{"missing", "inode", "uid", "gid", "mode", "links", "volume", "acl", "xattrs", "flags", "coverage", "revision"} {
		t.Run(name, func(t *testing.T) {
			e := cloneIssuedInstalled(issued)
			switch name {
			case "missing":
				e.files[0].parents = nil
			case "inode":
				e.files[0].parents[0].identity = "foreign"
			case "uid":
				e.files[0].parents[0].metadata.UID++
			case "gid":
				e.files[0].parents[0].metadata.GID++
			case "mode":
				e.files[0].parents[0].metadata.FullMode = 0700
			case "links":
				e.files[0].parents[0].metadata.Links++
			case "volume":
				e.files[0].parents[0].metadata.Volume = "foreign"
			case "acl":
				e.files[0].parents[0].metadata.ACLAbsent = false
			case "xattrs":
				e.files[0].parents[0].metadata.XattrsAbsent = false
			case "flags":
				e.files[0].parents[0].metadata.Flags = 1
			case "coverage":
				e.files[0].parents[0].coverage.allNamespaces = false
			case "revision":
				e.files[0].parents[0].revision = "foreign"
			}
			e.integrity = installedAttestationIntegrity(e)
			if _, err := verifyInstalledAttestation(context.Background(), ledger, &e, ledger.original, now); err == nil {
				t.Fatal("accepted incomplete parent descriptor")
			}
		})
	}
	issued.files[0].parents[0].identity = "caller-alias"
	if err := revalidateInstalledAdmission(context.Background(), a, a.ledger.original, now); err != nil {
		t.Fatal("nested parent slice leaked into private admission", err)
	}
	req.InstalledOriginalContext.Files[0].Parents = nil
	req.Installed.Directories = nil
	if _, err := freezeInstalledOriginal(req); err == nil {
		t.Fatal("omitted ancestry observation")
	}
}

func TestMetadataMissingExpectedBindingsStayUnknown(t *testing.T) {
	for _, name := range []string{"Issuer", "Schema", "Implementation", "Backend", "OS", "Kernel", "Filesystem", "Mount", "Volume", "SecurityContext", "UserNamespace", "CredentialVisibility"} {
		t.Run(name, func(t *testing.T) {
			req, _, e, now := syntheticInstalledAdmission(t)
			reflect.ValueOf(&req.InstalledOriginalContext.Expected).Elem().FieldByName(name).SetString("")
			ledger, err := freezeInstalledOriginal(req)
			if err != nil {
				t.Fatal(err)
			}
			e.original = copySyntheticOriginal(t, ledger.original)
			e.requestSeal = ledger.seal
			e.issuer.binding = e.original.Expected
			e.integrity = installedAttestationIntegrity(e)
			if _, err := verifyInstalledAttestation(context.Background(), ledger, &e, ledger.original, now); err == nil {
				t.Fatal("matching synthetic marker manufactured missing expectation")
			}
		})
	}
}

func TestMetadataAbsentLeafCannotAdmitExistingMetadata(t *testing.T) {
	req, _, e, now := syntheticInstalledAdmission(t)
	req.InstalledOriginalContext.Files[0].Exists = false
	req.InstalledOriginalContext.Files[0].Identity = ""
	req.InstalledOriginalContext.Files[0].Metadata = InstalledMetadata{}
	req.InstalledOriginalContext.Files[0].Mode = 0
	req.InstalledOriginalContext.Files[0].Digest = artifact.Digest{}
	req.InstalledOriginalContext.Files[0].Kind = ""
	f := &req.Installed.Files[0]
	f.BeforeExists = false
	f.BeforeIdentity = ""
	f.BeforeMetadata = InstalledMetadata{}
	f.BeforeMode = 0
	f.Before = artifact.Digest{}
	ledger, err := freezeInstalledOriginal(req)
	if err != nil {
		t.Fatal(err)
	}
	e.original = copySyntheticOriginal(t, ledger.original)
	e.requestSeal = ledger.seal
	e.files[0].identity = ""
	e.files[0].metadata = InstalledMetadata{}
	e.integrity = installedAttestationIntegrity(e)
	if _, err := verifyInstalledAttestation(context.Background(), ledger, &e, ledger.original, now); err != nil {
		t.Fatal("synthetic absent positive", err)
	}
	e.files[0].metadata = f.AfterMetadata
	e.integrity = installedAttestationIntegrity(e)
	if _, err := verifyInstalledAttestation(context.Background(), ledger, &e, ledger.original, now); err == nil {
		t.Fatal("absent bound descriptor claimed existing metadata")
	}
}

func TestMetadataPrivateEnvelopeIntegrityIsNotMutableWindow(t *testing.T) {
	_, ledger, e, now := syntheticInstalledAdmission(t)
	e.validUntil = e.validUntil.Add(-time.Second) // still fresh and inside original window
	if _, err := verifyInstalledAttestation(context.Background(), ledger, &e, ledger.original, now); err == nil {
		t.Fatal("accepted changed issued comparison snapshot")
	}
	// Integrity is not provenance: recomputing a DTO hash cannot supply issuer.
	e.integrity = installedAttestationIntegrity(e)
	e.issuer = nil
	if _, err := verifyInstalledAttestation(context.Background(), ledger, &e, ledger.original, now); err == nil {
		t.Fatal("integrity issued authority")
	}
}
