package goldens_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	layout "github.com/hollis-labs/substrate/harness/adapters/layout/plan"
	contract "github.com/hollis-labs/substrate/harness/adapters/nativefiles"
	"github.com/hollis-labs/substrate/harness/interception/permission"
	"github.com/hollis-labs/substrate/harness/workspace/goldens"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
	"github.com/hollis-labs/substrate/harness/workspace/render"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
)

// These snapshots write only test scratch. Production render returns a tree;
// materialize owns apply, manifests and refresh, which remain baseline coverage.
func TestGoldenIntended(t *testing.T) {
	dirs, err := goldens.Cases(filepath.Join("..", "testdata", "goldens", "intended"))
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
			req, err := intendedRequest(in, root)
			var result render.Result
			if err == nil {
				result, err = render.Render(req)
			}
			ev := goldens.Evidence{Writer: "workspace/render", Source: "authored plan-field table and resolved fixture inputs"}
			sidecar := intendedBindings{
				Compared: []string{"table argv tokens", "environment roots", "cwd", "RPC project parameters", "before-resume placement"},
				Excluded: map[string]string{"executable and protocol/turn argv": "runtime projection", "permission flags and environment": "registry posture mapper", "manifest, generation and reconcile report": "materialize"},
				Table:    result.Binding, ResumePlacement: "same table deltas apply to first and resumed launch; caller composes protocol tokens", Root: result.Root, Effects: result.Effects, Preparation: result.Preparations,
				CaseFamily: "native files and table bindings",
			}
			if in.Posture != "" {
				sidecar.CaseFamily = "posture-reference"
				sidecar.PermissionEvidence = permissionEvidence(in.Provider)
			}
			if in.Scenario == "host-slots" || in.Scenario == "duplicate-slot" || in.Scenario == "host-default" || in.Scenario == "installed-baseline" || in.Scenario == "installed-refresh" {
				sidecar.CaseFamily = "host-slot"
				sidecar.PermissionEvidence = "explicit host native settings; no posture reference"
			}
			if result.Binding.BeforeResume {
				sidecar.ResumePlacement = "table argv precedes caller-owned resume subcommand; first launch also receives it"
			}
			if req.Layer == layout.Installed {
				sidecar.InstalledSkillEvidence = "authored installed package row and declared-mode contract; archived installed seed supplies no installed skills; no installed-skill seed parity claimed"
				sidecar.ApplyAcceptance = installedApplyAcceptance(in.Provider)
			}
			if in.Scenario == "installed-refresh" {
				sidecar.CaseFamily = "supplied operator leaves and ownership metadata; no refresh byte parity claimed"
			}
			ev.Bindings = sidecar
			for _, d := range result.Diagnostics {
				raw, _ := json.Marshal(d)
				ev.Diagnostics = append(ev.Diagnostics, string(raw))
			}
			if err != nil {
				ev.Diagnostics = append(ev.Diagnostics, intendedError(err))
			} else {
				mode := result.RootMode
				if mode == 0 {
					mode = 0700
				}
				if err = os.MkdirAll(root, mode); err != nil {
					t.Fatal(err)
				}
				if err = os.Chmod(root, mode); err != nil {
					t.Fatal(err)
				}
				for _, e := range result.Tree.Entries {
					dest := filepath.Join(root, filepath.FromSlash(e.Path))
					if e.Kind == artifact.EntryDirectory {
						err = os.MkdirAll(dest, e.Mode)
					} else {
						err = os.WriteFile(dest, e.Bytes, e.Mode)
					}
					if err != nil {
						t.Fatal(err)
					}
					if err = os.Chmod(dest, e.Mode); err != nil {
						t.Fatal(err)
					}
				}
				type meta struct {
					Path       string              `json:"path"`
					Ownership  artifact.Ownership  `json:"ownership"`
					Provenance artifact.Provenance `json:"provenance"`
				}
				var entries []meta
				for _, e := range result.Tree.Entries {
					entries = append(entries, meta{e.Path, e.Ownership, e.Provenance})
				}
				ev.Ownership = entries
			}
			goldens.Check(t, dir, root, ev, normalize)
		})
	}
}
func intendedError(err error) string {
	var r *render.Diagnostic
	var p *layout.Diagnostic
	var n *contract.Refusal
	switch {
	case errors.As(err, &r):
		b, _ := json.Marshal(r)
		return string(b)
	case errors.As(err, &p):
		b, _ := json.Marshal(p)
		return string(b)
	case errors.As(err, &n):
		b, _ := json.Marshal(n)
		return string(b)
	}
	return err.Error()
}
func intendedRequest(in goldens.Input, root string) (render.Request, error) {
	req := render.Request{Provider: runtimes.ID(in.Provider), Layer: layout.Boot, Mode: runtimes.Mode(in.Runtime), Variant: layout.Variant(in.Variant), Agent: "fixture", Credentials: render.CredentialAvailable, Roots: map[layout.Root]string{layout.RootBoot: root, layout.RootProject: "/fixture/project", layout.RootHome: "/fixture/home"}}
	if in.Scenario == "installed" || in.Scenario == "installed-baseline" || in.Scenario == "installed-refresh" {
		req.DefinitionName = "fixture"
		req.Layer = layout.Installed
		req.Mode = layout.InstallMode
	}
	if in.Scenario == "invalid-agent" {
		req.Agent = "../DUMMY-SENTINEL"
	}
	req.Native.Servers = []contract.Server{
		{Name: "loopback", HTTPURL: "http://127.0.0.1:23456/mcp"},
		{Name: "mux", Command: "fixture-mcp", Args: []string{"--stdio"}, Env: []contract.Variable{{Name: "FIXTURE", Value: "yes"}}},
		{Name: "http", HTTPURL: "http://127.0.0.1:23457/mcp"},
		{Name: "stdio", Command: "fixture-server", Args: []string{"one", "two"}, Env: []contract.Variable{{Name: "FIXTURE", Value: "yes"}}},
	}
	if in.Scenario == "empty-mcp" {
		req.Native.Servers = nil
	}
	if in.Scenario == "stdio-no-env" {
		req.Native.Servers = []contract.Server{{Name: "mux", Command: "fixture-mcp"}}
	}
	if in.Scenario == "duplicate-mcp" {
		req.Native.Servers = append(req.Native.Servers, req.Native.Servers[0])
	}
	posture := permission.Mode(in.Posture)
	if in.Scenario == "yolo-deny" {
		_, err := permission.BindProfile("yolo", permission.Ceiling{Modes: []permission.Mode{permission.ModeYolo}, RequiresDenyEnforcement: true})
		return req, err
	}
	if posture != "" {
		bound, err := permission.BindProfile(in.Posture, permission.Ceiling{Modes: []permission.Mode{permission.ModeDefault, permission.ModePlan, permission.ModeAcceptEdits, permission.ModeYolo}})
		if err != nil {
			return req, err
		}
		posture = bound.Mode
	}
	if in.Scenario == "host-default" {
		req.Native.Codex.ApprovalPolicy = "never"
		req.Native.Codex.SandboxMode = "workspace-write"
	}
	if in.Scenario == "host-slots" || in.Scenario == "duplicate-slot" {
		switch req.Provider {
		case runtimes.Claude:
			req.Native.Claude.Slots = []contract.Slot{{Key: "permissions", Value: map[string]any{"defaultMode": "plan", "allow": []string{"Read"}}}}
			if in.Scenario == "duplicate-slot" {
				req.Native.Claude.Slots = append(req.Native.Claude.Slots, req.Native.Claude.Slots[0])
			}
		case runtimes.Codex:
			req.Native.Codex.Slots = []contract.Slot{{Key: "approval_policy", Value: "on-request"}, {Key: "sandbox_mode", Value: "read-only"}}
			if in.Scenario == "duplicate-slot" {
				req.Native.Codex.Slots = append(req.Native.Codex.Slots, req.Native.Codex.Slots[0])
			}
		case runtimes.OpenCode:
			req.Native.OpenCode.Slots = []contract.Slot{{Key: "permission", Value: map[string]string{"edit": "deny", "bash": "ask"}}}
			if in.Scenario == "duplicate-slot" {
				req.Native.OpenCode.Slots = append(req.Native.OpenCode.Slots, req.Native.OpenCode.Slots[0])
			}
		}
	}
	if in.Scenario == "installed-baseline" || in.Scenario == "installed-refresh" {
		switch req.Provider {
		case runtimes.Claude:
			req.Native.Claude.Slots = []contract.Slot{{Key: "permissions", Value: map[string]string{"defaultMode": "acceptEdits"}}}
			req.Native.Servers = nil
		case runtimes.Codex:
			req.Native.Codex.Slots = []contract.Slot{{Key: "approval_policy", Value: "on-request"}}
			req.Native.Servers = []contract.Server{{Name: "fixture", Command: "fixture-server", Args: []string{"one", "two"}}}
		}
	}
	if in.Scenario == "installed-refresh" {
		req.Native.OperatorKeyPaths = [][]string{{"fixture_operator"}}
		switch req.Provider {
		case runtimes.Claude:
			req.Native.Claude.Slots = append(req.Native.Claude.Slots, contract.Slot{Key: "fixture_operator", Value: true})
		case runtimes.Codex:
			req.Native.Codex.Slots = append(req.Native.Codex.Slots, contract.Slot{Key: "fixture_operator", Value: true})
		}
	}
	rows, err := layout.For(req.Provider, req.Layer, req.Mode, req.Variant)
	if err != nil {
		return req, err
	}
	fields := []layout.Field{layout.Instructions, layout.Settings, layout.Permissions, layout.MCP, layout.Skills, layout.Kickoff, layout.PlantingPlugin, layout.Credentials}
	if req.Layer == layout.Installed {
		fields = []layout.Field{layout.Instructions, layout.InstructionPointer, layout.Settings, layout.Skills}
		if req.Provider == runtimes.Codex {
			fields = []layout.Field{layout.Instructions, layout.Settings, layout.MCP, layout.Skills}
		} else {
			req.Native.Servers = nil
		}
	}
	if in.Scenario == "installed-baseline" || in.Scenario == "installed-refresh" {
		fields = []layout.Field{layout.Instructions, layout.InstructionPointer, layout.Settings}
		if req.Provider == runtimes.Codex {
			fields = []layout.Field{layout.Instructions, layout.Settings, layout.MCP}
		}
	}
	if in.Scenario == "pointer" {
		req.InstructionPointer = true
		fields = append(fields, layout.NeutralInstructions)
	}
	if in.Scenario == "registrations" {
		fields = append(fields, layout.Commands, layout.Subagents, layout.Prompts)
	}
	if in.Scenario == "omissions" {
		fields = append(fields, layout.Resources)
	}
	for _, field := range fields {
		present := false
		for _, row := range rows {
			if row.Field == field {
				present = true
			}
		}
		if !present && field != layout.Resources && field != layout.Commands && field != layout.Subagents && field != layout.Prompts {
			continue
		}
		name := "sample"
		if field == layout.Commands {
			name = "command"
		}
		if field == layout.Prompts {
			name = "prompt"
		}
		if field == layout.Subagents {
			name = "delegate"
		}
		resolve := layout.Request{Key: layout.Key{Provider: req.Provider, Layer: req.Layer, Mode: req.Mode, Variant: req.Variant, Field: field}, Requirement: layout.Required, Posture: posture, Components: map[string]string{"agent": req.Agent, "name": name}, LookupPosture: func(_ runtimes.ID, _ permission.Mode, _ runtimes.Mode) error { return nil }}
		if field == layout.Resources {
			resolve.Requirement = layout.Optional
		}
		if in.Scenario == "exclusive-mcp" && field == layout.MCP {
			resolve.ExclusiveMCP = true
		}
		resolution, err := layout.Resolve(resolve)
		if err != nil {
			return req, err
		}
		input := render.Input{Resolved: resolution}
		switch field {
		case layout.Instructions, layout.NeutralInstructions:
			input.Content.Body = []byte("Fixture instructions.\n")
			if req.InstructionPointer && field == layout.Instructions {
				input.Content.Body = nil
			}
		case layout.Kickoff:
			input.Content.Body = []byte("Fixture kickoff.\n")
		case layout.Skills:
			input.Content.Pin = render.Pin{Source: "fixture-package", Revision: "fixture-revision"}
			input.Content.Package = artifact.Tree{Entries: []artifact.Entry{{Path: "SKILL.md", Kind: artifact.EntryFile, Mode: 0640, Bytes: []byte("Fixture skill.\n")}, {Path: "scripts/run.sh", Kind: artifact.EntryFile, Mode: 0751, Bytes: []byte("#!/bin/sh\nexit 0\n")}}}
			if in.Scenario == "package-modes" {
				input.Content.Package.Entries = append(input.Content.Package.Entries, artifact.Entry{Path: "data.bin", Kind: artifact.EntryFile, Mode: 0600, Bytes: []byte{0, 255, 1}}, artifact.Entry{Path: "empty", Kind: artifact.EntryDirectory, Mode: 0755}, artifact.Entry{Path: "default.txt", Kind: artifact.EntryFile, Bytes: []byte{}}, artifact.Entry{Path: "clamp.sh", Kind: artifact.EntryFile, Mode: 04777, Bytes: []byte("#!/bin/sh\n")})
			}
			if in.Scenario == "credential-package" {
				input.Content.Package.Entries = append(input.Content.Package.Entries, artifact.Entry{Path: "auth.json", Kind: artifact.EntryFile, Bytes: []byte("DUMMY-CREDENTIAL-SENTINEL")})
			}
		case layout.Commands, layout.Subagents, layout.Prompts:
			input.Content.Pin = render.Pin{Source: "fixture-resource", Revision: "fixture-revision"}
			input.Content.Body = []byte(fmt.Sprintf("Fixture %s.\n", field))
		}
		req.Inputs = append(req.Inputs, input)
	}
	switch in.Scenario {
	case "missing-credentials":
		req.Credentials = render.CredentialMissing
	case "denied-credentials":
		req.Credentials = render.CredentialDenied
	case "credential-unresolved":
		req.Credentials = ""
	case "invalid-path":
		req.Overlays = []artifact.Entry{{Path: "notes/../escape", Kind: artifact.EntryFile, Bytes: []byte("DUMMY-SENTINEL")}}
	case "collision":
		req.Overlays = []artifact.Entry{{Path: "notes", Kind: artifact.EntryFile, Bytes: []byte("DUMMY-SENTINEL")}, {Path: "notes/child", Kind: artifact.EntryFile, Bytes: []byte("child")}}
	case "native-overlay":
		for _, r := range rows {
			if r.Field == layout.Settings || r.Field == layout.MCP {
				req.Overlays = []artifact.Entry{{Path: r.Path, Kind: artifact.EntryFile, Bytes: []byte("DUMMY-SENTINEL")}}
				break
			}
		}
	case "credential-overlay":
		req.Overlays = []artifact.Entry{{Path: "auth.json", Kind: artifact.EntryFile, Bytes: []byte("DUMMY-CREDENTIAL-SENTINEL")}}
	}
	return req, nil
}

type intendedBindings struct {
	InstalledSkillEvidence string               `json:"installed_skill_evidence,omitempty"`
	ApplyAcceptance        map[string]string    `json:"installed_apply_acceptance,omitempty"`
	Compared               []string             `json:"compared"`
	Excluded               map[string]string    `json:"excluded"`
	Table                  render.Binding       `json:"table"`
	ResumePlacement        string               `json:"resume_placement"`
	CaseFamily             string               `json:"case_family"`
	PermissionEvidence     string               `json:"permission_evidence,omitempty"`
	Root                   layout.Root          `json:"root"`
	Effects                []layout.Row         `json:"effects,omitempty"`
	Preparation            []render.Preparation `json:"preparation,omitempty"`
}

func permissionEvidence(provider string) string {
	switch provider {
	case "claude":
		return "adapters/provider/bootdir_claude.go: permissions.defaultMode native vocabulary and CLI equivalence; adapters/registry/posture.go: per-mode values"
	case "codex":
		return "adapters/provider/bootdir_codex.go: native approval/sandbox vocabulary; adapters/registry/posture.go: measured per-mode native overrides"
	case "opencode":
		return "adapters/registry/posture.go: OPENCODE_PERMISSION config object per mode; https://opencode.ai/docs/permissions/: native permission keys and actions"
	case "antigravity":
		return "native_permission_omitted: no evidenced native permission document; adapters/registry/posture.go: runtime-only mapping"
	}
	return ""
}

func TestInstalledCreateMatchesArchivedSeed(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			req, err := intendedRequest(goldens.Input{Provider: provider, Runtime: "install", Scenario: "installed-baseline"}, "/fixture/boot")
			if err != nil {
				t.Fatal(err)
			}
			out, err := render.Render(req)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join("..", "testdata", "goldens", "seeds", "cairn", provider, "install-create", "expected.json"))
			if err != nil {
				t.Fatal(err)
			}
			var archived []goldens.Entry
			if err = json.Unmarshal(raw, &archived); err != nil {
				t.Fatal(err)
			}
			wanted := map[string]goldens.Entry{}
			for _, e := range archived {
				if e.Kind == "file" {
					wanted[e.Path] = e
				}
			}
			for _, e := range out.Tree.Entries {
				if e.Kind != artifact.EntryFile {
					continue
				}
				want, exists := wanted[e.Path]
				if !exists {
					t.Fatalf("unattributed installed file %s", e.Path)
				}
				// The approved installed settings narrowing is the only mode delta.
				if provider == "claude" && e.Path == ".claude/settings.json" {
					want.Mode = "0600"
				}
				if string(e.Bytes) != want.Content || fmt.Sprintf("%04o", e.Mode.Perm()) != want.Mode {
					t.Fatalf("installed seed parity: %s\nwant %q\ngot %q", e.Path, want.Content, e.Bytes)
				}
				delete(wanted, e.Path)
			}
			if len(wanted) != 0 {
				t.Fatal("archived installed files omitted", wanted)
			}
		})
	}
}

func installedApplyAcceptance(provider string) map[string]string {
	seed := "seeds/cairn/" + provider + "/install-refresh"
	return map[string]string{
		"source_seed":           seed + " must pass byte-for-byte at installed apply; these pure fixtures do not perform a merge",
		"found_json_key_order":  "preserve existing JSON key order (Claude permissions before fixture_operator)",
		"unknown_nested_leaves": "preserve unknown operator leaves inside owned objects/tables, including Codex fixture_operator inside mcp_servers.fixture, without dropping or reordering",
		"unowned_values":        "never overwrite keys the installer does not own",
		"check_normalization":   "reproduce settings comparison after owned-key merge and Codex TOML normalization",
	}
}
