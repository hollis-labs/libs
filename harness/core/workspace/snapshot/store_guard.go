package snapshot

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/hollis-labs/substrate/harness/sandbox"
)

var ErrStoreCustodyUnsupported = errors.New("snapshot: confidential store custody unavailable")

// storeGuard pins native directory identity; it does not prove that a live
// agent's requested policy was actually enforced. A production capture factory
// must additionally possess the host's enforced isolation/custody capability.
type storeGuard struct {
	path     string
	root     *os.Root
	identity fs.FileInfo
	access   sandbox.ResolvedAccessPolicy
}

func openStoreGuard(storePath string, access sandbox.ResolvedAccessPolicy) (*storeGuard, error) {
	if !physicalDirectory(storePath) || access.Mode != sandbox.ConfinementRequired || access.Legacy.Enabled {
		return nil, ErrStoreCustodyUnsupported
	}
	info, err := os.Lstat(storePath)
	if err != nil || info.Mode().Perm() != 0700 || !ownedStore(info) {
		return nil, ErrStoreCustodyUnsupported
	}
	// An ancestor grant exposes the store; a descendant grant exposes a store
	// subdirectory. Also reject writable parents which could replace custody.
	for _, rules := range [][]sandbox.ResolvedPath{access.FS.Write, access.FS.Read, access.ProviderState.Write, access.ProviderState.Read, access.Runtime, access.Scratch} {
		for _, rule := range rules {
			if !cleanAbsolute(rule.Path) || intersection(storePath, rule.Path) != "" {
				return nil, ErrStoreCustodyUnsupported
			}
		}
	}
	root, err := os.OpenRoot(storePath)
	if err != nil {
		return nil, ErrStoreCustodyUnsupported
	}
	g := &storeGuard{path: storePath, root: root, identity: info, access: access}
	if err := g.check(); err != nil {
		root.Close()
		return nil, err
	}
	return g, nil
}

func (g *storeGuard) check() error {
	if g == nil || g.root == nil || !physicalDirectory(g.path) {
		return ErrStoreCustodyUnsupported
	}
	named, err := os.Lstat(g.path)
	if err != nil {
		return ErrStoreCustodyUnsupported
	}
	held, err := g.root.Stat(".")
	if err != nil {
		return ErrStoreCustodyUnsupported
	}
	if named.Mode().Perm() != 0700 || !ownedStore(named) || !os.SameFile(named, g.identity) || !os.SameFile(named, held) {
		return ErrStoreCustodyUnsupported
	}
	return nil
}

func (g *storeGuard) close() error {
	if g == nil || g.root == nil {
		return nil
	}
	return g.root.Close()
}

func secretFileIdentities(paths []string) ([]fs.FileInfo, error) {
	var out []fs.FileInfo
	for _, p := range paths {
		if !cleanAbsolute(p) {
			return nil, ErrCoverageUnsupported
		}
		canonical, err := filepath.EvalSymlinks(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil || !cleanAbsolute(canonical) {
			return nil, ErrCoverageUnsupported
		}
		info, err := os.Stat(canonical)
		if err != nil {
			return nil, ErrCoverageUnsupported
		}
		if info.Mode().IsRegular() {
			out = append(out, info)
		}
	}
	return out, nil
}
