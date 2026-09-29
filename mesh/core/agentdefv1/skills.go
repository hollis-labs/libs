package agentdef

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// SkillRef is one resolved, hash-pinned skill reference.
type SkillRef struct {
	Name        string // matches the Agent Skills name rule
	Description string // pulled from SKILL.md, required present
	Path        string // skills/<name> relative to the definition's layer root
	Hash        string // "sha256:<hex>" over the whole skills/<name>/ tree, sorted paths

	// src and dir locate the tree ResolveSkills read, for CopySkills.
	src fs.FS
	dir string
}

// ResolveSkills finds skills/<name>/SKILL.md under layerRoot for every name in
// d.Skills, checks that SKILL.md carries name (equal to its directory name)
// and description — the Agent Skills required fields; this is NOT a full
// conformance check — and pins each skill with a hash over its whole tree. A
// dangling reference is an error. Files named .DS_Store are ignored by the
// hash.
func ResolveSkills(fsys fs.FS, layerRoot string, d *Definition) ([]SkillRef, error) {
	if layerRoot == "" {
		layerRoot = "."
	}
	refs := make([]SkillRef, 0, len(d.Skills))
	for _, name := range d.Skills {
		if !namePattern.MatchString(name) || len(name) > maxSkillNameLen {
			return nil, fmt.Errorf("agentdef: skill %q: invalid skill name", name)
		}
		dir := path.Join(layerRoot, skillsDir, name)
		data, err := fs.ReadFile(fsys, path.Join(dir, "SKILL.md"))
		if err != nil {
			return nil, fmt.Errorf("agentdef: skill %q: %w", name, err)
		}
		fm, _, err := splitFrontmatter(data)
		if err != nil {
			return nil, fmt.Errorf("agentdef: skill %q: SKILL.md: %w", name, err)
		}
		var meta struct {
			Name        string `yaml:"name"`
			Description string `yaml:"description"`
		}
		if err = yaml.Unmarshal(fm, &meta); err != nil {
			return nil, fmt.Errorf("agentdef: skill %q: SKILL.md: invalid frontmatter: %w", name, err)
		}
		switch {
		case meta.Name == "":
			return nil, fmt.Errorf("agentdef: skill %q: SKILL.md has no name", name)
		case meta.Name != name:
			return nil, fmt.Errorf("agentdef: skill %q: SKILL.md name %q does not match its directory", name, meta.Name)
		case strings.TrimSpace(meta.Description) == "":
			return nil, fmt.Errorf("agentdef: skill %q: SKILL.md has no description", name)
		}
		hash, err := hashTree(fsys, dir)
		if err != nil {
			return nil, fmt.Errorf("agentdef: skill %q: %w", name, err)
		}
		refs = append(refs, SkillRef{
			Name:        name,
			Description: strings.TrimSpace(meta.Description),
			Path:        relTo(layerRoot, dir),
			Hash:        hash,
			src:         fsys,
			dir:         dir,
		})
	}
	return refs, nil
}

// hashTree hashes every file under dir: sorted relative paths, each framed by
// its path and length so no two distinct trees share a byte stream.
func hashTree(fsys fs.FS, dir string) (string, error) {
	var files []string
	err := fs.WalkDir(fsys, dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() && e.Name() != ".DS_Store" {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	h := sha256.New()
	for _, f := range files {
		data, err := fs.ReadFile(fsys, f)
		if err != nil {
			return "", err
		}
		rel := strings.TrimPrefix(f, dir+"/")
		h.Write([]byte(rel))
		h.Write([]byte{0})
		h.Write([]byte(strconv.Itoa(len(data))))
		h.Write([]byte{0})
		h.Write(data)
		h.Write([]byte{0})
	}
	return DigestPrefix + hex.EncodeToString(h.Sum(nil)), nil
}

// CopySkills vendors resolved skill trees into dst/<name>/ — an authoring-time
// operation, never a runtime one. refs must come from ResolveSkills. The
// source tree is re-hashed before copying and the copy is re-hashed after; a
// mismatch with the pinned Hash is an error. It refuses to overwrite an
// existing dst/<name>.
func CopySkills(dst string, refs []SkillRef) error {
	for _, r := range refs {
		if r.src == nil {
			return fmt.Errorf("agentdef: skill %q: ref was not produced by ResolveSkills", r.Name)
		}
		if !namePattern.MatchString(r.Name) {
			return fmt.Errorf("agentdef: skill %q: invalid skill name", r.Name)
		}
		if got, err := hashTree(r.src, r.dir); err != nil {
			return fmt.Errorf("agentdef: skill %q: %w", r.Name, err)
		} else if got != r.Hash {
			return fmt.Errorf("agentdef: skill %q: source changed since it was pinned (%s != %s)", r.Name, got, r.Hash)
		}
		target := filepath.Join(dst, r.Name)
		if _, err := os.Stat(target); err == nil {
			return fmt.Errorf("agentdef: skill %q: %s already exists", r.Name, target)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("agentdef: skill %q: %w", r.Name, err)
		}
		if err := copyTree(r.src, r.dir, target); err != nil {
			return fmt.Errorf("agentdef: skill %q: %w", r.Name, err)
		}
		if got, err := hashTree(os.DirFS(target), "."); err != nil {
			return fmt.Errorf("agentdef: skill %q: %w", r.Name, err)
		} else if got != r.Hash {
			return fmt.Errorf("agentdef: skill %q: copy does not match pinned hash (%s != %s)", r.Name, got, r.Hash)
		}
	}
	return nil
}

func copyTree(src fs.FS, dir, target string) error {
	return fs.WalkDir(src, dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		out := filepath.Join(target, filepath.FromSlash(strings.TrimPrefix(strings.TrimPrefix(p, dir), "/")))
		if e.IsDir() {
			return os.MkdirAll(out, 0o750)
		}
		if e.Name() == ".DS_Store" {
			return nil
		}
		data, err := fs.ReadFile(src, p)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if info, err := e.Info(); err == nil && info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		return os.WriteFile(out, bytes.Clone(data), mode) //nolint:gosec // vendored skill content is meant to be world-readable
	})
}
