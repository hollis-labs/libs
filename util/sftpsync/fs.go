package sftpsync

import (
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"time"

	"github.com/pkg/sftp"
)

// fileSystem is the seam that makes upload and download one implementation
// instead of two. Both directions run the same walk, the same escape check
// and the same temp-and-rename writer; only which side is source and which
// is destination differs.
//
// Keeping this unexported is deliberate. It is a shape chosen to make the
// walk symmetric, not a plugin point — exporting it would promise that any
// fileSystem works here, which is a much larger claim than "local and SFTP do".
type fileSystem interface {
	// side names this end of the transfer for error messages.
	side() Side

	// clean normalises a caller-supplied root path.
	clean(root string) string
	// resolve turns a slash-separated path relative to root into an absolute
	// path in this filesystem's own separator convention.
	resolve(root, rel string) string

	// lstat does not follow a final symlink; stat does.
	lstat(name string) (os.FileInfo, error)
	stat(name string) (os.FileInfo, error)
	// readDirNames returns the entry names in name, sorted. It returns names
	// only: the caller lstats each one, see the comment in walk.go.
	readDirNames(ctx context.Context, name string) ([]string, error)
	readLink(name string) (string, error)

	open(name string) (io.ReadCloser, error)
	// createExcl creates name, failing if it already exists. Every write this
	// package makes goes to a fresh temp name, so exclusive creation is free
	// insurance against clobbering something the walk did not expect.
	createExcl(name string) (io.WriteCloser, error)

	mkdirAll(name string) error
	chmod(name string, mode os.FileMode) error
	chtimes(name string, atime, mtime time.Time) error
	symlink(target, name string) error
	remove(name string) error
	// rename moves oldname onto newname, replacing newname if it exists.
	rename(oldname, newname string) error
}

// localFS is this machine, through package os.
type localFS struct{}

func (localFS) side() Side { return Local }

func (localFS) clean(root string) string { return filepath.Clean(root) }

func (localFS) resolve(root, rel string) string {
	if rel == "" || rel == "." {
		return root
	}
	return filepath.Join(root, filepath.FromSlash(rel))
}

func (localFS) lstat(name string) (os.FileInfo, error) { return os.Lstat(name) }
func (localFS) stat(name string) (os.FileInfo, error)  { return os.Stat(name) }

func (localFS) readDirNames(_ context.Context, name string) ([]string, error) {
	entries, err := os.ReadDir(name)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

func (localFS) readLink(name string) (string, error) { return os.Readlink(name) }

func (localFS) open(name string) (io.ReadCloser, error) { return os.Open(name) }

func (localFS) createExcl(name string) (io.WriteCloser, error) {
	// 0600 while it is a temp file: a half-written file should not be
	// readable by anyone who was not going to be allowed to read the finished
	// one. The real mode is applied before the rename.
	return os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_TRUNC, 0o600)
}

func (localFS) mkdirAll(name string) error {
	// 0700 while the walk is still writing into it. The source mode is
	// applied on the way out, so a source directory that forbids writing
	// still ends up with exactly its own bits and no window where the tree
	// is world-readable in between.
	return os.MkdirAll(name, 0o700)
}

func (localFS) chmod(name string, mode os.FileMode) error { return os.Chmod(name, mode) }

func (localFS) chtimes(name string, atime, mtime time.Time) error {
	return os.Chtimes(name, atime, mtime)
}

func (localFS) symlink(target, name string) error { return os.Symlink(target, name) }
func (localFS) remove(name string) error          { return os.Remove(name) }

func (localFS) rename(oldname, newname string) error { return os.Rename(oldname, newname) }

// remoteFS is the far end, through an *sftp.Client the caller owns.
type remoteFS struct {
	c *sftp.Client
	// posixRename caches whether the server advertised
	// posix-rename@openssh.com. Read once at construction from the version
	// packet the client already has — no round trip, and no boot-time probe
	// whose failure would be cached for the life of the connection.
	posixRename bool
}

func newRemoteFS(c *sftp.Client) remoteFS {
	_, ok := c.HasExtension("posix-rename@openssh.com")
	return remoteFS{c: c, posixRename: ok}
}

func (remoteFS) side() Side { return Remote }

// clean uses path, not filepath: remote paths are slash-separated regardless
// of the OS this code is compiled for. Running on Windows must not turn
// /srv/app into \srv\app on the far end.
func (remoteFS) clean(root string) string { return path.Clean(root) }

func (remoteFS) resolve(root, rel string) string {
	if rel == "" || rel == "." {
		return root
	}
	return path.Join(root, rel)
}

func (f remoteFS) lstat(name string) (os.FileInfo, error) { return f.c.Lstat(name) }
func (f remoteFS) stat(name string) (os.FileInfo, error)  { return f.c.Stat(name) }

func (f remoteFS) readDirNames(ctx context.Context, name string) ([]string, error) {
	infos, err := f.c.ReadDirContext(ctx, name)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(infos))
	for _, fi := range infos {
		if n := fi.Name(); n != "." && n != ".." {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names, nil
}

func (f remoteFS) readLink(name string) (string, error) { return f.c.ReadLink(name) }

func (f remoteFS) open(name string) (io.ReadCloser, error) { return f.c.Open(name) }

func (f remoteFS) createExcl(name string) (io.WriteCloser, error) {
	// SFTP has no mode argument on open, so the temp file briefly wears the
	// server's default. It is renamed into place only after chmod, so the
	// finished file never does.
	return f.c.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_TRUNC)
}

func (f remoteFS) mkdirAll(name string) error { return f.c.MkdirAll(name) }

func (f remoteFS) chmod(name string, mode os.FileMode) error { return f.c.Chmod(name, mode) }

func (f remoteFS) chtimes(name string, atime, mtime time.Time) error {
	return f.c.Chtimes(name, atime, mtime)
}

func (f remoteFS) symlink(target, name string) error { return f.c.Symlink(target, name) }
func (f remoteFS) remove(name string) error          { return f.c.Remove(name) }

func (f remoteFS) rename(oldname, newname string) error {
	if f.posixRename {
		// posix-rename@openssh.com replaces the destination atomically. When
		// the server offers it, a failure here is a real failure and must not
		// fall through to the destructive path below.
		return f.c.PosixRename(oldname, newname)
	}
	// Plain SSH_FXP_RENAME fails when the destination exists, so it has to go
	// first. This is the one window temp-and-rename cannot close, and it is
	// only open on servers without the OpenSSH extension.
	//
	// The remove's error is dropped on purpose: the destination usually does
	// not exist, and any reason the removal mattered resurfaces immediately as
	// a rename failure that names the path.
	_ = f.c.Remove(newname)
	return f.c.Rename(oldname, newname)
}
