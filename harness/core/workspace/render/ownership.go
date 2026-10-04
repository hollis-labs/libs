package render

import (
	"encoding/json"
	layout "github.com/hollis-labs/substrate/harness/adapters/layout/plan"
	contract "github.com/hollis-labs/substrate/harness/adapters/nativefiles"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
	"slices"
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

func documentOwnership(req Request, row layout.Row, keys [][]string, table []layout.Row, e *artifact.Entry) error {
	ownership := DocumentOwnership{Schema: "native-key-ownership.v1", OwnedKeyPaths: [][]string{}, ReservedSlots: []string{}}
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
		for _, unowned := range req.Native.OperatorKeyPaths {
			if slices.Equal(unowned, parts) {
				operator = true
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
