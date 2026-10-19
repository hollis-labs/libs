package pluginhooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

// decode preserves JSON numbers rather than rounding integers through float64.
func decode(data json.RawMessage) (any, error) {
	if !utf8.Valid(data) || !json.Valid(data) {
		return nil, ErrInvalidPayload
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	err := dec.Decode(&v)
	return v, err
}
func pointerParts(p string) []string {
	if p == "" {
		return nil
	}
	v := strings.Split(p[1:], "/")
	for i, s := range v {
		v[i] = strings.ReplaceAll(strings.ReplaceAll(s, "~1", "/"), "~0", "~")
	}
	return v
}
func getPath(v any, parts []string) (any, bool) {
	if len(parts) == 0 {
		return v, true
	}
	switch node := v.(type) {
	case map[string]any:
		child, ok := node[parts[0]]
		if !ok {
			return nil, false
		}
		return getPath(child, parts[1:])
	case []any:
		i, err := strconv.Atoi(parts[0])
		if err != nil || i < 0 || i >= len(node) || strconv.Itoa(i) != parts[0] {
			return nil, false
		}
		return getPath(node[i], parts[1:])
	default:
		return nil, false
	}
}

// project preserves array positions with null placeholders for hidden elements.
// Length and container shape are visible; values outside the view are absent.
func project(v any, paths []string) any { return projectAt(v, paths, "") }
func projectAt(v any, paths []string, path string) any {
	if permitted(path, paths) {
		return v
	}
	switch node := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, child := range node {
			p := path + "/" + escapePart(k)
			for _, visible := range paths {
				if pathWithin(visible, p) || pathWithin(p, visible) {
					out[k] = projectAt(child, paths, p)
					break
				}
			}
		}
		return out
	case []any:
		out := make([]any, len(node))
		for i, child := range node {
			p := path + "/" + strconv.Itoa(i)
			for _, visible := range paths {
				if pathWithin(visible, p) || pathWithin(p, visible) {
					out[i] = projectAt(child, paths, p)
					break
				}
			}
		}
		return out
	default:
		return nil
	}
}
func setObjectPath(v any, parts []string, value any, present bool) (any, error) {
	if len(parts) == 0 {
		if !present {
			return nil, nil
		}
		return value, nil
	}
	switch node := v.(type) {
	case map[string]any:
		if len(parts) == 1 {
			if present {
				node[parts[0]] = value
			} else {
				delete(node, parts[0])
			}
			return node, nil
		}
		child, ok := node[parts[0]]
		if !ok {
			child = map[string]any{}
		}
		updated, err := setObjectPath(child, parts[1:], value, present)
		if err != nil {
			return nil, err
		}
		node[parts[0]] = updated
		return node, nil
	case []any:
		i, err := strconv.Atoi(parts[0])
		if err != nil || i < 0 || i >= len(node) || strconv.Itoa(i) != parts[0] {
			return nil, ErrInvalidOutput
		}
		if len(parts) == 1 {
			if !present {
				return nil, ErrInvalidOutput
			}
			node[i] = value
			return node, nil
		}
		updated, err := setObjectPath(node[i], parts[1:], value, present)
		if err != nil {
			return nil, err
		}
		node[i] = updated
		return node, nil
	default:
		return nil, ErrInvalidOutput
	}
}
func pathWithin(path, ancestor string) bool {
	return ancestor == "" || path == ancestor || strings.HasPrefix(path, ancestor+"/")
}
func escapePart(p string) string {
	return strings.ReplaceAll(strings.ReplaceAll(p, "~", "~0"), "/", "~1")
}

// changedPaths descends objects and same-length arrays; array resizing and scalar
// replacements require permission on their containing path.
func changedPaths(a, b any, path string) []string {
	if reflect.DeepEqual(a, b) {
		return nil
	}
	if aa, ok := a.([]any); ok {
		if bb, ok := b.([]any); ok && len(aa) == len(bb) {
			var out []string
			for i := range aa {
				out = append(out, changedPaths(aa[i], bb[i], path+"/"+strconv.Itoa(i))...)
			}
			return out
		}
	}
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)
	if !aok || !bok {
		return []string{path}
	}
	var out []string
	for k, av := range am {
		bv, ok := bm[k]
		p := path + "/" + escapePart(k)
		if !ok {
			out = append(out, p)
		} else {
			out = append(out, changedPaths(av, bv, p)...)
		}
	}
	for k := range bm {
		if _, ok := am[k]; !ok {
			out = append(out, path+"/"+escapePart(k))
		}
	}
	return out
}
func permitted(path string, paths []string) bool {
	for _, p := range paths {
		if pathWithin(path, p) {
			return true
		}
	}
	return false
}
func mergeOutput(full, view, output any, d Definition, viewName string) (any, error) {
	visible := []string{""}
	if viewName != "" {
		visible = d.Views[viewName]
	}
	for _, p := range changedPaths(view, output, "") {
		if !permitted(p, d.MutablePaths) || !permitted(p, visible) {
			return nil, ErrInvalidOutput
		}
	}
	if viewName == "" {
		return output, nil
	}
	// Merge only changed visible paths. Selecting a mutable parent never grants
	// hidden siblings: the visible projection determines the individual changes.
	for _, p := range changedPaths(view, output, "") {
		value, present := getPath(output, pointerParts(p))
		var err error
		full, err = setObjectPath(full, pointerParts(p), value, present)
		if err != nil {
			return nil, err
		}
	}
	return full, nil
}
func encode(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func validateJSON(data json.RawMessage, limit int, validator Validator) (err error) {
	if len(data) > limit || !utf8.Valid(data) || !json.Valid(data) {
		return ErrInvalidPayload
	}
	defer func() {
		if recover() != nil {
			err = ErrInvalidPayload
		}
	}()
	// Host validation is synchronous, bounded, concurrency-safe and non-mutating.
	if err := validator(bytes.Clone(data)); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidPayload, err)
	}
	return nil
}
