package hitltest

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
)

//go:embed scenarios/*.json
var scenarioFS embed.FS

// Scenario is a named sequence of operations with expected results.
type Scenario struct {
	Name string `json:"name"`
	// Description says what the scenario pins down.
	Description string `json:"description"`
	// TangentEvidence names the Tangent test that already asserts the
	// behavior, or says that none does. Informational.
	TangentEvidence string `json:"tangent_evidence"`
	// Requires lists capabilities the adapter must declare; without them the
	// scenario is skipped (loudly).
	Requires []Capability `json:"requires"`
	Steps    []Step       `json:"steps"`
}

// Step is one operation. Op is enqueue, get, await, withdraw, resolve or
// expire. Doc (enqueue) or Fields (get, await, withdraw) or Response
// (resolve) carry the payload; "{{run}}" in any string becomes a per-run
// token and "${name}" becomes the item id captured by an earlier step's As.
type Step struct {
	Op     string `json:"op"`
	As     string `json:"as,omitempty"`
	Item   string `json:"item,omitempty"`   // variable naming the item (get, await, withdraw, resolve)
	Caller string `json:"caller,omitempty"` // application_id for get/await/withdraw (default "conf-a")
	// Doc is the enqueue request.
	Doc json.RawMessage `json:"doc,omitempty"`
	// Fields are merged into the get/await/withdraw command (wait_ms, reason,
	// expected_revision).
	Fields   json.RawMessage `json:"fields,omitempty"`
	Response json.RawMessage `json:"response,omitempty"`
	Expect   Expect          `json:"expect"`
}

// Expect is the set of assertions on a step's result. Unset fields assert
// nothing. A step is expected to succeed unless Code or CodeIn is set.
type Expect struct {
	Code   string   `json:"code,omitempty"`
	CodeIn []string `json:"code_in,omitempty"`

	State        string   `json:"state,omitempty"`
	StateIn      []string `json:"state_in,omitempty"`
	Mode         string   `json:"mode,omitempty"`
	WaitStatus   string   `json:"wait_status,omitempty"`
	OutcomeState string   `json:"outcome_state,omitempty"`
	OutcomeCause string   `json:"outcome_cause,omitempty"`
	Decision     string   `json:"decision,omitempty"`

	SameItemAs         string `json:"same_item_as,omitempty"`
	DistinctItemFrom   string `json:"distinct_item_from,omitempty"`
	ExistingItemIDIs   string `json:"existing_item_id_is,omitempty"`
	OutcomeEquals      string `json:"outcome_equals,omitempty"`
	RevisionSameAs     string `json:"revision_same_as,omitempty"`
	RevisionGreaterThn string `json:"revision_greater_than,omitempty"`
}

// Scenarios returns every embedded scenario, sorted by name.
func Scenarios() []Scenario {
	entries, err := fs.ReadDir(scenarioFS, "scenarios")
	if err != nil {
		panic("hitltest: " + err.Error())
	}
	var out []Scenario
	for _, e := range entries {
		b, err := scenarioFS.ReadFile(path.Join("scenarios", e.Name()))
		if err != nil {
			panic("hitltest: " + err.Error())
		}
		var s Scenario
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&s); err != nil {
			panic(fmt.Sprintf("hitltest: scenario %s: %v", e.Name(), err))
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
