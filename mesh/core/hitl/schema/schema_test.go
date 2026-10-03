package schema_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hollis-labs/substrate/mesh/hitl/schema"
)

func TestEveryDefinitionCompiles(t *testing.T) {
	defs := schema.Defs()
	if len(defs) != 36 {
		t.Fatalf("bundle has %d definitions, want 36 (12 copied + 3 profile + 21 core/new)", len(defs))
	}
	for _, d := range defs {
		if _, err := schema.NewValidator(d); err != nil {
			t.Errorf("%s: %v", d, err)
		}
	}
}

func TestUnknownDefinitionAndBadJSON(t *testing.T) {
	if _, err := schema.NewValidator("Nope"); err == nil {
		t.Fatal("unknown definition accepted")
	}
	v, _ := schema.NewValidator("CallerAssertionV1")
	if err := v.Validate([]byte(`{`)); err == nil {
		t.Fatal("malformed JSON accepted")
	} else if verr := (*schema.ValidationError)(nil); errors.As(err, &verr) {
		t.Fatalf("malformed JSON must not look like a schema violation: %v", err)
	}
	if v.Def() != "CallerAssertionV1" {
		t.Fatal("Def")
	}
}

func TestBundleReturnsACopy(t *testing.T) {
	b := schema.Bundle()
	b[0] = 'X'
	if schema.Bundle()[0] != '{' {
		t.Fatal("mutating the returned bundle changed the embedded one")
	}
}

func bundleDoc(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	var doc struct {
		ID   string                     `json:"$id"`
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(schema.Bundle(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.ID != schema.ID {
		t.Fatalf("$id = %s", doc.ID)
	}
	return doc.Defs
}

// Core definitions must not require Tangent presentation fields, and must not
// use the word "checkpoint" (decision D8: never name a def Checkpoint*).
func TestCoreDefinitionsCarryNoPresentationFields(t *testing.T) {
	forbidden := []string{"surface_id", "queue_sequence", "queue_position", "inbox_url", "item_url"}
	for name, raw := range bundleDoc(t) {
		if strings.Contains(strings.ToLower(name), "checkpoint") {
			t.Errorf("definition %s uses the word checkpoint", name)
		}
		if !strings.Contains(name, "Core") {
			continue
		}
		for _, f := range forbidden {
			if strings.Contains(string(raw), f) {
				t.Errorf("%s mentions %s", name, f)
			}
		}
	}
	res := string(bundleDoc(t)["ResolutionRecordCoreV1"])
	var d struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal([]byte(res), &d); err != nil {
		t.Fatal(err)
	}
	for _, r := range d.Required {
		if r == "presented_projection_revision" {
			t.Fatal("presented_projection_revision must be optional in ResolutionRecordCoreV1")
		}
	}
}

func TestOriginAndTierTags(t *testing.T) {
	tag := func(name, key string) string {
		var m map[string]any
		if err := json.Unmarshal(bundleDoc(t)[name], &m); err != nil {
			t.Fatal(err)
		}
		s, _ := m[key].(string)
		return s
	}
	for _, n := range []string{"HITLTerminalConflictErrorV1", "HITLErrorV1", "HITLPlainErrorV1", "ResponderV1", "ProofV1", "ProofBindV1"} {
		if tag(n, "x-hitl-origin") != "go-hitl" {
			t.Errorf("%s is not tagged x-hitl-origin: go-hitl", n)
		}
	}
	for _, n := range []string{"ApprovalResponseV1", "AttentionResponseV1", "HITLResponseV1"} {
		if tag(n, "x-hitl-tier") != "profile" {
			t.Errorf("%s is not tagged x-hitl-tier: profile", n)
		}
	}
}

func TestContractVersionIsPinnedToOnePointZero(t *testing.T) {
	v, _ := schema.NewValidator("HITLItemHandleCoreV1")
	if err := v.Validate([]byte(`{"contract_version":"1.1","item_id":"i","state":"staged","revision":1}`)); err == nil {
		t.Fatal("contract_version 1.1 accepted")
	}
}
