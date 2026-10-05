//go:build linux || darwin

package local

import (
	"context"
	"errors"
	"github.com/hollis-labs/substrate/harness/workspace"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestMutationMappingBindsHeldOpenedInode(t *testing.T) {
	base := t.TempDir()
	ns := filepath.Join(base, "control")
	if err := os.Mkdir(ns, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(ns)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	ref := workspace.RootRef{ID: "identity", Path: filepath.Join(base, "identity"), AllowedBase: base, Owner: "fixture", Provenance: "fixture"}
	p := &ports{options: Options{ControlRoot: workspace.RootRef{Path: ns}, Resources: workspace.Resources{Roots: []workspace.RootRef{ref}}}, control: root}
	key := workspace.LockKey{Namespace: ns, CanonicalID: ref.Path}
	path, err := MutationLockPath(key)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := p.Acquire(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	held := lock.(*heldLock)
	opened, err := held.file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	named, err := os.Lstat(path)
	if err != nil || !os.SameFile(opened, named) {
		t.Fatal("named mutation lock differs from held descriptor")
	}
	st := opened.Sys().(*syscall.Stat_t)
	if !opened.Mode().IsRegular() || opened.Mode().Perm() != 0600 || st.Nlink != 1 || int(st.Uid) != os.Getuid() {
		t.Fatal("unexpected native lock custody")
	}
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, held.file.Fd(), syscall.F_GETFD, 0)
	if errno != 0 || flags&syscall.FD_CLOEXEC == 0 {
		t.Fatal("held mutation descriptor inheritable")
	}
	contender, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer contender.Close()
	err = syscall.Flock(int(contender.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
		t.Fatal("separate existing-path descriptor not excluded")
	}
	t.Logf("mapping=%s namespace=%s rawCanonicalID=%s name=%s heldDev=%d heldInode=%d namedSame=true mode=%o uid=%d nlink=%d cloexec=%t separateEXblocked=true", MutationMappingVersion, key.Namespace, key.CanonicalID, filepath.Base(path), st.Dev, st.Ino, opened.Mode().Perm(), st.Uid, st.Nlink, flags&syscall.FD_CLOEXEC != 0)
}
