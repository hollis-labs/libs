package registry

import (
	"bytes"
	"encoding/json"
	"io"
)

// UnmarshalJSON rejects duplicate object keys rather than allowing last-writer-wins.
func (r *Response) UnmarshalJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := walkJSON(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrInvalidContribution
	}
	type wire Response
	var decoded wire
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*r = Response(decoded)
	return nil
}
func walkJSON(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			token, err := d.Token()
			if err != nil {
				return err
			}
			key, ok := token.(string)
			if !ok {
				return ErrInvalidContribution
			}
			if seen[key] {
				return ErrCollision
			}
			seen[key] = true
			if err := walkJSON(d); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := walkJSON(d); err != nil {
				return err
			}
		}
	default:
		return ErrInvalidContribution
	}
	_, err = d.Token()
	return err
}
func (c *Contribution) UnmarshalJSON(raw []byte) error {
	type wire Contribution
	var value wire
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if required, ok := fields["required"]; !ok || string(required) == "null" {
		return ErrInvalidContribution
	}
	*c = Contribution(value)
	return nil
}
