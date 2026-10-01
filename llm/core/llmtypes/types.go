package llmtypes

import "strings"

// ProviderCapabilities describes the capabilities supported by a provider.
type ProviderCapabilities struct {
	// SupportsStreamJSON indicates if the provider supports streaming responses with JSON tools
	SupportsStreamJSON bool
	// SupportsPreToolHooks indicates if the provider supports pre-tool execution hooks
	SupportsPreToolHooks bool
	// SupportsPostToolHooks indicates if the provider supports post-tool execution hooks
	SupportsPostToolHooks bool
	// SupportsSystemPromptCaching indicates if the provider supports system prompt caching
	SupportsSystemPromptCaching bool
	// SupportsToolCalling indicates if the provider supports tool/function calling
	SupportsToolCalling bool
	// SupportsBatch indicates if the provider supports batch processing
	SupportsBatch bool
	// SupportsImageInput indicates if the provider supports image inputs
	SupportsImageInput bool
	// MaxTokens is the maximum *output* tokens the model can generate in a single response.
	MaxTokens int
	// ContextWindowSize is the total context window in tokens (input + output combined).
	ContextWindowSize int
	// SupportsEmbedding indicates if the provider supports text embedding.
	SupportsEmbedding bool
	// DefaultEmbeddingModel is the default model name for embedding, if supported.
	DefaultEmbeddingModel string
}

// ToolDefinition describes a tool available to the LLM.
type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
	// Strict requests strict JSON-schema validation of tool inputs by the
	// underlying provider when supported. Set to a pointer to true to
	// opt in; nil (the default) is non-strict.
	Strict *bool `json:"strict,omitempty"`
}

// ToolUseBlock represents a tool_use content block from the LLM.
type ToolUseBlock struct {
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

// ContentBlock represents a content block in a multi-block message.
// NOTE: Input uses a pointer to distinguish "absent" from "empty object".
type ContentBlock struct {
	Type      string          `json:"type"`                  // text, tool_use, tool_result, thinking
	Text      string          `json:"text,omitempty"`        // text block; also thinking text for type="thinking"
	ID        string          `json:"id,omitempty"`          // tool_use block ID
	Name      string          `json:"name,omitempty"`        // tool_use tool name
	Input     *map[string]any `json:"input,omitempty"`       // tool_use input (always set for tool_use blocks)
	ToolUseID string          `json:"tool_use_id,omitempty"` // tool_result reference
	Content   string          `json:"content,omitempty"`     // tool_result text
	IsError   bool            `json:"is_error,omitempty"`    // tool_result error flag
	// Signature is the cryptographic signature attached to thinking blocks.
	Signature string `json:"signature,omitempty"`
}

// EventType identifies the kind of a StreamEvent.
type EventType string

const (
	// EventDelta carries an incremental text fragment in StreamEvent.Content.
	EventDelta EventType = "delta"
	// EventToolUse carries a tool invocation in StreamEvent.ToolUse.
	EventToolUse EventType = "tool_use"
	// EventUsage carries token-usage data in StreamEvent.Usage.
	EventUsage EventType = "usage"
	// EventError is a terminal failure event with the message in StreamEvent.Error.
	EventError EventType = "error"
	// EventDone is a terminal success event marking the end of a turn.
	EventDone EventType = "done"
	// EventSessionID carries an adapter-assigned session identifier.
	EventSessionID EventType = "session_id"
	// EventThinking carries a completed interleaved thinking block.
	EventThinking EventType = "thinking"
)

// ThinkingBlock carries the content and signed signature of one thinking block.
type ThinkingBlock struct {
	Thinking  string
	Signature string
}

// Delta phases for StreamEvent.Phase.
const (
	// PhaseNarration is streaming text the agent writes while it works.
	PhaseNarration = "narration"
	// PhaseFinal is the turn's result text.
	PhaseFinal = "final"
	// PhaseThinking is text from inside a thinking block.
	PhaseThinking = "thinking"
)

// StreamEvent represents a single event from a streaming provider response.
type StreamEvent struct {
	Type          EventType
	Content       string
	Usage         *Usage
	Error         string
	ToolUse       *ToolUseBlock
	SessionID     string
	ThinkingBlock *ThinkingBlock

	// BlockID identifies the content block an EventDelta or EventThinking
	// belongs to. Every delta of one block carries the same BlockID and the
	// next block a different one, so a consumer can separate consecutive
	// blocks without provider-specific rules. Its format is the adapter's
	// own and opaque to consumers; empty means the adapter cannot tell
	// blocks apart.
	BlockID string
	// Phase classifies an EventDelta as PhaseNarration, PhaseFinal or
	// PhaseThinking. Empty means the adapter did not classify it.
	Phase string
}

// IsTurnComplete reports whether ev is a terminal event marking the end of a turn.
func IsTurnComplete(ev StreamEvent) bool {
	return ev.Type == EventDone || ev.Type == EventError
}

// Usage contains token usage information.
type Usage struct {
	InputTokens         int
	OutputTokens        int
	CacheCreationTokens int
	CacheReadTokens     int
	// StopReason is why the model stopped, normalised with
	// NormalizeStopReason: one of the StopReason constants, or the
	// provider's own word when it maps to none of them. Empty means the
	// provider reported no reason.
	StopReason string

	// CostUSD is the provider-reported cost, in US dollars, of the work this
	// Usage covers. It is a per-event DELTA, never a running total: consumers
	// sum Usage events per run, so an adapter whose CLI reports a cumulative
	// figure (Claude's total_cost_usd, for one) must emit the difference since
	// the previous event. Zero means the provider reported no cost.
	CostUSD float64
}

// Normalised stop reasons for Usage.StopReason. The set is closed for the
// reasons every runtime shares; NormalizeStopReason maps each provider's
// spelling onto it and passes anything else through unchanged, so no
// information is lost. Each value maps one-to-one onto a go-chatstream
// finish reason (end_turn to stop, max_tokens to length, tool_use to
// tool_calls, and the rest by name).
const (
	// StopReasonEndTurn: the model finished its turn.
	StopReasonEndTurn = "end_turn"
	// StopReasonMaxTokens: the output was cut off at a token limit.
	StopReasonMaxTokens = "max_tokens"
	// StopReasonToolUse: the model stopped to call a tool.
	StopReasonToolUse = "tool_use"
	// StopReasonTurnLimit: the agent hit its limit on model requests per
	// turn.
	StopReasonTurnLimit = "turn_limit"
	// StopReasonRefusal: the model refused to continue.
	StopReasonRefusal = "refusal"
	// StopReasonCancelled: the turn was cancelled or interrupted.
	StopReasonCancelled = "cancelled" //nolint:misspell // the wire value, as ACP and go-chatstream spell it
	// StopReasonError: the turn ended in an error.
	StopReasonError = "error"
)

// stopReasonAliases maps provider spellings, lower-cased with '-' and ' '
// folded to '_', onto the shared vocabulary.
var stopReasonAliases = map[string]string{
	"end_turn":              StopReasonEndTurn,
	"stop":                  StopReasonEndTurn,
	"stop_sequence":         StopReasonEndTurn,
	"completed":             StopReasonEndTurn,
	"success":               StopReasonEndTurn,
	"max_tokens":            StopReasonMaxTokens,
	"length":                StopReasonMaxTokens,
	"max_output_tokens":     StopReasonMaxTokens,
	"max_completion_tokens": StopReasonMaxTokens,
	"tool_use":              StopReasonToolUse,
	"tool_calls":            StopReasonToolUse,
	"function_call":         StopReasonToolUse,
	"turn_limit":            StopReasonTurnLimit,
	"max_turn_requests":     StopReasonTurnLimit,
	"max_turns":             StopReasonTurnLimit,
	"refusal":               StopReasonRefusal,
	"cancelled":             StopReasonCancelled, //nolint:misspell // a provider spelling
	"canceled":              StopReasonCancelled,
	"interrupted":           StopReasonCancelled,
	"aborted":               StopReasonCancelled,
	"error":                 StopReasonError,
	"failed":                StopReasonError,
}

// NormalizeStopReason maps a provider's stop or finish reason onto the
// shared StopReason vocabulary: Anthropic's end_turn / max_tokens /
// tool_use, OpenAI's stop / length / tool_calls and max_output_tokens,
// OpenCode's step reasons (stop, length, tool-calls), ACP's stopReason
// (end_turn, max_tokens, max_turn_requests, refusal, cancelled), and their
// near spellings. A reason outside the vocabulary (Anthropic's pause_turn,
// for one) is returned as given, trimmed, so a consumer can still read it.
// The empty string stays empty.
func NormalizeStopReason(raw string) string {
	trimmed := strings.TrimSpace(raw)
	key := strings.NewReplacer("-", "_", " ", "_").Replace(strings.ToLower(trimmed))
	if norm, ok := stopReasonAliases[key]; ok {
		return norm
	}
	return trimmed
}

// CompleteResult contains the text and optional usage metadata for a
// non-streaming completion call.
type CompleteResult struct {
	Text  string
	Usage *Usage
}

// ChatMessage represents a single message in a conversation.
type ChatMessage struct {
	Role          string         `json:"role"`
	Content       string         `json:"content,omitempty"`
	ContentBlocks []ContentBlock `json:"content_blocks,omitempty"`
}

// SlotBlock is a pre-assembled region of the context window.
type SlotBlock struct {
	Name     string
	Content  string
	CacheKey string
	Changed  bool
}

// CacheHint tells a provider what content to cache.
// Position identifies the type of content ("system", "tools", "recent_message").
// Index provides ordering context — for "recent_message", 0 means the most recent
// user message, 1 means the second most recent, etc.
//
// Cache hints belong on ChatRequest.CacheHints so they travel with the call.
// The earlier pattern of setting hints on a shared provider singleton is
// unsafe under concurrent use — see CacheableProvider in go-llm-contracts.
type CacheHint struct {
	Position string // "system", "tools", "recent_message"
	Index    int    // ordering context (e.g. 0 = most recent, 1 = second most recent)
}

// ChatRequest is the unified input to provider chat methods.
type ChatRequest struct {
	Model        string
	SystemPrompt string
	SlotBlocks   []SlotBlock
	Messages     []ChatMessage
	Tools        []ToolDefinition
	MaxTokens    int
	// CacheHints tells the provider WHAT to cache for this call. Providers
	// that support prompt caching translate each hint into their own caching
	// primitive (e.g., Anthropic's cache_control markers). Threading hints
	// through the request — rather than mutating shared provider state —
	// keeps concurrent callers isolated.
	CacheHints []CacheHint
}

// EffectiveSystemPrompt returns SystemPrompt when no slots are set, otherwise
// returns SystemPrompt followed by each non-empty slot's content joined with
// "\n\n".
func (r ChatRequest) EffectiveSystemPrompt() string {
	if len(r.SlotBlocks) == 0 {
		return r.SystemPrompt
	}
	var b strings.Builder
	if r.SystemPrompt != "" {
		b.WriteString(r.SystemPrompt)
	}
	for _, s := range r.SlotBlocks {
		if s.Content == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(s.Content)
	}
	return b.String()
}
