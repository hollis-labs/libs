package sftpsync

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path"
	"time"
)

// tempSuffix marks the files this package writes before renaming them into
// place. It is distinctive so that a crashed transfer leaves debris a human
// can recognise, and the name starts with a dot so a half-written file is not
// picked up by a glob that was looking for the finished one.
const tempSuffix = ".sftpsync-tmp"

// maxTempBase bounds how much of the real filename the temp name carries, so
// the total stays inside the 255-byte limit most filesystems impose.
const maxTempBase = 120

func (s *session) copyFile(ctx context.Context, rel string, info os.FileInfo) error {
	s.res.record(Entry{
		Path:   rel,
		Action: ActionFile,
		Mode:   s.fileModeFor(info),
		Size:   info.Size(),
	})

	if s.cfg.dryRun {
		// Counted from the source stat so that a dry run reports the same
		// byte total as the transfer it previews.
		s.res.Bytes += info.Size()
		return nil
	}

	n, err := s.transfer(ctx, rel, info)
	s.res.Bytes += n
	return err
}

// transfer writes one file to a sibling temp name and renames it into place.
//
// The destination therefore only ever holds the previous file or the complete
// new one: an interrupted sync cannot leave a truncated file where a working
// one was. Mode and modification time are applied to the temp file before the
// rename, so the file arrives with its final attributes in the same instant it
// arrives with its contents — never complete but not yet executable.
func (s *session) transfer(ctx context.Context, rel string, info os.FileInfo) (int64, error) {
	srcPath := s.srcPath(rel)
	dstPath := s.dstPath(rel)

	r, err := s.src.open(srcPath)
	if err != nil {
		return 0, pathErr("open", s.src.side(), srcPath, err)
	}
	defer r.Close() //nolint:errcheck // read side; a close error cannot affect what was written

	tmpRel, err := tempRel(rel)
	if err != nil {
		return 0, err
	}
	tmpPath := s.dstPath(tmpRel)

	w, err := s.dst.createExcl(tmpPath)
	if err != nil {
		return 0, pathErr("create", s.dst.side(), tmpPath, err)
	}

	// From here on every failure has to take the temp file with it, or a
	// failing sync litters the destination with debris that looks like data.
	// The wrapped error keeps the cause reachable through errors.Is, so a
	// cancelled copy still answers to context.Canceled while naming the file.
	discard := func(op string, cause error) error {
		_ = s.dst.remove(tmpPath)
		return pathErr(op, s.dst.side(), tmpPath, cause)
	}

	n, err := io.Copy(w, &ctxReader{ctx: ctx, r: r})
	if err != nil {
		_ = w.Close()
		return n, discard("copy", err)
	}
	if err := w.Close(); err != nil {
		return n, discard("close", err)
	}

	if err := s.dst.chmod(tmpPath, s.fileModeFor(info)); err != nil {
		return n, discard("chmod", err)
	}
	if s.cfg.preserveModTime {
		if err := s.dst.chtimes(tmpPath, time.Now(), info.ModTime()); err != nil {
			return n, discard("chtimes", err)
		}
	}

	if err := s.dst.rename(tmpPath, dstPath); err != nil {
		_ = s.dst.remove(tmpPath)
		return n, pathErr("rename", s.dst.side(), dstPath, err)
	}
	return n, nil
}

// writeSymlink creates the link at a temp name and renames it into place, for
// the same reason files are written that way: the destination holds the old
// link or the new one, never neither.
func (s *session) writeSymlink(rel, target string) error {
	tmpRel, err := tempRel(rel)
	if err != nil {
		return err
	}
	tmpPath := s.dstPath(tmpRel)
	dstPath := s.dstPath(rel)

	if err := s.dst.symlink(target, tmpPath); err != nil {
		return pathErr("symlink", s.dst.side(), tmpPath, err)
	}
	if err := s.dst.rename(tmpPath, dstPath); err != nil {
		_ = s.dst.remove(tmpPath)
		return pathErr("rename", s.dst.side(), dstPath, err)
	}
	return nil
}

// tempRel builds a temp path that is a sibling of rel, so the rename that
// follows stays within one directory and therefore within one filesystem.
func tempRel(rel string) (string, error) {
	base := []rune(path.Base(rel))
	if len(base) > maxTempBase {
		base = base[:maxTempBase]
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("sftpsync: generate temp name for %q: %w", rel, err)
	}
	name := fmt.Sprintf(".%s.%x%s", string(base), suffix, tempSuffix)
	return path.Join(path.Dir(rel), name), nil
}

// ctxReader makes an io.Copy cancellable.
//
// io.Copy has no context, and the alternative — a hand-rolled loop with a
// ctx.Err() check per chunk — would give up pkg/sftp's pipelined writes,
// which are most of its throughput over a real link. Gating the reader
// instead keeps the fast path (io.Copy finds (*sftp.File).ReadFrom or
// (*os.File).ReadFrom and pipelines) while still checking cancellation on
// every chunk pulled through it, because the wrapper deliberately does not
// implement io.WriterTo and so cannot be bypassed.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
