package render

import (
	"encoding/json"
	"errors"
	layout "github.com/hollis-labs/substrate/harness/adapters/layout/plan"
	contract "github.com/hollis-labs/substrate/harness/adapters/nativefiles"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
	"io"
	"slices"
	"strings"
)

// DocumentOwnership is encoded in a native entry's Provenance.Note. The apply
// edge owns recursive key merge: declared scalar/array leaves may be replaced;
// unowned operator leaves remain. Components are separate, not dotted strings.
// The rule matches Cairn's install/merge.go and install/toml.go recursive merge.
type DocumentOwnership struct {
	Schema        string     `json:"schema"`
	OwnedKeyPaths [][]string `json:"owned_key_paths"`
	ReservedSlots []string   `json:"reserved_slots"`
}

func documentOwnership(req Request, row layout.Row, keys [][]string, table []layout.Row, e *artifact.Entry, matches []bool) error {
	ownership := DocumentOwnership{Schema: OwnershipNoteSchema, OwnedKeyPaths: [][]string{}, ReservedSlots: []string{}}
	ctx := contract.Context{Provider: string(req.Provider), Mode: string(req.Mode), Concern: "ownership"}
	if err := contract.ValidateValue(ctx, req.Native.OperatorKeyPaths); err != nil {
		return err
	}
	for _, parts := range req.Native.OperatorKeyPaths {
		if len(parts) == 0 {
			return refuse(req, layout.Settings, "invalid_ownership", "operator key path must contain components")
		}
	}
	for _, parts := range keys {
		operator := false
		for i, unowned := range req.Native.OperatorKeyPaths {
			if slices.Equal(unowned, parts) {
				operator = true
				matches[i] = true
			}
		}
		if !operator {
			ownership.OwnedKeyPaths = append(ownership.OwnedKeyPaths, slices.Clone(parts))
		}
	}
	for _, candidate := range table {
		if candidate.Path == row.Path && candidate.DocumentSlot != "" && !slices.Contains(ownership.ReservedSlots, candidate.DocumentSlot) {
			ownership.ReservedSlots = append(ownership.ReservedSlots, candidate.DocumentSlot)
		}
	}
	slices.Sort(ownership.ReservedSlots)
	note, err := json.Marshal(ownership)
	if err != nil {
		return refuse(req, layout.Settings, "invalid_ownership", "native ownership metadata cannot be encoded")
	}
	e.Provenance.Note = string(note)
	return nil
}

// OwnershipNoteSchema identifies render-minted native document ownership notes.
const OwnershipNoteSchema = "native-key-ownership.v1"

// ErrInvalidOwnershipNote is returned without echoing untrusted note data.
var ErrInvalidOwnershipNote = errors.New("render: invalid ownership note")

// ParseOwnershipNote decodes the versioned note, refusing malformed or unknown
// schemas and trailing input. The returned collections are detached.
func ParseOwnershipNote(note string) (DocumentOwnership, error) {
	var out DocumentOwnership
	if contract.ValidateValue(contract.Context{}, note) != nil {
		return DocumentOwnership{}, ErrInvalidOwnershipNote
	}
	if _, err := contract.Object(contract.Context{}, []contract.Slot{{Key: "note", Value: json.RawMessage(note)}}); err != nil {
		return DocumentOwnership{}, ErrInvalidOwnershipNote
	}
	d := json.NewDecoder(strings.NewReader(note))
	d.DisallowUnknownFields()
	if d.Decode(&out) != nil || out.Schema != OwnershipNoteSchema || out.OwnedKeyPaths == nil || out.ReservedSlots == nil {
		return DocumentOwnership{}, ErrInvalidOwnershipNote
	}
	var trailing any
	if d.Decode(&trailing) != io.EOF {
		return DocumentOwnership{}, ErrInvalidOwnershipNote
	}
	for _, parts := range out.OwnedKeyPaths {
		if len(parts) == 0 {
			return DocumentOwnership{}, ErrInvalidOwnershipNote
		}
	}
	if contract.ValidateValue(contract.Context{}, out) != nil {
		return DocumentOwnership{}, ErrInvalidOwnershipNote
	}
	return out, nil
}
