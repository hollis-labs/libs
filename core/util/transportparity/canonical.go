package transportparity

import (
	"bytes"
	"encoding/json"
	"math/big"
	"sort"
	"strconv"
	"strings"
)

// CanonicalJSON reduces v to a canonical JSON string for equality comparison.
// It is NOT the marshaled bytes: v is marshaled, decoded into generic values
// and marshaled again, so that
//
//   - object keys come out sorted, whether v was a struct (field order) or a map;
//   - a pointer and the value it points to are the same;
//   - numbers are normalized: an integral number is written as an integer (1, 1.0
//     and 1e0 are all "1"), any other number as its shortest float64 form. Two
//     distinct non-integral numbers that round to the same float64 compare equal;
//   - json.RawMessage and []byte-of-JSON fields are compared by content.
//
// A value that cannot be marshaled fails t.
func CanonicalJSON(t T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("transportparity: CanonicalJSON: cannot marshal %T: %v", v, err)
		return ""
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		t.Fatalf("transportparity: CanonicalJSON: cannot decode %s: %v", raw, err)
		return ""
	}
	var sb strings.Builder
	writeCanonical(&sb, generic)
	return sb.String()
}

func writeCanonical(sb *strings.Builder, v any) {
	switch x := v.(type) {
	case nil:
		sb.WriteString("null")
	case bool:
		sb.WriteString(strconv.FormatBool(x))
	case string:
		b, _ := json.Marshal(x)
		sb.Write(b)
	case json.Number:
		sb.WriteString(canonicalNumber(x))
	case []any:
		sb.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				sb.WriteByte(',')
			}
			writeCanonical(sb, e)
		}
		sb.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		sb.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				sb.WriteByte(',')
			}
			kb, _ := json.Marshal(k)
			sb.Write(kb)
			sb.WriteByte(':')
			writeCanonical(sb, x[k])
		}
		sb.WriteByte('}')
	default: // unreachable for decoded JSON; keep the output honest anyway
		b, _ := json.Marshal(x)
		sb.Write(b)
	}
}

func canonicalNumber(n json.Number) string {
	r, ok := new(big.Rat).SetString(string(n))
	if !ok {
		return string(n)
	}
	if r.IsInt() {
		return r.Num().String()
	}
	f, _ := r.Float64()
	return strconv.FormatFloat(f, 'g', -1, 64)
}
