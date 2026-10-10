package snapshot

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// CaptureLimits bounds one safe ingestion pass. Host admission additionally
// accounts for total retained storage, run limits and pinned objects.
type CaptureLimits struct {
	MaxBytes, MaxFileBytes int64
	MaxEntries             int
}

var ErrCaptureLimit = errors.New("snapshot: capture limit reached")

type mirrorUsage struct {
	Bytes             int64
	Entries, Excluded int
	Files             int
}

// prepareMirror never gives Git a live agent tree. Only regular eligible files
// reach the private mirror. Callers must hold actual store/source custody and
// budget admission through mirror ingestion, Git effects and accounting.
func prepareMirror(ctx context.Context, scope plannedRoot, destination string, limits CaptureLimits, secretIdentities []fs.FileInfo) (mirrorUsage, error) {
	usage := mirrorUsage{}
	if limits.MaxBytes <= 0 || limits.MaxFileBytes <= 0 || limits.MaxEntries <= 0 || limits.MaxFileBytes > limits.MaxBytes {
		return usage, ErrCaptureLimit
	}
	if !physicalDirectory(scope.Binding.Root) {
		return usage, ErrCoverageUnsupported
	}
	source, err := os.OpenRoot(scope.Binding.Root)
	if err != nil {
		return usage, ErrCoverageUnsupported
	}
	defer source.Close()
	if scope.identity != nil {
		held, e := source.Stat(".")
		if e != nil || !os.SameFile(scope.identity, held) {
			return usage, ErrCoverageUnsupported
		}
	}
	dest, err := os.OpenRoot(destination)
	if err != nil {
		return usage, ErrCoverageUnsupported
	}
	defer dest.Close()
	err = fs.WalkDir(source.FS(), ".", func(rel string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return ErrCoverageUnsupported
		}
		if rel == "." {
			return nil
		}
		usage.Entries++
		if usage.Entries > limits.MaxEntries {
			return ErrCaptureLimit
		}
		abs := filepath.Join(scope.Binding.Root, filepath.FromSlash(rel))
		// Ancestors of included paths must be traversed, but never read as files.
		selected := scope.allows(abs)
		ancestor := false
		for _, include := range scope.Include {
			if withinRelative(rel, include) {
				ancestor = true
			}
		}
		if SecretExcluded(rel) || excludedRelative(scope, rel) || (!selected && !ancestor) {
			usage.Excluded++
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return ErrCoverageUnsupported
		}
		before, err := entry.Info()
		if err != nil {
			return ErrCoverageUnsupported
		}
		if before.IsDir() {
			if err := dest.MkdirAll(filepath.FromSlash(rel), 0700); err != nil {
				return ErrCoverageUnsupported
			}
			return nil
		}
		if !selected || !before.Mode().IsRegular() {
			return ErrCoverageUnsupported
		}
		for _, secret := range secretIdentities {
			if os.SameFile(before, secret) {
				usage.Excluded++
				return nil
			}
		}
		if before.Size() > limits.MaxFileBytes || before.Size() > limits.MaxBytes-usage.Bytes {
			return ErrCaptureLimit
		}
		input, err := openCaptureFile(source, filepath.FromSlash(rel))
		if err != nil {
			return ErrCoverageUnsupported
		}
		defer input.Close()
		opened, err := input.Stat()
		if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
			return ErrCoverageUnsupported
		}
		// No content read occurs before descriptor identity/type validation.
		mode := fs.FileMode(0600)
		if before.Mode()&0111 != 0 {
			mode = 0700
		}
		output, err := dest.OpenFile(filepath.FromSlash(rel), os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return ErrCoverageUnsupported
		}
		n, copyErr := io.Copy(output, io.LimitReader(input, min(limits.MaxFileBytes, limits.MaxBytes-usage.Bytes)+1))
		closeErr := output.Close()
		after, statErr := input.Stat()
		if copyErr != nil || closeErr != nil || statErr != nil {
			return ErrCoverageUnsupported
		}
		if n > limits.MaxFileBytes || n > limits.MaxBytes-usage.Bytes {
			return ErrCaptureLimit
		}
		if n != before.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
			return ErrCoverageUnsupported
		}
		usage.Bytes += n
		usage.Files++
		return nil
	})
	if err != nil {
		return usage, err
	}
	if scope.identity != nil {
		named, e := os.Stat(scope.Binding.Root)
		if e != nil || !physicalDirectory(scope.Binding.Root) || !os.SameFile(scope.identity, named) {
			return usage, ErrCoverageUnsupported
		}
	}
	if usage.Files == 0 { // Empty coverage cannot quietly become a whole-root add.
		return usage, ErrCoverageUnsupported
	}
	return usage, nil
}

func excludedRelative(scope plannedRoot, rel string) bool {
	for _, exclude := range scope.Exclude {
		if withinRelative(exclude, rel) {
			return true
		}
	}
	return strings.ContainsRune(rel, 0)
}
