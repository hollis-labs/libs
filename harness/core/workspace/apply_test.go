package workspace_test

import (
	"context"
	"errors"
	"github.com/hollis-labs/substrate/harness/workspace"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type fixturePorts struct {
	observed      workspace.Observations
	events        []string
	failAcquire   int
	failRelease   bool
	failRecord    bool
	validateErr   error
	failPhase     workspace.Phase
	failDirectory string
}

func (f *fixturePorts) Now() time.Time { return f.observed.At.Add(time.Second) }
func (f *fixturePorts) Validate(context.Context, workspace.Spec, workspace.Resources) error {
	f.events = append(f.events, "validate")
	return f.validateErr
}
func (f *fixturePorts) EnsureOwnedDirectory(_ context.Context, r workspace.RootRef, m fs.FileMode) error {
	f.events = append(f.events, "directory:"+r.ID)
	if f.failDirectory == r.ID {
		return errors.New("fixture directory")
	}
	return os.Mkdir(r.Path, m)
}
func (f *fixturePorts) Observe(context.Context, workspace.Resources) (workspace.Observations, error) {
	f.events = append(f.events, "observe")
	return f.observed, nil
}
func (f *fixturePorts) Record(_ context.Context, r workspace.Receipt) error {
	f.events = append(f.events, "record:"+string(r.Phase))
	if f.failRecord || f.failPhase == r.Phase {
		return errors.New("fixture record")
	}
	return nil
}
func (f *fixturePorts) Acquire(_ context.Context, k workspace.LockKey) (workspace.HeldLock, error) {
	f.events = append(f.events, "acquire:"+filepath.Base(k.CanonicalID))
	if f.failAcquire > 0 {
		f.failAcquire--
		if f.failAcquire == 0 {
			return nil, errors.New("fixture acquire")
		}
	}
	return fixtureLock{f, filepath.Base(k.CanonicalID)}, nil
}

type fixtureLock struct {
	f    *fixturePorts
	name string
}

func (l fixtureLock) Release() error {
	l.f.events = append(l.f.events, "release:"+l.name)
	if l.f.failRelease {
		return errors.New("fixture release")
	}
	return nil
}
func (f *fixturePorts) ports() workspace.Ports {
	return workspace.Ports{Clock: f, Host: f, Locks: f, Observations: f, ReceiptStore: f}
}
func applyFixture(t *testing.T) (workspace.PlannedWorkspace, *fixturePorts, workspace.Spec) {
	t.Helper()
	s, c, r, o := planInputs(t)
	base := t.TempDir()
	refs := []*workspace.RootRef{&s.Home.Root, &s.Boot.IdentityRoot, &s.Boot.Current, &s.Boot.Candidate}
	for _, ref := range refs {
		rel, _ := filepath.Rel(ref.AllowedBase, ref.Path)
		ref.Path = filepath.Join(base, rel)
		ref.AllowedBase = base
	}
	r.Roots = []workspace.RootRef{s.Home.Root, s.Boot.IdentityRoot, s.Boot.Current, s.Boot.Candidate}
	r.LockNamespace = filepath.Join(base, "locks")
	for i := range o.Roots {
		for _, ref := range r.Roots {
			if o.Roots[i].RootID == ref.ID {
				o.Roots[i].CanonicalPath = ref.Path
				o.Roots[i].CanonicalBase = base
			}
		}
	}
	for k, v := range c.Roots {
		rel, _ := filepath.Rel(filepath.Join(string(filepath.Separator), "fixture"), v)
		c.Roots[k] = filepath.Join(base, rel)
	}
	c.Rendered[0].Binding.Environment["FIXTURE_ROOT"] = s.Boot.Candidate.Path
	return planned(t, s, c, r, o), &fixturePorts{observed: o}, s
}
func TestMaterializeCommitsRealManifestWithoutReady(t *testing.T) {
	p, f, s := applyFixture(t)
	got, err := workspace.Materialize(context.Background(), p, f.ports())
	if err != nil {
		t.Fatal(err)
	}
	if !got.ArtifactsComplete() || got.Status != workspace.Partial || got.Receipt.Phase != workspace.ArtifactsCommitted {
		t.Fatalf("result=%+v", got)
	}
	b, err := os.ReadFile(filepath.Join(s.Boot.Candidate.Path, "AGENTS.md"))
	if err != nil || string(b) != "fixture" {
		t.Fatal(string(b), err)
	}
	if len(got.Handles) != 1 {
		t.Fatal("manifest not verified")
	}
	if got.Receipt.Identity.AgentURN != s.Identity.AgentURN {
		t.Fatal("identity pins lost")
	}
}
func TestMaterializeReleasesPartialAcquisition(t *testing.T) {
	p, f, _ := applyFixture(t)
	f.failAcquire = 2
	got, err := workspace.Materialize(context.Background(), p, f.ports())
	if err == nil || got.ArtifactsComplete() {
		t.Fatal("acquisition failure completed")
	}
	want := []string{"acquire:boot", "acquire:home", "release:boot"}
	if !reflect.DeepEqual(f.events, want) {
		t.Fatalf("events=%v", f.events)
	}
}
func TestMaterializeRefusesBeforeMutation(t *testing.T) {
	for _, kind := range []string{"expired", "changed canonical", "authority", "record", "release"} {
		t.Run(kind, func(t *testing.T) {
			p, f, s := applyFixture(t)
			switch kind {
			case "expired":
				f.observed.ExpiresAt = f.Now()
			case "changed canonical":
				f.observed.Roots[0].CanonicalPath = filepath.Join(s.Home.Root.AllowedBase, "other")
			case "authority":
				f.validateErr = errors.New("fixture authority")
			case "record":
				f.failRecord = true
			case "release":
				f.failRelease = true
			}
			got, err := workspace.Materialize(context.Background(), p, f.ports())
			if err == nil || got.ArtifactsComplete() {
				t.Fatal("refusal completed")
			}
			if kind != "release" {
				if _, err := os.Stat(s.Boot.Candidate.Path); !os.IsNotExist(err) {
					t.Fatal("mutation before refusal", err)
				}
			} else if len(got.Retained) == 0 {
				t.Fatal("release failure lost retained roots")
			}
		})
	}
}
func TestMaterializeZeroPlanAndMissingPortsRefuse(t *testing.T) {
	p, f, _ := applyFixture(t)
	for _, test := range []struct {
		p     workspace.PlannedWorkspace
		ports workspace.Ports
	}{{workspace.PlannedWorkspace{}, f.ports()}, {p, workspace.Ports{}}} {
		got, err := workspace.Materialize(context.Background(), test.p, test.ports)
		if err == nil || got.ArtifactsComplete() {
			t.Fatal("invalid apply completed")
		}
	}
}

func TestMaterializeReceiptFailureBoundaries(t *testing.T) {
	for _, phase := range []workspace.Phase{workspace.Planned, workspace.Interrupted, workspace.ArtifactsCommitted} {
		t.Run(string(phase), func(t *testing.T) {
			p, f, s := applyFixture(t)
			f.failPhase = phase
			got, err := workspace.Materialize(context.Background(), p, f.ports())
			if err == nil || got.ArtifactsComplete() {
				t.Fatal("record failure earned completion")
			}
			_, statErr := os.Stat(s.Boot.Candidate.Path)
			if phase == workspace.ArtifactsCommitted {
				if statErr != nil || got.Status != workspace.Partial || got.Receipt.Phase != workspace.Interrupted || len(got.Retained) == 0 {
					t.Fatal("committed failure lost partial work", statErr)
				}
			} else if !os.IsNotExist(statErr) {
				t.Fatal("receipt failure allowed mutation", statErr)
			}
		})
	}
}
func TestMaterializePartialDirectoryFailureRetains(t *testing.T) {
	p, f, s := applyFixture(t)
	f.failDirectory = s.Home.Root.ID
	got, err := workspace.Materialize(context.Background(), p, f.ports())
	if err == nil || got.ArtifactsComplete() || got.Status != workspace.Partial || len(got.Retained) == 0 {
		t.Fatal("partial directory failure not accounted")
	}
	if _, err := os.Stat(s.Boot.IdentityRoot.Path); err != nil {
		t.Fatal("earlier successful directory disappeared")
	}
}
func TestMaterializeManifestObservationCannotInventOwnership(t *testing.T) {
	p, f, s := applyFixture(t)
	if err := os.MkdirAll(s.Boot.Candidate.Path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Boot.Candidate.Path, "operator.txt"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := workspace.Materialize(context.Background(), p, f.ports())
	if err == nil || got.ArtifactsComplete() {
		t.Fatal("populated manifestless root adopted")
	}
	b, _ := os.ReadFile(filepath.Join(s.Boot.Candidate.Path, "operator.txt"))
	if string(b) != "fixture" {
		t.Fatal("unowned content changed")
	}
}
func TestMaterializeCanonicalAliasRefuses(t *testing.T) {
	p, f, s := applyFixture(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, s.Boot.IdentityRoot.Path); err != nil {
		t.Fatal(err)
	}
	got, err := workspace.Materialize(context.Background(), p, f.ports())
	if err == nil || got.ArtifactsComplete() {
		t.Fatal("symlink canonical alias accepted")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("alias destination changed")
	}
}
func TestMaterializeCancellationBeforeAcquire(t *testing.T) {
	p, f, _ := applyFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := workspace.Materialize(ctx, p, f.ports())
	if !errors.Is(err, context.Canceled) || got.ArtifactsComplete() || len(f.events) != 0 {
		t.Fatal("cancellation ignored", err, f.events)
	}
}

func TestMaterializeLiveFenceCapabilitiesAndInputBinding(t *testing.T) {
	for _, kind := range []string{"fence", "capability", "operation binding", "duplicate observation"} {
		t.Run(kind, func(t *testing.T) {
			p, f, s := applyFixture(t)
			switch kind {
			case "fence":
				f.observed.FenceVersion = "different"
			case "capability":
				f.observed.Capabilities = nil
			case "operation binding":
				f.observed.Receipts = []workspace.Receipt{{SchemaVersion: workspace.SchemaVersion, OperationID: s.OperationID, InputDigest: "different", IdentityKey: s.Identity.EncodedKey}}
			case "duplicate observation":
				f.observed.Roots = append(f.observed.Roots, f.observed.Roots[0])
			}
			got, err := workspace.Materialize(context.Background(), p, f.ports())
			if err == nil || got.ArtifactsComplete() {
				t.Fatal("live conflict accepted")
			}
			if _, err := os.Stat(s.Boot.IdentityRoot.Path); !os.IsNotExist(err) {
				t.Fatal("live conflict mutated workspace", err)
			}
		})
	}
}
func TestInspectRootRefusesCredentialOwnedAndSymlinkManifests(t *testing.T) {
	for _, kind := range []string{"credential", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			r := workspace.RootRef{ID: "fixture", Path: filepath.Join(base, "root"), AllowedBase: base, Owner: "fixture", Provenance: "fixture"}
			os.MkdirAll(filepath.Join(r.Path, ".materialize"), 0700)
			if kind == "credential" {
				os.WriteFile(filepath.Join(r.Path, ".materialize", "manifest.json"), []byte(`{"schema_version":"materialize.v1","generation":"fixture","entries":[{"path":"auth.json","kind":"file","mode":384,"ownership":{"entry_id":"fixture","group_id":"fixture"},"provenance":{"source":"fixture"}}]}`), 0600)
			} else {
				sentinel := filepath.Join(base, "sentinel")
				os.WriteFile(sentinel, []byte("fixture"), 0600)
				os.Symlink(sentinel, filepath.Join(r.Path, ".materialize", "manifest.json"))
			}
			if _, err := workspace.InspectRoot(r); err == nil {
				t.Fatal("unsafe manifest accepted")
			}
		})
	}
}

func TestMaterializeReleasesCompleteLockSetInReverse(t *testing.T) {
	p, f, _ := applyFixture(t)
	_, err := workspace.Materialize(context.Background(), p, f.ports())
	if err != nil {
		t.Fatal(err)
	}
	events := f.events[len(f.events)-2:]
	want := []string{"release:home", "release:boot"}
	if !reflect.DeepEqual(events, want) {
		t.Fatal("release order", events)
	}
}

func TestArtifactProofIsBoundToResult(t *testing.T) {
	for name, change := range map[string]func(*workspace.ApplyResult){
		"input digest":    func(r *workspace.ApplyResult) { r.Receipt.InputDigest = "changed" },
		"root identity":   func(r *workspace.ApplyResult) { r.Receipt.Roots[0].Root.Owner = "changed" },
		"root completion": func(r *workspace.ApplyResult) { r.Receipt.Roots[0].Complete = false },
		"handle":          func(r *workspace.ApplyResult) { r.Handles = nil },
		"obligations":     func(r *workspace.ApplyResult) { r.Obligations = nil },
	} {
		t.Run(name, func(t *testing.T) {
			p, host, _ := applyFixture(t)
			result, err := workspace.Materialize(context.Background(), p, host.ports())
			if err != nil || !result.ArtifactsComplete() {
				t.Fatalf("fixture did not earn completion: %v", err)
			}
			change(&result)
			if result.ArtifactsComplete() {
				t.Fatal("edited result retained completion proof")
			}
		})
	}
}

func TestCallerCannotMintLaunchReadiness(t *testing.T) {
	if (workspace.ApplyResult{Status: workspace.Ready}).LaunchReady() {
		t.Fatal("caller minted launch readiness")
	}
	p, host, _ := applyFixture(t)
	result, err := workspace.Materialize(context.Background(), p, host.ports())
	if err != nil || !result.ArtifactsComplete() || result.LaunchReady() {
		t.Fatalf("artifact-only guarantee changed: %v", err)
	}
	result.Status = workspace.Ready
	if result.LaunchReady() || result.ArtifactsComplete() {
		t.Fatal("status mutation minted a proof")
	}
}
