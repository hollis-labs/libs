package schema

import (
	"bytes"
	_ "embed" // bundle is embedded
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed lifecycle.schema.json
var bundle []byte

// ID is the $id of the bundle.
const ID = "https://schemas.hollis-labs.dev/go-hitl/lifecycle/1.0/schema.json"

// Bundle returns a copy of the embedded JSON Schema bundle.
func Bundle() []byte { return bytes.Clone(bundle) }

var (
	defsOnce sync.Once
	defNames []string
)

// Defs returns the sorted names of every definition in the bundle.
func Defs() []string {
	defsOnce.Do(func() {
		var doc struct {
			Defs map[string]json.RawMessage `json:"$defs"`
		}
		if err := json.Unmarshal(bundle, &doc); err != nil {
			panic("go-hitl/schema: embedded bundle is not valid JSON: " + err.Error())
		}
		for n := range doc.Defs {
			defNames = append(defNames, n)
		}
		sort.Strings(defNames)
	})
	return append([]string(nil), defNames...)
}

// Validator validates documents against one definition of the bundle.
type Validator struct {
	def    string
	schema *jsonschema.Schema
}

// NewValidator compiles the named definition (for example
// "HITLRetrievalResultCoreV1"). It errors for an unknown name.
func NewValidator(def string) (*Validator, error) {
	found := false
	for _, n := range Defs() {
		if n == def {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("schema: unknown definition %q", def)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(bundle))
	if err != nil {
		return nil, fmt.Errorf("schema: decode bundle: %w", err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	if err = c.AddResource(ID, doc); err != nil {
		return nil, fmt.Errorf("schema: add bundle: %w", err)
	}
	s, err := c.Compile(ID + "#/$defs/" + def)
	if err != nil {
		return nil, fmt.Errorf("schema: compile %s: %w", def, err)
	}
	return &Validator{def: def, schema: s}, nil
}

// Def returns the definition name this validator checks.
func (v *Validator) Def() string { return v.def }

// ValidationError reports a document that does not satisfy a definition.
type ValidationError struct {
	Def string
	// Locations holds the JSON Pointer of every instance location involved in
	// a failing leaf keyword, sorted and de-duplicated. "" is the document
	// root; a missing required property is reported at its parent object.
	Locations []string
	// Detail is the validator's human-readable explanation.
	Detail string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("schema: document does not satisfy %s: %s", e.Def, e.Detail)
}

// Validate checks one JSON document. A non-conforming document yields a
// *ValidationError; unparseable JSON yields a plain error.
func (v *Validator) Validate(doc []byte) error {
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(doc))
	if err != nil {
		return fmt.Errorf("schema: document is not valid JSON: %w", err)
	}
	err = v.schema.Validate(inst)
	if err == nil {
		return nil
	}
	var verr *jsonschema.ValidationError
	if !errors.As(err, &verr) {
		return err
	}
	seen := map[string]bool{}
	var walk func(*jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			seen[pointer(e.InstanceLocation)] = true
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(verr)
	locs := make([]string, 0, len(seen))
	for l := range seen {
		locs = append(locs, l)
	}
	sort.Strings(locs)
	return &ValidationError{Def: v.def, Locations: locs, Detail: verr.Error()}
}

func pointer(loc []string) string {
	if len(loc) == 0 {
		return ""
	}
	var b strings.Builder
	for _, t := range loc {
		b.WriteByte('/')
		b.WriteString(strings.NewReplacer("~", "~0", "/", "~1").Replace(t))
	}
	return b.String()
}
