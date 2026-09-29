package agentdef

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func skillFS() fstest.MapFS {
	return fstest.MapFS{
		"skills/runbook/SKILL.md":            {Data: []byte("---\nname: runbook\ndescription: How to run incidents.\nlicense: MIT\n---\n# Runbook\n")},
		"skills/runbook/scripts/foo.sh":      {Data: []byte("#!/bin/sh\necho hi\n"), Mode: 0o755},
		"skills/runbook/references/notes.md": {Data: []byte("notes")},
	}
}

func TestResolveSkills(t *testing.T) {
	d := &Definition{Name: "a", Description: "d", Skills: []string{"runbook"}}
	refs, err := ResolveSkills(skillFS(), ".", d)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].Name != "runbook" || refs[0].Description != "How to run incidents." || refs[0].Path != "skills/runbook" || !strings.HasPrefix(refs[0].Hash, "sha256:") {
		t.Fatalf("refs = %+v", refs)
	}
	again, _ := ResolveSkills(skillFS(), ".", d)
	if again[0].Hash != refs[0].Hash {
		t.Error("identical tree hashed twice gave different hashes")
	}
}

func TestResolveSkills_LayerRootAndPath(t *testing.T) {
	fsys := fstest.MapFS{}
	for k, v := range skillFS() {
		fsys["layer/"+k] = v
	}
	refs, err := ResolveSkills(fsys, "layer", &Definition{Skills: []string{"runbook"}})
	if err != nil || refs[0].Path != "skills/runbook" {
		t.Fatalf("refs=%+v err=%v", refs, err)
	}
}

func TestResolveSkills_WholeTreeIsPinned(t *testing.T) {
	d := &Definition{Skills: []string{"runbook"}}
	base, _ := ResolveSkills(skillFS(), ".", d)

	changed := skillFS()
	changed["skills/runbook/scripts/foo.sh"] = &fstest.MapFile{Data: []byte("#!/bin/sh\necho  hi\n")}
	got, _ := ResolveSkills(changed, ".", d)
	if got[0].Hash == base[0].Hash {
		t.Error("whitespace change in scripts/foo.sh must change the hash")
	}

	added := skillFS()
	added["skills/runbook/extra.txt"] = &fstest.MapFile{Data: []byte("x")}
	got, _ = ResolveSkills(added, ".", d)
	if got[0].Hash == base[0].Hash {
		t.Error("an added file must change the hash")
	}
}

func TestResolveSkills_Errors(t *testing.T) {
	d := &Definition{Skills: []string{"runbook"}}
	cases := map[string]func(fstest.MapFS){
		"missing SKILL.md": func(m fstest.MapFS) { delete(m, "skills/runbook/SKILL.md") },
		"missing name": func(m fstest.MapFS) {
			m["skills/runbook/SKILL.md"] = &fstest.MapFile{Data: []byte("---\ndescription: d\n---\n")}
		},
		"missing description": func(m fstest.MapFS) {
			m["skills/runbook/SKILL.md"] = &fstest.MapFile{Data: []byte("---\nname: runbook\n---\n")}
		},
		"name mismatch": func(m fstest.MapFS) {
			m["skills/runbook/SKILL.md"] = &fstest.MapFile{Data: []byte("---\nname: other\ndescription: d\n---\n")}
		},
		"no frontmatter": func(m fstest.MapFS) { m["skills/runbook/SKILL.md"] = &fstest.MapFile{Data: []byte("# just markdown")} },
	}
	for name, mutate := range cases {
		m := skillFS()
		mutate(m)
		if _, err := ResolveSkills(m, ".", d); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if _, err := ResolveSkills(skillFS(), ".", &Definition{Skills: []string{"../etc"}}); err == nil {
		t.Error("path-like skill name must be rejected")
	}
	if _, err := ResolveSkills(skillFS(), ".", &Definition{Skills: []string{"absent"}}); err == nil {
		t.Error("dangling reference must error")
	}
}

func TestCopySkills(t *testing.T) {
	refs, err := ResolveSkills(skillFS(), ".", &Definition{Skills: []string{"runbook"}})
	if err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if err = CopySkills(dst, refs); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dst, "runbook", "scripts", "foo.sh")) //nolint:gosec // test reads a file it just wrote under t.TempDir
	if err != nil || !strings.Contains(string(b), "echo hi") {
		t.Fatalf("copy missing: %v", err)
	}
	info, _ := os.Stat(filepath.Join(dst, "runbook", "scripts", "foo.sh"))
	if info.Mode()&0o100 == 0 {
		t.Error("executable bit lost")
	}
	if err := CopySkills(dst, refs); err == nil {
		t.Error("second copy over an existing tree must be refused")
	}
	if err := CopySkills(t.TempDir(), []SkillRef{{Name: "runbook"}}); err == nil {
		t.Error("hand-built ref must be refused")
	}
}

func TestCopySkills_SourceDrift(t *testing.T) {
	m := skillFS()
	refs, _ := ResolveSkills(m, ".", &Definition{Skills: []string{"runbook"}})
	m["skills/runbook/scripts/foo.sh"] = &fstest.MapFile{Data: []byte("changed")}
	if err := CopySkills(t.TempDir(), refs); err == nil {
		t.Error("source changed after pinning must be refused")
	}
}

// writeSkillOnDisk lays a valid skill under root/skills/runbook and returns an
// os.DirFS over root, where a symlink is followable — unlike fstest.MapFS.
func writeSkillOnDisk(t *testing.T) (root string, fsys fs.FS) {
	t.Helper()
	root = t.TempDir()
	dir := filepath.Join(root, "skills", "runbook")
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: runbook\ndescription: How to run incidents.\n---\n# Runbook\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, os.DirFS(root)
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
}

// A symlink inside a skill must never be followed: its target would be hashed
// into the pin and vendored, so a hash-pinned skill could silently include a
// file from outside the tree.
func TestSkillSymlinksAreRefused(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("not part of the skill"), 0o600); err != nil {
		t.Fatal(err)
	}
	def := &Definition{Skills: []string{"runbook"}}

	t.Run("symlinked file", func(t *testing.T) {
		root, fsys := writeSkillOnDisk(t)
		symlinkOrSkip(t, secret, filepath.Join(root, "skills", "runbook", "scripts", "leak.sh"))

		if _, err := hashTree(fsys, "skills/runbook"); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("hashTree = %v, want a refusal of the symlink", err)
		}
		if _, err := ResolveSkills(fsys, ".", def); err == nil {
			t.Error("ResolveSkills must fail on a symlinked skill file")
		}
		if err := copyTree(fsys, "skills/runbook", t.TempDir()); err == nil {
			t.Error("copyTree must fail on a symlinked skill file")
		}
	})

	t.Run("symlinked directory", func(t *testing.T) {
		root, fsys := writeSkillOnDisk(t)
		symlinkOrSkip(t, filepath.Dir(secret), filepath.Join(root, "skills", "runbook", "linked"))
		if _, err := hashTree(fsys, "skills/runbook"); err == nil {
			t.Error("hashTree must refuse a symlinked directory")
		}
		if _, err := ResolveSkills(fsys, ".", def); err == nil {
			t.Error("ResolveSkills must fail on a symlinked directory")
		}
	})

	t.Run("symlinked SKILL.md", func(t *testing.T) {
		root, fsys := writeSkillOnDisk(t)
		md := filepath.Join(root, "skills", "runbook", "SKILL.md")
		if err := os.Remove(md); err != nil {
			t.Fatal(err)
		}
		symlinkOrSkip(t, secret, md)
		if _, err := ResolveSkills(fsys, ".", def); err == nil {
			t.Error("ResolveSkills must fail on a symlinked SKILL.md")
		}
	})

	t.Run("swapped in after pinning", func(t *testing.T) {
		root, fsys := writeSkillOnDisk(t)
		refs, err := ResolveSkills(fsys, ".", def)
		if err != nil {
			t.Fatal(err)
		}
		symlinkOrSkip(t, secret, filepath.Join(root, "skills", "runbook", "scripts", "leak.sh"))
		dst := t.TempDir()
		if err := CopySkills(dst, refs); err == nil {
			t.Error("CopySkills must fail when a symlink appears after pinning")
		}
		if _, statErr := os.Stat(filepath.Join(dst, "runbook")); statErr == nil {
			t.Error("nothing may be vendored when the source tree is refused")
		}
	})
}
