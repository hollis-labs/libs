package schema_test

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/hollis-labs/go-hitl/schema"
)

const tangentEnv = "HITL_TANGENT_SCHEMA"

// copied are the definitions the bundle takes from Tangent unchanged.
var copied = []string{
	"SourceAssertionV1", "CallerAssertionV1", "ExternalRefV1", "ParticipantCaptureV1",
	"HITLGetCommandV1", "HITLAwaitCommandV1", "HITLWithdrawCommandV1",
	"CanceledTerminalOutcomeV1", "ExpiredTerminalOutcomeV1", "FailedTerminalOutcomeV1",
	"SupersededTerminalOutcomeV1", "HITLIdempotencyConflictErrorV1",
}

// profile definitions are copied with one added annotation.
var profile = []string{"ApprovalResponseV1", "AttentionResponseV1", "HITLResponseV1"}

// examplesMap says where each Tangent definition's examples must validate.
// A definition absent from this map that has examples fails the test, so a
// new Tangent example cannot go unchecked. The empty target marks a
// definition that stays in Tangent (its examples are presentation-bound).
var examplesMap = map[string]string{
	"HITLItemRequestV1":        "HITLEnqueueRequestCoreV1",
	"ApprovalResponseV1":       "ApprovalResponseV1",
	"AttentionResponseV1":      "AttentionResponseV1",
	"HITLResolutionCommandV1":  "", // needs presented_projection_revision: Tangent-specific
	"HITLTerminalOutcomeV1":    "HITLTerminalOutcomeCoreV1",
	"HITLGetCommandV1":         "HITLGetCommandV1",
	"HITLAwaitCommandV1":       "HITLAwaitCommandV1",
	"HITLWithdrawCommandV1":    "HITLWithdrawCommandV1",
	"HITLItemHandleV1":         "HITLItemHandleCoreV1",
	"HITLRetrievalResultV1":    "HITLRetrievalResultCoreV1",
	"HITLStaleRevisionErrorV1": "HITLStaleRevisionErrorCoreV1",
}

func loadTangent(t *testing.T) map[string]map[string]any {
	t.Helper()
	path := os.Getenv(tangentEnv)
	if path == "" {
		t.Skipf("SKIPPED (not passed): %s is not set; point it at Tangent's request.schema.json to run the cross-check", tangentEnv)
	}
	b, err := os.ReadFile(path) //nolint:gosec // opt-in developer path from the environment
	if err != nil {
		t.Fatalf("%s=%s: %v", tangentEnv, path, err)
	}
	var doc struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Defs) == 0 {
		t.Fatalf("%s has no $defs", path)
	}
	return doc.Defs
}

func ours(t *testing.T) map[string]map[string]any {
	t.Helper()
	var doc struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(schema.Bundle(), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Defs
}

func TestTangentCopiedDefinitionsAreCanonicalEqual(t *testing.T) {
	tangent, mine := loadTangent(t), ours(t)
	for _, name := range append(slices.Clone(copied), profile...) {
		want, ok := tangent[name]
		if !ok {
			t.Errorf("Tangent's bundle has no %s", name)
			continue
		}
		got := mine[name]
		if slices.Contains(profile, name) {
			got = maps(got)
			if got["x-hitl-tier"] != "profile" {
				t.Errorf("%s is not tagged x-hitl-tier: profile", name)
			}
			delete(got, "x-hitl-tier")
		}
		if !reflect.DeepEqual(want, got) {
			t.Errorf("%s differs from Tangent's definition", name)
		}
	}
}

func maps(m map[string]any) map[string]any {
	c := make(map[string]any, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

func TestTangentExamplesValidateAgainstTheMappedCoreDefinition(t *testing.T) {
	tangent := loadTangent(t)
	checked := 0
	for name, def := range tangent {
		examples, _ := def["examples"].([]any)
		if len(examples) == 0 {
			continue
		}
		target, mapped := examplesMap[name]
		if !mapped {
			t.Errorf("Tangent def %s has %d examples but no entry in examplesMap", name, len(examples))
			continue
		}
		if target == "" {
			continue
		}
		v, err := schema.NewValidator(target)
		if err != nil {
			t.Fatal(err)
		}
		for i, ex := range examples {
			doc, _ := json.Marshal(ex)
			if err := v.Validate(doc); err != nil {
				t.Errorf("%s example %d does not validate against %s: %v", name, i, target, err)
			}
			checked++
		}
	}
	t.Logf("%d Tangent examples validated against their Core definitions", checked)
	if checked < 14 {
		t.Errorf("only %d examples were checked", checked)
	}
}
