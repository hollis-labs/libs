package hitl

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// splitExtra decodes a JSON object and returns the members whose names are
// not in known, preserving their raw values.
func splitExtra(data []byte, known ...string) (map[string]json.RawMessage, error) {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, err
	}
	if all == nil {
		return nil, fmt.Errorf("hitl: expected a JSON object, got null")
	}
	for _, k := range known {
		delete(all, k)
	}
	if len(all) == 0 {
		return nil, nil
	}
	return all, nil
}

// joinExtra marshals base (which must encode as a JSON object) and adds each
// extra member whose name base does not already use. Keys come out sorted.
func joinExtra(base any, extra map[string]json.RawMessage) ([]byte, error) {
	b, err := json.Marshal(base)
	if err != nil {
		return nil, err
	}
	if len(extra) == 0 {
		return b, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range extra {
		if _, ok := m[k]; !ok {
			m[k] = v
		}
	}
	return json.Marshal(m)
}

// strictDecode decodes one JSON value into v and rejects unknown fields and
// trailing data. It is the decoder for commands.
func strictDecode(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	if dec.More() {
		return fmt.Errorf("%w: trailing data after JSON document", ErrInvalidRequest)
	}
	return nil
}
