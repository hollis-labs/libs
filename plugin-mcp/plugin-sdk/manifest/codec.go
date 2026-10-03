package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
)

// MaxBytes bounds a manifest (including inline schemas and host extensions).
const MaxBytes = 1 << 20

// Decode reads exactly one generated manifest. Unknown fields and duplicate
// JSON keys are errors, including duplicates inside opaque extension objects:
// review and execution must not read two different declarations from one file.
// Legacy host dialects and future schema versions are deliberately refused.
func Decode(r io.Reader) (Manifest, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if err != nil {
		return Manifest{}, fmt.Errorf("manifest: read: %w", err)
	}
	if len(raw) > MaxBytes {
		return Manifest{}, fmt.Errorf("manifest exceeds %d bytes", MaxBytes)
	}
	if err := checkJSON(raw); err != nil {
		return Manifest{}, err
	}
	if err := exactFields(raw, reflect.TypeFor[Manifest]()); err != nil {
		return Manifest{}, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var m Manifest
	if err := d.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("manifest: decode: %w", err)
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// Encode validates and writes a canonical declaration as indented JSON with a
// trailing newline. It is valid plugin.yaml content. It does not write secrets,
// generate registrations, or inspect the plugin executable.
func Encode(w io.Writer, m Manifest) error {
	if err := m.Validate(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("manifest: encode: %w", err)
	}
	raw = append(raw, '\n')
	if len(raw) > MaxBytes {
		return fmt.Errorf("manifest exceeds %d bytes", MaxBytes)
	}
	if err := checkJSON(raw); err != nil {
		return err
	}
	n, err := w.Write(raw)
	if err != nil {
		return err
	}
	if n != len(raw) {
		return io.ErrShortWrite
	}
	return nil
}

func checkJSON(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := jsonValue(d, 0); err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("manifest: expected exactly one JSON value")
	}
	return nil
}

func jsonValue(d *json.Decoder, depth int) error {
	if depth > 64 {
		return fmt.Errorf("JSON nesting exceeds 64 levels")
	}
	t, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			t, err := d.Token()
			if err != nil {
				return err
			}
			key, ok := t.(string)
			if !ok {
				return fmt.Errorf("expected object key")
			}
			if seen[key] {
				return fmt.Errorf("duplicate JSON key %q", key)
			}
			seen[key] = true
			if err := jsonValue(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := jsonValue(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
	_, err = d.Token()
	return err
}

// encoding/json also accepts case-insensitive aliases of struct fields. Refuse
// those aliases so a declaration cannot override a reviewed field through an
// alternate spelling. Raw host extensions and schema objects stay opaque.
func exactFields(raw []byte, typ reflect.Type) error {
	if typ != reflect.TypeFor[json.RawMessage]() && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("manifest: null is not valid for a declared field")
	}
	if typ.Kind() == reflect.Pointer {
		return exactFields(raw, typ.Elem())
	}
	if typ == reflect.TypeFor[json.RawMessage]() {
		return nil
	}
	switch typ.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		allowed := map[string]reflect.Type{}
		for i := range typ.NumField() {
			field := typ.Field(i)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name != "" && name != "-" {
				allowed[name] = field.Type
			}
		}
		for _, name := range sortedKeys(fields) {
			fieldType, ok := allowed[name]
			if !ok {
				return fmt.Errorf("manifest: unknown field %q", name)
			}
			if err := exactFields(fields[name], fieldType); err != nil {
				return err
			}
		}
	case reflect.Map:
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		for _, name := range sortedKeys(fields) {
			if err := exactFields(fields[name], typ.Elem()); err != nil {
				return err
			}
		}
	case reflect.Slice:
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return err
		}
		for _, value := range values {
			if err := exactFields(value, typ.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

// DecodeExtension decodes a host-owned extension into a non-nil pointer to a
// struct, applying the same size, nesting, duplicate-key and exact-field checks
// as Decode. It leaves dst unchanged on failure. Hosts must separately validate
// the decoded value's meaning before applying registrations or granting access.
func DecodeExtension(raw json.RawMessage, dst any) error {
	v := reflect.ValueOf(dst)
	if v.Kind() != reflect.Pointer || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("manifest: extension destination must be a non-nil pointer to a struct")
	}
	if len(raw) > MaxBytes {
		return fmt.Errorf("manifest exceeds %d bytes", MaxBytes)
	}
	if !object(raw) {
		return fmt.Errorf("manifest: extension must be an object")
	}
	if err := checkJSON(raw); err != nil {
		return err
	}
	if err := exactFields(raw, v.Elem().Type()); err != nil {
		return err
	}
	tmp := reflect.New(v.Elem().Type())
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(tmp.Interface()); err != nil {
		return err
	}
	v.Elem().Set(tmp.Elem())
	return nil
}
