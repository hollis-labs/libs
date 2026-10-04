package render

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	layout "github.com/hollis-labs/substrate/harness/adapters/layout/plan"
	contract "github.com/hollis-labs/substrate/harness/adapters/nativefiles"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
	"io/fs"
	"strings"
	"testing"
)

func TestOverlayTrustBoundary(t *testing.T) {
	for _, tc := range []struct{ mode, want fs.FileMode }{{0, 0644}, {0666, 0644}, {0777, 0755}, {fs.ModeSetuid | fs.ModeSticky | 0751, 0751}, {fs.ModeSetuid, 0644}} {
		req := request(runtimes.Claude)
		req.Overlays = []artifact.Entry{{Path: "notes/ref", Kind: artifact.EntryFile, Mode: tc.mode, Bytes: []byte("body"), Digest: artifact.Digest{Algorithm: "sha256", Hex: "forged"}, Provenance: artifact.Provenance{Note: "forged", Source: "forged"}}}
		out, err := Render(req)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range out.Tree.Entries {
			if e.Kind != artifact.EntryFile {
				continue
			}
			sum := sha256.Sum256(e.Bytes)
			if e.Mode != tc.want || e.Digest.Hex != hex.EncodeToString(sum[:]) || e.Provenance.Note == "forged" || e.Provenance.Source == "forged" {
				t.Errorf("mode/digest/provenance: %#v", e)
			}
			if tc.mode != 0 && tc.mode != tc.want && (len(out.Diagnostics) != 1 || out.Diagnostics[0].Declared != tc.mode || out.Diagnostics[0].Applied != tc.want) {
				t.Error(out.Diagnostics)
			}
		}
	}
	req := request(runtimes.Claude)
	req.Overlays = []artifact.Entry{{Path: "notes", Kind: artifact.EntryFile, Mode: fs.ModeSymlink | 0644}}
	if _, err := Render(req); err == nil {
		t.Fatal("nonregular accepted")
	}
}
func TestPackageForgedDigestAndNote(t *testing.T) {
	req := request(runtimes.Claude)
	req.Inputs = []Input{{Resolved: resolved(t, req.Provider, layout.Skills), Content: Content{Pin: Pin{"fixture", "revision"}, Package: artifact.Tree{Entries: []artifact.Entry{{Path: "SKILL.md", Kind: artifact.EntryFile, Bytes: []byte("body"), Digest: artifact.Digest{Algorithm: "sha256", Hex: "forged"}, Provenance: artifact.Provenance{Note: "forged"}}}}}}}
	out, err := renderWithTestPosture(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range out.Tree.Entries {
		if e.Kind == artifact.EntryFile {
			sum := sha256.Sum256(e.Bytes)
			if e.Digest.Hex != hex.EncodeToString(sum[:]) || e.Provenance.Note == "forged" {
				t.Fatal(e)
			}
		}
	}
}
func TestCaseAndCredentialAliases(t *testing.T) {
	for _, rel := range []string{"Auth.json", "AUTH.JSON", ".Credentials.json", "oauth_creds.json/x", "auth.json/x", ".credentials.json/x", ".Git/config", ".SSH/key", ".ſsh/key", ".Materialize/manifest.json", "claude.MD", "agents.MD"} {
		req := request(runtimes.Claude)
		req.Overlays = []artifact.Entry{{Path: rel, Kind: artifact.EntryFile, Bytes: []byte{}}}
		if _, err := Render(req); err == nil {
			t.Error("alias accepted", rel)
		}
	}
	req := request(runtimes.Codex)
	req.Overlays = []artifact.Entry{{Path: "Config.toml", Kind: artifact.EntryFile, Bytes: []byte{}}}
	if _, err := Render(req); err == nil {
		t.Error("native alias accepted")
	}
	req = request(runtimes.Claude)
	req.Overlays = []artifact.Entry{{Path: "Ref.md", Kind: artifact.EntryFile, Bytes: []byte{}}, {Path: "ref.md", Kind: artifact.EntryFile, Bytes: []byte{}}}
	if _, err := Render(req); err == nil {
		t.Error("case collision accepted")
	}
}
func TestOperatorPathsFailClosed(t *testing.T) {
	for _, parts := range [][][]string{{{"permissions.defaultMode"}}, {{"permissions"}}, {{"absent"}}, {{"permissions", "defaultMode"}, {"permissions", "defaultMode", "x"}}} {
		req := request(runtimes.Claude)
		req.Inputs = []Input{{Resolved: resolved(t, req.Provider, layout.Settings)}}
		req.Native.Claude.Slots = []contract.Slot{{Key: "permissions", Value: map[string]string{"defaultMode": "default"}}}
		req.Native.OperatorKeyPaths = parts
		if _, err := Render(req); err == nil {
			t.Error("accepted", parts)
		}
	}
}
func TestEffectsDetached(t *testing.T) {
	req := request(runtimes.Codex)
	req.Inputs = []Input{{Resolved: resolved(t, req.Provider, layout.Credentials)}}
	req.Inputs[0].Resolved.Effects[0].Evidence.Observations = []string{"original"}
	out, err := renderWithTestPosture(req)
	if err != nil {
		t.Fatal(err)
	}
	out.Effects[0].Evidence.Observations[0] = "changed"
	if req.Inputs[0].Resolved.Effects[0].Evidence.Observations[0] != "original" {
		t.Fatal("alias")
	}
}
func TestHeadlessRequiresPosture(t *testing.T) {
	for _, mode := range []runtimes.Mode{runtimes.ModeSubprocessPerTurn, runtimes.ModeJSONRPCStdio} {
		req := request(runtimes.Codex)
		req.Mode = mode
		res, err := layout.Resolve(layout.Request{Key: layout.Key{Provider: req.Provider, Layer: req.Layer, Mode: mode, Field: layout.Settings}, Requirement: layout.Required})
		if err != nil {
			t.Fatal(err)
		}
		req.Inputs = []Input{{Resolved: res}}
		var d *Diagnostic
		if _, err = Render(req); !errors.As(err, &d) || d.Code != CodePostureRequired || d.Class != ClassRefusal {
			t.Fatalf("%s: %v", mode, err)
		}
		req.Native.Codex.Slots = []contract.Slot{{Key: "approval_policy", Value: json.RawMessage(`"never"`)}, {Key: "sandbox_mode", Value: json.RawMessage(`"workspace-write"`)}}
		if _, err = Render(req); err != nil {
			t.Fatal("explicit raw host default", err)
		}
	}
}
func TestPathBoundsAndRoots(t *testing.T) {
	for _, rel := range []string{"a\tfile", "a\x7ffile", "a\u202efile", string([]byte{255}), strings.Repeat("a", 256), strings.Repeat("a/", 65) + "b", strings.Repeat("a/", 2048) + "b"} {
		if ValidateRelPath(rel) == nil {
			t.Error("accepted path length", len(rel))
		}
	}
	for _, root := range []string{"/", "/fixture/project"} {
		req := request(runtimes.Claude)
		req.Roots[layout.RootBoot] = root
		if _, err := Render(req); err == nil {
			t.Error("root", root)
		}
	}
	req := request(runtimes.Claude)
	req.Agent = ""
	if _, err := Render(req); err != nil {
		t.Error("unused agent", err)
	}
	req = request(runtimes.Claude)
	req.Inputs = []Input{{Resolved: resolved(t, req.Provider, layout.Commands), Content: Content{Body: []byte("body"), Pin: Pin{string([]byte{255}), "revision"}}}}
	if _, err := Render(req); err == nil {
		t.Error("pin accepted")
	}
}
func TestPointerCannotDiscardBody(t *testing.T) {
	req := request(runtimes.Claude)
	req.InstructionPointer = true
	req.Inputs = []Input{{Resolved: resolved(t, req.Provider, layout.Instructions), Content: Content{Body: []byte("discarded")}}, {Resolved: resolved(t, req.Provider, layout.NeutralInstructions), Content: Content{Body: []byte("actual")}}}
	if _, err := Render(req); err == nil {
		t.Fatal("silently discarded")
	}
}
