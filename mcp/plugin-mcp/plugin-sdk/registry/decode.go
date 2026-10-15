package registry

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strings"
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
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return ErrInvalidContribution
	}
	for key := range fields {
		if strings.EqualFold(key, "protocol") {
			return ErrRegistryVersion
		}
	}
	type wire Response
	var decoded wire
	if err := decodeExact(raw, &decoded); err != nil {
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
			folded := strings.ToLower(key)
			if seen[folded] {
				return ErrCollision
			}
			seen[folded] = true
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
	if err := decodeExact(raw, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if required, ok := fields["required"]; !ok || string(required) == "null" {
		return ErrInvalidContribution
	}
	switch value.Representation {
	case Component:
		if _, ok := fields["declarative"]; ok {
			return ErrInvalidContribution
		}
		if _, ok := fields["handler"]; ok {
			return ErrInvalidContribution
		}
	case Declarative:
		if _, ok := fields["component"]; ok {
			return ErrInvalidContribution
		}
		if _, ok := fields["handler"]; ok {
			return ErrInvalidContribution
		}
	case Handler:
		if _, ok := fields["component"]; ok {
			return ErrInvalidContribution
		}
		if _, ok := fields["declarative"]; ok {
			return ErrInvalidContribution
		}
	}
	if err := optionalStrings(fields, "public_binding", "status_reason"); err != nil {
		return err
	}
	if value.Status == "" {
		return ErrInvalidContribution
	}
	*c = Contribution(value)
	return nil
}

// Present optional strings must be nonempty strings, rather than JSON null.
func optionalStrings(fields map[string]json.RawMessage, names ...string) error {
	for _, name := range names {
		if raw, present := fields[name]; present {
			var value string
			if json.Unmarshal(raw, &value) != nil || value == "" {
				return ErrInvalidContribution
			}
		}
	}
	return nil
}
func (p *Plugin) UnmarshalJSON(raw []byte) error {
	type wire Plugin
	var value wire
	if err := decodeExact(raw, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if err := optionalStrings(fields, "bundle_url", "stylesheet_url"); err != nil {
		return err
	}
	if _, present := fields["bundle_version"]; present && !digest.MatchString(value.BundleVersion) {
		return ErrIntegrity
	}
	if rt, present := fields["runtime"]; present && string(rt) == "null" {
		return ErrRuntime
	}
	*p = Plugin(value)
	return nil
}
func (r *Runtime) UnmarshalJSON(raw []byte) error {
	type wire Runtime
	var value wire
	if err := decodeExact(raw, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return ErrRuntime
	}
	if err := optionalStrings(fields, "min", "max"); err != nil {
		return ErrRuntime
	}
	*r = Runtime(value)
	return nil
}
func (f *Refusal) UnmarshalJSON(raw []byte) error {
	type wire Refusal
	var value wire
	if err := decodeExact(raw, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if required, ok := fields["required"]; !ok || string(required) == "null" {
		return ErrInvalidContribution
	}
	*f = Refusal(value)
	return nil
}

// decodeExact rejects case variants of defined tags before encoding/json can
// bind them case-insensitively. Unknown extension keys remain forward-compatible.
func decodeExact[T any](raw []byte, value *T) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	typ := reflect.TypeOf(value).Elem()
	for i := 0; i < typ.NumField(); i++ {
		tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		for key := range fields {
			if key != tag && strings.EqualFold(key, tag) {
				return ErrInvalidContribution
			}
		}
	}
	return json.Unmarshal(raw, value)
}
func (d *KindDescriptor) UnmarshalJSON(raw []byte) error {
	type wire KindDescriptor
	var value wire
	if err := decodeExact(raw, &value); err != nil {
		return err
	}
	*d = KindDescriptor(value)
	return nil
}
func (d *RegionDescriptor) UnmarshalJSON(raw []byte) error {
	type wire RegionDescriptor
	var value wire
	if err := decodeExact(raw, &value); err != nil {
		return err
	}
	*d = RegionDescriptor(value)
	return nil
}
func (r *ComponentRef) UnmarshalJSON(raw []byte) error {
	type wire ComponentRef
	var value wire
	if err := decodeExact(raw, &value); err != nil {
		return err
	}
	*r = ComponentRef(value)
	return nil
}
func (r *HandlerRef) UnmarshalJSON(raw []byte) error {
	type wire HandlerRef
	var value wire
	if err := decodeExact(raw, &value); err != nil {
		return err
	}
	*r = HandlerRef(value)
	return nil
}
