package workspace_test

import (
	"context"
	"encoding/json"
	"errors"
	layout "github.com/hollis-labs/substrate/harness/adapters/layout/plan"
	"github.com/hollis-labs/substrate/harness/workspace"
	"github.com/hollis-labs/substrate/harness/workspace/install"
	"github.com/hollis-labs/substrate/harness/workspace/materialize"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
	"github.com/hollis-labs/substrate/harness/workspace/render"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

type installedPorts struct {
	*fixturePorts
	control    workspace.RootRef
	records    []workspace.Receipt
	onRecord   func(workspace.Receipt) error
	onValidate func() error
}

func (f *installedPorts) ControlRoot() workspace.RootRef { return f.control }
func (f *installedPorts) Record(ctx context.Context, r workspace.Receipt) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.events = append(f.events, "record:"+string(r.Phase))
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	var detached workspace.Receipt
	if err = json.Unmarshal(b, &detached); err != nil {
		return err
	}
	f.records = append(f.records, detached)
	if f.onRecord != nil {
		return f.onRecord(r)
	}
	return nil
}
func (f *installedPorts) Validate(ctx context.Context, s workspace.Spec, r workspace.Resources) error {
	if err := f.fixturePorts.Validate(ctx, s, r); err != nil {
		return err
	}
	if f.onValidate != nil {
		return f.onValidate()
	}
	return ctx.Err()
}
func (f *installedPorts) ports() workspace.Ports {
	return workspace.Ports{Clock: f, Host: f, Locks: f, Observations: f, ReceiptStore: f}
}
func installedInputs(t *testing.T, provider runtimes.ID) (workspace.Spec, workspace.ResolvedContent, workspace.Resources, workspace.Observations, *installedPorts) {
	t.Helper()
	base := t.TempDir()
	target, control, locks := root("operator"), root("control"), root("locks")
	for _, ref := range []*workspace.RootRef{&target, &control, &locks} {
		ref.Path = filepath.Join(base, ref.ID)
		ref.AllowedBase = base
		if err := os.Mkdir(ref.Path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(target.Path, 0755); err != nil {
		t.Fatal(err)
	}
	identity := spec(t).Identity
	identity.Session = ""
	file := ".claude/settings.json"
	dir := ".claude"
	desired := []byte(`{"managed":1}`)
	slots := []string{}
	if provider == runtimes.Codex {
		file = ".codex/config.toml"
		dir = ".codex"
		desired = []byte("managed = 1\n")
		slots = []string{"mcp_servers"}
	}
	if err := os.Mkdir(filepath.Join(target.Path, dir), 0750); err != nil {
		t.Fatal(err)
	}
	note, _ := json.Marshal(render.DocumentOwnership{Schema: render.OwnershipNoteSchema, OwnedKeyPaths: [][]string{{"managed"}}, ReservedSlots: slots})
	tree := artifact.Tree{Entries: []artifact.Entry{{Path: dir, Kind: artifact.EntryDirectory, Mode: 0755}, {Path: file, Kind: artifact.EntryFile, Mode: 0600, Bytes: desired, Ownership: artifact.Ownership{EntryID: "fixture-native", GroupID: "fixture-native"}, Provenance: artifact.Provenance{Source: "fixture", Note: string(note)}}}}
	g := install.Grant{ID: "fixture-keys", Version: "1", Path: file, KeyPaths: [][]string{{"managed"}}}
	s := workspace.Spec{SchemaVersion: workspace.SchemaVersion, OperationID: "fixture-install", Operation: workspace.Install, Identity: identity, Installed: &workspace.InstallSpec{Target: target, Control: control, Provider: provider, Grants: []install.Grant{g}}, Effects: []workspace.EffectGrant{{Kind: workspace.InstalledEffect, RootID: target.ID, AuthorizationID: g.ID, Version: g.Version}, {Kind: workspace.DirectoryEffect, RootID: control.ID, AuthorizationID: "fixture-control", Version: "1"}}}
	r := workspace.Resources{Roots: []workspace.RootRef{target, control}, LockRoot: locks, LockNamespace: locks.Path, Grants: slices.Clone(s.Effects), Capabilities: []workspace.Capability{workspace.CanonicalRoots, workspace.MutationLocks, workspace.InstalledMerge}}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	o := workspace.Observations{At: now, ExpiresAt: now.Add(time.Minute), FenceVersion: identity.Fence.Revision, Capabilities: slices.Clone(r.Capabilities), InstalledCaseMode: materialize.CaseSensitive}
	for _, ref := range []workspace.RootRef{target, control, locks} {
		info, err := os.Lstat(ref.Path)
		if err != nil {
			t.Fatal(err)
		}
		o.Roots = append(o.Roots, workspace.RootObservation{RootID: ref.ID, DeclaredPath: ref.Path, CanonicalPath: ref.Path, CanonicalBase: ref.AllowedBase, Owner: ref.Owner, Exists: true, Directory: true, FileIdentity: materialize.InstalledIdentity(info)})
	}
	var err error
	o.InstalledFiles, err = materialize.InspectInstalled(context.Background(), target.Path, tree, o.InstalledCaseMode)
	if err != nil {
		t.Fatal(err)
	}
	c := workspace.ResolvedContent{Rendered: []render.Result{{Provider: provider, Layer: layout.Installed, Mode: layout.InstallMode, Root: layout.RootHome, Tree: tree}}, Roots: map[layout.Root]string{layout.RootHome: target.Path}}
	return s, c, r, o, &installedPorts{fixturePorts: &fixturePorts{observed: o}, control: control}
}
func TestInstalledRootExplicitApplyNeverReady(t *testing.T) {
	for _, provider := range []runtimes.ID{runtimes.Claude, runtimes.Codex} {
		t.Run(string(provider), func(t *testing.T) {
			s, c, r, o, f := installedInputs(t, provider)
			p := planned(t, s, c, r, o)
			if len(p.Actions()) != 1 || p.Actions()[0].Kind != workspace.InstalledTreeAction {
				t.Fatal("missing closed installed action")
			}
			result, err := workspace.Materialize(context.Background(), p, f.ports())
			if err != nil {
				t.Fatal(err)
			}
			if !result.ArtifactsComplete() || result.Status != workspace.Partial || result.Receipt.Phase != workspace.ArtifactsCommitted {
				t.Fatal("installed completion/readiness mismatch")
			}
			dir := filepath.Dir(filepath.Join(s.Installed.Target.Path, s.Installed.Grants[0].Path))
			info, err := os.Stat(dir)
			if err != nil || info.Mode().Perm() != 0750 {
				t.Fatal("changed operator directory")
			}
			info, err = os.Stat(s.Installed.Target.Path)
			if err != nil || info.Mode().Perm() != 0755 {
				t.Fatal("changed operator root")
			}
			if _, err = os.Stat(materialize.ManifestPath(s.Installed.Target.Path)); !os.IsNotExist(err) {
				t.Fatal("published manifest into user root")
			}
			b, _ := json.Marshal(result.Receipt)
			if strings.Contains(string(b), "managed\\\":1") {
				t.Fatal("receipt leaked document values")
			}
			if len(result.Obligations) == 0 || len(f.records) < 3 {
				t.Fatal("lost reservation/durable file intent")
			}
			if len(p.LockKeys()) != 2 {
				t.Fatal("incomplete target/control lock union")
			}
		})
	}
}
func TestInstalledAllDocumentsPreflightBeforeReceipts(t *testing.T) {
	s, c, r, o, f := installedInputs(t, runtimes.Claude)
	first := tree(".claude/AGENTS.md").Entries[0]
	c.Rendered[0].Tree.Entries = append(c.Rendered[0].Tree.Entries, first)
	g := install.Grant{ID: "fixture-file", Version: "1", Path: first.Path, WholeFile: true}
	s.Installed.Grants = append(s.Installed.Grants, g)
	grant := workspace.EffectGrant{Kind: workspace.InstalledEffect, RootID: s.Installed.Target.ID, AuthorizationID: g.ID, Version: g.Version}
	s.Effects = append(s.Effects, grant)
	r.Grants = append(r.Grants, grant)
	path := filepath.Join(s.Installed.Target.Path, ".claude/settings.json")
	if err := os.WriteFile(path, []byte{}, 0600); err != nil {
		t.Fatal(err)
	}
	var err error
	o.InstalledFiles, err = materialize.InspectInstalled(context.Background(), s.Installed.Target.Path, c.Rendered[0].Tree, o.InstalledCaseMode)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = workspace.Plan(s, c, r, o); err == nil {
		t.Fatal("accepted present zero-byte JSON")
	}
	if len(f.records) != 0 {
		t.Fatal("recorded a refused plan")
	}
	if _, err = os.Stat(filepath.Join(s.Installed.Target.Path, first.Path)); !os.IsNotExist(err) {
		t.Fatal("mutated earlier artifact")
	}
	// Planning succeeds with absence, but a new malformed file under the locks
	// is refused before the aggregate durable intent or staging.
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	o.InstalledFiles, err = materialize.InspectInstalled(context.Background(), s.Installed.Target.Path, c.Rendered[0].Tree, o.InstalledCaseMode)
	if err != nil {
		t.Fatal(err)
	}
	f.observed = o
	p := planned(t, s, c, r, o)
	if err = os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := workspace.Materialize(context.Background(), p, f.ports())
	if err == nil || result.ArtifactsComplete() || len(f.records) > 0 {
		t.Fatal("late unreadable document mutated or recorded")
	}
	entries, _ := os.ReadDir(s.Installed.Target.Path)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".installed-stage-") {
			t.Fatal("staged before aggregate refusal")
		}
	}
}
func TestInstalledReceiptFailureAndLateRevocation(t *testing.T) {
	for _, phase := range []string{"aggregate", "file-intent", "committed", "authority", "cancelled"} {
		t.Run(phase, func(t *testing.T) {
			s, c, r, o, f := installedInputs(t, runtimes.Claude)
			p := planned(t, s, c, r, o)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			f.onRecord = func(receipt workspace.Receipt) error {
				calls++
				switch phase {
				case "aggregate":
					if calls == 1 {
						return errors.New("fixture store")
					}
				case "file-intent":
					if calls == 2 {
						return errors.New("fixture store")
					}
				case "committed":
					if receipt.Phase == workspace.ArtifactsCommitted {
						return errors.New("fixture store")
					}
				case "authority":
					if calls == 1 {
						f.validateErr = errors.New("fixture revoked")
					}
				case "cancelled":
					if calls == 1 {
						cancel()
					}
				}
				return nil
			}
			result, err := workspace.Materialize(ctx, p, f.ports())
			if err == nil || result.ArtifactsComplete() {
				t.Fatal("failure earned completion")
			}
			path := filepath.Join(s.Installed.Target.Path, s.Installed.Grants[0].Path)
			_, diskerr := os.Stat(path)
			if phase == "committed" {
				if diskerr != nil || len(result.Retained) == 0 || result.Receipt.Phase != workspace.Interrupted {
					t.Fatal("lost committed partial root")
				}
			} else if !os.IsNotExist(diskerr) {
				t.Fatal("published without receipt/current authority")
			}
			if phase == "file-intent" && (len(result.Retained) == 0 || result.Status != workspace.Partial || result.Receipt.Phase != workspace.Interrupted || len(result.Obligations) == 0) {
				t.Fatal("lost actual staging mutation")
			}
			if !strings.HasPrefix(f.events[len(f.events)-1], "release:") {
				t.Fatal("did not release locks")
			}
		})
	}
}
func TestInstalledRefreshPreservesOperatorKeysAndOriginalObligations(t *testing.T) {
	for _, provider := range []runtimes.ID{runtimes.Claude, runtimes.Codex} {
		t.Run(string(provider), func(t *testing.T) {
			s, c, r, o, f := installedInputs(t, provider)
			p := planned(t, s, c, r, o)
			first, err := workspace.Materialize(context.Background(), p, f.ports())
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(s.Installed.Target.Path, s.Installed.Grants[0].Path)
			raw := []byte(`{"managed":1,"operator":"preserved"}`)
			desired := []byte(`{"managed":2}`)
			if provider == runtimes.Codex {
				raw = []byte("managed = 1\noperator = 'preserved'\n")
				desired = []byte("managed = 2\n")
			}
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			c.Rendered[0].Tree.Entries[1].Bytes = desired
			s.OperationID = "fixture-refresh"
			r.RecoveryReceipts = []workspace.Receipt{first.Receipt}
			o.InstalledFiles, err = materialize.InspectInstalled(context.Background(), s.Installed.Target.Path, c.Rendered[0].Tree, o.InstalledCaseMode)
			if err != nil {
				t.Fatal(err)
			}
			f.observed = o
			next, err := workspace.Materialize(context.Background(), planned(t, s, c, r, o), f.ports())
			if err != nil {
				t.Fatal(err)
			}
			if !next.ArtifactsComplete() || next.Receipt.Installed.PreviousGeneration != first.Receipt.InputDigest {
				t.Fatal("lost original ownership generation")
			}
			b, err := os.ReadFile(path)
			if err != nil || !strings.Contains(string(b), "preserved") {
				t.Fatal("lost operator key")
			}
			if !slices.Contains(next.Obligations, first.Obligations[0]) {
				t.Fatal("lost earlier trusted obligation")
			}
		})
	}
}
func TestInstalledFinalReceiptCallbackCannotEarnStaleCompletion(t *testing.T) {
	s, c, r, o, f := installedInputs(t, runtimes.Claude)
	p := planned(t, s, c, r, o)
	f.onRecord = func(receipt workspace.Receipt) error {
		if receipt.Phase == workspace.ArtifactsCommitted {
			return os.WriteFile(filepath.Join(s.Installed.Target.Path, s.Installed.Grants[0].Path), []byte(`{"managed":999}`), 0600)
		}
		return nil
	}
	result, err := workspace.Materialize(context.Background(), p, f.ports())
	if err == nil || result.ArtifactsComplete() {
		t.Fatal("earned stale completion after final callback")
	}
}
func TestInstalledDigestIsCanonicalAndExcludesObservationTime(t *testing.T) {
	s, c, r, o, _ := installedInputs(t, runtimes.Claude)
	p := planned(t, s, c, r, o)
	slices.Reverse(r.Roots)
	slices.Reverse(r.Grants)
	slices.Reverse(r.Capabilities)
	slices.Reverse(o.Roots)
	slices.Reverse(o.Capabilities)
	slices.Reverse(o.InstalledFiles)
	slices.Reverse(s.Effects)
	o.At = o.At.Add(time.Hour)
	o.ExpiresAt = o.ExpiresAt.Add(time.Hour)
	other := planned(t, s, c, r, o)
	if p.Digest() != other.Digest() || !reflect.DeepEqual(p.LockKeys(), other.LockKeys()) {
		t.Fatal("set order/time changed installed input digest")
	}
}

func TestInstalledPlanClosedInputsAndOriginBinding(t *testing.T) {
	for _, name := range []string{"operation reused", "unknown capability", "extra observed root", "reused lock ID", "target/control identity alias", "semantic grant omitted", "foreign control grant", "unknown physical identity", "mismatched origin", "foreign identity", "foreign root receipt"} {
		t.Run(name, func(t *testing.T) {
			s, c, r, o, f := installedInputs(t, runtimes.Claude)
			switch name {
			case "operation reused":
				o.Receipts = []workspace.Receipt{{SchemaVersion: workspace.SchemaVersion, IdentityKey: s.Identity.EncodedKey, OperationID: s.OperationID, InputDigest: "foreign"}}
			case "unknown capability":
				r.Capabilities = append(r.Capabilities, "unknown")
			case "extra observed root":
				o.Roots = append(o.Roots, workspace.RootObservation{RootID: "foreign"})
			case "reused lock ID":
				r.LockRoot.ID = s.Installed.Target.ID
				o.Roots[len(o.Roots)-1].RootID = r.LockRoot.ID
			case "target/control identity alias":
				o.Roots[1].FileIdentity = o.Roots[0].FileIdentity
			case "semantic grant omitted":
				r.Grants = r.Grants[1:]
			case "foreign control grant":
				s.Effects[1].RootID = "foreign"
				r.Grants = slices.Clone(s.Effects)
			case "unknown physical identity":
				o.Roots[0].FileIdentity = ""
			default:
				initial, err := workspace.Materialize(context.Background(), planned(t, s, c, r, o), f.ports())
				if err != nil {
					t.Fatal(err)
				}
				previous := initial.Receipt
				s.OperationID = "fixture-next"
				r.RecoveryReceipts = []workspace.Receipt{previous}
				var err2 error
				o.InstalledFiles, err2 = materialize.InspectInstalled(context.Background(), s.Installed.Target.Path, c.Rendered[0].Tree, o.InstalledCaseMode)
				if err2 != nil {
					t.Fatal(err2)
				}
				switch name {
				case "mismatched origin":
					r.RecoveryReceipts[0].Installed.Header.OperationID = "foreign"
				case "foreign identity":
					r.RecoveryReceipts[0].Identity.AgentURN = "urn:fixture:agent:foreign"
				case "foreign root receipt":
					r.RecoveryReceipts[0].Roots[0].Root.Owner = "foreign"
				}
			}
			if _, err := workspace.Plan(s, c, r, o); err == nil {
				t.Fatal("accepted unsupported/unbound installed contract")
			}
		})
	}
}
func TestInstalledLateRootControlReplacementAndFinalRevocation(t *testing.T) {
	for _, name := range []string{"target", "control", "lock", "final authority", "final cancel", "final directory"} {
		t.Run(name, func(t *testing.T) {
			s, c, r, o, f := installedInputs(t, runtimes.Claude)
			p := planned(t, s, c, r, o)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			f.onRecord = func(receipt workspace.Receipt) error {
				calls++
				if strings.HasPrefix(name, "final") && receipt.Phase == workspace.ArtifactsCommitted {
					switch name {
					case "final authority":
						f.validateErr = errors.New("fixture revoked")
					case "final cancel":
						cancel()
					case "final directory":
						return os.Chmod(filepath.Join(s.Installed.Target.Path, ".claude"), 0700)
					}
				}
				if calls == 1 {
					var ref workspace.RootRef
					switch name {
					case "target":
						ref = s.Installed.Target
					case "control":
						ref = s.Installed.Control
					case "lock":
						ref = r.LockRoot
					}
					if ref.Path != "" {
						if err := os.Rename(ref.Path, ref.Path+"-retained"); err != nil {
							return err
						}
						return os.Mkdir(ref.Path, 0700)
					}
				}
				return nil
			}
			result, err := workspace.Materialize(ctx, p, f.ports())
			if err == nil || result.ArtifactsComplete() {
				t.Fatal("replacement/revocation earned completion")
			}
			if strings.HasPrefix(name, "final") && (result.Receipt.Phase != workspace.Interrupted || len(result.Retained) == 0) {
				t.Fatal("lost final partial obligations")
			}
		})
	}
}

func TestInstalledExpiredFrozenObservationCannotBeRenewed(t *testing.T) {
	for _, point := range []string{"before-apply", "aggregate-callback", "file-callback", "final-callback"} {
		t.Run(point, func(t *testing.T) {
			s, c, r, o, f := installedInputs(t, runtimes.Claude)
			p := planned(t, s, c, r, o)
			renew := func() { f.observed.At = o.At.Add(time.Hour); f.observed.ExpiresAt = f.observed.At.Add(time.Minute) }
			if point == "before-apply" {
				renew()
			} else {
				calls := 0
				f.onRecord = func(receipt workspace.Receipt) error {
					calls++
					if point == "aggregate-callback" && calls == 1 || point == "file-callback" && calls == 2 || point == "final-callback" && receipt.Phase == workspace.ArtifactsCommitted {
						renew()
					}
					return nil
				}
			}
			result, err := workspace.Materialize(context.Background(), p, f.ports())
			if err == nil || result.ArtifactsComplete() {
				t.Fatal("renewed expired frozen observation into success")
			}
			if point == "before-apply" && len(f.records) > 0 {
				t.Fatal("recorded before frozen expiry refusal")
			}
			if point != "final-callback" {
				if _, err := os.Stat(filepath.Join(s.Installed.Target.Path, s.Installed.Grants[0].Path)); !os.IsNotExist(err) {
					t.Fatal("published past frozen expiry")
				}
			}
		})
	}
}
func TestInstalledFreshFenceAndControlAuthorityRefuseBeforeRecord(t *testing.T) {
	for _, name := range []string{"fresh expired", "future observation", "foreign fence", "unknown case", "changed key version", "missing configured control"} {
		t.Run(name, func(t *testing.T) {
			s, c, r, o, f := installedInputs(t, runtimes.Claude)
			p := planned(t, s, c, r, o)
			switch name {
			case "fresh expired":
				f.observed.ExpiresAt = f.Now()
			case "future observation":
				f.onValidate = func() error {
					f.observed.At = f.observed.At.Add(time.Hour)
					return errors.New("fixture clock mismatch")
				}
			case "foreign fence":
				f.observed.FenceVersion = "foreign"
			case "unknown case":
				f.observed.InstalledCaseMode = "unknown"
			case "changed key version":
				f.onValidate = func() error { return errors.New("fixture original authority expired") }
			case "missing configured control":
				f.control.Owner = "foreign"
			}
			result, err := workspace.Materialize(context.Background(), p, f.ports())
			if err == nil || result.ArtifactsComplete() || len(f.records) > 0 {
				t.Fatal("authority/freshness mismatch reached intent")
			}
		})
	}
}

func installedSeedFile(t *testing.T, provider, scenario, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "goldens", "seeds", "cairn", provider, "install-"+scenario, "expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	var entries []struct{ Path, Content string }
	if err = json.Unmarshal(raw, &entries); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Path == path {
			return []byte(e.Content)
		}
	}
	t.Fatal("archived native seed absent")
	return nil
}
func TestInstalledRootArchivedNativeCreateRefreshAndCheck(t *testing.T) {
	for _, provider := range []runtimes.ID{runtimes.Claude, runtimes.Codex} {
		t.Run(string(provider), func(t *testing.T) {
			s, c, r, o, f := installedInputs(t, provider)
			path := s.Installed.Grants[0].Path
			desired := installedSeedFile(t, string(provider), "create", path)
			expectedRefresh := installedSeedFile(t, string(provider), "refresh", path)
			owned := [][]string{{"permissions", "defaultMode"}}
			slots := []string{}
			if provider == runtimes.Codex {
				owned = [][]string{{"approval_policy"}, {"mcp_servers", "fixture", "args"}, {"mcp_servers", "fixture", "command"}}
				slots = []string{"mcp_servers"}
			}
			s.Installed.Grants[0].KeyPaths = owned
			note, _ := json.Marshal(render.DocumentOwnership{Schema: render.OwnershipNoteSchema, OwnedKeyPaths: owned, ReservedSlots: slots})
			c.Rendered[0].Tree.Entries[1].Bytes = desired
			c.Rendered[0].Tree.Entries[1].Provenance.Note = string(note)
			p := planned(t, s, c, r, o)
			first, err := workspace.Materialize(context.Background(), p, f.ports())
			if err != nil {
				t.Fatal(err)
			}
			actual, err := os.ReadFile(filepath.Join(s.Installed.Target.Path, path))
			if err != nil || !reflect.DeepEqual(actual, desired) {
				t.Fatal("installed create changed archived rendered bytes")
			}
			existing := []byte("{\"permissions\": {\"defaultMode\": \"acceptEdits\"}, \"fixture_operator\": true}\n")
			if provider == runtimes.Codex {
				existing = append(slices.Clone(desired), []byte("\nfixture_operator = true\n")...)
			}
			if err = os.WriteFile(filepath.Join(s.Installed.Target.Path, path), existing, 0600); err != nil {
				t.Fatal(err)
			}
			s.OperationID = "fixture-seed-refresh"
			r.RecoveryReceipts = []workspace.Receipt{first.Receipt}
			o.InstalledFiles, err = materialize.InspectInstalled(context.Background(), s.Installed.Target.Path, c.Rendered[0].Tree, o.InstalledCaseMode)
			if err != nil {
				t.Fatal(err)
			}
			f.observed = o
			next, err := workspace.Materialize(context.Background(), planned(t, s, c, r, o), f.ports())
			if err != nil {
				t.Fatal(err)
			}
			actual, err = os.ReadFile(filepath.Join(s.Installed.Target.Path, path))
			if err != nil || !reflect.DeepEqual(actual, expectedRefresh) {
				t.Fatal("installed refresh changed archived expected bytes")
			}
			s.OperationID = "fixture-seed-check"
			r.RecoveryReceipts = []workspace.Receipt{next.Receipt}
			o.InstalledFiles, err = materialize.InspectInstalled(context.Background(), s.Installed.Target.Path, c.Rendered[0].Tree, o.InstalledCaseMode)
			if err != nil {
				t.Fatal(err)
			}
			records := len(f.records)
			check := planned(t, s, c, r, o)
			dry, err := materialize.NewEngine(materialize.EngineOptions{}).Plan(context.Background(), check.Actions()[0].Request)
			if err != nil {
				t.Fatal(err)
			}
			for _, change := range dry.Report.Changes {
				if change.Kind != materialize.ChangeUnchanged {
					t.Fatal("at-rest archived owned check differs")
				}
			}
			if len(f.records) != records {
				t.Fatal("check recorded effects")
			}
		})
	}
}

func TestInstalledUserDirectoryModesAndSkillParents(t *testing.T) {
	for _, mode := range []os.FileMode{0700, 0750, 0755} {
		t.Run(mode.String(), func(t *testing.T) {
			s, c, r, o, f := installedInputs(t, runtimes.Codex)
			for _, rel := range []string{".codex", ".agents", ".agents/skills"} {
				path := filepath.Join(s.Installed.Target.Path, rel)
				if err := os.MkdirAll(path, mode); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, mode); err != nil {
					t.Fatal(err)
				}
			}
			entry := tree(".agents/skills/neutral/SKILL.md").Entries[0]
			entry.Ownership.EntryID = "fixture-skill"
			c.Rendered[0].Tree.Entries = append(c.Rendered[0].Tree.Entries, entry)
			grant := install.Grant{ID: "fixture-skill", Version: "1", Path: entry.Path, WholeFile: true}
			s.Installed.Grants = append(s.Installed.Grants, grant)
			authority := workspace.EffectGrant{Kind: workspace.InstalledEffect, RootID: s.Installed.Target.ID, AuthorizationID: grant.ID, Version: grant.Version}
			s.Effects = append(s.Effects, authority)
			r.Grants = append(r.Grants, authority)
			var err error
			o.InstalledFiles, err = materialize.InspectInstalled(context.Background(), s.Installed.Target.Path, c.Rendered[0].Tree, o.InstalledCaseMode)
			if err != nil {
				t.Fatal(err)
			}
			f.observed = o
			result, err := workspace.Materialize(context.Background(), planned(t, s, c, r, o), f.ports())
			if err != nil {
				t.Fatal(err)
			}
			for _, rel := range []string{".codex", ".agents", ".agents/skills"} {
				info, err := os.Stat(filepath.Join(s.Installed.Target.Path, rel))
				if err != nil || info.Mode().Perm() != mode {
					t.Fatal("changed existing user directory mode")
				}
			}
			created := false
			for _, d := range result.Receipt.Installed.Directories {
				if d.Path == ".agents/skills/neutral" && d.Created && d.Identity != "" && d.Phase == materialize.InstalledVerified {
					created = true
				}
				if d.Path == ".agents" && d.Created {
					t.Fatal("adopted existing provider directory")
				}
			}
			if !created {
				t.Fatal("lost exact operation-created skill parent")
			}
		})
	}
}
func TestInstalledCredentialSentinelUntouched(t *testing.T) {
	s, c, r, o, f := installedInputs(t, runtimes.Claude)
	credential := filepath.Join(s.Installed.Target.Path, ".claude/auth.json")
	sentinel := []byte("fixture-credential-sentinel")
	if err := os.WriteFile(credential, sentinel, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(credential)
	if err != nil {
		t.Fatal(err)
	}
	result, err := workspace.Materialize(context.Background(), planned(t, s, c, r, o), f.ports())
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Lstat(credential)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("changed credential identity")
	}
	raw, err := os.ReadFile(credential)
	if err != nil || !reflect.DeepEqual(raw, sentinel) {
		t.Fatal("changed credential bytes")
	}
	metadata, _ := json.Marshal(result)
	if strings.Contains(string(metadata), string(sentinel)) {
		t.Fatal("credential bytes leaked into output")
	}
}

func TestInstalledLockReleaseFailureRetainsCommittedRoot(t *testing.T) {
	s, c, r, o, f := installedInputs(t, runtimes.Claude)
	p := planned(t, s, c, r, o)
	f.failRelease = true
	result, err := workspace.Materialize(context.Background(), p, f.ports())
	if err == nil || result.ArtifactsComplete() || len(result.Retained) == 0 {
		t.Fatal("lost committed root after release failure")
	}
	for _, record := range f.records {
		if record.Phase == workspace.ArtifactsCommitted && record.Installed == nil {
			t.Fatal("lost durable committed metadata")
		}
	}
}

func TestInstalledFutureFrozenObservationCannotBorrowFreshWindow(t *testing.T) {
	s, c, r, o, f := installedInputs(t, runtimes.Claude)
	future := o
	future.At = future.At.Add(time.Hour)
	future.ExpiresAt = future.ExpiresAt.Add(time.Hour)
	p := planned(t, s, c, r, future)
	result, err := workspace.Materialize(context.Background(), p, f.ports())
	if err == nil || result.ArtifactsComplete() || len(f.records) > 0 {
		t.Fatal("borrowed another observation window")
	}
	for _, event := range f.events {
		if event == "validate" || event == "observe" {
			t.Fatal("known invalid frozen time reached host callback")
		}
	}
}
func TestInstalledBenignLateContentStillRequiresNewPlan(t *testing.T) {
	s, c, r, o, f := installedInputs(t, runtimes.Claude)
	p := planned(t, s, c, r, o)
	path := filepath.Join(s.Installed.Target.Path, s.Installed.Grants[0].Path)
	operator := []byte(`{"operator":"preserved"}`)
	if err := os.WriteFile(path, operator, 0600); err != nil {
		t.Fatal(err)
	}
	result, err := workspace.Materialize(context.Background(), p, f.ports())
	if err == nil || result.ArtifactsComplete() || len(f.records) > 0 {
		t.Fatal("bound new physical inputs to old frozen digest")
	}
	raw, err := os.ReadFile(path)
	if err != nil || !reflect.DeepEqual(raw, operator) {
		t.Fatal("mutated unplanned existing state")
	}
}

func TestInstalledMissingPhysicalObservationHasClosedRefusal(t *testing.T) {
	for _, missing := range []string{"operator", "control", "locks"} {
		t.Run(missing, func(t *testing.T) {
			s, c, r, o, _ := installedInputs(t, runtimes.Claude)
			o.Roots = slices.DeleteFunc(o.Roots, func(root workspace.RootObservation) bool { return root.RootID == missing })
			_, err := workspace.Plan(s, c, r, o)
			refusal(t, err, "installed_root_observation")
			var typed *workspace.Refusal
			if !errors.As(err, &typed) || typed.Status != workspace.Conflict {
				t.Fatal("missing explicit observation lost structural classification")
			}
		})
	}
}
