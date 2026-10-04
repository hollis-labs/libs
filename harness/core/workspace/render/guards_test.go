package render

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	claude "github.com/hollis-labs/substrate/harness/adapters/claude/nativefiles"
	codex "github.com/hollis-labs/substrate/harness/adapters/codex/nativefiles"
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

func policyRequest(t *testing.T, p runtimes.ID) Request {
	t.Helper()
	req := request(p)
	res, err := layout.Resolve(layout.Request{Key: layout.Key{Provider: p, Layer: req.Layer, Mode: req.Mode, Field: layout.Permissions}, Requirement: layout.Required, Posture: permission.ModePlan, LookupPosture: func(runtimes.ID, permission.Mode, runtimes.Mode) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	req.Inputs = []Input{{Resolved: res}}
	return req
}
func TestEveryRowComparison(t *testing.T) {
	for _, tc := range []struct {
		name   string
		field  layout.Field
		mutate func(*layout.Row)
	}{
		{"posture", layout.Permissions, func(r *layout.Row) { r.Posture.Mapper = "forged" }},
		{"posture-provider", layout.Permissions, func(r *layout.Row) { r.Posture.Provider = runtimes.Codex }},
		{"missing-posture", layout.Permissions, func(r *layout.Row) { r.Posture = nil }},
		{"extra-posture", layout.MCP, func(r *layout.Row) { r.Posture = &layout.PostureReference{} }},
		{"modebits", layout.MCP, func(r *layout.Row) { r.ModeBits = 0666 }},
		{"mode", layout.MCP, func(r *layout.Row) { r.Mode = runtimes.ModePTY }},
		{"variant", layout.MCP, func(r *layout.Row) { r.Variant = layout.VariantBare }},
		{"root", layout.MCP, func(r *layout.Row) { r.Root = layout.RootHome }},
		{"renderer", layout.MCP, func(r *layout.Row) { r.Renderer = "forged" }},
		{"credential", layout.MCP, func(r *layout.Row) { r.CredentialPolicy = layout.LinkOnlyNeverWrite }},
		{"exclusive", layout.MCP, func(r *layout.Row) { r.ExclusiveMCP = layout.Unsupported }},
		{"composition", layout.MCP, func(r *layout.Row) { r.Composition = "forged" }},
		{"concern", layout.MCP, func(r *layout.Row) { r.Concern = "forged" }},
		{"slot", layout.MCP, func(r *layout.Row) { r.DocumentSlot = "forged" }},
		{"capability", layout.MCP, func(r *layout.Row) { r.Capability = layout.Unsupported }},
		{"form", layout.MCP, func(r *layout.Row) { r.Form = layout.Package }},
		{"path", layout.Skills, func(r *layout.Row) { r.Path = "another/SKILL.md" }},
		{"provider", layout.MCP, func(r *layout.Row) { r.Provider = runtimes.Codex }},
		{"layer", layout.MCP, func(r *layout.Row) { r.Layer = layout.Installed }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := request(runtimes.Claude)
			r := resolved(t, req.Provider, tc.field).Row
			tc.mutate(&r)
			if validateRow(req, r) == nil {
				t.Fatal("forged row accepted")
			}
		})
	}
}
func TestPackageGuards(t *testing.T) {
	base := func() Request {
		req := request(runtimes.Claude)
		req.Inputs = []Input{{Resolved: resolved(t, req.Provider, layout.Skills), Content: Content{Pin: Pin{"fixture", "revision"}, Package: artifact.Tree{Entries: []artifact.Entry{{Path: "SKILL.md", Kind: artifact.EntryFile, Bytes: []byte("skill")}}}}}}
		return req
	}
	for _, rel := range []string{"/absolute", "../escape", "a/../b", ".", "a\\b", "a/", "C:/x"} {
		t.Run(rel, func(t *testing.T) {
			req := base()
			req.Inputs[0].Content.Package.Entries = append(req.Inputs[0].Content.Package.Entries, artifact.Entry{Path: rel, Kind: artifact.EntryFile, Bytes: []byte{}})
			if _, err := Render(req); err == nil {
				t.Fatal("unsafe package")
			}
		})
	}
	for _, tc := range []struct {
		name   string
		change func(*Request)
	}{
		{"unpinned", func(r *Request) { r.Inputs[0].Content.Pin = Pin{} }},
		{"missing-main", func(r *Request) { r.Inputs[0].Content.Package.Entries[0].Path = "support" }},
		{"empty", func(r *Request) { r.Inputs[0].Content.Package.Entries = nil }},
		{"unresolved", func(r *Request) { r.Inputs[0].Content.Package.Entries[0].ContentRef = &artifact.ImmutableRef{} }},
		{"credential-auth", func(r *Request) {
			r.Inputs[0].Content.Package.Entries = append(r.Inputs[0].Content.Package.Entries, artifact.Entry{Path: "auth.json", Kind: artifact.EntryDirectory})
		}},
		{"credential-dot", func(r *Request) {
			r.Inputs[0].Content.Package.Entries = append(r.Inputs[0].Content.Package.Entries, artifact.Entry{Path: ".credentials.json", Kind: artifact.EntryDirectory})
		}},
		{"credential-oauth", func(r *Request) {
			r.Inputs[0].Content.Package.Entries = append(r.Inputs[0].Content.Package.Entries, artifact.Entry{Path: "oauth_creds.json", Kind: artifact.EntryDirectory})
		}},
		{"case-names", func(r *Request) {
			other := r.Inputs[0]
			other.Resolved.Row.Path = ".claude/skills/Sample/SKILL.md"
			r.Inputs = append(r.Inputs, other)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := base()
			tc.change(&req)
			if _, err := Render(req); err == nil {
				t.Fatal("guard missing")
			}
		})
	}
	req := base()
	req.Inputs[0].Content.Package.Entries = append(req.Inputs[0].Content.Package.Entries, artifact.Entry{Path: "z", Kind: artifact.EntryFile, Mode: 0777, Bytes: []byte{}}, artifact.Entry{Path: "a", Kind: artifact.EntryFile, Mode: 0666, Bytes: []byte{}})
	a, err := Render(req)
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(req.Inputs[0].Content.Package.Entries)
	b, err := Render(req)
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("package order", err)
	}
}
func TestPolicyMixtureClauses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		p      runtimes.ID
		change func(*Request)
	}{
		{"claude-operator", runtimes.Claude, func(r *Request) { r.Native.OperatorKeyPaths = [][]string{{"permissions", "defaultMode"}} }},
		{"opencode-operator", runtimes.OpenCode, func(r *Request) { r.Native.OperatorKeyPaths = [][]string{{"permission", "edit"}} }},
		{"codex-operator", runtimes.Codex, func(r *Request) { r.Native.OperatorKeyPaths = [][]string{{"approval_policy"}} }},
		{"claude-typed", runtimes.Claude, func(r *Request) { r.Native.Claude.Permission = &claude.Permission{DefaultMode: "default"} }},
		{"codex-approval", runtimes.Codex, func(r *Request) { r.Native.Codex.ApprovalPolicy = "never" }},
		{"codex-roots", runtimes.Codex, func(r *Request) { r.Native.Codex.WritableRoots = []string{"/fixture"} }},
		{"codex-sandbox", runtimes.Codex, func(r *Request) { r.Native.Codex.SandboxMode = "workspace-write" }},
		{"opencode-agent", runtimes.OpenCode, func(r *Request) { r.Native.OpenCode.Slots = []contract.Slot{{Key: "agent", Value: map[string]any{}}} }},
		{"claude-slot", runtimes.Claude, func(r *Request) {
			r.Native.Claude.Slots = []contract.Slot{{Key: "permissions", Value: map[string]any{}}}
		}},
		{"opencode-slot", runtimes.OpenCode, func(r *Request) { r.Native.OpenCode.Slots = []contract.Slot{{Key: "permission", Value: "allow"}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := policyRequest(t, tc.p)
			tc.change(&req)
			var d *Diagnostic
			if _, err := Render(req); !errors.As(err, &d) || d.Code != CodeHostPolicyMixture {
				t.Fatal(err)
			}
		})
	}
	for _, key := range []string{"approval_policy", "sandbox_mode", "sandbox_workspace_write", "profiles", "profiles.fixture.sandbox_mode", "profile", "default_permissions", "permissions", "permissions.edit"} {
		t.Run(key, func(t *testing.T) {
			req := policyRequest(t, runtimes.Codex)
			req.Native.Codex.Slots = []contract.Slot{{Key: key, Value: "danger-full-access"}}
			var d *Diagnostic
			if _, err := Render(req); !errors.As(err, &d) || d.Code != CodeHostPolicyMixture {
				t.Fatal(err)
			}
		})
	}
	req := policyRequest(t, runtimes.Claude)
	req.Inputs[0].Resolved.Row.Posture.Posture = "unknown"
	if _, err := Render(req); err == nil {
		t.Fatal("invalid posture")
	}
}
func TestRemainingContracts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		p      runtimes.ID
		change func(*Request)
	}{
		{"foreign-claude", runtimes.OpenCode, func(r *Request) { r.Native.Claude.Slots = []contract.Slot{{Key: "operator", Value: true}} }},
		{"foreign-opencode", runtimes.Claude, func(r *Request) { r.Native.OpenCode.Slots = []contract.Slot{{Key: "operator", Value: true}} }},
		{"unresolved-mcp", runtimes.Claude, func(r *Request) { r.Native.Servers = []contract.Server{{Name: "fixture", Command: "fixture"}} }},
		{"unknown-credentials", runtimes.Claude, func(r *Request) { r.Credentials = "unknown" }},
		{"unresolved-overlay", runtimes.Claude, func(r *Request) {
			r.Overlays = []artifact.Entry{{Path: "notes", Kind: artifact.EntryFile, ContentRef: &artifact.ImmutableRef{}}}
		}},
		{"effect-content", runtimes.Codex, func(r *Request) {
			r.Inputs = []Input{{Resolved: resolved(t, r.Provider, layout.Credentials), Content: Content{Body: []byte("body")}}}
		}},
		{"effect-form", runtimes.Codex, func(r *Request) {
			r.Inputs = []Input{{Resolved: resolved(t, r.Provider, layout.Credentials)}}
			r.Inputs[0].Resolved.Effects[0].Form = layout.File
		}},
		{"empty-operator", runtimes.Claude, func(r *Request) {
			r.Inputs = []Input{{Resolved: resolved(t, r.Provider, layout.Settings)}}
			r.Native.OperatorKeyPaths = [][]string{{}}
		}},
		{"utf8-operator", runtimes.Claude, func(r *Request) {
			r.Inputs = []Input{{Resolved: resolved(t, r.Provider, layout.Settings)}}
			r.Native.OperatorKeyPaths = [][]string{{string([]byte{255})}}
		}},
		{"boot-encoding", runtimes.Codex, func(r *Request) {
			r.Inputs = []Input{{Resolved: resolved(t, r.Provider, layout.Settings)}}
			r.Native.Codex.Encoding = codex.InstalledEncoding
			r.Native.Codex.ApprovalPolicy = "never"
			r.Native.Codex.SandboxMode = "workspace-write"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := request(tc.p)
			tc.change(&req)
			_, err := Render(req)
			if err == nil {
				t.Fatal("guard missing")
			}
			expected := map[string]DiagnosticCode{"foreign-claude": CodeProviderInputMismatch, "foreign-opencode": CodeProviderInputMismatch, "unresolved-mcp": CodeUnresolvedInput, "unknown-credentials": CodeInvalidCredentials, "unresolved-overlay": CodeUnresolvedContent, "effect-content": CodeInvalidResolution, "effect-form": CodeInvalidResolution, "empty-operator": CodeInvalidOwnership, "boot-encoding": CodeInvalidEncoding}
			if code, ok := expected[tc.name]; ok {
				var d *Diagnostic
				if !errors.As(err, &d) || d.Code != code {
					t.Fatal("wrong refusal", err)
				}
			}
		})
	}
	for _, name := range []string{"", "space name", "../escape"} {
		req := request(runtimes.Claude)
		req.Layer = layout.Installed
		req.Mode = layout.InstallMode
		req.DefinitionName = name
		row, err := layout.Resolve(layout.Request{Key: layout.Key{Provider: req.Provider, Layer: req.Layer, Mode: req.Mode, Field: layout.Instructions}, Requirement: layout.Required})
		if err != nil {
			t.Fatal(err)
		}
		req.Inputs = []Input{{Resolved: row, Content: Content{Body: []byte("body")}}}
		if _, err = Render(req); err == nil {
			t.Fatal("unsafe definition")
		}
	}
	req := request(runtimes.Claude)
	req.Layer = layout.Installed
	req.Mode = layout.InstallMode
	req.DefinitionName = "fixture"
	row, _ := layout.Resolve(layout.Request{Key: layout.Key{Provider: req.Provider, Layer: req.Layer, Mode: req.Mode, Field: layout.Instructions}, Requirement: layout.Required})
	req.Inputs = []Input{{Resolved: row, Content: Content{Body: []byte{}}}}
	out, err := Render(req)
	if err != nil || len(out.Tree.Entries) != 0 || len(out.Diagnostics) != 1 || out.Diagnostics[0].Code != CodeOmittedEmptyInstalledInstructions || out.Diagnostics[0].Class != ClassOmission {
		t.Fatal(out, err)
	}
	req = request(runtimes.Claude)
	req.Overlays = make([]artifact.Entry, MaxTreeEntries+1)
	if _, err = Render(req); err == nil {
		t.Fatal("unbounded input")
	}
}
func TestConsumerMetadata(t *testing.T) {
	req := request(runtimes.Claude)
	req.Inputs = []Input{{Resolved: resolved(t, req.Provider, layout.MCP)}}
	req.Native.Servers = []contract.Server{{Name: "fixture", Command: "fixture", Env: []contract.Variable{{Name: "A", Value: "a"}, {Name: "B", Value: "b"}, {Name: "C", Value: "c"}}}}
	out, err := Render(req)
	if err != nil {
		t.Fatal(err)
	}
	if out.Provider != req.Provider || out.Layer != req.Layer || out.Mode != req.Mode || out.Variant != req.Variant {
		t.Fatal("lost target")
	}
	var ownership DocumentOwnership
	for _, e := range out.Tree.Entries {
		if e.Kind == artifact.EntryFile {
			ownership, err = ParseOwnershipNote(e.Provenance.Note)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, name := range []string{"A", "B", "C"} {
		want := []string{"mcpServers", "fixture", "env", name}
		found := false
		for _, p := range ownership.OwnedKeyPaths {
			found = found || slices.Equal(p, want)
		}
		if !found {
			t.Fatal("ownership clone corrupted", ownership)
		}
	}
	for _, note := range []string{"", "{}", strings.ReplaceAll(`{"schema":"native-key-ownership.v1","owned_key_paths":[],"reserved_slots":[]}`, "v1", "v2"), `{"schema":"native-key-ownership.v1","owned_key_paths":[],"reserved_slots":[]}{}`} {
		if _, err = ParseOwnershipNote(note); err == nil {
			t.Fatal("invalid note")
		}
	}
	raw, _ := json.Marshal(out)
	var decoded Result
	secondErr := json.Unmarshal(raw, &decoded)
	second, _ := json.Marshal(decoded)
	if secondErr != nil || !bytes.Equal(second, raw) {
		t.Fatal("populated Result JSON roundtrip")
	}
}
func TestDirectoryModeDiagnostics(t *testing.T) {
	req := request(runtimes.Claude)
	req.Overlays = []artifact.Entry{{Path: "notes", Kind: artifact.EntryDirectory, Mode: 0750}}
	out, err := Render(req)
	if err != nil || len(out.Diagnostics) != 1 || out.Diagnostics[0].Code != CodeNormalizedOverlayDirectoryMode || out.Diagnostics[0].Applied != 0755 {
		t.Fatal(out, err)
	}
	req.Inputs = []Input{{Resolved: resolved(t, req.Provider, layout.Skills), Content: Content{Pin: Pin{"fixture", "revision"}, Package: artifact.Tree{Entries: []artifact.Entry{{Path: "SKILL.md", Kind: artifact.EntryFile, Bytes: []byte{}}, {Path: "empty", Kind: artifact.EntryDirectory, Mode: 0750}}}}}}
	out, err = Render(req)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range out.Diagnostics {
		found = found || d.Code == CodeNormalizedPackageDirectoryMode && d.Declared == fs.FileMode(0750)
	}
	if !found {
		t.Fatal(out.Diagnostics)
	}
}

func TestExpandedBoundsAndFoldedParents(t *testing.T) {
	req := request(runtimes.Claude)
	for i := 0; i < 80; i++ {
		req.Overlays = append(req.Overlays, artifact.Entry{Path: fmt.Sprintf("branch%d/", i) + strings.Repeat("a/", 62) + "file", Kind: artifact.EntryFile, Bytes: []byte{}})
	}
	var d *Diagnostic
	if _, err := Render(req); !errors.As(err, &d) || d.Code != CodeInputLimit {
		t.Fatal("expanded limit", err)
	}
	req = request(runtimes.Claude)
	req.Overlays = []artifact.Entry{{Path: "Notes/a", Kind: artifact.EntryFile, Bytes: []byte{}}, {Path: "notes/b", Kind: artifact.EntryFile, Bytes: []byte{}}}
	if _, err := Render(req); err == nil {
		t.Fatal("folded synthesized parent")
	}
	valid := strings.Repeat("a", 240)
	rel := strings.Repeat(valid+"/", 16) + valid
	if len(rel) != MaxPathBytes || ValidateRelPath(rel) != nil || ValidateRelPath(rel+"a") == nil {
		t.Fatal("total path bound")
	}
	for _, pin := range []Pin{{"source", string([]byte{255})}, {string([]byte{255}), "revision"}} {
		req = request(runtimes.Claude)
		req.Inputs = []Input{{Resolved: resolved(t, req.Provider, layout.Skills), Content: Content{Pin: pin, Package: artifact.Tree{Entries: []artifact.Entry{{Path: "SKILL.md", Kind: artifact.EntryFile, Bytes: []byte{}}}}}}}
		if _, err := Render(req); err == nil {
			t.Fatal("invalid package pin")
		}
	}
	req = request(runtimes.Codex)
	r := resolved(t, runtimes.Codex, layout.MCP).Row
	req.Inputs = []Input{{Resolved: layout.Resolution{Effects: []layout.Row{r}}}}
	if _, err := Render(req); !errors.As(err, &d) || d.Code != CodeInvalidEffect {
		t.Fatal("non-link effect", err)
	}
}
