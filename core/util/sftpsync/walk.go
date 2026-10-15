package sftpsync

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// session is one transfer in progress. It holds the two ends, the two roots
// and the growing Result; nothing in here is reused between transfers.
type session struct {
	cfg     config
	src     fileSystem
	dst     fileSystem
	srcRoot string
	dstRoot string
	res     *Result
}

func newSession(cfg config, src, dst fileSystem, srcRoot, dstRoot string, dir Direction) *session {
	s := &session{
		cfg:     cfg,
		src:     src,
		dst:     dst,
		srcRoot: src.clean(srcRoot),
		dstRoot: dst.clean(dstRoot),
	}
	s.res = &Result{
		Direction:   dir,
		Source:      s.srcRoot,
		Destination: s.dstRoot,
		DryRun:      cfg.dryRun,
	}
	return s
}

func (s *session) srcPath(rel string) string { return s.src.resolve(s.srcRoot, rel) }
func (s *session) dstPath(rel string) string { return s.dst.resolve(s.dstRoot, rel) }

// run walks the source root and returns the Result even when it fails, so a
// caller can report how far the transfer got.
func (s *session) run(ctx context.Context) (*Result, error) {
	start := time.Now()
	err := s.walkRoot(ctx)
	s.res.Duration = time.Since(start)
	return s.res, err
}

func (s *session) walkRoot(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	// stat, not lstat: the caller named this path, so following a symlinked
	// root is their decision, not an escape. Only links found inside the tree
	// are subject to the escape check.
	info, err := s.src.stat(s.srcRoot)
	if err != nil {
		return pathErr("stat", s.src.side(), s.srcRoot, err)
	}
	if !info.IsDir() {
		return pathErr("stat", s.src.side(), s.srcRoot, ErrNotDirectory)
	}

	s.res.record(Entry{Path: ".", Action: ActionDir, Mode: info.Mode().Perm()})

	if !s.cfg.dryRun {
		if err := s.ensureDestRoot(info); err != nil {
			return err
		}
	}

	return s.walkDir(ctx, "", 0)
}

// ensureDestRoot creates the destination root if it is missing, and leaves it
// alone if it is not.
//
// A root that already exists keeps its own mode. Preserving the source's mode
// onto it would let a sync of a 0700 working copy quietly tighten an existing
// /srv/app that other things depend on being able to read — a side effect
// nobody asked for by saying "copy this directory into there". Directories
// created inside the tree do take the source mode; those are ours.
func (s *session) ensureDestRoot(srcInfo os.FileInfo) error {
	existing, err := s.dst.stat(s.dstRoot)
	if err == nil {
		if !existing.IsDir() {
			return pathErr("stat", s.dst.side(), s.dstRoot, ErrNotDirectory)
		}
		return nil
	}

	if err := s.dst.mkdirAll(s.dstRoot); err != nil {
		return pathErr("mkdir", s.dst.side(), s.dstRoot, err)
	}
	mode := s.cfg.dirMode
	if s.cfg.preserveMode {
		mode = srcInfo.Mode().Perm()
	}
	if err := s.dst.chmod(s.dstRoot, mode); err != nil {
		return pathErr("chmod", s.dst.side(), s.dstRoot, err)
	}
	return nil
}

func (s *session) walkDir(ctx context.Context, rel string, depth int) error {
	if depth >= s.cfg.maxDepth {
		return pathErr("walk", s.src.side(), s.srcPath(rel), ErrMaxDepthExceeded)
	}

	names, err := s.src.readDirNames(ctx, s.srcPath(rel))
	if err != nil {
		return pathErr("readdir", s.src.side(), s.srcPath(rel), err)
	}

	for _, name := range names {
		// Between entries as well as inside a file: a tree of ten thousand
		// small files would otherwise run to completion after the caller gave
		// up, because no single copy is long enough to notice.
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.walkEntry(ctx, path.Join(rel, name), depth); err != nil {
			return err
		}
	}
	return nil
}

func (s *session) walkEntry(ctx context.Context, rel string, depth int) error {
	srcPath := s.srcPath(rel)

	// lstat every entry rather than trusting the directory listing. SFTP's
	// SSH_FXP_READDIR carries attributes, but which stat they come from is
	// server-defined, and a server that reports a symlink's target instead of
	// the link would let a link present itself as a regular file and walk
	// straight past the escape check below. One extra round trip per entry is
	// the price of that check meaning anything.
	info, err := s.src.lstat(srcPath)
	if err != nil {
		return pathErr("lstat", s.src.side(), srcPath, err)
	}

	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return s.handleSymlink(ctx, rel, info, depth)
	case info.IsDir():
		return s.enterDir(ctx, rel, info, depth)
	case info.Mode().IsRegular():
		return s.copyFile(ctx, rel, info)
	default:
		// Sockets, devices, FIFOs. Recreating these across a transfer is
		// meaningless at best and a way to hand a remote host a device node
		// at worst, so they are reported and left behind.
		s.res.record(Entry{
			Path:   rel,
			Action: ActionSkip,
			Mode:   info.Mode().Perm(),
			Reason: fmt.Sprintf("irregular file (%s)", modeKind(info.Mode())),
		})
		return nil
	}
}

func (s *session) enterDir(ctx context.Context, rel string, info os.FileInfo, depth int) error {
	s.res.record(Entry{Path: rel, Action: ActionDir, Mode: s.dirModeFor(info)})

	if !s.cfg.dryRun {
		if err := s.dst.mkdirAll(s.dstPath(rel)); err != nil {
			return pathErr("mkdir", s.dst.side(), s.dstPath(rel), err)
		}
	}

	if err := s.walkDir(ctx, rel, depth+1); err != nil {
		return err
	}

	// Mode last, on the way out. povsister/scp and bramvdbogaerde/go-scp both
	// send a directory's mode with its creation, which is what the SCP
	// protocol requires; over SFTP we are free to do better. Applying it after
	// the contents are written means a source directory of 0500 arrives as
	// 0500 instead of failing to accept its own files.
	return s.applyDirAttrs(rel, info)
}

func (s *session) applyDirAttrs(rel string, info os.FileInfo) error {
	if s.cfg.dryRun {
		return nil
	}
	dstPath := s.dstPath(rel)
	if err := s.dst.chmod(dstPath, s.dirModeFor(info)); err != nil {
		return pathErr("chmod", s.dst.side(), dstPath, err)
	}
	if s.cfg.preserveModTime {
		if err := s.dst.chtimes(dstPath, time.Now(), info.ModTime()); err != nil {
			return pathErr("chtimes", s.dst.side(), dstPath, err)
		}
	}
	return nil
}

func (s *session) dirModeFor(info os.FileInfo) os.FileMode {
	if s.cfg.preserveMode {
		return info.Mode().Perm()
	}
	return s.cfg.dirMode
}

func (s *session) fileModeFor(info os.FileInfo) os.FileMode {
	if s.cfg.preserveMode {
		return info.Mode().Perm()
	}
	return s.cfg.fileMode
}

func (s *session) handleSymlink(ctx context.Context, rel string, info os.FileInfo, depth int) error {
	srcPath := s.srcPath(rel)

	if s.cfg.symlinks == SymlinkSkip {
		// Nothing is read and nothing is written, so an escaping target is
		// not an error here — it is simply not copied.
		s.res.record(Entry{
			Path:   rel,
			Action: ActionSkip,
			Mode:   info.Mode().Perm(),
			Reason: "symlink (policy: skip)",
		})
		return nil
	}

	target, err := s.src.readLink(srcPath)
	if err != nil {
		return pathErr("readlink", s.src.side(), srcPath, err)
	}

	if s.cfg.symlinks == SymlinkDereferenceIncludingOutsideRoot {
		return s.dereference(ctx, rel, target, depth)
	}

	// SymlinkReplicate.
	if escapesRoot(rel, target) {
		return pathErr("symlink", s.src.side(), srcPath,
			fmt.Errorf("%w: target %q", ErrSymlinkEscape, target))
	}

	s.res.record(Entry{
		Path: rel,
		// Mode is left zero: a symlink's own permission bits are not
		// portable (0777 on Linux, 0755 on macOS) and nothing chmods them.
		Action: ActionSymlink,
		Target: target,
	})
	if s.cfg.dryRun {
		return nil
	}
	return s.writeSymlink(rel, target)
}

// dereference follows a link and copies what it points at. Only reachable
// under SymlinkDereferenceIncludingOutsideRoot, where the caller has said in
// so many words that leaving the root is what they want.
func (s *session) dereference(ctx context.Context, rel, target string, depth int) error {
	srcPath := s.srcPath(rel)
	info, err := s.src.stat(srcPath)
	if err != nil {
		return pathErr("stat", s.src.side(), srcPath, err)
	}
	switch {
	case info.IsDir():
		// walkDir resolves children through srcPath(rel), which the server
		// follows for us; the depth bound is what stops a loop.
		return s.enterDir(ctx, rel, info, depth)
	case info.Mode().IsRegular():
		return s.copyFile(ctx, rel, info)
	default:
		s.res.record(Entry{
			Path:   rel,
			Action: ActionSkip,
			Mode:   info.Mode().Perm(),
			Reason: fmt.Sprintf("symlink to irregular file (%s) at %q", modeKind(info.Mode()), target),
		})
		return nil
	}
}

// escapesRoot reports whether a symlink at rel, pointing at target, resolves
// outside the sync root.
//
// The check is lexical on purpose. It needs no I/O, so it costs nothing and
// cannot be raced by a target that changes between the check and the copy; it
// gives the same answer on the local side and the remote one, where there is
// no EvalSymlinks to call; and it is sound precisely because nothing is ever
// read through a replicated link, so there is no chain to resolve.
func escapesRoot(rel, target string) bool {
	if target == "" {
		return true
	}
	// filepath.IsAbs as well as path.IsAbs, so a Windows-style "C:\secrets"
	// target is caught when this runs on Windows.
	if path.IsAbs(target) || filepath.IsAbs(target) {
		// An absolute target names a place on one machine. The same absolute
		// path on the other machine is a different place, so even a target
		// that happens to sit inside the source root is refused rather than
		// replicated into something this package cannot vouch for.
		return true
	}
	resolved := path.Join(path.Dir(rel), filepath.ToSlash(target))
	return resolved == ".." || strings.HasPrefix(resolved, "../")
}

func modeKind(m os.FileMode) string {
	switch {
	case m&os.ModeDevice != 0 && m&os.ModeCharDevice != 0:
		return "character device"
	case m&os.ModeDevice != 0:
		return "block device"
	case m&os.ModeNamedPipe != 0:
		return "named pipe"
	case m&os.ModeSocket != 0:
		return "socket"
	case m&os.ModeIrregular != 0:
		return "irregular"
	default:
		return m.Type().String()
	}
}
