package eval_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/libs/agent/sdk"
	"github.com/hollis-labs/libs/agent/sdk/eval"
	"github.com/hollis-labs/libs/agent/sdk/trace"
	"github.com/hollis-labs/libs/llm/core/usageledger"
	"github.com/hollis-labs/libs/mesh/core/gate"
	"github.com/hollis-labs/libs/mesh/core/gate/gatetest"
)

func fixture(t *testing.T, name string) eval.Scenario {
	t.Helper()
	f, err := os.Open("testdata/" + name + ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s, err := eval.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func ptr(v int64) *int64 { return &v }
func TestActualLoopTrajectoryUsageAndImmutablePrices(t *testing.T) {
	s := fixture(t, "pass")
	snapshot, build := gatetest.Reference("snapshot-1"), gatetest.Reference("build-1")
	opts := eval.Options{Start: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Evidence: trace.Metadata{Snapshot: &snapshot, Build: &build}}
	result, err := eval.Run(context.Background(), s, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Passed || result.Outcome.Text != "The value is 42." || result.Outcome.Status != sdk.Succeeded || result.Metrics.Turns != 2 || result.Metrics.FakeToolCalls != 1 || result.Metrics.VirtualDuration != 12*time.Millisecond {
		t.Fatalf("actual loop failed: %+v", result)
	}
	if len(result.Metrics.Usage) != 2 || len(result.Metrics.Costs) != 2 {
		t.Fatal("missing per-model usage evidence")
	}
	row := result.Metrics.Usage[0]
	if row.Usage.UncachedInputTokens.Tokens != 50 || row.Usage.OutputTokens.Tokens != 7 || row.Usage.ReasoningTokens.Tokens != 3 || row.Usage.TotalTokens() != 110 || row.Usage.TotalProvenance() != usageledger.ProvenanceMeasured {
		t.Fatalf("inclusive accounting double-counted: %+v", row.Usage)
	}
	cost := result.Metrics.Costs[0]
	if !cost.Priced || cost.IsBill() || cost.Total() < 0.000209 || cost.Total() > 0.000211 {
		t.Fatalf("wrong snapshot cost: %+v total=%g", cost, cost.Total())
	}
	if result.Metrics.Costs[1].Priced {
		t.Fatal("missing price became free")
	}
	if result.Recording.Metadata().Snapshot.ID != "snapshot-1" || result.Recording.Metadata().Build.ID != "build-1" {
		t.Fatal("lost evidence references")
	}
	var decision, effect bool
	for _, entry := range result.Recording.Entries() {
		c := entry.Correlation
		if string(c.RunID) != "eval/lookup-pass" || c.Binding.Generation != 7 || c.Environment.ID == "" || c.DefinitionDigest == "" {
			t.Fatalf("missing typed correlation: %+v", c)
		}
		if c.TurnID != "" && c.ModelCallID == "" {
			t.Fatal("turn/model conflation")
		}
		if c.DecisionID != "" {
			decision = true
		}
		if entry.Execution != nil && entry.Execution.State == gate.ExecutionSucceeded {
			effect = true
			if c.Tool != "lookup" || c.OperationID == "" || c.ToolCallID == "" || c.ApprovalID == "" || c.DecisionID == "" {
				t.Fatal("missing execution/approval correlation")
			}
		}
		if entry.Delivery != nil {
			t.Fatal("native delivery fabricated from SDK decision")
		}
	}
	if !decision || !effect {
		t.Fatal("missing separate decision/effect observations")
	}
	// Changing the source snapshot or returned metric rows cannot reprice history.
	s.ModelTurns[0].Price.InputPerMillion = 999
	result.Metrics.Usage[0].Price.Source = "mutated"
	snapshot.ID = "mutated"
	var recorded *usageledger.Row
	for _, e := range result.Recording.Entries() {
		if e.Usage != nil {
			recorded = e.Usage
			break
		}
	}
	if recorded == nil || recorded.Price.InputPerMillion != 2 || recorded.Price.Source != "synthetic-fixture" || result.Recording.Metadata().Snapshot.ID != "snapshot-1" {
		t.Fatal("accepted snapshot remained mutable")
	}
}
func TestDeliberateFailureDetectsProseWithoutToolExecution(t *testing.T) {
	result, err := eval.Run(context.Background(), fixture(t, "deliberate-fail"), eval.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Passed || result.Metrics.FakeToolCalls != 0 || !strings.Contains(result.Outcome.Text, "called lookup") {
		t.Fatal("deliberate failure not exercised")
	}
	found := false
	for _, f := range result.Findings {
		found = found || f.Criterion == "called_tool"
	}
	if !found {
		t.Fatal("tool-use prose fooled grader")
	}
}
func TestUncertainEffectStaysUnknownDuringEvidenceReplay(t *testing.T) {
	result, err := eval.Run(context.Background(), fixture(t, "uncertain"), eval.Options{})
	if !errors.Is(err, eval.ErrUncertainEffect) || result.Passed || result.Metrics.FakeToolCalls != 1 || result.Metrics.Turns != 1 {
		t.Fatalf("unknown effect flattened/retried: %+v %v", result, err)
	}
	entries, err := trace.Replay(context.Background(), result.Recording)
	if err != nil {
		t.Fatal(err)
	}
	unknown := false
	for _, entry := range entries {
		if entry.Execution != nil {
			unknown = unknown || entry.Execution.State == gate.ExecutionUnknown
			if entry.Execution.State == gate.ExecutionSucceeded {
				t.Fatal("unknown resolved as success")
			}
		}
	}
	if !unknown {
		t.Fatal("missing uncertain effect evidence")
	}
}
func TestUnsupportedAndMalformedSemanticsRefuseBeforeLoop(t *testing.T) {
	for _, test := range []struct {
		name   string
		modify func(*eval.Scenario)
	}{
		{"history", func(s *eval.Scenario) { s.PrevTurns = []eval.PreviousTurn{{Role: "user", Content: "history"}} }},
		{"clarification false", func(s *eval.Scenario) { v := false; s.Rubric.AskedClarification = &v }},
		{"unknown effect", func(s *eval.Scenario) { s.ToolsOffered[0].Mock.Outcome = "assume_success" }},
		{"duplicate call IDs", func(s *eval.Scenario) {
			s.ModelTurns[0].ToolCalls = append(s.ModelTurns[0].ToolCalls, s.ModelTurns[0].ToolCalls[0])
		}},
		{"unknown tool", func(s *eval.Scenario) { s.ModelTurns[0].ToolCalls[0].Name = "not-offered" }},
		{"wrong schema arguments", func(s *eval.Scenario) { s.ModelTurns[0].ToolCalls[0].Arguments = map[string]any{"unexpected": true} }},
		{"unpriced nonzero unknown rate", func(s *eval.Scenario) { s.ModelTurns[0].Price.UnknownRates.UncachedInput = true }},
		{"remote schema", func(s *eval.Scenario) {
			s.ToolsOffered[0].InputSchema = map[string]any{"$ref": "https://example.invalid/schema"}
		}},
		{"impossible accounting", func(s *eval.Scenario) { s.ModelTurns[0].Usage.Input = ptr(1) }},
		{"negative duration", func(s *eval.Scenario) { s.ModelTurns[0].DurationMillis = -1 }},
		{"overflowing cost", func(s *eval.Scenario) {
			s.ModelTurns[0].Usage.Input = ptr(1_000_000_000)
			s.ModelTurns[0].Price.InputPerMillion = 1e308
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := fixture(t, "pass")
			test.modify(&s)
			result, err := eval.Run(context.Background(), s, eval.Options{})
			if err == nil || result.Outcome.Turns != 0 || result.Metrics.FakeToolCalls != 0 || len(result.Recording.Entries()) != 0 {
				t.Fatalf("invalid scenario reached effects: %+v %v", result, err)
			}
		})
	}
}
func TestDecodeStrictSingleBoundedDocument(t *testing.T) {
	for _, source := range []string{"id: x\nunknown_field: true\n", "id: first\n---\nid: second\n", strings.Repeat("x", eval.MaxScenarioBytes+1), "id: a\nid: b\n"} {
		if _, err := eval.Decode(strings.NewReader(source)); err == nil {
			t.Fatal("invalid document accepted")
		}
	}
}
func TestUsagePresenceUnknownRatesAndKnownFree(t *testing.T) {
	for _, mode := range []string{"unreported", "measured-zero", "unknown-rate", "known-free", "inclusive-incomplete"} {
		t.Run(mode, func(t *testing.T) {
			s := fixture(t, "deliberate-fail")
			s.Rubric = eval.Rubric{ContainsAny: []string{"42"}}
			turn := &s.ModelTurns[0]
			switch mode {
			case "unreported":
				turn.Usage = nil
				turn.Price = nil
			case "measured-zero":
				turn.Usage = &eval.UsageReport{Input: ptr(0), Output: ptr(0), CacheRead: ptr(0), CacheWrite: ptr(0), Reasoning: ptr(0)}
			case "unknown-rate":
				turn.Usage = &eval.UsageReport{Input: ptr(10)}
				turn.Price = &usageledger.PriceSnapshot{UnknownRates: usageledger.UnknownRates{UncachedInput: true}}
			case "known-free":
				turn.Usage = &eval.UsageReport{Input: ptr(10), Output: ptr(0), CacheRead: ptr(0), CacheWrite: ptr(0), Reasoning: ptr(0)}
				turn.Price = &usageledger.PriceSnapshot{}
			case "inclusive-incomplete":
				turn.Usage = &eval.UsageReport{Input: ptr(100), CacheRead: ptr(40), Output: ptr(10), InputIncludesCache: true, OutputIncludesReasoning: true}
			}
			result, err := eval.Run(context.Background(), s, eval.Options{})
			if err != nil {
				t.Fatal(err)
			}
			row, cost := result.Metrics.Usage[0], result.Metrics.Costs[0]
			switch mode {
			case "unreported":
				if row.Usage.TotalProvenance() != usageledger.ProvenanceUnknown || cost.Priced {
					t.Fatal("unreported became measured/free")
				}
			case "measured-zero":
				if row.Usage.TotalTokens() != 0 || row.Usage.TotalProvenance() != usageledger.ProvenanceMeasured {
					t.Fatal("measured zero lost")
				}
			case "unknown-rate":
				if !cost.Partial() || cost.UnpricedTokens != 10 || cost.Total() != 0 {
					t.Fatal("unknown rate became free")
				}
			case "known-free":
				if !cost.Priced || cost.Partial() || cost.Total() != 0 || cost.IsBill() {
					t.Fatal("known free tariff lost")
				}
			case "inclusive-incomplete":
				if row.Usage.UncachedInputTokens.Provenance != usageledger.ProvenanceEstimated || row.Usage.OutputTokens.Provenance != usageledger.ProvenanceEstimated || row.Usage.CacheWriteTokens.Provenance != usageledger.ProvenanceUnknown || row.Usage.ReasoningTokens.Provenance != usageledger.ProvenanceUnknown {
					t.Fatal("unknown subdivisions invented")
				}
			}
		})
	}
}
func TestDeterministicResultsAndDurableJSONRoundTrip(t *testing.T) {
	s := fixture(t, "pass")
	a, err := eval.Run(context.Background(), s, eval.Options{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := eval.Run(context.Background(), s, eval.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if a.Passed != b.Passed || a.ScenarioDigest != b.ScenarioDigest || !reflect.DeepEqual(a.Metrics, b.Metrics) || !reflect.DeepEqual(a.Findings, b.Findings) {
		t.Fatal("deterministic case changed results")
	}
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	var restored eval.CaseResult
	if err = json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	before, err := trace.Replay(context.Background(), a.Recording)
	if err != nil {
		t.Fatal(err)
	}
	after, err := trace.Replay(context.Background(), restored.Recording)
	if err != nil {
		t.Fatal(err)
	}
	left, _ := json.Marshal(before)
	right, _ := json.Marshal(after)
	if string(left) != string(right) || restored.ScenarioDigest != a.ScenarioDigest {
		t.Fatal("persisted record changed evidence")
	}
}

func TestScriptedNumericArgumentsKeepExactJSONIdentity(t *testing.T) {
	source := `id: numeric-identity
benchmark: synthetic
user_message: Read this numeric identifier.
tools_offered:
  - name: lookup
    description: Synthetic numeric lookup.
    input_schema:
      type: object
      properties: {id: {type: integer}}
      required: [id]
    mock: {content: found, outcome: succeeded}
model_turns:
  - tool_calls:
      - id: lookup-1
        name: lookup
        arguments: {id: 9007199254740993}
  - text: found
rubric: {called_tool: lookup, contains_any: [found]}
`
	scenario, err := eval.Decode(strings.NewReader(source))
	if err != nil {
		t.Fatal(err)
	}
	number, ok := scenario.ModelTurns[0].ToolCalls[0].Arguments["id"].(json.Number)
	if !ok || number.String() != "9007199254740993" {
		t.Fatal("numeric identifier passed through float64")
	}
	result, err := eval.Run(context.Background(), scenario, eval.Options{})
	if err != nil || !result.Passed {
		t.Fatalf("valid exact numeric argument failed real loop: %v", err)
	}
}
