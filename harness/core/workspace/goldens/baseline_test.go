package goldens_test

import (
	"context"
	"github.com/hollis-labs/substrate/harness/adapters/provider"
	"github.com/hollis-labs/substrate/harness/agentlaunch"
	"github.com/hollis-labs/substrate/harness/agentlaunch/launcher"
	"github.com/hollis-labs/substrate/harness/interception/permission"
	"github.com/hollis-labs/substrate/harness/workspace/bootdir"
	"github.com/hollis-labs/substrate/harness/workspace/goldens"
	"github.com/hollis-labs/substrate/harness/workspace/materialize"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
	"github.com/hollis-labs/substrate/harness/workspace/plant"
	"github.com/hollis-labs/substrate/harness/workspace/providerplant"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
	"os"
	"path/filepath"
	"testing"
)

func TestGoldenBaseline(t *testing.T) {
	for _, writer := range []string{"plant", "providerplant", "bootdir", "agentlaunch"} {
		dirs, err := goldens.Cases(filepath.Join("..", "testdata", "goldens", "baseline", writer))
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
				normalize := goldens.Roots(root, "<boot>", scratch, "<scratch>")
				ev := goldens.Evidence{Writer: writer, Source: "substrate legacy planting baseline"}
				switch writer {
				case "providerplant":
					ev, err = renderProvider(t, in, root)
				case "plant":
					spec := plant.Spec{Files: map[string][]byte{"AGENTS.md": []byte("Fixture instructions.\n")}, MCPConfig: []byte("{\"mcpServers\":{}}\n"), ProviderSettings: map[string][]byte{in.Provider: []byte("fixture settings\n")}, Hooks: []plant.Hook{{Provider: in.Provider, Name: "start.sh", Payload: []byte("#!/bin/sh\nexit 0\n")}}, RecoveryPrompt: "Fixture recovery.\n", Artifacts: fixtureArtifacts(), Generation: "fixture-v1", Operation: materialize.OperationCreate}
					p := plant.SharedPlanter{}
					var res plant.Result
					res, err = p.Plant(context.Background(), root, spec)
					if err == nil && in.Scenario == "refresh" {
						unrelated(t, root)
						spec.Operation = materialize.OperationRefresh
						spec.ExpectedGeneration = "fixture-v1"
						spec.Generation = "fixture-v2"
						spec.Files["AGENTS.md"] = []byte("Refreshed instructions.\n")
						res, err = p.Plant(context.Background(), root, spec)
					}
					if res.Handle != nil {
						ev.Ownership = res.Handle.Manifest.Entries
						ev.Bindings = res.Handle.Report
					}
				case "bootdir":
					var writes []string
					w := bootdir.Writer{}
					if in.Scenario == "atomic" {
						w.AtomicWrite = func(p string, b []byte, m os.FileMode) error {
							rel, _ := filepath.Rel(root, p)
							writes = append(writes, filepath.ToSlash(rel))
							return os.WriteFile(p, b, m)
						}
					}
					spec := agentlaunch.InjectionSpec{NativeFiles: []agentlaunch.NativeFile{{Kind: agentlaunch.NativeFileRaw, RelPath: "skills/sample/scripts/run.sh", Content: "#!/bin/sh\nexit 0\n", Mode: 0751}}, BootDirOverlay: map[string]string{"AGENTS.md": "Fixture instructions.\n", "notes/empty.txt": ""}}
					var res bootdir.WriteResult
					res, err = w.WriteInjectionSpec(root, spec, bootdir.WriteOptions{})
					if err == nil && in.Scenario == "refresh" {
						unrelated(t, root)
						spec.BootDirOverlay["AGENTS.md"] = "Refreshed instructions.\n"
						res, err = w.WriteInjectionSpec(root, spec, bootdir.WriteOptions{})
					}
					ev.Bindings = struct {
						Result       bootdir.WriteResult
						AtomicWrites []string
					}{res, writes}
				case "agentlaunch":
					if in.Scenario == "populate" || in.Scenario == "replant" {
						ev, err = renderBootSpec(t, in, root)
						break
					}
					req := agentlaunch.ArtifactMaterializationRequest{TargetRoot: root, Roots: agentlaunch.ExecutionRoots{BootRoot: root}, Artifacts: fixtureArtifacts(), Operation: materialize.OperationCreate, Generation: "fixture-v1"}
					var h *materialize.Handle
					h, err = agentlaunch.MaterializeArtifacts(context.Background(), req)
					if err == nil && in.Scenario == "refresh" {
						unrelated(t, root)
						req.Operation = materialize.OperationRefresh
						req.ExpectedGeneration = "fixture-v1"
						req.Generation = "fixture-v2"
						req.Artifacts.Entries[0].Bytes = []byte("Refreshed skill.\n")
						h, err = agentlaunch.MaterializeArtifacts(context.Background(), req)
					}
					if h != nil {
						ev.Ownership = h.Manifest.Entries
						ev.Bindings = h.Report
					}
				}
				if err != nil {
					ev.Diagnostics = append(ev.Diagnostics, err.Error())
				}
				if ev.Ownership == nil {
					if m, e := materialize.LoadManifest(root); e == nil {
						ev.Ownership = m.Entries
					}
				}
				goldens.Check(t, dir, root, ev, normalize)
			})
		}
	}
}
func fixtureArtifacts() artifact.Tree {
	return artifact.Tree{Entries: []artifact.Entry{
		{Path: "skills/sample/SKILL.md", Kind: artifact.EntryFile, Mode: 0640, Bytes: []byte("---\nname: sample\n---\nFixture skill.\n"), Ownership: artifact.Ownership{EntryID: "fixture:skill", GroupID: "fixture"}, Provenance: artifact.Provenance{Source: "fixture"}},
		{Path: "skills/sample/scripts/run.sh", Kind: artifact.EntryFile, Mode: 0751, Bytes: []byte("#!/bin/sh\nexit 0\n"), Ownership: artifact.Ownership{EntryID: "fixture:script", GroupID: "fixture"}},
		{Path: "skills/sample/data.bin", Kind: artifact.EntryFile, Mode: 0600, Bytes: []byte{0, 255, 1}, Ownership: artifact.Ownership{EntryID: "fixture:data", GroupID: "fixture"}},
		{Path: "empty", Kind: artifact.EntryDirectory, Mode: 0750, Ownership: artifact.Ownership{EntryID: "fixture:empty", GroupID: "fixture"}},
	}}
}
func unrelated(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "operator.txt"), []byte("Operator owned.\n"), 0640); err != nil {
		t.Fatal(err)
	}
}
func renderProvider(t *testing.T, in goldens.Input, root string) (goldens.Evidence, error) {
	t.Helper()
	ev := goldens.Evidence{Writer: "providerplant", Source: "substrate legacy provider projection"}
	mode := runtimes.Mode(in.Runtime)
	plan := agentlaunch.LaunchPlan{Project: agentlaunch.ProjectSpec{ID: "fixture-project", Root: "/fixture/project"}, Agent: agentlaunch.AgentSpec{ID: "fixture-agent", Name: "fixture"}, Provider: agentlaunch.ProviderSpec{ID: in.Provider, Permission: permission.Mode(in.Posture)}, Runtime: mode, Workspace: agentlaunch.WorkspaceSpec{Mode: agentlaunch.WorkspaceShared, WorkspaceDir: "/fixture/state", Workdir: "/fixture/project"}, BootProfile: agentlaunch.BootProfileRef{Inline: &agentlaunch.BootProfileInline{BootPrompt: "Fixture instructions.\n", BootContent: "Fixture kickoff.\n", BootMode: agentlaunch.BootModePlanted}}, Mode: agentlaunch.LaunchInteractive}
	compiled, err := launcher.Compile(context.Background(), plan)
	if err != nil {
		return ev, err
	}
	compiled.Provenance.PlanHash = "fixture-plan"
	prepared := &agentlaunch.PreparedLaunch{Compiled: compiled, PlantedBootDir: root, WorkspaceDir: "/fixture/state", Workdir: "/fixture/project", Argv: []string{in.Provider}, BootPrompt: "Fixture instructions.\n", BootContent: "Fixture kickoff.\n", PlantContext: agentlaunch.PreparedPlantContext{AgentName: "fixture", MCPLoopbackURL: "http://127.0.0.1:23456/mcp", SelfMCPCommand: "fixture-mcp", SelfMCPArgs: []string{"--stdio"}, SelfMCPEnv: map[string]string{"FIXTURE": "yes"}}}
	prepared.PlantContext.MCPServers = []agentlaunch.MCPServerSpec{{Name: "http", URL: "http://127.0.0.1:23457/mcp"}, {Name: "stdio", Command: "fixture-server", Args: []string{"one", "two"}, Env: map[string]string{"FIXTURE": "yes"}}}
	if in.Scenario == "duplicate-mcp" {
		prepared.PlantContext.MCPServers = append(prepared.PlantContext.MCPServers, prepared.PlantContext.MCPServers[0])
	}
	if in.Scenario == "omissions" {
		prepared.BootPrompt = ""
		prepared.BootContent = ""
		prepared.PlantContext = agentlaunch.PreparedPlantContext{AgentName: "fixture"}
	}
	skillPath, err := agentlaunch.SkillRelPath(in.Provider, mode, "sample")
	if err != nil {
		return ev, err
	}
	compiled.Plan.Injection.NativeFiles = []agentlaunch.NativeFile{{Kind: agentlaunch.NativeFileSkill, ID: "sample", Content: "Fixture skill.\n", Mode: 0640}, {Kind: agentlaunch.NativeFileRaw, RelPath: filepath.ToSlash(filepath.Join(filepath.Dir(skillPath), "scripts/run.sh")), Content: "#!/bin/sh\nexit 0\n", Mode: 0751}}
	a, err := provider.NewAdapter(runtimes.ID(in.Provider), mode)
	if err != nil {
		return ev, err
	}
	if c, ok := a.(*provider.ClaudeAdapter); ok && in.Variant == "bare" {
		c.Bare = true
	}
	ex, err := providerplant.PrepareExecution(context.Background(), prepared, providerplant.WithAdapter(a.(provider.BootDirProvider)))
	if err != nil {
		return ev, err
	}
	if in.Scenario == "credential-clobber" {
		// Dummy sentinel only: pin the legacy bug, never read ambient credentials.
		if err := os.WriteFile(filepath.Join(root, "auth.json"), []byte("DUMMY-CREDENTIAL-SENTINEL"), 0600); err != nil {
			t.Fatal(err)
		}
		ex, err = providerplant.PrepareExecution(context.Background(), prepared, providerplant.WithAdapter(a.(provider.BootDirProvider)))
		if err != nil {
			return ev, err
		}
		ev.Diagnostics = append(ev.Diagnostics, "KNOWN BUG: providerplant overwrites a filled auth.json with its empty placeholder on replant; input was a dummy sentinel. Credentials must be link-only. Corrected behavior belongs to S4.")
	}
	if in.Scenario == "refresh" {
		unrelated(t, root)
		prepared.BootPrompt = "Refreshed instructions.\n"
		ex, err = providerplant.PrepareExecution(context.Background(), prepared, providerplant.WithAdapter(a.(provider.BootDirProvider)))
		if err != nil {
			return ev, err
		}
	}
	var first, resumed []string
	if ex.Bindings.Launch != nil {
		first, err = ex.Bindings.Launch.TurnArgv(provider.TurnInput{Prompt: "Fixture turn.", SystemPrompt: "Fixture instructions."})
		if err != nil {
			return ev, err
		}
		resumed, err = ex.Bindings.Launch.TurnArgv(provider.TurnInput{Prompt: "Fixture next turn.", SystemPrompt: "Fixture instructions.", ResumeID: "fixture-session"})
		if err != nil {
			return ev, err
		}
	}
	ev.Bindings = struct {
		Bindings       agentlaunch.ExecutionBindings
		First, Resumed []string
	}{ex.Bindings, first, resumed}
	ev.Ownership = ex.Materialization.Manifest.Entries
	return ev, nil
}

func renderBootSpec(t *testing.T, in goldens.Input, root string) (goldens.Evidence, error) {
	ev := goldens.Evidence{Writer: "agentlaunch.DefaultMaterializer", Source: "legacy BootSpec Populate/Replant"}
	spec := &agentlaunch.BootSpec{Runtime: agentlaunch.RuntimeBinding{Provider: in.Provider, RuntimeKind: runtimes.ModeSubprocessPerTurn}, Files: []agentlaunch.BootFileSpec{{ID: "instructions", RelPath: "AGENTS.md", Mode: 0640, Object: agentlaunch.ContractObject{Kind: agentlaunch.ContractObjectLiteral, Text: "Fixture instructions.\n"}}}, Injections: []agentlaunch.BootInjectionSpec{{ID: "skill", Kind: agentlaunch.NativeFileSkill, Name: "sample", Mode: 0640, Object: agentlaunch.ContractObject{Kind: agentlaunch.ContractObjectLiteral, Text: "Fixture skill.\n"}}, {ID: "support", Kind: agentlaunch.NativeFileRaw, RelPath: "support/run.sh", Mode: 0751, Object: agentlaunch.ContractObject{Kind: agentlaunch.ContractObjectLiteral, Text: "#!/bin/sh\nexit 0\n"}}}}
	m := agentlaunch.NewDefaultMaterializer(agentlaunch.MaterializerOptions{})
	req := agentlaunch.MaterializeRequest{Spec: spec}
	res, err := m.Populate(context.Background(), root, req, nil)
	if err == nil && in.Scenario == "replant" {
		unrelated(t, root)
		spec.Files[0].Object.Text = "Refreshed instructions.\n"
		res, err = m.Replant(context.Background(), root, req, agentlaunch.ReplantSelector{FileIDs: []string{"instructions"}}, nil)
	}
	ev.Bindings = res
	return ev, err
}
