// Package nativefiles is a contract/helper leaf for native serializers.
// It validates typed inputs and explicit composition without owning provider
// paths or encodings. It does not read files, credentials or ambient state.
// Provider leaves return bytes only. File modes come from the plan table;
// native config and MCP documents require 0600 except the stable plugin marker.
// Credential destinations are 0600 link effects, never serializer output.
// Pinned package modes preserve declared bits after removing special bits and
// group/other write; absent modes use the table default. Mode diagnostics and
// directory assembly belong to the renderer, rather than these byte encoders.
package nativefiles

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"reflect"
	"unicode/utf8"

	"github.com/hollis-labs/substrate/harness/adapters/internal/owner"
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

// Owner is an opaque serializer handle. Only the restricted adapters internal
// package can mint it; applications receive handles from provider leaves.
type Owner = owner.Owner

// Claim is sealed: its origin cannot be changed by an overlay caller.
type Claim struct {
	path        string
	owner       Owner
	composition string
	overlay     bool
}

// Overlay is application-supplied claim data. Owner is never trusted.
type Overlay struct {
	Path  string
	Owner Owner
}

func OverlayClaim(in Overlay) Claim { return Claim{path: in.Path, owner: in.Owner, overlay: true} }
func SerializerClaim(path string, owner Owner, composition string) Claim {
	return Claim{path: path, owner: owner, composition: composition}
}

// ValidateComposition canonicalizes relative destinations before comparison.
// Reserved native destinations reject overlays even with no emitted document.
// Overlay origins never compose with serializer origins or acquire ownership.
func ValidateComposition(ctx Context, claims []Claim, reserved []string) error {
	reservedPaths := []string{}
	for _, rel := range reserved {
		normalized, err := claimPath(ctx, rel)
		if err != nil {
			return err
		}
		reservedPaths = append(reservedPaths, normalized)
	}
	seen := map[string]Claim{}
	for _, c := range claims {
		normalized, err := claimPath(ctx, c.path)
		if err != nil {
			return err
		}
		c.path = normalized
		if !c.overlay && !c.owner.Valid() {
			return ctx.Refuse(InvalidInput, "serializer owner must be an issued handle")
		}
		if c.overlay {
			for _, rel := range reservedPaths {
				if normalized == rel || strings.HasPrefix(normalized, rel+"/") {
					return ctx.Refuse(PathCollision, "overlay claims a reserved native destination")
				}
			}
		}
		if old, ok := seen[normalized]; ok && (c.overlay || old.overlay || c.composition == "" || old.composition != c.composition || old.owner != c.owner) {
			return ctx.Refuse(PathCollision, "destination has conflicting serializer claims")
		}
		seen[normalized] = c
	}
	return nil
}
func claimPath(ctx Context, rel string) (string, error) {
	if err := ValidateValue(ctx, rel); err != nil {
		return "", err
	}
	if strings.TrimSpace(rel) == "" || path.IsAbs(rel) || strings.ContainsAny(rel, "\\:\x00\r\n") {
		return "", ctx.Refuse(InvalidInput, "claim destination must be a safe relative path")
	}
	for _, part := range strings.Split(rel, "/") {
		if part == ".." {
			return "", ctx.Refuse(InvalidInput, "claim destination cannot contain traversal")
		}
	}
	normalized := path.Clean(rel)
	if normalized == "." {
		return "", ctx.Refuse(InvalidInput, "claim destination must name an entry")
	}
	return normalized, nil
}

func ValidateServers(ctx Context, servers []Server) error {
	if err := ValidateValue(ctx, servers); err != nil {
		return err
	}
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
			if err := ValidateValue(ctx, s.Key); err != nil {
				return nil, err
			}
			if err := ValidateValue(ctx, s.Value); err != nil {
				return nil, err
			}
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
			if !utf8.Valid(raw) {
				return nil, ctx.Refuse(InvalidInput, "native slot contains invalid UTF-8")
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

// ValidateValue rejects invalid UTF-8 before JSON encoders can silently replace
// it. It checks nested keys and values, including raw JSON, without printing them.
func ValidateValue(ctx Context, value any) error {
	type visit struct {
		kind   reflect.Kind
		ptr    uintptr
		typ    reflect.Type
		length int
	}
	seen := map[visit]bool{}
	var valid func(reflect.Value) bool
	valid = func(v reflect.Value) bool {
		if !v.IsValid() {
			return true
		}
		if v.Type() == reflect.TypeFor[json.RawMessage]() {
			return utf8.Valid(v.Bytes())
		}
		switch v.Kind() {
		case reflect.String:
			return utf8.ValidString(v.String())
		case reflect.Interface:
			if v.IsNil() {
				return true
			}
			return valid(v.Elem())
		case reflect.Pointer, reflect.Map, reflect.Slice:
			if v.IsNil() {
				return true
			}
			length := 0
			if v.Kind() == reflect.Slice {
				length = v.Len()
			}
			id := visit{kind: v.Kind(), ptr: v.Pointer(), typ: v.Type(), length: length}
			if seen[id] {
				return true
			}
			seen[id] = true
			if v.Kind() == reflect.Pointer {
				return valid(v.Elem())
			}
			if v.Kind() == reflect.Map {
				iter := v.MapRange()
				for iter.Next() {
					if !valid(iter.Key()) || !valid(iter.Value()) {
						return false
					}
				}
				return true
			}
			for i := 0; i < v.Len(); i++ {
				if !valid(v.Index(i)) {
					return false
				}
			}
			return true
		case reflect.Array:
			for i := 0; i < v.Len(); i++ {
				if !valid(v.Index(i)) {
					return false
				}
			}
			return true
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				f := v.Type().Field(i)
				if f.PkgPath == "" && f.Tag.Get("json") != "-" && !valid(v.Field(i)) {
					return false
				}
			}
			return true
		}
		return true
	}
	if !valid(reflect.ValueOf(value)) {
		return ctx.Refuse(InvalidInput, "native input contains invalid UTF-8")
	}
	return nil
}

// ValidateDirectories refuses relative or traversal-bearing native grants.
// Filesystem existence and authorization are the caller's preparation concern.
func ValidateDirectories(ctx Context, dirs []string) error {
	if err := ValidateValue(ctx, dirs); err != nil {
		return err
	}
	for _, dir := range dirs {
		if !path.IsAbs(dir) || strings.ContainsAny(dir, "\\\x00\r\n") {
			return ctx.Refuse(InvalidInput, "directory grants must be absolute")
		}
		for _, part := range strings.Split(dir, "/") {
			if part == ".." {
				return ctx.Refuse(InvalidInput, "directory grants cannot contain traversal")
			}
		}
	}
	return nil
}
