package keymerge

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/pelletier/go-toml/v2"
	"io"
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

// Walk tokens once instead of reparsing each nested raw subtree. Duplicate
// validation covers unowned objects and objects inside arrays as well.
func validateJSONObjects(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return fmt.Errorf("keymerge: unreadable document")
	}
	if err = validateJSONObjectTokens(dec, 1); err != nil {
		return err
	}
	if _, err = dec.Token(); err != io.EOF {
		return fmt.Errorf("keymerge: unreadable document")
	}
	return nil
}
func validateJSONObjectTokens(dec *json.Decoder, depth int) error {
	if depth > 10000 {
		return fmt.Errorf("keymerge: unreadable document")
	}
	seen := map[string]bool{}
	for dec.More() {
		token, err := dec.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return fmt.Errorf("keymerge: unreadable document")
		}
		seen[key] = true
		if err = validateJSONTokenValue(dec, depth+1); err != nil {
			return err
		}
	}
	token, err := dec.Token()
	if err != nil || token != json.Delim('}') {
		return fmt.Errorf("keymerge: unreadable document")
	}
	return nil
}
func validateJSONTokenValue(dec *json.Decoder, depth int) error {
	token, err := dec.Token()
	if err != nil {
		return fmt.Errorf("keymerge: unreadable document")
	}
	switch token {
	case json.Delim('{'):
		return validateJSONObjectTokens(dec, depth)
	case json.Delim('['):
		if depth > 10000 {
			return fmt.Errorf("keymerge: unreadable document")
		}
		for dec.More() {
			if err = validateJSONTokenValue(dec, depth+1); err != nil {
				return err
			}
		}
		token, err = dec.Token()
		if err != nil || token != json.Delim(']') {
			return fmt.Errorf("keymerge: unreadable document")
		}
	case json.Delim('}'), json.Delim(']'):
		return fmt.Errorf("keymerge: unreadable document")
	}
	return nil
}
