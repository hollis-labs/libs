package subprocess

import (
	"encoding"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The encoder retains at most the configured JSON budget. It never calls
// json.Marshal on the complete authored result or an unbounded string. A
// plugin's own MarshalJSON may allocate; its returned bytes are checked before
// the SDK copies them. Transport cannot bound allocations inside plugin code.
type frameEncoder struct {
	data  []byte
	limit int
}

func (e *frameEncoder) Write(p []byte) (int, error) {
	if len(p) > e.limit-len(e.data) {
		return 0, &FrameTooLargeError{Direction: "output", Limit: e.limit + 1}
	}
	e.data = append(e.data, p...)
	return len(p), nil
}
func (e *frameEncoder) text(s string) error { _, err := e.Write([]byte(s)); return err }
func (e *frameEncoder) quoted(s string) error {
	if !utf8.ValidString(s) {
		return errors.New("invalid outbound UTF-8")
	}
	if len(s) > e.limit-len(e.data)-2 {
		return &FrameTooLargeError{Direction: "output", Limit: e.limit + 1}
	}
	if err := e.text(`"`); err != nil {
		return err
	}
	start := 0
	for i, r := range s {
		var escape string
		switch r {
		case '"':
			escape = `\"`
		case '\\':
			escape = `\\`
		case '\n':
			escape = `\n`
		case '\r':
			escape = `\r`
		case '\t':
			escape = `\t`
		case '\b':
			escape = `\b`
		case '\f':
			escape = `\f`
		default:
			if r < 0x20 {
				escape = fmt.Sprintf(`\u%04x`, r)
			}
		}
		if escape != "" {
			if err := e.text(s[start:i]); err != nil {
				return err
			}
			if err := e.text(escape); err != nil {
				return err
			}
			start = i + utf8.RuneLen(r)
		}
	}
	if err := e.text(s[start:]); err != nil {
		return err
	}
	return e.text(`"`)
}
func marshalBounded(value any, maxBytes int) ([]byte, error) {
	e := frameEncoder{limit: maxBytes}
	if maxBytes < 0 {
		return nil, &FrameTooLargeError{Direction: "output", Limit: maxBytes + 1}
	}
	if err := e.value(reflect.ValueOf(value), 0); err != nil {
		return nil, err
	}
	return e.data, nil
}
func (e *frameEncoder) value(v reflect.Value, depth int) error {
	if depth > 128 {
		return errors.New("outbound JSON nesting exceeds limit")
	}
	if !v.IsValid() {
		return e.text("null")
	}
	if (v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer) && v.IsNil() {
		return e.text("null")
	}
	if v.Kind() == reflect.Interface {
		return e.value(v.Elem(), depth)
	}
	if v.CanInterface() {
		switch x := v.Interface().(type) {
		case RPCID:
			if s, ok := x.Text(); ok {
				return e.quoted(s)
			}
			if n, ok := x.Integer(); ok {
				if n < -maxRPCInteger || n > maxRPCInteger {
					return errors.New("unsafe RPC integer ID")
				}
				return e.text(strconv.FormatInt(n, 10))
			}
			return e.text("null")
		case InitResult:
			if err := x.Validate(); err != nil {
				return err
			}
			type plain InitResult
			return e.value(reflect.ValueOf(plain(x)), depth)
		case *InitResult:
			if x == nil {
				return e.text("null")
			}
			return e.value(reflect.ValueOf(*x), depth)
		case json.RawMessage:
			if x == nil {
				return e.text("null")
			}
			return e.rawJSON(x)
		case json.Number:
			s := string(x)
			if s == "" {
				s = "0"
			}
			if len(s) > e.limit-len(e.data) {
				return &FrameTooLargeError{Direction: "output", Limit: e.limit + 1}
			}
			if !json.Valid([]byte(s)) || strings.ContainsAny(s, `"{}[]`) {
				return errors.New("invalid JSON number")
			}
			if _, err := strconv.ParseFloat(s, 64); err != nil {
				return errors.New("invalid JSON number")
			}
			return e.text(s)
		case json.Marshaler:
			raw, err := x.MarshalJSON()
			if err != nil {
				return errors.New("outbound marshaler failed")
			}
			return e.rawJSON(raw)
		case encoding.TextMarshaler:
			raw, err := x.MarshalText()
			if err != nil {
				return errors.New("outbound text marshaler failed")
			}
			if len(raw) > e.limit-len(e.data)-2 {
				return &FrameTooLargeError{Direction: "output", Limit: e.limit + 1}
			}
			return e.quoted(string(raw))
		}
	}
	switch v.Kind() {
	case reflect.Pointer:
		return e.value(v.Elem(), depth+1)
	case reflect.String:
		return e.quoted(v.String())
	case reflect.Bool:
		return e.text(strconv.FormatBool(v.Bool()))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return e.text(strconv.FormatInt(v.Int(), 10))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return e.text(strconv.FormatUint(v.Uint(), 10))
	case reflect.Float32, reflect.Float64:
		f := v.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return errors.New("nonfinite outbound number")
		}
		return e.text(strconv.FormatFloat(f, 'g', -1, v.Type().Bits()))
	case reflect.Slice:
		if v.IsNil() {
			return e.text("null")
		}
		if v.Type().Elem().Kind() == reflect.Uint8 {
			if err := e.text(`"`); err != nil {
				return err
			}
			enc := base64.NewEncoder(base64.StdEncoding, e)
			if _, err := enc.Write(v.Bytes()); err != nil {
				return err
			}
			if err := enc.Close(); err != nil {
				return err
			}
			return e.text(`"`)
		}
		fallthrough
	case reflect.Array:
		if err := e.text("["); err != nil {
			return err
		}
		for i := 0; i < v.Len(); i++ {
			if i > 0 {
				if err := e.text(","); err != nil {
					return err
				}
			}
			if err := e.value(v.Index(i), depth+1); err != nil {
				return err
			}
		}
		return e.text("]")
	case reflect.Map:
		if v.IsNil() {
			return e.text("null")
		}
		if err := e.text("{"); err != nil {
			return err
		}
		first := true
		iter := v.MapRange()
		for iter.Next() {
			key := iter.Key()
			var name string
			switch key.Kind() {
			case reflect.String:
				name = key.String()
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				name = strconv.FormatInt(key.Int(), 10)
			case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
				name = strconv.FormatUint(key.Uint(), 10)
			default:
				return errors.New("unsupported JSON map key")
			}
			if !first {
				if err := e.text(","); err != nil {
					return err
				}
			}
			first = false
			if err := e.quoted(name); err != nil {
				return err
			}
			if err := e.text(":"); err != nil {
				return err
			}
			if err := e.value(iter.Value(), depth+1); err != nil {
				return err
			}
		}
		return e.text("}")
	case reflect.Struct:
		if err := e.text("{"); err != nil {
			return err
		}
		first := true
		if err := e.fields(v, depth, &first); err != nil {
			return err
		}
		return e.text("}")
	default:
		return errors.New("unserializable outbound value")
	}
}
func emptyJSON(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr, reflect.Float32, reflect.Float64, reflect.Interface, reflect.Pointer:
		return v.IsZero()
	}
	return false
}

type frameField struct {
	value                reflect.Value
	name                 string
	depth                int
	tagged, omit, quoted bool
}

func (e *frameEncoder) fields(v reflect.Value, depth int, first *bool) error {
	fields := make(map[string][]frameField)
	var collect func(reflect.Value, int) error
	collect = func(obj reflect.Value, level int) error {
		if level+depth > 128 {
			return errors.New("outbound JSON nesting exceeds limit")
		}
		for i := 0; i < obj.NumField(); i++ {
			f := obj.Type().Field(i)
			value := obj.Field(i)
			tag := strings.Split(f.Tag.Get("json"), ",")
			if tag[0] == "-" {
				continue
			}
			embedded := value
			if f.Anonymous && tag[0] == "" && embedded.Kind() == reflect.Pointer && embedded.IsNil() && embedded.Type().Elem().Kind() == reflect.Struct {
				continue
			}
			if embedded.Kind() == reflect.Pointer && !embedded.IsNil() {
				embedded = embedded.Elem()
			}
			if f.Anonymous && tag[0] == "" && embedded.Kind() == reflect.Struct {
				if err := collect(embedded, level+1); err != nil {
					return err
				}
				continue
			}
			if f.PkgPath != "" {
				continue
			}
			name := tag[0]
			if name == "" {
				name = f.Name
			}
			item := frameField{value: value, name: name, depth: level, tagged: tag[0] != ""}
			for _, option := range tag[1:] {
				item.omit = item.omit || option == "omitempty"
				item.quoted = item.quoted || option == "string"
			}
			fields[name] = append(fields[name], item)
		}
		return nil
	}
	if err := collect(v, 0); err != nil {
		return err
	}
	for name, candidates := range fields {
		min := candidates[0].depth
		for _, f := range candidates {
			if f.depth < min {
				min = f.depth
			}
		}
		var matches []frameField
		tagged := false
		for _, f := range candidates {
			if f.depth == min && f.tagged {
				tagged = true
			}
		}
		for _, f := range candidates {
			if f.depth == min && (!tagged || f.tagged) {
				matches = append(matches, f)
			}
		}
		// encoding/json omits ambiguous promoted names.
		if len(matches) != 1 {
			continue
		}
		f := matches[0]
		value := f.value
		if f.omit && emptyJSON(value) {
			continue
		}
		if !*first {
			if err := e.text(","); err != nil {
				return err
			}
		}
		*first = false
		if err := e.quoted(name); err != nil {
			return err
		}
		if err := e.text(":"); err != nil {
			return err
		}
		scalar := value
		if scalar.Kind() == reflect.Pointer && !scalar.IsNil() {
			scalar = scalar.Elem()
		}
		quote := f.quoted && (scalar.Kind() == reflect.String || scalar.Kind() == reflect.Bool || scalar.Kind() >= reflect.Int && scalar.Kind() <= reflect.Float64)
		if quote {
			raw, err := marshalBounded(value.Interface(), e.limit-len(e.data))
			if err != nil {
				return err
			}
			if err = e.quoted(string(raw)); err != nil {
				return err
			}
		} else if err := e.value(value, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// A complete JSON value is staged before it is submitted to the writer.
// The writer never sees bytes belonging to a rejected oversized response.
func (s *server) encodeFrame(value any) ([]byte, error) {
	limit := s.outputLimit
	if limit == 0 {
		limit = DefaultFrameBytes
	}
	data, err := marshalBounded(value, limit-1)
	if err != nil {
		var large *FrameTooLargeError
		if errors.As(err, &large) {
			return nil, &FrameTooLargeError{Direction: "output", Limit: limit}
		}
		return nil, err
	}
	return append(data, '\n'), nil
}

// Compact whitespace without copying an unbounded authored RawMessage. Literal
// LF/CR whitespace must never escape into the newline-delimited transport.
func (e *frameEncoder) rawJSON(raw []byte) error {
	if !utf8.Valid(raw) || !json.Valid(raw) {
		return errors.New("invalid outbound raw JSON")
	}
	quoted, escaped := false, false
	start := 0
	for i, b := range raw {
		if quoted {
			if escaped {
				escaped = false
			} else if b == '\\' {
				escaped = true
			} else if b == '"' {
				quoted = false
			}
		} else if b == '"' {
			quoted = true
		} else if b == ' ' || b == '\n' || b == '\r' || b == '\t' {
			if _, err := e.Write(raw[start:i]); err != nil {
				return err
			}
			start = i + 1
		}
	}
	_, err := e.Write(raw[start:])
	return err
}
