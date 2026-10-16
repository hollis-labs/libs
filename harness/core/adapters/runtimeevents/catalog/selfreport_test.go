package catalog_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents/catalog"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestEffectiveProgressDetail(t *testing.T) {
	for _, c := range []struct {
		launch, task, template catalog.ProgressDetail
		want                   catalog.ProgressDetail
		src                    string
	}{
		{"", "", "", catalog.DetailMilestone, catalog.SourceDefault},
		{"", "", "verbose", catalog.DetailVerbose, catalog.SourceTemplate},
		{"", "step", "verbose", catalog.DetailStep, catalog.SourceTask},
		{"off", "step", "verbose", catalog.DetailOff, catalog.SourceLaunch},
		{"chatty", "", "step", catalog.DetailStep, catalog.SourceTemplate},
	} {
		got, src := catalog.EffectiveProgressDetail(c.launch, c.task, c.template)
		if got != c.want || src != c.src {
			t.Errorf("Effective(%q,%q,%q) = %q,%q; want %q,%q", c.launch, c.task, c.template, got, src, c.want, c.src)
		}
	}
}

func TestProgressDetailAllows(t *testing.T) {
	levels := []catalog.ProgressDetail{catalog.DetailMilestone, catalog.DetailStep, catalog.DetailVerbose}
	for _, eff := range append([]catalog.ProgressDetail{catalog.DetailOff}, levels...) {
		for _, lvl := range levels {
			want := eff != catalog.DetailOff && indexOf(levels, lvl) <= indexOf(levels, eff)
			if got := eff.Allows(lvl); got != want {
				t.Errorf("%q.Allows(%q) = %v, want %v", eff, lvl, got, want)
			}
		}
	}
	if catalog.DetailVerbose.Allows("chatty") || catalog.DetailVerbose.Allows(catalog.DetailOff) {
		t.Error("an unknown or off report level was allowed")
	}
}

func indexOf(s []catalog.ProgressDetail, v catalog.ProgressDetail) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

// Every tool schema compiles, has a valid fixture, and rejects its invalid
// fixture when one exists.
func TestToolSchemas(t *testing.T) {
	files, err := filepath.Glob("tools/*.schema.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no tool schemas: %v", err)
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".schema.json")
		b, err := catalog.ToolSchema(name)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		id, _ := doc.(map[string]any)["$id"].(string)
		comp := jsonschema.NewCompiler()
		comp.DefaultDraft(jsonschema.Draft2020)
		if err := comp.AddResource(id, doc); err != nil {
			t.Fatal(err)
		}
		s, err := comp.Compile(id)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, kind := range []string{"valid", "invalid"} {
			fb, err := os.ReadFile("testdata/tools/" + name + "." + kind + ".json")
			if os.IsNotExist(err) && kind == "invalid" {
				continue
			}
			if err != nil {
				t.Fatalf("%s: missing %s fixture", name, kind)
			}
			inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(fb))
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Validate(inst); (err == nil) != (kind == "valid") {
				t.Errorf("%s %s fixture: validation error = %v", name, kind, err)
			}
		}
	}
}
