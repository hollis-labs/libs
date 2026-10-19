package forums

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// UnmarshalJSON preserves strict consumer input validation even when Request
// is decoded by a transport without its own DisallowUnknownFields setting.
// Omitted patch strings keep existing values; explicitly empty titles/bodies
// are invalid, whereas empty descriptions/subjects and attachment arrays clear.
func (r *Request) UnmarshalJSON(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{' {
		return fmt.Errorf("%w: request must be a JSON object", ErrInvalid)
	}
	type wire Request
	var v wire
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, field := range []string{"title", "body_md"} {
		if value, ok := fields[field]; ok {
			var text string
			if err := json.Unmarshal(value, &text); err != nil || strings.TrimSpace(text) == "" {
				return fmt.Errorf("%w: %s must not be empty", ErrInvalid, field)
			}
		}
	}
	*r = Request(v)
	return nil
}
