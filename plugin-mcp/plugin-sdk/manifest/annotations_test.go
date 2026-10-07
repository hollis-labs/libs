package manifest_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/manifest"
)

func TestAnnotationFixtures(t *testing.T) {
	for _, name := range []string{"without-annotations", "with-annotations"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/annotations/" + name + ".json")
			if err != nil {
				t.Fatal(err)
			}
			var historical manifest.Manifest
			// Annotation fixtures predate the execution schema break; their tool declarations remain valid.
			var envelope struct {
				Tools []manifest.Tool `json:"tools"`
			}
			if err := json.Unmarshal(raw, &envelope); err != nil {
				t.Fatal(err)
			}
			historical = example()
			historical.Tools = envelope.Tools
			adapted, err := json.Marshal(historical)
			if err != nil {
				t.Fatal(err)
			}
			m, err := manifest.Decode(bytes.NewReader(adapted))
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := manifest.Encode(&out, m); err != nil {
				t.Fatal(err)
			}
			got, err := manifest.Decode(&out)
			wantJSON, _ := json.Marshal(m)
			gotJSON, _ := json.Marshal(got)
			if err != nil || !bytes.Equal(wantJSON, gotJSON) {
				t.Fatalf("roundtrip: %v, got %#v", err, got)
			}
			if name == "without-annotations" {
				if m.Tools[0].Annotations != nil || bytes.Contains(out.Bytes(), []byte(`"annotations"`)) {
					t.Fatal("omitted annotations acquired defaults")
				}
			} else {
				a := m.Tools[1].Annotations
				if a == nil || a.Title != "Save notes" || a.ReadOnlyHint == nil || *a.ReadOnlyHint || a.DestructiveHint == nil || *a.DestructiveHint || a.IdempotentHint == nil || !*a.IdempotentHint || a.OpenWorldHint == nil || *a.OpenWorldHint {
					t.Fatalf("explicit hints lost: %#v", a)
				}
			}
		})
	}
}

func TestAnnotationConsistency(t *testing.T) {
	// Effects are an open host vocabulary. Every combination of omitted,
	// false and true hints is valid except read-only plus destructive.
	values := []*bool{nil, new(false), new(true)}
	for _, effect := range []string{"read", "write", "destructive", "custom_host.effect"} {
		for ri, readOnly := range values {
			for di, destructive := range values {
				for ii, idempotent := range values {
					for oi, openWorld := range values {
						t.Run(fmt.Sprintf("%s/%d%d%d%d", effect, ri, di, ii, oi), func(t *testing.T) {
							m := example()
							m.Tools[0].Effect = effect
							m.Tools[0].Annotations = &manifest.ToolAnnotations{
								ReadOnlyHint: readOnly, DestructiveHint: destructive,
								IdempotentHint: idempotent, OpenWorldHint: openWorld,
							}
							var out bytes.Buffer
							err := manifest.Encode(&out, m)
							if ri == 2 && di == 2 {
								if err == nil || !strings.Contains(err.Error(), m.Tools[0].Name+").annotations.readOnlyHint=true cannot accompany destructiveHint=true") || out.Len() != 0 {
									t.Fatalf("expected named refusal without output, got %v", err)
								}
								return
							}
							if err != nil {
								t.Fatal(err)
							}
							got, err := manifest.Decode(&out)
							if err != nil || !reflect.DeepEqual(got.Tools[0].Annotations, m.Tools[0].Annotations) {
								t.Fatalf("annotation roundtrip: %v", err)
							}
						})
					}
				}
			}
		}
	}
}

func TestAnnotationDecodeRefusesMalformedHints(t *testing.T) {
	for _, annotations := range []string{
		`{"readOnlyHint":"true"}`, `{"openWorldHint":1}`, `{"title":true}`,
		`{"ReadOnlyHint":true}`, `{"readOnlyHint":true,"readOnlyHint":false}`,
		`{"title":"a","title":"b"}`, `[]`,
		`{"readOnly":true}`, `{"destructive":false}`, `{"idempotent":true}`, `{"openWorld":false}`,
		`{"readOnlyHint":true,"destructiveHint":true}`,
	} {
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

// The source revision is immutable. This test validates its tool definitions
// through the SDK without requiring a Nanite checkout or executing a binary.
func TestNanitePreCutoverGoldenDefinitions(t *testing.T) {
	raw, err := os.ReadFile("testdata/annotations/nanite-pre-cutover.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		SourceCommit       string `json:"source_commit"`
		BinarySHA256Prefix string `json:"binary_sha256_prefix"`
		Tools              []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
			Annotations json.RawMessage `json:"annotations"`
		} `json:"tools"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.SourceCommit != "2de304e3d1cebe8d875f7806c03ec0eae8f6b8fe" || fixture.BinarySHA256Prefix != "ef20f9be" {
		t.Fatal("golden source provenance changed")
	}
	names := []string{}
	for _, tool := range fixture.Tools {
		names = append(names, tool.Name)
		t.Run(tool.Name, func(t *testing.T) {
			var annotations manifest.ToolAnnotations
			if err := json.Unmarshal(tool.Annotations, &annotations); err != nil {
				t.Fatal(err)
			}
			m := example()
			m.Tools = []manifest.Tool{{
				Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema,
				// The old MCP definition has no effect field. This test uses an opaque
				// host vocabulary value rather than inventing historical metadata.
				Effect: "golden.host-defined", Annotations: &annotations,
			}}
			var out bytes.Buffer
			if err := manifest.Encode(&out, m); err != nil {
				t.Fatal(err)
			}
			got, err := manifest.Decode(&out)
			if err != nil {
				t.Fatal(err)
			}
			if err := got.Validate(); err != nil {
				t.Fatal(err)
			}
			if got.Tools[0].Name != tool.Name || got.Tools[0].Description != tool.Description {
				t.Fatal("golden name or description changed")
			}
			assertJSONEqual := func(want, got []byte) {
				t.Helper()
				var w, g any
				if err := json.Unmarshal(want, &w); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(got, &g); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(w, g) {
					t.Fatalf("golden projection differs: want %s, got %s", want, got)
				}
			}
			assertJSONEqual(tool.InputSchema, got.Tools[0].InputSchema)
			projected, err := json.Marshal(got.Tools[0].Annotations)
			if err != nil {
				t.Fatal(err)
			}
			assertJSONEqual(tool.Annotations, projected)
		})
	}
	if !reflect.DeepEqual(names, []string{"reminder_set", "context_pin", "context_unpin"}) {
		t.Fatalf("unexpected golden tools: %v", names)
	}
}
