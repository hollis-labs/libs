package eval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/hollis-labs/libs/agent/sdk"
	"github.com/hollis-labs/libs/agent/sdk/internal/fakehost"
	"github.com/hollis-labs/libs/agent/sdk/trace"
	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
	"github.com/hollis-labs/libs/llm/core/costcalc"
	"github.com/hollis-labs/libs/llm/core/llmtypes"
	"github.com/hollis-labs/libs/llm/core/usageledger"
	"github.com/hollis-labs/libs/mesh/core/gate"
	"github.com/hollis-labs/libs/mesh/core/gate/gatetest"
	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
)

// Options contains data only. No callbacks or live SDK/model/tool/host ports can
// be injected into this evaluator. Start is virtual time, not a service clock.
type Options struct {
	Start    time.Time
	Evidence trace.Metadata
}
type Finding struct {
	Criterion string `json:"criterion"`
	Message   string `json:"message"`
}
type Metrics struct {
	Turns           int               `json:"turns"`
	FakeToolCalls   int               `json:"fake_tool_calls"`
	VirtualDuration time.Duration     `json:"virtual_duration"`
	Usage           []usageledger.Row `json:"usage"`
	Costs           []costcalc.Cost   `json:"costs"` // derived from each stored snapshot, never a live catalog
}
type CaseResult struct {
	ScenarioID     string          `json:"scenario_id"`
	Benchmark      string          `json:"benchmark"`
	ScenarioDigest string          `json:"scenario_digest"`
	Passed         bool            `json:"passed"`
	Findings       []Finding       `json:"findings"`
	Outcome        sdk.Result      `json:"outcome"`
	Metrics        Metrics         `json:"metrics"`
	Recording      trace.Recording `json:"recording"`
}

// Run validates unsupported semantics, all scripts/schemas/accounting and options
// before any loop activity. It then invokes actual sdk.New/Run with synthetic
// providers/tools/host admission. Rubric failure is data (Passed=false, nil error);
// a runtime failure also returns its error and whatever evidence was observed.
func Run(ctx context.Context, scenario Scenario, options Options) (out CaseResult, err error) {
	schemas, err := validate(scenario)
	if err != nil {
		return out, err
	}
	// Detach caller-owned maps/slices before accepting a scenario.
	raw, err := json.Marshal(scenario)
	if err != nil {
		return out, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err = decoder.Decode(&scenario); err != nil {
		return out, err
	}
	recorder, err := trace.NewRecorder(options.Evidence)
	if err != nil {
		return out, err
	}
	h := fakehost.New()
	if !options.Start.IsZero() {
		h.NowTime = options.Start.UTC()
	}
	h.Environment.ExpiresAt = h.Now().Add(24 * time.Hour)
	if options.Evidence.Snapshot != nil && options.Evidence.Snapshot.OwnerID != h.Binding.OwnerID {
		return out, ErrInvalid
	}
	start := h.Now()
	sum := sha256.Sum256(raw)
	out = CaseResult{ScenarioID: scenario.ID, Benchmark: scenario.Benchmark, ScenarioDigest: "sha256:" + hex.EncodeToString(sum[:])}
	tools := make([]sdk.Tool, 0, len(scenario.ToolsOffered))
	mockByName := map[string]Mock{}
	for _, spec := range scenario.ToolsOffered {
		mockByName[spec.Name] = spec.Mock
		tool := sdk.Tool{Definition: llmtypes.ToolDefinition{Name: spec.Name, Description: spec.Description, InputSchema: spec.InputSchema}, Versions: []runtimecontract.Reference{gatetest.Reference("eval-tool-" + spec.Name)}}
		tool.Execute = func(c context.Context, args map[string]any) (sdk.ToolResult, error) {
			if err := c.Err(); err != nil {
				return sdk.ToolResult{}, err
			}
			if err := validateArguments(schemas[spec.Name], args); err != nil {
				return sdk.ToolResult{}, ErrInvalid
			}
			h.Effects++
			if spec.Mock.Outcome == gate.ExecutionUnknown {
				return sdk.ToolResult{}, ErrUncertainEffect
			}
			return sdk.ToolResult{Content: spec.Mock.Content}, nil
		}
		tools = append(tools, tool)
	}
	if err = h.SetTools(tools); err != nil {
		return out, err
	}
	h.ReceiptSink = recorder
	ports := h.Ports()
	ports.Events = recorder
	ports.Limits.MaxTurns = len(scenario.ModelTurns)
	definition := fakehost.Definition()
	definition.Requirements.Tools = nil
	for _, tool := range tools {
		definition.Requirements.Tools = append(definition.Requirements.Tools, tool.Definition.Name)
	}
	definition.Body = "Execute the synthetic scenario using only its offered tools."
	input := h.Input()
	input.ID = "eval/" + scenario.ID
	input.Text = scenario.UserMessage
	next := 0
	h.Model.Stream = func(c context.Context, request llmtypes.ChatRequest) (<-chan llmtypes.StreamEvent, error) {
		if next >= len(scenario.ModelTurns) {
			return nil, fmt.Errorf("%w: model script exhausted", ErrInvalid)
		}
		if next == 0 {
			if len(request.Messages) != 1 || request.Messages[0].Content != scenario.UserMessage {
				return nil, ErrInvalid
			}
		} else {
			previous := scenario.ModelTurns[next-1]
			if err := checkToolResults(request, previous.ToolCalls, mockByName); err != nil {
				return nil, err
			}
		}
		script := scenario.ModelTurns[next]
		next++
		recording, err := recorder.Snapshot()
		if err != nil {
			return nil, err
		}
		entries := recording.Entries()
		if len(entries) == 0 || entries[len(entries)-1].Event == nil || entries[len(entries)-1].Event.Kind != runtimeevents.KindTurnStarted {
			return nil, ErrInvalid
		}
		correlation := entries[len(entries)-1].Correlation
		h.NowTime = h.Now().Add(time.Duration(script.DurationMillis) * time.Millisecond)
		normalized, err := script.Usage.normalized()
		if err != nil {
			return nil, err
		}
		row := usageledger.Row{SessionID: string(h.Binding.ConversationID), MessageID: string(correlation.ModelCallID), Provider: "synthetic", Model: request.Model, Usage: normalized, Price: script.Price, CostKind: usageledger.CostKindUnspecified, RecordedAt: h.Now()}
		if err = recorder.RecordUsage(c, correlation, row); err != nil {
			return nil, err
		}
		var events []llmtypes.StreamEvent
		if script.Text != "" {
			events = append(events, llmtypes.StreamEvent{Type: llmtypes.EventDelta, Content: script.Text})
		}
		for _, call := range script.ToolCalls {
			block := llmtypes.ToolUseBlock{ID: call.ID, Name: call.Name, Input: call.Arguments}
			events = append(events, llmtypes.StreamEvent{Type: llmtypes.EventToolUse, ToolUse: &block})
		}
		// The legacy stream shape cannot represent presence/reasoning. Preserve the
		// complete report in the explicit typed row, never in StreamEvent.Content.
		if script.Usage != nil {
			events = append(events, llmtypes.StreamEvent{Type: llmtypes.EventUsage, Usage: &llmtypes.Usage{InputTokens: int(normalized.UncachedInputTokens.Tokens), OutputTokens: int(normalized.OutputTokens.Tokens + normalized.ReasoningTokens.Tokens), CacheReadTokens: int(normalized.CacheReadTokens.Tokens), CacheCreationTokens: int(normalized.CacheWriteTokens.Tokens)}})
		}
		events = append(events, llmtypes.StreamEvent{Type: llmtypes.EventDone})
		return fakehost.Stream(events...), nil
	}
	operative, err := sdk.New(definition, ports)
	if err != nil {
		return out, err
	}
	out.Outcome, err = operative.Run(ctx, input)
	recording, snapshotErr := recorder.Snapshot()
	out.Recording = recording
	out.Metrics = Metrics{Turns: out.Outcome.Turns, FakeToolCalls: h.Effects, VirtualDuration: h.Now().Sub(start)}
	for _, entry := range recording.Entries() {
		if entry.Usage != nil {
			out.Metrics.Usage = append(out.Metrics.Usage, *entry.Usage)
			out.Metrics.Costs = append(out.Metrics.Costs, costcalc.PriceRow(*entry.Usage))
		}
	}
	out.Findings = grade(scenario, out)
	if next != len(scenario.ModelTurns) {
		out.Findings = append(out.Findings, Finding{Criterion: "model_script", Message: "not all scripted turns were consumed"})
	}
	if err != nil {
		out.Findings = append(out.Findings, Finding{Criterion: "execution", Message: "SDK run failed; inspect terminal/effect evidence"})
	}
	out.Passed = len(out.Findings) == 0 && err == nil && snapshotErr == nil
	return out, errors.Join(err, snapshotErr)
}
func checkToolResults(request llmtypes.ChatRequest, calls []ToolCall, mocks map[string]Mock) error {
	if len(calls) == 0 || len(request.Messages) < 3 {
		return fmt.Errorf("%w: unpaired continuation", ErrInvalid)
	}
	last := request.Messages[len(request.Messages)-1]
	if last.Role != "user" || len(last.ContentBlocks) != len(calls) {
		return fmt.Errorf("%w: missing paired tool results", ErrInvalid)
	}
	for i, call := range calls {
		b := last.ContentBlocks[i]
		if b.Type != "tool_result" || b.ToolUseID != call.ID || b.Content != mocks[call.Name].Content || b.IsError {
			return fmt.Errorf("%w: changed tool result", ErrInvalid)
		}
	}
	return nil
}
func grade(s Scenario, result CaseResult) []Finding {
	var findings []Finding
	fail := func(criterion, message string) { findings = append(findings, Finding{criterion, message}) }
	expected := s.Rubric.ExpectedStatus
	if expected == "" {
		expected = sdk.Succeeded
	}
	if result.Outcome.Status != expected {
		fail("status", "run status differs from rubric")
	}
	// A tool invocation is observed only when host execution actually started.
	// A model request, allow decision or tool name in response text is insufficient.
	var trajectory []string
	for _, entry := range result.Recording.Entries() {
		if entry.Execution != nil && entry.Execution.State == gate.ExecutionStarted {
			trajectory = append(trajectory, entry.Correlation.Tool)
		}
	}
	if s.Rubric.CalledTool != "" && !includes(trajectory, s.Rubric.CalledTool) {
		fail("called_tool", "expected tool was not executed")
	}
	for _, tool := range s.Rubric.AvoidedTools {
		if includes(trajectory, tool) {
			fail("avoided_tools", "a forbidden tool was executed")
		}
	}
	if len(s.Rubric.ToolOrder) > 0 && !reflect.DeepEqual(trajectory, s.Rubric.ToolOrder) {
		fail("tool_order", "observed execution order differs")
	}
	lower := strings.ToLower(result.Outcome.Text)
	if len(s.Rubric.ContainsAny) > 0 {
		found := false
		for _, text := range s.Rubric.ContainsAny {
			found = found || strings.Contains(lower, strings.ToLower(text))
		}
		if !found {
			fail("contains_any", "response lacks expected text")
		}
	}
	for _, text := range s.Rubric.DoesNotContain {
		if strings.Contains(lower, strings.ToLower(text)) {
			fail("does_not_contain", "response contains forbidden text")
		}
	}
	return findings
}
func includes(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
