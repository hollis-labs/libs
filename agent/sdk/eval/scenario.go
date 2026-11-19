// Package eval runs bounded deterministic scenarios through the actual SDK loop
// with synthetic models, tools and host authority. Its API accepts no live ports.
package eval

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/hollis-labs/libs/agent/sdk"
	"github.com/hollis-labs/libs/llm/core/costcalc"
	"github.com/hollis-labs/libs/llm/core/usageledger"
	"github.com/hollis-labs/libs/mesh/core/gate"
	"gopkg.in/yaml.v3"
)

var (
	ErrInvalid         = errors.New("eval: invalid scenario")
	ErrUnsupported     = errors.New("eval: unsupported scenario semantics")
	ErrUncertainEffect = errors.New("eval: synthetic effect outcome unknown")
)

const MaxScenarioBytes = 1 << 20

// Scenario retains the original Nanite scenario field names. ModelTurns and
// full tool schemas/mock outcomes replace single-completion, prose-only grading.
type Scenario struct {
	ID               string         `json:"id"`
	Benchmark        string         `json:"benchmark"`
	Description      string         `json:"description,omitempty"`
	UserMessage      string         `json:"user_message"`
	ToolsOffered     []Tool         `json:"tools_offered,omitempty"`
	PrevTurns        []PreviousTurn `json:"prev_turns,omitempty"`
	ExpectedBehavior string         `json:"expected_behavior,omitempty"` // descriptive, never an implicit grader
	Rubric           Rubric         `json:"rubric"`
	ModelTurns       []ModelTurn    `json:"model_turns"`
}
type PreviousTurn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
	Mock        Mock           `json:"mock"`
}
type Mock struct {
	Content string              `json:"content"`
	Outcome gate.ExecutionState `json:"outcome"` // succeeded or unknown; never invokes an external tool
}
type ToolCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}
type ModelTurn struct {
	Text           string                     `json:"text,omitempty"`
	ToolCalls      []ToolCall                 `json:"tool_calls,omitempty"`
	Usage          *UsageReport               `json:"usage,omitempty"`
	Price          *usageledger.PriceSnapshot `json:"price,omitempty"`
	DurationMillis int64                      `json:"duration_ms,omitempty"` // virtual time; no sleeping
}

// UsageReport is the presence-aware provider fixture projection, not a second
// ledger. Counts are final per-call values. Nil means unreported; &0 is measured.
// Inclusive flags adapt provider-style totals to the shared disjoint ledger.
type UsageReport struct {
	Input                   *int64 `json:"input,omitempty"`
	Output                  *int64 `json:"output,omitempty"`
	CacheRead               *int64 `json:"cache_read,omitempty"`
	CacheWrite              *int64 `json:"cache_write,omitempty"`
	Reasoning               *int64 `json:"reasoning,omitempty"`
	InputIncludesCache      bool   `json:"input_includes_cache,omitempty"`
	OutputIncludesReasoning bool   `json:"output_includes_reasoning,omitempty"`
}

func (r *UsageReport) normalized() (usageledger.Usage, error) {
	u := usageledger.NewUsage()
	if r == nil {
		return u, nil
	}
	component := func(n *int64) (usageledger.Component, error) {
		if n == nil {
			return usageledger.Component{Provenance: usageledger.ProvenanceUnknown}, nil
		}
		if *n < 0 || *n > 1_000_000_000 {
			return usageledger.Component{}, ErrInvalid
		}
		return usageledger.Component{Tokens: *n, Provenance: usageledger.ProvenanceMeasured}, nil
	}
	for _, pair := range []struct {
		src *int64
		dst *usageledger.Component
	}{{r.Input, &u.UncachedInputTokens}, {r.Output, &u.OutputTokens}, {r.CacheRead, &u.CacheReadTokens}, {r.CacheWrite, &u.CacheWriteTokens}, {r.Reasoning, &u.ReasoningTokens}} {
		c, err := component(pair.src)
		if err != nil {
			return u, err
		}
		*pair.dst = c
	}
	if r.InputIncludesCache && r.Input != nil {
		cache := u.CacheReadTokens.Tokens + u.CacheWriteTokens.Tokens
		if cache > *r.Input {
			return u, fmt.Errorf("%w: cache exceeds inclusive input", ErrInvalid)
		}
		u.UncachedInputTokens.Tokens -= cache
		if r.CacheRead == nil || r.CacheWrite == nil {
			u.UncachedInputTokens.Provenance = usageledger.ProvenanceEstimated
		}
	}
	if r.OutputIncludesReasoning && r.Output != nil {
		if u.ReasoningTokens.Tokens > *r.Output {
			return u, fmt.Errorf("%w: reasoning exceeds inclusive output", ErrInvalid)
		}
		u.OutputTokens.Tokens -= u.ReasoningTokens.Tokens
		if r.Reasoning == nil {
			u.OutputTokens.Provenance = usageledger.ProvenanceEstimated
		}
	}
	return u, u.Validate()
}

type Rubric struct {
	CalledTool         string     `json:"called_tool,omitempty"`
	AvoidedTools       []string   `json:"avoided_tools,omitempty"`
	ToolOrder          []string   `json:"tool_order,omitempty"`
	ContainsAny        []string   `json:"contains_any,omitempty"`
	DoesNotContain     []string   `json:"does_not_contain,omitempty"`
	ExpectedStatus     sdk.Status `json:"expected_status,omitempty"`     // empty requires succeeded
	AskedClarification *bool      `json:"asked_clarification,omitempty"` // explicitly unsupported, including false
}

// Decode accepts one bounded YAML/JSON document and rejects unknown fields.
// Shared ledger price JSON tags work in YAML without a separate price model.
func Decode(reader io.Reader) (Scenario, error) {
	var s Scenario
	raw, err := io.ReadAll(io.LimitReader(reader, MaxScenarioBytes+1))
	if err != nil {
		return s, err
	}
	if len(raw) > MaxScenarioBytes {
		return s, fmt.Errorf("%w: scenario size", ErrInvalid)
	}
	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	if err = decoder.Decode(&document); err != nil {
		return s, err
	}
	var extra yaml.Node
	if err = decoder.Decode(&extra); err != io.EOF {
		return s, fmt.Errorf("%w: expected one document", ErrInvalid)
	}
	if err = checkAuthoredNumbers(&document); err != nil {
		return s, err
	}
	var value any
	if err = document.Decode(&value); err != nil {
		return s, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return s, err
	}
	jsonDecoder := json.NewDecoder(bytes.NewReader(encoded))
	jsonDecoder.DisallowUnknownFields()
	jsonDecoder.UseNumber()
	if err = jsonDecoder.Decode(&s); err != nil {
		return s, err
	}
	_, err = validate(s)
	return s, err
}
func validate(s Scenario) (map[string]*jsonschema.Resolved, error) {
	if len(s.PrevTurns) > 0 || s.Rubric.AskedClarification != nil {
		return nil, fmt.Errorf("%w: history and clarification grading", ErrUnsupported)
	}
	if strings.TrimSpace(s.ID) == "" || strings.TrimSpace(s.Benchmark) == "" || strings.TrimSpace(s.UserMessage) == "" || len(s.ModelTurns) == 0 || len(s.ModelTurns) > 64 || len(s.ToolsOffered) > 128 {
		return nil, ErrInvalid
	}
	raw, err := json.Marshal(s)
	if err != nil || len(raw) > MaxScenarioBytes {
		return nil, ErrInvalid
	}
	if s.Rubric.CalledTool == "" && len(s.Rubric.AvoidedTools) == 0 && len(s.Rubric.ToolOrder) == 0 && len(s.Rubric.ContainsAny) == 0 && len(s.Rubric.DoesNotContain) == 0 && s.Rubric.ExpectedStatus == "" {
		return nil, fmt.Errorf("%w: empty rubric", ErrInvalid)
	}
	if s.Rubric.ExpectedStatus != "" && s.Rubric.ExpectedStatus != sdk.Succeeded && s.Rubric.ExpectedStatus != sdk.Failed && s.Rubric.ExpectedStatus != sdk.Pending && s.Rubric.ExpectedStatus != sdk.Cancelled {
		return nil, ErrUnsupported
	}
	for _, list := range [][]string{s.Rubric.AvoidedTools, s.Rubric.ToolOrder, s.Rubric.ContainsAny, s.Rubric.DoesNotContain} {
		for _, v := range list {
			if strings.TrimSpace(v) == "" {
				return nil, ErrInvalid
			}
		}
	}
	schemas := map[string]*jsonschema.Resolved{}
	for _, tool := range s.ToolsOffered {
		if strings.TrimSpace(tool.Name) == "" || strings.TrimSpace(tool.Description) == "" || tool.InputSchema == nil || schemas[tool.Name] != nil || len(tool.Mock.Content) > 64*1024 {
			return nil, ErrInvalid
		}
		if tool.Mock.Outcome != gate.ExecutionSucceeded && tool.Mock.Outcome != gate.ExecutionUnknown {
			return nil, ErrUnsupported
		}
		data, err := json.Marshal(tool.InputSchema)
		if err != nil {
			return nil, err
		}
		var schema jsonschema.Schema
		if err = json.Unmarshal(data, &schema); err != nil {
			return nil, err
		}
		// A nil Loader refuses remote refs; evaluation never fetches schemas.
		resolved, err := schema.Resolve(&jsonschema.ResolveOptions{})
		if err != nil {
			return nil, fmt.Errorf("%w: schema: %v", ErrUnsupported, err)
		}
		schemas[tool.Name] = resolved
	}
	calls := 0
	ids := map[string]bool{}
	for _, turn := range s.ModelTurns {
		if turn.DurationMillis < 0 || turn.DurationMillis > 60000 || len(turn.ToolCalls) > 32 {
			return nil, ErrInvalid
		}
		normalized, err := turn.Usage.normalized()
		if err != nil {
			return nil, err
		}
		if turn.Price != nil {
			if err := turn.Price.Validate(); err != nil {
				return nil, err
			}
			priced := costcalc.PriceRow(usageledger.Row{Usage: normalized, Price: turn.Price})
			if math.IsInf(priced.Total(), 0) || math.IsNaN(priced.Total()) {
				return nil, fmt.Errorf("%w: non-finite fixture cost", ErrInvalid)
			}
		}
		for _, call := range turn.ToolCalls {
			calls++
			if calls > 128 || strings.TrimSpace(call.ID) == "" || ids[call.ID] || schemas[call.Name] == nil || call.Arguments == nil {
				return nil, ErrInvalid
			}
			ids[call.ID] = true
			if err := validateArguments(schemas[call.Name], call.Arguments); err != nil {
				return nil, fmt.Errorf("%w: tool arguments: %w", ErrInvalid, err)
			}
		}
	}
	return schemas, nil
}

// The published schema validator's type classifier treats json.Number as a
// string. Project only its validation view; authoritative arguments keep their
// accepted JSON tokens throughout the SDK operation descriptor.
func validateArguments(schema *jsonschema.Resolved, args map[string]any) error {
	view, err := validationView(args)
	if err != nil {
		return err
	}
	return schema.Validate(view)
}
func validationView(v any) (any, error) {
	switch value := v.(type) {
	case json.Number:
		if n, err := value.Int64(); err == nil {
			return n, nil
		}
		if n, err := strconv.ParseUint(value.String(), 10, 64); err == nil {
			return n, nil
		}
		n, err := value.Float64()
		if err != nil || math.IsInf(n, 0) || math.IsNaN(n) {
			return nil, fmt.Errorf("%w: non-finite numeric argument", ErrUnsupported)
		}
		encoded, err := json.Marshal(n)
		if err != nil {
			return nil, err
		}
		if err = compareNumbers(value.String(), string(encoded), false); err != nil {
			return nil, err
		}
		return n, nil
	case map[string]any:
		out := make(map[string]any, len(value))
		for k, v := range value {
			projected, err := validationView(v)
			if err != nil {
				return nil, err
			}
			out[k] = projected
		}
		return out, nil
	case []any:
		out := make([]any, len(value))
		for i, v := range value {
			projected, err := validationView(v)
			if err != nil {
				return nil, err
			}
			out[i] = projected
		}
		return out, nil
	default:
		return v, nil
	}
}

// Check original scalar text before YAML's generic decoder can turn a numeric
// value into float64. Alias targets are already in Content; do not expand them
// here. The normal decoder retains its existing alias/merge/duplicate checks.
func checkAuthoredNumbers(node *yaml.Node) error {
	// An out-of-range plain JSON number can resolve as a YAML string. Refuse it
	// rather than letting a numeric token acquire string-schema permissions.
	if node.Kind == yaml.ScalarNode && node.Style == 0 && node.ShortTag() == "!!str" {
		decoder := json.NewDecoder(strings.NewReader(node.Value))
		decoder.UseNumber()
		var value any
		if decoder.Decode(&value) == nil {
			if _, numeric := value.(json.Number); numeric && json.Valid([]byte(node.Value)) {
				return fmt.Errorf("%w: unsupported numeric scalar", ErrUnsupported)
			}
		}
	}

	if node.Kind == yaml.ScalarNode && (node.ShortTag() == "!!int" || node.ShortTag() == "!!float") {
		var decoded any
		if err := node.Decode(&decoded); err != nil {
			return fmt.Errorf("%w: unsupported numeric scalar", ErrUnsupported)
		}
		encoded, err := json.Marshal(decoded)
		if err != nil {
			return fmt.Errorf("%w: unsupported numeric scalar", ErrUnsupported)
		}
		if err = compareNumbers(strings.ReplaceAll(node.Value, "_", ""), string(encoded), node.ShortTag() == "!!int"); err != nil {
			return err
		}
	}
	for _, child := range node.Content {
		if err := checkAuthoredNumbers(child); err != nil {
			return err
		}
	}
	return nil
}

// Compare mathematical decimal values, not their spellings or a second rounded
// float. JSON's shortest float representation must retain the original value.
// The length bound also prevents unbounded big-number work on fixture input.
func compareNumbers(authored, projected string, yamlInteger bool) error {
	const maxNumericBytes = 1024
	unsupported := func() error {
		return fmt.Errorf("%w: numeric value cannot be preserved at supported precision", ErrUnsupported)
	}
	if len(authored) > maxNumericBytes || len(projected) > maxNumericBytes {
		return unsupported()
	}
	if !yamlInteger {
		if index := strings.LastIndexAny(authored, "eE"); index >= 0 {
			exponent, err := strconv.ParseInt(authored[index+1:], 10, 32)
			if err != nil || exponent < -1024 || exponent > 1024 {
				return unsupported()
			}
		}
	}
	var original *big.Rat
	if yamlInteger {
		integer, ok := new(big.Int).SetString(authored, 0)
		if !ok {
			return unsupported()
		}
		original = new(big.Rat).SetInt(integer)
	} else {
		var ok bool
		original, ok = new(big.Rat).SetString(authored)
		if !ok {
			return unsupported()
		}
	}
	converted, ok := new(big.Rat).SetString(projected)
	if !ok || original.Cmp(converted) != 0 {
		return unsupported()
	}
	return nil
}
