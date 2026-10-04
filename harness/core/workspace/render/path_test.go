package render

import "testing"

func TestCanonicalRelativePaths(t *testing.T) {
	// artifact rejects unclean paths and backslashes more strictly than the
	// legacy injection validator; injection additionally reserves root names.
	// This union preserves every refusal before either caller is migrated.
	for _, rel := range []string{"", " ", "/abs", "../escape", "a/../../b", "a/../b", "a/./b", "./x", "x//y", "x/", ".", "..", "x\\y", ".materialize", ".materialize/manifest.json", ".git", ".git/config", ".ssh", ".ssh/id_rsa", ".gnupg/key", ".aws/config", "C:/x", "x\x00y", "x\ny", "{name}/SKILL.md"} {
		if err := ValidateRelPath(rel); err == nil {
			t.Errorf("accepted %q", rel)
		}
	}
	for _, rel := range []string{"CLAUDE.md", "a/b/c.md", ".claude/skills/x.md", "skills/tool/scripts/run.sh", "empty"} {
		if err := ValidateRelPath(rel); err != nil {
			t.Errorf("refused %q: %v", rel, err)
		}
	}
	for _, component := range []string{"", ".", "..", "a/b", "a\\b", "bad name", "{name}", "x\ny"} {
		if err := ValidateComponent(component); err == nil {
			t.Errorf("accepted component %q", component)
		}
	}
	for _, component := range []string{"a", "v1.2_review", "code-review"} {
		if err := ValidateComponent(component); err != nil {
			t.Errorf("refused component %q", component)
		}
	}
}
