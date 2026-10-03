package agentdef

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, e := os.ReadFile("testdata/" + name + ".md")
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func definition(t *testing.T) *Definition {
	t.Helper()
	d, e := Parse(fixture(t, "reviewer"))
	if e != nil {
		t.Fatal(e)
	}
	return d
}
func requireCode(t *testing.T, err error, code, field string) {
	t.Helper()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected diagnostics, got %v", err)
	}
	for _, d := range ve.Diagnostics {
		if d.Code == code && (field == "" || d.Field == field) {
			return
		}
	}
	t.Fatalf("missing %s/%s in %+v", code, field, ve.Diagnostics)
}

func TestConformanceFixtures(t *testing.T) {
	for _, name := range []string{"reviewer", "durable"} {
		t.Run(name, func(t *testing.T) {
			d, e := Parse(fixture(t, name))
			if e != nil {
				t.Fatal(e)
			}
			if e = d.Validate(); e != nil {
				t.Fatal(e)
			}
			b, e := Canonical(d)
			if e != nil || !json.Valid(b) {
				t.Fatalf("canonical JSON: %s %v", b, e)
			}
			h, e := Digest(d)
			if e != nil || !digestPattern.MatchString(h) {
				t.Fatalf("digest: %s %v", h, e)
			}
		})
	}
}

func TestStrictParsing(t *testing.T) {
	base := string(fixture(t, "reviewer"))
	tests := map[string]string{
		"legacy_identity":          strings.Replace(base, "name: reviewer", "identity: stable\nname: reviewer", 1),
		"provider":                 strings.Replace(base, "name: reviewer", "provider: example\nname: reviewer", 1),
		"nested_model":             strings.Replace(base, "  purpose: Review repository changes.", "  model: example\n  purpose: Review repository changes.", 1),
		"permission_requests":      strings.Replace(base, "    profile: example-read-only", "    profile: example-read-only\n    requests: [repository.read]", 1),
		"permission_restrictions":  strings.Replace(base, "    profile: example-read-only", "    profile: example-read-only\n    restrictions: [repository.read-only]", 1),
		"permission_policy":        strings.Replace(base, "    profile: example-read-only", "    profile: example-read-only\n    policy: {uri: policy:example, digest: invalid}", 1),
		"unknown_envelope":         strings.Replace(base, "    version: \"1\"", "    surprise: true\n    version: \"1\"", 1),
		"duplicate":                strings.Replace(base, "name: reviewer", "name: reviewer\nname: another", 1),
		"duplicate_extension_data": strings.Replace(base, "      max_findings: 10", "      max_findings: 10\n      max_findings: 11", 1),
		"alias":                    strings.Replace(base, "      labels: [security, correctness]", "      labels: &labels [security, correctness]\n      other: *labels", 1),
		"merge":                    strings.Replace(base, "      max_findings: 10", "      <<: {surprise: true}\n      max_findings: 10", 1),
		"non_string_key":           strings.Replace(base, "      max_findings: 10", "      42: value", 1),
		"timestamp":                strings.Replace(base, "      max_findings: 10", "      created: 2026-01-01", 1),
		"custom_collection":        strings.Replace(base, "    data:", "    data: !custom", 1),
		"nonfinite":                strings.Replace(base, "      max_findings: 10", "      max_findings: .inf", 1),
		"missing_open":             "name: reviewer\n---\nbody",
		"missing_close":            "---\nname: reviewer",
		"multiple_docs":            "---\n" + strings.Replace(strings.TrimPrefix(base, "---\n"), "schema_version:", "...\n---\nschema_version:", 1),
		"scalar":                   "---\nhello\n---\nbody",
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			d, e := Parse([]byte(input))
			if e == nil || d != nil {
				t.Fatalf("accepted invalid source: %+v", d)
			}
			var ve *ValidationError
			if !errors.As(e, &ve) {
				t.Fatalf("unstructured parse error: %v", e)
			}
		})
	}
}

func TestVersionAndContinuity(t *testing.T) {
	for _, v := range []string{"", "1", "3"} {
		d := definition(t)
		d.SchemaVersion = v
		requireCode(t, d.Validate(), "version", "schema_version")
	}
	for _, v := range []ContinuityMode{"", "stable", "temporary"} {
		d := definition(t)
		d.Continuity.Mode = v
		requireCode(t, d.Validate(), "continuity", "continuity.mode")
	}
	for _, v := range []ContinuityMode{Durable, Ephemeral} {
		d := definition(t)
		d.Continuity.Mode = v
		if e := d.Validate(); e != nil {
			t.Fatal(e)
		}
	}
}

func TestValidationDiagnostics(t *testing.T) {
	d := definition(t)
	d.DefinitionID = ""
	d.Revision = ""
	d.Behavior.Purpose = ""
	d.Requirements.Skills[0].Content.Digest = "latest"
	d.Capabilities = append(d.Capabilities, d.Capabilities[0])
	e := d.Validate()
	requireCode(t, e, "required", "definition_id")
	requireCode(t, e, "required", "revision")
	requireCode(t, e, "required", "behavior.purpose")
	requireCode(t, e, "digest", "requirements.skills[0].content.digest")
	requireCode(t, e, "duplicate", "capabilities[1].id")
	var nilDef *Definition
	requireCode(t, nilDef.Validate(), "required", "$")
}

func TestPermissionProfileBoundary(t *testing.T) {
	for _, name := range []string{"", " \t\n"} {
		d := definition(t)
		d.HarnessProfile.Permissions.Profile = name
		requireCode(t, d.Validate(), "required", "harness_profile.permissions.profile")
	}
	base := string(fixture(t, "reviewer"))
	_, err := Parse([]byte(strings.Replace(base, "    profile: example-read-only", "    profile: \"\"", 1)))
	requireCode(t, err, "required", "harness_profile.permissions.profile")
	_, err = Parse([]byte(strings.Replace(base, "    profile: example-read-only", "    {}", 1)))
	requireCode(t, err, "required", "harness_profile.permissions.profile")

	// The library records an opaque host-owned name; it cannot require that
	// a particular harness table already recognizes it or treat it as a mode.
	d := definition(t)
	d.HarnessProfile.Permissions.Profile = "example.org/custom-review"
	if err := d.Validate(); err != nil {
		t.Fatalf("profile binding escaped into definition validation: %v", err)
	}
}

func TestCapabilityCatalogueBoundary(t *testing.T) {
	d := definition(t)
	d.Requirements.Tools = []string{"anything.the.host.resolves"}
	d.Capabilities[0].ID = "cap:private.service"
	known := func(n string) bool { return n == "skills" || n == "memory" }
	if e := d.Validate(WithCapabilities(known)); e != nil {
		t.Fatal(e)
	}
	d.Requirements.Requires = append(d.Requirements.Requires, "unsupported")
	requireCode(t, d.Validate(WithCapabilities(known)), "capability", "requirements.requires[1]")
}

func TestExtensionsNegotiation(t *testing.T) {
	d := definition(t)
	e := d.Extensions["example.org/review"]
	e.Mandatory = true
	d.Extensions["example.org/review"] = e
	requireCode(t, d.Validate(), "mandatory_extension", "extensions[example.org/review]")
	handler := func(e Extension) error {
		if e.Data["max_findings"] != 10 {
			return fmt.Errorf("max_findings must be 10")
		}
		return nil
	}
	opts := WithExtensions(func(n, v string) (func(Extension) error, bool) { return handler, n == "example.org/review" && v == "1" })
	if err := d.Validate(opts); err != nil {
		t.Fatal(err)
	}
	e.Version = "2"
	d.Extensions["example.org/review"] = e
	requireCode(t, d.Validate(opts), "mandatory_extension", "")
	e.Version = "1"
	e.Mandatory = false
	e.Data["max_findings"] = 11
	d.Extensions["example.org/review"] = e
	requireCode(t, d.Validate(opts), "extension", "")
	if err := d.Validate(); err != nil {
		t.Fatalf("unknown optional must be carried: %v", err)
	}
	// A resolver that only claims support cannot authorize a mandatory feature.
	e.Mandatory = true
	d.Extensions["example.org/review"] = e
	requireCode(t, d.Validate(WithExtensions(func(string, string) (func(Extension) error, bool) { return nil, true })), "mandatory_extension", "")
}

func TestExtensionShape(t *testing.T) {
	for _, ns := range []string{"review", "example-org/review", "Example.org/review", "example.org/../review", "example.org/review/extra"} {
		d := definition(t)
		e := d.Extensions["example.org/review"]
		d.Extensions = map[string]Extension{ns: e}
		requireCode(t, d.Validate(), "namespace", "")
	}
	d := definition(t)
	e := d.Extensions["example.org/review"]
	e.Area = "presence"
	e.Data = map[string]any{"bad": math.NaN()}
	d.Extensions["example.org/review"] = e
	requireCode(t, d.Validate(), "area", "")
	requireCode(t, d.Validate(), "extension_data", "")
}

func TestSemanticRevisionBoundaries(t *testing.T) {
	base := definition(t)
	want, _ := Digest(base)
	edits := map[string]func(*Definition){
		"behavior":           func(d *Definition) { d.Body += "\nAdditional instruction." },
		"capabilities":       func(d *Definition) { d.Capabilities[0].Description = "Different service contract" },
		"requirements":       func(d *Definition) { d.Requirements.Skills[0].Content.Digest = "sha256:" + strings.Repeat("b", 64) },
		"harness":            func(d *Definition) { d.HarnessProfile.Permissions.Profile = "example-no-access" },
		"continuity":         func(d *Definition) { d.Continuity.Mode = Durable },
		"optional_extension": func(d *Definition) { e := d.Extensions["example.org/review"]; e.Data["max_findings"] = 11 },
		"extension_version": func(d *Definition) {
			e := d.Extensions["example.org/review"]
			e.Version = "2"
			d.Extensions["example.org/review"] = e
		},
		"extension_mandatory": func(d *Definition) {
			e := d.Extensions["example.org/review"]
			e.Mandatory = true
			d.Extensions["example.org/review"] = e
		},
		"extension_area": func(d *Definition) {
			e := d.Extensions["example.org/review"]
			e.Area = "behavior"
			d.Extensions["example.org/review"] = e
		},
	}
	for name, edit := range edits {
		t.Run(name, func(t *testing.T) {
			d := definition(t)
			edit(d)
			got, e := Digest(d)
			if e != nil || got == want {
				t.Fatalf("semantic edit did not change hash: %s %v", got, e)
			}
		})
	}
	d := definition(t)
	d.DefinitionID = "def:another"
	d.Revision = "9"
	d.Name = "another"
	d.Title = "Other title"
	d.Description = "Display description"
	d.Presentation.Tags = []string{"new"}
	d.Provenance = map[string]string{"source": "other"}
	got, _ := Digest(d)
	if got != want {
		t.Fatal("metadata manufactured a semantic revision")
	}
}

func TestCanonicalNormalizationAndArtifactIntegrity(t *testing.T) {
	raw := fixture(t, "reviewer")
	d := definition(t)
	a, _ := Canonical(d)
	d.Body = "\r\n" + strings.ReplaceAll(d.Body, "\n", "\r\n") + "\r\n"
	b, _ := Canonical(d)
	if !bytes.Equal(a, b) {
		t.Fatal("line endings changed semantic content")
	}
	changed := bytes.Replace(raw, []byte("title: Example Reviewer"), []byte("title: Other Reviewer"), 1)
	other, e := Parse(changed)
	if e != nil {
		t.Fatal(e)
	}
	x, _ := Digest(d)
	y, _ := Digest(other)
	if x != y || ArtifactDigest(raw) == ArtifactDigest(changed) {
		t.Fatal("semantic/artifact boundary broken")
	}
	left := definition(t)
	right := definition(t)
	ext := right.Extensions["example.org/review"]
	ext.Data = map[string]any{"labels": []any{"security", "correctness"}, "max_findings": 10}
	right.Extensions["example.org/review"] = ext
	x, _ = Digest(left)
	y, _ = Digest(right)
	if x != y {
		t.Fatal("map insertion order changed digest")
	}
	right.Requirements.Requires = []string{"memory", "skills"}
	left.Requirements.Requires = []string{"skills", "memory"}
	x, _ = Digest(left)
	y, _ = Digest(right)
	if x == y {
		t.Fatal("meaningful list order lost")
	}
}

func TestExtensionsRoundTripWithoutInterpretation(t *testing.T) {
	d := definition(t)
	b, e := json.Marshal(d)
	if e != nil {
		t.Fatal(e)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var round Definition
	if e = dec.Decode(&round); e != nil {
		t.Fatal(e)
	}
	x, _ := Digest(d)
	y, _ := Digest(&round)
	if x != y {
		t.Fatal("extension content was lost")
	}
}

func FuzzParse(f *testing.F) {
	b, e := os.ReadFile("testdata/reviewer.md")
	if e != nil {
		f.Fatal(e)
	}
	f.Add(b)
	f.Add([]byte("---\n{}\n---"))
	f.Fuzz(func(t *testing.T, data []byte) {
		d, e := Parse(data)
		if e != nil {
			return
		}
		if e = d.Validate(); e != nil {
			t.Fatal(e)
		}
		b, e := Canonical(d)
		if e != nil || !json.Valid(b) {
			t.Fatalf("accepted source cannot canonicalize: %v", e)
		}
	})
}
