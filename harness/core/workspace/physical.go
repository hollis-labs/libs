package workspace

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/hollis-labs/substrate/harness/workspace/materialize"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
)

// InspectRoot observes only root metadata and the engine control manifest. It
// never walks arbitrary content or reads credential destinations. Ownership is
// supplied by an authority; this function does not infer it from disk.
func InspectRoot(ref RootRef) (RootObservation, error) {
	if err := ref.Validate(); err != nil {
		return RootObservation{}, err
	}
	base, err := filepath.EvalSymlinks(ref.AllowedBase)
	if err != nil {
		return RootObservation{}, refuse(CodeCanonicalBaseUnavailable, "roots", Unsupported)
	}
	canonical, err := canonicalMissing(ref.Path)
	if err != nil || !within(base, canonical) || canonical != ref.Path || base != ref.AllowedBase {
		return RootObservation{}, refuse(CodeNoncanonicalRoot, "roots", Conflict)
	}
	o := RootObservation{RootID: ref.ID, DeclaredPath: ref.Path, CanonicalPath: canonical, CanonicalBase: base, Owner: ref.Owner}
	info, err := os.Lstat(ref.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return o, nil
	}
	if err != nil {
		return o, err
	}
	o.Exists = true
	o.Directory = info.IsDir() && info.Mode()&os.ModeSymlink == 0
	if !o.Directory {
		return o, refuse(CodeRootNotDirectory, "roots", Conflict)
	}
	root, err := os.OpenRoot(ref.Path)
	if err != nil {
		return o, err
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return o, err
	}
	names, readErr := dir.Readdirnames(1)
	closeErr := dir.Close()
	if readErr != nil && readErr != io.EOF {
		return o, readErr
	}
	if closeErr != nil {
		return o, closeErr
	}
	o.Empty = len(names) == 0
	manifest, err := readManifest(root)
	if errors.Is(err, fs.ErrNotExist) {
		return o, nil
	}
	if err != nil {
		return o, err
	}
	o.Manifest = &manifest
	return o, nil
}
func canonicalMissing(path string) (string, error) {
	var suffix []string
	for {
		_, err := os.Lstat(path)
		if err == nil {
			physical, err := filepath.EvalSymlinks(path)
			if err != nil {
				return "", err
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				physical = filepath.Join(physical, suffix[i])
			}
			return physical, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", err
		}
		suffix = append(suffix, filepath.Base(path))
		path = parent
	}
}
func noSymlinks(root *os.Root, rel string) error {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i := range parts {
		info, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return refuse(CodeSymlinkManagedPath, "artifacts", Conflict)
		}
	}
	return nil
}
func readManifest(root *os.Root) (materialize.Manifest, error) {
	if err := noSymlinks(root, materialize.ManifestRelPath); err != nil {
		return materialize.Manifest{}, err
	}
	// Reject static unsafe types before open: opening a FIFO for reading can
	// wait indefinitely for a writer while apply holds the workspace locks.
	info, err := root.Lstat(materialize.ManifestRelPath)
	if err != nil {
		return materialize.Manifest{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return materialize.Manifest{}, refuse(CodeInvalidCommittedManifest, "manifest", Conflict)
	}
	file, err := root.Open(materialize.ManifestRelPath)
	if err != nil {
		return materialize.Manifest{}, err
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return materialize.Manifest{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return materialize.Manifest{}, refuse(CodeInvalidCommittedManifest, "manifest", Conflict)
	}
	var m materialize.Manifest
	decoder := json.NewDecoder(io.LimitReader(file, (16<<20)+1))
	if err := decoder.Decode(&m); err != nil {
		return m, refuse(CodeInvalidCommittedManifest, "manifest", Conflict)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return m, refuse(CodeInvalidCommittedManifest, "manifest", Conflict)
	}
	if m.SchemaVersion != "materialize.v1" || m.Generation == "" {
		return m, refuse(CodeInvalidCommittedManifest, "manifest", Conflict)
	}
	seen := map[string]bool{}
	for _, e := range m.Entries {
		if !e.Kind.Valid() || e.Ownership.EntryID == "" || e.Ownership.GroupID == "" || e.Provenance.Source == "" || seen[e.Path] || e.Mode > 0777 {
			return m, refuse(CodeInvalidCommittedManifest, "manifest", Conflict)
		}
		seen[e.Path] = true
	}
	if err := ValidateManagedManifest(m, nil); err != nil {
		return m, err
	}
	return m, nil
}
func verifyHandle(a Action, h materialize.Handle, destinations []string) (materialize.Manifest, error) {
	if !h.Report.Complete || h.TargetRoot != a.Root.Path {
		return materialize.Manifest{}, refuse(CodeIncompleteEngineResult, "artifacts", Partial)
	}
	info, err := os.Lstat(a.Root.Path)
	if err != nil {
		return materialize.Manifest{}, err
	}
	if !info.IsDir() || info.Mode()&(os.ModeSymlink|os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || info.Mode().Perm() != a.RootMode.Perm() {
		return materialize.Manifest{}, refuse(CodeCommittedRootModeMismatch, "roots", Partial)
	}
	root, err := os.OpenRoot(a.Root.Path)
	if err != nil {
		return materialize.Manifest{}, err
	}
	defer root.Close()
	m, err := readManifest(root)
	if err != nil {
		return m, err
	}
	if !reflect.DeepEqual(m, h.Manifest) || m.Generation != a.Request.Generation {
		return m, refuse(CodeCommittedManifestMismatch, "manifest", Partial)
	}
	if err := ValidateManagedManifest(m, destinations); err != nil {
		return m, err
	}
	for _, entry := range m.Entries {
		if err := noSymlinks(root, entry.Path); err != nil {
			return m, err
		}
		info, err := root.Stat(entry.Path)
		if err != nil {
			return m, err
		}
		if info.Mode().Perm() != fs.FileMode(entry.Mode) || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return m, refuse(CodeCommittedModeMismatch, "artifacts", Partial)
		}
		if entry.Kind == artifact.EntryDirectory {
			if !info.IsDir() {
				return m, refuse(CodeCommittedKindMismatch, "artifacts", Partial)
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return m, refuse(CodeCommittedKindMismatch, "artifacts", Partial)
		}
		// Compare to frozen desired bytes when available; retained unselected files
		// are checked by their committed digest without reading credential slots.
		file, err := root.Open(entry.Path)
		if err != nil {
			return m, err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, 64<<20))
		closeErr := file.Close()
		if readErr != nil {
			return m, readErr
		}
		if closeErr != nil {
			return m, closeErr
		}
		if info.Size() > 64<<20 || artifact.DigestBytes(data) != entry.Digest {
			return m, refuse(CodeCommittedDigestMismatch, "artifacts", Partial)
		}
	}
	return m, nil
}

func validateExistingPaths(a Action) error {
	root, err := os.OpenRoot(a.Root.Path)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, entry := range a.Request.Artifacts.Entries {
		if err := noSymlinks(root, entry.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}
