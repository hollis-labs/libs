package eval_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hollis-labs/libs/agent/sdk/eval"
)

func numericScenario(t *testing.T, kind string, value any) eval.Scenario {
	t.Helper()
	s := fixture(t, "pass")
	s.ToolsOffered[0].InputSchema = map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": kind}}, "required": []string{"id"}}
	s.ModelTurns[0].ToolCalls[0].Arguments = map[string]any{"id": value}
	return s
}
func assertNumericRefusalBeforeActivity(t *testing.T, s eval.Scenario) {
	t.Helper()
	result, err := eval.Run(context.Background(), s, eval.Options{})
	if err == nil || result.Outcome.Turns != 0 || result.Metrics.Turns != 0 || result.Metrics.FakeToolCalls != 0 || len(result.Recording.Entries()) != 0 {
		t.Fatalf("unsupported numeric value reached model/tool activity: err=%v turns=%d effects=%d entries=%d", err, result.Outcome.Turns, result.Metrics.FakeToolCalls, len(result.Recording.Entries()))
	}
}
func numericDocument(t *testing.T, format, number, kind string) string {
	t.Helper()
	if format == "json" {
		// RawMessage embeds the authored numeric lexeme without a float conversion.
		s := numericScenario(t, kind, json.RawMessage(number))
		raw, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	return fmt.Sprintf(`id: numeric-decoding
benchmark: synthetic
user_message: Read the synthetic value.
tools_offered:
  - name: lookup
    description: Synthetic numeric lookup.
    input_schema:
      type: object
      properties: {id: {type: %s}}
      required: [id]
    mock: {content: '42', outcome: succeeded}
model_turns:
  - tool_calls:
      - id: lookup-1
        name: lookup
        arguments: {id: %s}
  - text: The value is 42.
rubric: {called_tool: lookup, contains_any: ['42']}
`, kind, number)
}
func TestDecodeRefusesLossyNumericValuesInYAMLAndJSON(t *testing.T) {
	for _, format := range []string{"yaml", "json"} {
		for _, number := range []string{
			"9007199254740993.0", "9007199254740993e0", "0.12345678901234567890123456789",
			"1.0000000000000000001", "1.0000000000000000001e0", "1e9999",
		} {
			t.Run(format+"/"+number, func(t *testing.T) {
				s, err := eval.Decode(strings.NewReader(numericDocument(t, format, number, "number")))
				if !errors.Is(err, eval.ErrUnsupported) {
					t.Fatalf("lossy authored numeric value was not explicitly refused: %v", err)
				}
				// A partially decoded value must not be executable after its error either.
				assertNumericRefusalBeforeActivity(t, s)
			})
		}
	}
}
func TestProgrammaticNumbersCannotRoundThroughIntegerSchema(t *testing.T) {
	for _, number := range []string{"1.0000000000000000001", "-1.0000000000000000001", "1.0000000000000000001e0", "0.99999999999999999999"} {
		t.Run(number, func(t *testing.T) {
			s := numericScenario(t, "integer", json.Number(number))
			assertNumericRefusalBeforeActivity(t, s)
		})
	}
}
func TestProgrammaticNumberSchemaAlsoRefusesLossyValues(t *testing.T) {
	for _, number := range []string{"9007199254740993.0", "9007199254740993e0", "0.12345678901234567890123456789", "0e999999999"} {
		t.Run(number, func(t *testing.T) {
			s := numericScenario(t, "number", json.Number(number))
			result, err := eval.Run(context.Background(), s, eval.Options{})
			if !errors.Is(err, eval.ErrUnsupported) {
				t.Fatalf("lossy Number accepted or implicit refusal: %v", err)
			}
			if result.Outcome.Turns != 0 || result.Metrics.FakeToolCalls != 0 || len(result.Recording.Entries()) != 0 {
				t.Fatal("numeric refusal followed execution")
			}
		})
	}
}
func TestSupportedNumbersStillExecuteUnderOriginalSchema(t *testing.T) {
	for _, test := range []struct{ number, kind string }{
		{"9007199254740993", "integer"}, {"18446744073709551615", "integer"}, {"-9223372036854775808", "integer"},
		{"1.0", "integer"}, {"1e3", "integer"}, {"0.1", "number"}, {"1.5", "number"}, {"1e-3", "number"},
	} {
		for _, format := range []string{"yaml", "json", "programmatic"} {
			t.Run(format+"/"+test.number, func(t *testing.T) {
				s := numericScenario(t, test.kind, json.Number(test.number))
				if format != "programmatic" {
					var err error
					s, err = eval.Decode(strings.NewReader(numericDocument(t, format, test.number, test.kind)))
					if err != nil {
						t.Fatal(err)
					}
				}
				result, err := eval.Run(context.Background(), s, eval.Options{})
				if err != nil || !result.Passed || result.Metrics.Turns != 2 || result.Metrics.FakeToolCalls != 1 {
					t.Fatalf("supported numeric behavior changed: err=%v passed=%t turns=%d effects=%d", err, result.Passed, result.Metrics.Turns, result.Metrics.FakeToolCalls)
				}
			})
		}
	}
	// A genuine fractional value must still fail an integer schema, rather than
	// changing the schema or normalizing the argument into an integer.
	assertNumericRefusalBeforeActivity(t, numericScenario(t, "integer", json.Number("1.5")))
}
