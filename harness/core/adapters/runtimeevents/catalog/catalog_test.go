package catalog_test

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents/catalog"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

var update = flag.Bool("update", false, "regenerate kinds_gen.go and kinds.ts from catalog.json")

var kindName = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

// Legacy kinds that predate the naming rule and are kept only as
// deprecated aliases.
var legacyNames = map[string]bool{"tool_call_start": true, "tool_call_end": true}

func mustLoad(t *testing.T) catalog.Catalog {
	t.Helper()
	c, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCatalogIsConsistent(t *testing.T) {
	c := mustLoad(t)
	seen := map[string]bool{}
	for _, e := range c.Kinds {
		if seen[e.Kind] {
			t.Errorf("duplicate kind %q", e.Kind)
		}
		seen[e.Kind] = true
		if !kindName.MatchString(e.Kind) && !legacyNames[e.Kind] {
			t.Errorf("kind %q does not follow %s", e.Kind, c.Naming)
		}
		if !slices.Contains(c.Categories, e.Category) {
			t.Errorf("kind %q has unknown category %q", e.Kind, e.Category)
		}
		switch e.Status {
		case catalog.StatusStable, catalog.StatusNew:
			if e.Successor != "" {
				t.Errorf("kind %q is %s but names a successor", e.Kind, e.Status)
			}
		case catalog.StatusDeprecated:
			if e.Successor == "" {
				t.Errorf("deprecated kind %q has no successor", e.Kind)
			}
		default:
			t.Errorf("kind %q has unknown status %q", e.Kind, e.Status)
		}
		if e.Status == catalog.StatusNew && e.PayloadSchema == "" {
			t.Errorf("new kind %q has no payload schema", e.Kind)
		}
	}
	for _, e := range c.Kinds {
		if e.Successor != "" && !seen[e.Successor] {
			t.Errorf("kind %q names unknown successor %q", e.Kind, e.Successor)
		}
	}
}

// Every kind runtimeevents defines is registered.
func TestCatalogCoversRuntimeeventsKinds(t *testing.T) {
	src, err := os.ReadFile("../kinds.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`EventKind = "([^"]+)"`)
	for _, m := range re.FindAllSubmatch(src, -1) {
		if _, ok := catalog.Lookup(string(m[1])); !ok {
			t.Errorf("runtimeevents kind %q is not in catalog.json", m[1])
		}
	}
}

// Every payload schema compiles, and every registered id resolves.
func TestPayloadSchemasCompile(t *testing.T) {
	c := mustLoad(t)
	ids := map[string]bool{}
	for _, e := range c.Kinds {
		if e.PayloadSchema != "" {
			ids[e.PayloadSchema] = true
		}
	}
	for id := range ids {
		b, err := catalog.PayloadSchema(id)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		comp := jsonschema.NewCompiler()
		comp.DefaultDraft(jsonschema.Draft2020)
		if err := comp.AddResource(id, doc); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if _, err := comp.Compile(id); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if m, ok := doc.(map[string]any); !ok || m["$id"] != id {
			t.Errorf("%s: $id does not match the registered id", id)
		}
	}
	entries, err := os.ReadDir("schemas")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range entries {
		id := "urn:hollis-labs:events:" + strings.TrimSuffix(f.Name(), ".schema.json")
		if !ids[id] {
			t.Errorf("schema file %s is not referenced by any kind", f.Name())
		}
	}
}

// kinds_gen.go and kinds.ts match catalog.json.
func TestGeneratedConstantsAreCurrent(t *testing.T) {
	c := mustLoad(t)
	goSrc, tsSrc := render(c)
	for _, f := range []struct {
		path string
		want []byte
	}{{"kinds_gen.go", goSrc}, {"kinds.ts", tsSrc}} {
		if *update {
			if err := os.WriteFile(f.path, f.want, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(f.path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, f.want) {
			t.Errorf("%s is stale; run: go test ./adapters/runtimeevents/catalog -run TestGenerated -update", f.path)
		}
	}
}

func identifier(kind string) string {
	var b strings.Builder
	for _, part := range strings.FieldsFunc(kind, func(r rune) bool { return r == '.' || r == '_' }) {
		b.WriteString(strings.ToUpper(part[:1]) + part[1:])
	}
	return b.String()
}

func render(c catalog.Catalog) (goSrc, tsSrc []byte) {
	const header = "Code generated from catalog.json by catalog_test.go -update. DO NOT EDIT."
	var g, ts bytes.Buffer
	fmt.Fprintf(&g, "// %s\n\npackage catalog\n\n// Kind constants, one per catalog.json entry.\nconst (\n", header)
	fmt.Fprintf(&ts, "// %s\n\n", header)
	width := 0
	for _, e := range c.Kinds {
		width = max(width, len("Kind"+identifier(e.Kind)))
	}
	for _, e := range c.Kinds {
		name := "Kind" + identifier(e.Kind)
		fmt.Fprintf(&g, "\t%-*s = %q\n", width, name, e.Kind)
	}
	g.WriteString(")\n")
	ts.WriteString("export const Kind = {\n")
	for _, e := range c.Kinds {
		fmt.Fprintf(&ts, "  %s: %q,\n", identifier(e.Kind), e.Kind)
	}
	ts.WriteString("} as const;\n\nexport type Kind = (typeof Kind)[keyof typeof Kind];\n\n")
	ts.WriteString("export interface CatalogEntry {\n  category: string;\n  status: \"stable\" | \"new\" | \"deprecated\";\n  payloadSchema?: string;\n  successor?: string;\n}\n\n")
	ts.WriteString("export const Catalog: Record<Kind, CatalogEntry> = {\n")
	for _, e := range c.Kinds {
		fmt.Fprintf(&ts, "  %q: { category: %q, status: %q", e.Kind, e.Category, e.Status)
		if e.PayloadSchema != "" {
			fmt.Fprintf(&ts, ", payloadSchema: %q", e.PayloadSchema)
		}
		if e.Successor != "" {
			fmt.Fprintf(&ts, ", successor: %q", e.Successor)
		}
		ts.WriteString(" },\n")
	}
	ts.WriteString("};\n")
	return g.Bytes(), ts.Bytes()
}

func compilePayloadSchema(t *testing.T, id string) *jsonschema.Schema {
	t.Helper()
	b, err := catalog.PayloadSchema(id)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	comp := jsonschema.NewCompiler()
	comp.DefaultDraft(jsonschema.Draft2020)
	comp.AssertFormat()
	if err := comp.AddResource(id, doc); err != nil {
		t.Fatal(err)
	}
	s, err := comp.Compile(id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Every schema has a valid example in testdata/examples, and the payloads in
// testdata/invalid (a started call reporting ok, a message carrying its
// body, data with neither inline data nor a ref, a transition without a
// trigger) are rejected.
func TestPayloadExamples(t *testing.T) {
	entries, err := os.ReadDir("schemas")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range entries {
		name := strings.TrimSuffix(f.Name(), ".schema.json")
		s := compilePayloadSchema(t, "urn:hollis-labs:events:"+name)
		for dir, wantValid := range map[string]bool{"examples": true, "invalid": false} {
			b, err := os.ReadFile("testdata/" + dir + "/" + name + ".json")
			if os.IsNotExist(err) && !wantValid {
				continue
			}
			if err != nil {
				t.Fatalf("%s: missing %s fixture: %v", name, dir, err)
			}
			inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Validate(inst); (err == nil) != wantValid {
				t.Errorf("%s/%s: valid=%v, want %v (%v)", dir, name, err == nil, wantValid, err)
			}
		}
	}
}
