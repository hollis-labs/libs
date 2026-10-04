// Package nativefiles is a contract/helper leaf for native serializers.
// It validates typed inputs and explicit composition without owning provider
// paths or encodings. It does not read files, credentials or ambient state.
package nativefiles

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Context identifies the resolved target when reporting a refusal.
type Context struct{ Provider, Mode, Concern string }
type Code string

const (
	InvalidInput  Code = "invalid_native_input"
	DuplicateKey  Code = "duplicate_native_key"
	PathCollision Code = "native_path_collision"
)

// Refusal describes an invalid native input without including its values.
type Refusal struct {
	Context
	Code   Code
	Reason string
}

func (r *Refusal) Error() string {
	return fmt.Sprintf("%s: provider=%s mode=%s concern=%s: %s", r.Code, r.Provider, r.Mode, r.Concern, r.Reason)
}
func (c Context) Refuse(code Code, reason string) error {
	return &Refusal{Context: c, Code: code, Reason: reason}
}

// Server is an explicit host MCP binding. Exactly one transport must be set.
// Names, including loopback and mux, are supplied once in this unified list.
type Server struct {
	Name, HTTPURL, Command string
	Args                   []string
	Env                    []Variable
}
type Variable struct{ Name, Value string }

// Slot is an app-supplied native document key. Values must be JSON representable.
// Raw JSON is checked for duplicate keys before decoding.
type Slot struct {
	Key   string
	Value any
}

// Claim associates a destination with its sole serializer owner. Shared paths
// require the same nonempty owner and explicit nonempty composition name.
type Claim struct{ Path, Owner, Composition string }

func ValidateComposition(ctx Context, claims []Claim) error {
	seen := map[string]Claim{}
	for _, c := range claims {
		if c.Path == "" || c.Owner == "" {
			return ctx.Refuse(InvalidInput, "destination and owner must be explicit")
		}
		if old, ok := seen[c.Path]; ok && (c.Composition == "" || old.Composition != c.Composition || old.Owner != c.Owner) {
			return ctx.Refuse(PathCollision, "destination has conflicting serializer claims")
		}
		seen[c.Path] = c
	}
	return nil
}
func ValidateServers(ctx Context, servers []Server) error {
	seen := map[string]bool{}
	for _, s := range servers {
		if !identifier(s.Name) {
			return ctx.Refuse(InvalidInput, "MCP name must contain only letters, digits, underscore or hyphen")
		}
		if seen[s.Name] {
			return ctx.Refuse(DuplicateKey, "MCP server name is repeated")
		}
		seen[s.Name] = true
		if (s.HTTPURL == "") == (s.Command == "") {
			return ctx.Refuse(InvalidInput, "MCP binding must set exactly one transport")
		}
		if s.HTTPURL != "" && (len(s.Args) > 0 || len(s.Env) > 0) {
			return ctx.Refuse(InvalidInput, "HTTP binding cannot carry stdio arguments or environment")
		}
		env := map[string]bool{}
		for _, v := range s.Env {
			if v.Name == "" || strings.ContainsAny(v.Name, "=\x00") {
				return ctx.Refuse(InvalidInput, "invalid MCP environment name")
			}
			if env[v.Name] {
				return ctx.Refuse(DuplicateKey, "MCP environment name is repeated")
			}
			env[v.Name] = true
		}
	}
	return nil
}
func identifier(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// Object merges disjoint native slots recursively. Duplicate keys in any one
// group or raw JSON object refuse, as do conflicting leaves across groups.
// Returned objects do not alias caller inputs.
func Object(ctx Context, groups ...[]Slot) (map[string]any, error) {
	out := map[string]any{}
	for _, slots := range groups {
		next := map[string]any{}
		for _, s := range slots {
			if s.Key == "" {
				return nil, ctx.Refuse(InvalidInput, "native slot key is empty")
			}
			if _, ok := next[s.Key]; ok {
				return nil, ctx.Refuse(DuplicateKey, "native slot key is repeated")
			}
			raw, err := json.Marshal(s.Value)
			if err != nil {
				return nil, ctx.Refuse(InvalidInput, "native slot value is not JSON representable")
			}
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.UseNumber()
			v, err := decode(ctx, dec)
			if err != nil {
				return nil, err
			}
			if _, err = dec.Token(); err != io.EOF {
				return nil, ctx.Refuse(InvalidInput, "native slot contains trailing JSON")
			}
			next[s.Key] = v
		}
		if err := merge(ctx, out, next); err != nil {
			return nil, err
		}
	}
	return out, nil
}
func decode(ctx Context, d *json.Decoder) (any, error) {
	tok, err := d.Token()
	if err != nil {
		return nil, ctx.Refuse(InvalidInput, "native slot contains invalid JSON")
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	switch delim {
	case '{':
		out := map[string]any{}
		for d.More() {
			k, e := d.Token()
			if e != nil {
				return nil, ctx.Refuse(InvalidInput, "invalid JSON object")
			}
			key, ok := k.(string)
			if !ok {
				return nil, ctx.Refuse(InvalidInput, "invalid JSON key")
			}
			if _, exists := out[key]; exists {
				return nil, ctx.Refuse(DuplicateKey, "native JSON object key is repeated")
			}
			v, e := decode(ctx, d)
			if e != nil {
				return nil, e
			}
			out[key] = v
		}
		if _, err = d.Token(); err != nil {
			return nil, ctx.Refuse(InvalidInput, "unterminated JSON object")
		}
		return out, nil
	case '[':
		out := []any{}
		for d.More() {
			v, e := decode(ctx, d)
			if e != nil {
				return nil, e
			}
			out = append(out, v)
		}
		if _, err = d.Token(); err != nil {
			return nil, ctx.Refuse(InvalidInput, "unterminated JSON array")
		}
		return out, nil
	default:
		return nil, ctx.Refuse(InvalidInput, "unexpected JSON delimiter")
	}
}
func merge(ctx Context, dst, src map[string]any) error {
	keys := make([]string, 0, len(src))
	for k := range src {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := src[k]
		if old, exists := dst[k]; exists {
			a, aok := old.(map[string]any)
			b, bok := v.(map[string]any)
			if !aok || !bok {
				return ctx.Refuse(DuplicateKey, "native document leaf is claimed more than once")
			}
			if err := merge(ctx, a, b); err != nil {
				return err
			}
		} else {
			dst[k] = v
		}
	}
	return nil
}
