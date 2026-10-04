// Package install freezes conservative installed changes. It is pure and has
// no filesystem writer. The workspace authority boundary applies its requests
// through the concrete materialize engine and an external durable receipt store.
// Existing user directories are traversal facts, never implicitly owned entries.
package install

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	layout "github.com/hollis-labs/substrate/harness/adapters/layout/plan"
	"github.com/hollis-labs/substrate/harness/workspace/effects"
	"github.com/hollis-labs/substrate/harness/workspace/keymerge"
	"github.com/hollis-labs/substrate/harness/workspace/materialize"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
	"github.com/hollis-labs/substrate/harness/workspace/render"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
	"github.com/pelletier/go-toml/v2"
	"path"
	"reflect"
	"slices"
	"strings"
)

type Grant struct {
	ID, Version, Path string
	WholeFile         bool
	KeyPaths          [][]string
}
type FileSnapshot = materialize.InstalledSnapshot
type Request struct {
	Header          effects.Header
	Target, Control effects.RootInput
	Grants          []Grant
	Previous        *Evidence
	CaseMode        materialize.CaseMode
}
type Evidence struct {
	Header                                                 effects.Header
	TargetID, CanonicalTarget, ControlID, CanonicalControl string
	PreviousGeneration, Generation                         string
	Files                                                  []materialize.InstalledFileChange
	Directories                                            []materialize.InstalledDirectoryChange
	Stage                                                  materialize.InstalledTemp
}
type Prepared struct {
	request  materialize.Request
	evidence Evidence
	valid    bool
}

var ErrConflict = errors.New("install: ownership or document conflict")
var ErrUnsupported = errors.New("install: unsupported installed contract")

func clone[T any](v T) T { b, _ := json.Marshal(v); var out T; _ = json.Unmarshal(b, &out); return out }
func (p Prepared) EngineRequest() materialize.Request {
	out := clone(p.request)
	out.Artifacts.Entries = artifact.CloneEntries(p.request.Artifacts.Entries)
	return out
}
func (p Prepared) Evidence() Evidence { return clone(p.evidence) }

// Prepare checks every grant and document before producing any write request.
// Present unreadable documents, conflicting first ownership and owned drift
// refuse; the keymerge leaf's unreadable fallback is never used for apply.
func Prepare(req Request, r render.Result, observed []FileSnapshot) (Prepared, error) {
	if r.Provider != runtimes.Claude && r.Provider != runtimes.Codex {
		return Prepared{}, ErrUnsupported
	}
	if r.Layer != layout.Installed || r.Root != layout.RootHome || r.RootMode != 0 || len(r.Effects) != 0 || len(r.Preparations) != 0 {
		return Prepared{}, ErrUnsupported
	}
	if req.CaseMode != materialize.CaseSensitive && req.CaseMode != materialize.CaseInsensitive {
		return Prepared{}, ErrUnsupported
	}
	if req.Target.ID == "" || req.Target.Path == "" || req.Control.ID == "" || req.Control.Path == "" || req.Target.Path == req.Control.Path {
		return Prepared{}, ErrConflict
	}
	if req.Header != (effects.Header{}) && !req.Header.Valid() {
		return Prepared{}, ErrConflict
	}
	rows, err := layout.For(r.Provider, r.Layer, r.Mode, r.Variant)
	if err != nil {
		return Prepared{}, ErrUnsupported
	}
	entries, err := artifact.Normalize(r.Tree.Entries)
	if err != nil {
		return Prepared{}, ErrConflict
	}
	snapshots := map[string]FileSnapshot{}
	for _, s := range observed {
		if _, ok := snapshots[s.Path]; ok {
			return Prepared{}, ErrConflict
		}
		snapshots[s.Path] = s
	}
	grants := map[string]Grant{}
	for _, g := range req.Grants {
		if g.ID == "" || g.Version == "" || render.ValidateRelPath(g.Path) != nil || g.WholeFile == (len(g.KeyPaths) > 0) {
			return Prepared{}, ErrConflict
		}
		if _, dup := grants[g.Path]; dup {
			return Prepared{}, ErrConflict
		}
		grants[g.Path] = g
	}
	prior := map[string]materialize.InstalledFileChange{}
	if req.Previous != nil {
		p := req.Previous
		if !p.Header.Valid() || p.Generation == "" || p.Header.InputDigest != p.Generation || p.TargetID != req.Target.ID || p.CanonicalTarget != req.Target.Path || p.ControlID != req.Control.ID || p.CanonicalControl != req.Control.Path {
			return Prepared{}, ErrConflict
		}
		for _, c := range p.Files {
			if _, dup := prior[c.Path]; dup || c.Phase != materialize.InstalledVerified {
				return Prepared{}, ErrConflict
			}
			prior[c.Path] = c
		}
	}
	evidence := Evidence{Header: req.Header, TargetID: req.Target.ID, CanonicalTarget: req.Target.Path, ControlID: req.Control.ID, CanonicalControl: req.Control.Path}
	if req.Previous != nil {
		evidence.PreviousGeneration = req.Previous.Generation
	}
	directoryMap := map[string]materialize.InstalledDirectoryChange{}
	for _, snapshot := range observed {
		for _, d := range snapshot.Parents {
			if prior, ok := directoryMap[d.Path]; ok && prior != d {
				return Prepared{}, ErrConflict
			}
			directoryMap[d.Path] = d
		}
	}
	for _, entry := range entries {
		if entry.Kind == artifact.EntryDirectory {
			s, ok := snapshots[entry.Path]
			if !ok {
				return Prepared{}, ErrConflict
			}
			directoryMap[entry.Path] = materialize.InstalledDirectoryChange{Path: entry.Path, Exists: s.Exists, Identity: s.Identity, Mode: s.Mode}
		}
	}
	for _, d := range directoryMap {
		if render.ValidateRelPath(d.Path) != nil || d.Created || d.Phase != "" || d.Exists && (d.Identity == "" || d.Mode > 0777) || !d.Exists && (d.Identity != "" || d.Mode != 0) {
			return Prepared{}, ErrConflict
		}
		evidence.Directories = append(evidence.Directories, d)
	}
	slices.SortFunc(evidence.Directories, func(a, b materialize.InstalledDirectoryChange) int { return strings.Compare(a.Path, b.Path) })
	if req.Header.Valid() {
		raw, _ := json.Marshal(req.Header)
		h := sha256.Sum256(raw)
		evidence.Stage = materialize.InstalledTemp{Path: ".installed-stage-" + hex.EncodeToString(h[:]), Phase: materialize.InstalledPrepared}
	}
	out := artifact.Tree{Provenance: r.Tree.Provenance}
	for _, e := range entries {
		s, ok := snapshots[e.Path]
		if !ok {
			return Prepared{}, ErrConflict
		}
		delete(snapshots, e.Path)
		if render.ValidateRelPath(e.Path) != nil || render.IsCredentialDestination(e.Path) || e.Mode&^e.Mode.Perm() != 0 {
			return Prepared{}, ErrConflict
		}
		if s.Exists && (s.Identity == "" || s.Mode > 0777 || s.Kind != e.Kind) {
			return Prepared{}, ErrConflict
		}
		if !s.Exists && (s.Identity != "" || s.Mode != 0 || s.Kind != "" || s.Bytes != nil || s.Digest != (artifact.Digest{})) {
			return Prepared{}, ErrConflict
		}
		if e.Kind == artifact.EntryDirectory {
			out.Entries = append(out.Entries, e)
			continue
		}
		if e.Bytes == nil || len(e.Bytes) > materialize.InstalledFileLimit || e.Ownership.EntryID == "" || e.Ownership.GroupID == "" || e.Provenance.Source == "" {
			return Prepared{}, ErrConflict
		}
		if e.Digest != (artifact.Digest{}) && e.Digest != artifact.DigestBytes(e.Bytes) {
			return Prepared{}, ErrConflict
		}
		g, ok := grants[e.Path]
		if !ok || !allowedFile(rows, e.Path) {
			return Prepared{}, ErrConflict
		}
		delete(grants, e.Path)
		if s.Exists && s.Digest != artifact.DigestBytes(s.Bytes) {
			return Prepared{}, ErrConflict
		}
		c := materialize.InstalledFileChange{Path: e.Path, BeforeIdentity: s.Identity, BeforeExists: s.Exists, BeforeMode: s.Mode, Before: s.Digest, AfterMode: uint32(e.Mode.Perm()), GrantID: g.ID, GrantVersion: g.Version, Phase: materialize.InstalledPrepared}
		old, hadPrior := prior[e.Path]
		if g.WholeFile {
			if isNativeDocument(r.Provider, e.Path) {
				return Prepared{}, ErrConflict
			}
			if s.Exists && (!hadPrior || old.After != s.Digest || old.AfterMode != s.Mode || len(old.KeysAfter) != 0) {
				return Prepared{}, ErrConflict
			}
		} else {
			kind := "json"
			if r.Provider == runtimes.Codex {
				kind = "toml"
			}
			if !isNativeDocument(r.Provider, e.Path) || e.Mode.Perm() != 0600 {
				return Prepared{}, ErrConflict
			}
			note, er := render.ParseOwnershipNote(e.Provenance.Note)
			expectedSlots := []string{}
			for _, row := range rows {
				if row.Path == e.Path && row.DocumentSlot != "" && !slices.Contains(expectedSlots, row.DocumentSlot) {
					expectedSlots = append(expectedSlots, row.DocumentSlot)
				}
			}
			slices.Sort(expectedSlots)
			if er != nil || !samePaths(g.KeyPaths, note.OwnedKeyPaths) || !slices.Equal(expectedSlots, note.ReservedSlots) {
				return Prepared{}, ErrConflict
			}
			paths := make([]keymerge.KeyPath, len(g.KeyPaths))
			for i, p := range g.KeyPaths {
				paths[i] = slices.Clone(p)
			}
			existing := s.Bytes
			if !s.Exists {
				if kind == "json" {
					existing = []byte("{}")
				} else {
					existing = []byte{}
				}
			}
			if _, er := keymerge.ObserveKeys(kind, e.Bytes, paths); er != nil {
				return Prepared{}, ErrConflict
			}
			before, er := keymerge.ObserveKeys(kind, existing, paths)
			if er != nil {
				return Prepared{}, ErrConflict
			}
			// Every present leaf must be evidenced by the trusted previous committed
			// operation. An explicit grant does not silently adopt an existing value.
			for _, k := range before {
				if !k.Exists {
					continue
				}
				known := false
				for _, pk := range old.KeysAfter {
					if reflect.DeepEqual(pk.Path, k.Path) && pk.Exists && pk.Digest == k.Digest {
						known = true
					}
				}
				if !hadPrior || !known {
					return Prepared{}, ErrConflict
				}
			}
			desiredBytes := slices.Clone(e.Bytes)
			var notes []keymerge.Note
			if kind == "json" {
				merged, er := keymerge.MergeJSON(e.Bytes, existing, paths)
				if er != nil || merged.Outcome != keymerge.OutcomeMerged {
					return Prepared{}, ErrConflict
				}
				e.Bytes = merged.Document
				notes = merged.Notes
			} else {
				merged, er := keymerge.MergeTOML(e.Bytes, existing, paths)
				if er != nil {
					return Prepared{}, ErrConflict
				}
				e.Bytes = merged.Document
				notes = merged.Notes
			}
			for _, n := range notes {
				if n.Code == keymerge.NoteTypeConflict {
					return Prepared{}, ErrConflict
				}
			}
			if !s.Exists && sameDocument(kind, desiredBytes, e.Bytes) {
				e.Bytes = desiredBytes
			}
			c.KeysBefore = before
			c.KeysAfter, er = keymerge.ObserveKeys(kind, e.Bytes, paths)
			if er != nil {
				return Prepared{}, ErrConflict
			}
			for _, k := range c.KeysAfter {
				if !k.Exists {
					return Prepared{}, ErrConflict
				}
			}
		}
		e.Digest = artifact.DigestBytes(e.Bytes)
		c.After = e.Digest
		out.Entries = append(out.Entries, e)
		evidence.Files = append(evidence.Files, c)
	}
	if len(grants) > 0 || len(snapshots) > 0 {
		return Prepared{}, ErrConflict
	}
	// Reconstruct needed parent declarations without ownership of existing dirs.
	parents := map[string]bool{}
	for _, e := range out.Entries {
		parents[e.Path] = true
	}
	for _, e := range slices.Clone(out.Entries) {
		for p := path.Dir(e.Path); p != "."; p = path.Dir(p) {
			if !parents[p] {
				out.Entries = append(out.Entries, artifact.Entry{Path: p, Kind: artifact.EntryDirectory, Mode: 0755})
				parents[p] = true
			}
		}
	}
	out.Entries, err = artifact.Normalize(out.Entries)
	if err != nil {
		return Prepared{}, ErrConflict
	}
	request := materialize.Request{Operation: materialize.OperationInstall, TargetRoot: req.Target.Path, Artifacts: out, Installed: &materialize.InstalledPolicy{CaseMode: req.CaseMode, Files: clone(evidence.Files), Directories: clone(evidence.Directories), Stage: evidence.Stage}}
	return Prepared{request: request, evidence: evidence, valid: true}, nil
}
func samePaths(a, b [][]string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]bool{}
	for _, p := range a {
		if len(p) == 0 {
			return false
		}
		k, _ := json.Marshal(p)
		if seen[string(k)] {
			return false
		}
		seen[string(k)] = true
	}
	for _, p := range b {
		k, _ := json.Marshal(p)
		if !seen[string(k)] {
			return false
		}
	}
	return true
}
func isNativeDocument(provider runtimes.ID, p string) bool {
	return provider == runtimes.Claude && p == ".claude/settings.json" || provider == runtimes.Codex && p == ".codex/config.toml"
}
func allowedFile(rows []layout.Row, p string) bool {
	for _, r := range rows {
		if r.Form == layout.File && r.Path == p {
			return true
		}
		if r.Form == layout.Package {
			prefix := strings.Split(r.Path, "{name}")[0]
			if strings.HasPrefix(p, prefix) {
				suffix := strings.TrimPrefix(p, prefix)
				parts := strings.Split(suffix, "/")
				if len(parts) >= 2 && parts[0] != "" {
					return true
				}
			}
		}
	}
	return false
}

// EquivalentSnapshot compares content as well as identity, preserving explicit
// zero-byte files. It is not a filesystem observation or an authority proof.
func EquivalentSnapshot(a, b FileSnapshot) bool {
	return a.Path == b.Path && a.Exists == b.Exists && a.Kind == b.Kind && a.Mode == b.Mode && a.Identity == b.Identity && a.Digest == b.Digest && bytes.Equal(a.Bytes, b.Bytes) && reflect.DeepEqual(a.Parents, b.Parents)
}

// Preserve a fully authorized absent render exactly. Equality of the COMPLETE
// document, rather than just owned leaves, prevents creating ungranted slots.
func sameDocument(kind string, a, b []byte) bool {
	var left, right map[string]any
	if kind == "json" {
		da, db := json.NewDecoder(bytes.NewReader(a)), json.NewDecoder(bytes.NewReader(b))
		da.UseNumber()
		db.UseNumber()
		if da.Decode(&left) != nil || db.Decode(&right) != nil {
			return false
		}
	} else if toml.Unmarshal(a, &left) != nil || toml.Unmarshal(b, &right) != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}
