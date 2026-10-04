package render

import (
	"errors"
	layout "github.com/hollis-labs/substrate/harness/adapters/layout/plan"
	contract "github.com/hollis-labs/substrate/harness/adapters/nativefiles"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
	"testing"
)

func resolved(t *testing.T, p runtimes.ID, f layout.Field) layout.Resolution {
	t.Helper()
	r, err := layout.Resolve(layout.Request{Key: layout.Key{Provider: p, Layer: layout.Boot, Mode: runtimes.ModeSubprocessPerTurn, Field: f}, Requirement: layout.Required, Components: map[string]string{"agent": "fixture", "name": "sample"}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func request(p runtimes.ID) Request {
	return Request{Provider: p, Layer: layout.Boot, Mode: runtimes.ModeSubprocessPerTurn, Agent: "fixture", Roots: map[layout.Root]string{layout.RootBoot: "/fixture/boot", layout.RootProject: "/fixture/project", layout.RootHome: "/fixture/home"}}
}
func TestComposeOneNativeDocument(t *testing.T) {
	req := request(runtimes.Codex)
	req.Inputs = []Input{{Resolved: resolved(t, runtimes.Codex, layout.Settings)}, {Resolved: resolved(t, runtimes.Codex, layout.MCP)}, {Resolved: resolved(t, runtimes.Codex, layout.Instructions), Content: Content{Body: []byte("instructions\n")}}}
	req.Native.Servers = []contract.Server{{Name: "fixture", Command: "fixture-mcp"}}
	result, err := Render(req)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]artifact.Entry{}
	for _, e := range result.Tree.Entries {
		if e.Kind == artifact.EntryFile {
			files[e.Path] = e
		}
	}
	if files["config.toml"].Mode != 0600 || string(files["AGENTS.md"].Bytes) != "instructions\n" {
		t.Fatalf("%#v", files)
	}
	if result.Binding.Environment["CODEX_HOME"] != "/fixture/boot" {
		t.Fatalf("%#v", result.Binding)
	}
	req.Overlays = []artifact.Entry{{Path: "config.toml", Kind: artifact.EntryFile, Bytes: []byte("replace")}}
	if _, err = Render(req); err == nil {
		t.Fatal("overlay replaced native document")
	}
}
func TestCredentialEffectsAndMissingCredentialRefusals(t *testing.T) {
	req := request(runtimes.Codex)
	r := resolved(t, runtimes.Codex, layout.Credentials)
	req.Inputs = []Input{{Resolved: r}}
	req.Credentials = CredentialAvailable
	result, err := Render(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Effects) != 1 {
		t.Fatalf("%#v", result.Effects)
	}
	for _, e := range result.Tree.Entries {
		if e.Path == "auth.json" {
			t.Fatal("credential became managed file")
		}
	}
	for _, status := range []CredentialAvailability{CredentialMissing, CredentialDenied} {
		req.Credentials = status
		var d *Diagnostic
		if _, err = Render(req); !errors.As(err, &d) {
			t.Fatalf("%v", err)
		}
	}
	req = request(runtimes.Codex)
	req.Overlays = []artifact.Entry{{Path: "auth.json", Kind: artifact.EntryFile, Bytes: []byte("DUMMY-SENTINEL")}}
	if _, err = Render(req); err == nil {
		t.Fatal("overlay creates credentials")
	}
}
func TestPinnedRegistrationsAndRefusals(t *testing.T) {
	req := request(runtimes.Claude)
	req.Inputs = []Input{{Resolved: resolved(t, runtimes.Claude, layout.Commands), Content: Content{Body: []byte("command\n")}}}
	if _, err := Render(req); err == nil {
		t.Fatal("unpinned command accepted")
	}
	req.Inputs[0].Content.Pin = Pin{Source: "fixture-resource", Revision: "fixture-revision"}
	result, err := Render(req)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range result.Tree.Entries {
		if e.Path == ".claude/commands/boot/sample.md" {
			found = true
			if e.Provenance.Revision != "fixture-revision" {
				t.Fatal("lost pin")
			}
		}
	}
	if !found {
		t.Fatal("command missing")
	}
	req.Inputs = append(req.Inputs, req.Inputs[0])
	if _, err = Render(req); err == nil {
		t.Fatal("same-path duplicate without selected content accepted")
	}
}
