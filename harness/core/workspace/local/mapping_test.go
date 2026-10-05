//go:build linux || darwin

package local_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/hollis-labs/substrate/harness/workspace"
	"github.com/hollis-labs/substrate/harness/workspace/local"
	"os"
	"path/filepath"
	"testing"
)

func TestMutationMappingIsExistingLockInode(t *testing.T) {
	options, _ := fixture(t)
	ports, closePorts, err := local.New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer closePorts()
	key := workspace.LockKey{Namespace: options.Resources.LockNamespace, CanonicalID: options.Resources.Roots[0].Path}
	name, err := local.MutationLockPath(key)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(key.CanonicalID))
	want := filepath.Join(key.Namespace, "lock-"+hex.EncodeToString(sum[:]))
	if name != want {
		t.Fatal("mapping changed existing filename")
	}
	held, err := ports.Locks.Acquire(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.Lstat(name)
	if err != nil {
		t.Fatal(err)
	}
	if err = held.Release(); err != nil {
		t.Fatal(err)
	}
	again, err := ports.Locks.Acquire(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Release()
	current, err := os.Lstat(name)
	if err != nil || !os.SameFile(original, current) {
		t.Fatal("existing mutation inode replaced")
	}
}
func TestMutationMappingRejectsUnresolvedKeys(t *testing.T) {
	for _, key := range []workspace.LockKey{{Namespace: "relative", CanonicalID: "/fixture/root"}, {Namespace: "/fixture/locks", CanonicalID: "/fixture/root/../other"}, {Namespace: "/fixture/locks", CanonicalID: "/fixture/root\x00foreign"}} {
		if _, err := local.MutationLockPath(key); err == nil {
			t.Fatal("unresolved key mapped")
		}
	}
}
