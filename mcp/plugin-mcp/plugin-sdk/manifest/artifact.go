package manifest

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"
)

// Artifact covers all regular bundle files except plugin.yaml. The host must
// separately pin the reviewed manifest. Directories carry no digest material.
type Artifact struct {
	Files      []ArtifactFile `json:"files"`
	TreeSHA256 string         `json:"tree_sha256"`
}
type ArtifactFile struct {
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	Executable bool   `json:"executable,omitempty"`
}

func validHash(s string) bool {
	if len(s) != 64 || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
func validateFiles(files []ArtifactFile) error {
	if len(files) == 0 {
		return fmt.Errorf("artifact.files must be nonempty")
	}
	seen := map[string]bool{}
	for _, f := range files {
		key := strings.ToLower(f.Path)
		if !validBundlePath(f.Path) || key == strings.ToLower(Filename) {
			return fmt.Errorf("artifact.files has invalid path %q", f.Path)
		}
		if seen[key] {
			return fmt.Errorf("artifact.files duplicates path %q (case-insensitive)", f.Path)
		}
		seen[key] = true
		if !validHash(f.SHA256) {
			return fmt.Errorf("artifact.files.%s.sha256 must be lowercase hex SHA-256", f.Path)
		}
	}
	// A path cannot be both a file and a directory, on any supported filesystem.
	for _, f := range files {
		parts := strings.Split(strings.ToLower(f.Path), "/")
		for i := 1; i < len(parts); i++ {
			if seen[strings.Join(parts[:i], "/")] {
				return fmt.Errorf("artifact.files has file/directory collision at %s", f.Path)
			}
		}
	}
	return nil
}

// TreeDigest hashes domain || records ordered by ASCII path bytes. Each record
// is uint32-BE path byte length || path bytes || raw 32-byte SHA-256 || executable
// byte (0 or 1). Domain is "plugin-sdk-artifact-v1\x00". Input order is irrelevant.
func TreeDigest(files []ArtifactFile) (string, error) {
	if err := validateFiles(files); err != nil {
		return "", err
	}
	ordered := slices.Clone(files)
	slices.SortFunc(ordered, func(a, b ArtifactFile) int { return strings.Compare(a.Path, b.Path) })
	h := sha256.New()
	h.Write([]byte("plugin-sdk-artifact-v1\x00"))
	for _, f := range ordered {
		if uint64(len(f.Path)) > uint64(^uint32(0)) {
			return "", fmt.Errorf("artifact path too long")
		}
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(f.Path)))
		h.Write(size[:])
		h.Write([]byte(f.Path))
		sum, _ := hex.DecodeString(f.SHA256)
		h.Write(sum)
		mode := byte(0)
		if f.Executable {
			mode = 1
		}
		h.Write([]byte{mode})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func (a Artifact) Validate() error {
	digest, err := TreeDigest(a.Files)
	if err != nil {
		return err
	}
	if !validHash(a.TreeSHA256) || digest != a.TreeSHA256 {
		return fmt.Errorf("artifact.tree_sha256 does not match file inventory")
	}
	return nil
}

// VerifyBundle verifies an immutable, private staged tree. It rejects symlinks
// (including directories), special files, missing/extra files and mode/hash
// mismatches. It never executes plugin code. The caller must prevent concurrent
// mutation and execute the same verified snapshot; this is not a filesystem lock.
func (m Manifest) VerifyBundle(dir string) error {
	if err := m.Validate(); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("bundle root must be a real directory")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	expected := map[string]ArtifactFile{}
	for _, f := range m.Artifact.Files {
		expected[f.Path] = f
	}
	manifestSeen := false
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		st, err := root.Lstat(name)
		if err != nil {
			return err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("bundle symlink rejected: %s", name)
		}
		if st.IsDir() {
			return nil
		}
		if !st.Mode().IsRegular() || st.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
			return fmt.Errorf("bundle special file/mode rejected: %s", name)
		}
		if name == Filename {
			manifestSeen = true
			f, err := root.Open(name)
			if err != nil {
				return err
			}
			defer f.Close()
			decoded, err := Decode(f)
			if err != nil {
				return err
			}
			// Compare canonical declarations to ensure the caller verified the file
			// that it reviewed, not a different manifest beside the same artifacts.
			var a, b strings.Builder
			if err := Encode(&a, m); err != nil {
				return err
			}
			if err := Encode(&b, decoded); err != nil {
				return err
			}
			if a.String() != b.String() {
				return fmt.Errorf("bundle manifest differs from reviewed declaration")
			}
			return nil
		}
		want, ok := expected[name]
		if !ok {
			return fmt.Errorf("bundle contains unlisted file %s", name)
		}
		if (st.Mode().Perm()&0111 != 0) != want.Executable {
			return fmt.Errorf("bundle executable mode mismatch: %s", name)
		}
		f, err := root.Open(name)
		if err != nil {
			return err
		}
		h := sha256.New()
		_, readErr := io.Copy(h, f)
		closeErr := f.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if hex.EncodeToString(h.Sum(nil)) != want.SHA256 {
			return fmt.Errorf("bundle SHA-256 mismatch: %s", name)
		}
		delete(expected, name)
		return nil
	})
	if err != nil {
		return err
	}
	if !manifestSeen {
		return fmt.Errorf("bundle is missing %s", Filename)
	}
	if len(expected) > 0 {
		return fmt.Errorf("bundle is missing %s", sortedKeys(expected)[0])
	}
	return nil
}
