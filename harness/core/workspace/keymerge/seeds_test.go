package keymerge_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace/keymerge"
)

// The archived installed-refresh seeds pin what the installer did to a
// document an operator also writes. Each scenario installs into an empty root
// (the create seed is the document a clean install writes, which is the desired
// document), adds one operator key, and installs again (the refresh seed is the
// result). The pre-existing documents are rebuilt here the way the capture
// script builds them, and the expected bytes come from the refresh seeds, never
// from this package.
//
// There is no seed for the third provider's installed documents: its installed
// layer did not exist when the seeds were captured, so its seed directories
// hold nothing to merge. Its JSON shapes are covered only by the neutral
// fixtures in the other tests, and no claim of archived parity is made for them.

const seedRoot = "../testdata/goldens/seeds/cairn"

type seedEntry struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// seedFile returns the content of one file of one archived seed.
func seedFile(t *testing.T, provider, scenario, path string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(seedRoot, provider, "install-"+scenario, "expected.json"))
	if err != nil {
		t.Fatalf("read the %s %s seed: %v", provider, scenario, err)
	}
	var entries []seedEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("decode the %s %s seed: %v", provider, scenario, err)
	}
	for _, e := range entries {
		if e.Path == path {
			return e.Content
		}
	}
	t.Fatalf("the %s %s seed has no %s", provider, scenario, path)
	return ""
}

func TestSeedClaudeSettingsRefresh(t *testing.T) {
	const path = ".claude/settings.json"
	desired := seedFile(t, "claude", "create", path)
	want := seedFile(t, "claude", "refresh", path)
	owned := []keymerge.KeyPath{p("permissions", "defaultMode")}

	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(desired)); err != nil {
		t.Fatal(err)
	}
	withOperator := strings.TrimSuffix(compact.String(), "}") + `,"fixture_operator":true}`
	for name, existing := range map[string]string{
		// How the capture script writes it: one line, spaces after ':' and ','.
		"capture-script layout": `{"permissions": {"defaultMode": "acceptEdits"}, "fixture_operator": true}` + "\n",
		"compact layout":        withOperator + "\n",
		"already at rest":       want,
	} {
		t.Run(name, func(t *testing.T) {
			got := mergeJSON(t, desired, existing, owned...)
			if got.Outcome != keymerge.OutcomeMerged || string(got.Document) != want {
				t.Fatalf("the merge gave %q (%s/%s), want the refresh seed\n\t%q", got.Document, got.Outcome, got.Reason, want)
			}
			// The operator key stands after the key the installer owns, in the
			// order found, and nothing is noted: every declared key is owned.
			if len(got.Notes) != 0 {
				t.Errorf("notes = %v, want none", notesOf(got))
			}
			// The installer's comparison of the document before the merge is
			// quiet about the operator's key, and of the one after it too.
			for _, doc := range []string{existing, want} {
				cmp, err := keymerge.CompareJSON([]byte(desired), []byte(doc), owned)
				if err != nil || !cmp.Match {
					t.Errorf("CompareJSON(%q) = %+v, %v, want a match", doc, cmp, err)
				}
			}
		})
	}
	// A document that already holds the render is returned as it is.
	got := mergeJSON(t, desired, desired, owned...)
	if string(got.Document) != desired {
		t.Errorf("merging the render into itself gave %q, want %q", got.Document, desired)
	}
	// The same operator key placed before the owned one keeps its place.
	got = mergeJSON(t, desired, `{"fixture_operator": true, "permissions": {"defaultMode": "plan"}}`, owned...)
	if wantFirst := "{\n  \"fixture_operator\": true,\n  \"permissions\": {\n    \"defaultMode\": \"acceptEdits\"\n  }\n}\n"; string(got.Document) != wantFirst {
		t.Errorf("gave %q, want %q", got.Document, wantFirst)
	}
}

func TestSeedCodexConfigRefresh(t *testing.T) {
	const path = ".codex/config.toml"
	desired := seedFile(t, "codex", "create", path)
	want := seedFile(t, "codex", "refresh", path)
	owned := []keymerge.KeyPath{
		p("approval_policy"),
		p("mcp_servers", "fixture", "args"),
		p("mcp_servers", "fixture", "command"),
	}
	// The capture script appends the key after the last table, so it lands
	// inside the server's table.
	existing := desired + "\nfixture_operator = true\n"

	got := mergeTOML(t, desired, existing, owned...)
	if string(got.Document) != want {
		t.Fatalf("the merge gave\n\t%q\nwant the refresh seed\n\t%q", got.Document, want)
	}
	if len(got.Notes) != 0 {
		t.Errorf("notes = %v, want none", tomlNotes(got))
	}
	// The operator's key inside the server table was neither dropped nor moved
	// out of it, and the installer's own keys are still the declared ones.
	for _, doc := range []string{existing, want} {
		cmp, err := keymerge.CompareTOML([]byte(desired), []byte(doc), owned)
		if err != nil || !cmp.Match {
			t.Errorf("CompareTOML(%q) = %+v, %v, want a match", doc, cmp, err)
		}
	}
	// An operator edit of an owned key is a finding; of the operator's own key it is not.
	edited := strings.Replace(existing, "command = 'fixture-server'", "command = 'other'", 1)
	if cmp, _ := keymerge.CompareTOML([]byte(desired), []byte(edited), owned); cmp.Match {
		t.Error("an edited owned key matched")
	}
	moved := strings.Replace(existing, "fixture_operator = true", "fixture_operator = false", 1)
	if cmp, _ := keymerge.CompareTOML([]byte(desired), []byte(moved), owned); !cmp.Match {
		t.Error("an edited operator key did not match")
	}
	// The document a clean install writes merges to itself.
	if again := mergeTOML(t, desired, desired, owned...); string(again.Document) != desired {
		t.Errorf("merging the render into itself gave %q, want %q", again.Document, desired)
	}
}
