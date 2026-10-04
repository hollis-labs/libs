package plan

import (
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
)

const discovery = "adapters/provider/testdata/harness-discovery/2026-09-29-claude-2.1.285-codex-0.154.0-opencode-1.18.30.tsv"
const exclusive = "adapters/provider/testdata/mcp-exclusive/2026-10-01-claude-2.1.286-codex-0.159.3-opencode-1.18.33.tsv"
const placementEvidence = "adapters/layout/plan/doc.go"

// rows is the sole authored plan-field placement table. Helper constructors
// fill mechanical defaults only; every path and semantic selector is here.
var rows = buildTable()

func buildTable() []Row {
	var out []Row
	add := func(p runtimes.ID, l Layer, f Field, c, path, renderer string, form Form, bits uint32, e Evidence) {
		root := RootBoot
		if l == Installed {
			root = RootHome
		}
		out = append(out, Row{Provider: p, Layer: l, Field: f, Concern: c, Root: root, Path: path, Form: form, ModeBits: bits, Renderer: renderer, Capability: Supported, Evidence: e, Locator: locator(p, l)})
	}
	obs := func(ids ...string) Evidence { return Evidence{Reference: discovery, Observations: ids} }
	source := func(note string) Evidence { return Evidence{Reference: placementEvidence, Note: note} }
	claude := runtimes.Claude
	codex := runtimes.Codex
	oc := runtimes.OpenCode
	agy := runtimes.Antigravity
	add(claude, Boot, Instructions, "instructions", "CLAUDE.md", "claude-instructions", File, 0644, source("model-visible discovery; direct body or explicit pointer"))
	add(claude, Boot, NeutralInstructions, "neutral-body", "AGENTS.md", "instructions", File, 0644, source("optional content body; not a second discovery claim"))
	for _, f := range []Field{Permissions, Settings, Hooks} {
		add(claude, Boot, f, "native-config", ".claude/settings.json", "claude-settings", File, 0600, obs("CFG1", "CFG2"))
	}
	add(claude, Boot, MCP, "mcp", ".mcp.json", "claude-mcp", File, 0600, Evidence{Reference: exclusive, Observations: []string{"MCP2", "MCP6", "MCP8", "MCP10"}, Note: "strict covers measured config sources only"})
	out[len(out)-1].ExclusiveMCP = Supported
	out[len(out)-1].Locator.Argv = []string{"--mcp-config", "{path}", "--add-dir", "{P}"}
	add(claude, Boot, Skills, "skills", ".claude/skills/{name}/SKILL.md", "skill-package", Package, 0644, obs("C1", "C2", "C3"))
	add(claude, Boot, Subagents, "subagents", ".claude/agents/{name}.md", "claude-subagent", File, 0644, source("resolved pinned content registration"))
	for _, f := range []Field{Commands, Prompts} {
		add(claude, Boot, f, "prompts", ".claude/commands/boot/{name}.md", "claude-command", File, 0644, source("boot command namespace; resolved pinned content registration"))
	}
	// The bare variant replaces discovery with explicit token bindings.
	for _, f := range []Field{Instructions, Permissions, Settings, Hooks, Skills} {
		var r Row
		for _, base := range out {
			if base.Provider == claude && base.Layer == Boot && base.Field == f && base.Mode == "" {
				r = base.clone()
				break
			}
		}
		r.Mode = runtimes.ModeSubprocessPerTurn
		r.Variant = VariantBare
		switch f {
		case Instructions:
			r.Locator.Argv = []string{"--append-system-prompt-file", "{path}", "--add-dir", "{P}"}
		case Skills:
			r.Locator.Argv = []string{"--add-dir", "{B}", "--add-dir", "{P}"}
			r.Evidence = obs("C4", "C5")
		default:
			r.Locator.Argv = []string{"--settings", "{path}", "--add-dir", "{P}"}
			r.Evidence = obs("CFG2")
		}
		out = append(out, r)
	}
	add(codex, Boot, Instructions, "instructions", "AGENTS.md", "instructions", File, 0644, obs("CFG2", "CFG3"))
	for _, f := range []Field{Permissions, Settings} {
		add(codex, Boot, f, "native-config", "config.toml", "codex-config", File, 0600, obs("CFG2", "CFG3"))
	}
	add(codex, Boot, MCP, "mcp", "config.toml", "codex-config", Slot, 0600, Evidence{Reference: exclusive, Observations: []string{"MCP2", "MCP3", "MCP6", "MCP8"}, Note: "exclusivity depends on CODEX_HOME binding"})
	out[len(out)-1].DocumentSlot = "mcp_servers"
	out[len(out)-1].ExclusiveMCP = Supported
	add(codex, Boot, Skills, "skills", "skills/{name}/SKILL.md", "skill-package", Package, 0644, obs("X2", "X3", "X4"))
	add(codex, Boot, Credentials, "credentials", "auth.json", "credential-link", Link, 0600, source("explicit real-provider-home link; no rendering or ambient read"))
	for _, item := range []struct {
		f Field
		p string
	}{{Hooks, "hooks.json"}, {Resources, "hooks"}} {
		add(codex, Boot, item.f, "home-resource", item.p, "resource-request", Resource, 0644, source("Cairn resource declaration is not hook support"))
		out[len(out)-1].Capability = Unsupported
		out[len(out)-1].Reason = "requires a declared resource implementation; no native hook projection in this cut"
	}
	add(oc, Boot, Instructions, "instructions", "agents/{agent}.md", "opencode-agent", File, 0644, source("primary agent front matter; ID must match launch selection"))
	add(oc, Boot, Permissions, "native-config", "opencode.json", "opencode-config", Slot, 0600, source("native permission slot plus runtime posture mapping"))
	out[len(out)-1].DocumentSlot = "permission"
	add(oc, Boot, Settings, "native-config", "opencode.json", "opencode-config", File, 0600, obs("CFG2"))
	add(oc, Boot, MCP, "mcp", "opencode.json", "opencode-config", Slot, 0600, Evidence{Reference: exclusive, Observations: []string{"MCP1", "MCP2", "MCP3", "MCP4"}, Note: "no measured MCP-only isolation"})
	out[len(out)-1].DocumentSlot = "mcp"
	out[len(out)-1].ExclusiveMCP = Unsupported
	add(oc, Boot, Skills, "skills", "skills/{name}/SKILL.md", "skill-package", Package, 0644, obs("O2"))
	add(agy, Boot, Permissions, "runtime-permissions", "", "", RuntimeBinding, 0, source("adapters/registry/posture.go: agy permission mapping is runtime-only; no native permission file; agy 1.2.14 accepts flags, behavior is not fully measured"))
	add(agy, Boot, Instructions, "instructions", "AGENTS.md", "instructions", File, 0644, source("agy 1.2.7 live transcript; model-visible, separate from the archived probe"))
	add(agy, Boot, PlantingPlugin, "native-config", ".agents/plugins/tether/plugin.json", "antigravity-plugin", File, 0644, source("agy 1.2.7: stable tether marker"))
	add(agy, Boot, MCP, "mcp", ".agents/plugins/tether/mcp_config.json", "antigravity-mcp", File, 0600, source("agy 1.2.7 live plugin discovery; isolation unmeasured"))
	out[len(out)-1].ExclusiveMCP = Unmeasured
	add(agy, Boot, Skills, "skills", ".agents/skills/{name}/SKILL.md", "skill-package", Package, 0644, source("agy 1.2.7 live workspace skill discovery"))
	for _, p := range []runtimes.ID{claude, codex, oc, agy} {
		add(p, Boot, Kickoff, "kickoff", "boot.md", "kickoff", File, 0644, source("launcher-read artifact, distinct from persona"))
	}
	// Installation takes the explicitly supplied H root, never an ambient home.
	add(claude, Installed, Instructions, "instructions", ".claude/AGENTS.md", "instructions", File, 0644, source("Cairn installed instruction body"))
	add(claude, Installed, InstructionPointer, "pointer", ".claude/CLAUDE.md", "claude-pointer", File, 0644, source("explicit @AGENTS.md pointer"))
	for _, f := range []Field{Permissions, Settings, Hooks} {
		add(claude, Installed, f, "native-config", ".claude/settings.json", "claude-settings", File, 0600, source("owned-key JSON merge preserves operator settings"))
	}
	add(claude, Installed, Skills, "skills", ".claude/skills/{name}/SKILL.md", "skill-package", Package, 0644, source("Cairn installed skill ownership"))
	add(codex, Installed, Instructions, "instructions", ".codex/AGENTS.md", "instructions", File, 0644, source("Cairn installed instructions"))
	for _, f := range []Field{Permissions, Settings, MCP} {
		add(codex, Installed, f, "native-config", ".codex/config.toml", "codex-config", File, 0600, source("owned-key TOML merge preserves operator settings"))
		if f == MCP {
			out[len(out)-1].Form = Slot
			out[len(out)-1].DocumentSlot = "mcp_servers"
			out[len(out)-1].ExclusiveMCP = Unsupported
		}
	}
	add(codex, Installed, Skills, "skills", ".agents/skills/{name}/SKILL.md", "skill-package", Package, 0644, source("installed skill path deliberately differs from isolated boot"))
	// Exact-mode bindings override layer defaults, preserving paths/serializers.
	defaults := append([]Row(nil), out...)
	for _, r := range defaults {
		if r.Layer != Boot || r.Mode != "" {
			continue
		}
		if r.Provider == codex {
			r.Mode = runtimes.ModeSubprocessPerTurn
			r.Locator.Argv = []string{"--cd", "{P}"}
			r.Locator.BeforeResume = true
			out = append(out, r.clone())
			r.Mode = runtimes.ModeJSONRPCStdio
			r.Locator.Argv = nil
			r.Locator.BeforeResume = false
			r.Locator.RPCProject = "thread.cwd"
			out = append(out, r)
		}
		if r.Provider == oc {
			r.Mode = runtimes.ModeSubprocessPerTurn
			r.Locator.Argv = []string{"run", "--agent", "{agent}", "--dir", "{P}"}
			out = append(out, r.clone())
			r.Mode = runtimes.ModeHTTPSSE
			r.Locator.Argv = []string{"serve", "--hostname", "127.0.0.1"}
			r.Locator.RPCProject = "directory/agent"
			out = append(out, r)
		}
	}
	for i := range out {
		if out[i].Form == Link || out[i].Form == RuntimeBinding {
			out[i].Locator = Locator{}
		}
		if out[i].Concern == "native-config" || out[i].Form == Slot {
			out[i].Composition = out[i].Renderer
		}
		if out[i].Field == Commands || out[i].Field == Prompts {
			out[i].Composition = "claude-command-pack"
		}

		if out[i].Form == Link {
			out[i].CredentialPolicy = LinkOnlyNeverWrite
		}
		if out[i].Provider == agy && out[i].Field != Permissions {
			out[i].Evidence.Reference = "adapters/providertest/fixtures/antigravity/print_mcp_tool.jsonl"
		}
		if out[i].Field == Permissions && out[i].Layer == Boot {
			out[i].Posture = &PostureReference{Provider: out[i].Provider, Mapper: "adapters/registry.Descriptor.PostureFor"}
		}
	}
	return out
}
func locator(p runtimes.ID, l Layer) Locator {
	if l == Installed {
		return Locator{}
	}
	switch p {
	case runtimes.Claude:
		return Locator{CWD: RootBoot, Argv: []string{"--add-dir", "{P}"}}
	case runtimes.Codex:
		return Locator{CWD: RootBoot, Env: map[string]Root{"CODEX_HOME": RootBoot}}
	case runtimes.OpenCode:
		return Locator{CWD: RootProject, Env: map[string]Root{"OPENCODE_CONFIG_DIR": RootBoot}}
	default:
		return Locator{CWD: RootBoot, Argv: []string{"--add-dir", "{P}"}}
	}
}

// The support and refusal selectors are authored beside the placement rows.
// Empty refusal selectors match any value; ordered explicit refusals precede
// positive shape lookup. Generic unknown-shape reasons are data here too.
type shapeSupport struct {
	Provider runtimes.ID
	Layer    Layer
	Mode     runtimes.Mode
	Variant  Variant
}

var supportedShapes = []shapeSupport{
	{runtimes.Claude, Boot, runtimes.ModeStreamingStdio, ""},
	{runtimes.Claude, Boot, runtimes.ModeSubprocessPerTurn, ""},
	{runtimes.Claude, Boot, runtimes.ModePTY, ""},
	{runtimes.Claude, Boot, runtimes.ModeSubprocessPerTurn, VariantBare},
	{runtimes.Codex, Boot, runtimes.ModeSubprocessPerTurn, ""},
	{runtimes.Codex, Boot, runtimes.ModeJSONRPCStdio, ""},
	{runtimes.OpenCode, Boot, runtimes.ModeSubprocessPerTurn, ""},
	{runtimes.OpenCode, Boot, runtimes.ModeHTTPSSE, ""},
	{runtimes.Antigravity, Boot, runtimes.ModeSubprocessPerTurn, ""},
	{runtimes.Claude, Installed, InstallMode, ""},
	{runtimes.Codex, Installed, InstallMode, ""},
}

type refusalRow struct {
	Provider     runtimes.ID
	Layer        Layer
	Mode         runtimes.Mode
	Code, Reason string
}

var refusals = []refusalRow{
	{Mode: runtimes.ModeACPStdio, Code: "unsupported_runtime", Reason: "ACP transport projection is deferred"},
	{Mode: runtimes.ModeACPTCP, Code: "unsupported_runtime", Reason: "ACP transport projection is deferred"},
	{Provider: runtimes.Copilot, Code: "unsupported_runtime", Reason: "this runtime is ACP-only; ACP projection is deferred"},
	{Provider: runtimes.Pi, Code: "unsupported_runtime", Reason: "this runtime is ACP-only; ACP projection is deferred"},
	{Provider: "gemini", Code: "unsupported_provider", Reason: "Gemini is unsupported and is not an Antigravity alias"},
	{Provider: runtimes.OpenCode, Layer: Installed, Code: "unsupported_layer", Reason: "OpenCode installation is deferred"},
	{Provider: runtimes.Antigravity, Layer: Installed, Code: "unsupported_layer", Reason: "Antigravity installation is deferred"},
}
var refusalReasons = map[string]string{
	"unsupported_provider": "provider has no authored projection rows",
	"unsupported_layer":    "layer has no authored support for this provider",
	"unsupported_runtime":  "transport has no authored support for this provider and layer",
	"unsupported_variant":  "variant has no authored support in this transport",
}
