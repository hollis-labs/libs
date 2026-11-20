package agentdir

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProfiles(t *testing.T) {
	for _, kind := range []Kind{Stable, Task, Embedded} {
		t.Run(string(kind), func(t *testing.T) {
			snap, err := Load(filepath.Join("examples", "agents", string(kind)))
			if err != nil {
				t.Fatal(err)
			}
			if snap.Profile.Kind != kind {
				t.Fatalf("kind = %s", snap.Profile.Kind)
			}
			for _, tc := range []struct {
				provider, mode string
				steps          int
				valid          bool
			}{
				{"anthropic", "accept-edits", 50, true},
				{"openai", "plan", 0, true},
				{"", "default", 0, true},
				{"", "yolo", 0, true},
				{"typo", "", 0, false},
				{"", "typo", 0, false},
				{"", "", -1, false},
			} {
				p := snap.Profile
				p.Runtime.Provider, p.Permissions.Mode, p.Runtime.MaxSteps = tc.provider, tc.mode, tc.steps
				if err := p.Validate(&snap.Definition); (err == nil) != tc.valid {
					t.Errorf("%+v: %v", tc, err)
				}
			}
		})
	}
}

func TestSubtaskRuntimeKeys(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"agent.md", "host.yaml"} {
		data, err := os.ReadFile(filepath.Join("examples", "agents", "embedded", name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "host.yaml" {
			data = []byte(strings.Replace(string(data), "runtime:\n", "runtime:\n  subtask_limit: 6\n  turn_token_budget: 200000\n  subtask_result_cap: 4000\n  compact_at: 110000\n  compact_keep_tokens: 22000\n  compact_prompt: Summarize.\n  compact_input_tokens: 100000\n  idle_timeout: 3m\n", 1))
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	r := snap.Profile.Runtime
	if r.SubtaskLimit != 6 || r.TurnTokenBudget != 200000 || r.SubtaskResultCap != 4000 ||
		r.CompactAt != 110000 || r.CompactKeepTokens != 22000 || r.CompactPrompt != "Summarize." || r.CompactInputTokens != 100000 {
		t.Fatalf("runtime %+v", r)
	}
	if d, err := r.IdleTimeoutDuration(); err != nil || d != 3*time.Minute {
		t.Fatalf("idle_timeout %v, %v", d, err)
	}
	for field, set := range map[string]func(*Runtime){
		"subtask_limit":           func(r *Runtime) { r.SubtaskLimit = -1 },
		"turn_token_budget":       func(r *Runtime) { r.TurnTokenBudget = -1 },
		"subtask_result_cap":      func(r *Runtime) { r.SubtaskResultCap = -1 },
		"compact_at":              func(r *Runtime) { r.CompactAt = -1 },
		"compact_keep_tokens":     func(r *Runtime) { r.CompactKeepTokens = -1 },
		"compact_input_tokens":    func(r *Runtime) { r.CompactInputTokens = -1 },
		"idle_timeout_unparsable": func(r *Runtime) { r.IdleTimeout = "soon" },
		"idle_timeout_zero":       func(r *Runtime) { r.IdleTimeout = "0s" },
		"idle_timeout_negative":   func(r *Runtime) { r.IdleTimeout = "-5s" },
		"compact_prompt_nul":      func(r *Runtime) { r.CompactPrompt = "sum\x00marize" },
		"keep_equals_at":          func(r *Runtime) { r.CompactAt, r.CompactKeepTokens = 50000, 50000 },
		"keep_over_at":            func(r *Runtime) { r.CompactAt, r.CompactKeepTokens = 50000, 60000 },
		"at_over_window":          func(r *Runtime) { r.CompactAt, r.CompactKeepTokens, r.ContextWindow = 250000, 1000, 200000 },
	} {
		p := snap.Profile
		set(&p.Runtime)
		if err := p.Validate(&snap.Definition); err == nil {
			t.Errorf("an invalid %s was accepted: %+v", field, p.Runtime)
		}
	}
}

func TestMCP(t *testing.T) {
	for _, tc := range []struct{ raw, transport string }{
		{`{"command":"tool","args":["test"]}`, "stdio"},
		{`{"type":"http","url":"https://example.org/mcp"}`, "http"},
		{`{"type":"sse","url":"http://localhost:8080/sse"}`, "sse"},
		{`{"type":"http","url":"relative"}`, ""},
		{`{"command":"tool","typo":true}`, ""},
		{`null`, ""},
		{`[]`, ""},
	} {
		cfg, err := ParseMCP([]byte(`{"ignored":true,"mcpServers":{"test":` + tc.raw + `}}`))
		if tc.transport == "" && err == nil || tc.transport != "" && (err != nil || cfg.Servers["test"].Type != tc.transport) {
			t.Errorf("%s: %+v, %v", tc.raw, cfg, err)
		}
	}
}

func TestDigestAndSkills(t *testing.T) {
	root := t.TempDir()
	write := func(name, value string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"agent.md", "host.yaml"} {
		data, err := os.ReadFile(filepath.Join("examples", "agents", "stable", name))
		if err != nil {
			t.Fatal(err)
		}
		write(name, string(data))
	}
	load := func() Snapshot {
		t.Helper()
		snapshot, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	write("skills/demo/SKILL.md", "---\ndescription: Sample\n---\nbody")
	first := load()
	if len(first.Skills) != 1 || first.Skills[0].Name != "demo" || first.Skills[0].Description != "Sample" {
		t.Fatalf("skills: %+v", first.Skills)
	}
	_, body, err := ReadSkill(first.Skills[0])
	if err != nil || body != "body" {
		t.Fatalf("body %q: %v", body, err)
	}
	if first.Digest != load().Digest {
		t.Fatal("digest changed without an edit")
	}
	write("skills/demo/SKILL.md", "---\ndescription: Sample\n---\nedit")
	second := load()
	if first.Skills[0].Body != "body" || second.Skills[0].Body != "edit" {
		t.Fatalf("skill bodies were not captured in their original snapshots")
	}

	if first.Digest == second.Digest {
		t.Fatal("skill edit did not change digest")
	}
	write("context/notes.md", "notes")
	write("permissions.yaml", "permissions: {}")
	snapshot := load()
	if len(snapshot.Context) != 1 || snapshot.Context[0].Body != "notes" || string(snapshot.PermissionsRaw) != "permissions: {}" {
		t.Fatalf("unexpected snapshot files: %v, %q", snapshot.Context, snapshot.PermissionsRaw)
	}
	write("context/notes.md", "changed")
	write("permissions.yaml", "permissions: {mode: plan}")
	if snapshot.Context[0].Body != "notes" || string(snapshot.PermissionsRaw) != "permissions: {}" {
		t.Fatal("snapshot changed after editing the authored files")
	}

	if second.Digest == load().Digest {
		t.Fatal("context addition did not change digest")
	}
}

func TestProfileValidationAndMalformedFiles(t *testing.T) {
	dir := t.TempDir()
	definition, err := os.ReadFile(filepath.Join("examples", "agents", "embedded", "agent.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.md"), definition, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "host.yaml"), []byte("kind: stable\nlifetime: on-demand\nseat_id: seat-1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); !errors.Is(err, ErrProfile) {
		t.Fatalf("unsupported continuity: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "host.yaml"), []byte("kind: concurrent\nlifetime: on-demand\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); !errors.Is(err, ErrProfile) {
		t.Fatalf("unsupported kind: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "host.yaml"), []byte("kind: embedded\nlifetime: application\napplication_id: app\nunknown: value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); !errors.Is(err, ErrProfile) {
		t.Fatalf("unknown field: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "host.yaml"), []byte("kind: embedded\nlifetime: application\napplication_id: app\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(`{} {}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("accepted trailing MCP document")
	}
	if err := os.Remove(filepath.Join(dir, ".mcp.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(dir, "skills")); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("accepted symlinked skills directory")
	}
	if err := os.Remove(filepath.Join(dir, "skills")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.md"), []byte("not a definition"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("accepted malformed agent.md")
	}
}

func TestRejectedInputs(t *testing.T) {
	stable := filepath.Join("examples", "agents", "stable")
	for _, tc := range []struct {
		name, path, content, want string
		link, directory, missing  bool
	}{
		{name: "root symlink", path: ".", link: true, want: "real directory"},
		{name: "agent symlink", path: "agent.md", link: true, want: "regular file"},
		{name: "skill symlink", path: "skills/demo/SKILL.md", link: true, want: "regular file"},
		{name: "context symlink", path: "context/notes.md", link: true, want: "regular file"},
		{name: "skill directory symlink", path: "skills/demo", link: true, want: "symlink"},
		{name: "context directory entry", path: "context/notes.md", directory: true, want: "regular file"},
		{name: "oversize", path: "context/notes.md", content: strings.Repeat("a", 1<<20+1), want: "1 MiB"},
		{name: "unknown key", path: "host.yaml", content: "unknown: true\n", want: "field unknown"},
		{name: "multiple documents", path: "host.yaml", content: "kind: stable\n---\nkind: task\n", want: "one document"},
		{name: "missing agent", path: "agent.md", missing: true, want: "agent.md"},
		{name: "missing profile", path: "host.yaml", missing: true, want: "host.yaml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, name := range []string{"agent.md", "host.yaml"} {
				if name == tc.path {
					continue
				}
				raw, err := os.ReadFile(filepath.Join(stable, name))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, name), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(root, tc.path)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			var err error
			switch {
			case tc.link:
				target := filepath.Join(t.TempDir(), "link-target")
				if tc.path == "." || tc.path == "skills/demo" {
					if err := os.Mkdir(target, 0700); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(target, []byte("linked"), 0600); err != nil {
					t.Fatal(err)
				}
				if tc.path == "." {
					root = filepath.Join(t.TempDir(), "linked-agent")
					path = root
				}
				err = os.Symlink(target, path)
			case tc.directory:
				err = os.Mkdir(path, 0700)
			case !tc.missing:
				err = os.WriteFile(path, []byte(tc.content), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Load(root); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load error = %v, want %q", err, tc.want)
			}
		})
	}
}
