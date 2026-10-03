package contexthook

import (
	"github.com/hollis-labs/substrate/harness/agentcontext"
	"github.com/hollis-labs/substrate/harness/workspace/goldens"
	"os"
	"path/filepath"
	"testing"
)

func TestGoldenContextFiles(t *testing.T) {
	dirs, err := goldens.Cases(filepath.Join("..", "..", "workspace", "testdata", "goldens", "baseline", "contexthook"))
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range dirs {
		t.Run(dir, func(t *testing.T) {
			in, err := goldens.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			scratch := goldens.Sandbox(t)
			root := filepath.Join(scratch, "boot")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			slots := []agentcontext.SlotResult{{Name: "task-context", Content: "Fixture task.\n"}, {Name: "empty", Content: ""}}
			if in.Scenario == "collision" {
				slots = append(slots, agentcontext.SlotResult{Name: "task_context", Content: "Colliding slot.\n"})
			}
			err = plantArtifacts(root, slots)
			ev := goldens.Evidence{Writer: "contexthook", Source: "legacy context slot file writer", Bindings: slots}
			if err == nil && in.Scenario == "refresh" {
				if err := os.WriteFile(filepath.Join(root, "operator.txt"), []byte("Operator owned.\n"), 0640); err != nil {
					t.Fatal(err)
				}
				slots[0].Content = "Refreshed task.\n"
				err = plantArtifacts(root, slots)
				ev.Bindings = slots
			}
			if err != nil {
				ev.Diagnostics = append(ev.Diagnostics, err.Error())
			}
			goldens.Check(t, dir, root, ev, goldens.Roots(root, "<boot>", scratch, "<scratch>"))
		})
	}
}
