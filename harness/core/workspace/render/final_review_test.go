package render

import (
	"errors"
	"fmt"
	layout "github.com/hollis-labs/substrate/harness/adapters/layout/plan"
	contract "github.com/hollis-labs/substrate/harness/adapters/nativefiles"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
	"io/fs"
	"strings"
	"testing"
)

func TestLocatorAgentsValidatedWithoutInstructions(t *testing.T) {
	for _, mode := range []runtimes.Mode{runtimes.ModeSubprocessPerTurn, runtimes.ModeHTTPSSE} {
		for _, agent := range []string{"", "../evil", "--dir=/fixture", "a b", "-x", "a/b"} {
			req := request(runtimes.OpenCode)
			req.Mode = mode
			req.Agent = agent
			row, err := layout.Resolve(layout.Request{Key: layout.Key{Provider: req.Provider, Layer: req.Layer, Mode: mode, Field: layout.Kickoff}, Requirement: layout.Required})
			if err != nil {
				t.Fatal(err)
			}
			req.Inputs = []Input{{Resolved: row, Content: Content{Body: []byte("kickoff")}}}
			var d *Diagnostic
			if _, err = Render(req); !errors.As(err, &d) || d.Code != CodeInvalidComponent {
				t.Errorf("%s/%q: %v", mode, agent, err)
			}
		}
	}
}
func TestDirectoryDefaultsAndTypeRefusalEntry(t *testing.T) {
	for _, mode := range []fs.FileMode{0, 0755, fs.ModeDir | 0755} {
		req := request(runtimes.Claude)
		req.Overlays = []artifact.Entry{{Path: "notes", Kind: artifact.EntryDirectory, Mode: mode, Digest: artifact.Digest{Algorithm: "sha256", Hex: "forged"}}}
		out, err := Render(req)
		if err != nil || len(out.Diagnostics) != 0 {
			t.Errorf("%o: %v %v", mode, out.Diagnostics, err)
		}
		for _, e := range out.Tree.Entries {
			if e.Digest != (artifact.Digest{}) {
				t.Fatal("directory digest retained")
			}
		}
	}
	for _, kind := range []artifact.EntryKind{artifact.EntryFile, artifact.EntryDirectory} {
		for _, mode := range []fs.FileMode{fs.ModeSymlink, fs.ModeNamedPipe, fs.ModeSocket, fs.ModeDevice, fs.ModeCharDevice, fs.ModeIrregular, fs.ModeDir} {
			if kind == artifact.EntryDirectory && mode == fs.ModeDir {
				continue
			}
			req := request(runtimes.Claude)
			e := artifact.Entry{Path: "notes", Kind: kind, Mode: mode | 0644}
			if kind == artifact.EntryFile {
				e.Bytes = []byte("body")
			}
			req.Overlays = []artifact.Entry{e}
			var d *Diagnostic
			if _, err := Render(req); !errors.As(err, &d) || d.Code != CodeInvalidEntryType || d.Entry != "notes" {
				t.Errorf("%s/%o: %v", kind, mode, err)
			}
			if kind == artifact.EntryFile {
				req.Overlays = nil
				e.Path = "SKILL.md"
				req.Inputs = []Input{{Resolved: resolved(t, req.Provider, layout.Skills), Content: Content{Pin: Pin{"fixture", "revision"}, Package: artifact.Tree{Entries: []artifact.Entry{e}}}}}
				if _, err := Render(req); !errors.As(err, &d) || d.Code != CodeInvalidEntryType || d.Entry != ".claude/skills/sample/SKILL.md" {
					t.Errorf("package/%o: %v", mode, err)
				}
			}
		}
	}
}
func TestOwnershipNoteExactKeys(t *testing.T) {
	for _, note := range []string{
		`{"Schema":"native-key-ownership.v1","owned_key_paths":[],"reserved_slots":[]}`,
		`{"schema":"native-key-ownership.v1","OWNED_KEY_PATHS":[],"reserved_slots":[]}`,
		`{"schema":"native-key-ownership.v1","owned_key_paths":[],"RESERVED_SLOTS":[]}`,
	} {
		if _, err := ParseOwnershipNote(note); !errors.Is(err, ErrInvalidOwnershipNote) {
			t.Error("case key accepted", note)
		}
	}
}
func TestHalfDeclaredHeadlessDefault(t *testing.T) {
	for _, mode := range []runtimes.Mode{runtimes.ModeSubprocessPerTurn, runtimes.ModeJSONRPCStdio} {
		for _, tc := range []struct {
			name   string
			change func(*Request)
		}{
			{"approval", func(r *Request) { r.Native.Codex.ApprovalPolicy = "never" }},
			{"sandbox", func(r *Request) { r.Native.Codex.SandboxMode = "workspace-write" }},
			{"approval-slot", func(r *Request) { r.Native.Codex.Slots = []contract.Slot{{Key: "approval_policy", Value: "never"}} }},
			{"sandbox-slot", func(r *Request) {
				r.Native.Codex.Slots = []contract.Slot{{Key: "sandbox_mode", Value: "workspace-write"}}
			}},
		} {
			req := request(runtimes.Codex)
			req.Mode = mode
			row, err := layout.Resolve(layout.Request{Key: layout.Key{Provider: req.Provider, Layer: req.Layer, Mode: mode, Field: layout.Settings}, Requirement: layout.Required})
			if err != nil {
				t.Fatal(err)
			}
			req.Inputs = []Input{{Resolved: row}}
			tc.change(&req)
			var d *Diagnostic
			if _, err = Render(req); !errors.As(err, &d) || d.Code != CodePostureRequired {
				t.Errorf("%s/%s: %v", mode, tc.name, err)
			}
		}
	}
}
func TestFinalPathAndTargetContext(t *testing.T) {
	req := request(runtimes.Claude)
	req.Inputs = []Input{{Resolved: resolved(t, req.Provider, layout.Skills), Content: Content{Pin: Pin{"fixture", "revision"}, Package: artifact.Tree{Entries: []artifact.Entry{{Path: "SKILL.md", Kind: artifact.EntryFile, Bytes: []byte{}}, {Path: strings.Repeat("a/", 61) + "file", Kind: artifact.EntryFile, Bytes: []byte("body")}}}}}}
	var d *Diagnostic
	if _, err := Render(req); !errors.As(err, &d) || d.Code != CodeUnsafePath {
		t.Fatal("prefixed depth", err)
	}
	req = request(runtimes.Claude)
	req.Layer = layout.Installed
	req.Mode = layout.InstallMode
	out, err := Render(req)
	if err != nil || out.Layer != layout.Installed || out.RootMode != 0 {
		t.Fatal("installed context", out, err)
	}
	req = request(runtimes.Claude)
	req.Variant = layout.VariantBare
	out, err = Render(req)
	if err != nil || out.Variant != layout.VariantBare {
		t.Fatal("variant context", out, err)
	}
	for _, value := range []string{"/fixture/a\troot", "/fixture/a\x7froot", "/fixture/a\u202eroot"} {
		req = request(runtimes.Claude)
		req.Roots[layout.RootBoot] = value
		if _, err := Render(req); !errors.As(err, &d) || d.Code != CodeUnsafeRoot {
			t.Fatal("root text", err)
		}
	}
	for _, r := range []rune{0x202a, 0x202e, 0x2066, 0x2069} {
		if ValidateRelPath("notes/a"+string(r)+"b") == nil {
			t.Errorf("bidi boundary %x", r)
		}
	}
	if ValidateComponent(strings.Repeat("a", 255)) != nil || ValidateComponent(strings.Repeat("a", 256)) == nil {
		t.Fatal("component bound")
	}
	req = request(runtimes.Claude)
	req.Overlays = make([]artifact.Entry, MaxTreeEntries+1)
	if _, err := Render(req); !errors.As(err, &d) || d.Code != CodeInputLimit {
		t.Fatal("initial limit", err)
	}
	for _, note := range []string{
		`{"schema":"native-key-ownership.v1","owned_key_paths":null,"reserved_slots":[]}`,
		`{"schema":"native-key-ownership.v1","owned_key_paths":[],"reserved_slots":null}`,
		`{"schema":"native-key-ownership.v1","owned_key_paths":[],"reserved_slots":[],"unknown":true}`,
		string([]byte{255}),
	} {
		if _, err := ParseOwnershipNote(note); !errors.Is(err, ErrInvalidOwnershipNote) {
			t.Fatal("parser accepted", note)
		}
	}
}

func TestParentWalkBoundAndPackageDefault(t *testing.T) {
	req := request(runtimes.Claude)
	prefix := strings.Repeat("parent/", 60)
	entries := make([]artifact.Entry, 4000)
	files := map[string]artifact.Entry{}
	for i := range entries {
		entries[i] = artifact.Entry{Path: fmt.Sprintf("%sfile%d", prefix, i), Kind: artifact.EntryFile, Bytes: []byte("body")}
		files[contract.FoldPath(entries[i].Path)] = entries[i]
	}
	steps, err := synthesizeDirectories(req, entries, files)
	if err != nil || steps > len(entries)+60 || len(files) != len(entries)+60 {
		t.Fatal("parent synthesis work", steps, len(files), err)
	}
	// The table currently defaults packages to 0644. The helper takes that row's
	// default rather than introducing an independent permission authoring home.
	row := resolved(t, req.Provider, layout.Skills).Row
	if row.ModeBits != layout.FileMode {
		t.Fatal("published package default")
	}
	row.ModeBits = 0640
	content := Content{Pin: Pin{"fixture", "revision"}, Package: artifact.Tree{Entries: []artifact.Entry{{Path: "SKILL.md", Kind: artifact.EntryFile, Bytes: []byte("body")}}}}
	got, _, err := packageEntries(req, row, content)
	if err != nil || len(got) != 1 || got[0].Mode != 0640 {
		t.Fatal("row default ignored", got, err)
	}
	for _, mode := range []fs.FileMode{0, 0755, fs.ModeDir | 0755} {
		row.ModeBits = layout.FileMode
		content.Package.Entries = append(content.Package.Entries[:1], artifact.Entry{Path: "empty", Kind: artifact.EntryDirectory, Mode: mode})
		_, diags, err := packageEntries(req, row, content)
		if err != nil || len(diags) != 0 {
			t.Fatal("package directory default", mode, diags, err)
		}
	}
}

func TestSpecialBitNormalizationDiagnostics(t *testing.T) {
	for _, special := range []fs.FileMode{fs.ModeSetuid, fs.ModeSetgid, fs.ModeSticky, 04000, 02000, 01000} {
		for _, pkg := range []bool{false, true} {
			for _, kind := range []artifact.EntryKind{artifact.EntryFile, artifact.EntryDirectory} {
				descriptive := []fs.FileMode{0}
				if kind == artifact.EntryDirectory {
					descriptive = append(descriptive, fs.ModeDir)
				}
				for _, typeBit := range descriptive {
					t.Run(fmt.Sprintf("%o/package=%t/%s/type=%o", special, pkg, kind, typeBit), func(t *testing.T) {
						req := request(runtimes.Claude)
						path := "notes"
						declared := special | typeBit | 0755
						entry := artifact.Entry{Path: path, Kind: kind, Mode: declared}
						if kind == artifact.EntryFile {
							entry.Bytes = []byte("body")
						}
						wantCode := CodeClampedOverlayMode
						if kind == artifact.EntryDirectory {
							wantCode = CodeNormalizedOverlayDirectoryMode
						}
						if pkg {
							if kind == artifact.EntryDirectory {
								wantCode = CodeNormalizedPackageDirectoryMode
							} else {
								wantCode = CodeClampedPackageMode
							}
							req.Inputs = []Input{{Resolved: resolved(t, req.Provider, layout.Skills), Content: Content{Pin: Pin{"fixture", "revision"}, Package: artifact.Tree{Entries: []artifact.Entry{{Path: "SKILL.md", Kind: artifact.EntryFile, Bytes: []byte{}}, entry}}}}}
							path = ".claude/skills/sample/notes"
						} else {
							req.Overlays = []artifact.Entry{entry}
						}
						out, err := Render(req)
						if err != nil {
							t.Fatal(err)
						}
						if len(out.Diagnostics) != 1 {
							t.Fatalf("expected normalization diagnostic: %v", out.Diagnostics)
						}
						d := out.Diagnostics[0]
						if d.Class != ClassInformational || d.Code != wantCode || d.Provider != req.Provider || d.Mode != req.Mode || d.Entry != path || d.Declared != declared || d.Applied != 0755 {
							t.Fatalf("incorrect diagnostic: %+v", d)
						}
						found := false
						for _, applied := range out.Tree.Entries {
							if applied.Path == path {
								found = true
								if applied.Mode != 0755 {
									t.Fatalf("special bits retained: %o", applied.Mode)
								}
							}
						}
						if !found {
							t.Fatal("entry missing")
						}
					})
				}
			}
		}
	}
}
