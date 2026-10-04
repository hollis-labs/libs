package goldens

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace"
	"github.com/hollis-labs/substrate/harness/workspace/materialize"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
)

// RoutingDeltas identifies reviewed changes to historical apply semantics.
// Archived files remain untouched; every adjustment below is explicit and
// logged, while payload bytes, file modes and bindings remain compared.
type RoutingDeltas struct {
	PrivateRoot             bool
	DeclaredDirectory       bool
	CommittedGeneration     bool
	DesiredProvenance       bool
	NewManifest             bool
	CollisionBeforeMutation bool
	EngineDirectories       bool
	DeclaredModes           map[string]string
}

func archiveTree(t *testing.T, dir string) []Entry {
	t.Helper()
	var entries []Entry
	b, err := os.ReadFile(filepath.Join(dir, "expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &entries); err != nil {
		t.Fatal(err)
	}
	return entries
}

func CheckRouting(t *testing.T, dir, root string, ev Evidence, normalize func([]byte) []byte, delta RoutingDeltas) {
	t.Helper()
	want := archiveTree(t, dir)
	got, err := Snapshot(root, normalize)
	if err != nil {
		t.Fatal(err)
	}
	next := []Entry{}
	for _, e := range want {
		if e.Path == "." && delta.PrivateRoot && (e.Mode == "0755" || e.Mode == "0750") {
			t.Log("delta private-root: archived root mode -> explicit 0700")
			e.Mode = "0700"
		}
		if e.Path == "empty" && e.Kind == "directory" && e.Mode == "0750" && delta.DeclaredDirectory {
			t.Log("delta declared-directory: active request explicitly declares 0755; archived 0750 separately refuses")
			e.Mode = "0755"
		}
		if (e.Path == "context" || e.Path == "context/task_context.txt") && delta.CollisionBeforeMutation {
			t.Log("delta collision-preflight: no directory mutation before refusal")
			continue
		}
		if delta.EngineDirectories && e.Path != "." && e.Kind == "directory" && e.Mode == "0750" {
			t.Log("delta sole-engine parent directories: archived0750 -> engine0755")
			e.Mode = "0755"
		}
		if mode, ok := delta.DeclaredModes[e.Path]; ok && e.Kind == "file" {
			t.Logf("delta declared file mode: %s archived%s -> declared%s", e.Path, e.Mode, mode)
			e.Mode = mode
		}
		if e.Path == ".materialize/manifest.json" {
			var old, current map[string]any
			if err := json.Unmarshal([]byte(e.Content), &old); err != nil {
				t.Fatal(err)
			}
			for _, a := range got {
				if a.Path == e.Path {
					if err := json.Unmarshal([]byte(a.Content), &current); err != nil {
						t.Fatal(err)
					}
				}
			}
			if current == nil {
				t.Fatal("active routing has no committed manifest")
			}
			adjustRecords(t, old, current, delta)
			if !reflect.DeepEqual(old, current) {
				t.Errorf("committed manifest differs beyond named deltas: old=%v active=%v", old, current)
			}
			// The full manifest was compared structurally; serialization layout is an
			// engine detail, while archived serialization remains historical evidence.
			for _, a := range got {
				if a.Path == e.Path {
					e.Content = a.Content
				}
			}
		}
		next = append(next, e)
	}
	if delta.NewManifest {
		m, err := materialize.LoadManifest(root)
		if err != nil {
			t.Fatal(err)
		}
		if err := workspace.ValidateManagedManifest(m, nil); err != nil {
			t.Fatal(err)
		}
		t.Log("delta sole-engine: context writer now has a real committed manifest")
		for _, e := range got {
			if e.Path == ".materialize" || e.Path == ".materialize/manifest.json" {
				if e.Path == ".materialize" && (e.Kind != "directory" || e.Mode != "0750") {
					t.Fatal("engine control directory mode changed")
				}
				if e.Path == ".materialize/manifest.json" && (e.Kind != "file" || e.Mode != "0600") {
					t.Fatal("committed manifest mode changed")
				}
				next = append(next, e)
			}
		}
	}
	// Sorting does not discard any paths or bytes.
	sortEntries(next)
	if !reflect.DeepEqual(next, got) {
		t.Errorf("active tree differs beyond named deltas: archived=%+v active=%+v", next, got)
	}
	var old, current map[string]any
	b, err := os.ReadFile(filepath.Join(dir, "evidence.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &old); err != nil {
		t.Fatal(err)
	}
	if ev.Diagnostics == nil {
		ev.Diagnostics = []string{}
	}
	b, err = json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if normalize != nil {
		b = normalize(b)
	}
	if err := json.Unmarshal(b, &current); err != nil {
		t.Fatal(err)
	}
	adjustRecords(t, old, current, delta)
	if !reflect.DeepEqual(old, current) {
		t.Errorf("routing evidence differs beyond named deltas: archived=%v active=%v", old, current)
	}
}

func adjustRecords(t *testing.T, old, current any, delta RoutingDeltas) {
	t.Helper()
	switch a := old.(type) {
	case map[string]any:
		b, ok := current.(map[string]any)
		if !ok {
			return
		}
		if delta.CommittedGeneration && a["generation"] == "" {
			generation, ok := b["generation"].(string)
			digest, err := hex.DecodeString(generation)
			if !ok || err != nil || len(digest) != 32 {
				t.Fatal("active generation is not a frozen input digest")
			}
			t.Log("delta committed-generation: empty legacy generation -> frozen v2 input digest")
			a["generation"] = generation
		}
		if delta.DesiredProvenance {
			if provenance, ok := a["provenance"].(map[string]any); ok && len(provenance) == 0 {
				if active, ok := b["provenance"].(map[string]any); ok && len(active) == 1 && active["source"] == "agentlaunch.artifacts" {
					t.Log("delta desired-provenance: literal desired entry names its adapter")
					a["provenance"] = map[string]any{"source": "agentlaunch.artifacts"}
				}
			}
		}
		if delta.DeclaredDirectory && a["path"] == "empty" && a["kind"] == "directory" && a["mode"] == float64(0750) {
			a["mode"] = float64(0755)
		}
		for k, v := range a {
			adjustRecords(t, v, b[k], delta)
		}
	case []any:
		b, ok := current.([]any)
		if !ok || len(a) != len(b) {
			return
		}
		for i, v := range a {
			adjustRecords(t, v, b[i], delta)
		}
	}
}

// CheckProjectionArchive compares pure provider bytes/modes and spawn bindings
// even when the archived credential placeholder cannot enter active apply.
func CheckProjectionArchive(t *testing.T, dir string, tree artifact.Tree, ev Evidence, normalize func([]byte) []byte) {
	t.Helper()
	entries := archiveTree(t, dir)
	want := map[string]Entry{}
	for _, e := range entries {
		if e.Kind == "file" && !strings.HasPrefix(e.Path, ".materialize/") && e.Path != "operator.txt" {
			want[e.Path] = e
		}
	}
	for _, e := range tree.Entries {
		old, ok := want[e.Path]
		if !ok || e.Kind != artifact.EntryFile {
			t.Fatalf("unexpected projected entry: %+v", e)
		}
		content := e.Bytes
		if normalize != nil {
			content = normalize(content)
		}
		if old.Content != string(content) || old.Mode != modeString(e.Mode) {
			t.Errorf("historical provider bytes/mode changed at %s", e.Path)
		}
		delete(want, e.Path)
	}
	if len(want) != 0 {
		t.Errorf("historical provider artifacts omitted: %v", want)
	}
	var archived struct {
		Bindings any `json:"bindings"`
	}
	b, err := os.ReadFile(filepath.Join(dir, "evidence.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &archived); err != nil {
		t.Fatal(err)
	}
	b, err = json.Marshal(ev.Bindings)
	if err != nil {
		t.Fatal(err)
	}
	if normalize != nil {
		b = normalize(b)
	}
	var active any
	if err := json.Unmarshal(b, &active); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(archived.Bindings, active) {
		t.Errorf("historical projection bindings changed: old=%v active=%v", archived.Bindings, active)
	}
}

func sortEntries(e []Entry) {
	slices.SortFunc(e, func(a, b Entry) int { return strings.Compare(a.Path, b.Path) })
}
func modeString(mode fs.FileMode) string {
	if mode == 0 {
		mode = 0644
	}
	return fmt.Sprintf("%04o", mode.Perm())
}
