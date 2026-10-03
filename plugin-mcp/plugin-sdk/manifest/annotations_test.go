package manifest_test

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/hollis-labs/plugin-sdk/manifest"
)

func TestAnnotationFixtures(t *testing.T) {
	for _, name := range []string{"without-annotations", "with-annotations"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/annotations/" + name + ".json")
			if err != nil {
				t.Fatal(err)
			}
			m, err := manifest.Decode(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := manifest.Encode(&out, m); err != nil {
				t.Fatal(err)
			}
			got, err := manifest.Decode(&out)
			if err != nil || !reflect.DeepEqual(got, m) {
				t.Fatalf("roundtrip: %v, got %#v", err, got)
			}
			if name == "without-annotations" {
				if m.Tools[0].Annotations != nil || bytes.Contains(out.Bytes(), []byte(`"annotations"`)) {
					t.Fatal("omitted annotations acquired defaults")
				}
			} else {
				a := m.Tools[1].Annotations
				if a == nil || a.ReadOnly == nil || *a.ReadOnly || a.Destructive == nil || *a.Destructive || a.Idempotent == nil || !*a.Idempotent || a.OpenWorld == nil || *a.OpenWorld {
					t.Fatalf("explicit hints lost: %#v", a)
				}
			}
		})
	}
}

func TestAnnotationConsistency(t *testing.T) {
	// Each row specifies the allowed explicit boolean values. Omission is
	// always valid, including for custom host-defined effects.
	rows := []struct {
		effect  string
		allowed map[string][]bool
	}{
		{"read", map[string][]bool{"readOnly": {true}, "destructive": {false}, "idempotent": {false}, "openWorld": {false, true}}},
		{"write", map[string][]bool{"readOnly": {false}, "destructive": {false}, "idempotent": {false, true}, "openWorld": {false, true}}},
		{"destructive", map[string][]bool{"readOnly": {false}, "destructive": {true}, "idempotent": {false}, "openWorld": {false, true}}},
		{"custom_host.effect", map[string][]bool{"openWorld": {false, true}}},
	}
	for _, row := range rows {
		for _, field := range []string{"readOnly", "destructive", "idempotent", "openWorld"} {
			for _, value := range []string{"omitted", "false", "true"} {
				t.Run(row.effect+"/"+field+"/"+value, func(t *testing.T) {
					m := example()
					m.Tools[0].Effect = row.effect
					m.Tools[0].Annotations = &manifest.ToolAnnotations{}
					valid := value == "omitted"
					if !valid {
						if err := json.Unmarshal([]byte(`{"`+field+`":`+value+`}`), m.Tools[0].Annotations); err != nil {
							t.Fatal(err)
						}
						for _, allowed := range row.allowed[field] {
							valid = valid || allowed == (value == "true")
						}
					}
					var out bytes.Buffer
					err := manifest.Encode(&out, m)
					if valid {
						if err != nil {
							t.Fatal(err)
						}
						if _, err := manifest.Decode(&out); err != nil {
							t.Fatal(err)
						}
					} else if err == nil || !strings.Contains(err.Error(), m.Tools[0].Name+").annotations."+field) || out.Len() != 0 {
						t.Fatalf("expected named refusal without output, got %v", err)
					}
				})
			}
		}
	}
}

func TestAnnotationDecodeRefusesMalformedHints(t *testing.T) {
	for _, annotations := range []string{`{"readOnly":"true"}`, `{"openWorld":1}`, `{"title":"Notes"}`, `{"ReadOnly":true}`, `{"readOnly":true,"readOnly":false}`, `[]`, `{"readOnly":false}`, `{"destructive":true}`, `{"idempotent":true}`, `{"readOnly":true,"destructive":true}`} {
		t.Run(annotations, func(t *testing.T) {
			var out bytes.Buffer
			if err := manifest.Encode(&out, example()); err != nil {
				t.Fatal(err)
			}
			raw := strings.Replace(out.String(), `"effect": "read"`, `"effect": "read", "annotations": `+annotations, 1)
			if got, err := manifest.Decode(strings.NewReader(raw)); err == nil || !reflect.DeepEqual(got, manifest.Manifest{}) {
				t.Fatalf("expected atomic refusal, got %#v, %v", got, err)
			}
		})
	}
}
