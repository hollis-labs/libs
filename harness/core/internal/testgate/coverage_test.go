package testgate

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// scope lists, relative to the module root, the directories this guard covers:
// the packages imported from go-agent-wrapper, where the guard started (it
// walked that repository's root). A trailing slash walks the directory and
// everything below it; without one only the directory's own files are read,
// because adapters and interception/filters also hold packages of other
// imported modules in their subdirectories, and other modules' tests do not
// use this gate.
var scope = []string{
	"adapters",
	"adapters/acp/", "adapters/activity/", "adapters/launch/", "adapters/turnoutput/", "adapters/wrapper/",
	"adapters/claude/", "adapters/claudeacp/", "adapters/codex/", "adapters/codexacp/",
	"adapters/copilotacp/", "adapters/opencode/", "adapters/opencodeacp/", "adapters/piacp/",
	"interception/classifybridge/", "interception/policy/", "interception/filters",
	"agentlaunch/planting/", "sandbox/wrapper/", "sandbox/snapshot/",
	"internal/childoutput/", "internal/closegate/", "internal/sidebyside/", "internal/testgate/",
}

// inScope reports whether a directory (slash-separated, relative to the module root) is covered.
func inScope(dir string) bool {
	for _, s := range scope {
		if strings.HasSuffix(s, "/") {
			if dir == strings.TrimSuffix(s, "/") || strings.HasPrefix(dir, s) {
				return true
			}
		} else if dir == s {
			return true
		}
	}
	return false
}

func TestEveryInstalledProviderTestUsesTheSharedGate(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not return this test's source path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	// A scope entry that names no directory would silently shrink the guard.
	for _, s := range scope {
		if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(strings.TrimSuffix(s, "/")))); err != nil || !info.IsDir() {
			t.Fatalf("scope entry %q is not a directory under the module root (%v)", s, err)
		}
	}
	lookups := []string{
		`exec.LookPath("claude")`,
		`exec.LookPath("codex")`,
		`exec.LookPath("copilot")`,
		`exec.LookPath("opencode")`,
		`exec.LookPath("pi")`,
		`exec.LookPath("npx")`,
		`exec.LookPath("agy")`,
		`exec.LookPath("pi-acp")`,
		// A go-providers registry descriptor resolving its runtime's
		// installed binary (registry.Descriptor.LookPath): a test that
		// finds real CLIs through the registry is a live test too.
		`.LookPath()`,
	}
	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || path == source || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if rel, err := filepath.Rel(root, filepath.Dir(path)); err != nil || !inScope(filepath.ToSlash(rel)) {
			return nil
		}
		contents, err := os.ReadFile(path) //nolint:gosec // G304: a test source file this test found by walking the repository
		if err != nil {
			return err
		}
		for _, lookup := range lookups {
			if strings.Contains(string(contents), lookup) {
				files = append(files, path)
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("discover installed-provider tests: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("installed-provider test discovery found no positive control")
	}
	sort.Strings(files)
	for _, path := range files {
		name, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatalf("make installed-provider test path relative: %v", err)
		}
		t.Run(name, func(t *testing.T) {
			contents, err := os.ReadFile(path) //nolint:gosec // G304: a test source file this test found by walking the repository
			if err != nil {
				t.Fatalf("read installed-provider test: %v", err)
			}
			if !strings.Contains(string(contents), "testgate.RequireLiveProvider(t)") {
				t.Fatalf("%s does not call the shared installed-provider opt-in gate", name)
			}
		})
	}
}
