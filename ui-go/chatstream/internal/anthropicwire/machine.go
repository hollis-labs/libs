package anthropicwire

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	chatstream "github.com/hollis-labs/go-chatstream"
	"github.com/hollis-labs/go-chatstream/internal/decodekit"
)

// DialectName is the Raw.Dialect of raw events the Messages API adapter emits.
const DialectName = "anthropic.messages"

// Config selects how a Machine treats the run around the messages it decodes.
type Config struct {
	// OwnsRun: message_start begins the run (run.start) and message_stop ends it
	// (run.finish). This is the Messages API stream. When false, the Machine
	// serves a host that owns the run (the Claude CLI, whose stream carries one
	// message per model call): message_start and message_stop become
	// step.start+message.start and message.end+step.finish, and error events are
	// left to the host.
	OwnsRun bool
	// EmitUsage emits a cumulative usage event for message_start and
	// message_delta usage. A host whose messages each restart the cumulative
	// counters turns it off, since cumulative usage may not go down.
	EmitUsage bool
	// Dialect is Raw.Dialect on raw events; empty means DialectName.
	Dialect string
}

// Machine is the block state machine of the Anthropic Messages stream: the
// six message events, block by block. It is not safe for concurrent use.
type Machine struct {
	b   *decodekit.Base
	cfg Config

	msgID     string
	blocks    map[int]*block
	usage     usageAcc
	stopRaw   string
	stopSeq   json.RawMessage
	stopExtra json.RawMessage
	streamed  map[string]bool
	calls     map[string]string // tool_use id -> part id of the tool_call part
}

// NewMachine returns a Machine that emits through b.
func NewMachine(b *decodekit.Base, cfg Config) *Machine {
	if cfg.Dialect == "" {
		cfg.Dialect = DialectName
	}
	return &Machine{b: b, cfg: cfg, blocks: map[int]*block{}, streamed: map[string]bool{}, calls: map[string]string{}}
}

// RegisterCall records that the tool call with the upstream id toolUseID is the
// tool_call part partID, so a later result can name it (chatstream.MetaCallID).
func (m *Machine) RegisterCall(toolUseID, partID string) {
	if toolUseID != "" {
		m.calls[toolUseID] = partID
	}
}

// CallPart returns the part id of the tool_call part for an upstream tool use id.
func (m *Machine) CallPart(toolUseID string) (string, bool) {
	p, ok := m.calls[toolUseID]
	return p, ok
}

// Streamed reports whether events for the message id have been decoded.
func (m *Machine) Streamed(id string) bool { return m.streamed[id] }

// MessageID is the id of the message being decoded.
func (m *Machine) MessageID() string { return m.msgID }

// StopReason is the raw stop_reason last seen in a message_delta.
func (m *Machine) StopReason() string { return m.stopRaw }

type block struct {
	index     int
	partID    string
	kind      chatstream.PartKind
	blockType string
	sig       string
	data      string
	redacted  bool
	args      strings.Builder
	text      strings.Builder // compaction deltas
	raw       json.RawMessage // the start block, for whole-block parts
	cites     int
}

// RawUnknown builds the raw event for an upstream event the mapping has no verb
// for. The payload is the frame's JSON when it is valid JSON, else it is kept
// as a JSON string.
func (m *Machine) RawUnknown(data []byte, typ string) chatstream.Event {
	return m.raw(typ, data)
}

func (m *Machine) raw(typ string, data []byte) chatstream.Event {
	ev := m.b.Event(chatstream.VerbRaw)
	payload := bytes.TrimSpace(data)
	if !json.Valid(payload) {
		payload, _ = json.Marshal(string(payload))
	}
	ev.Raw = &chatstream.Raw{Dialect: m.cfg.Dialect, Type: typ, Payload: append(json.RawMessage(nil), payload...)}
	return ev
}

func (m *Machine) emit(out []chatstream.Event, ev chatstream.Event) []chatstream.Event {
	return m.b.Emit(out, ev)
}

// Handle decodes one event of the Messages stream, appending events to out. typ
// is the SSE event name, or "" to use the JSON type field. handled is false when
// the event type is not one the Machine knows (the caller decides what to do);
// malformed JSON is handled here, as a raw event.
func (m *Machine) Handle(out []chatstream.Event, typ string, data []byte) ([]chatstream.Event, bool) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return m.emit(out, m.raw("malformed", data)), true
	}
	if head.Type != "" {
		typ = head.Type
	}
	switch typ {
	case "ping":
		return out, true
	case "message_start":
		return m.messageStart(out, data), true
	case "content_block_start":
		return m.blockStart(out, data), true
	case "content_block_delta":
		return m.blockDelta(out, data), true
	case "content_block_stop":
		return m.blockStop(out, data), true
	case "message_delta":
		return m.messageDelta(out, data), true
	case "message_stop":
		return m.messageStop(out), true
	case "error":
		if m.cfg.OwnsRun {
			return m.errorEvent(out, data), true
		}
		return out, false
	}
	return out, false
}

func (m *Machine) partID(index int) string {
	if m.msgID != "" {
		return fmt.Sprintf("%s:%d", m.msgID, index)
	}
	return fmt.Sprintf("b%d", index)
}

func (m *Machine) messageStart(out []chatstream.Event, data []byte) []chatstream.Event {
	var w struct {
		Message struct {
			ID    string          `json:"id"`
			Model string          `json:"model"`
			Role  string          `json:"role"`
			Usage json.RawMessage `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return m.emit(out, m.raw("malformed", data))
	}
	// A new message begins: whatever the previous one left open is closed first.
	out = m.closeBlocks(out)
	m.msgID = w.Message.ID
	m.streamed[w.Message.ID] = true
	m.usage = usageAcc{}
	m.stopRaw, m.stopSeq, m.stopExtra = "", nil, nil
	if m.cfg.OwnsRun {
		m.b.SetRunID(w.Message.ID)
		if !m.b.Started() {
			ev := m.b.Event(chatstream.VerbRunStart)
			ev.Provider, ev.Model = m.b.Options().Provider, m.b.Options().Model
			if ev.Provider == "" {
				ev.Provider = "anthropic"
			}
			if w.Message.Model != "" {
				ev.Model = w.Message.Model
			}
			out = m.emit(out, ev)
		}
	} else {
		st := m.b.Event(chatstream.VerbStepStart)
		st.StepID = w.Message.ID
		out = m.emit(out, st)
	}
	ms := m.b.Event(chatstream.VerbMessageStart)
	ms.MessageID, ms.Role = w.Message.ID, w.Message.Role
	if ms.Role == "" {
		ms.Role = "assistant"
	}
	out = m.emit(out, ms)
	if len(w.Message.Usage) > 0 {
		m.usage.merge(w.Message.Usage)
		out = m.emitUsage(out)
	}
	return out
}

func (m *Machine) emitUsage(out []chatstream.Event) []chatstream.Event {
	if !m.cfg.EmitUsage {
		return out
	}
	u, ok := m.usage.build(chatstream.UsageCumulative)
	if !ok {
		return out
	}
	ev := m.b.Event(chatstream.VerbUsage)
	ev.Usage = &u
	return m.emit(out, ev)
}

func (m *Machine) blockStart(out []chatstream.Event, data []byte) []chatstream.Event {
	var w struct {
		Index int             `json:"index"`
		Block json.RawMessage `json:"content_block"`
	}
	if err := json.Unmarshal(data, &w); err != nil || len(w.Block) == 0 {
		return m.emit(out, m.raw("malformed", data))
	}
	var cb struct {
		Type      string          `json:"type"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Text      string          `json:"text"`
		Thinking  string          `json:"thinking"`
		Signature string          `json:"signature"`
		Data      string          `json:"data"`
		ToolUseID string          `json:"tool_use_id"`
		Content   json.RawMessage `json:"content"`
		IsError   *bool           `json:"is_error"`
	}
	if err := json.Unmarshal(w.Block, &cb); err != nil {
		return m.emit(out, m.raw("malformed", data))
	}
	if old, open := m.blocks[w.Index]; open {
		out = m.closeBlock(out, old) // a start for an open index replaces it
	}
	bl := &block{index: w.Index, partID: m.partID(w.Index), blockType: cb.Type, raw: append(json.RawMessage(nil), w.Block...)}
	meta := map[string]json.RawMessage{}
	switch {
	case cb.Type == "text":
		bl.kind = chatstream.PartText
	case cb.Type == "thinking":
		bl.kind = chatstream.PartReasoning
		bl.sig = cb.Signature
	case cb.Type == "redacted_thinking":
		bl.kind = chatstream.PartReasoning
		bl.redacted, bl.data = true, cb.Data
		meta["redacted"] = json.RawMessage("true")
	case cb.Type == "tool_use" || cb.Type == "server_tool_use" || cb.Type == "mcp_tool_use":
		bl.kind = chatstream.PartToolCall
		meta[chatstream.MetaName], meta["id"] = jsonString(cb.Name), jsonString(cb.ID)
		if cb.Type != "tool_use" {
			meta["server"] = json.RawMessage("true")
			meta["block_type"] = jsonString(cb.Type)
		}
		m.RegisterCall(cb.ID, bl.partID)
	case strings.HasSuffix(cb.Type, "tool_result"):
		meta["block_type"] = jsonString(cb.Type)
		if cb.ToolUseID != "" {
			meta["tool_use_id"] = jsonString(cb.ToolUseID)
		}
		if call, known := m.CallPart(cb.ToolUseID); known {
			// a result names the tool_call part it answers
			bl.kind = chatstream.PartToolResult
			meta[chatstream.MetaCallID] = jsonString(call)
			if cb.Type != "tool_result" {
				meta["server"] = json.RawMessage("true")
			}
			if ResultIsError(cb.IsError, cb.Content) {
				meta[chatstream.MetaIsError] = json.RawMessage("true")
			}
		} else {
			// a result for a call this stream never showed cannot name it: keep the
			// block as a data part rather than a tool_result with a dangling call_id
			bl.kind = chatstream.PartData
		}
	default:
		bl.kind = chatstream.PartData
		meta["block_type"] = jsonString(cb.Type)
	}
	m.blocks[w.Index] = bl
	ev := m.b.Event(chatstream.VerbPartStart)
	ev.PartID, ev.Kind = bl.partID, string(bl.kind)
	if len(meta) > 0 {
		ev.Meta = meta
	}
	out = m.emit(out, ev)
	// text and thinking blocks may start with content already in them
	if bl.kind == chatstream.PartText && cb.Text != "" {
		out = m.textDelta(out, bl, cb.Text)
	}
	if cb.Type == "thinking" && cb.Thinking != "" {
		out = m.textDelta(out, bl, cb.Thinking)
	}
	return out
}

func (m *Machine) textDelta(out []chatstream.Event, bl *block, text string) []chatstream.Event {
	ev := m.b.Event(chatstream.VerbPartDelta)
	ev.PartID, ev.Text = bl.partID, text
	return m.emit(out, ev)
}

func (m *Machine) blockDelta(out []chatstream.Event, data []byte) []chatstream.Event {
	var w struct {
		Index int             `json:"index"`
		Delta json.RawMessage `json:"delta"`
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return m.emit(out, m.raw("malformed", data))
	}
	var d struct {
		Type        string          `json:"type"`
		Text        string          `json:"text"`
		PartialJSON string          `json:"partial_json"`
		Thinking    string          `json:"thinking"`
		Signature   string          `json:"signature"`
		Content     string          `json:"content"`
		Citation    json.RawMessage `json:"citation"`
	}
	if err := json.Unmarshal(w.Delta, &d); err != nil {
		return m.emit(out, m.raw("malformed", data))
	}
	bl, open := m.blocks[w.Index]
	if !open {
		return m.emit(out, m.raw("content_block_delta", data))
	}
	switch d.Type {
	case "text_delta":
		if bl.kind == chatstream.PartText {
			return m.textDelta(out, bl, d.Text)
		}
	case "thinking_delta":
		if bl.kind == chatstream.PartReasoning && !bl.redacted {
			return m.textDelta(out, bl, d.Thinking)
		}
	case "signature_delta":
		if bl.kind == chatstream.PartReasoning && !bl.redacted {
			bl.sig = d.Signature
			return out
		}
	case "input_json_delta":
		if bl.kind == chatstream.PartToolCall {
			bl.args.WriteString(d.PartialJSON)
			if d.PartialJSON == "" {
				return out
			}
			ev := m.b.Event(chatstream.VerbPartDelta)
			ev.PartID, ev.JSONFragment = bl.partID, d.PartialJSON
			return m.emit(out, ev)
		}
	case "citations_delta":
		if bl.kind == chatstream.PartText && len(d.Citation) > 0 {
			bl.cites++
			id := fmt.Sprintf("%s:c%d", bl.partID, bl.cites)
			st := m.b.Event(chatstream.VerbPartStart)
			st.PartID, st.Kind = id, string(chatstream.PartSource)
			out = m.emit(out, st)
			en := m.b.Event(chatstream.VerbPartEnd)
			en.PartID, en.Final = id, append(json.RawMessage(nil), d.Citation...)
			return m.emit(out, en)
		}
	case "compaction_delta":
		if bl.kind == chatstream.PartData {
			bl.text.WriteString(d.Content)
			if d.Content == "" {
				return out
			}
			return m.textDelta(out, bl, d.Content)
		}
	}
	return m.emit(out, m.raw("content_block_delta", data))
}

func (m *Machine) blockStop(out []chatstream.Event, data []byte) []chatstream.Event {
	var w struct {
		Index int `json:"index"`
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return m.emit(out, m.raw("malformed", data))
	}
	bl, open := m.blocks[w.Index]
	if !open {
		return m.emit(out, m.raw("content_block_stop", data))
	}
	return m.closeBlock(out, bl)
}

// closeBlock ends a block with the final value it accumulated.
func (m *Machine) closeBlock(out []chatstream.Event, bl *block) []chatstream.Event {
	delete(m.blocks, bl.index)
	ev := m.b.Event(chatstream.VerbPartEnd)
	ev.PartID = bl.partID
	switch bl.kind { //nolint:exhaustive // text, source, file and refusal parts end without a final value
	case chatstream.PartReasoning:
		switch {
		case bl.redacted:
			ev.Final = mustJSON(map[string]string{"data": bl.data})
		case bl.sig != "":
			ev.Final = mustJSON(map[string]string{"signature": bl.sig})
		}
	case chatstream.PartToolCall:
		s := strings.TrimSpace(bl.args.String())
		switch {
		case s == "":
			ev.Final = json.RawMessage("{}")
		case json.Valid([]byte(s)):
			var buf bytes.Buffer
			_ = json.Compact(&buf, []byte(s))
			ev.Final = buf.Bytes()
		default:
			ev.Ext = map[string]json.RawMessage{"anthropic": json.RawMessage(`{"args_invalid":true}`)}
		}
	case chatstream.PartToolResult, chatstream.PartData:
		ev.Final = bl.raw
	}
	return m.emit(out, ev)
}

// CloseBlocks closes every open block with the final value it accumulated,
// lowest index first. A host calls it before ending a run that stopped in the
// middle of a message.
func (m *Machine) CloseBlocks(out []chatstream.Event) []chatstream.Event { return m.closeBlocks(out) }

// closeBlocks closes every open block, lowest index first.
func (m *Machine) closeBlocks(out []chatstream.Event) []chatstream.Event {
	idx := make([]int, 0, len(m.blocks))
	for i := range m.blocks {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	for _, i := range idx {
		if bl, ok := m.blocks[i]; ok {
			out = m.closeBlock(out, bl)
		}
	}
	return out
}

func (m *Machine) messageDelta(out []chatstream.Event, data []byte) []chatstream.Event {
	var w struct {
		Delta struct {
			StopReason   *string         `json:"stop_reason"`
			StopSequence json.RawMessage `json:"stop_sequence"`
			StopDetails  json.RawMessage `json:"stop_details"`
		} `json:"delta"`
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return m.emit(out, m.raw("malformed", data))
	}
	if w.Delta.StopReason != nil {
		m.stopRaw = *w.Delta.StopReason
	}
	if isPresent(w.Delta.StopSequence) {
		m.stopSeq = w.Delta.StopSequence
	}
	if isPresent(w.Delta.StopDetails) {
		m.stopExtra = w.Delta.StopDetails
	}
	if len(w.Usage) > 0 {
		m.usage.merge(w.Usage)
		out = m.emitUsage(out)
	}
	return out
}

func (m *Machine) messageStop(out []chatstream.Event) []chatstream.Event {
	out = m.closeBlocks(out)
	if !m.cfg.OwnsRun {
		out = m.unwindMessage(out)
		return out
	}
	out = m.b.Unwind(out)
	fin := m.b.Event(chatstream.VerbRunFinish)
	fin.Reason, fin.RawReason = string(FinishReason(m.stopRaw)), m.stopRaw
	if u, ok := m.usage.build(chatstream.UsageFinal); ok {
		fin.Usage = &u
	}
	ext := map[string]json.RawMessage{}
	if m.stopSeq != nil {
		ext["stop_sequence"] = m.stopSeq
	}
	if m.stopExtra != nil {
		ext["stop_details"] = m.stopExtra
	}
	if len(ext) > 0 {
		fin.Ext = map[string]json.RawMessage{"anthropic": mustJSON(ext)}
	}
	return m.emit(out, fin)
}

// unwindMessage ends the current message (and its step) without ending the run.
func (m *Machine) unwindMessage(out []chatstream.Event) []chatstream.Event {
	if m.msgID == "" {
		return out
	}
	me := m.b.Event(chatstream.VerbMessageEnd)
	me.MessageID = m.msgID
	out = m.emit(out, me)
	sf := m.b.Event(chatstream.VerbStepFinish)
	sf.StepID = m.msgID
	return m.emit(out, sf)
}

func (m *Machine) errorEvent(out []chatstream.Event, data []byte) []chatstream.Event {
	var w struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return m.emit(out, m.raw("malformed", data))
	}
	out = m.closeBlocks(out)
	out = m.b.Unwind(out)
	ev := m.b.Event(chatstream.VerbRunError)
	ev.Code, ev.Message = w.Error.Type, w.Error.Message
	if ev.Code == "" {
		ev.Code = chatstream.CodeUpstreamError
	}
	switch w.Error.Type {
	case "overloaded_error", "api_error", "timeout_error", "rate_limit_error":
		ev.Retryable = true
	}
	ev.Ext = map[string]json.RawMessage{"anthropic": bytes.TrimSpace(data)}
	return m.emit(out, ev)
}

// FinishReason maps Anthropic's stop_reason to the closed vocabulary.
func FinishReason(stop string) chatstream.FinishReason {
	switch stop {
	case "end_turn", "stop_sequence":
		return chatstream.FinishStop
	case "max_tokens":
		return chatstream.FinishLength
	case "tool_use":
		return chatstream.FinishToolCalls
	case "pause_turn":
		return chatstream.FinishPause
	case "refusal":
		return chatstream.FinishRefusal
	case "model_context_window_exceeded":
		return chatstream.FinishContextExceeded
	}
	return chatstream.FinishOther
}

func isPresent(r json.RawMessage) bool {
	s := strings.TrimSpace(string(r))
	return s != "" && s != "null"
}

func jsonString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// ResultIsError reports whether a tool result block failed: an explicit
// is_error true, or, for the server tools' structured results, a content object
// whose type ends in "_error" (web_search_tool_result_error and the like).
func ResultIsError(isError *bool, content json.RawMessage) bool {
	if isError != nil && *isError {
		return true
	}
	var c struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(content, &c) == nil && strings.HasSuffix(c.Type, "_error")
}
