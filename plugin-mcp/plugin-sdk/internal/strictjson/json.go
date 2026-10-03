// Package strictjson checks JSON tokens before decoding can discard duplicate
// keys. Opaque values retain their JSON representation and field casing.
package strictjson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"
)

// MaxDepth bounds recursive JSON inspection independently of transport framing.
const MaxDepth = 128

// Validate accepts one JSON value, rejecting duplicate keys at every depth.
func Validate(data []byte) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("invalid JSON UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := value(d, 0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("expected one JSON value")
	}
	return nil
}

func value(d *json.Decoder, depth int) error {
	if depth > MaxDepth {
		return fmt.Errorf("JSON nesting exceeds limit")
	}
	t, err := d.Token()
	if err != nil {
		return err
	}
	switch t {
	case json.Delim('{'):
		seen := make(map[string]bool)
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			k, ok := key.(string)
			if !ok {
				return fmt.Errorf("expected JSON object key")
			}
			if seen[k] {
				return fmt.Errorf("duplicate JSON key")
			}
			seen[k] = true
			if err := value(d, depth+1); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return fmt.Errorf("invalid JSON object")
		}
	case json.Delim('['):
		for d.More() {
			if err := value(d, depth+1); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return fmt.Errorf("invalid JSON array")
		}
	case json.Delim('}'), json.Delim(']'):
		return fmt.Errorf("unexpected JSON delimiter")
	}
	return nil
}

// Object returns raw fields of a closed object. Every named field is required
// and non-null. Exact key casing is enforced; no permissive struct matching.
func Object(data []byte, fields ...string) (map[string]json.RawMessage, error) {
	if err := Validate(data); err != nil {
		return nil, err
	}
	if b := bytes.TrimSpace(data); len(b) == 0 || b[0] != '{' {
		return nil, fmt.Errorf("expected JSON object")
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	allowed := make(map[string]bool, len(fields))
	for _, f := range fields {
		allowed[f] = true
	}
	for k := range out {
		if !allowed[k] {
			return nil, fmt.Errorf("unknown or incorrectly cased JSON field")
		}
	}
	for _, f := range fields {
		v, ok := out[f]
		if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return nil, fmt.Errorf("required non-null field %s", f)
		}
	}
	return out, nil
}
