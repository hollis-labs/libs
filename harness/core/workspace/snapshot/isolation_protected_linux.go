//go:build linux

package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

type protectedRoot struct {
	path     string
	identity os.FileInfo
}
type protectedRoots struct {
	roots  []protectedRoot
	digest string
}
type protectedMetadata struct {
	identities map[string]bool
	ancestors  []os.FileInfo
}

// These metadata observations include file identities, never their contents.
// They detect retained descriptors/mappings and hard-link aliases which cannot
// be excluded by comparing mount names alone. Dynamic contents are refreshed
// under the host fence; protected directory identities remain immutable.
func observeProtectedRoots(store string, paths []string) (*protectedRoots, error) {
	if len(paths) == 0 || len(paths) > 32 {
		return nil, ErrStoreCustodyUnsupported
	}
	all := append([]string{store}, paths...)
	slices.Sort(all)
	all = slices.Compact(all)
	p := &protectedRoots{}
	var binding strings.Builder
	for _, path := range all {
		if !physicalDirectory(path) || path == string(os.PathSeparator) || strings.ContainsAny(path, "\n\r\t") {
			return nil, ErrStoreCustodyUnsupported
		}
		info, e := os.Stat(path)
		if e != nil {
			return nil, ErrStoreCustodyUnsupported
		}
		p.roots = append(p.roots, protectedRoot{path, info})
		fmt.Fprintf(&binding, "%d:%s:%s;", len(path), path, admissionStoreIdentity(info))
	}
	sum := sha256.Sum256([]byte(binding.String()))
	p.digest = hex.EncodeToString(sum[:])
	return p, nil
}
func (p *protectedRoots) contains(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	for _, root := range p.roots {
		if pathWithin(path, root.path) {
			return true
		}
	}
	return false
}
func (p *protectedRoots) current(ctx context.Context) (protectedMetadata, error) {
	out := protectedMetadata{identities: map[string]bool{}}
	entries := 0
	for _, protected := range p.roots {
		info, e := os.Stat(protected.path)
		if e != nil || !physicalDirectory(protected.path) || !os.SameFile(info, protected.identity) {
			return out, ErrStoreCustodyUnsupported
		}
		for path := protected.path; ; path = filepath.Dir(path) {
			info, e := os.Stat(path)
			if e != nil {
				return out, ErrStoreCustodyUnsupported
			}
			out.ancestors = append(out.ancestors, info)
			if filepath.Dir(path) == path {
				break
			}
		}
		root, e := os.OpenRoot(protected.path)
		if e != nil {
			return out, ErrStoreCustodyUnsupported
		}
		held, e := root.Stat(".")
		if e != nil || !os.SameFile(held, protected.identity) {
			root.Close()
			return out, ErrStoreCustodyUnsupported
		}
		e = fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
			entries++
			if walkErr != nil || ctx.Err() != nil || entries > 16384 || strings.Count(path, "/") > 64 || entry.Type()&os.ModeSymlink != 0 {
				return ErrStoreCustodyUnsupported
			}
			info, e := entry.Info()
			if e != nil {
				return ErrStoreCustodyUnsupported
			}
			// A link outside the protected mount inventory is reachable even
			// before the child opens it. Without full link-location custody,
			// reject multiply linked protected files rather than infer privacy.
			st, ok := info.Sys().(*syscall.Stat_t)
			if !ok || !info.IsDir() && st.Nlink != 1 {
				return ErrStoreCustodyUnsupported
			}
			key := protectedInode(info)
			if key == "" {
				return ErrStoreCustodyUnsupported
			}
			out.identities[key] = true
			return nil
		})
		root.Close()
		if e != nil {
			return out, ErrStoreCustodyUnsupported
		}
		info, e = os.Stat(protected.path)
		if e != nil || !physicalDirectory(protected.path) || !os.SameFile(info, protected.identity) {
			return out, ErrStoreCustodyUnsupported
		}
	}
	return out, nil
}
func protectedInode(info os.FileInfo) string {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%x:%x:%d", unix.Major(uint64(st.Dev)), unix.Minor(uint64(st.Dev)), st.Ino)
}
func (m protectedMetadata) hasIdentity(info os.FileInfo) bool {
	return m.identities[protectedInode(info)]
}
func (m protectedMetadata) exposedMapping(raw string) bool {
	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			return true
		}
		major, minor, ok := strings.Cut(fields[3], ":")
		a, ea := strconv.ParseUint(major, 16, 32)
		b, eb := strconv.ParseUint(minor, 16, 32)
		inode, ei := strconv.ParseUint(fields[4], 10, 64)
		if !ok || ea != nil || eb != nil || ei != nil {
			return true
		}
		if inode != 0 && m.identities[fmt.Sprintf("%x:%x:%d", a, b, inode)] {
			return true
		}
	}
	return false
}
