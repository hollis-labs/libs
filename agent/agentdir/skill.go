package agentdir

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// SkillRef locates an authored skill and exposes its listing metadata.
type SkillRef struct {
	Name, Description, Dir, File string
	// Body holds the instructions read alongside the listing metadata.
	Body string
}

// ListSkills reads frontmatter from direct child skill directories.
func ListSkills(dir string) ([]SkillRef, error) {
	return listSkills(dir, func(path string) ([]byte, error) { return readFile(path, true) })
}

func listSkills(dir string, read func(string) ([]byte, error)) ([]SkillRef, error) {
	exists, err := realDir(dir, true)
	if err != nil || !exists {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var refs []SkillRef
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("agentdir: skill path %s must not be a symlink", filepath.Join(dir, entry.Name()))
		}
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		file := filepath.Join(path, "SKILL.md")
		raw, err := read(file)
		if err != nil {
			return nil, err
		}
		if raw == nil {
			continue
		}
		meta, body, err := parseSkill(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		name, _ := meta["name"].(string)
		if strings.TrimSpace(name) == "" {
			name = entry.Name()
		}
		desc, _ := meta["description"].(string)
		refs = append(refs, SkillRef{Name: name, Description: desc, Dir: path, File: file, Body: body})
	}
	return refs, nil
}

// ReadSkill reads a skill's frontmatter and body from disk.
func ReadSkill(ref SkillRef) (map[string]any, string, error) {
	if _, err := realDir(filepath.Dir(ref.File), false); err != nil {
		return nil, "", err
	}
	raw, err := readFile(ref.File, false)
	if err != nil {
		return nil, "", err
	}
	return parseSkill(raw)
}

func parseSkill(raw []byte) (map[string]any, string, error) {
	text := string(raw)
	first, rest, ok := strings.Cut(text, "\n")
	if !ok || strings.TrimSuffix(first, "\r") != "---" {
		return nil, text, nil
	}
	remaining := rest
	for remaining != "" {
		line, next, _ := strings.Cut(remaining, "\n")
		if strings.TrimSuffix(line, "\r") == "---" {
			var meta map[string]any
			if err := yaml.Unmarshal([]byte(rest[:len(rest)-len(remaining)]), &meta); err != nil {
				return nil, "", err
			}
			return meta, next, nil
		}
		remaining = next
	}
	return nil, "", fmt.Errorf("unterminated skill frontmatter")
}
