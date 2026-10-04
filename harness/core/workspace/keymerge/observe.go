package keymerge

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/pelletier/go-toml/v2"
	"unicode/utf8"
)

// KeyState is content-free evidence for one exact leaf. Absence is distinct
// from an empty value; paths use components, never dotted strings.
type KeyState struct {
	Path   KeyPath
	Exists bool
	Digest string
}

// ObserveKeys validates the complete document and observes exact leaf values.
// It performs no merge and returns no document values. JSON digests forgive
// token whitespace, while preserving value spellings. TOML uses its encoder.
func ObserveKeys(kind string, raw []byte, paths []KeyPath) ([]KeyState, error) {
	if !utf8.Valid(raw) {
		return nil, fmt.Errorf("keymerge: unreadable document")
	}
	if _, err := newOwnedSet(paths); err != nil {
		return nil, err
	}
	var object map[string]any
	switch kind {
	case "json":
		if err := validateJSONObjects(raw); err != nil {
			return nil, err
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&object); err != nil || object == nil {
			return nil, fmt.Errorf("keymerge: unreadable document")
		}
	case "toml":
		if err := toml.Unmarshal(raw, &object); err != nil {
			return nil, &Error{Code: CodeExistingTOMLInvalid, Reason: ReasonNotTOML}
		}
	default:
		return nil, fmt.Errorf("keymerge: unsupported document")
	}
	result := make([]KeyState, 0, len(paths))
	for _, p := range paths {
		state := KeyState{Path: append(KeyPath(nil), p...)}
		var value any = object
		for i, c := range p {
			obj, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("keymerge: incompatible key ancestor")
			}
			value, ok = obj[c]
			if !ok {
				value = nil
				break
			}
			if i == len(p)-1 {
				state.Exists = true
			}
		}
		if state.Exists {
			if obj, ok := value.(map[string]any); ok && len(obj) > 0 {
				return nil, errNotLeaf()
			}
			var data []byte
			var err error
			if kind == "json" {
				data, err = json.Marshal(value)
			} else {
				data, err = toml.Marshal(map[string]any{"value": value})
			}
			if err != nil {
				return nil, fmt.Errorf("keymerge: unsupported leaf")
			}
			sum := sha256.Sum256(data)
			state.Digest = hex.EncodeToString(sum[:])
		}
		result = append(result, state)
	}
	return result, nil
}

func validateJSONObjects(raw []byte) error {
	members, reason := readObject(raw)
	if reason != "" {
		return fmt.Errorf("keymerge: unreadable document")
	}
	for _, m := range members {
		if err := validateJSONValue(m.value); err != nil {
			return err
		}
	}
	return nil
}
func validateJSONValue(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '{' {
		return validateJSONObjects(raw)
	}
	if len(raw) > 0 && raw[0] == '[' {
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil {
			return fmt.Errorf("keymerge: unreadable document")
		}
		for _, v := range values {
			if err := validateJSONValue(v); err != nil {
				return err
			}
		}
	}
	return nil
}
