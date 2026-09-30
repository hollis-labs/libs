package transportparity

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

type httpWrite struct {
	MemoryKey string          `json:"memory_key"`
	Body      string          `json:"body,omitempty"`
	Owner     *owner          `json:"owner"`
	When      time.Time       `json:"when"`
	Data      json.RawMessage `json:"data"`
	Skipped   string          `json:"-"`
	private   string
	NoTag     string
	Tags      []tag `json:"tags"`
}

type owner struct {
	ID    string `json:"id"`
	Group struct {
		Name string `json:"name"`
	} `json:"group"`
}

type tag struct {
	Label string `json:"label"`
}

func TestAcceptedFieldNames(t *testing.T) {
	_ = httpWrite{}.private
	got := AcceptedFieldNames(httpWrite{})
	want := []string{"NoTag", "body", "data", "memory_key", "owner", "owner.group", "owner.group.name", "owner.id", "tags", "when"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %v\nwant %v", got, want)
	}
	// A pointer to the struct gives the same answer.
	if !reflect.DeepEqual(AcceptedFieldNames(&httpWrite{}), want) {
		t.Error("a pointer to the struct gave a different answer")
	}
}

type base struct {
	ID string `json:"id"`
}
type quiet struct {
	Note string `json:"note"`
}
type withEmbedded struct {
	base
	*quiet
	Name string `json:"name"`
}
type namedEmbed struct {
	base `json:"parent"`
}

// encoding/json flattens an embedded struct with no json name into its parent;
// a named one is a sub-object.
func TestAcceptedFieldNamesFlattensEmbeddedStructs(t *testing.T) {
	if got, want := AcceptedFieldNames(withEmbedded{}), []string{"id", "name", "note"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got, want := AcceptedFieldNames(namedEmbed{}), []string{"parent", "parent.id"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestAcceptedFieldNamesOfANonStructIsEmpty(t *testing.T) {
	for _, v := range []any{1, "x", []string{"a"}, map[string]int{}, nil} {
		if got := AcceptedFieldNames(v); len(got) != 0 {
			t.Errorf("AcceptedFieldNames(%T) = %v", v, got)
		}
	}
}

// Reproduces Tesseract's own miss: the response bytes agree, but the MCP argument
// is "key" and the HTTP field is "memory_key".
func TestAssertSameFieldNamesNamesTheMismatch(t *testing.T) {
	type mcpArgs struct {
		Key  string `json:"key"`
		Body string `json:"body"`
	}
	type httpReq struct {
		MemoryKey string `json:"memory_key"`
		Body      string `json:"body"`
	}
	rec := &recorder{}
	AssertSameFieldNames(rec, "MCP", "HTTP", AcceptedFieldNames(mcpArgs{}), AcceptedFieldNames(httpReq{}))
	msg := rec.text()
	for _, want := range []string{"only MCP: [key]", "only HTTP: [memory_key]"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}

	rec = &recorder{}
	AssertSameFieldNames(rec, "A", "B", AcceptedFieldNames(httpReq{}), AcceptedFieldNames(httpReq{}))
	if rec.failed() {
		t.Errorf("identical shapes failed: %s", rec.text())
	}
}

func TestAssertSameFieldNamesIgnoresOrderAndDuplicates(t *testing.T) {
	rec := &recorder{}
	AssertSameFieldNames(rec, "A", "B", []string{"b", "a", "a"}, []string{"a", "b"})
	if rec.failed() {
		t.Errorf("order or duplicates caused a failure: %s", rec.text())
	}
}

// Ground truth: whatever encoding/json actually emits for a populated value must
// be exactly the set AcceptedFieldNames reports.
func TestAcceptedFieldNamesMatchesWhatEncodingJSONEmits(t *testing.T) {
	for name, v := range map[string]any{
		"embedded":       withEmbedded{base: base{ID: "1"}, quiet: &quiet{Note: "n"}, Name: "x"},
		"named embedded": namedEmbed{base: base{ID: "1"}},
		"nested":         httpWrite{MemoryKey: "k", Body: "b", Owner: &owner{ID: "o"}, NoTag: "n", Data: json.RawMessage(`{}`)},
	} {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var generic map[string]any
		if err := json.Unmarshal(raw, &generic); err != nil {
			t.Fatal(err)
		}
		var emitted []string
		var walk func(m map[string]any, prefix string)
		walk = func(m map[string]any, prefix string) {
			for k, val := range m {
				p := k
				if prefix != "" {
					p = prefix + "." + k
				}
				emitted = append(emitted, p)
				// json.RawMessage and time.Time render as single values.
				if sub, ok := val.(map[string]any); ok && k != "data" {
					walk(sub, p)
				}
			}
		}
		walk(generic, "")
		sort.Strings(emitted)
		want := AcceptedFieldNames(v)
		// AcceptedFieldNames also lists fields that were empty and omitted, so
		// the emitted set must be a subset, and every emitted path must appear.
		set := map[string]bool{}
		for _, w := range want {
			set[w] = true
		}
		for _, e := range emitted {
			if !set[e] {
				t.Errorf("%s: encoding/json emitted %q but AcceptedFieldNames does not list it (got %v)", name, e, want)
			}
		}
	}
}
