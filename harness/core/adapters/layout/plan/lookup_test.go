package plan

import (
	"errors"
	"github.com/hollis-labs/substrate/harness/adapters/layout"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
	"testing"
)

func key(p runtimes.ID, f Field) Key {
	return Key{Provider: p, Layer: Boot, Mode: runtimes.ModeSubprocessPerTurn, Field: f}
}
func code(t *testing.T, err error, want string) {
	t.Helper()
	var d *Diagnostic
	if !errors.As(err, &d) || d.Code != want {
		t.Fatalf("error %v, want %s", err, want)
	}
}
func TestAuthoredTable(t *testing.T) {
	if err := Validate(Table()); err != nil {
		t.Fatal(err)
	}
	for _, r := range Table() {
		k := Key{r.Provider, r.Layer, r.Mode, r.Variant, r.Field}
		if k.Mode == "" {
			k.Mode = runtimes.ModeSubprocessPerTurn
			if r.Layer == Installed {
				k.Mode = InstallMode
			}
		}
		got, err := Find(k)
		if err != nil {
			t.Fatal(err)
		}
		if got.Path != r.Path || got.Renderer != r.Renderer || got.ModeBits != r.ModeBits {
			t.Fatalf("lookup %v: %+v", k, got)
		}
	}
}
func TestWaveOnePlacements(t *testing.T) {
	cases := []struct {
		p          runtimes.ID
		l          Layer
		f          Field
		path, slot string
		form       Form
		bits       uint32
	}{
		{runtimes.Claude, Boot, Instructions, "CLAUDE.md", "", File, 0644},
		{runtimes.Claude, Boot, NeutralInstructions, "AGENTS.md", "", File, 0644},
		{runtimes.Claude, Boot, Settings, ".claude/settings.json", "", File, 0600},
		{runtimes.Claude, Boot, MCP, ".mcp.json", "", File, 0600},
		{runtimes.Claude, Boot, Skills, ".claude/skills/{name}/SKILL.md", "", Package, 0644},
		{runtimes.Claude, Boot, Subagents, ".claude/agents/{name}.md", "", File, 0644},
		{runtimes.Claude, Boot, Commands, ".claude/commands/boot/{name}.md", "", File, 0644},
		{runtimes.Claude, Boot, Prompts, ".claude/commands/boot/{name}.md", "", File, 0644},
		{runtimes.Codex, Boot, Instructions, "AGENTS.md", "", File, 0644},
		{runtimes.Codex, Boot, Settings, "config.toml", "", File, 0600},
		{runtimes.Codex, Boot, MCP, "config.toml", "mcp_servers", Slot, 0600},
		{runtimes.Codex, Boot, Skills, "skills/{name}/SKILL.md", "", Package, 0644},
		{runtimes.Codex, Boot, Credentials, "auth.json", "", Link, 0600},
		{runtimes.OpenCode, Boot, Instructions, "agents/{agent}.md", "", File, 0644},
		{runtimes.OpenCode, Boot, Settings, "opencode.json", "", File, 0600},
		{runtimes.OpenCode, Boot, MCP, "opencode.json", "mcp", Slot, 0600},
		{runtimes.OpenCode, Boot, Skills, "skills/{name}/SKILL.md", "", Package, 0644},
		{runtimes.Antigravity, Boot, Instructions, "AGENTS.md", "", File, 0644},
		{runtimes.Antigravity, Boot, PlantingPlugin, ".agents/plugins/tether/plugin.json", "", File, 0644},
		{runtimes.Antigravity, Boot, MCP, ".agents/plugins/tether/mcp_config.json", "", File, 0600},
		{runtimes.Antigravity, Boot, Skills, ".agents/skills/{name}/SKILL.md", "", Package, 0644},
		{runtimes.Claude, Installed, Instructions, ".claude/AGENTS.md", "", File, 0644},
		{runtimes.Claude, Installed, InstructionPointer, ".claude/CLAUDE.md", "", File, 0644},
		{runtimes.Claude, Installed, Settings, ".claude/settings.json", "", File, 0600},
		{runtimes.Claude, Installed, Skills, ".claude/skills/{name}/SKILL.md", "", Package, 0644},
		{runtimes.Codex, Installed, Instructions, ".codex/AGENTS.md", "", File, 0644},
		{runtimes.Codex, Installed, Settings, ".codex/config.toml", "", File, 0600},
		{runtimes.Codex, Installed, MCP, ".codex/config.toml", "mcp_servers", Slot, 0600},
		{runtimes.Codex, Installed, Skills, ".agents/skills/{name}/SKILL.md", "", Package, 0644},
	}
	for _, c := range cases {
		t.Run(string(c.p)+"/"+string(c.l)+"/"+string(c.f), func(t *testing.T) {
			k := key(c.p, c.f)
			k.Layer = c.l
			if c.l == Installed {
				k.Mode = InstallMode
			}
			r, err := Find(k)
			if err != nil {
				t.Fatal(err)
			}
			if r.Path != c.path || r.Form != c.form || r.ModeBits != c.bits || r.DocumentSlot != c.slot {
				t.Fatalf("unexpected row: %+v", r)
			}
			root := layout.RootBoot
			if c.l == Installed {
				root = layout.RootHome
			}
			if r.Root != root {
				t.Fatalf("root %s", r.Root)
			}
		})
	}
	for _, p := range []runtimes.ID{runtimes.Claude, runtimes.Codex, runtimes.OpenCode, runtimes.Antigravity} {
		r, err := Find(key(p, Kickoff))
		if err != nil || r.Path != "boot.md" {
			t.Fatalf("kickoff %s: %+v %v", p, r, err)
		}
	}
}
func TestLocators(t *testing.T) {
	k := key(runtimes.Codex, Settings)
	r, _ := Find(k)
	if r.Locator.Argv[0] != "--cd" || !r.Locator.BeforeResume || r.Locator.Env["CODEX_HOME"] != layout.RootBoot {
		t.Fatalf("exec: %+v", r)
	}
	k.Mode = runtimes.ModeJSONRPCStdio
	r, _ = Find(k)
	if len(r.Locator.Argv) != 0 || r.Locator.RPCProject != "thread.cwd" {
		t.Fatalf("RPC %+v", r)
	}
	k = key(runtimes.OpenCode, Instructions)
	r, _ = Find(k)
	if r.Locator.CWD != layout.RootProject || r.Locator.Argv[0] != "--agent" || r.Locator.Env["OPENCODE_CONFIG_DIR"] != layout.RootBoot {
		t.Fatalf("OpenCode %+v", r)
	}
	k.Mode = runtimes.ModeHTTPSSE
	r, _ = Find(k)
	if r.Locator.RPCProject != "directory/agent" || r.Locator.Argv[2] != "127.0.0.1" {
		t.Fatalf("serve %+v", r)
	}
	for _, f := range []Field{Instructions, Settings, Skills} {
		k = key(runtimes.Claude, f)
		k.Variant = layout.VariantBare
		r, err := Find(k)
		if err != nil || len(r.Locator.Argv) == 0 {
			t.Fatalf("bare %s: %+v %v", f, r, err)
		}
	}
}
func TestPrecedenceAndAmbiguity(t *testing.T) {
	base, _ := Find(key(runtimes.Claude, Instructions))
	base.Mode = ""
	base.Variant = ""
	mode := base
	mode.Mode = runtimes.ModeSubprocessPerTurn
	mode.Renderer = "mode"
	variant := mode
	variant.Variant = layout.VariantBare
	variant.Renderer = "variant"
	table := []Row{base, mode, variant}
	k := key(runtimes.Claude, Instructions)
	got, err := FindIn(table, k)
	if err != nil || got.Renderer != "mode" {
		t.Fatal(got, err)
	}
	k.Variant = layout.VariantBare
	got, err = FindIn(table, k)
	if err != nil || got.Renderer != "variant" {
		t.Fatal(got, err)
	}
	k.Mode = runtimes.ModeStreamingStdio
	k.Variant = ""
	got, err = FindIn(table, k)
	if err != nil || got.Renderer != base.Renderer {
		t.Fatal(got, err)
	}
	code(t, Validate(append(table, variant)), "duplicate_layout")
	k.Mode = runtimes.ModeSubprocessPerTurn
	k.Variant = layout.VariantBare
	_, err = FindIn(append(table, variant), k)
	code(t, err, "ambiguous_layout")
	got, err = FindIn([]Row{base, base, variant}, k)
	if err != nil || got.Renderer != "variant" {
		t.Fatal(got, err)
	}
	variant.Mode = ""
	code(t, Validate([]Row{variant}), "invalid_layout")
}
func TestRefusals(t *testing.T) {
	cases := []struct {
		k Key
		c string
	}{
		{key("gemini", Instructions), "unsupported_provider"}, {key("other", Instructions), "unsupported_provider"}, {key("agy", Instructions), "unsupported_provider"},
		{Key{runtimes.Claude, Boot, runtimes.ModeACPStdio, "", Instructions}, "unsupported_runtime"},
		{Key{runtimes.Codex, Boot, runtimes.ModeACPTCP, "", Instructions}, "unsupported_runtime"},
		{Key{runtimes.OpenCode, Installed, InstallMode, "", Instructions}, "unsupported_layer"},
		{Key{runtimes.Antigravity, Installed, InstallMode, "", Instructions}, "unsupported_layer"},
		{Key{runtimes.Claude, "other", runtimes.ModeSubprocessPerTurn, "", Instructions}, "unsupported_layer"},
		{Key{runtimes.Codex, Installed, runtimes.ModeSubprocessPerTurn, "", Instructions}, "unsupported_runtime"},
		{Key{runtimes.Claude, Boot, runtimes.ModeJSONRPCStdio, "", Instructions}, "unsupported_runtime"},
		{Key{runtimes.Claude, Boot, runtimes.ModeStreamingStdio, layout.VariantBare, Instructions}, "unsupported_variant"},
		{key(runtimes.Claude, "invented"), "unknown_plan_field"},
	}
	for _, c := range cases {
		_, err := Resolve(Request{Key: c.k})
		code(t, err, c.c)
		var d *Diagnostic
		errors.As(err, &d)
		if d.Provider != c.k.Provider || d.Mode != c.k.Mode || d.Concern != c.k.Field || d.Reason == "" {
			t.Fatalf("incomplete: %+v", d)
		}
	}
	if NormalizeProvider("agy") != runtimes.Antigravity || NormalizeProvider("gemini") != "gemini" {
		t.Fatal("alias")
	}
	for _, f := range []Field{Hooks, Subagents, Prompts, Resources} {
		k := key(runtimes.Codex, f)
		_, err := Resolve(Request{Key: k, Required: true})
		code(t, err, "unsupported_feature")
		res, err := Resolve(Request{Key: k})
		if err != nil || res.Omission == nil || res.Omission.Code != "omitted_"+string(f) {
			t.Fatal(res, err)
		}
	}
	_, err := Resolve(Request{Key: key(runtimes.Antigravity, Permissions), Required: true, PostureID: "unknown", LookupPosture: func(runtimes.ID, string, runtimes.Mode) error { return errors.New("unmapped") }})
	code(t, err, "unsupported_feature")
	for _, p := range []runtimes.ID{runtimes.OpenCode, runtimes.Antigravity} {
		_, err := Resolve(Request{Key: key(p, MCP), ExclusiveMCP: true})
		code(t, err, "unsupported_mcp_exclusivity")
	}
	_, err = Resolve(Request{Key: key(runtimes.Claude, Settings), ExclusiveMCP: true})
	code(t, err, "invalid_request")
	res, err := Resolve(Request{Key: key(runtimes.Claude, MCP), ExclusiveMCP: true})
	if err != nil || res.Row.Locator.Argv[len(res.Row.Locator.Argv)-1] != "--strict-mcp-config" {
		t.Fatal(res, err)
	}
	res, err = Resolve(Request{Key: key(runtimes.Codex, MCP), ExclusiveMCP: true})
	if err != nil || res.Row.Locator.Env["CODEX_HOME"] != layout.RootBoot {
		t.Fatal(res, err)
	}
}
func TestPathSafety(t *testing.T) {
	for _, v := range []string{"", ".", "..", "a/b", "a\\b", "a:b", "x\x00", "x\n", "{name}"} {
		_, err := Expand("skills/{name}/SKILL.md", map[string]string{"name": v})
		if err == nil {
			t.Fatalf("accepted %q", v)
		}
	}
	for _, p := range []string{"/absolute", "../escape", "a/../escape", "a//b", "a\\b", "a/{unknown}"} {
		_, err := Expand(p, nil)
		if err == nil {
			t.Fatalf("accepted %q", p)
		}
	}
	got, err := Expand("agents/{agent}.md", map[string]string{"agent": "worker"})
	if err != nil || got != "agents/worker.md" {
		t.Fatal(got, err)
	}
	_, err = Resolve(Request{Key: key(runtimes.Codex, Skills), Components: map[string]string{"name": "../escape"}})
	code(t, err, "invalid_component")
	res, err := Resolve(Request{Key: key(runtimes.Codex, Skills), Components: map[string]string{"name": "safe"}})
	if err != nil || res.Row.Path != "skills/safe/SKILL.md" {
		t.Fatal(res, err)
	}
}
func TestDetachedTableAndSources(t *testing.T) {
	a := Table()
	for i := range a {
		a[i].Path = "changed"
		if a[i].Locator.Env != nil {
			a[i].Locator.Env["CODEX_HOME"] = layout.RootProject
		}
		if len(a[i].Locator.Argv) > 0 {
			a[i].Locator.Argv[0] = "changed"
		}
	}
	r, _ := Find(key(runtimes.Codex, Settings))
	r.Locator.Env["CODEX_HOME"] = layout.RootProject
	again, _ := Find(key(runtimes.Codex, Settings))
	if again.Path != "config.toml" || again.Locator.Env["CODEX_HOME"] != layout.RootBoot {
		t.Fatal("aliasing")
	}
	for _, f := range []Field{Commands, Subagents, Prompts} {
		s, ok := Sources(f)
		if !ok || s[0] != "requirements.resources" {
			t.Fatal(s)
		}
		s[0] = "changed"
		again, _ := Sources(f)
		if again[0] != "requirements.resources" {
			t.Fatal("sources alias")
		}
	}
	for _, shape := range []struct {
		p runtimes.ID
		m runtimes.Mode
	}{{runtimes.Claude, runtimes.ModeStreamingStdio}, {runtimes.Claude, runtimes.ModePTY}, {runtimes.Codex, runtimes.ModeJSONRPCStdio}, {runtimes.OpenCode, runtimes.ModeHTTPSSE}, {runtimes.Antigravity, runtimes.ModeSubprocessPerTurn}} {
		r, err := For(shape.p, Boot, shape.m, "")
		if err != nil || len(r) == 0 {
			t.Fatal(r, err)
		}
	}
}
func TestInvalidRows(t *testing.T) {
	base, _ := Find(key(runtimes.Claude, MCP))
	cases := []struct {
		name   string
		mutate func(*Row)
	}{
		{"path", func(r *Row) { r.Path = "../escape" }},
		{"placeholder", func(r *Row) { r.Path = "{unknown}/file" }},
		{"mode", func(r *Row) { r.ModeBits = 0 }},
		{"special-mode", func(r *Row) { r.ModeBits = 04755 }},
		{"public-mcp", func(r *Row) { r.ModeBits = 0644 }},
		{"root", func(r *Row) { r.Root = "unknown" }},
		{"evidence", func(r *Row) { r.Evidence.Reference = "" }},
		{"concern", func(r *Row) { r.Concern = "" }},
		{"capability", func(r *Row) { r.Capability = "other" }},
		{"reason", func(r *Row) { r.Capability = Unsupported; r.Reason = "" }},
		{"form", func(r *Row) { r.Form = "other" }},
		{"renderer", func(r *Row) { r.Renderer = "" }},
		{"slot", func(r *Row) { r.Form = Slot; r.DocumentSlot = "" }},
		{"credential-copy", func(r *Row) { r.Field = Credentials; r.Form = File }},
		{"shell-string", func(r *Row) { r.Locator.Argv = []string{"--mcp-config file; command"} }},
		{"shell-expansion", func(r *Row) { r.Locator.Argv = []string{"$(command)"} }},
		{"unknown-token", func(r *Row) { r.Locator.Argv = []string{"{unknown}"} }},
		{"env-root", func(r *Row) { r.Locator.Env = map[string]layout.Root{"CONFIG": "other"} }},
		{"env-name", func(r *Row) { r.Locator.Env = map[string]layout.Root{"bad name": layout.RootBoot} }},
		{"cwd", func(r *Row) { r.Locator.CWD = "other" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { r := base.clone(); c.mutate(&r); code(t, Validate([]Row{r}), "invalid_layout") })
	}
}

func TestRuntimePermissionBindings(t *testing.T) {
	for _, p := range []runtimes.ID{runtimes.Claude, runtimes.Codex, runtimes.OpenCode, runtimes.Antigravity} {
		k := key(p, Permissions)
		missing, err := Resolve(Request{Key: k})
		if err != nil || missing.Row.Posture == nil || missing.Row.Posture.PostureID != "" {
			t.Fatal(missing, err)
		}
		calls := 0
		lookup := func(provider runtimes.ID, posture string, mode runtimes.Mode) error {
			calls++
			if provider != p || mode != k.Mode || posture != "plan" {
				return errors.New("unmapped")
			}
			return nil
		}
		res, err := Resolve(Request{Key: k, Required: true, PostureID: "plan", LookupPosture: lookup})
		if err != nil || calls != 1 || res.Row.Posture.PostureID != "plan" {
			t.Fatal(res, err)
		}
		if p == runtimes.Antigravity && (res.Row.Path != "" || res.Row.ModeBits != 0 || res.Row.Form != RuntimeBinding) {
			t.Fatal("invented native file")
		}
		if p == runtimes.OpenCode && (res.Row.Path != "opencode.json" || res.Row.DocumentSlot != "permission") {
			t.Fatal(res)
		}
		_, err = Resolve(Request{Key: k, PostureID: "unknown", Required: true, LookupPosture: lookup})
		code(t, err, "unsupported_feature")
		res, err = Resolve(Request{Key: k, PostureID: "unknown", LookupPosture: lookup})
		if err != nil || res.Omission == nil || res.Omission.Code != "omitted_permissions" {
			t.Fatal(res, err)
		}
		_, err = Resolve(Request{Key: k, PostureID: "plan"})
		code(t, err, "unresolved_runtime_binding")
	}
}
func TestCredentialDestinationsNeverWrite(t *testing.T) {
	for _, r := range Table() {
		if r.Field == Credentials || r.Path == "auth.json" || r.Path == ".credentials.json" || r.Path == "oauth_creds.json" {
			if r.Form != Link || r.CredentialPolicy != LinkOnlyNeverWrite {
				t.Fatalf("credential can become a managed file: %+v", r)
			}
		}
	}
	for _, mode := range []runtimes.Mode{runtimes.ModeSubprocessPerTurn, runtimes.ModeJSONRPCStdio} {
		k := key(runtimes.Codex, Credentials)
		k.Mode = mode
		res, err := Resolve(Request{Key: k, Required: true})
		if err != nil {
			t.Fatal(err)
		}
		if res.Row.Path != "" || res.Row.Renderer != "" {
			t.Fatal("credential returned as managed row")
		}
		found := false
		for _, effect := range res.Effects {
			if effect.Path == "auth.json" && effect.Form == Link && effect.CredentialPolicy == LinkOnlyNeverWrite {
				found = true
			}
		}
		if !found {
			t.Fatal("missing link-only preserve-on-replant effect")
		}
	}
	row, _ := Find(key(runtimes.Codex, Credentials))
	row.CredentialPolicy = ""
	code(t, Validate([]Row{row}), "invalid_layout")
}
