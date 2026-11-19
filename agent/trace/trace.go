// Package trace defines the per-turn trace record an agent writes once for
// every turn: what the turn was given (slots, tools), what it did (tool calls,
// compactions), what it cost (usage) and how it ended (outcome).
//
// One record per turn is the shared shape that evaluation, a classifier's
// training data and debugging all read. The package holds only the schema
// and its validation; writing records (JSONL files, a host sink, span
// attributes) is the agent's job.
//
// Field names follow two existing vocabularies where they overlap: the SDK
// evidence recording (run_id, turn_id, tool_call_id, tool, model) and Surface's
// context trace (at, slots, trimmed, why, fired, classified). Reserved fields
// are empty until the layers that fill them exist; filling them later does not
// change the schema version.
//
// The evidence subpackage is the other half: a passive recorder of the run's
// runtime events with typed correlations, receipts, export and replay. A turn
// record summarises a turn; an evidence recording keeps its event trail.
//
// A record never holds prompts, completions, tool arguments or tool output.
// Error text it does hold must already be redacted by the writer.
//
// Lists: the five lists every record has (slots, trimmed, tools_offered,
// tool_calls, compactions) are always written, as [] when empty. In the
// reserved lists (fired, hooks_run), null means the layer did not run and []
// means it ran and produced nothing.
//
// The evidence subpackage was extracted from agent/sdk/trace, which remains
// until agent/sdk is retired.
package trace

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Version is the schema version written in every record.
const Version = 1

// ErrInvalid reports a record that does not satisfy the schema.
var ErrInvalid = errors.New("trace: invalid turn record")

// Turn is one turn's trace record.
type Turn struct {
	// Version is the schema version, Version.
	Version int `json:"version"`
	// At is when the turn started; DurationMS how long it ran.
	At         time.Time `json:"at"`
	DurationMS int64     `json:"duration_ms"`

	// ConversationID, RunID and TurnID identify the turn. RunID and TurnID
	// may be equal when an agent has one run per turn.
	ConversationID string `json:"conversation_id"`
	RunID          string `json:"run_id"`
	TurnID         string `json:"turn_id"`
	// ParentConversationID is set for a subagent's turn.
	ParentConversationID string `json:"parent_conversation_id,omitempty"`
	// Role is the conversation's role, such as chat, worker or planner.
	Role string `json:"role,omitempty"`
	// Source says what started the turn: one of the Source constants.
	Source Source `json:"source"`

	// Provider and Model are the ones the turn's requests used.
	Provider string `json:"provider"`
	Model    string `json:"model"`

	// Slots are the context slots assembled for the turn, in order.
	Slots []Slot `json:"slots"`
	// Trimmed lists entries the selector cut or replaced with a pointer, as
	// "slot/owner/key".
	Trimmed []string `json:"trimmed"`

	// ToolsOffered names the tools offered to the model, sorted.
	ToolsOffered []string `json:"tools_offered"`
	// ToolCalls are the calls the model made, in order.
	ToolCalls []ToolCall `json:"tool_calls"`
	// Compactions are the compaction passes during the turn, in order.
	Compactions []Compaction `json:"compactions"`

	// Usage is the turn's token use across every model request.
	Usage Usage `json:"usage"`
	// Outcome is how the turn ended.
	Outcome Outcome `json:"outcome"`

	// Reserved for later layers. They are omitted while empty.

	// Situation describes the moment the turn ran in (message source, mode,
	// focus, host signals). Its shape belongs to the layer that fills it.
	Situation json.RawMessage `json:"situation,omitempty"`
	// Classified is the classifier's result for the turn's message.
	Classified json.RawMessage `json:"classified,omitempty"`
	// Why is the selector's reason for the slots it chose.
	Why string `json:"why,omitempty"`
	// Fired names the reflexes that fired: null when no reflex layer ran.
	Fired []string `json:"fired"`
	// HooksRun names the hooks that ran, as "event/name": null when no hook
	// layer ran.
	HooksRun []string `json:"hooks_run"`
}

// MarshalJSON writes the always-present lists as [] when they are nil.
func (t Turn) MarshalJSON() ([]byte, error) {
	type plain Turn
	p := plain(t)
	if p.Slots == nil {
		p.Slots = []Slot{}
	}
	if p.Trimmed == nil {
		p.Trimmed = []string{}
	}
	if p.ToolsOffered == nil {
		p.ToolsOffered = []string{}
	}
	if p.ToolCalls == nil {
		p.ToolCalls = []ToolCall{}
	}
	if p.Compactions == nil {
		p.Compactions = []Compaction{}
	}
	return json.Marshal(p)
}

// Source says what started a turn.
type Source string

const (
	SourceUser     Source = "user"     // a person's message
	SourceHost     Source = "host"     // an embedding application
	SourceSubagent Source = "subagent" // a parent conversation's task
	SourceResume   Source = "resume"   // recovery of an interrupted turn
	SourceSchedule Source = "schedule" // a scheduled activation
	SourceReflex   Source = "reflex"   // a reflex or internal service
	SourceGoal     Source = "goal"     // a goal-loop step
)

// Slot is one assembled context slot.
type Slot struct {
	Name string `json:"name"`
	// Tokens is the slot's estimated size as sent.
	Tokens int `json:"tokens"`
	// Changed reports that the slot differs from the previous turn's, which
	// breaks the provider's prompt cache from this slot on.
	Changed bool `json:"changed"`
}

// ToolCall is one tool call the model made.
type ToolCall struct {
	ToolCallID string `json:"tool_call_id"`
	Tool       string `json:"tool"`
	DurationMS int64  `json:"duration_ms"`
	// Permission is the decision on the call: one of the Permission
	// constants.
	Permission Permission `json:"permission"`
	// ErrorKind classifies a failed call: one of the ErrorKind constants, or
	// empty for a call that succeeded.
	ErrorKind ErrorKind `json:"error_kind,omitempty"`
}

// Permission is the decision on a tool call.
type Permission string

const (
	PermissionAllow    Permission = "allow"    // allowed by rule or policy
	PermissionApproved Permission = "approved" // allowed after asking
	PermissionDeny     Permission = "deny"     // refused by rule, policy or the person
	PermissionNone     Permission = "none"     // never reached a decision (invalid or unknown call)
)

// ErrorKind classifies a failed tool call.
type ErrorKind string

const (
	ErrorTool        ErrorKind = "tool_error"   // the tool ran and reported an error
	ErrorDenied      ErrorKind = "denied"       // permission refused the call
	ErrorInvalidArgs ErrorKind = "invalid_args" // arguments failed validation
	ErrorUnknownTool ErrorKind = "unknown_tool" // the model named no offered tool
	ErrorCancelled   ErrorKind = "cancelled"    // the turn was cancelled first
	ErrorPanic       ErrorKind = "panic"        // the tool panicked and was contained
	ErrorLoop        ErrorKind = "loop"         // refused as a repeated identical call
)

// Compaction is one compaction pass.
type Compaction struct {
	// Before and After are the conversation's estimated tokens.
	Before int `json:"before"`
	After  int `json:"after"`
	// Forced reports a pass made to recover from a context overflow.
	Forced bool `json:"forced,omitempty"`
	// Failed reports a pass that failed or freed nothing; Error says why.
	Failed bool   `json:"failed,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Usage is token use. Input and output match the OpenTelemetry GenAI usage
// attributes; the cache counts map to gen_ai.usage.cache_read.input_tokens
// and gen_ai.usage.cache_creation.input_tokens. Cost stays nil until the
// agent knows a price.
type Usage struct {
	InputTokens      int      `json:"input_tokens"`
	OutputTokens     int      `json:"output_tokens"`
	CacheReadTokens  int      `json:"cache_read_tokens"`
	CacheWriteTokens int      `json:"cache_write_tokens"`
	Cost             *float64 `json:"cost,omitempty"`
}

// Outcome is how a turn ended.
type Outcome struct {
	// Status is one of the Status constants.
	Status Status `json:"status"`
	// FinishReason is the turn's stop reason (end_turn, max_tokens,
	// max_steps, model_context_window_exceeded, ...), empty when it failed
	// before one was known.
	FinishReason string `json:"finish_reason,omitempty"`
	// Steps is the number of model requests that completed.
	Steps int `json:"steps"`
	// Error is the redacted error of a failed turn.
	Error string `json:"error,omitempty"`
}

// Status is how a turn ended.
type Status string

const (
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// Validate reports the first way t breaks the schema. It is for writers: a
// reader should not call it, since a newer record may carry a version or an
// enum value this one does not know.
func (t Turn) Validate() error {
	switch {
	case t.Version != Version:
		return fmt.Errorf("%w: version %d, want %d", ErrInvalid, t.Version, Version)
	case t.At.IsZero():
		return fmt.Errorf("%w: at is required", ErrInvalid)
	case t.ConversationID == "" || t.RunID == "" || t.TurnID == "":
		return fmt.Errorf("%w: conversation_id, run_id and turn_id are required", ErrInvalid)
	case t.DurationMS < 0 || t.Outcome.Steps < 0:
		return fmt.Errorf("%w: duration_ms and steps must not be negative", ErrInvalid)
	case t.Usage.InputTokens < 0 || t.Usage.OutputTokens < 0 || t.Usage.CacheReadTokens < 0 || t.Usage.CacheWriteTokens < 0:
		return fmt.Errorf("%w: usage must not be negative", ErrInvalid)
	}
	for i, slot := range t.Slots {
		if slot.Name == "" || slot.Tokens < 0 {
			return fmt.Errorf("%w: slots[%d] needs a name and nonnegative tokens", ErrInvalid, i)
		}
	}
	for i, c := range t.Compactions {
		if c.Before < 0 || c.After < 0 {
			return fmt.Errorf("%w: compactions[%d] must not be negative", ErrInvalid, i)
		}
	}
	switch t.Source {
	case SourceUser, SourceHost, SourceSubagent, SourceResume, SourceSchedule, SourceReflex, SourceGoal:
	default:
		return fmt.Errorf("%w: source %q", ErrInvalid, t.Source)
	}
	switch t.Outcome.Status {
	case StatusCompleted, StatusFailed, StatusCancelled:
	default:
		return fmt.Errorf("%w: outcome status %q", ErrInvalid, t.Outcome.Status)
	}
	for i, call := range t.ToolCalls {
		if call.ToolCallID == "" || call.Tool == "" {
			return fmt.Errorf("%w: tool_calls[%d] needs tool_call_id and tool", ErrInvalid, i)
		}
		switch call.Permission {
		case PermissionAllow, PermissionApproved, PermissionDeny, PermissionNone:
		default:
			return fmt.Errorf("%w: tool_calls[%d] permission %q", ErrInvalid, i, call.Permission)
		}
		switch call.ErrorKind {
		case "", ErrorTool, ErrorDenied, ErrorInvalidArgs, ErrorUnknownTool, ErrorCancelled, ErrorPanic, ErrorLoop:
		default:
			return fmt.Errorf("%w: tool_calls[%d] error_kind %q", ErrInvalid, i, call.ErrorKind)
		}
		if call.DurationMS < 0 {
			return fmt.Errorf("%w: tool_calls[%d] duration_ms is negative", ErrInvalid, i)
		}
	}
	return nil
}

// Attribute is a span attribute key and value, so an agent can mirror a
// record onto its turn span without this package importing OpenTelemetry.
type Attribute struct {
	Key   string
	Value any
}

// Attributes returns the record's summary as span attributes. Provider,
// model, usage and finish reason use the current OpenTelemetry GenAI keys;
// the rest are under "agent.turn.".
func (t Turn) Attributes() []Attribute {
	attrs := []Attribute{
		{"gen_ai.provider.name", t.Provider},
		{"gen_ai.request.model", t.Model},
		{"gen_ai.usage.input_tokens", t.Usage.InputTokens},
		{"gen_ai.usage.output_tokens", t.Usage.OutputTokens},
		{"agent.turn.source", string(t.Source)},
		{"agent.turn.status", string(t.Outcome.Status)},
		{"agent.turn.steps", t.Outcome.Steps},
		{"agent.turn.tools_offered", len(t.ToolsOffered)},
		{"agent.turn.tool_calls", len(t.ToolCalls)},
		{"agent.turn.compactions", len(t.Compactions)},
		{"gen_ai.usage.cache_read.input_tokens", t.Usage.CacheReadTokens},
		{"gen_ai.usage.cache_creation.input_tokens", t.Usage.CacheWriteTokens},
	}
	if t.Outcome.FinishReason != "" {
		attrs = append(attrs, Attribute{"gen_ai.response.finish_reasons", []string{t.Outcome.FinishReason}})
	}
	return attrs
}
