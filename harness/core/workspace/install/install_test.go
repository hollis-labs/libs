package install_test

import (
	"bytes"
	"encoding/json"
	"errors"
	layout "github.com/hollis-labs/substrate/harness/adapters/layout/plan"
	"github.com/hollis-labs/substrate/harness/workspace/effects"
	"github.com/hollis-labs/substrate/harness/workspace/install"
	"github.com/hollis-labs/substrate/harness/workspace/keymerge"
	"github.com/hollis-labs/substrate/harness/workspace/materialize"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
	"github.com/hollis-labs/substrate/harness/workspace/render"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
	"path"
	"reflect"
	"testing"
)

func nativeFixture(t *testing.T, provider runtimes.ID) (install.Request, render.Result, []install.FileSnapshot) {
	t.Helper()
	file := ".claude/settings.json"
	desired := []byte(`{"managed":1}`)
	slots := []string{}
	if provider == runtimes.Codex {
		file = ".codex/config.toml"
		desired = []byte("managed=1\n")
		slots = []string{"mcp_servers"}
	}
	note, _ := json.Marshal(render.DocumentOwnership{Schema: render.OwnershipNoteSchema, OwnedKeyPaths: [][]string{{"managed"}}, ReservedSlots: slots})
	r := render.Result{Provider: provider, Layer: layout.Installed, Mode: layout.InstallMode, Root: layout.RootHome, Tree: artifact.Tree{Entries: []artifact.Entry{{Path: file, Kind: artifact.EntryFile, Bytes: desired, Mode: 0600, Ownership: artifact.Ownership{EntryID: "fixture-entry", GroupID: "fixture-group"}, Provenance: artifact.Provenance{Source: "fixture", Note: string(note)}}}}}
	req := install.Request{Header: effects.Header{Version: effects.SchemaVersion, OperationID: "fixture-operation", InputDigest: "sha256:" + artifact.DigestBytes([]byte("fixture")).Hex}, Target: effects.RootInput{ID: "target", Path: "/fixture/operator"}, Control: effects.RootInput{ID: "control", Path: "/fixture/control"}, CaseMode: materialize.CaseSensitive, Grants: []install.Grant{{ID: "fixture-grant", Version: "1", Path: file, KeyPaths: [][]string{{"managed"}}}}}
	return req, r, []install.FileSnapshot{{Path: file, Parents: []materialize.InstalledDirectoryChange{{Path: path.Dir(file)}}}}
}
func presentSnapshot(s *install.FileSnapshot, raw []byte) {
	s.Exists = true
	s.Kind = artifact.EntryFile
	s.Mode = 0600
	s.Identity = "fixture-inode"
	s.Bytes = raw
	s.Digest = artifact.DigestBytes(raw)
}
func TestInstalledPureMissingAndUnknownOperatorContent(t *testing.T) {
	for _, provider := range []runtimes.ID{runtimes.Claude, runtimes.Codex} {
		t.Run(string(provider), func(t *testing.T) {
			req, r, obs := nativeFixture(t, provider)
			p, err := install.Prepare(req, r, obs)
			if err != nil {
				t.Fatal(err)
			}
			if p.Evidence().Stage.Path == "" {
				t.Fatal("aggregate intent lacks operation-bound stage")
			}
			request := p.EngineRequest()
			if request.Operation != materialize.OperationInstall {
				t.Fatal("writer routed outside closed engine")
			}
			raw := []byte(`{"operator":"preserved"}`)
			if provider == runtimes.Codex {
				raw = []byte("operator='preserved'\n")
			}
			presentSnapshot(&obs[0], raw)
			p, err = install.Prepare(req, r, obs)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(p.EngineRequest().Artifacts.Entries[len(p.EngineRequest().Artifacts.Entries)-1].Bytes, []byte("preserved")) {
				t.Fatal("lost unknown operator value")
			}
			evidence := p.Evidence()
			b, _ := json.Marshal(evidence)
			if bytes.Contains(b, []byte("preserved")) || bytes.Contains(b, []byte("\"Bytes\"")) {
				t.Fatal("receipt includes values")
			}
			before := p.EngineRequest()
			r.Tree.Entries[0].Bytes[0] = 'X'
			obs[0].Bytes[0] = 'X'
			req.Grants[0].KeyPaths[0][0] = "changed"
			exposed := p.EngineRequest()
			exposed.Artifacts.Entries[len(exposed.Artifacts.Entries)-1].Bytes[0] = 'X'
			if !reflect.DeepEqual(before, p.EngineRequest()) {
				t.Fatal("prepared request aliases mutable input")
			}
		})
	}
}
func TestInstalledPureRefusals(t *testing.T) {
	for _, name := range []string{"zero-byte JSON", "malformed", "duplicate unowned key", "duplicate nested unowned key", "array root", "existing ownership", "ancestor conflict", "missing grant", "wrong grant key", "whole native", "unknown provider", "unknown case", "wrong root mode", "effect", "invalid ownership", "wrong reserved slot", "unknown existing type", "credential path"} {
		t.Run(name, func(t *testing.T) {
			req, r, obs := nativeFixture(t, runtimes.Claude)
			switch name {
			case "zero-byte JSON":
				presentSnapshot(&obs[0], []byte{})
			case "malformed":
				presentSnapshot(&obs[0], []byte("{"))
			case "duplicate unowned key":
				presentSnapshot(&obs[0], []byte(`{"operator":1,"operator":2}`))
			case "duplicate nested unowned key":
				presentSnapshot(&obs[0], []byte(`{"operator":{"x":1,"x":2}}`))
			case "array root":
				presentSnapshot(&obs[0], []byte("[]"))
			case "existing ownership":
				presentSnapshot(&obs[0], []byte(`{"managed":1}`))
			case "ancestor conflict":
				req.Grants[0].KeyPaths = [][]string{{"managed", "leaf"}}
				r.Tree.Entries[0].Bytes = []byte(`{"managed":{"leaf":1}}`)
				note, _ := json.Marshal(render.DocumentOwnership{Schema: render.OwnershipNoteSchema, OwnedKeyPaths: req.Grants[0].KeyPaths, ReservedSlots: []string{}})
				r.Tree.Entries[0].Provenance.Note = string(note)
				presentSnapshot(&obs[0], []byte(`{"managed":"operator"}`))
			case "missing grant":
				req.Grants = nil
			case "wrong grant key":
				req.Grants[0].KeyPaths = [][]string{{"other"}}
			case "whole native":
				req.Grants[0].KeyPaths = nil
				req.Grants[0].WholeFile = true
			case "unknown provider":
				r.Provider = runtimes.OpenCode
			case "unknown case":
				req.CaseMode = ""
			case "wrong root mode":
				r.RootMode = 0700
			case "effect":
				r.Effects = []layout.Row{{}}
			case "invalid ownership":
				r.Tree.Entries[0].Provenance.Note = "{}"
			case "wrong reserved slot":
				note, _ := json.Marshal(render.DocumentOwnership{Schema: render.OwnershipNoteSchema, OwnedKeyPaths: req.Grants[0].KeyPaths, ReservedSlots: []string{"foreign"}})
				r.Tree.Entries[0].Provenance.Note = string(note)
			case "unknown existing type":
				presentSnapshot(&obs[0], []byte("{}"))
				obs[0].Kind = artifact.EntryDirectory
			case "credential path":
				r.Tree.Entries[0].Path = ".claude/auth.json"
				obs[0].Path = r.Tree.Entries[0].Path
				req.Grants[0].Path = r.Tree.Entries[0].Path
			}
			if _, err := install.Prepare(req, r, obs); err == nil {
				t.Fatal("accepted unsafe installed input")
			}
		})
	}
}
func TestInstalledOwnedRefreshAndDrift(t *testing.T) {
	req, r, obs := nativeFixture(t, runtimes.Claude)
	initial, err := install.Prepare(req, r, obs)
	if err != nil {
		t.Fatal(err)
	}
	previous := initial.Evidence()
	previous.Generation = req.Header.InputDigest
	for j := range previous.Files {
		previous.Files[j].Phase = materialize.InstalledVerified
	}
	req.Previous = &previous
	presentSnapshot(&obs[0], []byte(`{"managed":1,"operator":true}`))
	r.Tree.Entries[0].Bytes = []byte(`{"managed":2}`)
	req.Header.OperationID = "fixture-refresh"
	req.Header.InputDigest = "sha256:" + artifact.DigestBytes([]byte("next")).Hex
	next, err := install.Prepare(req, r, obs)
	if err != nil {
		t.Fatal(err)
	}
	if next.Evidence().PreviousGeneration != previous.Generation {
		t.Fatal("forgot original generation")
	}
	keys, err := keymerge.ObserveKeys("json", next.EngineRequest().Artifacts.Entries[1].Bytes, []keymerge.KeyPath{{"managed"}})
	if err != nil || keys[0].Digest == previous.Files[0].KeysAfter[0].Digest {
		t.Fatal("owned key not updated")
	}
	presentSnapshot(&obs[0], []byte(`{"managed":999,"operator":true}`))
	if _, err = install.Prepare(req, r, obs); !errors.Is(err, install.ErrConflict) {
		t.Fatal("accepted owned drift")
	}
}

func TestInstalledAbsentDocumentPreservesApprovedRenderedBytes(t *testing.T) {
	req, r, obs := nativeFixture(t, runtimes.Codex)
	r.Tree.Entries[0].Bytes = []byte("# generated fixture\nmanaged = 1\n")
	p, err := install.Prepare(req, r, obs)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(p.EngineRequest().Artifacts.Entries[1].Bytes, r.Tree.Entries[0].Bytes) {
		t.Fatal("changed rendered bytes for absent fully granted document")
	}
	// Native render can contain an operator-only input slot. It is not authority
	// to create that slot when no existing document carries it.
	r.Tree.Entries[0].Bytes = []byte("# generated fixture\nmanaged = 1\noperator = 'excluded'\n")
	p, err = install.Prepare(req, r, obs)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(p.EngineRequest().Artifacts.Entries[1].Bytes, []byte("excluded")) {
		t.Fatal("created ungranted slot")
	}
}
