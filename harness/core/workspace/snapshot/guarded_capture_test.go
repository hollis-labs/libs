//go:build linux || darwin

package snapshot

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/substrate/harness/sandbox"
)

func TestGuardedMirrorSecretsNeverEnterGitObjects(t *testing.T) {
	root, mirror, store := t.TempDir(), t.TempDir(), t.TempDir()
	secret := []byte("owned-fake-secret-not-for-ingestion")
	for _, rel := range []string{".env.production", "keys/private.key", ".codex/auth.json", ".git/config"} {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, secret, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("eligible content\n"), 0600); err != nil {
		t.Fatal(err)
	}
	scope := plannedRoot{Binding: RootBinding{"root", root, "fixture"}, Include: []string{"."}}
	usage, err := prepareMirror(context.Background(), scope, mirror, CaptureLimits{MaxBytes: 1024, MaxFileBytes: 512, MaxEntries: 32}, nil)
	if err != nil || usage.Files != 1 || usage.Excluded == 0 {
		t.Fatalf("safe mirror result: %+v %v", usage, err)
	}
	sg, err := NewShadowGit(store)
	if err != nil {
		t.Fatal(err)
	}
	set, err := sg.Capture(context.Background(), []Target{{ID: "root", Root: mirror}})
	if err != nil || set.Roots["root"].Err != nil {
		t.Fatal("safe mirror capture failed")
	}
	cmd := exec.Command("git", "--git-dir", sg.gitDirFor("root"), "cat-file", "--batch-all-objects", "--batch")
	objects, err := cmd.Output()
	if err != nil {
		t.Fatal("inspect owned object inventory")
	}
	if bytes.Contains(objects, secret) {
		t.Fatal("excluded secret reached Git object storage")
	}
	if !bytes.Contains(objects, []byte("eligible content")) {
		t.Fatal("positive content never reached Git")
	}
	diff, err := sg.Diff(context.Background(), SnapshotSet{}, set)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range diff.Roots["root"].Files {
		if SecretExcluded(change.Path) {
			t.Fatal("secret appeared in diff")
		}
	}
	preview, err := sg.Preview(context.Background(), set, []string{JoinPath("root", ".env.production")})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Roots["root"].Changes) != 1 || preview.Roots["root"].Changes[0].SnapshotHash != "" {
		t.Fatal("excluded secret present in preview")
	}
}

func TestGuardedMirrorScopeLimitsAndSymlinksRefuseBeforeIngestion(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "source"), []byte("too large"), 0600); err != nil {
		t.Fatal(err)
	}
	scope := plannedRoot{Binding: RootBinding{"root", root, "fixture"}, Include: []string{"."}}
	_, err := prepareMirror(context.Background(), scope, t.TempDir(), CaptureLimits{MaxBytes: 4, MaxFileBytes: 4, MaxEntries: 10}, nil)
	if !errors.Is(err, ErrCaptureLimit) {
		t.Fatal("file limit did not refuse")
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	_, err = prepareMirror(context.Background(), scope, t.TempDir(), CaptureLimits{MaxBytes: 1024, MaxFileBytes: 512, MaxEntries: 10}, nil)
	if !errors.Is(err, ErrCoverageUnsupported) {
		t.Fatal("symlink escape accepted")
	}
	if err := os.Remove(filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	scope.Exclude = []string{"."}
	_, err = prepareMirror(context.Background(), scope, t.TempDir(), CaptureLimits{MaxBytes: 1024, MaxFileBytes: 512, MaxEntries: 10}, nil)
	if !errors.Is(err, ErrCoverageUnsupported) {
		t.Fatal("empty effective scope accepted")
	}
}

func TestGuardedMirrorHardlinkToKnownCredentialExcluded(t *testing.T) {
	root := t.TempDir()
	credential := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(credential, []byte("owned fake credential"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(credential, filepath.Join(root, "ordinary.md")); err != nil {
		t.Fatal(err)
	}
	identities, err := secretFileIdentities([]string{credential})
	if err != nil {
		t.Fatal(err)
	}
	usage, err := prepareMirror(context.Background(), plannedRoot{Binding: RootBinding{"r", root, "fixture"}, Include: []string{"."}}, t.TempDir(), CaptureLimits{1024, 512, 10}, identities)
	if !errors.Is(err, ErrCoverageUnsupported) || usage.Excluded != 1 {
		t.Fatal("credential hardlink ingested")
	}
}

func TestNativeCaptureOpenRejectsParentSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "private"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "private", "source"), []byte("fake secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("private", filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	held, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	file, err := openCaptureFile(held, "alias/source")
	if file != nil {
		file.Close()
	}
	if !errors.Is(err, ErrCoverageUnsupported) {
		t.Fatal("parent symlink followed")
	}
}

func TestStoreGuardRefusesPermissiveOverlapAndReplacement(t *testing.T) {
	root, store := t.TempDir(), t.TempDir()
	access := sandbox.ResolvedAccessPolicy{ID: "fixture", Mode: sandbox.ConfinementRequired, FS: sandbox.ResolvedFilesystemAccess{Write: []sandbox.ResolvedPath{{Path: root}}}}
	if err := os.Chmod(store, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := openStoreGuard(store, access); !errors.Is(err, ErrStoreCustodyUnsupported) {
		t.Fatal("permissive store accepted")
	}
	if err := os.Chmod(store, 0700); err != nil {
		t.Fatal(err)
	}
	access.FS.Read = []sandbox.ResolvedPath{{Path: filepath.Dir(store)}}
	if _, err := openStoreGuard(store, access); !errors.Is(err, ErrStoreCustodyUnsupported) {
		t.Fatal("agent-readable store accepted")
	}
	access.FS.Read = nil
	guard, err := openStoreGuard(store, access)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.close()
	if err := os.Rename(store, store+"-old"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(store + "-old") })
	if err := os.Mkdir(store, 0700); err != nil {
		t.Fatal(err)
	}
	if err := guard.check(); !errors.Is(err, ErrStoreCustodyUnsupported) {
		t.Fatal("replaced store identity accepted")
	}
}
