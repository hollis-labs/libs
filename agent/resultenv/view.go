package resultenv

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ModelView is derived from a verified receipt. It never changes the receipt.
// If the redacted payload exceeds maxBytes, it returns a short pointer-only
// view. A too-small budget is an error rather than an unbounded result.
func ModelView(r Receipt, maxBytes int) ([]byte, error) {
	raw, err := r.Envelope()
	if err != nil {
		return nil, err
	}
	var e Envelope
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, err
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	for _, pointer := range e.Redact {
		if !maskPointer(value, pointer) {
			return nil, fmt.Errorf("%w: redaction pointer missing: %s", ErrInvalid, pointer)
		}
	}
	full, err := json.Marshal(map[string]any{"receipt_ref": r.Ref.ID, "envelope": value})
	if err != nil {
		return nil, err
	}
	if len(full) <= maxBytes {
		return full, nil
	}
	compact, err := json.Marshal(map[string]any{"receipt_ref": r.Ref.ID, "outcome": e.Outcome, "truncated": true})
	if err != nil {
		return nil, err
	}
	if len(compact) > maxBytes {
		return nil, fmt.Errorf("%w: model view budget too small", ErrInvalid)
	}
	return compact, nil
}

func maskPointer(root any, pointer string) bool {
	parts := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	var current any = root
	for i, part := range parts {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		last := i == len(parts)-1
		switch node := current.(type) {
		case map[string]any:
			child, ok := node[part]
			if !ok {
				return false
			}
			if last {
				node[part] = "[REDACTED]"
				return true
			}
			current = child
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(node) {
				return false
			}
			if last {
				node[index] = "[REDACTED]"
				return true
			}
			current = node[index]
		default:
			return false
		}
	}
	return false
}
