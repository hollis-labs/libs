package agentsessions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hollis-labs/substrate/harness/adapters/provider"
	"github.com/hollis-labs/substrate/harness/workspace"
)

func TestAutoPlantHoldRefusesBeforeRenderFilesystemOrCallback(t *testing.T) {
	for _, exists := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing parent", true: "retained existing parent"}[exists], func(t *testing.T) {
			parent := filepath.Join(t.TempDir(), "boots")
			if exists {
				if err := os.Mkdir(parent, 0750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(parent, "operator.txt"), []byte("retain"), 0640); err != nil {
					t.Fatal(err)
				}
			}
			rendered, notified := false, false
			adapter := &fakeBootDirAdapter{name: "fixture", spec: provider.BootDirSpec{PlantedFiles: []provider.PlantedFile{{RelPath: "AGENTS.md", Render: func(provider.PlantContext) (string, error) { rendered = true; return "must not render", nil }}}}}
			opts := StartOptions{AutoPlantBootDir: true, BootDirRoot: parent, Workdir: "/fixture/project", OnBootDirPlanted: func(string) { notified = true }}
			root, planted, session, err := preparePlant(context.Background(), opts, adapter, "fixture")
			var refusal *workspace.Refusal
			if !errors.As(err, &refusal) || refusal.Status != workspace.Unsupported || root != "" || rendered || notified || session != adapter || planted.Workdir != opts.Workdir || !reflect.DeepEqual(planted.Env, opts.Env) {
				t.Fatalf("auto-plant crossed hold: root=%s error=%v render=%v callback=%v", root, err, rendered, notified)
			}
			if !exists {
				if _, err := os.Stat(parent); !os.IsNotExist(err) {
					t.Fatalf("parent created: %v", err)
				}
			} else {
				info, err := os.Stat(parent)
				if err != nil || info.Mode().Perm() != 0750 {
					t.Fatalf("parent changed: %v %v", info, err)
				}
				b, err := os.ReadFile(filepath.Join(parent, "operator.txt"))
				entries, _ := os.ReadDir(parent)
				if err != nil || string(b) != "retain" || len(entries) != 1 {
					t.Fatalf("existing root was cleaned or mutated: %v %v", entries, err)
				}
			}
		})
	}
}
