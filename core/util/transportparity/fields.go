package transportparity

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"time"
)

// AcceptedFieldNames returns every dotted JSON field path the type of v accepts,
// sorted (nil for a nil v or a type that is not a struct), derived by reflection: "title", "owner.id", and so on. v is only used
// for its type. It follows encoding/json's naming (the json tag name, else the Go
// field name; "-" is skipped) and descends into nested structs and pointers to
// structs, but not into a type that serializes as one value (time.Time, anything
// implementing json.Marshaler such as json.RawMessage). Fields of an embedded
// struct with no json tag name are promoted to the parent, as encoding/json does.
//
// Slices, arrays and maps are not descended into: a path through a collection
// has no single spelling, so a caller who needs one compares those element types
// with their own AcceptedFieldNames call.
func AcceptedFieldNames(v any) []string {
	if v == nil {
		return nil // reflect.TypeOf(nil) is nil; there is no type to walk
	}
	var out []string
	seen := map[string]bool{}
	var walk func(t reflect.Type, prefix string)
	walk = func(t reflect.Type, prefix string) {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct {
			return
		}
		for i := range t.NumField() {
			f := t.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "-" && f.Tag.Get("json") == "-" {
				continue
			}
			// An embedded struct with no json name is flattened into the parent.
			if f.Anonymous && name == "" && isStruct(f.Type) {
				walk(f.Type, prefix)
				continue
			}
			// encoding/json still processes an unexported EMBEDDED struct (its
			// exported fields are emitted, under the tag name if there is one).
			if !f.IsExported() && (!f.Anonymous || !isStruct(f.Type)) {
				continue
			}
			if name == "" {
				name = f.Name
			}
			path := name
			if prefix != "" {
				path = prefix + "." + name
			}
			if !seen[path] {
				seen[path] = true
				out = append(out, path)
			}
			if descendsIntoJSONObject(f.Type) {
				walk(f.Type, path)
			}
		}
	}
	walk(reflect.TypeOf(v), "")
	sort.Strings(out)
	return out
}

// isStruct reports whether t is a struct or a pointer to one.
func isStruct(t reflect.Type) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Kind() == reflect.Struct
}

// descendsIntoJSONObject reports whether encoding/json renders this type as an
// object built from its own fields, the only case where a sub-path exists.
// json.RawMessage and time.Time serialize as a single value, so recursing into
// them would invent paths no caller can send.
func descendsIntoJSONObject(t reflect.Type) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || t == reflect.TypeOf(time.Time{}) {
		return false
	}
	return !reflect.PointerTo(t).Implements(reflect.TypeOf((*json.Marshaler)(nil)).Elem())
}

// AssertSameFieldNames fails t, naming each field, unless two AcceptedFieldNames
// results are the same set. labelA and labelB name the two doors in the message.
func AssertSameFieldNames(t T, labelA, labelB string, a, b []string) {
	t.Helper()
	inA, inB := map[string]bool{}, map[string]bool{}
	for _, n := range a {
		inA[n] = true
	}
	for _, n := range b {
		inB[n] = true
	}
	var onlyA, onlyB []string
	for n := range inA {
		if !inB[n] {
			onlyA = append(onlyA, n)
		}
	}
	for n := range inB {
		if !inA[n] {
			onlyB = append(onlyB, n)
		}
	}
	sort.Strings(onlyA)
	sort.Strings(onlyB)
	if len(onlyA) > 0 || len(onlyB) > 0 {
		t.Errorf("accepted fields differ between %s and %s:\n  only %s: %v\n  only %s: %v", labelA, labelB, labelA, onlyA, labelB, onlyB)
	}
}
