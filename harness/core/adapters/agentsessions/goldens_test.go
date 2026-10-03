package agentsessions

import (
	"github.com/hollis-labs/substrate/harness/adapters/provider"
	"github.com/hollis-labs/substrate/harness/workspace/goldens"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
	"path/filepath"
	"testing"
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
			root, planted, sa, err := preparePlant(opts, a, "fixture-session")
			ev := goldens.Evidence{Writer: "agentsessions", Source: "legacy preparePlant (per-file Mode ignored)"}
			if err != nil {
				ev.Diagnostics = append(ev.Diagnostics, err.Error())
				root = filepath.Join(scratch, "empty")
			} else {
				ev.Bindings = struct {
					CWD                            string
					Env, ExtraArgs, First, Resumed []string
				}{planted.Workdir, planted.Env, planted.ExtraArgs, sa.BuildArgs("Fixture turn.", "Fixture instructions.", ""), sa.BuildArgs("Fixture next turn.", "Fixture instructions.", "fixture-session")}
			}
			goldens.Check(t, dir, root, ev, goldens.Roots(root, "<boot>", scratch, "<scratch>"))
		})
	}
}
