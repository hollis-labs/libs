package workspace_test

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hollis-labs/substrate/harness/sandbox"
	"github.com/hollis-labs/substrate/harness/workspace"
	"github.com/hollis-labs/substrate/harness/workspace/bootkey"
	"github.com/hollis-labs/substrate/harness/workspace/materialize"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
)

func root(id string) workspace.RootRef {
	base := filepath.Join(string(filepath.Separator), "fixture")
	return workspace.RootRef{ID: id, Path: filepath.Join(base, id), AllowedBase: base, Owner: "fixture-owner", Provenance: "fixture"}
}
func spec(t *testing.T) workspace.Spec {
	t.Helper()
	key, err := bootkey.Encode("urn:fixture:agent:one")
	if err != nil {
		t.Fatal(err)
	}
	d := artifact.DigestBytes([]byte("fixture"))
	boot := root("boot")
	current, candidate := root("current"), root("candidate")
	current.Path = filepath.Join(boot.Path, "current")
	candidate.Path = filepath.Join(boot.Path, "candidate")
	return workspace.Spec{
		SchemaVersion: workspace.SchemaVersion, OperationID: "fixture-operation", Operation: workspace.Prepare,
		Identity: workspace.IdentitySpec{AgentURN: "urn:fixture:agent:one", EncodedKey: key, Session: "fixture-session", DefinitionRevision: "fixture-revision", SemanticDigest: d, ArtifactDigest: d, DependencyDigest: d, Fence: workspace.ResourceRef{ID: "fixture-fence", Revision: "1"}},
		Home:     workspace.HomeSpec{Root: root("home"), Layout: workspace.FullHome, Continuity: workspace.Durable, Retention: workspace.Keep},
		Boot:     workspace.BootSpec{IdentityRoot: boot, Current: current, Candidate: candidate, Retention: workspace.RetainForRecovery},
		CWD:      workspace.CWDSpec{RootID: "home", Relative: "."},
		Sandbox:  workspace.SandboxSpec{Policy: sandbox.ResolvedAccessPolicy{Mode: sandbox.ConfinementDisabled}},
		Cleanup:  workspace.CleanupPolicy{Retention: workspace.Keep},
	}
}
func refusal(t *testing.T, err error, code string) {
	t.Helper()
	var got *workspace.Refusal
	if !errors.As(err, &got) || got.Code != code {
		t.Fatalf("refusal = %v, want %s", err, code)
	}
}

func TestExplicitSpecRefusals(t *testing.T) {
	if err := spec(t).Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, code string
		change     func(*workspace.Spec)
	}{
		{"schema", "unsupported_schema", func(s *workspace.Spec) { s.SchemaVersion = "future" }},
		{"operation", "unsupported_operation", func(s *workspace.Spec) { s.Operation = "reconnect" }},
		{"identity", "identity_key_mismatch", func(s *workspace.Spec) { s.Identity.AgentURN = "urn:fixture:agent:two" }},
		{"fence", "missing_identity_pin", func(s *workspace.Spec) { s.Identity.Fence.Revision = "" }},
		{"digest", "unsupported_input_digest", func(s *workspace.Spec) { s.Identity.SemanticDigest.Hex = "invalid" }},
		{"capability", "unsupported_capability", func(s *workspace.Spec) { s.Sandbox.RequiredCapabilities = []workspace.Capability{"future"} }},
		{"grant", "invalid_effect_grant", func(s *workspace.Spec) { s.Effects = []workspace.EffectGrant{{Kind: "future"}} }},
		{"continuity", "unsupported_continuity", func(s *workspace.Spec) { s.Home.Continuity = "" }},
		{"cwd", "unsafe_cwd", func(s *workspace.Spec) { s.CWD.Relative = "../escape" }},
		{"base", "unsafe_root", func(s *workspace.Spec) { s.Home.Root.AllowedBase = filepath.Join(s.Home.Root.AllowedBase, "other") }},
		{"current", "candidate_is_current", func(s *workspace.Spec) { s.Boot.Candidate = s.Boot.Current }},
		{"resume", "missing_expected_generation", func(s *workspace.Spec) { s.Operation = workspace.Resume }},
		{"confinement", "unsupported_confinement", func(s *workspace.Spec) { s.Sandbox.Policy.Mode = "" }},
		{"overwrite", "overwrite_not_authorized", func(s *workspace.Spec) { s.Boot.Reconcile.Conflict = materialize.ConflictOverwrite }},
		{"remove", "retirement_deferred", func(s *workspace.Spec) { s.Boot.Reconcile.RemoveOwned = true }},
	} {
		t.Run(tc.name, func(t *testing.T) { s := spec(t); tc.change(&s); refusal(t, s.Validate(), tc.code) })
	}
}

func tree(rel string) artifact.Tree {
	return artifact.Tree{Entries: []artifact.Entry{{Path: rel, Kind: artifact.EntryFile, Mode: 0644, Bytes: []byte("fixture"), Ownership: artifact.Ownership{EntryID: "fixture-entry", GroupID: "fixture-group"}, Provenance: artifact.Provenance{Source: "fixture"}}}}
}
func TestCredentialAndControlSlotsNeverManaged(t *testing.T) {
	for _, rel := range []string{"auth.json", "AUTH.JSON", "nested/auth.json", "auth.json/child", ".credentials.json", "oauth_creds.json", ".materialize/manifest.json", ".MATERIALIZE/manifest.json"} {
		t.Run(rel, func(t *testing.T) {
			refusal(t, workspace.ValidateManagedTree(tree(rel), nil), "reserved_artifact_path")
		})
	}
	refusal(t, workspace.ValidateManagedTree(tree("private/token"), []string{"private/token"}), "credential_artifact_collision")
	refusal(t, workspace.ValidateManagedTree(tree("private"), []string{"private/token"}), "credential_artifact_collision")
	if err := workspace.ValidateManagedTree(tree("AGENTS.md"), nil); err != nil {
		t.Fatal(err)
	}
	if err := workspace.ValidateManagedTree(artifact.Tree{}, nil); err != nil {
		t.Fatal("empty tree must be an explicit no-op:", err)
	}
	refusal(t, workspace.ValidateManagedTree(artifact.Tree{}, []string{"../escape"}), "invalid_credential_destination")
	refusal(t, workspace.ValidateManagedManifest(materialize.Manifest{}, []string{"../escape"}), "invalid_credential_destination")
	manifest := materialize.Manifest{Entries: []materialize.ManifestEntry{{Path: "auth.json", Kind: artifact.EntryFile}}}
	refusal(t, workspace.ValidateManagedManifest(manifest, nil), "credential_owned_invalid")
	bad := tree("AGENTS.md")
	bad.Entries[0].Digest = artifact.Digest{Hex: "forged"}
	refusal(t, workspace.ValidateManagedTree(bad, nil), "artifact_digest_mismatch")
	unresolved := tree("AGENTS.md")
	unresolved.Entries[0].ContentRef = &artifact.ImmutableRef{}
	refusal(t, workspace.ValidateManagedTree(unresolved, nil), "unresolved_artifact")
}

func TestLockOrderUsesPhysicalIdentityAndRejectsAmbiguity(t *testing.T) {
	a, b := root("z"), root("a")
	obs := []workspace.RootObservation{{RootID: a.ID, CanonicalPath: a.Path, CanonicalBase: a.AllowedBase, Owner: a.Owner}, {RootID: b.ID, CanonicalPath: b.Path, CanonicalBase: b.AllowedBase, Owner: b.Owner}}
	ns := filepath.Join(a.AllowedBase, "locks")
	got, err := workspace.OrderedLockKeys(ns, []workspace.RootRef{a, b, a}, obs)
	if err != nil {
		t.Fatal(err)
	}
	want := []workspace.LockKey{{Namespace: ns, CanonicalID: b.Path}, {Namespace: ns, CanonicalID: a.Path}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keys=%v, want %v", got, want)
	}
	other, err := workspace.OrderedLockKeys(ns, []workspace.RootRef{b, a}, obs)
	if err != nil || !reflect.DeepEqual(got, other) {
		t.Fatal("input order changed lock order", err)
	}
	obs[1].CanonicalPath = obs[0].CanonicalPath
	_, err = workspace.OrderedLockKeys(ns, []workspace.RootRef{a, b}, obs)
	refusal(t, err, "ambiguous_root_alias")
	_, err = workspace.OrderedLockKeys(a.Path, []workspace.RootRef{a}, obs[:1])
	refusal(t, err, "lock_namespace_inside_root")
	_, err = workspace.OrderedLockKeys(a.AllowedBase, []workspace.RootRef{a}, obs[:1])
	refusal(t, err, "lock_namespace_inside_root")
	alias := a
	alias.Path = filepath.Join(a.AllowedBase, "alias")
	_, err = workspace.OrderedLockKeys(ns, []workspace.RootRef{a, alias}, obs[:1])
	refusal(t, err, "ambiguous_root_reference")
	obs[0].Uncertainty = "fixture uncertainty"
	_, err = workspace.OrderedLockKeys(ns, []workspace.RootRef{a}, obs[:1])
	refusal(t, err, "unknown_canonical_root")
}

func TestHandBuiltReceiptCannotClaimArtifactCompletion(t *testing.T) {
	for _, r := range []workspace.ApplyResult{{}, {Status: workspace.Ready}, {Status: workspace.Partial, Receipt: workspace.Receipt{Phase: workspace.ArtifactsCommitted}}} {
		if r.ArtifactsComplete() {
			t.Fatal("hand-built result forged artifact proof")
		}
	}
}
