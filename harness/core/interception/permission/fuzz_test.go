package permission

import "testing"

func FuzzMatchPathGlob(f *testing.F) {
	for _, s := range [][2]string{
		{"/work/proj/**", "/work/proj/a"},
		{"/work/proj/**", "/work/proj-secret/x"},
		{"**/*.env", "/a/.env"},
		{"[", "["},
		{"", ""},
		{"/**", "/"},
		{"**/", "x"},
		{"a[b-", "ab"},
		{"\\", "\\"},
	} {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, pattern, path string) {
		_ = matchPathGlob(pattern, path)
		_ = matchGlob(pattern, path)
		// Go through the public evaluation path as well: it must not panic.
		rs := RuleSet{Rules: []Rule{{Tool: "*", Pattern: pattern, Behavior: DecisionAllow}}}
		_ = rs.Evaluate("t", map[string]any{"path": path, "command": path})
		_ = rs.Validate()
	})
}
