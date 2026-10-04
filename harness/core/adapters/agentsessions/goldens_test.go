package agentsessions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hollis-labs/substrate/harness/adapters/provider"
	"github.com/hollis-labs/substrate/harness/workspace/goldens"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
)

func TestGoldenSessionPlant(t *testing.T) {
	dirs, err := goldens.Cases(filepath.Join("..", "..", "workspace", "testdata", "goldens", "baseline", "agentsessions"))
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
			a, err := provider.NewAdapter(runtimes.ID(in.Provider), runtimes.Mode(in.Runtime))
			if err != nil {
				t.Fatal(err)
			}
			if c, ok := a.(*provider.ClaudeAdapter); ok && in.Variant == "bare" {
				c.Bare = true
			}
			pc := provider.PlantContext{AgentName: "fixture", MCPLoopbackURL: "http://127.0.0.1:23456/mcp", MCPServers: []provider.MCPServerSpec{{Name: "http", HTTPURL: "http://127.0.0.1:23457/mcp"}, {Name: "stdio", Command: "fixture-server", Args: []string{"one", "two"}}}}
			if in.Scenario == "duplicate-mcp" {
				pc.MCPServers = append(pc.MCPServers, pc.MCPServers[0])
			}
			opts := StartOptions{AutoPlantBootDir: true, BootDirRoot: filepath.Join(scratch, "boots"), Workdir: "/fixture/project", BootPrompt: "Fixture instructions.\n", BootContent: "Fixture kickoff.\n", PlantContext: pc}
			root := filepath.Join(scratch, "candidate")
			tree, planted, sa, err := historicalSessionProjection(opts, a, root)
			ev := goldens.Evidence{Writer: "agentsessions", Source: "legacy preparePlant (per-file Mode ignored)"}
			if err != nil {
				ev.Diagnostics = append(ev.Diagnostics, err.Error())
			} else {
				ev.Bindings = struct {
					CWD                            string
					Env, ExtraArgs, First, Resumed []string
				}{planted.Workdir, planted.Env, planted.ExtraArgs, sa.BuildArgs("Fixture turn.", "Fixture instructions.", ""), sa.BuildArgs("Fixture next turn.", "Fixture instructions.", "fixture-session")}
			}
			goldens.CheckHistoricalProjection(t, dir, tree, ev, goldens.Roots(root, "<boot>", scratch, "<scratch>"))
			// Active requests use a separately declared host candidate and authority.
			if err == nil {
				active := fixtureStartOptions(t, opts)
				active.ArtifactRoot = root
				_, _, _, applyErr := preparePlant(context.Background(), active, a, "fixture")
				if in.Provider == "codex" {
					if applyErr == nil {
						t.Fatal("credential placeholder entered active session planting")
					}
					if _, e := os.Stat(root); !os.IsNotExist(e) {
						t.Fatalf("reserved credential refusal mutated root: %v", e)
					}
				} else {
					if applyErr != nil {
						t.Fatal(applyErr)
					}
					modes := map[string]string{}
					for _, file := range a.(provider.BootDirProvider).BootDirSpec().PlantedFiles {
						if file.Mode != 0 {
							modes[file.RelPath] = fmt.Sprintf("%04o", file.Mode)
						}
					}
					goldens.CheckRouting(t, dir, root, ev, goldens.Roots(root, "<boot>", scratch, "<scratch>"), goldens.RoutingDeltas{NewManifest: true, EngineDirectories: true, DeclaredModes: modes})
				}
			}

		})
	}
}

func sessionGoldenRoot(root, parent string, err error) string {
	if err != nil {
		return parent
	}
	return root
}
func TestGoldenFailedPlantDetectsPartialLeftovers(t *testing.T) {
	scratch := goldens.Sandbox(t)
	parent := filepath.Join(scratch, "boots")
	a := &fakeBootDirAdapter{name: "fixture", spec: provider.BootDirSpec{PlantedFiles: []provider.PlantedFile{
		{RelPath: "AGENTS.md", Render: staticContent("partial")},
		{RelPath: "settings.json", Render: func(provider.PlantContext) (string, error) { return "", errors.New("fixture render failure") }},
	}}}
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	root, _, _, err := testPreparePlant(t, StartOptions{AutoPlantBootDir: true, BootDirRoot: parent}, a, "fixture")
	if err == nil {
		t.Fatal("expected fixture render failure")
	}
	observed := sessionGoldenRoot(root, parent, err)
	before, e := goldens.Snapshot(observed, nil)
	if e != nil {
		t.Fatal(e)
	}
	// Simulate a cleanup regression in the error path, after the real render
	// failure. The consumer snapshot must expose a partially planted directory.
	leftover := filepath.Join(parent, "partial-boot")
	if e := os.Mkdir(leftover, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(leftover, "AGENTS.md"), []byte("partial"), 0644); e != nil {
		t.Fatal(e)
	}
	after, e := goldens.Snapshot(observed, nil)
	if e != nil {
		t.Fatal(e)
	}
	if reflect.DeepEqual(before, after) {
		t.Error("failed-plant consumer did not observe partial leftovers")
	}
}
