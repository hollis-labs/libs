package manifest_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hollis-labs/plugin-sdk/manifest"
)

func TestHookSchemaDigestPresenceAndOpaqueRoundtrip(t *testing.T) {
	for _, digest := range []*string{nil, new("catalog-v2"), new("SHA256:ABC/opaque+value="), new("  Résumé:二\n ")} {
		m := nodeExample()
		m.Hooks = []manifest.Hook{{Name: "session.end", Mode: "sequential", Timeout: 1000, OnError: "open", SchemaDigest: digest}}
		var out bytes.Buffer
		if err := manifest.Encode(&out, m); err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(out.Bytes(), []byte(`"schema_digest"`)) != (digest != nil) {
			t.Fatal("option presence lost")
		}
		got, err := manifest.Decode(&out)
		if err != nil {
			t.Fatal(err)
		}
		actual := got.Hooks[0].SchemaDigest
		if (actual == nil) != (digest == nil) || (digest != nil && *actual != *digest) {
			t.Fatal("opaque token changed")
		}
	}
}

func TestHookSchemaDigestStrictDecoding(t *testing.T) {
	m := nodeExample()
	m.Hooks = []manifest.Hook{{Name: "session.end", Mode: "sequential", Timeout: 1000, OnError: "open"}}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		`"schema_digest":null`, `"schema_digest":true`, `"schema_digest":42`,
		`"schema_digest":{}`, `"schema_digest":[]`, `"schema_digest":""`,
		`"schema_digest":" \t\n"`, `"schema_digest":"\u0085\u2003"`,
		`"Schema_Digest":"opaque"`, `"schemaDigest":"opaque"`,
		`"schema_digest":"one","schema_digest":"two"`,
		`"schema_digest":"one","schema_\u0064igest":"two"`,
	} {
		t.Run(field, func(t *testing.T) {
			bad := strings.Replace(string(raw), `"on_error":"open"`, `"on_error":"open",`+field, 1)
			if _, err := manifest.Decode(strings.NewReader(bad)); err == nil {
				t.Fatal("accepted malformed digest")
			}
		})
	}
	for _, digest := range []string{"", " \t\n", "\u0085\u2003"} {
		m.Hooks[0].SchemaDigest = new(digest)
		if err := m.Validate(); err == nil {
			t.Fatal("accepted blank digest")
		}
		if err := manifest.Encode(&bytes.Buffer{}, m); err == nil {
			t.Fatal("encoded blank digest")
		}
	}
}
