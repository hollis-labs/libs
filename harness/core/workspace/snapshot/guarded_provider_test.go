//go:build linux || darwin

package snapshot

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hollis-labs/substrate/harness/sandbox"
)

func ownedGuardedConfig(t *testing.T) GuardedConfig {
	t.Helper()
	root := t.TempDir()
	store := filepath.Join(t.TempDir(), "store")
	if err := os.Mkdir(store, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("eligible owned content\n"), 0600); err != nil {
		t.Fatal(err)
	}
	access := sandbox.ResolvedAccessPolicy{ID: "owned-access", Mode: sandbox.ConfinementRequired, FS: sandbox.ResolvedFilesystemAccess{Write: []sandbox.ResolvedPath{{Path: root}}}}
	plan, err := DeriveTargets(access, TargetPolicy{Version: "owned-targets", Roots: []RootBinding{{"root", root, "owned-fixture"}}})
	if err != nil {
		t.Fatal(err)
	}
	policy := testCapturePolicy()
	policy.Budgets = CaptureBudgets{MaxCaptureBytes: 8 << 20, MaxRootBytes: 1 << 20, MaxStorageBytes: 32 << 20, MaxRunBytes: 32 << 20, MaxCaptureEntries: 1024, MaxRoots: 4, MaxRunCaptures: 4, MaxDuration: time.Minute}
	return GuardedConfig{StorePath: store, Access: access, Targets: plan, Policy: policy}
}

func TestGuardedProviderPublicConstructionCannotMintIsolation(t *testing.T) {
	config := ownedGuardedConfig(t)
	if _, err := NewGuardedProvider(config); !errors.Is(err, ErrStoreCustodyUnsupported) {
		t.Fatal("policy data minted isolation")
	}
	entries, err := os.ReadDir(config.StorePath)
	if err != nil || len(entries) != 0 {
		t.Fatal("unsupported production construction affected store")
	}
}

func TestGuardedCapturePersistsManifestAndRejectsTamperedReload(t *testing.T) {
	config := ownedGuardedConfig(t)
	if err := os.WriteFile(filepath.Join(config.Targets.roots[0].Binding.Root, ".env"), []byte("fake-excluded-value"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := newOwnedGuardedProvider(config)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	intent := testCaptureIntent("owned-set")
	intent.TargetMapDigest = config.Targets.Digest()
	result, err := p.CaptureBound(context.Background(), intent)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := result.Retained()
	if err != nil {
		t.Fatal(err)
	}
	lease, err := receipt.Pin(context.Background(), intent.OperationID, PinJournal)
	if err != nil {
		t.Fatal(err)
	}
	if err = lease.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := lease.Manifest()
	if !m.Complete || len(m.Roots) != 1 || m.Roots[0].TreeHash == "" || len(m.Roots[0].Skipped) == 0 {
		t.Fatal("durable manifest lost actual capture outcomes")
	}
	m.Roots[0].Skipped[0].Count = 999
	if lease.Manifest().Roots[0].Skipped[0].Count == 999 {
		t.Fatal("manifest accessor aliases admitted receipt")
	}
	if err = lease.Close(); err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(config.StorePath, "admission.json")
	original, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.Replace(original, []byte(`"Complete":true`), []byte(`"Complete":false`), 1)
	if bytes.Equal(original, changed) {
		t.Fatal("test did not reach persisted manifest")
	}
	if err = os.WriteFile(ledgerPath, changed, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = receipt.Pin(context.Background(), "different-owner", PinJournal); !errors.Is(err, ErrAdmissionUnavailable) {
		t.Fatal("changed durable outcome accepted")
	}
}

func TestGuardedCaptureReplacedRootRetainsPendingWithoutObjects(t *testing.T) {
	config := ownedGuardedConfig(t)
	p, err := newOwnedGuardedProvider(config)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	root := config.Targets.roots[0].Binding.Root
	old := filepath.Join(t.TempDir(), "original-root")
	if err = os.Rename(root, old); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "source.txt"), []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	intent := testCaptureIntent("replaced-root")
	intent.TargetMapDigest = config.Targets.Digest()
	result, err := p.CaptureBound(context.Background(), intent)
	if !errors.Is(err, ErrCoverageUnsupported) {
		t.Fatal("replaced target accepted")
	}
	if _, err = result.Retained(); !errors.Is(err, ErrAdmissionUnavailable) {
		t.Fatal("failed attempt issued retained receipt")
	}
	ledger, err := p.admission.load()
	if err != nil {
		t.Fatal(err)
	}
	if !ledger.Sets[intent.SetID].Pending {
		t.Fatal("uncertain capture reservation discarded")
	}
	if _, err = os.Stat(p.git.gitDirFor("root")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("replacement root entered object store")
	}
}

func TestGuardedCaptureIgnoresAmbientGitTemplate(t *testing.T) {
	config := ownedGuardedConfig(t)
	home, template := t.TempDir(), t.TempDir()
	fake := []byte("fake-template-secret")
	if err := os.WriteFile(filepath.Join(template, "secret.txt"), fake, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[init]\n templateDir = "+filepath.ToSlash(template)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	p, err := newOwnedGuardedProvider(config)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	intent := testCaptureIntent("ambient-template")
	intent.TargetMapDigest = config.Targets.Digest()
	if _, err = p.CaptureBound(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(p.git.gitDirFor("root"), "secret.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("ambient template entered confidential store")
	}
}

func TestGuardedCaptureFreshCredentialHardlinkNeverEntersObjects(t *testing.T) {
	config := ownedGuardedConfig(t)
	credential := filepath.Join(t.TempDir(), "host-credential")
	var err error
	config.Targets, err = DeriveTargets(config.Access, TargetPolicy{Version: "owned-targets", Roots: []RootBinding{{"root", config.Targets.roots[0].Binding.Root, "owned-fixture"}}, CredentialPaths: []string{credential}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := newOwnedGuardedProvider(config)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	fake := []byte("fake-newly-created-host-credential")
	if err = os.WriteFile(credential, fake, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Link(credential, filepath.Join(config.Targets.roots[0].Binding.Root, "ordinary-looking-file")); err != nil {
		t.Fatal(err)
	}
	intent := testCaptureIntent("fresh-credential")
	intent.TargetMapDigest = config.Targets.Digest()
	result, err := p.CaptureBound(context.Background(), intent)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := result.Retained()
	if err != nil {
		t.Fatal(err)
	}
	lease, err := receipt.Pin(context.Background(), intent.OperationID, PinJournal)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	object, err := p.git.runGit(context.Background(), p.git.gitDirFor("root"), "", nil, "cat-file", "--batch-all-objects", "--batch")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(object, fake) {
		t.Fatal("fresh credential hardlink entered object storage")
	}
	if !bytes.Contains(object, []byte("eligible owned content")) {
		t.Fatal("eligible content not captured")
	}
}

func TestGuardedCaptureOversizeManifestPersistsIncompleteCoverage(t *testing.T) {
	config := ownedGuardedConfig(t)
	config.Policy.Budgets.MaxRootBytes = 4 << 20
	config.Policy.Budgets.MaxCaptureBytes = 16 << 20
	if err := os.WriteFile(filepath.Join(config.Targets.roots[0].Binding.Root, "oversize.bin"), bytes.Repeat([]byte("x"), (2<<20)+1), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := newOwnedGuardedProvider(config)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	intent := testCaptureIntent("incomplete")
	intent.TargetMapDigest = config.Targets.Digest()
	result, err := p.CaptureBound(context.Background(), intent)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := result.Retained()
	if err != nil {
		t.Fatal(err)
	}
	lease, err := receipt.Pin(context.Background(), intent.OperationID, PinJournal)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if err = lease.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := lease.Manifest()
	if m.Complete {
		t.Fatal("eligible oversize omission presented as complete")
	}
	found := false
	for _, skip := range m.Roots[0].Skipped {
		if skip.Reason == "oversize_untracked" && skip.Count == 1 {
			found = true
		}
	}
	if !found {
		t.Fatal("incomplete outcome not durably bound")
	}
}
