//go:build linux || darwin

package materialize

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/hollis-labs/substrate/harness/workspace/publication"
)

func preflightPublicationCustody(context.Context, PublishRequest) (*publicationCustody, error) {
	// Complete volume/root descriptor/revision and ALL metadata enumeration
	// capability has no approved actual producer yet. Private mode or an empty
	// unprivileged xattr list is not an attestation. No public bool/callback mint.
	return nil, ErrUnsupportedOperation
}

type publicationNative struct {
	parent, control *os.Root
	request         PublishRequest
}

func openPublicationNative(req PublishRequest) (*publicationNative, error) {
	parent, err := os.OpenRoot(req.Journal.Layout.Parent.Path)
	if err != nil {
		return nil, err
	}
	control, err := os.OpenRoot(req.Journal.Control.Path)
	if err != nil {
		return nil, errors.Join(err, parent.Close())
	}
	return &publicationNative{parent: parent, control: control, request: req}, nil
}
func (p *publicationNative) close() error { return errors.Join(p.parent.Close(), p.control.Close()) }
func (p *publicationNative) rename(from, to string) error {
	// This private method only receives the two names derived from the frozen
	// closed request; no caller port, generic write callback or fallback exists.
	if err := p.parent.Rename(from, to); err != nil {
		return err
	}
	dir, err := p.parent.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
func nativePublicationIdentity(info os.FileInfo, expected publication.FileIdentity) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && expected.Valid() && uint64(stat.Dev) == expected.Device && stat.Ino == expected.Inode && int(stat.Uid) == os.Getuid()
}
func publicationRootCustody(root *os.Root, expected publication.Root) error {
	opened, err := root.Stat(".")
	if err != nil {
		return err
	}
	named, err := os.Lstat(expected.Path)
	if err != nil {
		return err
	}
	canonical, err := filepath.EvalSymlinks(expected.Path)
	if err != nil {
		return err
	}
	if canonical != expected.Path || !os.SameFile(opened, named) || opened.Mode() != os.ModeDir|0700 || !nativePublicationIdentity(opened, expected.Identity) {
		return ErrUnsafeTarget
	}
	return nil
}
func (p *publicationNative) directory(name string, identity publication.FileIdentity, generation, digest string) error {
	if identity.Device != p.request.Journal.Layout.Parent.Identity.Device {
		return ErrUnsafeTarget
	}
	info, err := p.parent.Lstat(name)
	if err != nil {
		return err
	}
	if info.Mode() != os.ModeDir|0700 || !nativePublicationIdentity(info, identity) {
		return ErrUnsafeTarget
	}
	root, err := p.parent.OpenRoot(name)
	if err != nil {
		return err
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(opened, info) {
		return ErrUnsafeTarget
	}
	meta, err := root.Lstat(".materialize")
	if err != nil || !meta.IsDir() || meta.Mode()&(os.ModeSymlink|os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return ErrUnsafeTarget
	}
	manifestInfo, err := root.Lstat(ManifestRelPath)
	if err != nil || !privatePublicationManifest(manifestInfo) || manifestInfo.Size() > 1<<20 {
		return ErrUnsafeTarget
	}
	file, err := root.OpenFile(ManifestRelPath, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil || !os.SameFile(actual, manifestInfo) || !privatePublicationManifest(actual) {
		return ErrUnsafeTarget
	}
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return ErrUnsafeTarget
	}
	got := sha256.Sum256(data)
	var manifest Manifest
	if hex.EncodeToString(got[:]) != digest || json.Unmarshal(data, &manifest) != nil || manifest.Generation != generation {
		return ErrStaleGeneration
	}
	return nil
}

func privatePublicationManifest(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode() == 0600 && stat.Nlink == 1 && int(stat.Uid) == os.Getuid()
}
func (p *publicationNative) absent(name string) error {
	_, err := p.parent.Lstat(name)
	if !os.IsNotExist(err) {
		return ErrTargetExists
	}
	return nil
}
func (p *publicationNative) validate(ctx context.Context, j publication.Journal, phase publication.Phase) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !samePublicationPins(j, p.request.Journal) {
		return ErrUnsafeTarget
	}
	if err := publicationRootCustody(p.parent, j.Layout.Parent); err != nil {
		return err
	}
	if err := publicationRootCustody(p.control, j.Control); err != nil {
		return err
	}
	key := sha256.Sum256([]byte(j.Use.CanonicalID))
	for _, resource := range []struct {
		path, prefix string
		identity     publication.FileIdentity
	}{{j.Use.MutationPath, "lock-", j.Use.MutationIdentity}, {j.Use.PinPath, "pin-", j.Use.PinIdentity}} {
		if resource.path != filepath.Join(j.Use.Namespace, resource.prefix+hex.EncodeToString(key[:])) {
			return ErrUnsafeTarget
		}
		info, err := p.control.Lstat(filepath.Base(resource.path))
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || info.Mode() != 0600 || stat.Nlink != 1 || !nativePublicationIdentity(info, resource.identity) {
			return ErrUnsafeTarget
		}
	}
	l := j.Layout
	newCurrent := phase == publication.CurrentObserved || phase == publication.PublicationCommitted
	oldMoved := l.CurrentExists && (phase == publication.OldAsideObserved || phase == publication.CandidateCurrentIntent || newCurrent)
	if newCurrent {
		if err := p.directory(filepath.Base(l.Current.Path), l.Candidate.Identity, l.CandidateGeneration, l.CandidateManifestDigest); err != nil {
			return err
		}
		if err := p.absent(filepath.Base(l.Candidate.Path)); err != nil {
			return err
		}
	} else {
		if err := p.directory(filepath.Base(l.Candidate.Path), l.Candidate.Identity, l.CandidateGeneration, l.CandidateManifestDigest); err != nil {
			return err
		}
		if l.CurrentExists && !oldMoved {
			if err := p.directory(filepath.Base(l.Current.Path), l.Current.Identity, l.ExpectedCurrentGeneration, l.ExpectedCurrentManifestDigest); err != nil {
				return err
			}
		} else if err := p.absent(filepath.Base(l.Current.Path)); err != nil {
			return err
		}
	}
	if oldMoved {
		if err := p.directory(filepath.Base(l.Aside.Path), l.Current.Identity, l.ExpectedCurrentGeneration, l.ExpectedCurrentManifestDigest); err != nil {
			return err
		}
	} else if err := p.absent(filepath.Base(l.Aside.Path)); err != nil {
		return err
	}
	return ctx.Err()
}
