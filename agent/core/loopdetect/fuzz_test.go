package loopdetect

import (
	"encoding/json"
	"strings"
	"testing"
)

func FuzzNormalizeArgs(f *testing.F) {
	for _, s := range []string{
		"", "{}", `{"b":1,"a":2}`, "[1,2,3]", `"str"`, "null", "42", "not json",
		`{"a":{"z":1,"y":[1,{"q":null}]}}`, "{\"a\":\"\xff\xfe\"}", "\xff\xfe\x00",
		`{"a":1,"a":2}`, "  {\"a\":1}  ", strings.Repeat("[", 5000) + strings.Repeat("]", 5000),
		strings.Repeat(`{"a":`, 2000) + "1" + strings.Repeat("}", 2000),
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		a := normalizeArgs(json.RawMessage(raw))
		if b := normalizeArgs(json.RawMessage(raw)); a != b {
			t.Fatalf("normalizeArgs not deterministic: %q vs %q", a, b)
		}
		if fp := compute("tool", raw); len(fp) != 16 {
			t.Fatalf("fingerprint %q is not 16 hex chars", fp)
		}
	})
}
