//go:build linux || darwin

package workspace_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hollis-labs/substrate/harness/sandbox"
	"github.com/hollis-labs/substrate/harness/workspace"
	"github.com/hollis-labs/substrate/harness/workspace/credentials"
	"github.com/hollis-labs/substrate/harness/workspace/credentials/localfs"
	"github.com/hollis-labs/substrate/harness/workspace/effects"
	"github.com/hollis-labs/substrate/harness/workspace/trust"
)

type effectStore struct {
	f       *fixturePorts
	records []workspace.Receipt
	fail    func(workspace.Receipt) bool
}

func (s *effectStore) Record(ctx context.Context, r workspace.Receipt) error {
	s.f.events = append(s.f.events, "durable:"+string(r.Phase))
	if len(r.EffectEvidence) > 0 {
		e := r.EffectEvidence[len(r.EffectEvidence)-1]
		s.f.events = append(s.f.events, "effect:"+string(e.Kind)+":"+string(e.Phase))
	}
	if s.fail != nil && s.fail(r) {
		return errors.New("fixture receipt refusal")
	}
	s.records = append(s.records, r)
	return s.f.Record(ctx, r)
}

type rootTrustPort struct {
	f                               *fixturePorts
	unsupported, uncertain, present bool
	candidate                       string
}

func (p *rootTrustPort) Supported(trust.Mechanism) bool { return !p.unsupported }
func (p *rootTrustPort) Observe(_ context.Context, r trust.Request) (trust.Observation, error) {
	p.f.events = append(p.f.events, "trust:observe")
	return trust.Observation{Target: filepath.Join(r.Target.CanonicalParent, filepath.Base(r.Target.LogicalPath)), Present: p.present}, nil
}
func (p *rootTrustPort) Begin(_ context.Context, r trust.Request) (trust.Session, error) {
	p.f.events = append(p.f.events, "trust:begin")
	if _, err := os.Readlink(filepath.Join(p.candidate, "auth.json")); err != nil {
		return nil, errors.New("credential link missing before trust")
	}
	return &rootTrustSession{p}, nil
}

type rootTrustSession struct{ p *rootTrustPort }

func (s *rootTrustSession) Apply(ctx context.Context, r trust.Request, validate func(context.Context) error) (trust.Observation, bool, error) {
	s.p.f.events = append(s.p.f.events, "trust:apply")
	if err := validate(ctx); err != nil {
		return trust.Observation{}, false, err
	}
	s.p.present = true
	o := trust.Observation{Target: filepath.Join(r.Target.CanonicalParent, filepath.Base(r.Target.LogicalPath)), Present: true}
	if s.p.uncertain {
		return o, true, errors.New("retained staging fixture")
	}
	return o, true, nil
}
func (s *rootTrustSession) Close() error {
	s.p.f.events = append(s.p.f.events, "trust:close")
	return nil
}

func effectApplyInputs(t *testing.T) (workspace.Spec, workspace.ResolvedContent, workspace.Resources, *fixturePorts, *effectStore, *rootTrustPort) {
	t.Helper()
	s, c, r, o := planInputs(t)
	base := t.TempDir()
	for _, ref := range []*workspace.RootRef{&s.Home.Root, &s.Boot.IdentityRoot, &s.Boot.Current, &s.Boot.Candidate, &r.LockRoot} {
		rel, _ := filepath.Rel(ref.AllowedBase, ref.Path)
		ref.Path = filepath.Join(base, rel)
		ref.AllowedBase = base
	}
	r.Roots = []workspace.RootRef{s.Home.Root, s.Boot.IdentityRoot, s.Boot.Current, s.Boot.Candidate}
	r.LockNamespace = r.LockRoot.Path
	config := workspace.RootRef{ID: "provider-config", Path: filepath.Join(base, "provider-config"), AllowedBase: base, Owner: s.Home.Root.Owner, Provenance: "fixture"}
	r.Roots = append(r.Roots, config)
	for _, ref := range []workspace.RootRef{r.LockRoot, config} {
		if err := os.Mkdir(ref.Path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for k, v := range c.Roots {
		rel, _ := filepath.Rel("/fixture", v)
		c.Roots[k] = filepath.Join(base, rel)
	}
	c.Rendered[0].Binding.Argv = []string{"--add-dir", s.Home.Root.Path}
	c.Rendered[0].Binding.CWD = s.Boot.Candidate.Path
	o.Roots = nil
	for _, ref := range append(slices.Clone(r.Roots), r.LockRoot) {
		obs, err := workspace.InspectRoot(ref)
		if err != nil {
			t.Fatal(err)
		}
		o.Roots = append(o.Roots, obs)
	}
	sourceHome := filepath.Join(base, "captured-home")
	if err := os.Mkdir(sourceHome, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceHome, "auth.json"), []byte("credential-byte-sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	link := workspace.EffectGrant{Kind: workspace.CredentialLinkEffect, RootID: s.Boot.Candidate.ID, AuthorizationID: "credential-authority", Version: "1"}
	tr := workspace.EffectGrant{Kind: workspace.TrustEffect, RootID: config.ID, AuthorizationID: "trust-authority", Version: "1"}
	s.Effects = append(s.Effects, link, tr)
	r.Grants = slices.Clone(s.Effects)
	r.Capabilities = append(r.Capabilities, workspace.CredentialLinks, workspace.TrustHandling)
	o.Capabilities = slices.Clone(r.Capabilities)
	s.Credentials = []workspace.CredentialSpec{{Source: workspace.ResourceRef{ID: "source", Path: filepath.Join(sourceHome, "auth.json")}, DestinationRootID: s.Boot.Candidate.ID, Destination: "auth.json", Concern: "fixture", Required: true, Authorization: workspace.ResourceRef{ID: link.AuthorizationID, Revision: link.Version}, Access: []sandbox.AccessKind{sandbox.AccessSourceRead}}}
	s.Trust = []workspace.TrustSpec{{Mechanism: string(trust.ClaudeProjects), Required: true, Authorization: workspace.ResourceRef{ID: tr.AuthorizationID, Revision: tr.Version}, Targets: []workspace.ResourceRef{{ID: s.Boot.Current.ID, Path: s.Boot.Current.Path, Provenance: "fixture"}}}}
	s.EffectInputs.Credentials = []credentials.Group{{Layer: credentials.BootLayer, Candidate: effects.RootInput{ID: s.Boot.Candidate.ID, Path: s.Boot.Candidate.Path, AllowedBase: base, Owner: s.Boot.Candidate.Owner, Provenance: s.Boot.Candidate.Provenance, MutationIdentity: s.Boot.IdentityRoot.Path, Inactive: true, PrivateCustody: true}, Home: credentials.ResolvedHome{BeforeRedirect: true, LogicalPath: sourceHome, CanonicalPath: sourceHome, CanonicalBase: base, Provider: "claude", CaptureID: "capture", Revision: "1", Provenance: "fixture", PlantedRoots: []string{s.Home.Root.Path, s.Boot.IdentityRoot.Path}}, Bindings: []credentials.Binding{{Source: "auth.json", Destination: "auth.json", Required: true, AuthorizationID: link.AuthorizationID, AuthorizationVersion: link.Version, SourceRead: true}}}}
	s.EffectInputs.Trust = []trust.Request{{Mechanism: trust.ClaudeProjects, Required: true, AuthorizationID: tr.AuthorizationID, AuthorizationVersion: tr.Version, CandidateRootID: s.Boot.Candidate.ID, Config: effects.RootInput{ID: config.ID, Path: config.Path, AllowedBase: base, Owner: config.Owner, Provenance: config.Provenance, MutationIdentity: config.Path}, Target: trust.Target{LogicalPath: s.Boot.Current.Path, CanonicalParent: s.Boot.IdentityRoot.Path, AllowedBase: base, Provenance: "fixture", Stable: true}}}
	f := &fixturePorts{observed: o}
	store := &effectStore{f: f}
	tp := &rootTrustPort{f: f, candidate: s.Boot.Candidate.Path}
	return s, c, r, f, store, tp
}
func effectPorts(f *fixturePorts, store *effectStore, tp *rootTrustPort) workspace.Ports {
	p := f.ports()
	p.ReceiptStore = store
	p.Credentials = localfs.New()
	p.Trust = tp
	return p
}

func TestEffectsPreflightBeforeArtifactsAndTrustLast(t *testing.T) {
	s, c, r, f, store, tp := effectApplyInputs(t)
	p := planned(t, s, c, r, f.observed)
	got, err := workspace.Materialize(context.Background(), p, effectPorts(f, store, tp))
	if err != nil || !got.ArtifactsComplete() || got.LaunchReady() || got.Status != workspace.Partial {
		t.Fatalf("effect apply: %v %#v", err, got)
	}
	firstObserve := slices.Index(f.events, "trust:observe")
	firstDirectory := -1
	for i, e := range f.events {
		if strings.HasPrefix(e, "directory:") {
			firstDirectory = i
			break
		}
	}
	if firstObserve < len(p.LockKeys()) || firstDirectory <= firstObserve {
		t.Fatal("trust not preflighted under full locks before mutation", f.events)
	}
	credentialComplete := slices.Index(f.events, "effect:credential_links:complete")
	trustBegin := slices.Index(f.events, "trust:begin")
	if credentialComplete < 0 || trustBegin <= credentialComplete {
		t.Fatal("trust ran before credential completion", f.events)
	}
	if len(got.Receipt.EffectEvidence) == 0 || len(store.records) == 0 {
		t.Fatal("leaf evidence bypassed receipt store")
	}
	encoded, _ := json.Marshal(got)
	if strings.Contains(string(encoded), "credential-byte-sentinel") {
		t.Fatal("credential bytes leaked")
	}
}

func TestLaterEffectPreflightRefusalPreventsAllMutation(t *testing.T) {
	s, c, r, f, store, tp := effectApplyInputs(t)
	tp.unsupported = true
	p := planned(t, s, c, r, f.observed)
	got, err := workspace.Materialize(context.Background(), p, effectPorts(f, store, tp))
	if err == nil || got.Status != workspace.Unsupported || got.ArtifactsComplete() || len(store.records) != 0 || len(got.Retained) != 0 {
		t.Fatal("unsupported trust crossed mutation boundary", err, got)
	}
	if _, err := os.Stat(s.Boot.Candidate.Path); !os.IsNotExist(err) {
		t.Fatal("preflight created candidate", err)
	}
}

func TestEffectReceiptFailureBeforeIntentPreventsTrustMutation(t *testing.T) {
	s, c, r, f, store, tp := effectApplyInputs(t)
	p := planned(t, s, c, r, f.observed)
	store.fail = func(r workspace.Receipt) bool {
		if len(r.EffectEvidence) == 0 {
			return false
		}
		e := r.EffectEvidence[len(r.EffectEvidence)-1]
		return e.Kind == effects.Trust && e.Phase == effects.IntentPhase
	}
	got, err := workspace.Materialize(context.Background(), p, effectPorts(f, store, tp))
	if err == nil || got.ArtifactsComplete() || got.Status != workspace.Partial || len(got.Retained) == 0 || slices.Contains(f.events, "trust:begin") {
		t.Fatal("failed intent reached trust or lost artifacts", err, f.events)
	}
}

func TestUncertainTrustRetainsObligationsAcrossRetry(t *testing.T) {
	s, c, r, f, store, tp := effectApplyInputs(t)
	tp.uncertain = true
	p := planned(t, s, c, r, f.observed)
	first, err := workspace.Materialize(context.Background(), p, effectPorts(f, store, tp))
	if err == nil || first.ArtifactsComplete() || first.Status != workspace.Partial {
		t.Fatal("uncertain staging accepted", err)
	}
	obligation := workspace.Obligation{Kind: workspace.RecoveryInspectionRequired, RootID: "provider-config", Code: "recovery_required"}
	if !slices.Contains(first.Obligations, obligation) {
		t.Fatal("lost uncertain trust obligation", first.Obligations)
	}
	s.OperationID = "retry-operation"
	s.Boot.CandidateGeneration = first.Handles[0].Manifest.Generation
	r.RecoveryReceipts = []workspace.Receipt{first.Receipt}
	f.observed.Roots = nil
	for _, ref := range append(slices.Clone(r.Roots), r.LockRoot) {
		o, e := workspace.InspectRoot(ref)
		if e != nil {
			t.Fatal(e)
		}
		f.observed.Roots = append(f.observed.Roots, o)
	}
	tp.uncertain = false
	retry := planned(t, s, c, r, f.observed)
	second, err := workspace.Materialize(context.Background(), retry, effectPorts(f, store, tp))
	if err != nil || !second.ArtifactsComplete() || second.LaunchReady() || !slices.Contains(second.Obligations, obligation) {
		t.Fatal("retry lost prior recovery obligation", err, second.Obligations)
	}
}

func TestArtifactCommitReceiptFailureStopsAllEffectMutation(t *testing.T) {
	s, c, r, f, store, tp := effectApplyInputs(t)
	p := planned(t, s, c, r, f.observed)
	store.fail = func(r workspace.Receipt) bool { return r.Phase == workspace.ArtifactsCommitted }
	got, err := workspace.Materialize(context.Background(), p, effectPorts(f, store, tp))
	if err == nil || got.ArtifactsComplete() || got.Status != workspace.Partial || slices.Contains(f.events, "trust:begin") {
		t.Fatal("failed artifact receipt reached effects", err, f.events)
	}
	if _, err := os.Lstat(filepath.Join(s.Boot.Candidate.Path, "auth.json")); !os.IsNotExist(err) {
		t.Fatal("failed artifact receipt created credential link", err)
	}
}

func TestEffectCapabilityRevocationRefusesBeforeMutation(t *testing.T) {
	s, c, r, f, store, tp := effectApplyInputs(t)
	p := planned(t, s, c, r, f.observed)
	f.observed.Capabilities = slices.DeleteFunc(slices.Clone(f.observed.Capabilities), func(c workspace.Capability) bool { return c == workspace.TrustHandling })
	got, err := workspace.Materialize(context.Background(), p, effectPorts(f, store, tp))
	if err == nil || got.Status != workspace.Unsupported || len(store.records) > 0 || len(got.Retained) > 0 {
		t.Fatal("revoked effect capability reached mutation", err)
	}
}

func TestForeignRecoveryRootsCannotBeCarriedIntoRetry(t *testing.T) {
	s, c, r, f, store, tp := effectApplyInputs(t)
	r.RecoveryReceipts = []workspace.Receipt{{SchemaVersion: workspace.SchemaVersion, OperationID: "prior", InputDigest: "prior-digest", IdentityKey: s.Identity.EncodedKey, Phase: workspace.Interrupted, Roots: []workspace.RootReceipt{{Root: workspace.RootRef{ID: s.Home.Root.ID, Path: "/foreign", AllowedBase: "/", Owner: "foreign", Provenance: "foreign"}}}, Obligations: []workspace.Obligation{{Kind: workspace.RecoveryInspectionRequired}}}}
	p := planned(t, s, c, r, f.observed)
	got, err := workspace.Materialize(context.Background(), p, effectPorts(f, store, tp))
	if err == nil || len(store.records) > 0 || got.ArtifactsComplete() {
		t.Fatal("foreign recovery root accepted", err)
	}
}
