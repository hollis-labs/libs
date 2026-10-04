package render

import (
	"bytes"
	"errors"
	layout "github.com/hollis-labs/substrate/harness/adapters/layout/plan"
	contract "github.com/hollis-labs/substrate/harness/adapters/nativefiles"
	"github.com/hollis-labs/substrate/harness/interception/permission"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
	"io/fs"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestPackageModesAndInputIsolation(t *testing.T) {
	req := request(runtimes.Codex)
	entries := []artifact.Entry{
		{Path: "SKILL.md", Kind: artifact.EntryFile, Mode: 0640, Bytes: []byte("skill")},
		{Path: "default.txt", Kind: artifact.EntryFile, Bytes: []byte("default")},
		{Path: "scripts/run.sh", Kind: artifact.EntryFile, Mode: 0751, Bytes: []byte("script")},
		{Path: "unsafe", Kind: artifact.EntryFile, Mode: fs.ModeSetuid | 0672, Bytes: []byte("unsafe")},
		{Path: "empty", Kind: artifact.EntryDirectory, Mode: 0750},
	}
	req.Inputs = []Input{{Resolved: resolved(t, runtimes.Codex, layout.Skills), Content: Content{Pin: Pin{"fixture-package", "revision"}, Package: artifact.Tree{Entries: entries}}}}
	out, err := Render(req)
	if err != nil {
		t.Fatal(err)
	}
	modes := map[string]fs.FileMode{}
	for _, e := range out.Tree.Entries {
		modes[e.Path] = e.Mode
		if e.Path == "skills/sample/SKILL.md" {
			e.Bytes[0] = 'X'
		}
	}
	for rel, want := range map[string]fs.FileMode{"SKILL.md": 0640, "default.txt": 0644, "scripts/run.sh": 0751, "unsafe": 0650, "empty": 0755} {
		if modes["skills/sample/"+rel] != want {
			t.Errorf("%s: %o", rel, modes["skills/sample/"+rel])
		}
	}
	if string(entries[0].Bytes) != "skill" || entries[3].Mode != fs.ModeSetuid|0672 {
		t.Fatal("input mutated")
	}
	found := false
	for _, d := range out.Diagnostics {
		if d.Code == "clamped_package_mode" && d.Entry == "skills/sample/unsafe" {
			found = d.Declared == fs.ModeSetuid|0672 && d.Applied == 0650
		}
	}
	if !found {
		t.Fatal("clamp diagnostic missing", out.Diagnostics)
	}
}
func TestDeterministicAssembly(t *testing.T) {
	req := request(runtimes.Claude)
	req.Inputs = []Input{{Resolved: resolved(t, runtimes.Claude, layout.Skills), Content: Content{Pin: Pin{"fixture", "revision"}, Package: artifact.Tree{Entries: []artifact.Entry{{Path: "SKILL.md", Kind: artifact.EntryFile, Mode: 0640, Bytes: []byte("skill")}}}}}, {Resolved: resolved(t, runtimes.Claude, layout.Instructions), Content: Content{Body: []byte("instructions")}}, {Resolved: resolved(t, runtimes.Claude, layout.MCP)}}
	first, err := Render(req)
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(req.Inputs)
	second, err := Render(req)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("input ordering changes assembly")
	}
}
func TestCodexAbsentAndDeclaredNativeDefaults(t *testing.T) {
	req := request(runtimes.Codex)
	req.Inputs = []Input{{Resolved: resolved(t, runtimes.Codex, layout.Settings)}}
	out, err := Render(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Diagnostics) != 1 || out.Diagnostics[0].Code != "posture_absent" || len(out.Tree.Entries[0].Bytes) != 0 {
		t.Fatalf("%#v", out)
	}
	req.Native.Codex.ApprovalPolicy = "never"
	req.Native.Codex.SandboxMode = "workspace-write"
	out, err = Render(req)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Tree.Entries[0].Bytes, []byte("approval_policy = \"never\"\nsandbox_mode = \"workspace-write\"\n")) {
		t.Fatalf("%q", out.Tree.Entries[0].Bytes)
	}
	if out.Diagnostics[0].Reason == "posture absent: no native policy emitted; runtime default applies" {
		t.Fatal("declared default incorrectly diagnosed")
	}
}
func TestCredentialPackageRefused(t *testing.T) {
	req := request(runtimes.Codex)
	req.Inputs = []Input{{Resolved: resolved(t, runtimes.Codex, layout.Skills), Content: Content{Pin: Pin{"fixture", "revision"}, Package: artifact.Tree{Entries: []artifact.Entry{{Path: "SKILL.md", Kind: artifact.EntryFile, Bytes: []byte("skill")}, {Path: "auth.json", Kind: artifact.EntryFile, Bytes: []byte("DUMMY-SENTINEL")}}}}}}
	if _, err := Render(req); err == nil {
		t.Fatal("credential managed inside package")
	}
}

func TestExplicitClaudePointer(t *testing.T) {
	req := request(runtimes.Claude)
	req.InstructionPointer = true
	req.Inputs = []Input{{Resolved: resolved(t, runtimes.Claude, layout.Instructions)}}
	if _, err := Render(req); err == nil {
		t.Fatal("pointer without neutral body accepted")
	}
	req.Inputs = append(req.Inputs, Input{Resolved: resolved(t, runtimes.Claude, layout.NeutralInstructions), Content: Content{Body: []byte("resolved instructions\n")}})
	out, err := Render(req)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, e := range out.Tree.Entries {
		files[e.Path] = string(e.Bytes)
	}
	if files["CLAUDE.md"] != "@AGENTS.md\n" || files["AGENTS.md"] != "resolved instructions\n" {
		t.Fatal(files)
	}
	req.Provider = runtimes.Codex
	if _, err := Render(req); err == nil {
		t.Fatal("provider-specific pointer accepted on Codex")
	}
}

func TestPosturePolicyMixtureRefusal(t *testing.T) {
	for _, p := range []runtimes.ID{runtimes.Claude, runtimes.Codex, runtimes.OpenCode} {
		req := request(p)
		r, err := layout.Resolve(layout.Request{Key: layout.Key{Provider: p, Layer: layout.Boot, Mode: req.Mode, Field: layout.Permissions}, Requirement: layout.Required, Posture: permission.ModePlan, LookupPosture: func(runtimes.ID, permission.Mode, runtimes.Mode) error { return nil }})
		if err != nil {
			t.Fatal(err)
		}
		req.Inputs = []Input{{Resolved: r}}
		switch p {
		case runtimes.Claude:
			req.Native.Claude.Slots = []contract.Slot{{Key: "permissions", Value: map[string]string{"defaultMode": "bypassPermissions"}}}
		case runtimes.Codex:
			req.Native.Codex.SandboxMode = "danger-full-access"
		case runtimes.OpenCode:
			req.Native.OpenCode.Slots = []contract.Slot{{Key: "permission", Value: "allow"}}
		}
		_, err = Render(req)
		var d *Diagnostic
		if !errors.As(err, &d) || d.Code != "host_policy_mixture" || d.Provider != p || d.Mode != req.Mode || d.Concern != layout.Permissions || d.Reason == "" {
			t.Fatalf("%s: %v", p, err)
		}
	}
}

func TestReservedDocumentsWithoutEmittedContent(t *testing.T) {
	for _, p := range []runtimes.ID{runtimes.Claude, runtimes.Codex, runtimes.OpenCode, runtimes.Antigravity} {
		req := request(p)
		rows, err := layout.For(p, req.Layer, req.Mode, req.Variant)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.Path == "" || row.Form == layout.Package || strings.Contains(row.Path, "{") {
				continue
			}
			req.Overlays = []artifact.Entry{{Path: "./" + row.Path, Kind: artifact.EntryFile, Bytes: []byte("DUMMY-SENTINEL")}}
			if _, err = Render(req); err == nil {
				t.Errorf("%s %s native path accepted without emitted row", p, row.Path)
			}
		}
	}
}
func TestResolutionCannotSpoofAgentOrLocator(t *testing.T) {
	req := request(runtimes.OpenCode)
	req.Inputs = []Input{{Resolved: resolved(t, runtimes.OpenCode, layout.Instructions), Content: Content{Body: []byte("instructions")}}}
	req.Agent = "other"
	if _, err := Render(req); err == nil {
		t.Fatal("agent name diverged from selection")
	}
	req.Agent = "fixture"
	req.Inputs[0].Resolved.Row.Locator.Argv = []string{"--unsafe"}
	if _, err := Render(req); err == nil {
		t.Fatal("locator spoof accepted")
	}
}
func TestMissingRootIsTyped(t *testing.T) {
	req := request(runtimes.Codex)
	req.Roots = nil
	req.Inputs = []Input{{Resolved: resolved(t, runtimes.Codex, layout.Settings)}}
	_, err := Render(req)
	var d *Diagnostic
	if !errors.As(err, &d) || d.Code != "missing_root" || d.Concern != layout.Settings {
		t.Fatalf("%v", err)
	}
}

func TestExplicitDirectoryMode(t *testing.T) {
	req := request(runtimes.Claude)
	req.Overlays = []artifact.Entry{{Path: "notes", Kind: artifact.EntryDirectory, Mode: 0750}}
	out, err := Render(req)
	if err != nil {
		t.Fatal(err)
	}
	if out.Tree.Entries[0].Mode != 0755 {
		t.Fatalf("directory mode %o", out.Tree.Entries[0].Mode)
	}
}
func TestRootsAreExplicitAbsoluteDirectories(t *testing.T) {
	for _, root := range []string{"relative", "/fixture/../escape", "/fixture\nsecret"} {
		req := request(runtimes.Codex)
		req.Roots[layout.RootBoot] = root
		req.Inputs = []Input{{Resolved: resolved(t, runtimes.Codex, layout.Settings)}}
		if _, err := Render(req); err == nil {
			t.Errorf("accepted root %q", root)
		}
	}
}

func TestNativeInputNeedsSelectedOwner(t *testing.T) {
	req := request(runtimes.Codex)
	req.Native.Codex.ApprovalPolicy = "never"
	if _, err := Render(req); err == nil {
		t.Fatal("native policy silently discarded without resolved config row")
	}
	req = request(runtimes.Claude)
	req.Inputs = []Input{{Resolved: resolved(t, runtimes.Claude, layout.Settings)}}
	req.Native.Codex.SandboxMode = "danger-full-access"
	if _, err := Render(req); err == nil {
		t.Fatal("foreign provider native inputs silently discarded")
	}
}
func TestPostureOwnershipCannotBeDisclaimed(t *testing.T) {
	req := request(runtimes.Codex)
	r, err := layout.Resolve(layout.Request{Key: layout.Key{Provider: req.Provider, Layer: req.Layer, Mode: req.Mode, Field: layout.Permissions}, Requirement: layout.Required, Posture: permission.ModePlan, LookupPosture: func(runtimes.ID, permission.Mode, runtimes.Mode) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	req.Inputs = []Input{{Resolved: r}}
	req.Native.OperatorKeyPaths = [][]string{{"sandbox_mode"}}
	if _, err = Render(req); err == nil {
		t.Fatal("bound native policy marked unowned")
	}
}

func TestOverlayCannotBlockReservedNativeParent(t *testing.T) {
	req := request(runtimes.Claude)
	req.Overlays = []artifact.Entry{{Path: ".claude", Kind: artifact.EntryFile, Bytes: []byte("DUMMY-SENTINEL")}}
	if _, err := Render(req); err == nil {
		t.Fatal("overlay blocks reserved native namespace when no row emitted")
	}
}
