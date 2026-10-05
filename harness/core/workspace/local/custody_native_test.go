//go:build linux || darwin

package local

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace"
)

func custodyFixture(t *testing.T) (*ports, workspace.LockKey, string) {
	t.Helper()
	base := t.TempDir()
	ns := filepath.Join(base, "control")
	if err := os.Mkdir(ns, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(ns)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	ref := workspace.RootRef{ID: "identity", Path: filepath.Join(base, "identity"), AllowedBase: base, Owner: "fixture", Provenance: "fixture"}
	p := &ports{options: Options{ControlRoot: workspace.RootRef{Path: ns}, Resources: workspace.Resources{Roots: []workspace.RootRef{ref}}}, control: root}
	key := workspace.LockKey{Namespace: ns, CanonicalID: ref.Path}
	path, err := MutationLockPath(key)
	if err != nil {
		t.Fatal(err)
	}
	return p, key, path
}

func TestHeldLockCustodyDetectsLateNameAndDescriptorChanges(t *testing.T) {
	for _, kind := range []string{"named-inode", "hard-link", "inheritable", "control-mode"} {
		t.Run(kind, func(t *testing.T) {
			p, key, path := custodyFixture(t)
			lock, err := p.Acquire(context.Background(), key)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = lock.Release() })
			held := lock.(*heldLock)
			name := filepath.Base(path)
			if err := p.validateFileCustody(held.file, name); err != nil {
				t.Fatalf("unchanged descriptor control: %v", err)
			}
			switch kind {
			case "named-inode":
				if err := os.Rename(path, path+"-retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("foreign-inode"), 0600); err != nil {
					t.Fatal(err)
				}
			case "hard-link":
				if err := os.Link(path, path+"-alias"); err != nil {
					t.Fatal(err)
				}
			case "inheritable":
				if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, held.file.Fd(), syscall.F_SETFD, 0); errno != 0 {
					t.Fatal(errno)
				}
			case "control-mode":
				if err := os.Chmod(key.Namespace, 0755); err != nil {
					t.Fatal(err)
				}
			}
			if err := p.validateFileCustody(held.file, name); err == nil {
				t.Fatal("late physical custody change accepted")
			}
			if kind == "named-inode" {
				contents, err := os.ReadFile(path)
				if err != nil || string(contents) != "foreign-inode" {
					t.Fatal("refusal repaired or rewrote replacement")
				}
			}
		})
	}
}

func TestAcquireRefusesHardLinkedLockWithoutReplacingInode(t *testing.T) {
	p, key, path := custodyFixture(t)
	if err := os.WriteFile(path, []byte("retained-lock-content"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(key.Namespace, "retained-alias")
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	held, err := p.Acquire(context.Background(), key)
	if held != nil {
		_ = held.Release()
	}
	if err == nil || held != nil {
		t.Fatal("multiply named inode admitted as private mutation lock")
	}
	after, statErr := os.Lstat(path)
	contents, readErr := os.ReadFile(alias)
	if statErr != nil || readErr != nil || !os.SameFile(before, after) || string(contents) != "retained-lock-content" {
		t.Fatal("custody refusal changed existing inode/content")
	}
}

func TestAcquireRefusesReboundNamedControlWithoutCreatingLock(t *testing.T) {
	p, key, path := custodyFixture(t)
	old := key.Namespace + "-retained"
	if err := os.Rename(key.Namespace, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(key.Namespace, 0700); err != nil {
		t.Fatal(err)
	}
	held, err := p.Acquire(context.Background(), key)
	if held != nil {
		_ = held.Release()
	}
	if err == nil || held != nil {
		t.Fatal("opened control and configured named control mismatch admitted")
	}
	for _, candidate := range []string{path, filepath.Join(old, filepath.Base(path))} {
		if _, err := os.Lstat(candidate); !os.IsNotExist(err) {
			t.Fatalf("rebound control created or adopted lock: %s %v", candidate, err)
		}
	}
}
